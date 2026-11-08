//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCleanupStaleTemps_readdir_error_does_not_panic(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "inaccessible")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := CleanupStaleTemps(t.Context(), dir, time.Hour); err == nil {
		t.Error("CleanupStaleTemps(EACCES dir) = nil error, want readdir error")
	}
}

func TestCleanupStaleTemps_continues_after_remove_failure(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses directory-write EACCES")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, ".atomicfile-111.tmp")
	other := filepath.Join(dir, ".atomicfile-222.tmp")
	for _, p := range []string{blocked, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", p, err)
		}
		oldTime := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", p, err)
		}
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	// A read-only dir blocks unlink; CleanupStaleTemps must not panic and must
	// report zero removed since every remove fails.
	removed, err := CleanupStaleTemps(t.Context(), dir, time.Hour)
	if err != nil {
		t.Fatalf("CleanupStaleTemps = %v, want nil (per-file failures are skipped)", err)
	}
	if removed.Removed != 0 {
		t.Errorf("removed = %d, want 0 (all removes blocked)", removed)
	}
	_ = os.Chmod(dir, 0o755)
	for _, p := range []string{blocked, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("CleanupStaleTemps removed %q despite EACCES: %v", p, err)
		}
	}
}

func TestCleanupStaleTemps_RemoveFailure_CountsAsFailed(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses directory-write EACCES")
	}
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{".atomicfile-111.tmp", ".atomicfile-222.tmp"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("Chtimes %q: %v", name, err)
		}
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// Both unreclaimable candidates must reach the caller as Failed: the number
	// that says orphans are accumulating and could not be reclaimed.
	h := &captureHandler{}
	got, err := CleanupStaleTemps(t.Context(), dir, time.Hour, WithLogger(slog.New(h)))
	if err != nil {
		t.Fatalf("CleanupStaleTemps = %v, want nil (per-file failures are counted, not returned)", err)
	}
	want := SweepResult{Failed: 2}
	if got != want {
		t.Errorf("SweepResult = %+v, want %+v", got, want)
	}
	// The per-entry Debug diagnostics stay: they carry which path and which errno,
	// which the counts cannot.
	if n := h.CountLevel(slog.LevelDebug); n != 2 {
		t.Errorf("Debug record count = %d, want 2 (one per candidate)", n)
	}
	if n := h.CountLevel(slog.LevelWarn); n != 0 {
		t.Errorf("Warn record count = %d, want 0: SweepResult.Failed is the report", n)
	}
}

// An unreadable subdirectory under WithRecursive reaches the caller as a count
// rather than only as a log line, so a caller can tell "swept clean" from
// "could not look in half the tree".
func TestCleanupStaleTemps_UnreadableSubdirectoryIsCounted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0o000 directory")
	}
	t.Parallel()
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	got, err := CleanupStaleTemps(t.Context(), dir, time.Hour, WithRecursive(true))
	if err != nil {
		t.Fatalf("CleanupStaleTemps = %v, want nil (a partial sweep beats none)", err)
	}
	if got.Unreadable != 1 {
		t.Errorf("SweepResult.Unreadable = %d, want 1", got.Unreadable)
	}
}

// writeStaleTemp writes a temp-named file at path and backdates it two hours,
// past the one-hour maxAge the sweep tests use.
func writeStaleTemp(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// A recursive sweep handed a symlink to a directory sweeps the target at
// every level, as the flat sweep and a trailing-slash path do; walking it
// with filepath.WalkDir would Lstat the link and sweep nothing.
func TestCleanupStaleTemps_RecursiveSweepsThroughDirSymlink(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(base, ".atomicfile-1.tmp")
	nested := filepath.Join(base, "sub", ".atomicfile-2.tmp")
	writeStaleTemp(t, top)
	writeStaleTemp(t, nested)
	link := filepath.Join(parent, "link")
	if err := os.Symlink("base", link); err != nil {
		t.Fatal(err)
	}

	got, err := CleanupStaleTemps(t.Context(), link, time.Hour, WithRecursive(true))
	if err != nil {
		t.Fatalf("CleanupStaleTemps(symlink to dir, recursive) = %v, want nil", err)
	}
	if got.Removed != 2 {
		t.Errorf("SweepResult.Removed = %d, want 2 (one temp at each level)", got.Removed)
	}
	for _, p := range []string{top, nested} {
		if _, statErr := os.Lstat(p); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want the stale temp removed", p, statErr)
		}
	}
}

