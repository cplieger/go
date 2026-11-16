package httpserver

import (
	"errors"
	"io/fs"
)

// BundlePath is the manifest's path at the root of a bundle tree.
const BundlePath = "bundle.json"

// BundleKind is how a bundle entry was built.
type BundleKind string

const (
	BundleModule  BundleKind = "module"
	BundleClassic BundleKind = "classic"
	BundleStyle   BundleKind = "style"
)

// Bundle is the manifest a bundler writes at BundlePath.
type Bundle struct {
	Files   map[string]string `json:"files"`
	Entries []BundleEntry     `json:"entries"`
}

// BundleEntry is one built entry.
type BundleEntry struct {
	Name string     `json:"name"`
	Kind BundleKind `json:"kind"`
	Path string     `json:"path"`
}

// ReadBundle reads and checks fsys's manifest.
func ReadBundle(fsys fs.FS) (Bundle, bool, error) {
	return Bundle{}, false, errors.New("not implemented")
}
