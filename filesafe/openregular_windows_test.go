package filesafe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestReparsePointRefusal pins the README's refusal of a symlink and of any
// other reparse point at the final component. Go 1.23 reports a symlink as
// ModeSymlink and a junction as ModeIrregular (https://go.dev/doc/go1.23#os),
// so a symlink gives ErrSymlinkTarget and a junction ErrNotRegular.
func TestReparsePointRefusal(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (root *os.Root, dir string) {
		t.Helper()
		root, dir = openTestRoot(t)
		if err := os.WriteFile(filepath.Join(dir, "real"), []byte("old"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return root, dir
	}
	withSymlink := func(t *testing.T) (root *os.Root, dir string) {
		t.Helper()
		root, dir = setup(t)
		symlinkOrSkip(t, filepath.Join(dir, "real"), filepath.Join(dir, "link"))
		return root, dir
	}
	// A junction needs no privilege, so its cases never skip.
	withJunction := func(t *testing.T) (root *os.Root, dir string) {
		t.Helper()
		root, dir = setup(t)
		if err := os.Mkdir(filepath.Join(dir, "realdir"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		junction := filepath.Join(dir, "junction")
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, filepath.Join(dir, "realdir")).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %v: %s", err, out)
		}
		return root, dir
	}

	t.Run("OpenRegular_symlink", func(t *testing.T) {
		t.Parallel()
		_, dir := withSymlink(t)
		f, _, err := OpenRegularNoFollow(filepath.Join(dir, "link"))
		if !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("OpenRegularNoFollow(symlink) = %v, want ErrSymlinkTarget", err)
		}
		if f != nil {
			t.Errorf("OpenRegularNoFollow(symlink) returned a file, want nil")
		}
	})

	t.Run("OpenRegular_junction", func(t *testing.T) {
		t.Parallel()
		_, dir := withJunction(t)
		f, _, err := OpenRegularNoFollow(filepath.Join(dir, "junction"))
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("OpenRegularNoFollow(junction) = %v, want ErrNotRegular", err)
		}
		if f != nil {
			t.Errorf("OpenRegularNoFollow(junction) returned a file, want nil")
		}
	})

	t.Run("OpenRegularInRootNoFollow_symlink", func(t *testing.T) {
		t.Parallel()
		root, _ := withSymlink(t)
		if _, _, err := OpenRegularInRootNoFollow(root, "link"); !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("OpenRegularInRootNoFollow(symlink) = %v, want ErrSymlinkTarget", err)
		}
	})

	t.Run("WriteFile_symlink_target", func(t *testing.T) {
		t.Parallel()
		_, dir := withSymlink(t)
		link := filepath.Join(dir, "link")
		if _, err := WriteFile(t.Context(), link, []byte("new")); !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("WriteFile(symlink) = %v, want ErrSymlinkTarget", err)
		}
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Lstat(link) = %v, %v; want the symlink left in place", fi, err)
		}
		assertContent(t, filepath.Join(dir, "real"), "old")
		assertNoTempLeak(t, dir)
	})

	t.Run("WriteFileInRoot_junction_target", func(t *testing.T) {
		t.Parallel()
		root, dir := withJunction(t)
		if _, err := WriteFileInRoot(t.Context(), root, "junction", []byte("new")); !errors.Is(err, ErrNotRegular) {
			t.Fatalf("WriteFileInRoot(junction) = %v, want ErrNotRegular", err)
		}
		fi, err := os.Lstat(filepath.Join(dir, "junction"))
		if err != nil || fi.Mode()&os.ModeIrregular == 0 {
			t.Errorf("Lstat(junction) = %v, %v; want the junction left in place", fi, err)
		}
		assertNoTempLeak(t, dir)
	})
}
