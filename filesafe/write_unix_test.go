//go:build !windows

package filesafe

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestWriteFile_ro_dir(t *testing.T) {
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "out.txt")
	_, err := WriteFile(t.Context(), path, []byte("data"))
	if err == nil {
		t.Fatal("expected error for read-only dir")
	}
	we, ok := errors.AsType[*WriteError](err)
	if !ok {
		t.Fatalf("error = %T, want *WriteError", err)
	}
	if we.Phase != PhaseTempCreate {
		t.Errorf("WriteError.Phase = %v, want PhaseTempCreate", we.Phase)
	}
}

func TestWriteFile_ZeroPerm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "noperm.txt")
	if _, err := WriteFile(t.Context(), path, []byte("secret"), WithMode(0o000)); err != nil {
		t.Fatalf("WriteFile(0o000): %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o000 {
		t.Errorf("mode = %o, want 0000", fi.Mode().Perm())
	}
	_ = os.Chmod(path, 0o644)
}
