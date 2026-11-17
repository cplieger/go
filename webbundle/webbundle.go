package webbundle

import (
	"errors"

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

var errNotBuilt = errors.New("webbundle: not built yet")

// Build builds c's tree into c.Out.
func Build(c Config) (Result, error) { //nolint:gocritic // hugeParam: read once per build
	_ = c
	return Result{}, errNotBuilt
}

// BuildStyle returns the CSS file at path under src bundled exactly as a
// Style entry is, and writes nothing.
func BuildStyle(src, path string) ([]byte, error) {
	_, _ = src, path
	return nil, errNotBuilt
}

// RenderPages returns the TypeScript page table for pages and apiPrefix.
func RenderPages(pages []httpserver.Page, apiPrefix string) ([]byte, error) {
	_, _ = pages, apiPrefix
	return nil, errNotBuilt
}

// RenderPageCorpus returns the page corpus for pages and apiPrefix.
func RenderPageCorpus(pages []httpserver.Page, apiPrefix string) ([]byte, error) {
	_, _ = pages, apiPrefix
	return nil, errNotBuilt
}
