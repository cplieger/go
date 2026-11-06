package filesafe

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestValidatePathPerformsNoFilesystemIO pins the property that separates
// ValidatePath from ProbeWritable: the gate inspects the string and consults
// nothing on disk. Every path below is one an implementation that stat-ed,
// opened, or staged a file could not accept — a name that does not exist, a
// name whose parent is a regular file (any open of it is ENOTDIR), a name under
// a directory that does not exist, a directory, a FIFO (a blocking open never
// returns), and an existing file a create-and-remove probe would have
// truncated. The directory is snapshotted around the batch, and the seeded
// file's content and identity re-checked, so a staged temp or a touched file
// would surface even if the verdicts stayed nil.
func TestValidatePathPerformsNoFilesystemIO(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	seeded := filepath.Join(dir, "seeded.txt")
	if err := os.WriteFile(seeded, []byte("untouched"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info, err := os.Stat(seeded)
	if err != nil {
		t.Fatalf("stat seed: %v", err)
	}
	id := Identify(info)

	paths := []string{
		filepath.Join(dir, "does-not-exist.txt"),
		filepath.Join(seeded, "parent-is-a-regular-file.txt"),
		filepath.Join(dir, "no-such-dir", "deep", "name.txt"),
		dir,
		seeded,
	}
	fifo := filepath.Join(dir, "pipe")
	if mkErr := MkfifoForTest(fifo); mkErr != nil {
		t.Logf("mkfifo unsupported here (%v); the blocking-open case is skipped", mkErr)
	} else {
		paths = append(paths, fifo)
	}

	before := dirEntryNames(t, dir)

	// An implementation that opened the FIFO would block until a writer
	// arrived, so the batch runs off the test goroutine: a hang becomes a
	// failure here instead of a wedged package.
	errs := make([]error, len(paths))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i, path := range paths {
			errs[i] = ValidatePath(path)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ValidatePath did not return within 10s; it must not open the path (a FIFO open blocks until a writer arrives)")
	}
	for i, path := range paths {
		if errs[i] != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil (the gate reads the string, not the filesystem)", path, errs[i])
		}
	}

	if after := dirEntryNames(t, dir); !slices.Equal(before, after) {
		t.Errorf("directory entries changed: before %v, after %v", before, after)
	}
	assertNoTempLeak(t, dir)
	assertContent(t, seeded, "untouched")
	nowInfo, err := os.Stat(seeded)
	if err != nil {
		t.Fatalf("re-stat seed: %v", err)
	}
	if !id.Matches(nowInfo) {
		t.Error("seeded file identity changed (mtime or inode); ValidatePath must not touch it")
	}
}

// dirEntryNames returns dir's entry names in ReadDir order, for a before/after
// comparison that fails on anything created or removed.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) = %v, want nil", dir, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

func TestSymlinkTarget(t *testing.T) {
	t.Parallel()

	t.Run("refuses_symlink_target", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "real.txt")
		if err := os.WriteFile(real, []byte("original"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		link := filepath.Join(dir, "link.txt")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		_, err := WriteFile(t.Context(), link, []byte("new"))
		if !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("WriteFile(symlink) = %v, want ErrSymlinkTarget", err)
		}
		got, _ := os.ReadFile(real)
		if string(got) != "original" {
			t.Errorf("original file modified: %q", got)
		}
		assertNoTempLeak(t, dir)
	})

	t.Run("no_error_for_nonexistent_target", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "new.txt")
		if _, err := WriteFile(t.Context(), path, []byte("data")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("WriteReader_refuses_symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "real.txt")
		if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		link := filepath.Join(dir, "link.txt")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := WriteReader(t.Context(), link, strings.NewReader("new")); !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("WriteReader(symlink) = %v, want ErrSymlinkTarget", err)
		}
	})

	t.Run("PendingFile_refuses_symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "real.txt")
		if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		link := filepath.Join(dir, "link.txt")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := NewPendingFile(t.Context(), link); !errors.Is(err, ErrSymlinkTarget) {
			t.Fatalf("NewPendingFile(symlink) = %v, want ErrSymlinkTarget", err)
		}
	})
}

func TestWriteFile_SymlinkInParentDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	realDir := filepath.Join(dir, "realdir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	linkDir := filepath.Join(dir, "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// The target file itself is not a symlink, only an ancestor directory is,
	// so the write is permitted and lands in the real directory.
	path := filepath.Join(linkDir, "file.txt")
	if _, err := WriteFile(t.Context(), path, []byte("through symlink parent")); err != nil {
		t.Fatalf("WriteFile through symlink parent: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(realDir, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile from realdir: %v", err)
	}
	if string(got) != "through symlink parent" {
		t.Errorf("got %q", got)
	}
}

func TestNullByte_AllEntryPoints(t *testing.T) {
	t.Parallel()
	nullPath := "/tmp/test\x00evil"

	t.Run("WriteFile", func(t *testing.T) {
		if _, err := WriteFile(t.Context(), nullPath, []byte("x")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("got %v, want ErrUnsafePath", err)
		}
	})
	t.Run("WriteReader", func(t *testing.T) {
		if _, err := WriteReader(t.Context(), nullPath, strings.NewReader("x")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("got %v, want ErrUnsafePath", err)
		}
	})
	t.Run("NewPendingFile", func(t *testing.T) {
		if _, err := NewPendingFile(t.Context(), nullPath); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("got %v, want ErrUnsafePath", err)
		}
	})
	t.Run("ReadBounded", func(t *testing.T) {
		if _, err := ReadBounded(t.Context(), nullPath, 1024); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("got %v, want ErrUnsafePath", err)
		}
	})
}

func TestEmptyPath_AllEntryPoints(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	if _, err := WriteFile(ctx, "", []byte("x")); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("WriteFile empty: %v", err)
	}
	if _, err := WriteReader(ctx, "", strings.NewReader("x")); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("WriteReader empty: %v", err)
	}
	if _, err := NewPendingFile(ctx, ""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("NewPendingFile empty: %v", err)
	}
	if _, err := ReadBounded(ctx, "", 1024); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("ReadBounded empty: %v", err)
	}
}
