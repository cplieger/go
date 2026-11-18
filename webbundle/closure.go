package webbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/evanw/esbuild/pkg/api"
)

// metafile is the part of esbuild's metafile the closure checks read. Input
// paths are slash paths relative to Src; esbuild's own inputs (a define, its
// runtime) are named in angle brackets.
type metafile struct {
	Inputs map[string]struct {
		Imports []struct {
			Path     string `json:"path"`
			External bool   `json:"external"`
		} `json:"imports"`
	} `json:"inputs"`
	Outputs map[string]struct {
		EntryPoint string `json:"entryPoint"`
	} `json:"outputs"`
}

const modulesDir = "node_modules/"

// packageOf names the npm package an input belongs to ("@scope/name" or
// "name") and the package's root directory as a slash path relative to Src,
// or "" for a file of the app's own. The last node_modules segment decides,
// so a package nested in another's, or a pnpm store path, names the package
// itself.
func packageOf(input string) (name, root string) {
	var base, p string
	if i := strings.LastIndex(input, "/"+modulesDir); i >= 0 {
		base, p = input[:i+1+len(modulesDir)], input[i+1+len(modulesDir):]
	} else if rest, ok := strings.CutPrefix(input, modulesDir); ok {
		base, p = modulesDir, rest
	} else {
		return "", ""
	}
	parts := strings.SplitN(p, "/", 3)
	switch {
	case strings.HasPrefix(p, "@") && len(parts) == 3:
		name = parts[0] + "/" + parts[1]
	case !strings.HasPrefix(p, "@") && len(parts) >= 2:
		name = parts[0]
	default:
		return "", ""
	}
	return name, base + name
}

func ownInput(input string) bool { return !strings.HasPrefix(input, "<") }

// closure walks the inputs reachable from start and returns, for each, the
// input that first imported it ("" for start itself), so a chain can be
// named.
func (m *metafile) closure(start string) map[string]string {
	from := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		in := queue[0]
		queue = queue[1:]
		for _, imp := range m.Inputs[in].Imports {
			if imp.External || !ownInput(imp.Path) {
				continue
			}
			if _, seen := from[imp.Path]; !seen {
				from[imp.Path] = in
				queue = append(queue, imp.Path)
			}
		}
	}
	return from
}

func chain(from map[string]string, in string) string {
	links := []string{in}
	for p := from[in]; p != ""; p = from[p] {
		links = append(links, p)
	}
	slices.Reverse(links)
	return strings.Join(links, " -> ")
}

// packageRoots holds each bundled npm package's root directories, slash
// paths under Src, by package name; a package bundled at two versions has
// two. Each root maps to the directories inside it, root "." included, that
// hold a bundled file or lie between one and the root: those whose notices
// cover what the bundle ships.
type packageRoots map[string]map[string]map[string]bool

// add records input, a bundled file of package name rooted at root.
func (r packageRoots) add(name, root, input string) {
	if r[name] == nil {
		r[name] = map[string]map[string]bool{}
	}
	dirs := r[name][root]
	if dirs == nil {
		dirs = map[string]bool{".": true}
		r[name][root] = dirs
	}
	for d := path.Dir(strings.TrimPrefix(input, root+"/")); !dirs[d]; d = path.Dir(d) {
		dirs[d] = true
	}
}

func (r packageRoots) merge(other packageRoots) {
	for name, roots := range other {
		if r[name] == nil {
			r[name] = map[string]map[string]bool{}
		}
		for root, dirs := range roots {
			if r[name][root] == nil {
				r[name][root] = map[string]bool{}
			}
			maps.Copy(r[name][root], dirs)
		}
	}
}

func (b *build) allowed(pkg string) bool {
	return slices.ContainsFunc(b.packages, func(p string) bool { ok, _ := path.Match(p, pkg); return ok })
}

