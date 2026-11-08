//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withPinOpenHook installs fn between a component's Lstat and its open for
// one test. Serial (no t.Parallel): the hook is package state, and a serial
// test runs while every parallel test is paused.
func withPinOpenHook(t *testing.T, fn func(name string)) {
	t.Helper()
	pinOpenHook = fn
	t.Cleanup(func() { pinOpenHook = nil })
}

// TestOpenParentInRoot_SwappedComponentIsErrRaced pins that every swap of a
// component between its Lstat and its open matches ErrRaced, the one refusal
// a retry can cure: another directory (the identity check), a regular file
// (ENOTDIR), and a symlink to a sibling or out of the root.
func TestOpenParentInRoot_SwappedComponentIsErrRaced(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, dir string){
		"another directory": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "d"), filepath.Join(dir, "d.old"))
			if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"a regular file": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "d"), filepath.Join(dir, "d.old"))
			if err := os.WriteFile(filepath.Join(dir, "d"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a symlink to a sibling": func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "sib"), 0o700); err != nil {
				t.Fatal(err)
			}
			mustRename(t, filepath.Join(dir, "d"), filepath.Join(dir, "d.old"))
			if err := os.Symlink("sib", filepath.Join(dir, "d")); err != nil {
				t.Fatal(err)
			}
		},
		"a symlink out of the root": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "d"), filepath.Join(dir, "d.old"))
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "d")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := openTestRoot(t)
			if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
				t.Fatal(err)
			}
			withPinOpenHook(t, func(component string) {
				if component == "d" {
					swap(t, dir)
				}
			})
			parent, _, err := OpenParentInRoot(root, "d/x")
			if err == nil {
				_ = parent.Close()
			}
			if !errors.Is(err, ErrRaced) {
				t.Errorf("OpenParentInRoot after the swap = %v, want ErrRaced", err)
			}
		})
	}
}

// TestRacedOpen_Classification pins racedOpen's rule directly: ENOTDIR and
// ELOOP are races, a name that is no longer what the Lstat saw is a race, and
// a vanished name or a failure on an unchanged name is not.
func TestRacedOpen_Classification(t *testing.T) {
	t.Parallel()
	root, dir := openTestRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := root.Lstat("f")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"ENOTDIR":            {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ENOTDIR}, true},
		"ELOOP":              {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ELOOP}, true},
		"unchanged EACCES":   {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EACCES}, false},
		"vanished (ENOENT)":  {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ENOENT}, false},
		"unchanged EMFILE":   {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EMFILE}, false},
		"unchanged io error": {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EIO}, false},
	} {
		got := racedOpen(root, "f", before, tc.err)
		if errors.Is(got, ErrRaced) != tc.want {
			t.Errorf("%s: racedOpen = %v, want ErrRaced %v", name, got, tc.want)
		}
		if !errors.Is(got, tc.err) {
			t.Errorf("%s: racedOpen = %v, lost the cause", name, got)
		}
	}

	// The name now a symlink out of the root: the open fails as an escape,
	// and the second Lstat shows the type changed.
	mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	f, openErr := root.Open("f")
	if openErr == nil {
		_ = f.Close()
		t.Fatal("opening an escaping symlink through the root succeeded")
	}
	if got := racedOpen(root, "f", before, openErr); !errors.Is(got, ErrRaced) {
		t.Errorf("racedOpen(escape after a swap) = %v, want ErrRaced", got)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

// TestWalkDirInRoot_MidReadFailureCarriesTheDirEntry pins the read-failure arm
// of the fs.WalkDir contract: a directory removed after the root was opened
// lists with ENOENT, and the failure call for "." carries its DirEntry.
func TestWalkDirInRoot_MidReadFailureCarriesTheDirEntry(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	var failed fs.DirEntry
	sawFailure := false
	walkErr := WalkDirInRoot(t.Context(), root, func(rel string, d fs.DirEntry, err error) error {
		if err != nil && rel == "." {
			sawFailure, failed = true, d
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDirInRoot = %v, want nil (the callback swallowed the failure)", walkErr)
	}
	if !sawFailure {
		t.Skip("listing a removed directory did not fail on this filesystem")
	}
	if failed == nil || !failed.IsDir() {
		t.Errorf("failure call DirEntry = %v, want the root's directory entry", failed)
	}
}

// TestWalkDirInRoot_SkipAllAtAFailureStopsTheWalk pins that fs.SkipAll from a
// failure call ends the walk: the next directory's entries are never visited.
func TestWalkDirInRoot_SkipAllAtAFailureStopsTheWalk(t *testing.T) {
	t.Parallel()
	root, dir := openTestRoot(t)
	sub := filepath.Join(dir, "sub")
	for _, d := range []string{sub, filepath.Join(dir, "zzz")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "zzz", "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var visited []string
	err := WalkDirInRoot(t.Context(), root, func(rel string, d fs.DirEntry, walkErr error) error {
		visited = append(visited, rel)
		if walkErr != nil {
			return fs.SkipAll
		}
		if rel == "sub" {
			if rmErr := os.Remove(sub); rmErr != nil {
				return rmErr
			}
			return os.WriteFile(sub, nil, 0o600)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDirInRoot = %v, want nil", err)
	}
	for _, rel := range visited {
		if rel == "zzz/file" {
			t.Errorf("visited %v, want the walk stopped before zzz's entries", visited)
		}
	}
}
