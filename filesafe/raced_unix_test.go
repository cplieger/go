//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
		"ENOTDIR":                  {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ENOTDIR}, true},
		"ELOOP":                    {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ELOOP}, true},
		"unchanged EACCES":         {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EACCES}, false},
		"ENOENT, name still there": {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ENOENT}, true},
		"os.Root not a directory":  {&fs.PathError{Op: "open", Path: "f", Err: errors.New("not a directory")}, true},
		"closed root":              {&fs.PathError{Op: "openat", Path: "f", Err: os.ErrClosed}, false},
		"ENXIO":                    {&fs.PathError{Op: "openat", Path: "f", Err: syscall.ENXIO}, true},
		"unchanged EMFILE":         {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EMFILE}, false},
		"unchanged io error":       {&fs.PathError{Op: "openat", Path: "f", Err: syscall.EIO}, false},
	} {
		got := racedOpen(root, "f", before, tc.err)
		if errors.Is(got, ErrRaced) != tc.want {
			t.Errorf("%s: racedOpen = %v, want ErrRaced %v", name, got, tc.want)
		}
		if !errors.Is(got, tc.err) {
			t.Errorf("%s: racedOpen = %v, lost the cause", name, got)
		}
	}

	if got := racedOpen(root, "nothere", before, &fs.PathError{Op: "openat", Path: "nothere", Err: syscall.ENOENT}); errors.Is(got, ErrRaced) || !errors.Is(got, fs.ErrNotExist) {
		t.Errorf("racedOpen(vanished) = %v, want fs.ErrNotExist and not ErrRaced", got)
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

// TestOpenRegularInRootNoFollow_SwappedNameIsErrRaced pins both ErrRaced arms
// of OpenRegularInRootNoFollow: a name swapped between its Lstat and its open
// for another file or a directory (the identity check), or for a symlink out
// of the root or a dangling one (racedOpen), matches ErrRaced.
func TestOpenRegularInRootNoFollow_SwappedNameIsErrRaced(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, dir string){
		"another regular file": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			if err := os.WriteFile(filepath.Join(dir, "f"), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			if err := os.Mkdir(filepath.Join(dir, "f"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"a symlink out of the root": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "f")); err != nil {
				t.Fatal(err)
			}
		},
		"a dangling symlink": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			if err := os.Symlink("missing", filepath.Join(dir, "f")); err != nil {
				t.Fatal(err)
			}
		},
		"a socket": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			listenUnixForTest(t, filepath.Join(dir, "f"))
		},
		"a symlink to a socket": func(t *testing.T, dir string) {
			listenUnixForTest(t, filepath.Join(dir, "s"))
			mustRename(t, filepath.Join(dir, "f"), filepath.Join(dir, "f.old"))
			if err := os.Symlink("s", filepath.Join(dir, "f")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := shortTestRoot(t)
			if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			withPinOpenHook(t, func(n string) {
				if n == "f" {
					swap(t, dir)
				}
			})
			f, _, err := OpenRegularInRootNoFollow(root, "f")
			if err == nil {
				_ = f.Close()
			}
			if !errors.Is(err, ErrRaced) {
				t.Errorf("OpenRegularInRootNoFollow after the swap = %v, want ErrRaced", err)
			}
		})
	}
}

// shortTestRoot is openTestRoot in a short directory, so a socket path inside
// it stays under the 108-byte limit.
func shortTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

// listenUnixForTest binds a Unix socket at path for the test's lifetime.
func listenUnixForTest(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

// TestOpenParentInRoot_FIFOSwappedForAComponentIsRefused pins that a FIFO
// swapped in for a directory component between its Lstat and its open is
// refused at once (ErrRaced) instead of blocking the open on a FIFO with no
// writer.
func TestOpenParentInRoot_FIFOSwappedForAComponentIsRefused(t *testing.T) {
	root, dir := openTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	withPinOpenHook(t, func(component string) {
		if component != "d" {
			return
		}
		mustRename(t, filepath.Join(dir, "d"), filepath.Join(dir, "d.old"))
		if err := MkfifoForTest(filepath.Join(dir, "d")); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}
	})
	done := make(chan error, 1)
	go func() {
		parent, _, err := OpenParentInRoot(root, "d/x")
		if err == nil {
			_ = parent.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRaced) {
			t.Errorf("OpenParentInRoot after a FIFO swap = %v, want ErrRaced", err)
		}
		if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Path != "d" {
			t.Errorf("OpenParentInRoot after a FIFO swap = %v, want a *fs.PathError naming d", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OpenParentInRoot blocked on a FIFO swapped in for a directory")
	}
}

// TestDirectoryEntryPoints_RefuseAFIFOWithoutBlocking pins that a FIFO where a
// directory belongs is refused at once by the ambient entry points that open
// a directory by path, with ENOTDIR naming the caller's path: a plain refusal,
// not a lost race. ProbeWritable reports it in its result.
func TestDirectoryEntryPoints_RefuseAFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := MkfifoForTest(fifo); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	for name, call := range map[string]func() error{
		"WriteFile": func() error {
			_, err := WriteFile(t.Context(), filepath.Join(fifo, "f.txt"), []byte("x"))
			return err
		},
		"ProbeWritable": func() error {
			res, err := ProbeWritable(t.Context(), fifo)
			if err != nil {
				return err
			}
			return res.Err
		},
		"CleanupStaleTemps": func() error { _, err := CleanupStaleTemps(t.Context(), fifo, time.Hour); return err },
		"CleanupStaleTemps, deep": func() error {
			_, err := CleanupStaleTemps(t.Context(), fifo, time.Hour, WithRecursive(true))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { done <- call() }()
			select {
			case err := <-done:
				if !errors.Is(err, syscall.ENOTDIR) || errors.Is(err, ErrRaced) {
					t.Errorf("%s on a FIFO = %v, want ENOTDIR and not ErrRaced", name, err)
				}
				if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Path != fifo {
					t.Errorf("%s on a FIFO = %v, want a *fs.PathError naming %s", name, err, fifo)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s blocked on a FIFO where a directory belongs", name)
			}
		})
	}
}
