//go:build !windows

package filesafe

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A parent-directory fsync failure is not a hard error: the rename already
// succeeded, so the data is at the final path. The write reports
// Result{Durable:false} with a nil error, and logs a Warn. Must hold for
// every durable write entry point.
func TestDirSyncFailure_ReportsNotDurableNotError(t *testing.T) {
	sentinel := errors.New("injected dir fsync failure")

	t.Run("WriteFile", func(t *testing.T) {
		stubFsyncRootDir(t, sentinel)
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		res, err := WriteFile(t.Context(), path, []byte("payload"), WithMode(0o644))
		if err != nil {
			t.Fatalf("WriteFile(dir-fsync fail) = %v, want nil error", err)
		}
		if res.Durable {
			t.Errorf("Result.Durable = true, want false after dir-fsync failure")
		}
		assertContent(t, path, "payload")
	})

	t.Run("WriteReader", func(t *testing.T) {
		stubFsyncRootDir(t, sentinel)
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		res, err := WriteReader(t.Context(), path, strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("WriteReader(dir-fsync fail) = %v, want nil error", err)
		}
		if res.Durable {
			t.Errorf("Result.Durable = true, want false after dir-fsync failure")
		}
		assertContent(t, path, "payload")
	})

	t.Run("PendingFile.Commit", func(t *testing.T) {
		stubFsyncRootDir(t, sentinel)
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		pf, err := NewPendingFile(t.Context(), path)
		if err != nil {
			t.Fatalf("NewPendingFile() = %v", err)
		}
		if _, err := pf.WriteString("payload"); err != nil {
			t.Fatalf("WriteString() = %v", err)
		}
		res, err := pf.Commit(t.Context())
		if err != nil {
			t.Fatalf("Commit(dir-fsync fail) = %v, want nil error", err)
		}
		if res.Durable {
			t.Errorf("Result.Durable = true, want false after dir-fsync failure")
		}
		assertContent(t, path, "payload")
	})
}

// A dir-fsync failure must emit a Warn-level log so operators can detect the
// degraded-durability condition.
func TestDirSyncFailure_LogsWarn(t *testing.T) {
	stubFsyncRootDir(t, errors.New("injected dir fsync failure"))
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	res, err := WriteFile(t.Context(), path, []byte("payload"), WithLogger(logger))
	if err != nil {
		t.Fatalf("WriteFile = %v, want nil", err)
	}
	if res.Durable {
		t.Errorf("Result.Durable = true, want false")
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected a WARN log, got: %q", buf.String())
	}
}

// The production fsyncRootDir is replaced by a stub in every other dir-sync
// test. This pins the real implementation directly: a valid directory syncs
// cleanly, and a missing directory surfaces the open error. Serial (no
// t.Parallel) so it never races the stub swappers.
func TestFsyncRootDir_Production(t *testing.T) {
	t.Run("valid_dir_returns_nil", func(t *testing.T) {
		root, _ := openTestRoot(t)
		if err := fsyncRootDir(root, "."); err != nil {
			t.Errorf("fsyncRootDir(validDir) = %v, want nil", err)
		}
	})

	t.Run("missing_dir_returns_open_error", func(t *testing.T) {
		root, _ := openTestRoot(t)
		err := fsyncRootDir(root, "does-not-exist")
		if err == nil {
			t.Fatal("fsyncRootDir(missingDir) = nil, want open error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("fsyncRootDir(missingDir) = %v, want errors.Is os.ErrNotExist", err)
		}
	})
}

// Commit is idempotent even when the first call hit a dir-fsync failure: the
// recorded (not-durable, nil-error) Result is returned again, and the content
// remains at the final path.
func TestPendingFile_Commit_IdempotentAfterDirSyncFailure(t *testing.T) {
	stubFsyncRootDir(t, errors.New("injected dir fsync failure"))
	dir := t.TempDir()
	path := filepath.Join(dir, "retry.txt")
	pf, err := NewPendingFile(t.Context(), path)
	if err != nil {
		t.Fatalf("NewPendingFile() = %v", err)
	}
	if _, err := pf.WriteString("payload"); err != nil {
		t.Fatalf("WriteString() = %v", err)
	}
	first, firstErr := pf.Commit(t.Context())
	if firstErr != nil {
		t.Fatalf("first Commit() = %v, want nil", firstErr)
	}
	if first.Durable {
		t.Errorf("first Result.Durable = true, want false")
	}
	second, secondErr := pf.Commit(t.Context())
	if secondErr != nil {
		t.Fatalf("second Commit() = %v, want nil", secondErr)
	}
	if second != first {
		t.Fatalf("second Commit() = %+v, want identical recorded result %+v", second, first)
	}
	assertContent(t, path, "payload")
}

// TestWriteFileInRoot_DirFsyncFailureNotDurable mutates the fsyncRootDir
// seam, so it must not run in parallel.
func TestWriteFileInRoot_DirFsyncFailureNotDurable(t *testing.T) {
	stubFsyncRootDir(t, errors.New("injected dir fsync failure"))
	root, dir := openTestRoot(t)
	h := &captureHandler{}
	res, err := WriteFileInRoot(t.Context(), root, "f", []byte("x"), WithLogger(slog.New(h)))
	if err != nil {
		t.Fatalf("WriteFileInRoot = %v; a post-rename fsync failure is not a hard error", err)
	}
	if res.Durable {
		t.Errorf("Durable = true, want false when the parent-dir fsync fails")
	}
	assertContent(t, filepath.Join(dir, "f"), "x")
	// The non-durable Warn must carry both root and path so an operator
	// using multiple roots can identify which on-disk file is not durable.
	var found bool
	for _, r := range h.Records() {
		if r.Level != slog.LevelWarn {
			continue
		}
		var hasRoot, hasPath bool
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "root":
				hasRoot = a.Value.String() == root.Name()
			case "path":
				hasPath = a.Value.String() == "f"
			}
			return true
		})
		if hasRoot && hasPath {
			found = true
		}
	}
	if !found {
		t.Errorf("non-durable Warn missing root=%q and path=%q attributes", root.Name(), "f")
	}
}
