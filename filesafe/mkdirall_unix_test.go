//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// WithMkdirAll and WithMkdirMode replace each other, so the last one decides
// what the created directories are: 0777 through the umask with nothing
// chmod'ed (0775 under umask 002, which tells 0777 from 0755), the proved
// mode, or no creation at all.
//
// Serial (no t.Parallel): umask is process state.
func TestMkdirAll_TheLastParentOptionDecides(t *testing.T) {
	withUmask(t, 0o002)
	for _, tc := range []struct {
		name string
		opts []Option
		want os.FileMode // 0: no directory is created
	}{
		{"all", []Option{WithMkdirAll(true)}, 0o775},
		{"mode then all", []Option{WithMkdirMode(0o700), WithMkdirAll(true)}, 0o775},
		{"all then mode", []Option{WithMkdirAll(true), WithMkdirMode(0o700)}, 0o700},
		{"mode then all false", []Option{WithMkdirMode(0o700), WithMkdirAll(false)}, 0},
		{"all then mode zero", []Option{WithMkdirAll(true), WithMkdirMode(0)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "a", "b", "f.txt")
			_, err := WriteFile(t.Context(), path, []byte("x"), tc.opts...)
			if tc.want == 0 {
				var we *WriteError
				if !errors.As(err, &we) || we.Phase != PhaseTempCreate {
					t.Fatalf("WriteFile() into a missing parent = %v, want a PhaseTempCreate WriteError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteFile() = %v", err)
			}
			assertDirMode(t, filepath.Join(dir, "a"), tc.want)
			assertDirMode(t, filepath.Join(dir, "a", "b"), tc.want)
		})
	}
}

// Each directory WithMkdirAll creates is synced into its parent, as under
// WithMkdirMode, on the absolute path (a root on the deepest existing
// ancestor) and the InRoot path, so a crash cannot drop the subtree.
//
// Serial (no t.Parallel): the fsyncRootDir seam is package state.
func TestMkdirAll_FsyncsEveryCreatedDirectoryParent(t *testing.T) {
	t.Run("WriteFile", func(t *testing.T) {
		recorded := recordFsyncRootDir(t)
		path := filepath.Join(t.TempDir(), "a", "b", "f.txt")
		res, err := WriteFile(t.Context(), path, []byte("payload"), WithMkdirAll(true))
		if err != nil {
			t.Fatalf("WriteFile(WithMkdirAll) = %v", err)
		}
		if !res.Durable {
			t.Errorf("Result.Durable = false, want true")
		}
		assertFsynced(t, recorded(), []string{".", "a", "."})
	})
	t.Run("WriteFileInRoot", func(t *testing.T) {
		recorded := recordFsyncRootDir(t)
		root, _ := openTestRoot(t)
		res, err := WriteFileInRoot(t.Context(), root, "a/b/f.txt", []byte("payload"), WithMkdirAll(true))
		if err != nil {
			t.Fatalf("WriteFileInRoot(WithMkdirAll) = %v", err)
		}
		if !res.Durable {
			t.Errorf("Result.Durable = false, want true")
		}
		assertFsynced(t, recorded(), []string{".", "a", "a/b"})
	})
}

// A probe that creates its directory under WithMkdirMode proves the mode on
// each level, as a write does; otherwise a later WithMkdirMode write into it
// would accept the umask's narrower mode, since a pre-existing level is never
// checked.
//
// Serial (no t.Parallel): umask is process state.
func TestMkdirMode_ProbesProveTheModeOnEachCreatedLevel(t *testing.T) {
	withUmask(t, 0o077)
	t.Run("ProbeWritable", func(t *testing.T) {
		dir := t.TempDir()
		res, err := ProbeWritable(t.Context(), filepath.Join(dir, "a", "b"), WithMkdirMode(0o750))
		if err != nil || res.Err != nil {
			t.Fatalf("ProbeWritable() = %+v, %v", res, err)
		}
		assertDirMode(t, filepath.Join(dir, "a"), 0o750)
		assertDirMode(t, filepath.Join(dir, "a", "b"), 0o750)
	})
	t.Run("ProbeWritableInRoot", func(t *testing.T) {
		root, dir := openTestRoot(t)
		res, err := ProbeWritableInRoot(t.Context(), root, "a/b", WithMkdirMode(0o750))
		if err != nil || res.Err != nil {
			t.Fatalf("ProbeWritableInRoot() = %+v, %v", res, err)
		}
		assertDirMode(t, filepath.Join(dir, "a"), 0o750)
		assertDirMode(t, filepath.Join(dir, "a", "b"), 0o750)
	})
}

// A level a racing writer created between mkdirAllAbs's stat and its mkdir
// is synced into its parent too, so an absolute-path write that lost the
// race reports Durable only once that entry is flushed.
//
// Serial (no t.Parallel): the fsyncRootDir seam is package state.
func TestMkdirAllAbs_SyncsALevelARacingWriterCreated(t *testing.T) {
	recorded := recordFsyncRootDir(t)
	dir := t.TempDir()
	base, rel, err := deepestExistingDir(filepath.Join(dir, "a", "b"))
	if err != nil || base != dir || rel != filepath.Join("a", "b") {
		t.Fatalf("deepestExistingDir() = %q, %q, %v", base, rel, err)
	}
	// The racing writer's level appears between the stat and the mkdir.
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirSyncFailed, err := mkdirBelowExisting(base, rel, 0o777, false, slog.Default())
	if err != nil || dirSyncFailed {
		t.Fatalf("mkdirBelowExisting() = %v, %v; want false, nil", dirSyncFailed, err)
	}
	assertFsynced(t, recorded(), []string{".", "a"})
}

// A level whose mode the filesystem refused is removed again, so a retried
// write or a write after a refused probe cannot publish under a directory
// that was never proved, as a pre-existing level would be accepted.
//
// Serial (no t.Parallel): the enforceMode seam is package state.
func TestMkdirMode_ARefusedLevelIsRemoved(t *testing.T) {
	stubEnforceMode(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "f.txt")
	for attempt := range 3 {
		if _, err := WriteFile(t.Context(), path, []byte("x"), WithMkdirMode(0o750)); !errors.Is(err, ErrModeNotStored) {
			t.Fatalf("WriteFile() attempt %d = %v, want ErrModeNotStored", attempt+1, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "a")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("after attempt %d the refused level is %v, want it gone", attempt+1, err)
		}
	}
	res, err := ProbeWritable(t.Context(), filepath.Join(dir, "a"), WithMkdirMode(0o750))
	if err != nil || res.Stage != ProbeStageMkdir || !errors.Is(res.Err, ErrModeNotStored) {
		t.Fatalf("ProbeWritable() = %+v, %v; want ProbeStageMkdir with ErrModeNotStored", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after the refused probe the level is %v, want it gone", err)
	}
}
