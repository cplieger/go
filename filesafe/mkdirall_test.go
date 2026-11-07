package filesafe

import (
	"os"
	"path/filepath"
	"testing"
)

// WithMkdirAll creates missing parents on every platform, Windows included,
// where WithMkdirMode is refused: a tool that only needs its output folder
// to exist must not branch its options by GOOS.
func TestMkdirAll_CreatesMissingParentsOnEveryPlatform(t *testing.T) {
	t.Parallel()
	t.Run("WriteFile", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "a", "b", "f.txt")
		if _, err := WriteFile(t.Context(), path, []byte("payload"), WithMkdirAll()); err != nil {
			t.Fatalf("WriteFile(WithMkdirAll) = %v", err)
		}
		assertContent(t, path, "payload")
	})
	t.Run("WriteFileInRoot", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)
		if _, err := WriteFileInRoot(t.Context(), root, "a/b/f.txt", []byte("payload"), WithMkdirAll()); err != nil {
			t.Fatalf("WriteFileInRoot(WithMkdirAll) = %v", err)
		}
		assertContent(t, filepath.Join(dir, "a", "b", "f.txt"), "payload")
	})
	t.Run("NewPendingFile", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "a", "f.txt")
		pf, err := NewPendingFile(t.Context(), path, WithMkdirAll())
		if err != nil {
			t.Fatalf("NewPendingFile(WithMkdirAll) = %v", err)
		}
		defer func() { _ = pf.Cleanup() }()
		if _, err := pf.Write([]byte("payload")); err != nil {
			t.Fatalf("Write() = %v", err)
		}
		if _, err := pf.Commit(t.Context()); err != nil {
			t.Fatalf("Commit() = %v", err)
		}
		assertContent(t, path, "payload")
	})
	t.Run("ProbeWritable", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "a", "b")
		res, err := ProbeWritable(t.Context(), dir, WithMkdirAll())
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
		res, err := ProbeWritableInRoot(t.Context(), root, "a/b", WithMkdirAll())
		if err != nil || res.Err != nil {
			t.Fatalf("ProbeWritableInRoot(WithMkdirAll) = %+v, %v", res, err)
		}
		if fi, statErr := os.Stat(filepath.Join(dir, "a", "b")); statErr != nil || !fi.IsDir() {
			t.Fatalf("probed dir = %v, %v; want it created", fi, statErr)
		}
	})
}

// The two parent options replace each other, so the last one decides
// whether the created directories carry a guaranteed mode.
func TestMkdirAll_AndMkdirModeReplaceEachOther(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		opts    []Option
		mode    os.FileMode
		enforce bool
		create  bool
	}{
		{"none", nil, 0, false, false},
		{"mkdir all", []Option{WithMkdirAll()}, 0o777, false, true},
		{"mkdir mode", []Option{WithMkdirMode(0o750)}, 0o750, true, true},
		{"mode then all", []Option{WithMkdirMode(0o750), WithMkdirAll()}, 0o777, false, true},
		{"all then mode", []Option{WithMkdirAll(), WithMkdirMode(0o750)}, 0o750, true, true},
		{"all then mode zero", []Option{WithMkdirAll(), WithMkdirMode(0)}, 0, false, false},
	} {
		mode, enforce, create := buildCfg(tc.opts).parentDirs()
		if mode != tc.mode || enforce != tc.enforce || create != tc.create {
			t.Errorf("%s: parentDirs() = %o, %v, %v; want %o, %v, %v",
				tc.name, mode, enforce, create, tc.mode, tc.enforce, tc.create)
		}
	}
}

// WithMkdirAll is not a mode option, so it never trips the Windows refusal.
func TestMkdirAll_IsNotAModeOption(t *testing.T) {
	t.Parallel()
	if err := buildCfg([]Option{WithMkdirAll()}).modeOptionsErr(); err != nil {
		t.Errorf("modeOptionsErr(WithMkdirAll) = %v, want nil", err)
	}
}
