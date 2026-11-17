package filesafe

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsurePrivateDirRepairsWidenedCreate pins that a directory this
// call created, whose mode came back WIDER than 0700, is repaired and
// re-verified rather than returned as compliant.
//
// Linux only: a new directory inherits its parent's S_ISGID there (mkdir(2),
// fs/inode.c inode_init_owner), and macOS and the BSDs never set it on mkdir.
// The fixture is verified before the assertion, so a kernel that stops
// widening fails the test as INVALID rather than passing vacuously.
func TestEnsurePrivateDirRepairsWidenedCreate(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "parent")
	mkdirExact(t, parent, os.ModeSetgid|privateDirMode)

	// Confirm the fixture still produces a mode mkdir did not ask for.
	witness := filepath.Join(parent, "witness")
	if err := os.Mkdir(witness, privateDirMode); err != nil {
		t.Fatalf("mkdir witness: %v", err)
	}
	widened := permOf(t, witness)
	if widened == privateDirMode {
		t.Fatalf("fixture no longer widens: a 0700 mkdir under a %v parent stored %#o, so this test cannot exercise the repair",
			os.ModeSetgid|privateDirMode, widened)
	}

	h := &captureHandler{}
	dir := filepath.Join(parent, "state")
	pd, err := EnsurePrivateDir(dir, WithLogger(slog.New(h)))
	if err != nil {
		t.Fatalf("EnsurePrivateDir(%s) = %v, want nil", dir, err)
	}
	if !pd.Created {
		t.Fatal("Created = false, want true")
	}
	if !pd.Repaired {
		t.Errorf("Repaired = false, want true: mkdir stored %#o instead of %#o", widened, privateDirMode)
	}
	if pd.Mode != privateDirMode {
		t.Errorf("Mode = %#o, want %#o: the repair must re-stat, not report what was asked for", pd.Mode, privateDirMode)
	}
	if onDisk := permOf(t, dir); onDisk != privateDirMode {
		t.Errorf("directory is %#o on disk, want %#o: the widening survived the repair", onDisk, privateDirMode)
	}
	if got := h.CountLevelExact(slog.LevelWarn, msgPrivateDirRepaired); got != 1 {
		t.Errorf("Warn %q count = %d, want 1: a filesystem that ignores mode requests is operator-visible news",
			msgPrivateDirRepaired, got)
	}
}
