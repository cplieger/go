package filesafe

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildCfg_Defaults pins every default in buildCfg, including that a nil
// option slice is accepted (the no-options path).
func TestBuildCfg_Defaults(t *testing.T) {
	t.Parallel()
	c := buildWriteCfg(nil)
	if c.modeSet || c.mode != 0 {
		t.Errorf("default mode = %o, modeSet = %v; want 0 and false (no mode requested)", c.mode, c.modeSet)
	}
	if c.logger == nil {
		t.Error("logger is nil, want slog.Default()")
	}
	if c.recursive {
		t.Error("recursive should be false by default")
	}
	if c.repairOwnedDir {
		t.Error("repairOwnedDir should be false by default")
	}
	if c.mkdirMode != 0 {
		t.Errorf("mkdirMode = %o, want 0", c.mkdirMode)
	}
}

// TestOptions_Threading pins that each option threads its value into the
// resolved cfg.
func TestOptions_Threading(t *testing.T) {
	t.Parallel()

	t.Run("WithMode", func(t *testing.T) {
		t.Parallel()
		if c := buildWriteCfg([]WriteOption{WithMode(0o755)}); c.mode != 0o755 || !c.modeSet {
			t.Errorf("WithMode(0o755): mode = %o, modeSet = %v; want 0755 and true", c.mode, c.modeSet)
		}
	})
	t.Run("WithMode_zero", func(t *testing.T) {
		t.Parallel()
		if c := buildWriteCfg([]WriteOption{WithMode(0)}); c.mode != 0 || !c.modeSet {
			t.Errorf("WithMode(0): mode = %o, modeSet = %v; want 0 and true (an explicit 0000)", c.mode, c.modeSet)
		}
	})
	t.Run("WithMkdirMode", func(t *testing.T) {
		t.Parallel()
		if c := buildWriteCfg([]WriteOption{WithMkdirMode(0o750)}); c.mkdirMode != 0o750 {
			t.Errorf("mkdirMode = %o, want 0750", c.mkdirMode)
		}
	})
	t.Run("WithLogger", func(t *testing.T) {
		t.Parallel()
		l := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
		if c := buildWriteCfg([]WriteOption{WithLogger(l)}); c.logger != l {
			t.Error("WithLogger did not thread the logger")
		}
	})
	t.Run("WithRepairOwnedDir", func(t *testing.T) {
		t.Parallel()
		if c := buildPrivateDirCfg([]PrivateDirOption{WithRepairOwnedDir(true)}); !c.repairOwnedDir {
			t.Error("WithRepairOwnedDir(true) did not set repairOwnedDir")
		}
		if c := buildPrivateDirCfg([]PrivateDirOption{WithRepairOwnedDir(false)}); c.repairOwnedDir {
			t.Error("WithRepairOwnedDir(false) set repairOwnedDir, want the no-option default")
		}
	})
	t.Run("WithRecursive", func(t *testing.T) {
		t.Parallel()
		if c := buildSweepCfg([]SweepOption{WithRecursive(true)}); !c.recursive {
			t.Error("WithRecursive(true) did not set recursive")
		}
		if c := buildSweepCfg([]SweepOption{WithRecursive(false)}); c.recursive {
			t.Error("WithRecursive(false) set recursive, want the no-option default")
		}
	})
}

// TestOptions_OrderLastWins pins that options apply in slice order, so a later
// WithMode overrides an earlier one and unrelated options still take effect.
func TestOptions_OrderLastWins(t *testing.T) {
	t.Parallel()
	c := buildWriteCfg([]WriteOption{
		WithMode(0o600),
		WithMaxBytes(10),
		WithMode(0o755), // overrides the earlier mode
		WithMkdirMode(0o700),
	})
	if c.mode != 0o755 {
		t.Errorf("mode = %o, want 0755 (last wins)", c.mode)
	}
	if c.maxBytes != 10 {
		t.Errorf("maxBytes = %d, want 10", c.maxBytes)
	}
	if c.mkdirMode != 0o700 || !c.mkdirModeSet {
		t.Errorf("mkdirMode = %o, mkdirModeSet = %v; want 0700 and true", c.mkdirMode, c.mkdirModeSet)
	}
}

