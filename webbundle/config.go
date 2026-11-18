package webbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
)

const (
	maxEntries = 32
	maxOutput  = 64 << 20

	// bundleURLs is the define every module entry reads its classic entries'
	// hashed URLs from.
	bundleURLs = "BUNDLE_URLS"

	jsExt  = ".js"
	cssExt = ".css"

	defaultPagesFile  = "pages.gen.ts"
	defaultCorpusFile = "pages.corpus.gen.json"
)

var defaultPackages = []string{"@cplieger/*"}

// build is a checked Config: absolute Src and Out, defaults filled in, and
// each entry's path in the slash form esbuild's metafile names it by.
type build struct {
	define     map[string]string
	icon       *iconArt     // nil is no icon
	maskable   *iconArt     // nil is no maskable icon
	manifest   *appManifest // nil writes none
	src        string
	out        string
	apiPrefix  string
	pagesFile  string
	corpusFile string
	shells     []string
	entries    []Entry
	rules      []closureRules // each entry's Forbid and Allow, by index
	pages      []httpserver.Page
	packages   []string
}

func newBuild(c *Config) (*build, error) {
	if c.Src == "" {
		return nil, errors.New("webbundle: Config.Src is empty")
	}
	if c.Out == "" {
		return nil, errors.New("webbundle: Config.Out is empty")
	}
	src, err := srcDir("Config.Src", c.Src)
	if err != nil {
		return nil, err
	}
	out, err := filepath.Abs(c.Out)
	if err != nil {
		return nil, fmt.Errorf("webbundle: Config.Out: %w", err)
	}
	if info, lerr := os.Lstat(out); lerr == nil && !info.IsDir() {
		return nil, fmt.Errorf("webbundle: Config.Out %q is not a directory", c.Out)
	}
	if out, err = resolveExisting(out); err != nil {
		return nil, fmt.Errorf("webbundle: Config.Out: %w", err)
	}
	if within(src, out) || within(out, src) {
		return nil, fmt.Errorf("webbundle: Config.Out %q and Config.Src %q overlap; Out is replaced whole, so it sits beside Src", c.Out, c.Src)
	}
	b := &build{
		src:        src,
		out:        out,
		apiPrefix:  c.APIPrefix,
		pages:      slices.Clone(c.Pages),
		shells:     orDefault(c.Shells, []string{httpserver.DefaultShell}),
		packages:   orDefault(c.Packages, defaultPackages),
		pagesFile:  cmpOr(c.PagesFile, defaultPagesFile),
		corpusFile: cmpOr(c.PageCorpusFile, defaultCorpusFile),
		define:     maps.Clone(c.Define),
	}
	for _, check := range []func() error{
		func() error { return b.checkEntries(c.Entries) },
		func() error { return checkDefine(c.Define) },
		func() error { return b.checkShells(c.Pages) },
		b.checkFiles,
		b.checkClosureItems,
		func() error { return b.checkIcons(c) },
	} {
		if err := check(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// srcDir returns Src absolute with every symlink resolved: esbuild names
// each input by its real path relative to its working directory, so a
// symlink in Src's own path would put every input outside it.
func srcDir(field, name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("webbundle: %s: %w", field, err)
	}
	if info, serr := os.Stat(abs); serr != nil || !info.IsDir() {
		return "", fmt.Errorf("webbundle: %s %q is not a directory", field, name)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("webbundle: %s: %w", field, err)
	}
	return resolved, nil
}

// resolveExisting resolves the symlinks of p's longest existing prefix and
// joins the rest back, so a p that does not exist yet still compares with
// resolved paths.
func resolveExisting(p string) (string, error) {
	for d := p; ; d = filepath.Dir(d) {
		resolved, err := filepath.EvalSymlinks(d)
		switch {
		case err == nil:
			rest, rerr := filepath.Rel(d, p)
			return filepath.Join(resolved, rest), rerr
		case !errors.Is(err, fs.ErrNotExist):
			return "", err
		case filepath.Dir(d) == d:
			return p, nil
		}
	}
}

// within reports whether p is dir or inside it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func orDefault(v, def []string) []string {
	if v == nil {
		return slices.Clone(def)
	}
	return slices.Clone(v)
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (b *build) checkEntries(entries []Entry) error {
	if len(entries) > maxEntries {
		return fmt.Errorf("webbundle: %d entries, more than %d", len(entries), maxEntries)
	}
	logical := map[string]bool{}
	for _, e := range entries {
		p, err := b.srcFile("entry", e.Path)
		if err != nil {
			return err
		}
		if err := checkEntryName(&e); err != nil {
			return err
		}
		if err := checkEntryKind(&e, p); err != nil {
			return err
		}
		l := logicalPath(&e)
		if logical[l] {
			return fmt.Errorf("webbundle: two entries are %s", l)
		}
		logical[l] = true
		e.Path, e.Forbid, e.Allow = p, slices.Clone(e.Forbid), slices.Clone(e.Allow)
		b.entries = append(b.entries, e)
	}
	return nil
}

// checkEntryName keeps a logical name one plain file name: a shell names it
// as "/<Name>.js" or "/<Name>.css", and its hashed file is
// assets/<Name>-<hash>.<ext>.
func checkEntryName(e *Entry) error {
	if e.Name == "" {
		return fmt.Errorf("webbundle: entry %s has no name", e.Path)
	}
	if strings.HasPrefix(e.Name, ".") {
		return fmt.Errorf("webbundle: entry name %q starts with a dot", e.Name)
	}
	for _, r := range e.Name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return fmt.Errorf("webbundle: entry name %q holds %q; a name is letters, digits, '-', '_' and '.'", e.Name, r)
		}
	}
	return nil
}

