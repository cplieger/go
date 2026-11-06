//go:build !windows

package filesafe

import (
	"os"
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
