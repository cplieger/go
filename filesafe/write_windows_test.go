package filesafe

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestReplaceOverOpenHandle writes through every entry point while another
// handle, opened through an *os.Root and so sharing delete, holds the target.
// FILE_RENAME_POSIX_SEMANTICS replaces it and leaves that handle on the old
// file; on NTFS the flush after the rename makes the write durable.
func TestReplaceOverOpenHandle(t *testing.T) {
	t.Parallel()
	for _, form := range writeForms() {
		t.Run(form.name, func(t *testing.T) {
			t.Parallel()
			root, dir := openTestRoot(t)
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			held, err := root.Open("target")
			if err != nil {
				t.Fatalf("root.Open(target): %v", err)
			}
			defer held.Close()

			res, err := form.write(t.Context(), root, dir, "target", []byte("new"))
			if err != nil {
				t.Fatalf("%s over an open share-delete handle = %v, want nil", form.name, err)
			}
			if got := readHandle(t, held); got != "old" {
				t.Errorf("held handle reads %q, want %q", got, "old")
			}
			assertContent(t, target, "new")
			fsName, _ := volumeInfo(t, dir)
			if want := fsName == "NTFS"; res.Durable != want {
				t.Errorf("Durable = %t on %s, want %t", res.Durable, fsName, want)
			}
			assertNoTempLeak(t, dir)
		})
	}
}

// TestSharingViolationRetry pins the bounded retry of a replace another
// handle refuses: 7 attempts, then ErrFileBusy in a PhaseRename WriteError
// with the old file and no temp left. It swaps rootRename, so it and its
// subtests run serially.
func TestSharingViolationRetry(t *testing.T) {
	seed := func(t *testing.T) (dir, target string) {
		t.Helper()
		dir = t.TempDir()
		target = filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return dir, target
	}

	t.Run("released", func(t *testing.T) {
		dir, target := seed(t)
		holder := holdWithoutShareDelete(t, target)
		spy := spyRenames(t)
		spy.after = func(attempt int, err error) {
			if attempt == 1 && err != nil {
				_ = holder.Close()
			}
		}
		if _, err := WriteFile(t.Context(), target, []byte("new")); err != nil {
			t.Fatalf("WriteFile after the holder closed = %v, want nil", err)
		}
		if len(spy.errs) < 2 || spy.errs[0] == nil {
			t.Errorf("attempt errors = %v, want a refused first attempt and a later success", spy.errs)
		}
		assertContent(t, target, "new")
		assertNoTempLeak(t, dir)
	})

	t.Run("held", func(t *testing.T) {
		dir, target := seed(t)
		holdWithoutShareDelete(t, target)
		spy := spyRenames(t)
		_, err := WriteFile(t.Context(), target, []byte("new"))
		if !errors.Is(err, ErrFileBusy) {
			t.Fatalf("WriteFile with the target held = %v, want ErrFileBusy", err)
		}
		var we *WriteError
		if !errors.As(err, &we) || we.Phase != PhaseRename {
			t.Errorf("err = %v, want a *WriteError with PhaseRename", err)
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			t.Errorf("err = %v, want it to wrap ERROR_ACCESS_DENIED", err)
		}
		if len(spy.errs) != 7 {
			t.Errorf("replace attempts = %d, want 7", len(spy.errs))
		}
		assertContent(t, target, "old")
		assertNoTempLeak(t, dir)
	})

	t.Run("cancelled", func(t *testing.T) {
		dir, target := seed(t)
		holdWithoutShareDelete(t, target)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		spy := spyRenames(t)
		spy.after = func(int, error) { cancel() }
		_, err := WriteFile(ctx, target, []byte("new"))
		if !errors.Is(err, ErrFileBusy) || !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteFile cancelled during the retry = %v, want ErrFileBusy and context.Canceled", err)
		}
		if len(spy.errs) != 1 {
			t.Errorf("replace attempts = %d, want 1 (the cancel ends the wait)", len(spy.errs))
		}
		assertContent(t, target, "old")
		assertNoTempLeak(t, dir)
	})

	t.Run("sourceheld", func(t *testing.T) {
		dir, target := seed(t)
		pf, err := NewPendingFile(t.Context(), target)
		if err != nil {
			t.Fatalf("NewPendingFile: %v", err)
		}
		if _, err := pf.Write([]byte("new")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// A scanner's open: shares read and write with the staged handle, not
		// delete, so the rename's DELETE open of the temp is refused.
		holder := holdWithoutShareDelete(t, pf.Name())
		spy := spyRenames(t)
		spy.after = func(attempt int, err error) {
			if attempt == 1 && err != nil {
				_ = holder.Close()
			}
		}
		if _, err := pf.Commit(t.Context()); err != nil {
			t.Fatalf("Commit after the temp's holder closed = %v, want nil", err)
		}
		if len(spy.errs) < 2 || !errors.Is(spy.errs[0], errSharingViolation) {
			t.Errorf("attempt errors = %v, want ERROR_SHARING_VIOLATION first and a later success", spy.errs)
		}
		assertContent(t, target, "new")
		assertNoTempLeak(t, dir)
	})
}