func checkEntryKind(e *Entry, p string) error {
	css := path.Ext(p) == cssExt
	switch e.Kind {
	case Module, Classic:
		if css {
			return fmt.Errorf("webbundle: %s entry %q is a stylesheet, %s; make it a Style entry", e.Kind, e.Name, e.Path)
		}
	case Style:
		if !css {
			return fmt.Errorf("webbundle: style entry %q is %s, not a .css file", e.Name, e.Path)
		}
	default:
		return fmt.Errorf("webbundle: entry %q (%s) has kind %q, not module, classic or style", e.Name, e.Path, e.Kind)
	}
	return nil
}

func logicalPath(e *Entry) string {
	if e.Kind == Style {
		return "/" + e.Name + cssExt
	}
	return "/" + e.Name + jsExt
}

func checkDefine(define map[string]string) error {
	for name, value := range define {
		if name == bundleURLs || strings.HasPrefix(name, bundleURLs+".") {
			return fmt.Errorf("webbundle: define %s is Build's own: it holds the classic entries' URLs", name)
		}
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("webbundle: define %s is %q, not a JSON value", name, value)
		}
	}
	return nil
}

func (b *build) checkShells(pages []httpserver.Page) error {
	if !slices.Contains(b.shells, httpserver.DefaultShell) {
		return fmt.Errorf("webbundle: Config.Shells has no %s, the shell httpserver.SPA answers a path that is no page with", httpserver.DefaultShell)
	}
	seen := map[string]bool{}
	for _, s := range b.shells {
		if strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".") || path.Ext(s) != ".html" || s == ".html" {
			return fmt.Errorf("webbundle: shell %q is not an .html file name at Src's root", s)
		}
		if seen[s] {
			return fmt.Errorf("webbundle: shell %s is listed twice", s)
		}
		seen[s] = true
		if _, err := b.srcFile("shell", s); err != nil {
			return err
		}
	}
	for _, p := range pages {
		shell := cmpOr(p.Shell, httpserver.DefaultShell)
		if !seen[shell] {
			return fmt.Errorf("webbundle: page %q answers with %s, which Config.Shells does not list", p.Name, shell)
		}
	}
	return nil
}

func (b *build) checkFiles() error {
	for _, f := range []struct{ field, name string }{{"PagesFile", b.pagesFile}, {"PageCorpusFile", b.corpusFile}} {
		if !filepath.IsLocal(filepath.FromSlash(f.name)) {
			return fmt.Errorf("webbundle: Config.%s %q is not a path under Src", f.field, f.name)
		}
	}
	for _, p := range b.packages {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("webbundle: Config.Packages holds %q: %w", p, err)
		}
	}
	return nil
}

// srcFile checks that name, a slash path, is a regular file under Src and
// returns its slash path under Src with every symlink resolved, the path
// esbuild's metafile names it by (a pnpm package directory is a symlink into
// node_modules/.pnpm). A symlink leading out of Src is refused: its bytes
// would ship.
func (b *build) srcFile(what, name string) (string, error) {
	clean := path.Clean(filepath.ToSlash(name))
	if name == "" || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("webbundle: %s %q is not a path under Src", what, name)
	}
	full := filepath.Join(b.src, filepath.FromSlash(clean))
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("webbundle: %s %s does not exist under Src", what, name)
	case err != nil:
		return "", fmt.Errorf("webbundle: %s %s: %w", what, name, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("webbundle: %s %s is not a regular file", what, name)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("webbundle: %s %s: %w", what, name, err)
	}
	rel, err := filepath.Rel(b.src, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("webbundle: %s %s is a link to %s, outside Src", what, name, resolved)
	}
	return filepath.ToSlash(rel), nil
}
