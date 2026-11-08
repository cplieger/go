package filesafe

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNilHandles_MatchErrUnsafePath pins one rule for every function taking
// an *os.Root or *os.File: nil is refused with an error matching
// ErrUnsafePath, before anything is created or removed.
func TestNilHandles_MatchErrUnsafePath(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	calls := map[string]func() error{
		"OpenRegularInRoot": func() error { _, _, err := OpenRegularInRoot(nil, "f"); return err },
		"OpenRegularInRootNoFollow": func() error {
			_, _, err := OpenRegularInRootNoFollow(nil, "f")
			return err
		},
		"ReadBoundedInRoot":       func() error { _, err := ReadBoundedInRoot(ctx, nil, "f", 1); return err },
		"ReadBoundedFile":         func() error { _, err := ReadBoundedFile(ctx, nil, 1); return err },
		"OpenParentInRoot":        func() error { _, _, err := OpenParentInRoot(nil, "f"); return err },
		"RemoveFileInRoot":        func() error { return RemoveFileInRoot(nil, "f") },
		"WalkDirInRoot":           func() error { return WalkDirInRoot(ctx, nil, func(string, fs.DirEntry, error) error { return nil }) },
		"CleanupStaleTempsInRoot": func() error { _, err := CleanupStaleTempsInRoot(ctx, nil, time.Hour); return err },
		"WriteFileInRoot":         func() error { _, err := WriteFileInRoot(ctx, nil, "f", []byte("x")); return err },
		"WriteReaderInRoot": func() error {
			_, err := WriteReaderInRoot(ctx, nil, "f", strings.NewReader("x"))
			return err
		},
		"NewPendingFileInRoot": func() error { _, err := NewPendingFileInRoot(ctx, nil, "f"); return err },
		"ProbeWritableInRoot":  func() error { _, err := ProbeWritableInRoot(ctx, nil, "."); return err },
		"EnforceMode":          func() error { _, err := EnforceMode(nil, 0o600); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := call(); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("%s(nil) = %v, want ErrUnsafePath", name, err)
			}
		})
	}
}

// TestNilHandles_RefusedBeforeOtherChecks pins that a nil handle wins over a
// done context and an over-cap size, so the ErrUnsafePath rule holds for any
// other argument.
func TestNilHandles_RefusedBeforeOtherChecks(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range map[string]func() error{
		"ReadBoundedFile, done ctx":   func() error { _, err := ReadBoundedFile(ctx, nil, 1); return err },
		"ReadBoundedInRoot, -1":       func() error { _, err := ReadBoundedInRoot(t.Context(), nil, "f", -1); return err },
		"ReadBoundedInRoot, done ctx": func() error { _, err := ReadBoundedInRoot(ctx, nil, "f", 1); return err },
		"WriteFileInRoot, over cap": func() error {
			_, err := WriteFileInRoot(t.Context(), nil, "f", []byte("xyz"), WithMaxBytes(1))
			return err
		},
		"ProbeWritableInRoot, done ctx": func() error { _, err := ProbeWritableInRoot(ctx, nil, "."); return err },
	} {
		if err := call(); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%s = %v, want ErrUnsafePath", name, err)
		}
	}
}

