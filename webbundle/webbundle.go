package webbundle

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"

	"github.com/cplieger/go/httpserver/v2"
)

// Config is one app's bundle: where its sources are, where the tree goes,
// and what it holds.
type Config struct {
	Define         map[string]string // esbuild defines, each value JSON
	Manifest       *Manifest         // the web app manifest; nil writes none
	IconPNGs       map[int]string    // committed PNGs under Src by size (32, 180, 192, 512), copied instead of rendered; nil renders
	Src            string            // the static-src root, holding package.json and node_modules
	Out            string            // the output tree the UI package embeds, replaced whole
	PagesFile      string            // the TypeScript page table under Src; "" is "pages.gen.ts"
	APIPrefix      string            // the API's path prefix ("/api"), as httpserver.SPAConfig.APIPrefix
	PageCorpusFile string            // the page corpus under Src; "" is "pages.corpus.gen.json"
	Icon           string            // the app's icon, an SVG under Src; "" is none
	Shells         []string          // the shells under Src; nil is ["index.html"]
	Entries        []Entry
	Pages          []httpserver.Page // the UI package's pages
	Packages       []string          // npm packages a bundle may include; nil is ["@cplieger/*"]
}

// Manifest is the web app manifest Build writes beside the icons.
type Manifest struct {
	Name, ShortName string
	ThemeColor      string // #rrggbb
	BackgroundColor string // #rrggbb
}

// EntryKind is how an entry is built.
type EntryKind string

// The entry kinds, as bundle.json records them.
const (
	Module  = EntryKind(httpserver.BundleModule)  // ESM, with shared chunks under assets/chunks/
	Classic = EntryKind(httpserver.BundleClassic) // an IIFE with its own closure: the prepaint, a worker host
	Style   = EntryKind(httpserver.BundleStyle)   // CSS, its url() files written as hashed assets
)

// Entry is one bundle entry point.
type Entry struct {
	Name   string // the logical name: "/<Name>.js" or "/<Name>.css" in a shell
	Path   string // the source file under Src
	Kind   EntryKind
	Forbid []string // packages or Src paths its import closure may not reach
}

// Result is what a build wrote.
type Result struct {
	Files   []string // every written path under Out, sorted
	Notices []string // every bundled npm package, sorted
}

// Build builds c's tree into c.Out: the classic entries first, then the
// module entries (which read the classic entries' hashed URLs through the
// BUNDLE_URLS define), then the style entries, the shells with their logical
// paths replaced, and bundle.json. The tree is built beside Out and swapped
// in whole, so a refused or failed build leaves Out as it was; Out/.gitignore
// is carried through byte for byte. Two builds of the same inputs write the
// same bytes.
func Build(c Config) (Result, error) { //nolint:gocritic // hugeParam: read once per build, and a value keeps the caller's config unshared
	b, err := newBuild(&c)
	if err != nil {
		return Result{}, err
	}
	if stale := b.checkPageFiles(); stale != nil {
		return Result{}, stale
	}
	shells, named, err := b.readShells()
	if err != nil {
		return Result{}, err
	}
	t, err := newTree(b.out)
	if err != nil {
		return Result{}, err
	}
	r, err := b.write(t, shells, named)
	if err != nil {
		return Result{}, errors.Join(err, t.discard())
	}
	if err := t.swap(); err != nil {
		return Result{}, err
	}
	return r, nil
}

// readShells reads every shell and refuses a module or style entry none
// names; a classic entry may instead be read through BUNDLE_URLS.
func (b *build) readShells() ([]shell, []bool, error) {
	shells := make([]shell, 0, len(b.shells))
	named := make([]bool, len(b.entries))
	for _, name := range b.shells {
		sh, err := b.readShell(name)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range sh.links {
			named[l.entry] = true
		}
		shells = append(shells, sh)
	}
	for i, e := range b.entries {
		if !named[i] && e.Kind != Classic {
			return nil, nil, fmt.Errorf("webbundle: no shell names entry %q (%s)", e.Name, logicalPath(e))
		}
	}
	return shells, named, nil
}