// TestOptions_LoggerAppliesToEveryFamily pins that WithLogger is the one
// Option every family takes, and that a nil logger falls back to the default.
func TestOptions_LoggerAppliesToEveryFamily(t *testing.T) {
	t.Parallel()
	l := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if c := buildSweepCfg([]SweepOption{WithLogger(l)}); c.logger != l {
		t.Error("sweep: WithLogger did not thread the logger")
	}
	if c := buildPrivateDirCfg([]PrivateDirOption{WithLogger(l)}); c.logger != l {
		t.Error("private dir: WithLogger did not thread the logger")
	}
	if c := buildWriteCfg([]WriteOption{WithLogger(nil)}); c.logger != slog.Default() {
		t.Error("WithLogger(nil) did not fall back to slog.Default()")
	}
}

// TestOptions_ZeroModesRefused pins that a zero WithMode or WithMkdirMode is
// an argument error before anything is created, on every platform, and that a
// later option can still replace a zero mkdir mode.
func TestOptions_ZeroModesRefused(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]WriteOption{
		"WithMode(0)":                    {WithMode(0)},
		"WithMkdirMode(0)":               {WithMkdirMode(0)},
		"WithMkdirAll then mkdir mode 0": {WithMkdirAll(true), WithMkdirMode(0)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := filepath.Join(dir, "sub", "f.txt")
			if _, err := WriteFile(t.Context(), p, []byte("x"), opts...); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("WriteFile = %v, want ErrInvalidArgument", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "sub")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the refused write created %s/sub (stat err %v)", dir, err)
			}
			if _, err := ProbeWritable(t.Context(), dir, opts...); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("ProbeWritable = %v, want ErrInvalidArgument", err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			inRoot := map[string]func() error{
				"WriteFileInRoot": func() error {
					_, err := WriteFileInRoot(t.Context(), root, "sub/f.txt", []byte("x"), opts...)
					return err
				},
				"WriteReaderInRoot": func() error {
					_, err := WriteReaderInRoot(t.Context(), root, "sub/f.txt", strings.NewReader("x"), opts...)
					return err
				},
				"NewPendingFileInRoot": func() error {
					p, err := NewPendingFileInRoot(t.Context(), root, "sub/f.txt", opts...)
					if err == nil {
						_ = p.Cleanup()
					}
					return err
				},
				"ProbeWritableInRoot": func() error {
					_, err := ProbeWritableInRoot(t.Context(), root, "sub", opts...)
					return err
				},
			}
			for call, run := range inRoot {
				if err := run(); !errors.Is(err, ErrInvalidArgument) {
					t.Errorf("%s = %v, want ErrInvalidArgument", call, err)
				}
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Errorf("the refused calls left %d entries in the directory (err %v)", len(entries), err)
			}
		})
	}
	if c := buildWriteCfg([]WriteOption{WithMkdirMode(0), WithMkdirAll(true)}); c.writeOptionsErr() != nil {
		t.Errorf("WithMkdirMode(0) then WithMkdirAll(true) = %v, want the later option to replace the zero mode", c.writeOptionsErr())
	}
}

// TestOptions_NilElement ensures a nil Option in the variadic slice is skipped
// rather than dereferenced (which would panic), at every public entry point.
func TestOptions_NilElement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("WriteFile", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "f.txt")
		if _, err := WriteFile(ctx, p, []byte("x"), nil, WithMaxBytes(1), nil); err != nil {
			t.Fatalf("WriteFile with nil option: %v", err)
		}
		assertContent(t, p, "x")
	})
	t.Run("WriteReader", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "r.txt")
		if _, err := WriteReader(ctx, p, strings.NewReader("hello"), nil, nil); err != nil {
			t.Fatalf("WriteReader with nil option: %v", err)
		}
		assertContent(t, p, "hello")
	})
	t.Run("NewPendingFile", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "pf.txt")
		pf, err := NewPendingFile(ctx, p, nil, nil)
		if err != nil {
			t.Fatalf("NewPendingFile with nil option: %v", err)
		}
		defer func() { _ = pf.Cleanup() }()
		if _, err := pf.Write([]byte("pf")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := pf.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		assertContent(t, p, "pf")
	})
	t.Run("CleanupStaleTemps", func(t *testing.T) {
		t.Parallel()
		if _, err := CleanupStaleTemps(t.Context(), t.TempDir(), time.Hour, nil, nil); err != nil {
			t.Fatalf("CleanupStaleTemps with nil option: %v", err)
		}
	})
}

// TestOptions_Logger pins that a custom logger is accepted and the write still
// lands.
func TestOptions_Logger(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "logged.txt")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := WriteFile(t.Context(), path, []byte("data"), WithLogger(logger)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	assertContent(t, path, "data")
}
