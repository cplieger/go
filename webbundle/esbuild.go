package webbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/evanw/esbuild/pkg/api"
)

// Engine is a browser engine and the oldest version of it a bundle runs on.
type Engine struct {
	Name    string // "chrome", "firefox" or "safari"
	Version string // the engine's version, as "157" or "27"
}

// supportFloor is the fleet's browser support floor, bumped with esbuild.
// esbuild's esnext target would ship syntax one of them lacks unchanged.
var supportFloor = []Engine{
	{Name: "chrome", Version: "157"},
	{Name: "firefox", Version: "160"},
	{Name: "safari", Version: "27"},
}

// Engines returns the browser engines and versions Build and BuildStyle
// target: syntax one of them lacks is lowered, or the build fails naming the
// feature and the engine. A test that needs the support floor reads it here.
func Engines() []Engine { return slices.Clone(supportFloor) }

var esbuildEngine = map[string]api.EngineName{
	"chrome":  api.EngineChrome,
	"firefox": api.EngineFirefox,
	"safari":  api.EngineSafari,
}

func engines() []api.Engine {
	out := make([]api.Engine, len(supportFloor))
	for i, e := range supportFloor {
		out[i] = api.Engine{Name: esbuildEngine[e.Name], Version: e.Version}
	}
	return out
}

// fileLoaders sets esbuild's file loader, which esbuild configures for no
// font or image type, on every font and image extension httpserver serves,
// so a url() or an import of one is emitted as a hashed asset; no dataurl
// loader is set, so none is inlined. Any other extension has no loader and
// fails the build naming the file.
func fileLoaders() map[string]api.Loader {
	loaders := map[string]api.Loader{}
	for ext, ct := range httpserver.ContentTypes() {
		if strings.HasPrefix(ct, "font/") || strings.HasPrefix(ct, "image/") {
			loaders[ext] = api.LoaderFile
		}
	}
	return loaders
}

// options is what every entry's build shares. outdir is where esbuild
// places the outputs; every path written under it is relative, so the bytes
// do not depend on it.
func options(src, outdir string, write bool) api.BuildOptions {
	return api.BuildOptions{
		AbsWorkingDir: src,
		Outdir:        outdir,
		Write:         write,
		Bundle:        true,
		Metafile:      true,
		EntryNames:    "[dir]/[name]-[hash]",
		ChunkNames:    "assets/chunks/[name]-[hash]",
		AssetNames:    "assets/[name]-[hash]",
		// Without a public path a script's import of an asset is a URL
		// relative to the tree root, which a page at /a/b resolves under /a/.
		PublicPath:        "/",
		Loader:            fileLoaders(),
		Engines:           engines(),
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		// A sourcemap would publish the sources to anyone who reaches the
		// port, and every built file is served.
		Sourcemap: api.SourceMapNone,
		Charset:   api.CharsetUTF8,
		LogLevel:  api.LogLevelSilent,
	}
}

func entryPoint(e Entry) api.EntryPoint {
	return api.EntryPoint{InputPath: "./" + e.Path, OutputPath: "assets/" + e.Name}
}

// output is one file a build wrote, by its slash path in the tree.
type output struct {
	path     string
	contents []byte
}

// built is one esbuild run's result.
type built struct {
	meta    metafile
	entries map[string]string // the hashed path of each entry, by its Src path
	outputs []output
}

// runBuild runs opts; style says whether its entries are stylesheets, since
// a script build that emits CSS (a script importing a stylesheet) would ship
// a file no shell links.
func runBuild(opts *api.BuildOptions, what string, style bool) (built, error) {
	r := api.Build(*opts)
	if len(r.Errors) > 0 {
		return built{}, fmt.Errorf("webbundle: %s: %s", what, messages(r.Errors))
	}
	var b built
	if err := json.Unmarshal([]byte(r.Metafile), &b.meta); err != nil {
		return built{}, fmt.Errorf("webbundle: %s: esbuild's metafile: %w", what, err)
	}
	for _, f := range r.OutputFiles {
		rel, err := filepath.Rel(opts.Outdir, f.Path)
		if err != nil || !filepath.IsLocal(rel) {
			return built{}, fmt.Errorf("webbundle: %s: esbuild wrote %s, outside the tree", what, f.Path)
		}
		rel = filepath.ToSlash(rel)
		if !style && path.Ext(rel) == cssExt {
			return built{}, fmt.Errorf("webbundle: %s imports CSS, emitted as %s; a Style entry builds a stylesheet", what, rel)
		}
		b.outputs = append(b.outputs, output{path: rel, contents: f.Contents})
	}
	var err error
	if b.entries, err = entryOutputs(&b.meta, opts, style); err != nil {
		return built{}, fmt.Errorf("webbundle: %s: %w", what, err)
	}
	return b, nil
}