// entriesBuilt is what building every entry into the tree gave.
type entriesBuilt struct {
	pkgs     map[string]bool // every bundled npm package
	urls     map[string]string
	hashed   []string // each entry's hashed path, by index
	moduleJS []byte   // every module output's bytes, chunks included
}

// write builds every entry into t and writes the shells and bundle.json.
func (b *build) write(t *tree, shells []shell, named []bool) (Result, error) {
	if err := t.keepGitignore(); err != nil {
		return Result{}, err
	}
	eb, err := b.buildEntries(t.dir)
	if err != nil {
		return Result{}, err
	}
	head, err := b.writeIcons(t)
	if err != nil {
		return Result{}, err
	}
	for i, e := range b.entries {
		if e.Kind == Classic && !named[i] && !bytes.Contains(eb.moduleJS, []byte(`"`+eb.urls[e.Name]+`"`)) {
			return Result{}, fmt.Errorf("webbundle: classic entry %q is named by no shell and read through BUNDLE_URLS by no module entry", e.Name)
		}
	}
	for _, sh := range shells {
		if werr := t.write(sh.name, sh.render(eb.hashed, head), 0o644); werr != nil {
			return Result{}, werr
		}
	}
	entries := make([]httpserver.BundleEntry, len(b.entries))
	for i, e := range b.entries {
		entries[i] = httpserver.BundleEntry{Name: e.Name, Kind: httpserver.BundleKind(e.Kind), Path: eb.hashed[i]}
	}
	files, err := t.manifest(entries)
	if err != nil {
		return Result{}, err
	}
	return Result{Files: files, Notices: slices.Sorted(maps.Keys(eb.pkgs))}, nil
}

// buildEntries builds the classic entries, then the module entries, then the
// style entries into outdir.
func (b *build) buildEntries(outdir string) (*entriesBuilt, error) {
	eb := &entriesBuilt{pkgs: map[string]bool{}, urls: map[string]string{}, hashed: make([]string, len(b.entries))}
	if err := b.buildClassicEntries(eb, outdir); err != nil {
		return nil, err
	}
	if err := b.buildModuleEntries(eb, outdir); err != nil {
		return nil, err
	}
	for i, e := range b.entries {
		if e.Kind != Style {
			continue
		}
		r, err := buildStyle(b.src, e, outdir, true)
		if err != nil {
			return nil, err
		}
		if err := b.take(eb, r, i); err != nil {
			return nil, err
		}
	}
	return eb, nil
}

// take checks entry i's closure in r and records its packages and hashed
// path.
func (b *build) take(eb *entriesBuilt, r built, i int) error {
	e := b.entries[i]
	pkgs, err := b.checkClosure(&r.meta, e)
	if err != nil {
		return err
	}
	maps.Copy(eb.pkgs, pkgs)
	if eb.hashed[i] = r.entries[e.Path]; eb.hashed[i] == "" {
		return fmt.Errorf("webbundle: entry %q built no output", e.Name)
	}
	return nil
}

func (b *build) buildClassicEntries(eb *entriesBuilt, outdir string) error {
	for i, e := range b.entries {
		if e.Kind != Classic {
			continue
		}
		r, err := b.buildClassic(e, outdir)
		if err != nil {
			return err
		}
		if err := b.take(eb, r, i); err != nil {
			return err
		}
		eb.urls[e.Name] = "/" + eb.hashed[i]
	}
	return nil
}

func (b *build) buildModuleEntries(eb *entriesBuilt, outdir string) error {
	var modules []int
	for i, e := range b.entries {
		if e.Kind == Module {
			modules = append(modules, i)
		}
	}
	if len(modules) == 0 {
		return nil
	}
	es := make([]Entry, len(modules))
	for j, i := range modules {
		es[j] = b.entries[i]
	}
	r, err := b.buildModules(es, eb.urls, outdir)
	if err != nil {
		return err
	}
	for _, i := range modules {
		if err := b.take(eb, r, i); err != nil {
			return err
		}
	}
	for _, o := range r.outputs {
		if path.Ext(o.path) == jsExt {
			eb.moduleJS = append(eb.moduleJS, o.contents...)
		}
	}
	return nil
}
