package webbundle

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
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

// checkClosure walks e's closure in m and refuses an input from a package
// Config.Packages does not list, or one e.Forbid names, naming the importer
// chain. It returns the packages the closure bundles.
func (b *build) checkClosure(m *metafile, e Entry) (packageRoots, error) {
	from := m.closure(e.Path)
	pkgs := packageRoots{}
	for _, in := range slices.Sorted(maps.Keys(from)) {
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
		if f, ok := forbidden(e.Forbid, in, pkg); ok {
			return nil, fmt.Errorf("webbundle: entry %q reaches %s, which its Forbid names: %s", e.Name, f, chain(from, in))
		}
	}
	return pkgs, nil
}

// forbidden returns the Forbid item naming input in, of package pkg ("" for
// the app's own file): a package by name, an Src path by whole segments.
func forbidden(forbid []string, in, pkg string) (string, bool) {
	for _, f := range forbid {
		if pkg == f || pkg == "" && (in == f || strings.HasPrefix(in, strings.TrimSuffix(f, "/")+"/")) {
			return f, true
		}
	}
	return "", false
}
