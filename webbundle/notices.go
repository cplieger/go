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

// writeNotices copies the notice files of each bundled package version to
// licenses/<package>@<version>/ and writes licenses/index.txt, one
// "<package> <version>" line per directory, sorted. Every version gets its
// own directory, so a bundle holding two versions of a package ships both
// licence texts. A package root with no notice file fails the build.
func writeNotices(t *tree, src string, pkgs packageRoots) error {
	if len(pkgs) == 0 {
		return nil
	}
	written := map[string][]byte{}
	var index []string
	for _, name := range slices.Sorted(maps.Keys(pkgs)) {
		for _, root := range slices.Sorted(maps.Keys(pkgs[name])) {
			version, notices, err := readNotices(name, filepath.Join(src, filepath.FromSlash(root)))
			if err != nil {
				return err
			}
			index = append(index, name+" "+version)
			if err := writeVersionNotices(t, name+"@"+version, notices, written); err != nil {
				return err
			}
		}
	}
	slices.Sort(index)
	index = slices.Compact(index)
	return t.write(licensesDir+"index.txt", []byte(strings.Join(index, "\n")+"\n"), 0o644)
}

// writeVersionNotices writes notices under licenses/<dir>/. written holds
// every notice already written by its path: pnpm installs one version once
// per set of peers, and those copies carry the same files, so a second copy
// is skipped and one whose bytes differ (a patched package) is refused.
func writeVersionNotices(t *tree, dir string, notices []notice, written map[string][]byte) error {
	for _, n := range notices {
		p := licensesDir + dir + "/" + n.name
		prev, ok := written[p]
		switch {
		case ok && !bytes.Equal(prev, n.body):
			return fmt.Errorf("webbundle: two copies of %s carry different %s files", dir, n.name)
		case ok:
			continue
		}
		written[p] = n.body
		if err := t.write(p, n.body, 0o644); err != nil {
			return err
		}
	}
	return nil
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
	case !versionSegment(pkg.Version):
		return "", fmt.Errorf("webbundle: bundled package %s: package.json's version %q is not a semver version", name, pkg.Version)
	}
	return pkg.Version, nil
}

// versionSegment reports whether v holds only semver's characters (digits,
// letters, '.', '-' and '+') and starts with a digit, so it names one
// licenses/ directory and one index line.
func versionSegment(v string) bool {
	if v == "" || v[0] < '0' || v[0] > '9' {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '.' && r != '-' && r != '+' {
			return false
		}
	}
	return true
}
