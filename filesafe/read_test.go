package filesafe

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadBounded(t *testing.T) {
	t.Parallel()

	t.Run("reads_file_within_limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "bounded.txt")
		if err := os.WriteFile(path, []byte("bounded content"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := ReadBounded(t.Context(), path, 1024)
		if err != nil {
			t.Fatalf("ReadBounded: %v", err)
		}
		if string(got) != "bounded content" {
			t.Errorf("got %q, want %q", got, "bounded content")
		}
	})

	t.Run("rejects_file_exceeding_limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "large.txt")
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := ReadBounded(t.Context(), path, 50)
		if !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("ReadBounded(over) = %v, want ErrFileTooLarge", err)
		}
	})

	t.Run("returns_error_for_missing_file", func(t *testing.T) {
		t.Parallel()
		_, err := ReadBounded(t.Context(), filepath.Join(t.TempDir(), "missing.txt"), 1024)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ReadBounded(missing) = %v, want errors.Is fs.ErrNotExist", err)
		}
	})

	t.Run("reads_empty_file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := ReadBounded(t.Context(), path, 1024)
		if err != nil {
			t.Fatalf("ReadBounded: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d bytes, want 0", len(got))
		}
	})

	t.Run("exact_limit_succeeds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "exact.txt")
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := ReadBounded(t.Context(), path, 5)
		if err != nil {
			t.Fatalf("ReadBounded: %v", err)
		}
		if string(got) != "12345" {
			t.Errorf("got %q, want %q", got, "12345")
		}
	})

	t.Run("maxint64_no_overflow", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "maxint.txt")
		if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := ReadBounded(t.Context(), path, math.MaxInt64)
		if err != nil {
			t.Fatalf("ReadBounded with MaxInt64: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("context_cancelled", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "ctx.txt")
		if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ReadBounded(ctx, path, 1024); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadBounded(cancelled) = %v, want context.Canceled", err)
		}
	})

	t.Run("empty_path_returns_error", func(t *testing.T) {
		t.Parallel()
		if _, err := ReadBounded(t.Context(), "", 1024); !errors.Is(err, ErrEmptyPath) {
			t.Fatalf("ReadBounded(empty) = %v, want ErrEmptyPath", err)
		}
	})
}

func TestSaturateAdd_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{"zero+zero", 0, 0, 0},
		{"zero+one", 0, 1, 1},
		{"max+0", math.MaxInt64, 0, math.MaxInt64},
		{"max+1_saturates", math.MaxInt64, 1, math.MaxInt64},
		{"max-1+1", math.MaxInt64 - 1, 1, math.MaxInt64},
		{"max-1+2_saturates", math.MaxInt64 - 1, 2, math.MaxInt64},
		{"max+max_saturates", math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{"negative_a", -1, 1, 0},
		{"both_positive", 100, 200, 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := saturateAdd(tt.a, tt.b); got != tt.want {
				t.Errorf("saturateAdd(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestReadBounded_ZeroMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonempty.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadBounded(t.Context(), path, 0); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadBounded(0) = %v, want ErrFileTooLarge", err)
	}
}

func TestReadBounded_ZeroMax_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	data, err := ReadBounded(t.Context(), path, 0)
	if err != nil {
		t.Fatalf("ReadBounded(empty, 0): %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d bytes", len(data))
	}
}

func TestReadBounded_NegativeMax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadBounded(t.Context(), path, -1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ReadBounded(-1) = %v, want ErrInvalidArgument", err)
	}
}

// TestReadBoundedFile_GrowsPastLimitDuringRead drives the post-read length
// recheck through a FIFO the caller opened itself: it reports Size()==0 from
// Stat, so only the recheck can catch the overflow.
func TestReadBoundedFile_GrowsPastLimitDuringRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := MkfifoForTest(fifo); err != nil {
		t.Skipf("Mkfifo unsupported: %v", err)
	}
	const limit = 8
	payload := bytes.Repeat([]byte("x"), limit+4)
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		_, _ = w.Write(payload)
	}()
	f, err := os.Open(fifo)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	defer f.Close()
	if _, err := ReadBoundedFile(t.Context(), f, limit); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadBoundedFile(fifo overflow) = %v, want ErrFileTooLarge", err)
	}
}

// TestReadBounded_RefusesNonRegular pins that ReadBounded reads only regular
// files: a FIFO with no writer is refused at once instead of blocking the
// open, and a directory is refused rather than read.
func TestReadBounded_RefusesNonRegular(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targets := map[string]string{"directory": dir}
	fifo := filepath.Join(dir, "pipe")
	if err := MkfifoForTest(fifo); err == nil {
		targets["fifo"] = fifo
	}
	for name, path := range targets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() {
				_, err := ReadBounded(t.Context(), path, 64)
				done <- err
			}()
			select {
			case err := <-done:
				var nre *NotRegularError
				if !errors.Is(err, ErrNotRegular) || !errors.As(err, &nre) {
					t.Fatalf("ReadBounded(%s) = %v, want a *NotRegularError", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("ReadBounded(%s) blocked: the open is not non-blocking", name)
			}
		})
	}
}

// cancelAt=2 trips the post-stat cancellation guard (the open and stat
// succeed first).
func TestReadBounded_CancelAfterStat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ctx-after-stat.txt")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := &seqCancelCtx{Context: t.Context(), cancelAt: 2}
	if _, err := ReadBounded(ctx, path, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadBounded(cancel-after-stat) = %v, want context.Canceled", err)
	}
}
