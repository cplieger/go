package health

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// FuzzProbeCheck asserts bounded output (ProbeCheck never panics and
// returns only 0 or 1 for any raw input path) and state consistency (a
// present marker yields 0, absent yields 1) in a controlled writable
// dir. The marker name is a hash of the input so pathological inputs
// ("..", ".", "/") can neither escape the temp dir nor collapse onto it.
func FuzzProbeCheck(f *testing.F) {
	f.Add(".healthy")
	f.Add("/tmp/.healthy")
	f.Add("/nonexistent/path/.healthy")
	f.Add("")
	f.Add(".")
	f.Add("/")
	f.Add("..")
	f.Add("../")
	f.Add("../../../etc/passwd")

	dir := f.TempDir()

	f.Fuzz(func(t *testing.T, suffix string) {
		// Bounded output: ProbeCheck must never panic and must return
		// only 0 or 1 for any raw input path.
		if got := ProbeCheck(suffix); got != 0 && got != 1 {
			t.Fatalf("ProbeCheck(%q) = %d, want 0 or 1", suffix, got)
		}

		name := fmt.Sprintf("m-%x", sha256.Sum256([]byte(suffix)))
		path := filepath.Join(dir, name)

		if len(suffix)%2 == 0 {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Skip()
			}
			if got := ProbeCheck(path); got != 0 {
				t.Errorf("ProbeCheck(%q) with marker present = %d, want 0", path, got)
			}
			_ = os.Remove(path) // keep the shared TempDir bounded across fuzz execs
			return
		}
		_ = os.Remove(path)
		if got := ProbeCheck(path); got != 1 {
			t.Errorf("ProbeCheck(%q) with marker absent in writable dir = %d, want 1", path, got)
		}
	})
}
