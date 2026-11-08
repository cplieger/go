package filesafe

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// msgProbeTeardown is the Debug line tearDown emits for an unreported
// teardown failure.
const msgProbeTeardown = "filesafe: writability probe teardown failed"

// skipIfRootCannotBeDenied skips a test whose outcome depends on POSIX
// permission bits being enforced against the running user.
func skipIfRootCannotBeDenied(t *testing.T) {
	t.Helper()
	if isWindows() {
		t.Skip("POSIX mode bits drive directory-write permission")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
}

// assertEmptyDir fails t unless dir has no entries at all — the probe
// must leave the directory exactly as it found it.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) = %v, want nil", dir, err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries in %q, want 0: %v", len(entries), dir, entries)
	}
}

func TestProbeWritable(t *testing.T) {
	t.Parallel()

	t.Run("success_reports_every_stage_passed_and_leaves_nothing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		res, err := ProbeWritable(t.Context(), dir)
		if err != nil {
			t.Fatalf("ProbeWritable(%q) = err %v, want nil (a phase failure is never an error)", dir, err)
		}
		if !res.OK() || !res.Writable() {
			t.Errorf("OK()=%v Writable()=%v, want true true (phase %v, err %v)",
				res.OK(), res.Writable(), res.Phase, res.Err)
		}
		if res.Phase != ProbePhaseNone || res.Err != nil {
			t.Errorf("Phase=%v Err=%v, want ProbePhaseNone and nil", res.Phase, res.Err)
		}
		if res.Leaked {
			t.Error("Leaked = true, want false; the probe removed its own file")
		}
		if res.Dir != dir {
			t.Errorf("Dir = %q, want the probed directory %q", res.Dir, dir)
		}
		if !IsPackageTemp(res.Name) {
			t.Errorf("Name = %q, which IsPackageTemp rejects; a leaked probe would be unreclaimable", res.Name)
		}
		assertEmptyDir(t, dir)
	})

	t.Run("missing_dir_fails_at_create", func(t *testing.T) {
		t.Parallel()
		parent := t.TempDir()
		missing := filepath.Join(parent, "nope")

		res, err := ProbeWritable(t.Context(), missing)
		if err != nil {
			t.Fatalf("ProbeWritable(missing) = err %v, want nil", err)
		}
		if res.Phase != ProbePhaseCreate {
			t.Errorf("Phase = %v, want ProbePhaseCreate (a missing dir without WithMkdirMode)", res.Phase)
		}
		if !errors.Is(res.Err, fs.ErrNotExist) {
			t.Errorf("Err = %v, want an fs.ErrNotExist match", res.Err)
		}
		if res.Writable() || res.Name != "" || res.Leaked {
			t.Errorf("Writable()=%v Name=%q Leaked=%v, want false, \"\", false",
				res.Writable(), res.Name, res.Leaked)
		}
		if _, statErr := os.Stat(missing); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("probe created %q without WithMkdirMode", missing)
		}
	})

	t.Run("missing_dir_with_mkdir_mode_succeeds", func(t *testing.T) {
		t.Parallel()
		skipModeOptions(t)
		created := filepath.Join(t.TempDir(), "sub", "deeper")

		res, err := ProbeWritable(t.Context(), created, WithMkdirMode(0o750))
		if err != nil {
			t.Fatalf("ProbeWritable(missing, WithMkdirMode) = err %v, want nil", err)
		}
		if !res.OK() {
			t.Fatalf("Phase = %v (err %v), want ProbePhaseNone", res.Phase, res.Err)
		}
		fi, statErr := os.Stat(created)
		if statErr != nil {
			t.Fatalf("Stat(%q) = %v, want the directory chain created", created, statErr)
		}
		if !fi.IsDir() {
			t.Errorf("%q is not a directory", created)
		}
		assertEmptyDir(t, created)
	})

	t.Run("mkdir_failure_reports_the_mkdir_stage", func(t *testing.T) {
		t.Parallel()
		skipModeOptions(t)
		// A regular file in the parent chain makes MkdirAll fail without
		// permission trickery, so the phase is reachable as root too.
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed blocker: %v", err)
		}

		res, err := ProbeWritable(t.Context(), filepath.Join(blocker, "under"), WithMkdirMode(0o755))
		if err != nil {
			t.Fatalf("ProbeWritable = err %v, want nil", err)
		}
		if res.Phase != ProbePhaseMkdir || res.Err == nil {
			t.Errorf("Phase=%v Err=%v, want ProbePhaseMkdir and a non-nil error", res.Phase, res.Err)
		}
		if res.Writable() {
			t.Error("Writable() = true, want false; the directory does not exist")
		}
	})

	t.Run("non_directory_fails_at_create", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "bind-mounted-file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}

		res, err := ProbeWritable(t.Context(), file)
		if err != nil {
			t.Fatalf("ProbeWritable(file) = err %v, want nil", err)
		}
		if res.Phase != ProbePhaseCreate || res.Err == nil {
			t.Errorf("Phase=%v Err=%v, want ProbePhaseCreate and a non-nil error", res.Phase, res.Err)
		}
	})

	t.Run("unwritable_dir_fails_at_create", func(t *testing.T) {
		skipIfRootCannotBeDenied(t)
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		res, err := ProbeWritable(t.Context(), dir)
		if err != nil {
			t.Fatalf("ProbeWritable(unwritable) = err %v, want nil", err)
		}
		if res.Phase != ProbePhaseCreate {
			t.Errorf("Phase = %v, want ProbePhaseCreate", res.Phase)
		}
		if !errors.Is(res.Err, fs.ErrPermission) {
			t.Errorf("Err = %v, want an fs.ErrPermission match", res.Err)
		}
		if res.Writable() {
			t.Error("Writable() = true on a directory that refused the create")
		}
	})

	t.Run("empty_dir_is_an_argument_error", func(t *testing.T) {
		t.Parallel()
		res, err := ProbeWritable(t.Context(), "")
		if !errors.Is(err, ErrEmptyPath) {
			t.Errorf("err = %v, want ErrEmptyPath", err)
		}
		if res.Phase != ProbePhaseNone {
			t.Errorf("Phase = %v, want the zero value when the probe never ran", res.Phase)
		}
	})

	t.Run("cancelled_context_is_an_argument_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := ProbeWritable(ctx, dir); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want a context.Canceled match", err)
		}
		assertEmptyDir(t, dir)
	})
}