// TestReadOnlyTarget pins that a target with the read-only attribute, which
// no replace can overwrite, is refused with fs.ErrPermission before a temp
// exists or a rename is tried. It swaps rootRename, so it runs serially.
func TestReadOnlyTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatalf("set read-only: %v", err)
	}
	spy := spyRenames(t)
	_, err := WriteFile(t.Context(), target, []byte("new"))
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("WriteFile(read-only target) = %v, want fs.ErrPermission", err)
	}
	if len(spy.errs) != 0 {
		t.Errorf("replace attempts = %d, want 0", len(spy.errs))
	}
	assertContent(t, target, "old")
	assertNoTempLeak(t, dir)
}

// TestFilesafeReaderDoesNotBlockWrite pins that filesafe's own read handles
// share delete, so holding one open does not refuse a filesafe write of the
// same file. os.Open, which does not share delete, refuses it (see
// TestSharingViolationRetry/held).
func TestFilesafeReaderDoesNotBlockWrite(t *testing.T) {
	t.Parallel()
	readers := map[string]func(t *testing.T, root *os.Root, path string) *os.File{
		"OpenRegular": func(t *testing.T, _ *os.Root, path string) *os.File {
			f, _, err := OpenRegular(path)
			if err != nil {
				t.Fatalf("OpenRegular: %v", err)
			}
			return f
		},
		"ReadBounded": func(t *testing.T, _ *os.Root, path string) *os.File {
			f, err := openRead(path)
			if err != nil {
				t.Fatalf("openRead: %v", err)
			}
			return f
		},
		"OpenRegularInRoot": func(t *testing.T, root *os.Root, _ string) *os.File {
			f, _, err := OpenRegularInRoot(root, "target")
			if err != nil {
				t.Fatalf("OpenRegularInRoot: %v", err)
			}
			return f
		},
	}
	for name, open := range readers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root, dir := openTestRoot(t)
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			held := open(t, root, target)
			defer held.Close()
			if _, err := WriteFile(t.Context(), target, []byte("new")); err != nil {
				t.Fatalf("WriteFile while a %s handle is open = %v, want nil", name, err)
			}
			if got := readHandle(t, held); got != "old" {
				t.Errorf("held %s handle reads %q, want %q", name, got, "old")
			}
			assertContent(t, target, "new")
		})
	}
}

// TestRemoteVolumeNotDurable writes through the loopback administrative share
// of a local NTFS directory. The share reports the server volume's NTFS, but
// a flush over SMB reaches only the file on the server, so the write must
// report Durable false. A missing share skips off CI and fails on it.
func TestRemoteVolumeNotDurable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vol := filepath.VolumeName(dir)
	if len(vol) != 2 || vol[1] != ':' {
		t.Fatalf("TempDir %q is not on a drive letter", dir)
	}
	unc := `\\localhost\` + vol[:1] + `$` + dir[len(vol):]
	if _, err := os.Stat(unc); err != nil {
		if os.Getenv("CI") == "" {
			t.Skipf("loopback administrative share unreachable: %v", err)
		}
		t.Fatalf("Stat(%q) = %v, want the runner's loopback administrative share", unc, err)
	}
	if fsName, _ := volumeInfo(t, unc); fsName != "NTFS" {
		t.Fatalf("share %q reports filesystem %q; the test needs NTFS to isolate the remote check", unc, fsName)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := WriteFile(t.Context(), filepath.Join(unc, "target"), []byte("new"))
	if err != nil {
		t.Fatalf("WriteFile over the share = %v, want nil", err)
	}
	if res.Durable {
		t.Error("Durable = true over an SMB share, want false")
	}
	assertContent(t, target, "new")
	assertNoTempLeak(t, dir)
}
