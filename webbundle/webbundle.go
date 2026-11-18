package webbundle

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"

	"github.com/cplieger/go/httpserver/v2"
	"golang.org/x/net/html"
)

// Config is one app's bundle: where its sources are, where the tree goes,
// and what it holds.
//
// Manifest's members pass through to the built manifest, which gains
// "icons" from Icon and MaskableIcon; each "src" member names a file by its
// path from the manifest's folder, written as a hashed asset and named by
// its served URL. Its id, start_url, scope, shortcut urls and
// share_target.action are root-relative, and all but id lie inside scope.
type Config struct {
	Define         map[string]string  // esbuild defines, each value JSON
	IconPNGs       map[IconPNG]string // committed PNGs under Src, copied instead of rendered; nil renders
	Src            string             // the static-src root, holding package.json and node_modules
	Out            string             // the output tree the UI package embeds, replaced whole
	PagesFile      string             // the TypeScript page table under Src; "" is "pages.gen.ts"
	APIPrefix      string             // the API's path prefix ("/api"), as httpserver.SPAConfig.APIPrefix
	PageCorpusFile string             // the page corpus under Src; "" is "pages.corpus.gen.json"
	Icon           string             // the app's icon, an SVG under Src; "" is none
	MaskableIcon   string             // an SVG under Src drawn for a mask, listed only in the manifest; "" is none
	Manifest       string             // the web app manifest, a JSON file under Src; "" writes none
	Shells         []string           // the shells under Src; nil is ["index.html"]
	Entries        []Entry
	Pages          []httpserver.Page // the UI package's pages
	Packages       []string          // npm packages a bundle may include; nil is ["@cplieger/*"]
}

// IconPNG names one PNG Build writes for the icons: Icon's at 32, 180, 192
// and 512 px, MaskableIcon's at 192 and 512 px.
type IconPNG struct {
	Purpose IconPurpose
	Size    int // in px, the PNG being square
}

// IconPurpose is the purpose a manifest lists an icon with.
type IconPurpose string

// The icon purposes.
const (
	PurposeAny      IconPurpose = ""         // Icon's PNGs, listed with no purpose, which is "any"
	PurposeMaskable IconPurpose = "maskable" // MaskableIcon's PNGs, listed as "maskable"
)

// EntryKind is how an entry is built.
type EntryKind string

// The entry kinds, as bundle.json records them.
const (
	Module  = EntryKind(httpserver.BundleModule)  // ESM, with shared chunks under assets/chunks/
	Classic = EntryKind(httpserver.BundleClassic) // an IIFE with its own closure: the prepaint, a worker host
	Style   = EntryKind(httpserver.BundleStyle)   // CSS, its url() files written as hashed assets
)

// Entry is one bundle entry point.
//
// A Forbid or Allow item names an npm package ("@scope/name"), a file a
// package exports ("@scope/name/sub") or a path under Src ("src/wire", or
// "./<path>" where it is also a package name), by whole path segments. Both
// judge only the files whose code the entry ships; the entry file is exempt,
// and an item that can match no input fails the build. docs/configuration.md
// gives the full rules.
type Entry struct {
	Name string // the logical name, [a-z][a-z0-9_]*: "/<Name>.js" or "/<Name>.css" in a shell
	Path string // the source file under Src
	Kind EntryKind
	// URL is "" for a hashed path under /assets/. On a Classic entry it may
	// be "/<file>.js": the entry is written there unhashed, the same URL in
	// every build, and bundle.json lists it as stable. A service worker
	// needs one, since the browser looks for its update at the URL it
	// registered; a hashed URL changes with every build, and scope is no
	// reason, since a Service-Worker-Allowed header lets one under /assets/
	// control /.
	URL    string
	Forbid []string // items its imports may not reach
	Allow  []string // when set, the only items its imports may reach
}

// Result is what a build wrote.
type Result struct {
	Outputs map[string]Output // every script in the tree (entries and chunks), by its path under Out; bundle.json does not record it
	Files   []string          // every written path under Out, sorted
	Notices []string          // every npm package whose code the bundle ships, sorted; licenses/index.txt also lists the notices under Src
}