// A recursive sweep handed a stale temp-named FILE refuses it with ENOTDIR,
// as the flat sweep does, rather than reaping the path as its own entry, and
// the error names the caller's path, not path/. with the trailing dot the
// directory open adds.
func TestCleanupStaleTemps_FileIsNotADirectory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), ".atomicfile-1.tmp")
	writeStaleTemp(t, file)
	for _, recursive := range []bool{false, true} {
		_, err := CleanupStaleTemps(t.Context(), file, time.Hour, WithRecursive(recursive))
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("CleanupStaleTemps(file, recursive=%v) = %v, want ENOTDIR", recursive, err)
		}
		if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Path != file {
			t.Errorf("CleanupStaleTemps(file, recursive=%v) = %v, want a *fs.PathError naming %s", recursive, err, file)
		}
		if _, statErr := os.Lstat(file); statErr != nil {
			t.Errorf("recursive=%v: Lstat(file) = %v, want the file kept", recursive, statErr)
		}
	}
}

// A missing dir is a sweep with nothing to reclaim in both modes.
func TestCleanupStaleTemps_MissingDirIsNotAnError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	for _, recursive := range []bool{false, true} {
		if got, err := CleanupStaleTemps(t.Context(), missing, time.Hour, WithRecursive(recursive)); err != nil || got != (SweepResult{}) {
			t.Errorf("CleanupStaleTemps(missing, recursive=%v) = (%+v, %v), want an empty result and nil", recursive, got, err)
		}
	}
}

// The directory opens append /. to the name, so each error that reaches a
// caller is given the caller's own path back.
func TestDirOpenErrors_NameTheCallersPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	_, err := WriteFile(t.Context(), filepath.Join(missing, "x"), []byte("x"))
	if pe, ok := errors.AsType[*fs.PathError](err); !ok || pe.Path != missing {
		t.Errorf("WriteFile(into a missing parent) = %v, want a *fs.PathError naming %s", err, missing)
	}
	file := filepath.Join(dir, "file")
	if writeErr := os.WriteFile(file, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	res, err := ProbeWritable(t.Context(), file)
	if err != nil {
		t.Fatalf("ProbeWritable(file) = %v, want the failure in the result", err)
	}
	if pe, ok := errors.AsType[*fs.PathError](res.Err); !ok || pe.Path != file {
		t.Errorf("ProbeWritable(file).Err = %v, want a *fs.PathError naming %s", res.Err, file)
	}
}

// An ambient PendingFile's temp is named as filepath.Join would name it, with
// nothing the directory open added, so its path matches the InRoot sibling's
// and any path a caller builds itself.
func TestNewPendingFile_NameIsClean(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, err := NewPendingFile(t.Context(), filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	if want := filepath.Join(dir, filepath.Base(p.Name())); p.Name() != want {
		t.Errorf("PendingFile.Name() = %q, want %q", p.Name(), want)
	}
}

// A PendingFile staged through the root OpenParentInRoot returns reports a
// clean temp path, though that root's own Name keeps the "/." its open added.
func TestNewPendingFileInRoot_NameIsCleanThroughAPinnedParent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	parent, base, err := OpenParentInRoot(root, "d/out")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	p, err := NewPendingFileInRoot(t.Context(), parent, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	if want := filepath.Join(dir, "d", filepath.Base(p.Name())); p.Name() != want {
		t.Errorf("PendingFile.Name() = %q, want %q", p.Name(), want)
	}
}
