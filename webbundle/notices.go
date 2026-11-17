package webbundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const licensesDir = "licenses/"

// noticeFamilies and sourceExts are the families cplieger/ci
// configs/collect-licenses.sh copies for a Go module (copy_files), with the
// script's .go exclusion widened to the source files an npm package ships.
var (
	noticeFamilies = []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"}
	sourceExts     = []string{".JS", ".MJS", ".CJS", ".TS", ".MTS", ".CTS", ".MAP"}
)

func isNotice(name string) bool {
	upper := strings.ToUpper(name)
	hasPrefix := func(p string) bool { return strings.HasPrefix(upper, p) }
	return slices.ContainsFunc(noticeFamilies, hasPrefix) && !slices.Contains(sourceExts, path.Ext(upper))
}

// notice is one notice file at a package's root.
type notice struct {
	name string
	body []byte
}

// writeNotices copies each bundled package's notice files to
// licenses/<package>/ and writes licenses/index.txt, one "<package>
// <version>" line per bundled version, sorted. A package root with no notice
// file, or two versions of a package whose notice files of one name differ,
// fails the build: either would ship the bundle without a licence text it
// redistributes.
func writeNotices(t *tree, src string, pkgs packageRoots) error {
	if len(pkgs) == 0 {
		return nil
	}
	var index []string
	for _, name := range slices.Sorted(maps.Keys(pkgs)) {
		versions, err := writePackageNotices(t, src, name, pkgs[name])
		if err != nil {
			return err
		}
		for _, v := range versions {
			index = append(index, name+" "+v)
		}
	}
	slices.Sort(index)
	index = slices.Compact(index)
	return t.write(licensesDir+"index.txt", []byte(strings.Join(index, "\n")+"\n"), 0o644)
}

// writePackageNotices copies the notice files of every root of the package
// name into licenses/<name>/, one copy of each file name, and returns the
// version at each root.
func writePackageNotices(t *tree, src, name string, roots map[string]bool) ([]string, error) {
	written := map[string][]byte{}
	var versions []string
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		version, notices, err := readNotices(name, filepath.Join(src, filepath.FromSlash(root)))
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
		for _, n := range notices {
			prev, ok := written[n.name]
			switch {
			case ok && !bytes.Equal(prev, n.body):
				return nil, fmt.Errorf("webbundle: %s is bundled at more than one version and their %s files differ; dedupe it", name, n.name)
			case ok:
				continue
			}
			written[n.name] = n.body
			if err := t.write(licensesDir+name+"/"+n.name, n.body, 0o644); err != nil {
				return nil, err
			}
		}
	}
	return versions, nil
}

// readNotices reads the version and the notice files of the package name
// rooted at dir, refusing a package with none.
func readNotices(name, dir string) (string, []notice, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, fmt.Errorf("webbundle: notices of %s: %w", name, err)
	}
	defer root.Close()
	version, err := packageVersion(root, name)
	if err != nil {
		return "", nil, err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return "", nil, fmt.Errorf("webbundle: notices of %s: %w", name, err)
	}
	var notices []notice
	for _, e := range entries {
		if !isNotice(e.Name()) {
			continue
		}
		// Stat follows a symlink, as the script's [ -f ] does; the root keeps
		// it inside the package.
		info, err := root.Stat(e.Name())
		if err != nil {
			return "", nil, fmt.Errorf("webbundle: notices of %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		body, err := root.ReadFile(e.Name())
		if err != nil {
			return "", nil, fmt.Errorf("webbundle: notices of %s: %w", name, err)
		}
		notices = append(notices, notice{name: e.Name(), body: body})
	}
	if len(notices) == 0 {
		return "", nil, fmt.Errorf("webbundle: bundled package %s %s ships no LICENSE, LICENCE, COPYING or NOTICE file at its root", name, version)
	}
	return version, notices, nil
}

func packageVersion(root *os.Root, name string) (string, error) {
	raw, err := root.ReadFile("package.json")
	if err != nil {
		return "", fmt.Errorf("webbundle: bundled package %s: %w", name, err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("webbundle: bundled package %s: package.json: %w", name, err)
	}
	switch {
	case pkg.Version == "":
		return "", fmt.Errorf("webbundle: bundled package %s: package.json has no version", name)
	case strings.ContainsFunc(pkg.Version, unicode.IsSpace):
		return "", fmt.Errorf("webbundle: bundled package %s: package.json's version %q holds a space", name, pkg.Version)
	}
	return pkg.Version, nil
}