// Output is what one script of the tree holds: an app can tell a chunk
// built only from npm packages from one that also holds its own code,
// though both list the same packages.
type Output struct {
	Packages []string // the npm packages whose code it holds, sorted
	Own      bool     // whether it holds code of a file under Src outside node_modules
}

// Build builds c's tree into c.Out: the classic entries first, then the
// module entries (which read the classic entries' served URLs through the
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
		for _, l := range sh.refs {
			named[l.entry] = true
		}
		shells = append(shells, sh)
	}
	for i, e := range b.entries {
		if !named[i] && e.Kind != Classic {
			return nil, nil, fmt.Errorf("webbundle: no shell names entry %q (%s)", e.Name, logicalPath(&e))
		}
	}
	return shells, named, nil
}

// entriesBuilt is what building every entry into the tree gave.
type entriesBuilt struct {
	pkgs     packageRoots
	own      map[string]bool // every bundled file under Src outside node_modules
	urls     map[string]string
	scripts  map[string]Output
	paths    []string // each entry's path in the tree, by index
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
			return Result{}, fmt.Errorf("webbundle: classic entry %q is named by no shell and read by no module entry, through BUNDLE_URLS or its URL", e.Name)
		}
	}
	if werr := writeShells(t, shells, eb.paths, head); werr != nil {
		return Result{}, werr
	}
	for _, f := range b.iconFiles() {
		eb.own[f] = true
	}
	if nerr := writeNotices(t, b.src, eb.pkgs, eb.own); nerr != nil {
		return Result{}, nerr
	}
	entries := make([]httpserver.BundleEntry, len(b.entries))
	for i, e := range b.entries {
		entries[i] = httpserver.BundleEntry{Name: e.Name, Kind: httpserver.BundleKind(e.Kind), Path: eb.paths[i], Stable: e.URL != ""}
	}
	files, err := t.manifest(entries)
	if err != nil {
		return Result{}, err
	}
	return Result{Outputs: eb.scripts, Files: files, Notices: slices.Sorted(maps.Keys(eb.pkgs))}, nil
}

// writeShells renders every shell into t.
func writeShells(t *tree, shells []shell, paths []string, head [][]html.Attribute) error {
	for _, sh := range shells {
		body, err := sh.render(paths, head)
		if err != nil {
			return err
		}
		if err := t.write(sh.name, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// buildEntries builds the classic entries, then the module entries, then the
// style entries into outdir.
func (b *build) buildEntries(outdir string) (*entriesBuilt, error) {
	eb := &entriesBuilt{pkgs: packageRoots{}, own: map[string]bool{}, urls: map[string]string{}, scripts: map[string]Output{}, paths: make([]string, len(b.entries))}
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
		r, err := buildStyle(b.src, &e, outdir, true)
		if err != nil {
			return nil, err
		}
		if err := b.take(eb, &r, i); err != nil {
			return nil, err
		}
	}
	return eb, nil
}

// take checks entry i's closure and its built file in r, and records its
// packages and its path in the tree.
func (b *build) take(eb *entriesBuilt, r *built, i int) error {
	e := b.entries[i]
	pkgs, own, err := b.checkClosure(&r.meta, i)
	if err != nil {
		return err
	}
	eb.pkgs.merge(pkgs)
	for _, f := range own {
		eb.own[f] = true
	}
	name, body, ok := r.entryFile(e.Path)
	switch {
	case !ok:
		return fmt.Errorf("webbundle: entry %q built no output", e.Name)
	case r.empty(name, body):
		return fmt.Errorf("webbundle: %s entry %q (%s) builds an empty file", e.Kind, e.Name, e.Path)
	}
	eb.paths[i] = name
	maps.Copy(eb.scripts, r.scripts)
	return nil
}

func (b *build) buildClassicEntries(eb *entriesBuilt, outdir string) error {
	for i, e := range b.entries {
		if e.Kind != Classic {
			continue
		}
		r, err := b.buildClassic(&e, outdir)
		if err != nil {
			return err
		}
		if err := b.take(eb, &r, i); err != nil {
			return err
		}
		eb.urls[e.Name] = "/" + eb.paths[i]
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
		if err := b.take(eb, &r, i); err != nil {
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