// TestInvalidArguments_MatchErrInvalidArgument pins the arguments no call
// could mean: each is refused with ErrInvalidArgument (or ErrEmptyPath for an
// empty sweep dir) before anything is read, created or removed.
func TestInvalidArguments_MatchErrInvalidArgument(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	fh, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fh.Close() })

	for name, tc := range map[string]struct {
		call func() error
		want error
	}{
		"CleanupStaleTemps maxAge 0": {func() error { _, err := CleanupStaleTemps(ctx, dir, 0); return err }, ErrInvalidArgument},
		"CleanupStaleTemps maxAge -1h": {func() error {
			_, err := CleanupStaleTemps(ctx, dir, -time.Hour)
			return err
		}, ErrInvalidArgument},
		"CleanupStaleTemps empty dir": {func() error { _, err := CleanupStaleTemps(ctx, "", time.Hour); return err }, ErrEmptyPath},
		"CleanupStaleTempsInRoot maxAge 0": {func() error {
			_, err := CleanupStaleTempsInRoot(ctx, root, 0)
			return err
		}, ErrInvalidArgument},
		"ReadBounded maxBytes -1":       {func() error { _, err := ReadBounded(ctx, file, -1); return err }, ErrInvalidArgument},
		"ReadBoundedFile maxBytes -1":   {func() error { _, err := ReadBoundedFile(ctx, fh, -1); return err }, ErrInvalidArgument},
		"ReadBoundedInRoot maxBytes -1": {func() error { _, err := ReadBoundedInRoot(ctx, root, "f.txt", -1); return err }, ErrInvalidArgument},
		"WriteReader nil reader": {func() error {
			_, err := WriteReader(ctx, filepath.Join(dir, "new.txt"), nil)
			return err
		}, ErrInvalidArgument},
		"WriteReaderInRoot nil reader": {func() error {
			_, err := WriteReaderInRoot(ctx, root, "new.txt", nil)
			return err
		}, ErrInvalidArgument},
		"WalkDirInRoot nil fn": {func() error { return WalkDirInRoot(ctx, root, nil) }, ErrInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call(); !errors.Is(err, tc.want) {
				t.Errorf("%s = %v, want %v", name, err, tc.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused write created new.txt (stat err %v)", err)
	}
}

// TestReadBounded_ZeroMaxBytesAdmitsOnlyAnEmptyFile pins the read-side
// meaning of 0, which is not WithMaxBytes's "no cap".
func TestReadBounded_ZeroMaxBytesAdmitsOnlyAnEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty, full := filepath.Join(dir, "empty"), filepath.Join(dir, "full")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadBounded(t.Context(), empty, 0); err != nil || len(data) != 0 {
		t.Errorf("ReadBounded(empty, 0) = %q, %v; want no bytes and nil", data, err)
	}
	if _, err := ReadBounded(t.Context(), full, 0); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("ReadBounded(one byte, 0) = %v, want ErrFileTooLarge", err)
	}
}

// TestWriteError_NilErrDoesNotPanic pins that a hand-built WriteError with no
// cause renders instead of dereferencing nil.
func TestWriteError_NilErrDoesNotPanic(t *testing.T) {
	t.Parallel()
	if got, want := (&WriteError{Phase: PhaseRename}).Error(), "filesafe: rename to final path: no cause"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestWalkDirInRoot_FailureReportCarriesTheDirEntry pins the fs.WalkDir
// contract for a directory that cannot be opened: the second call for its
// path carries its DirEntry, not nil. The callback swaps the directory for a
// file once it has been listed, so the open that follows fails.
func TestWalkDirInRoot_FailureReportCarriesTheDirEntry(t *testing.T) {
	t.Parallel()
	root, dir := openTestRoot(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	var failure struct {
		seen bool
		d    fs.DirEntry
		err  error
	}
	err := WalkDirInRoot(context.Background(), root, func(rel string, d fs.DirEntry, walkErr error) error {
		if rel != "sub" {
			return walkErr
		}
		if walkErr != nil {
			failure.seen, failure.d, failure.err = true, d, walkErr
			return nil
		}
		if rmErr := os.Remove(sub); rmErr != nil {
			return rmErr
		}
		return os.WriteFile(sub, []byte("now a file"), 0o600)
	})
	if err != nil {
		t.Fatalf("WalkDirInRoot = %v, want nil (the callback swallowed the failure)", err)
	}
	if !failure.seen {
		t.Fatal("the walk never reported the swapped directory")
	}
	if failure.d == nil || failure.d.Name() != "sub" || !failure.d.IsDir() {
		t.Errorf("failure report DirEntry = %v, want the directory entry for sub", failure.d)
	}
}
