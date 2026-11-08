//go:build !windows

package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenParentInRoot_AncestorRefusalsMatchSentinels pins that a refused
// ancestor is matchable: a symlinked one matches ErrSymlinkTarget and a
// regular file in a directory's position matches ErrNotDirectory, so a caller
// can tell both from ErrRaced, the refusal a retry can cure.
func TestOpenParentInRoot_AncestorRefusalsMatchSentinels(t *testing.T) {
	t.Parallel()
	root, dir := openTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]error{
		"link/x": ErrSymlinkTarget,
		"file/x": ErrNotDirectory,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parent, _, err := OpenParentInRoot(root, name)
			if err == nil {
				_ = parent.Close()
			}
			if !errors.Is(err, want) || errors.Is(err, ErrRaced) {
				t.Errorf("OpenParentInRoot(%q) = %v, want %v and not ErrRaced", name, err, want)
			}
			if rmErr := RemoveFileInRoot(root, name); !errors.Is(rmErr, want) {
				t.Errorf("RemoveFileInRoot(%q) = %v, want %v", name, rmErr, want)
			}
		})
	}
}