// TestProbeWritableRelativeDir pins that dir may be relative, matching
// CleanupStaleTemps rather than the write functions' absolute-path
// requirement. Not parallel: t.Chdir forbids it.
func TestProbeWritableRelativeDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	res, err := ProbeWritable(t.Context(), "./.")
	if err != nil {
		t.Fatalf("ProbeWritable(\"./.\") = err %v, want nil", err)
	}
	if !res.OK() {
		t.Errorf("Phase = %v (err %v), want ProbePhaseNone", res.Phase, res.Err)
	}
	if res.Dir != "." {
		t.Errorf("Dir = %q, want the cleaned argument %q", res.Dir, ".")
	}
	assertEmptyDir(t, dir)
}

func TestProbeWritableInRoot(t *testing.T) {
	t.Parallel()

	t.Run("probes_the_root_itself", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)

		res, err := ProbeWritableInRoot(t.Context(), root, ".")
		if err != nil {
			t.Fatalf("ProbeWritableInRoot(root, \".\") = err %v, want nil", err)
		}
		if !res.OK() {
			t.Fatalf("Phase = %v (err %v), want ProbePhaseNone", res.Phase, res.Err)
		}
		if !IsPackageTemp(res.Name) {
			t.Errorf("Name = %q, which IsPackageTemp rejects", res.Name)
		}
		assertEmptyDir(t, dir)
	})

	t.Run("probes_a_subdirectory", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)
		sub := filepath.Join(dir, "out")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}

		res, err := ProbeWritableInRoot(t.Context(), root, "out")
		if err != nil {
			t.Fatalf("ProbeWritableInRoot(root, \"out\") = err %v, want nil", err)
		}
		if !res.OK() {
			t.Fatalf("Phase = %v (err %v), want ProbePhaseNone", res.Phase, res.Err)
		}
		if res.Dir != sub {
			t.Errorf("Dir = %q, want root.Name() joined with the name (%q)", res.Dir, sub)
		}
		assertEmptyDir(t, sub)
	})

	t.Run("missing_subdirectory_with_mkdir_mode_succeeds", func(t *testing.T) {
		t.Parallel()
		skipModeOptions(t)
		root, dir := openTestRoot(t)

		res, err := ProbeWritableInRoot(t.Context(), root, "made/here", WithMkdirMode(0o755))
		if err != nil {
			t.Fatalf("ProbeWritableInRoot(WithMkdirMode) = err %v, want nil", err)
		}
		if !res.OK() {
			t.Fatalf("Phase = %v (err %v), want ProbePhaseNone", res.Phase, res.Err)
		}
		if fi, statErr := os.Stat(filepath.Join(dir, "made", "here")); statErr != nil || !fi.IsDir() {
			t.Errorf("Stat(made/here) = %v, %v; want the chain created inside the root", fi, statErr)
		}
	})

	t.Run("escaping_name_is_refused", func(t *testing.T) {
		t.Parallel()
		outer := t.TempDir()
		inner := filepath.Join(outer, "inner")
		if err := os.Mkdir(inner, 0o755); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		root, err := os.OpenRoot(inner)
		if err != nil {
			t.Fatalf("OpenRoot: %v", err)
		}
		t.Cleanup(func() { _ = root.Close() })

		cases := map[string]struct {
			opts      []WriteOption
			wantPhase ProbePhase
		}{
			"create_stage_without_mkdir": {wantPhase: ProbePhaseCreate},
			"mkdir_stage_with_mkdir":     {opts: []WriteOption{WithMkdirMode(0o755)}, wantPhase: ProbePhaseMkdir},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				if tc.opts != nil {
					skipModeOptions(t)
				}
				res, probeErr := ProbeWritableInRoot(t.Context(), root, "../escaped", tc.opts...)
				if probeErr != nil {
					t.Fatalf("ProbeWritableInRoot(escaping) = err %v, want nil (the root refuses it as a phase failure)", probeErr)
				}
				if res.Phase != tc.wantPhase || res.Err == nil {
					t.Errorf("Phase=%v Err=%v, want %v and a non-nil error", res.Phase, res.Err, tc.wantPhase)
				}
				if res.Writable() || res.Name != "" {
					t.Errorf("Writable()=%v Name=%q, want false and \"\"", res.Writable(), res.Name)
				}
			})
		}

		// Nothing may exist outside the root, at any name.
		entries, readErr := os.ReadDir(outer)
		if readErr != nil {
			t.Fatalf("ReadDir(%q) = %v", outer, readErr)
		}
		if len(entries) != 1 || entries[0].Name() != "inner" {
			t.Errorf("escaping probe touched the parent: %v", entries)
		}
		assertEmptyDir(t, inner)
	})

	t.Run("argument_errors", func(t *testing.T) {
		t.Parallel()
		root, _ := openTestRoot(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := ProbeWritableInRoot(t.Context(), nil, "."); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("nil root: err = %v, want ErrUnsafePath", err)
		}
		if _, err := ProbeWritableInRoot(t.Context(), root, ""); !errors.Is(err, ErrEmptyPath) {
			t.Errorf("empty name: err = %v, want ErrEmptyPath", err)
		}
		if _, err := ProbeWritableInRoot(t.Context(), root, "/abs"); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("absolute name: err = %v, want ErrUnsafePath", err)
		}
		if _, err := ProbeWritableInRoot(ctx, root, "."); !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled ctx: err = %v, want a context.Canceled match", err)
		}
	})
}

