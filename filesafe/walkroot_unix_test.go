//go:build !windows

package filesafe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWalkDirInRoot_reports_an_unreadable_directory_and_continues pins the
// two-level error contract: an unopenable directory is reported through the
// callback for ITS OWN path, and a callback answering nil keeps the walk
// running over the rest of the tree.
func TestWalkDirInRoot_reports_an_unreadable_directory_and_continues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Parallel()
	root, dir := openTestRoot(t)
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatalf("setup: Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })
	if err := os.WriteFile(filepath.Join(dir, "readable"), []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	var got collectWalk
	if err := WalkDirInRoot(t.Context(), root, got.visit); err != nil {
		t.Fatalf("WalkDirInRoot(tree with an unreadable directory) = %v, want nil", err)
	}
	if len(got.failed) != 1 || got.failed[0] != "blocked" {
		t.Errorf("WalkDirInRoot reported failures %v, want exactly [\"blocked\"]", got.failed)
	}
	// Reported once for its own path, and visited once as its parent's entry.
	if n := got.countVisits("blocked"); n != 2 {
		t.Errorf("WalkDirInRoot visited %q %d times, want 2 (once as an entry, once carrying the open"+
			" failure): a caller counting sub-paths must be able to tell those apart by the error", "blocked", n)
	}
	if !got.has("readable") {
		t.Errorf("WalkDirInRoot stopped at the unreadable directory; visited %v", got.visited)
	}
}
