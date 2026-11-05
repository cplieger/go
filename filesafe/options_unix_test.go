//go:build !windows

package filesafe

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// Serial: withUmask is process-global.
func TestOptions_AllNil(t *testing.T) {
	withUmask(t, 0o027)
	p := filepath.Join(t.TempDir(), "allnil.txt")
	if _, err := WriteFile(t.Context(), p, []byte("x"), nil, nil, nil, nil); err != nil {
		t.Fatalf("WriteFile all nils: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("all-nil mode under umask 027 = %o, want 0640", fi.Mode().Perm())
	}
}

// TestOptions_AllCombined_WriteFile threads logger + mode + mkdir through a
// single write and pins that each takes effect together.
func TestOptions_AllCombined_WriteFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "combined.txt")
	l := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	res, err := WriteFile(t.Context(), path, []byte("combined"),
		WithLogger(l),
		WithMode(0o600),
		WithMkdirMode(0o755),
	)
	if err != nil {
		t.Fatalf("WriteFile combined opts: %v", err)
	}
	if !res.Durable {
		t.Errorf("Result.Durable = false, want true for a fully synced write")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", fi.Mode().Perm())
	}
	assertContent(t, path, "combined")
}