// TestProbeData covers the phases that run while the probe file is open, at
// probeData rather than ProbeWritable: a failure is not constructible on a
// healthy directory the test owns. The sync failure is in probe_unix_test.go.
func TestProbeData(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		open      func(t *testing.T) *os.File
		wantPhase ProbePhase
	}{
		"regular_file_passes": {
			open:      func(t *testing.T) *os.File { return openProbeTemp(t) },
			wantPhase: ProbePhaseNone,
		},
		"closed_handle_fails_at_write": {
			open: func(t *testing.T) *os.File {
				f := openProbeTemp(t)
				if err := f.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				return f
			},
			wantPhase: ProbePhaseWrite,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			phase, err := probeData(tc.open(t))
			if phase != tc.wantPhase {
				t.Errorf("phase = %v, want %v (err %v)", phase, tc.wantPhase, err)
			}
			if (err != nil) != (tc.wantPhase != ProbePhaseNone) {
				t.Errorf("err = %v, want non-nil only for a failing phase", err)
			}
		})
	}
}

// openProbeTemp creates a package-shaped probe file in a fresh temp
// directory and returns the open handle.
func openProbeTemp(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), TempName()))
	if err != nil {
		t.Fatalf("create probe file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestProbeTearDown covers the two teardown phases and the leak
// accounting. A same-owner test cannot make a real directory accept a
// create and refuse the unlink, so the refusal is constructed instead: a
// NON-EMPTY directory swapped in makes Remove fail with ENOTEMPTY, and an
// already-closed handle makes Close fail.
func TestProbeTearDown(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// prepare mutates the created probe file (and may pre-close the
		// handle) to force a teardown failure.
		prepare    func(t *testing.T, path string, f *os.File)
		firstPhase ProbePhase
		wantPhase  ProbePhase
		wantLeaked bool
		wantDebug  int
	}{
		"clean_teardown": {
			prepare:   func(*testing.T, string, *os.File) {},
			wantPhase: ProbePhaseNone,
		},
		"already_gone_is_success": {
			prepare: func(t *testing.T, path string, _ *os.File) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("pre-remove: %v", err)
				}
			},
			wantPhase: ProbePhaseNone,
		},
		"close_failure_is_reported": {
			prepare: func(t *testing.T, _ string, f *os.File) {
				if err := f.Close(); err != nil {
					t.Fatalf("pre-close: %v", err)
				}
			},
			wantPhase: ProbePhaseClose,
		},
		"remove_failure_is_reported_and_leaks": {
			prepare: func(t *testing.T, path string, _ *os.File) {
				replaceWithNonEmptyDir(t, path)
			},
			wantPhase:  ProbePhaseRemove,
			wantLeaked: true,
		},
		"first_failure_wins_and_the_second_is_logged": {
			prepare: func(t *testing.T, path string, f *os.File) {
				if err := f.Close(); err != nil {
					t.Fatalf("pre-close: %v", err)
				}
				replaceWithNonEmptyDir(t, path)
			},
			wantPhase:  ProbePhaseClose,
			wantLeaked: true,
			wantDebug:  1,
		},
		"an_earlier_stage_failure_is_never_overwritten": {
			prepare: func(t *testing.T, path string, _ *os.File) {
				replaceWithNonEmptyDir(t, path)
			},
			firstPhase: ProbePhaseWrite,
			wantPhase:  ProbePhaseWrite,
			wantLeaked: true,
			wantDebug:  1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root, dir := openTestRoot(t)
			tmpName := TempName()
			f, err := root.Create(tmpName)
			if err != nil {
				t.Fatalf("create probe file: %v", err)
			}
			t.Cleanup(func() { _ = f.Close() })
			tc.prepare(t, filepath.Join(dir, tmpName), f)

			h := &captureHandler{}
			res := ProbeResult{Dir: dir, Name: tmpName, Leaked: true, Phase: tc.firstPhase}
			if tc.firstPhase != ProbePhaseNone {
				res.Err = errors.New("earlier phase")
			}
			res.tearDown(root, f, tmpName, buildWriteCfg([]WriteOption{WithLogger(slog.New(h))}))

			if res.Phase != tc.wantPhase {
				t.Errorf("Phase = %v, want %v (err %v)", res.Phase, tc.wantPhase, res.Err)
			}
			if (res.Err != nil) != (tc.wantPhase != ProbePhaseNone) {
				t.Errorf("Err = %v, want non-nil only for a failing phase", res.Err)
			}
			if res.Leaked != tc.wantLeaked {
				t.Errorf("Leaked = %v, want %v", res.Leaked, tc.wantLeaked)
			}
			if got := h.CountLevelExact(slog.LevelDebug, msgProbeTeardown); got != tc.wantDebug {
				t.Errorf("Debug %q count = %d, want %d", msgProbeTeardown, got, tc.wantDebug)
			}
		})
	}
}

