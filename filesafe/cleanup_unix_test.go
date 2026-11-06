//go:build !windows

package filesafe

import (
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
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
