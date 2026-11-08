//go:build !windows

package filesafe_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/go/filesafe"
)

// TestCleanupStaleTempsInRoot_counts_unreadable_separately pins the distinction the
// SweepResult exists for: a subdirectory that cannot be entered is Unreadable, not
// Failed, because it is a different thing for an operator to go fix.
func TestCleanupStaleTempsInRoot_counts_unreadable_separately(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Parallel()
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	stalePFXTemp(t, blocked, ".atomicfile-8888888888.tmp")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	got, sweepErr := filesafe.CleanupStaleTempsInRoot(t.Context(), root, time.Hour, filesafe.WithRecursive(true))
	if sweepErr != nil {
		t.Fatalf("CleanupStaleTempsInRoot = %v, want nil: one unreadable subdir must not abort the sweep", sweepErr)
	}
	if got.Unreadable != 1 {
		t.Errorf("Unreadable = %d, want 1", got.Unreadable)
	}
	if got.Failed != 0 {
		t.Errorf("Failed = %d, want 0: an unenterable subdir is not a failed reclaim", got.Failed)
	}
}