func TestProbePhaseString(t *testing.T) {
	t.Parallel()
	cases := map[ProbePhase]string{
		ProbePhaseNone:   "no failure",
		ProbePhaseMkdir:  "create directory",
		ProbePhaseCreate: "create probe file",
		ProbePhaseWrite:  "write probe file",
		ProbePhaseSync:   "sync probe file",
		ProbePhaseClose:  "close probe file",
		ProbePhaseRemove: "remove probe file",
		ProbePhase(99):   "unknown phase",
	}
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Errorf("ProbePhase(%d).String() = %q, want %q", int(phase), got, want)
		}
	}
}

func TestProbeResultWritable(t *testing.T) {
	t.Parallel()
	// A teardown failure still proves the directory took the bytes;
	// anything earlier does not.
	cases := map[ProbePhase]bool{
		ProbePhaseNone:   true,
		ProbePhaseMkdir:  false,
		ProbePhaseCreate: false,
		ProbePhaseWrite:  false,
		ProbePhaseSync:   false,
		ProbePhaseClose:  true,
		ProbePhaseRemove: true,
	}
	for phase, want := range cases {
		res := ProbeResult{Phase: phase}
		if got := res.Writable(); got != want {
			t.Errorf("ProbeResult{Phase: %v}.Writable() = %v, want %v", phase, got, want)
		}
		if got := res.OK(); got != (phase == ProbePhaseNone) {
			t.Errorf("ProbeResult{Phase: %v}.OK() = %v, want %v", phase, got, phase == ProbePhaseNone)
		}
	}
}

