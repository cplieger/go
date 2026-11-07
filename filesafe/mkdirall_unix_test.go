//go:build !windows

package filesafe

import (
	"path/filepath"
	"testing"
)

// WithMkdirAll leaves the created directories at 0777 through the umask, as
// os.MkdirAll does, and chmods nothing; WithMkdirMode proves its mode instead
// (TestMkdirMode_EnforcesTheRequestedMode).
//
// Serial (no t.Parallel): umask is process state.
func TestMkdirAll_CreatesThroughTheUmaskAndChmodsNothing(t *testing.T) {
	withUmask(t, 0o027)
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "f.txt")
	if _, err := WriteFile(t.Context(), path, []byte("x"), WithMkdirAll()); err != nil {
		t.Fatalf("WriteFile(WithMkdirAll) = %v", err)
	}
	assertDirMode(t, filepath.Join(dir, "a"), 0o750)
	assertDirMode(t, filepath.Join(dir, "a", "b"), 0o750)
}

// Each directory WithMkdirAll creates is synced into its parent, as under
// WithMkdirMode, so a crash cannot drop the subtree.
//
// Serial (no t.Parallel): the fsyncRootDir seam is package state.
func TestMkdirAll_FsyncsEveryCreatedDirectoryParent(t *testing.T) {
	recorded := recordFsyncRootDir(t)
	root, _ := openTestRoot(t)
	res, err := WriteFileInRoot(t.Context(), root, "a/b/f.txt", []byte("payload"), WithMkdirAll())
	if err != nil {
		t.Fatalf("WriteFileInRoot(WithMkdirAll) = %v", err)
	}
	if !res.Durable {
		t.Errorf("Result.Durable = false, want true")
	}
	assertFsynced(t, recorded(), []string{".", "a", "a/b"})
}