// entryOutputs maps each entry's Src path to its main output's slash path
// in the tree: its script, or its stylesheet for a style build.
func entryOutputs(m *metafile, opts *api.BuildOptions, style bool) (map[string]string, error) {
	want := jsExt
	if style {
		want = cssExt
	}
	entries := map[string]string{}
	for out, o := range m.Outputs {
		if o.EntryPoint == "" || path.Ext(out) != want {
			continue
		}
		rel, err := filepath.Rel(opts.Outdir, filepath.Join(opts.AbsWorkingDir, filepath.FromSlash(out)))
		if err != nil {
			return nil, err
		}
		entries[path.Clean(o.EntryPoint)] = filepath.ToSlash(rel)
	}
	return entries, nil
}

func messages(msgs []api.Message) string {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if l := m.Location; l != nil {
			lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", l.File, l.Line, l.Column+1, m.Text))
			continue
		}
		lines = append(lines, m.Text)
	}
	return strings.Join(lines, "; ")
}

func defines(user, urls map[string]string) (map[string]string, error) {
	raw, err := json.Marshal(urls)
	if err != nil {
		return nil, err
	}
	d := make(map[string]string, len(user)+1)
	maps.Copy(d, user)
	d[bundleURLs] = string(raw)
	return d, nil
}

// buildClassic builds one classic entry as an IIFE with its whole closure.
func (b *build) buildClassic(e Entry, outdir string) (built, error) {
	opts := options(b.src, outdir, true)
	opts.EntryPointsAdvanced = []api.EntryPoint{entryPoint(e)}
	opts.Format = api.FormatIIFE
	opts.Define = b.define
	return runBuild(&opts, "classic entry "+e.Name, false)
}

// buildModules builds every module entry in one ESM run, so code two of them
// import lands once in a shared chunk; urls is each classic entry's served
// path by name, read through BUNDLE_URLS.
func (b *build) buildModules(modules []Entry, urls map[string]string, outdir string) (built, error) {
	opts := options(b.src, outdir, true)
	for _, e := range modules {
		opts.EntryPointsAdvanced = append(opts.EntryPointsAdvanced, entryPoint(e))
	}
	opts.Format = api.FormatESModule
	opts.Splitting = true
	d, err := defines(b.define, urls)
	if err != nil {
		return built{}, err
	}
	opts.Define = d
	return runBuild(&opts, "module entries", false)
}

// buildStyle builds one stylesheet, its url() files emitted as hashed
// assets. BuildStyle runs the same options with Write off.
func buildStyle(src string, e Entry, outdir string, write bool) (built, error) {
	opts := options(src, outdir, write)
	opts.EntryPointsAdvanced = []api.EntryPoint{entryPoint(e)}
	return runBuild(&opts, "style entry "+e.Path, true)
}

// Stylesheet names one CSS file for [BuildStyle].
type Stylesheet struct {
	Src  string // the static-src root, as Config.Src
	Path string // the CSS file, a slash path under Src
}

// BuildStyle returns s.Path bundled exactly as a Style entry is (the pinned
// engines, the file loaders, the assets/<name>-<hash> names, minified), and
// writes nothing. A test passes it where it needs one stylesheet's built text
// without a whole Build.
func BuildStyle(s Stylesheet) ([]byte, error) {
	if s.Src == "" {
		return nil, errors.New("webbundle: BuildStyle: Src is empty")
	}
	abs, err := filepath.Abs(s.Src)
	if err != nil {
		return nil, fmt.Errorf("webbundle: BuildStyle: %w", err)
	}
	b := &build{src: abs}
	p, err := b.srcFile("stylesheet", s.Path)
	if err != nil {
		return nil, err
	}
	if path.Ext(p) != cssExt {
		return nil, fmt.Errorf("webbundle: stylesheet %s is not a .css file", s.Path)
	}
	e := Entry{Name: strings.TrimSuffix(path.Base(p), cssExt), Path: p, Kind: Style}
	r, err := buildStyle(abs, e, filepath.Join(abs, ".webbundle-style"), false)
	if err != nil {
		return nil, err
	}
	css, ok := r.entries[p]
	if !ok {
		return nil, fmt.Errorf("webbundle: stylesheet %s built no CSS", s.Path)
	}
	i := slices.IndexFunc(r.outputs, func(o output) bool { return o.path == css })
	return r.outputs[i].contents, nil
}