func TestProbeCause(t *testing.T) {
	t.Parallel()
	// A probe reports the filesystem error, not a *WriteError, which means
	// "the data did not reach its final path" — a claim about a write the
	// probe never performed.
	cause := errors.New("EACCES")
	if got := probeCause(&WriteError{Phase: PhaseTempCreate, Err: cause}); !errors.Is(got, cause) {
		t.Errorf("probeCause(*WriteError) = %v, want the unwrapped %v", got, cause)
	}
	if got := probeCause(cause); !errors.Is(got, cause) {
		t.Errorf("probeCause(plain) = %v, want it unchanged", got)
	}
	if got := probeCause(nil); got != nil {
		t.Errorf("probeCause(nil) = %v, want nil", got)
	}
}

// TestProbeLeakIsReclaimable pins that a probe left behind by a crash, or
// by a directory that denies the unlink, is reclaimed by the library's
// normal stale-temp sweep.
func TestProbeLeakIsReclaimable(t *testing.T) {
	t.Parallel()

	t.Run("ambient_sweep", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		res, err := ProbeWritable(t.Context(), dir)
		if err != nil || !res.OK() {
			t.Fatalf("ProbeWritable = %+v, err %v; want a clean probe", res, err)
		}
		leaked := seedLeakedProbe(t, dir, res.Name)

		if !IsPackageTemp(res.Name) {
			t.Fatalf("IsPackageTemp(%q) = false; the probe named a file no sweep reclaims", res.Name)
		}
		removed, sweepErr := CleanupStaleTemps(t.Context(), dir, time.Hour)
		if sweepErr != nil {
			t.Fatalf("CleanupStaleTemps: %v", sweepErr)
		}
		if removed.Removed != 1 {
			t.Errorf("removed = %d, want 1", removed)
		}
		if _, statErr := os.Stat(leaked); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("leaked probe %q survived the sweep: %v", leaked, statErr)
		}
	})

	t.Run("root_confined_sweep", func(t *testing.T) {
		t.Parallel()
		root, dir := openTestRoot(t)
		res, err := ProbeWritableInRoot(t.Context(), root, ".")
		if err != nil || !res.OK() {
			t.Fatalf("ProbeWritableInRoot = %+v, err %v; want a clean probe", res, err)
		}
		leaked := seedLeakedProbe(t, dir, res.Name)

		sweep, sweepErr := CleanupStaleTempsInRoot(t.Context(), root, time.Hour)
		if sweepErr != nil {
			t.Fatalf("CleanupStaleTempsInRoot: %v", sweepErr)
		}
		if sweep.Removed != 1 || sweep.Failed != 0 {
			t.Errorf("sweep = %+v, want Removed 1 and Failed 0", sweep)
		}
		if _, statErr := os.Stat(leaked); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("leaked probe %q survived the sweep: %v", leaked, statErr)
		}
	})
}

// seedLeakedProbe recreates a probe file under the exact name a probe
// chose and backdates it past any sweep cutoff, standing in for the crash
// between create and unlink.
func seedLeakedProbe(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte{0}, 0o600); err != nil {
		t.Fatalf("seed leaked probe %q: %v", path, err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes %q: %v", path, err)
	}
	return path
}
