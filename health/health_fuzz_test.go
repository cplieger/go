package health

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// FuzzProbe asserts bounded output (the probe returns only 0 or 1) and state
// consistency (a present marker yields 0, an absent one 1) in a writable
// folder. The marker name is a hash of the input, so no input can name a
// path outside the temp folder.
func FuzzProbe(f *testing.F) {
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
		path := filepath.Join(dir, fmt.Sprintf("m-%x", sha256.Sum256([]byte(suffix))))
		present := len(suffix)%2 == 0
		if present {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path) // keep the shared folder bounded across runs
		}
		got := probeCode(path)
		if got != 0 && got != 1 {
			t.Fatalf("probeCode(%q) = %d, want 0 or 1", path, got)
		}
		want := 1
		if present {
			want = 0
		}
		if got != want {
			t.Errorf("probeCode(%q), marker present %v = %d, want %d", path, present, got, want)
		}
	})
}
