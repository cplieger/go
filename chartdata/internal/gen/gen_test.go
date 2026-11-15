package gen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/go/chartdata/internal/gen"
)

// root is the chartdata module directory, from this package's.
const root = "../.."

// TestTheContractIsGenerated is the contract's drift check: frame.ts and
// deadset-edges.json, as committed, are what Generate writes today.
// Regenerate them with UPDATE_GOLDEN=1 go test ./... in this directory.
func TestTheContractIsGenerated(t *testing.T) {
	tmp := t.TempDir()
	if err := gen.Generate(t.Context(), root, filepath.Join(tmp, "contract"), filepath.Join(tmp, "deadset-edges.json")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, file := range []string{filepath.Join("contract", gen.TypesFile), "deadset-edges.json"} {
		got, err := os.ReadFile(filepath.Join(tmp, file))
		if err != nil {
			t.Fatalf("Generate wrote no %s: %v", file, err)
		}
		committed := filepath.Join(root, file)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(committed, got, 0o644); err != nil {
				t.Fatalf("write %s: %v", committed, err)
			}
			continue
		}
		want, err := os.ReadFile(committed)
		if err != nil {
			t.Fatalf("read %s: %v; regenerate with UPDATE_GOLDEN=1 go test ./... in chartdata/internal/gen", committed, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("chartdata/%s is stale; regenerate with UPDATE_GOLDEN=1 go test ./... in chartdata/internal/gen, and sync @cplieger/ui's copy", file)
		}
	}
}