// checkClosure walks entry i's closure in m and refuses an input outside
// Src, one from a package Config.Packages does not list, one its Forbid
// names, or, when it has an Allow, one its Allow does not name, naming the
// importer chain. It returns the packages the closure bundles.
func (b *build) checkClosure(m *metafile, i int) (packageRoots, error) {
	e, rules := b.entries[i], b.rules[i]
	from := m.closure(e.Path)
	pkgs := packageRoots{}
	for _, in := range slices.Sorted(maps.Keys(from)) {
		// esbuild names an input by its real path, symlinks resolved, so a
		// link out of Src shows here as a path leaving it.
		if !filepath.IsLocal(filepath.FromSlash(in)) {
			return nil, fmt.Errorf("webbundle: entry %q bundles %s, which is outside Src: %s", e.Name, in, chain(from, in))
		}
		pkg, root := packageOf(in)
		if pkg != "" {
			if !b.allowed(pkg) {
				return nil, fmt.Errorf("webbundle: entry %q bundles %s, which Config.Packages does not list: %s", e.Name, pkg, chain(from, in))
			}
			pkgs.add(pkg, root, in)
		}
		if in == e.Path {
			continue
		}
		if f, ok := matchItem(rules.forbid, in, pkg, root); ok {
			return nil, fmt.Errorf("webbundle: entry %q reaches %s, which its Forbid names: %s", e.Name, f.text, chain(from, in))
		}
		if _, ok := matchItem(rules.allow, in, pkg, root); len(rules.allow) > 0 && !ok {
			return nil, fmt.Errorf("webbundle: entry %q bundles %s, which its Allow does not name: %s", e.Name, in, chain(from, in))
		}
	}
	return pkgs, nil
}

// closureRules is one entry's Forbid and Allow, parsed.
type closureRules struct{ forbid, allow []closureItem }

// closureItem is one Forbid or Allow item: a path under Src (dir), a whole
// npm package (pkg), or one file of a package (pkg and file, the file's
// slash path inside the package).
type closureItem struct {
	text string // as the entry lists it
	pkg  string
	file string
	dir  string // links resolved, as esbuild names inputs
}

// matches reports whether input in, of package pkg rooted at root ("" for a
// file of the app's own), is it. A package file matches in every installed
// copy of the package, a nested one or a second version included.
func (it *closureItem) matches(in, pkg, root string) bool {
	switch {
	case it.pkg == "":
		return in == it.dir || strings.HasPrefix(in, it.dir+"/")
	case it.file == "":
		return pkg == it.pkg
	default:
		return pkg == it.pkg && strings.TrimPrefix(in, root+"/") == it.file
	}
}

func matchItem(items []closureItem, in, pkg, root string) (*closureItem, bool) {
	for i := range items {
		if items[i].matches(in, pkg, root) {
			return &items[i], true
		}
	}
	return nil, false
}

// checkClosureItems parses every entry's Forbid and Allow and refuses an
// item that can match no input.
func (b *build) checkClosureItems() error {
	b.rules = make([]closureRules, len(b.entries))
	var files []pendingFile
	for i, e := range b.entries {
		for _, list := range []struct {
			dst   *[]closureItem
			field string
			items []string
		}{{&b.rules[i].forbid, "Forbid", e.Forbid}, {&b.rules[i].allow, "Allow", e.Allow}} {
			*list.dst = make([]closureItem, len(list.items))
			for j, text := range list.items {
				label := fmt.Sprintf("entry %q %s item %q", e.Name, list.field, text)
				it, err := b.parseItem(text)
				if err != nil {
					return fmt.Errorf("webbundle: %s %w", label, err)
				}
				(*list.dst)[j] = it
				if it.file != "" {
					files = append(files, pendingFile{item: &(*list.dst)[j], kind: importKind(e.Kind), label: label})
				}
			}
		}
	}
	return b.resolveItems(files)
}

// pendingFile is a package-file item whose file resolveItems sets.
type pendingFile struct {
	item  *closureItem
	label string
	kind  api.ResolveKind
}

// parseItem reads one item: a path under Src when it exists there or is
// written "./<path>", else an npm package ("@scope/name", "name") or a file
// it exports ("@scope/name/sub", its file left to resolveItems as the
// specifier itself).
func (b *build) parseItem(text string) (closureItem, error) {
	clean := path.Clean(text)
	if text == "" || clean == "." || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return closureItem{}, errors.New("is no package, package file or path under Src")
	}
	if slices.Contains(strings.Split(clean, "/"), "node_modules") {
		return closureItem{}, errors.New(`is a path under node_modules; name the package ("@scope/name") or a file it exports ("@scope/name/sub")`)
	}
	if !strings.HasPrefix(clean, "@") {
		it, found, err := b.srcItem(text, clean)
		if found || err != nil {
			return it, err
		}
	}
	return b.packageItem(text, clean)
}

