//go:build !windows

package filesafe

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
)

// msgPrivateDirRepaired is the line EnsurePrivateDir logs after repairing a
// created directory's mode.
const msgPrivateDirRepaired = "filesafe: created directory did not keep the requested mode; repaired"

// MkfifoForTest creates a FIFO at path. It is exported for the external test
// package; on Windows it returns an error, so callers skip the case.
func MkfifoForTest(path string) error { return syscall.Mkfifo(path, 0o600) }

// stubFsyncRootDir replaces the package fsyncRootDir seam with one that
// returns err, restoring the original on test end. Every write entry point
// commits through this seam. Callers must not use t.Parallel: package state.
func stubFsyncRootDir(t *testing.T, err error) {
	t.Helper()
	orig := fsyncRootDir
	t.Cleanup(func() { fsyncRootDir = orig })
	fsyncRootDir = func(*os.Root, string) error { return err }
}

// recordFsyncRootDir replaces the package fsyncRootDir seam with one that
// records every dir it is asked to sync and then runs the original,
// restoring it on test end. Returns a function reporting the recorded dirs
// in call order. Callers must not use t.Parallel: package state.
func recordFsyncRootDir(t *testing.T) func() []string {
	t.Helper()
	orig := fsyncRootDir
	t.Cleanup(func() { fsyncRootDir = orig })
	var mu sync.Mutex
	var seen []string
	fsyncRootDir = func(root *os.Root, dir string) error {
		mu.Lock()
		seen = append(seen, dir)
		mu.Unlock()
		return orig(root, dir)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// withUmask sets the process umask for the test's duration and restores it
// after. Process-global, so callers must not use t.Parallel. Needed because a
// mode passed to mkdir(2) is only a REQUEST that umask narrows, and a test
// run under the usual 022 can't tell an enforced mode from a requested one.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	prev := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(prev) })
}

// assertDirMode fails t unless the directory at path stores exactly want in the
// bits chmod(2) can set.
func assertDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) = %v", path, err)
	}
	if !fi.IsDir() {
		t.Fatalf("Stat(%q) is not a directory (mode %s)", path, fi.Mode())
	}
	if got := chmodBits(fi.Mode()); got != want {
		t.Errorf("mode of %q = %#o, want %#o", path, got, want)
	}
}

// occupants is the Lstat of each kind of entry a refused open can meet.
type occupants struct {
	regular, dir, symlink, socket os.FileInfo
}

// lstatOccupants creates a regular file, a directory, a symlink and a Unix
// socket in a short directory and returns each one's Lstat.
func lstatOccupants(t *testing.T) occupants {
	t.Helper()
	_, dir := shortTestRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	listenUnixForTest(t, filepath.Join(dir, "s"))
	lstat := func(name string) os.FileInfo {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	return occupants{regular: lstat("f"), dir: lstat("d"), symlink: lstat("l"), socket: lstat("s")}
}

// found is a stat func that reports fi.
func found(fi os.FileInfo) func() (os.FileInfo, error) {
	return func() (os.FileInfo, error) { return fi, nil }
}
