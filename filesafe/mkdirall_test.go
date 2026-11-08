package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// WithMkdirAll(true) creates missing parents on every platform, Windows
// included, where WithMkdirMode is refused: a tool that only needs its
// output folder to exist must not branch its options by GOOS. A local
// t.TempDir is a durable volume on Linux, macOS and a Windows runner's NTFS.
func TestMkdirAll_CreatesMissingParentsOnEveryPlatform(t *testing.T) {
	t.Parallel()
	t.Run("WriteFile", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "a", "b", "f.txt")
		res, err := WriteFile(t.Context(), path, []byte("payload"), WithMkdirAll(true))
		if err != nil {
			t.Fatalf("WriteFile(WithMkdirAll) = %v", err)
		}
		if !res.Durable {
			t.Errorf("Result.Durable = false, want true")
		}
		assertContent(t, path, "payload")
	})
	t.Run("WriteFileInRoot", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)
		res, err := WriteFileInRoot(t.Context(), root, "a/b/f.txt", []byte("payload"), WithMkdirAll(true))
		if err != nil {
			t.Fatalf("WriteFileInRoot(WithMkdirAll) = %v", err)
		}
		if !res.Durable {
			t.Errorf("Result.Durable = false, want true")
		}
		assertContent(t, filepath.Join(dir, "a", "b", "f.txt"), "payload")
	})
	t.Run("NewPendingFile", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "a", "f.txt")
		pf, err := NewPendingFile(t.Context(), path, WithMkdirAll(true))
		if err != nil {
			t.Fatalf("NewPendingFile(WithMkdirAll) = %v", err)
		}
		defer func() { _ = pf.Cleanup() }()
		if _, err := pf.Write([]byte("payload")); err != nil {
			t.Fatalf("Write() = %v", err)
		}
		res, err := pf.Commit(t.Context())
		if err != nil {
			t.Fatalf("Commit() = %v", err)
		}
		if !res.Durable {
			t.Errorf("Result.Durable = false, want true")
		}
		assertContent(t, path, "payload")
	})
	t.Run("ProbeWritable", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "a", "b")
		res, err := ProbeWritable(t.Context(), dir, WithMkdirAll(true))
		if err != nil || res.Err != nil {
			t.Fatalf("ProbeWritable(WithMkdirAll) = %+v, %v", res, err)
		}
		if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
			t.Fatalf("probed dir = %v, %v; want it created", fi, statErr)
		}
	})
	t.Run("ProbeWritableInRoot", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)
		res, err := ProbeWritableInRoot(t.Context(), root, "a/b", WithMkdirAll(true))
		if err != nil || res.Err != nil {
			t.Fatalf("ProbeWritableInRoot(WithMkdirAll) = %+v, %v", res, err)
		}
		if fi, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil || !fi.IsDir() {
			t.Fatalf("probed dir = %v, %v; want it created", fi, statErr)
		}
	})
	t.Run("false creates nothing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "a", "f.txt")
		_, err := WriteFile(t.Context(), path, []byte("x"), WithMkdirAll(false))
		var we *WriteError
		if !errors.As(err, &we) || we.Phase != PhaseTempCreate {
			t.Fatalf("WriteFile(WithMkdirAll(false)) into a missing parent = %v, want a PhaseTempCreate WriteError", err)
		}
	})
}