// srcItem reads text as a path under Src, found when it is one.
func (b *build) srcItem(text, clean string) (it closureItem, found bool, err error) {
	explicit := strings.HasPrefix(text, "./")
	dir, found, err := b.srcPath(clean)
	switch {
	case err != nil:
		return closureItem{}, false, err
	case explicit && !found:
		return closureItem{}, false, errors.New("is written as a path under Src, and none exists there")
	case !found:
		return closureItem{}, false, nil
	}
	if first, _, _ := strings.Cut(clean, "/"); !explicit && b.installed(first) {
		return closureItem{}, false, fmt.Errorf("is both a path under Src and the package %s; write %q for the path", first, "./"+clean)
	}
	return closureItem{text: text, dir: dir}, true, nil
}

func (b *build) packageItem(text, clean string) (closureItem, error) {
	n := 1
	if strings.HasPrefix(clean, "@") {
		n = 2
	}
	parts := strings.SplitN(clean, "/", n+1)
	if len(parts) < n || parts[0] == "@" {
		return closureItem{}, errors.New("is no package name and no path under Src")
	}
	it := closureItem{text: text, pkg: strings.Join(parts[:n], "/")}
	if !b.allowed(it.pkg) {
		return closureItem{}, fmt.Errorf("names no path under Src, and the package %s is not one Config.Packages lists", it.pkg)
	}
	if len(parts) > n {
		it.file = clean
	}
	return it, nil
}

// srcPath returns name, a clean slash path, under Src with its links
// resolved, and whether it exists.
func (b *build) srcPath(name string) (dir string, found bool, err error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(b.src, filepath.FromSlash(name)))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(b.src, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false, fmt.Errorf("is a link to %s, outside Src", resolved)
	}
	rel = filepath.ToSlash(rel)
	if slices.Contains(strings.Split(rel, "/"), "node_modules") {
		return "", false, fmt.Errorf("is a link to %s, under node_modules; name the package or a file it exports", rel)
	}
	return rel, true, nil
}

func (b *build) installed(pkg string) bool {
	_, err := os.Stat(filepath.Join(b.src, "node_modules", pkg))
	return err == nil
}

// importKind is how an entry's build resolves an import: a script's import
// reads a package's "import" export condition, a stylesheet's @import does
// not.
func importKind(k EntryKind) api.ResolveKind {
	if k == Style {
		return api.ResolveCSSImportRule
	}
	return api.ResolveJSImportStatement
}

// resolveItems sets each package-file item's file to the path inside its
// package of the file an import of the item resolves to, resolved by esbuild
// as the entry's own import of it would be (the exports map, conditions and
// links alike), so the item names the file the bundle holds.
func (b *build) resolveItems(items []pendingFile) error {
	if len(items) == 0 {
		return nil
	}
	results := make([]api.ResolveResult, len(items))
	opts := options(b.src, filepath.Join(b.src, ".webbundle-resolve"), false)
	opts.Plugins = []api.Plugin{{Name: "webbundle-items", Setup: func(pb api.PluginBuild) {
		pb.OnStart(func() (api.OnStartResult, error) {
			for i, it := range items {
				results[i] = pb.Resolve(it.item.file, api.ResolveOptions{ResolveDir: b.src, Kind: it.kind})
			}
			return api.OnStartResult{}, nil
		})
	}}}
	if r := api.Build(opts); len(r.Errors) > 0 {
		return fmt.Errorf("webbundle: resolving the Forbid and Allow items: %s", messages(r.Errors))
	}
	for i, it := range items {
		file, err := b.packageFile(it.item.pkg, &results[i])
		if err != nil {
			return fmt.Errorf("webbundle: %s %w", it.label, err)
		}
		it.item.file = file
	}
	return nil
}

func (b *build) packageFile(pkg string, r *api.ResolveResult) (string, error) {
	if len(r.Errors) > 0 {
		return "", fmt.Errorf("resolves to no file: %s", messages(r.Errors))
	}
	if r.Path == "" || r.Namespace != "file" {
		return "", fmt.Errorf("resolves to %q in esbuild's %q namespace, not a file", r.Path, r.Namespace)
	}
	rel, err := filepath.Rel(b.src, r.Path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("resolves to %s, outside Src", r.Path)
	}
	rel = filepath.ToSlash(rel)
	got, root := packageOf(rel)
	if got != pkg {
		return "", fmt.Errorf("resolves to %s, not a file of %s", rel, pkg)
	}
	return strings.TrimPrefix(rel, root+"/"), nil
}
