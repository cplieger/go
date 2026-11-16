package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
)

// BundlePath is the manifest's path at the root of a bundle tree.
const BundlePath = "bundle.json"

// BundleKind is how a bundle entry was built.
type BundleKind string

// The kinds a bundler writes.
const (
	BundleModule  BundleKind = "module"  // ESM with shared chunks
	BundleClassic BundleKind = "classic" // a self-contained script: the prepaint, a worker host
	BundleStyle   BundleKind = "style"   // CSS
)

// Bundle is the manifest a bundler writes at BundlePath: every file it wrote,
// by its path in the tree, with its SHA-256 in lowercase hex, and every entry
// it built. SPA serves only the files it lists.
type Bundle struct {
	Files   map[string]string `json:"files"`
	Entries []BundleEntry     `json:"entries"`
}

// BundleEntry is one built entry: its logical name ("app" for "/app.js" or
// "/app.css" in a shell), its kind and its hashed path in the tree.
type BundleEntry struct {
	Name string     `json:"name"`
	Kind BundleKind `json:"kind"`
	Path string     `json:"path"`
}

// ReadBundle reads fsys's manifest; found is false with a nil error when fsys
// is nil or holds no BundlePath (unbuilt). A manifest that does not read,
// decode or check is an error. Each listed path is a regular file in fsys with
// no dot segment: a root .html shell, or a file under assets/ or licenses/
// other than index.html. Each entry's logical path is unique and its Path is a
// listed file under assets/.
func ReadBundle(fsys fs.FS) (Bundle, bool, error) {
	if fsys == nil {
		return Bundle{}, false, nil
	}
	raw, err := fs.ReadFile(fsys, BundlePath)
	if errors.Is(err, fs.ErrNotExist) {
		return Bundle{}, false, nil
	}
	if err != nil {
		return Bundle{}, false, fmt.Errorf("httpserver: read %s: %w", BundlePath, err)
	}
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return Bundle{}, false, fmt.Errorf("httpserver: decode %s: %w", BundlePath, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Bundle{}, false, fmt.Errorf("httpserver: %s holds data after its object", BundlePath)
	}
	if b.Files == nil {
		b.Files = map[string]string{}
	}
	if err := checkBundleFiles(fsys, b.Files); err != nil {
		return Bundle{}, false, err
	}
	if err := checkBundleEntries(b); err != nil {
		return Bundle{}, false, err
	}
	return b, true, nil
}

func checkBundleFiles(fsys fs.FS, files map[string]string) error {
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if err := checkBundlePath(p); err != nil {
			return fmt.Errorf("httpserver: %s lists %q: %w", BundlePath, p, err)
		}
		if !sha256Hex(files[p]) {
			return fmt.Errorf("httpserver: %s gives %q the hash %q, not a lowercase hex SHA-256", BundlePath, p, files[p])
		}
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return fmt.Errorf("httpserver: %s lists %q: %w", BundlePath, p, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("httpserver: %s lists %q, which is not a regular file", BundlePath, p)
		}
	}
	return nil
}

func checkBundlePath(p string) error {
	if !fs.ValidPath(p) || p == "." {
		return errors.New("not a path in the tree")
	}
	if hasDotSegment(p) {
		return errors.New("a segment starts with a dot")
	}
	top, rest, nested := strings.Cut(p, "/")
	switch {
	case !nested:
		if path.Ext(p) != htmlExt {
			return errors.New("a file at the root is a shell, an .html file")
		}
	case top != "assets" && top != "licenses":
		return errors.New("a nested file is under assets/ or licenses/")
	case path.Base(rest) == "index.html":
		// http.FileServer redirects any path ending in /index.html.
		return errors.New("a file under assets/ or licenses/ is never index.html")
	}
	return nil
}

func checkBundleEntries(b Bundle) error {
	seen := make(map[string]bool, len(b.Entries))
	for _, e := range b.Entries {
		var logical string
		switch e.Kind {
		case BundleModule, BundleClassic:
			logical = "/" + e.Name + ".js"
		case BundleStyle:
			logical = "/" + e.Name + ".css"
		default:
			return fmt.Errorf("httpserver: %s entry %q has kind %q", BundlePath, e.Name, e.Kind)
		}
		switch {
		case e.Name == "":
			return fmt.Errorf("httpserver: %s has an entry with no name", BundlePath)
		case seen[logical]:
			return fmt.Errorf("httpserver: %s names %s twice", BundlePath, logical)
		case !strings.HasPrefix(e.Path, "assets/"):
			return fmt.Errorf("httpserver: %s entry %s is at %q, outside assets/", BundlePath, logical, e.Path)
		}
		if _, ok := b.Files[e.Path]; !ok {
			return fmt.Errorf("httpserver: %s entry %s is at %q, which it does not list", BundlePath, logical, e.Path)
		}
		seen[logical] = true
	}
	return nil
}

func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hasDotSegment reports whether a slash-separated path has a segment starting
// with ".", which SPA never serves.
func hasDotSegment(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
