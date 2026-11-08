package filesafe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupStaleTemps_HeldTempIsCountedAndSkipped pins the sweeps'
// best-effort contract against a Windows refusal Unix never gives: a stale
// temp held open without FILE_SHARE_DELETE cannot be deleted, so it counts
// as Failed and stays, while the sweep still removes the free one.
func TestCleanupStaleTemps_HeldTempIsCountedAndSkipped(t *testing.T) {
	t.Parallel()
	sweeps := map[string]func(t *testing.T, dir string) (SweepResult, error){
		"CleanupStaleTemps": func(t *testing.T, dir string) (SweepResult, error) {
			return CleanupStaleTemps(t.Context(), dir, time.Hour)
		},
		"CleanupStaleTempsInRoot": func(t *testing.T, dir string) (SweepResult, error) {
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatalf("OpenRoot: %v", err)
			}
			defer root.Close()
			return CleanupStaleTempsInRoot(t.Context(), root, time.Hour)
		},
	}
	for name, sweep := range sweeps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old := time.Now().Add(-2 * time.Hour)
			held := filepath.Join(dir, ".atomicfile-111111111.tmp")
			free := filepath.Join(dir, ".atomicfile-222222222.tmp")
			for _, p := range []string{held, free} {
				if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
					t.Fatalf("seed %q: %v", p, err)
				}
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatalf("Chtimes(%q): %v", p, err)
				}
			}
			holdWithoutShareDelete(t, held)

			res, err := sweep(t, dir)
			if err != nil {
				t.Fatalf("%s = %v, want nil (a per-file failure is counted, not returned)", name, err)
			}
			if res.Removed != 1 || res.Failed != 1 {
				t.Errorf("%s = %+v, want Removed 1 and Failed 1", name, res)
			}
			if _, statErr := os.Lstat(held); statErr != nil {
				t.Errorf("Lstat(held temp) = %v, want it left in place", statErr)
			}
			if _, statErr := os.Lstat(free); !os.IsNotExist(statErr) {
				t.Errorf("Lstat(free temp) = %v, want it removed", statErr)
			}
		})
	}
}
