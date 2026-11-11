package health

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestHealthMarker_SetCreatesAndRemoves covers the happy path: Set(true)
// creates the marker, Set(false) removes it.
func TestHealthMarker_SetCreatesAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	m.Set(true)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after Set(true): %v", err)
	}

	m.Set(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should not exist after Set(false): %v", err)
	}
}

// TestHealthMarker_Cleanup confirms Cleanup removes the marker and is
// safe to call when the marker already does not exist.
func TestHealthMarker_Cleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	m.Set(true)
	m.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should be gone after Cleanup: %v", err)
	}

	m.Cleanup() // must not error
}

// TestHealthMarker_DegradedMode verifies that when the marker directory
// is not writable, the marker enters degraded mode: Set and Cleanup are
// no-ops and no file is ever created.
func TestHealthMarker_DegradedMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}

	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)

	if !m.degraded.Load() {
		t.Skip("test environment bypasses directory mode; skipping") // root/Windows may allow writes through 0500
	}

	m.Set(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("degraded marker should never create file: %v", err)
	}
	m.Cleanup() // must not panic
}

// TestHealthMarker_Idempotent ensures repeated Set(true) and Set(false)
// calls are safe and converge to the expected file state.
func TestHealthMarker_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	for range 3 {
		m.Set(true)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after repeated Set(true): %v", err)
	}

	for range 3 {
		m.Set(false)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should not exist after repeated Set(false): %v", err)
	}
}

// TestHealthMarker_Property exercises arbitrary Set sequences and asserts
// that the file state always matches the last Set argument.
func TestHealthMarker_Property(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(rt *rapid.T) {
		nonce := rapid.StringMatching(`[a-z0-9]{8}`).Draw(rt, "nonce")
		subdir := filepath.Join(dir, nonce)
		if err := os.Mkdir(subdir, 0o755); err != nil {
			rt.Fatalf("mkdir subdir: %v", err)
		}
		path := filepath.Join(subdir, ".healthy")
		m := NewMarker(path)

		calls := rapid.SliceOfN(rapid.Bool(), 1, 30).Draw(rt, "calls")
		for _, ok := range calls {
			m.Set(ok)
		}
		last := calls[len(calls)-1]

		_, err := os.Stat(path)
		exists := err == nil
		if exists != last {
			rt.Fatalf("after Set(%v): exists=%v, want %v",
				last, exists, last)
		}
	})
}

func TestProbe_healthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	if got := probeCode(path); got != 0 {
		t.Errorf("probeCode(marker present) = %d, want 0", got)
	}
}

func TestProbe_unhealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if got := probeCode(path); got != 1 {
		t.Errorf("probeCode(marker absent, writable dir) = %d, want 1", got)
	}
}

func TestProbe_degraded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".healthy")
	if err := probeHealthDir(path); err == nil {
		t.Skip("test environment bypasses directory mode; skipping")
	}
	if got := probeCode(path); got != 0 {
		t.Errorf("probeCode(unwritable dir) = %d, want 0 (degraded)", got)
	}
}

// TestProbe_maxAge covers the opt-in freshness deadline: fresh
// markers pass, stale markers fail with a reason naming both ages, a
// non-positive deadline disables the check, and absence keeps its own
// diagnostic (the deadline never masks it).
func TestProbe_maxAge(t *testing.T) {
	newMarker := func(t *testing.T, age time.Duration) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), ".healthy")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("create marker: %v", err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("backdate marker: %v", err)
		}
		return path
	}

	t.Run("fresh_passes", func(t *testing.T) {
		path := newMarker(t, 0)
		if got := probeCode(path, WithMaxAge(time.Hour)); got != 0 {
			t.Errorf("probeCode(fresh, 1h deadline) = %d, want 0", got)
		}
	})

	t.Run("stale_fails_with_reason", func(t *testing.T) {
		path := newMarker(t, 2*time.Hour)
		code, reason := probeCheck(path, WithMaxAge(time.Hour))
		if code != 1 {
			t.Fatalf("probeCheck(2h old, 1h deadline) = %d, want 1", code)
		}
		if !strings.Contains(reason, "stale") || !strings.Contains(reason, "1h0m0s") {
			t.Errorf("reason = %q, want it to name staleness and the max-age", reason)
		}
	})

	t.Run("non_positive_disables", func(t *testing.T) {
		path := newMarker(t, 24*time.Hour)
		for _, d := range []time.Duration{0, -time.Minute} {
			if got := probeCode(path, WithMaxAge(d)); got != 0 {
				t.Errorf("probeCode(old marker, WithMaxAge(%v)) = %d, want 0 (disabled)", d, got)
			}
		}
	})

	t.Run("absent_keeps_absence_diagnostic", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".healthy")
		code, reason := probeCheck(path, WithMaxAge(time.Hour))
		if code != 1 {
			t.Fatalf("probeCheck(absent, deadline armed) = %d, want 1", code)
		}
		if !strings.Contains(reason, "marker absent") {
			t.Errorf("reason = %q, want the absence diagnostic", reason)
		}
	})
}

// TestProbe_maxAge_boundary property-checks the freshness decision
// around the deadline: strictly younger than max-age passes, strictly
// older fails. Ages are constructed with at least a second of headroom
// on the fresh side so the test's own execution time cannot flip the
// verdict.
func TestProbe_maxAge_boundary(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		maxAgeSec := rapid.IntRange(2, 7200).Draw(rt, "maxAgeSec")
		deltaSec := rapid.IntRange(1, 3600).Draw(rt, "deltaSec")
		stale := rapid.Bool().Draw(rt, "stale")

		ageSec := maxAgeSec + deltaSec
		if !stale {
			ageSec = max(maxAgeSec-deltaSec, 0)
		}

		path := filepath.Join(t.TempDir(), ".healthy")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			rt.Fatalf("create marker: %v", err)
		}
		old := time.Now().Add(-time.Duration(ageSec) * time.Second)
		if err := os.Chtimes(path, old, old); err != nil {
			rt.Fatalf("backdate marker: %v", err)
		}

		got := probeCode(path, WithMaxAge(time.Duration(maxAgeSec)*time.Second))
		want := 0
		if stale {
			want = 1
		}
		if got != want {
			rt.Fatalf("probeCode(age %ds, max-age %ds) = %d, want %d", ageSec, maxAgeSec, got, want)
		}
	})
}

// TestHealthMarker_SetTrue_refreshesMtime pins the writer-side contract
// an armed WithMaxAge deadline reads: every Set(true) refreshes the
// marker's mtime (os.Create's O_TRUNC on the existing file), so
// per-cycle Set(true) calls are heartbeats.
func TestHealthMarker_SetTrue_refreshesMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	m.Set(true)

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}

	m.Set(true) // repeated Set must still refresh mtime

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if age := time.Since(info.ModTime()); age > time.Minute {
		t.Errorf("marker mtime is %v old after Set(true), want refreshed", age)
	}
}

// TestProbe_statFailure_diagnostic pins the unhealthy-reason
// classification: a stat failure that is not ENOENT (here a symlink
// loop in a writable dir) must exit 1 with a diagnostic naming the
// stat error, not the misleading "marker absent".
func TestProbe_statFailure_diagnostic(t *testing.T) {
	loop := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("cannot create self-referencing symlink: %v", err)
	}

	code, reason := probeCheck(loop)
	if code != 1 {
		t.Fatalf("probeCheck(symlink loop) = %d, want 1", code)
	}
	if !strings.Contains(reason, "marker stat failed") {
		t.Errorf("reason = %q, want it to name the stat failure", reason)
	}
	if strings.Contains(reason, "marker absent") {
		t.Errorf("reason = %q, must not claim absence for a non-ENOENT stat failure", reason)
	}
}

// TestHealthMarker_Healthy covers the Signal interface method.
func TestHealthMarker_Healthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	if m.Healthy() {
		t.Error("Healthy() = true before Set(true), want false")
	}

	m.Set(true)
	if !m.Healthy() {
		t.Error("Healthy() = false after Set(true), want true")
	}

	m.Set(false)
	if m.Healthy() {
		t.Error("Healthy() = true after Set(false), want false")
	}

	m.Set(true)
	m.Cleanup()
	if m.Healthy() {
		t.Error("Healthy() = true after Cleanup(), want false")
	}
}

// TestProbeHealthDir_Writable confirms the writability probe succeeds on
// a writable dir and leaves no artifact behind.
func TestProbeHealthDir_Writable(t *testing.T) {
	dir := t.TempDir()
	if err := probeHealthDir(filepath.Join(dir, ".healthy")); err != nil {
		t.Fatalf("probeHealthDir on writable dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probeHealthDir left artifacts behind: %v", entries)
	}
}

// TestProbeHealthDir_NonExistent confirms a missing parent directory is
// reported as an error rather than masked.
func TestProbeHealthDir_NonExistent(t *testing.T) {
	if err := probeHealthDir(filepath.Join(t.TempDir(), "nope", ".healthy")); err == nil {
		t.Fatal("expected error for non-existent parent dir")
	}
}

// TestHealthMarker_HealthyDegraded verifies that in degraded mode
// Healthy reports false (strict os.Stat semantics).
func TestHealthMarker_HealthyDegraded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}

	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)

	if !m.degraded.Load() {
		t.Skip("test environment bypasses directory mode; skipping")
	}

	if m.Healthy() {
		t.Error("Healthy() = true in degraded mode, want false (strict os.Stat)")
	}
	if m.Within(time.Hour).Healthy() || !Inspect(path, WithMaxAge(time.Hour)).Healthy() {
		t.Error("absent marker in degraded mode: want Within unhealthy and the probe healthy")
	}
}

// TestHealthMarker_DegradedWithALeftoverMarker: a marker left in an
// unwritable directory is one Set cannot change, so the in-process signals
// are unhealthy whatever its age, and the probe stays healthy even past its
// lease rather than restart-looping the container.
func TestHealthMarker_DegradedWithALeftoverMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	m := NewMarker(path)
	if !m.degraded.Load() {
		t.Skip("test environment bypasses directory mode; skipping")
	}
	inProcessUnhealthy := func(age string) {
		t.Helper()
		if m.Healthy() || m.Within(time.Hour).Healthy() || m.Within(0).Healthy() {
			t.Errorf("%s leftover in degraded mode: want Healthy and Within unhealthy", age)
		}
		if rec := serve(t, Handler(m)); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s leftover in degraded mode: Handler = %d, want 503", age, rec.Code)
		}
	}
	inProcessUnhealthy("fresh")
	if !Inspect(path).Healthy() || !Inspect(path, WithMaxAge(time.Hour)).Healthy() {
		t.Error("fresh leftover in degraded mode: want the probe healthy")
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	inProcessUnhealthy("stale")
	if got := Inspect(path, WithMaxAge(time.Hour)); got.State != MarkerDirUnavailable || got.Age < time.Hour || got.Err == nil {
		t.Errorf("stale leftover in degraded mode: Inspect = %+v, want MarkerDirUnavailable with its age and the directory error", got)
	}
	if !Inspect(path, WithMaxAge(time.Hour)).Healthy() {
		t.Error("stale leftover in degraded mode: want a healthy probe, not a restart loop")
	}
}

// TestHealthMarker_ConcurrentSetCleanupHealthy exercises concurrent
// Set, Cleanup, and Healthy calls under the race detector.
func TestHealthMarker_ConcurrentSetCleanupHealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() { m.Set(i%2 == 0) })
		wg.Go(func() { m.Cleanup() })
		wg.Go(func() { _ = m.Healthy() })
	}
	wg.Wait()
}

func TestHealthMarker_SetWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}

	m.Set(true) // must not panic when os.Create fails

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat(%q) after failed Set(true): %v", path, err)
	}
	if !info.IsDir() {
		t.Errorf("Set(true) on unwritable marker path created a file; want path unchanged (dir)")
	}
}

func TestHealthMarker_SetRemoveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	m.Set(false) // must not panic when os.Remove fails with non-ErrNotExist

	if _, err := os.Stat(path); err != nil {
		t.Errorf("path should still exist after failed Set(false) removal: %v", err)
	}
}

// captureLogs makes a text handler over a fresh buffer slog's default for the
// test's duration and returns the buffer. Callers must be serial (no
// t.Parallel): the default logger is a process global.
//
// slog.SetDefault also points the standard log package at the installed
// handler, and it skips that redirect when the logger being installed carries
// slog's own default handler. Reinstalling the previous logger therefore does
// not undo the redirect, so the writer and flags are saved and restored
// explicitly. slog goes back first: reinstalling a previous handler that is not
// slog's default re-runs the redirect and would overwrite a log restore done
// before it.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestCaptureLogsRestoresLogGlobals pins the restore in captureLogs: the swap
// redirects the standard log package's writer and zeroes its flags, and the
// cleanup must put both back. Without it, one test silences slog for the rest
// of the package, because slog's own default handler writes through log.Output.
func TestCaptureLogsRestoresLogGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("swap", func(t *testing.T) {
		captureLogs(t)
		if log.Writer() == wantWriter {
			t.Fatal("captureLogs did not redirect log.Writer(); the restore under test would guard nothing")
		}
	})

	if got := log.Writer(); got != wantWriter {
		t.Errorf("log.Writer() after captureLogs cleanup = %#v, want the original %#v", got, wantWriter)
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() after captureLogs cleanup = %d, want %d", got, wantFlags)
	}
}

// TestHealthMarker_SetWriteFailure_warnsOncePerStreak verifies the
// failure-gating contract: under a persistent write failure Set emits
// exactly one Warn, not one per call, so a stuck marker directory does
// not spam the log every tick.
func TestHealthMarker_SetWriteFailure_warnsOncePerStreak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}

	buf := captureLogs(t)

	// Marker path is a directory, so os.Create fails on every call.
	m.Set(true)
	m.Set(true)
	m.Set(true)

	if got := strings.Count(buf.String(), "failed to create health marker"); got != 1 {
		t.Errorf("want exactly 1 write-failure Warn under persistent failure, got %d\nlog:\n%s", got, buf.String())
	}
}

// TestHealthMarker_SetWriteFailure_logsRecoveryAfterStreak verifies that
// once a write finally succeeds after a failure streak, Set creates the
// marker and logs both the change to healthy and the recovery, at Info.
func TestHealthMarker_SetWriteFailure_logsRecoveryAfterStreak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}

	// The marker path is a directory, so this Set(true) fails and flags the streak.
	m.Set(true)

	buf := captureLogs(t)

	// Clear the blocker so the next write succeeds and recovers.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove blocking dir: %v", err)
	}
	m.Set(true)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after recovery Set(true): %v", err)
	}
	logged := buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("INFO healthy=true lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `level=INFO msg="health marker write recovered"`); got != 1 {
		t.Errorf("INFO recovery lines = %d, want 1\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_SetRemoveFailureWarnDedup verifies the remove path
// emits exactly one Warn under a persistent remove failure.
func TestHealthMarker_SetRemoveFailureWarnDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	buf := captureLogs(t)

	// Non-empty directory -> os.Remove fails with non-ErrNotExist.
	m.Set(false)
	m.Set(false)
	m.Set(false)

	if got := strings.Count(buf.String(), "failed to remove health marker"); got != 1 {
		t.Errorf("want exactly 1 remove-failure Warn under persistent failure, got %d\nlog:\n%s", got, buf.String())
	}
}

func TestHealthMarker_CleanupRemoveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction: a marker NewMarker cannot write is
	// degraded mode, and these tests exercise a failure at run time.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	m.Cleanup() // must not panic when os.Remove fails with non-ErrNotExist

	if _, err := os.Stat(path); err != nil {
		t.Errorf("path should still exist after failed Cleanup removal: %v", err)
	}
}

func TestRunProbe_exits(t *testing.T) {
	if os.Getenv("HEALTH_RUNPROBE_CASE") != "" {
		RunProbe(os.Getenv("HEALTH_RUNPROBE_PATH"))
		return
	}

	okPath := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(okPath, nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), ".healthy")

	tests := []struct {
		name     string
		path     string
		wantExit int
	}{
		{"marker_present_exits_0", okPath, 0},
		{"marker_absent_writable_dir_exits_1", badPath, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestRunProbe_exits")
			cmd.Env = append(os.Environ(),
				"HEALTH_RUNPROBE_CASE=1",
				"HEALTH_RUNPROBE_PATH="+tt.path)
			err := cmd.Run()

			if tt.wantExit == 0 {
				if err != nil {
					t.Errorf("RunProbe(%q) exited non-zero: %v", tt.path, err)
				}
				return
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("RunProbe(%q): expected *exec.ExitError, got %v", tt.path, err)
			}
			if ee.ExitCode() != tt.wantExit {
				t.Errorf("RunProbe(%q) exit = %d, want %d", tt.path, ee.ExitCode(), tt.wantExit)
			}
		})
	}
}

// TestHealthMarker_Healthy_nilReceiver pins the documented nil-receiver
// contract: a nil *Marker reports unhealthy rather than panicking, both
// directly and when handed through the Signal interface (a non-nil
// interface wrapping a nil pointer).
func TestHealthMarker_Healthy_nilReceiver(t *testing.T) {
	var m *Marker

	if m.Healthy() {
		t.Error("(*Marker)(nil).Healthy() = true, want false")
	}

	var s Signal = m
	if s.Healthy() {
		t.Error("Signal backed by nil *Marker: Healthy() = true, want false")
	}
}

// TestRunProbe_unhealthy_emitsDiagnostic asserts the operator diagnostic on the
// unhealthy path: RunProbe prints "unhealthy: marker absent" to stderr before
// exiting 1. TestRunProbe_exits covers the exit code but not this message.
func TestRunProbe_unhealthy_emitsDiagnostic(t *testing.T) {
	if os.Getenv("HEALTH_DIAG_CASE") != "" {
		RunProbe(os.Getenv("HEALTH_DIAG_PATH"))
		return
	}
	badPath := filepath.Join(t.TempDir(), ".healthy")

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunProbe_unhealthy_emitsDiagnostic$")
	cmd.Env = append(os.Environ(),
		"HEALTH_DIAG_CASE=1",
		"HEALTH_DIAG_PATH="+badPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("RunProbe(absent marker): expected *exec.ExitError, got %v", err)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("RunProbe(absent marker) exit = %d, want 1", ee.ExitCode())
	}
	if !strings.Contains(stderr.String(), "unhealthy: marker absent") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "unhealthy: marker absent")
	}
}

// TestHealthMarker_Set_transitionLoggingSilentOnRepeat pins the edge-transition
// logging contract: repeated Set calls with the same value stay silent. A
// mutant that logs on every call (changed := true) passes every other test.
func TestHealthMarker_Set_transitionLoggingSilentOnRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	buf := captureLogs(t)

	m.Set(true)  // transition false->true: one INFO
	m.Set(true)  // repeat: silent
	m.Set(true)  // repeat: silent
	m.Set(false) // transition true->false: one WARN
	m.Set(false) // repeat: silent

	logged := buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("Set(true) edge-transition INFO logs = %d, want exactly 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `level=WARN msg="health state changed" healthy=false`); got != 1 {
		t.Errorf("Set(false) edge-transition WARN logs = %d, want exactly 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `msg="health state changed"`); got != 2 {
		t.Errorf("health state changed lines = %d, want 2\nlog:\n%s", got, logged)
	}
}

// writeExternalMarker writes a marker at path as another process would, and
// confirms the probe side reads it as healthy.
func writeExternalMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write external marker: %v", err)
	}
	if code := probeCode(path); code != 0 {
		t.Fatalf("the probe on an external marker = %d, want 0", code)
	}
}

// TestHealthMarker_Set_inheritedMarkerFalseWarns pins the starting state as
// the file on disk: a marker another process left reports healthy, so the
// first Set(false) that removes it is a real change for every observer.
func TestHealthMarker_Set_inheritedMarkerFalseWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	writeExternalMarker(t, path)
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	if !m.Healthy() {
		t.Fatal("Healthy on an inherited marker = false, want true")
	}

	buf := captureLogs(t)

	m.Set(false)

	logged := buf.String()
	if m.Healthy() {
		t.Error("Healthy after Set(false) = true, want false")
	}
	if got := strings.Count(logged, `level=WARN msg="health state changed" healthy=false`); got != 1 {
		t.Errorf("WARN healthy=false lines = %d, want 1\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_Set_inheritedMarkerTrueIsSilent pins the shared-marker
// pattern: a separate process writing healthy over a marker that already
// reports healthy changes nothing, so it logs no state change.
func TestHealthMarker_Set_inheritedMarkerTrueIsSilent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	writeExternalMarker(t, path)
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	buf := captureLogs(t)

	m.Set(true)

	logged := buf.String()
	if got := strings.Count(logged, `msg="health state changed"`); got != 0 {
		t.Errorf("health state changed lines = %d, want 0\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_Cleanup_thenSetTrueLogsChange pins the boot that clears an
// inherited marker: Cleanup removes it without a log line, and the later
// Set(true) logs the change to healthy, as a clean boot does.
func TestHealthMarker_Cleanup_thenSetTrueLogsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	writeExternalMarker(t, path)
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	buf := captureLogs(t)

	m.Cleanup()
	m.Set(true)

	logged := buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("INFO healthy=true lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if strings.Contains(logged, "level=WARN") {
		t.Errorf("Cleanup then Set(true) logged a WARN\nlog:\n%s", logged)
	}
}

// TestHealthMarker_Set_firstSetFalseIsSilent pins the initial state: with no
// marker on disk a new marker is already unhealthy, so a first Set(false)
// changes nothing and logs nothing.
func TestHealthMarker_Set_firstSetFalseIsSilent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	buf := captureLogs(t)

	m.Set(false)
	m.Set(false)

	logged := buf.String()
	if got := strings.Count(logged, `msg="health state changed"`); got != 0 {
		t.Errorf("health state changed lines after Set(false) on a new marker = %d, want 0\nlog:\n%s", got, logged)
	}
	if strings.Contains(logged, "level=WARN") {
		t.Errorf("Set(false) on a new marker logged a WARN\nlog:\n%s", logged)
	}
}

// TestHealthMarker_Set_bootSequenceWarnsNothing pins the distroless boot
// pattern, Set(false) at startup then Set(true) once ready: one Info line for
// the change to healthy, and no WARN.
func TestHealthMarker_Set_bootSequenceWarnsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	buf := captureLogs(t)

	m.Set(false)
	m.Set(true)

	logged := buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("INFO healthy=true lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `msg="health state changed"`); got != 1 {
		t.Errorf("health state changed lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if strings.Contains(logged, "level=WARN") {
		t.Errorf("boot sequence logged a WARN\nlog:\n%s", logged)
	}
}

// TestHealthMarker_Set_externallyCreatedMarkerFalseWarns pins the baseline as
// the file at the call: a separate process creates the marker after NewMarker,
// so the probe reports healthy, and the Set(false) removing it is a real change.
func TestHealthMarker_Set_externallyCreatedMarkerFalseWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	writeExternalMarker(t, path)

	buf := captureLogs(t)

	m.Set(false)

	logged := buf.String()
	if code := probeCode(path); code != 1 {
		t.Errorf("the probe after Set(false) = %d, want 1", code)
	}
	if got := strings.Count(logged, `level=WARN msg="health state changed" healthy=false`); got != 1 {
		t.Errorf("WARN healthy=false lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `msg="health state changed"`); got != 1 {
		t.Errorf("health state changed lines = %d, want 1\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_Set_externallyRemovedMarkerTrueLogsChange is the reverse:
// a separate process removes the marker NewMarker found, so the probe reports
// unhealthy, and the Set(true) recreating it is a real change to healthy.
func TestHealthMarker_Set_externallyRemovedMarkerTrueLogsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	writeExternalMarker(t, path)
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove marker externally: %v", err)
	}
	if code := probeCode(path); code != 1 {
		t.Fatalf("the probe after external remove = %d, want 1", code)
	}

	buf := captureLogs(t)

	m.Set(true)

	logged := buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("INFO healthy=true lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `msg="health state changed"`); got != 1 {
		t.Errorf("health state changed lines = %d, want 1\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_SetChecked_unreadableMarkerIsAFailure pins a marker whose
// state cannot be read (a symlink loop: the probe reports a stat failure, not
// absence): SetChecked fails, logs the stat error once and no state change,
// and leaves the marker as it is; once it is readable the next write recovers.
func TestHealthMarker_SetChecked_unreadableMarkerIsAFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	if err := os.Symlink(path, path); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}
	if got := Inspect(path).State; got != MarkerUnreadable {
		t.Fatalf("Inspect on a symlink loop = %v, want unreadable", got)
	}

	buf := captureLogs(t)

	errTrue := m.SetChecked(true)
	errFalse := m.SetChecked(false)

	logged := buf.String()
	if errTrue == nil || errFalse == nil {
		t.Errorf("SetChecked on an unreadable marker = (%v, %v), want two errors", errTrue, errFalse)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("unreadable marker should be left in place: %v", err)
	}
	if got := strings.Count(logged, `level=WARN msg="failed to stat health marker"`); got != 1 {
		t.Errorf("WARN stat-failure lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `msg="health state changed"`); got != 0 {
		t.Errorf("health state changed lines on an unreadable marker = %d, want 0\nlog:\n%s", got, logged)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove symlink loop: %v", err)
	}
	buf.Reset()
	if err := m.SetChecked(true); err != nil {
		t.Fatalf("SetChecked(true) once readable: %v", err)
	}
	logged = buf.String()
	if got := strings.Count(logged, `level=INFO msg="health state changed" healthy=true`); got != 1 {
		t.Errorf("INFO healthy=true lines after recovery = %d, want 1\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `level=INFO msg="health marker write recovered"`); got != 1 {
		t.Errorf("INFO recovery lines = %d, want 1\nlog:\n%s", got, logged)
	}
}

// TestNewMarker_degraded_logsHintOnce pins the folder branch's WARN: NewMarker
// on an unwritable dir emits exactly one, with its full message, the effect
// and the compose-fix hint. It runs only as a non-root user.
func TestNewMarker_degraded_logsHintOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}

	buf := captureLogs(t)

	m := NewMarker(filepath.Join(dir, ".healthy"))
	if !m.degraded.Load() {
		t.Skip("test environment bypasses directory mode; skipping")
	}

	logged := buf.String()
	if got := strings.Count(logged, `msg="health marker directory not writable, marker in degraded mode"`); got != 1 {
		t.Errorf("degraded-construction WARN count = %d, want exactly 1\nlog:\n%s", got, logged)
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("degraded construction must log at WARN; log:\n%s", logged)
	}
	if !strings.Contains(logged, degradedEffect) {
		t.Errorf("degraded WARN must name both verdicts; log:\n%s", logged)
	}
	if !strings.Contains(logged, "hint=") || !strings.Contains(logged, "tmpfs") {
		t.Errorf("degraded WARN must carry a compose-fix hint mentioning tmpfs; log:\n%s", logged)
	}
}

// TestSet_leavesDegradedModeOnceTheMarkerPathClears: degraded mode lasts only
// while Set cannot write, because the probe decides it from the filesystem on
// every run. Once what blocked the marker path is gone, the next Set writes
// the marker and logs one Info, and Healthy, Within (a signal built while
// degraded included) and Inspect all read the marker. A directory at the
// marker path blocks it for any user, root included.
func TestSet_leavesDegradedModeOnceTheMarkerPathClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	m := NewMarker(path, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	if !m.degraded.Load() {
		t.Fatal("a directory at the marker path: want degraded mode")
	}
	within := m.Within(time.Hour)
	m.Set(true)
	if !m.degraded.Load() || m.Healthy() || within.Healthy() {
		t.Fatal("Set while the directory is still there: want degraded, and Healthy and Within unhealthy")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.SetChecked(true); err != nil {
		t.Fatalf("SetChecked(true) once the path cleared = %v, want nil", err)
	}
	if m.degraded.Load() {
		t.Error("once the path cleared: want degraded mode left")
	}
	if !m.Healthy() || !within.Healthy() {
		t.Error("once the path cleared: want Healthy and the Within signal built while degraded both healthy")
	}
	if got := Inspect(path, WithMaxAge(time.Hour)); got.State != MarkerFresh {
		t.Errorf("once the path cleared: Inspect = %+v, want MarkerFresh", got)
	}
	m.Set(true)
	if got := strings.Count(buf.String(), `msg="health marker writable again, leaving degraded mode"`); got != 1 {
		t.Errorf("leaving degraded mode logged %d times, want 1\nlog:\n%s", got, buf.String())
	}
}

// TestSet_leavesDegradedModeOnceTheFolderExists is the folder branch of the
// same rule: a folder that cannot be written at start (here, one that does
// not exist yet; in production, a mode fixed by hand) becomes writable, and
// the next Set writes the marker. A missing folder blocks it for any user,
// root included.
func TestSet_leavesDegradedModeOnceTheFolderExists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path, WithLogger(slog.New(slog.DiscardHandler)))
	if !m.degraded.Load() {
		t.Fatal("a marker folder that does not exist: want degraded mode")
	}
	m.Set(true)
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Set with no folder: stat = %v, want no marker", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m.Set(true)
	if m.degraded.Load() || !m.Healthy() {
		t.Error("once the folder exists: want degraded mode left and the marker written")
	}
	if got := Inspect(path); got.State != MarkerFresh {
		t.Errorf("once the folder exists: Inspect = %+v, want MarkerFresh", got)
	}
}

// TestSet_leavingDegradedModeIsOneWay: once a marker has left degraded mode,
// a folder that cannot be written again is a write failure like any other,
// returned, not a second entry into degraded mode. Removing the folder blocks
// it for any user, root included.
func TestSet_leavingDegradedModeIsOneWay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path, WithLogger(slog.New(slog.DiscardHandler)))
	if !m.degraded.Load() {
		t.Fatal("a marker folder that does not exist: want degraded mode")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.SetChecked(true); err != nil || m.degraded.Load() {
		t.Fatalf("once the folder exists: SetChecked(true) = %v, degraded %v; want nil and left", err, m.degraded.Load())
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := m.SetChecked(true); err == nil {
		t.Error("SetChecked(true) with the folder gone = nil, want the write error")
	}
	if m.degraded.Load() {
		t.Error("after the failed write: want degraded mode not re-entered")
	}
}

// TestSet_leavingDegradedModeRacesNoReader: Healthy and a Within signal read
// degraded mode without the lock while Set clears it, so the race detector
// must see no data race between them.
func TestSet_leavingDegradedModeRacesNoReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewMarker(path, WithLogger(slog.New(slog.DiscardHandler)))
	within := m.Within(0)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = m.Healthy()
					_ = within.Healthy()
				}
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.Set(true)
	close(done)
	wg.Wait()
	if !m.Healthy() {
		t.Error("after leaving degraded mode: want Healthy")
	}
}

// TestNewMarker_degradedWarningNamesBothVerdicts pins that the warning states
// what degraded mode reports through each path, since the container's verdict
// depends on which one its healthcheck uses: RunProbe reports healthy, and an
// HTTP endpoint over the marker reports unhealthy.
func TestNewMarker_degradedWarningNamesBothVerdicts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = NewMarker(path, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	logged := buf.String()
	for _, want := range []string{
		`msg="health marker not writable, marker in degraded mode"`,
		`effect="RunProbe and Inspect report healthy; Healthy, Within and an HTTP endpoint over this marker (Handler, CheckHandler) report unhealthy"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("degraded-mode warning = %q, want it to carry %s", logged, want)
		}
	}
}

// TestNewMarker_degradedOnAnUnwritableMarker: a marker Set cannot write, here a
// read-only one in a writable directory (in production, one left in a shared
// /tmp by an earlier run as another user), is degraded mode like an
// unwritable directory, so a stale one cannot restart-loop the container.
func TestNewMarker_degradedOnAnUnwritableMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	// Decide the skip from the environment, never from the code under test:
	// root opens a 0444 file for writing.
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Skip("test environment bypasses file mode; skipping")
	}
	buf := captureLogs(t)
	m := NewMarker(path)
	if !m.degraded.Load() {
		t.Fatal("a marker this process cannot open for writing: want degraded mode")
	}
	logged := buf.String()
	if strings.Count(logged, "health marker not writable") != 1 || !strings.Contains(logged, "another user") || !strings.Contains(logged, "permission denied") {
		t.Errorf("want one WARN naming the leftover marker; log:\n%s", logged)
	}
	if m.Healthy() || m.SetChecked(true) != nil {
		t.Error("an unwritable marker: want Healthy false and SetChecked a nil no-op")
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := Inspect(path, WithMaxAge(time.Hour)); got.State != MarkerDirUnavailable || got.Err == nil || got.Age < time.Hour {
		t.Errorf("stale unwritable marker: Inspect = %+v, want MarkerDirUnavailable with its age and the open error", got)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Inspect(path, WithMaxAge(time.Hour)).State; got != MarkerStale {
		t.Errorf("stale marker the app can write: Inspect = %v, want MarkerStale", got)
	}
}

// TestMarkerWritableLeavesTheMarkerAlone: the check opens an existing marker
// without truncating it or changing its time, and an absent one is writable.
func TestMarkerWritableLeavesTheMarkerAlone(t *testing.T) {
	dir := t.TempDir()
	if err := markerWritable(filepath.Join(dir, "absent")); err != nil {
		t.Errorf("absent marker: %v, want nil", err)
	}
	path := filepath.Join(dir, ".healthy")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := markerWritable(path); err != nil {
		t.Fatalf("writable marker: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(old) || info.Size() != 1 {
		t.Errorf("after the check: %v, mtime %v size %d; want %v and the content kept", err, info.ModTime(), info.Size(), old)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if markerWritable(blocked) == nil || !NewMarker(blocked).degraded.Load() {
		t.Error("a directory at the marker path: want an error and a degraded marker, for root too")
	}
}

// TestSetRefreshesAWriteOnlyMarker: Set(true) opens an existing marker
// write-only, without O_CREATE, so a marker this process may write but not
// read (mode 0222) is refreshed rather than refused, and is not degraded.
// os.Create's O_RDWR needed read permission and failed every Set here.
func TestSetRefreshesAWriteOnlyMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o222); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Fatal("a marker this process can write: want no degraded mode")
	}
	if err := m.SetChecked(true); err != nil {
		t.Fatalf("SetChecked(true) on a 0222 marker: %v", err)
	}
	if got := Inspect(path, WithMaxAge(time.Hour)); got.State != MarkerFresh {
		t.Errorf("after Set(true): Inspect = %+v, want MarkerFresh (the mtime refreshed)", got)
	}
}

// TestDegradedMarkerMeansAHealthyProbe: whatever is at the marker path, a
// marker NewMarker calls degraded is one the probe reports healthy, even
// past its lease, so the warning's effect ("RunProbe and Inspect report
// healthy") is true.
func TestDegradedMarkerMeansAHealthyProbe(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	for name, setup := range map[string]func(path string) error{
		"directory": func(path string) error { return os.Mkdir(path, 0o755) },
		"read-only": func(path string) error { return os.WriteFile(path, nil, 0o444) },
		"symlink loop": func(path string) error {
			return os.Symlink(path, path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".healthy")
			if err := setup(path); err != nil {
				t.Skip(err)
			}
			_ = os.Chtimes(path, old, old) // fails on the loop, which stat cannot read
			m := NewMarker(path, WithLogger(slog.New(slog.DiscardHandler)))
			f := Inspect(path, WithMaxAge(time.Hour))
			if m.degraded.Load() && !f.Healthy() {
				t.Errorf("NewMarker is degraded but Inspect = %+v, so the probe would restart the container", f)
			}
			if name == "symlink loop" && (m.degraded.Load() || f.State != MarkerUnreadable) {
				t.Errorf("symlink loop: degraded %v, Inspect %v; want not degraded and MarkerUnreadable, as the probe sees it", m.degraded.Load(), f.State)
			}
		})
	}
}

// TestHealthMarker_Set_recoveryOnFalseBranchIsNotAStateChange pins the
// Set(false) recovery (the Set(true) twin is
// TestHealthMarker_SetWriteFailure_logsRecoveryAfterStreak): the probe reads
// the marker as absent throughout, so the successful write logs one Info
// recovery line and no state change.
func TestHealthMarker_Set_recoveryOnFalseBranchIsNotAStateChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")

	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	m.Set(false)

	// A dangling symlink into a missing directory: the probe reads it as
	// absent, Set(true) fails to create its target, and removing it succeeds.
	if err := os.Symlink(filepath.Join(dir, "missing", "marker"), path); err != nil {
		t.Fatalf("dangling symlink: %v", err)
	}
	if got := Inspect(path).State; got != MarkerAbsent {
		t.Fatalf("Inspect on a dangling symlink = %v, want absent", got)
	}
	if err := m.SetChecked(true); err == nil {
		t.Fatal("SetChecked(true) through a dangling symlink = nil, want an error")
	}

	buf := captureLogs(t)

	m.Set(false)

	logged := buf.String()
	if got := strings.Count(logged, `msg="health state changed"`); got != 0 {
		t.Errorf("health state changed lines on an unchanged state = %d, want 0\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, `level=INFO msg="health marker write recovered"`); got != 1 {
		t.Errorf("INFO recovery lines = %d, want 1\nlog:\n%s", got, logged)
	}
	if strings.Contains(logged, "level=WARN") {
		t.Errorf("recovery logged a WARN\nlog:\n%s", logged)
	}
}

// TestHealthMarker_SetFailureWarnDedupAlternatingBranches pins per-signature
// de-dup across alternating failing branches in one streak: the marker path
// is a non-empty directory, so both Set(true) and Set(false) fail, and the
// de-dup must key on the (message, error) set rather than re-logging on toggle.
func TestHealthMarker_SetFailureWarnDedupAlternatingBranches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction (see TestHealthMarker_SetWriteFailure).
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	buf := captureLogs(t)

	m.Set(true)
	m.Set(false)
	m.Set(true)
	m.Set(false)

	logged := buf.String()
	if got := strings.Count(logged, "failed to create health marker"); got != 1 {
		t.Errorf("want exactly 1 create-failure Warn in one streak, got %d\nlog:\n%s", got, logged)
	}
	if got := strings.Count(logged, "failed to remove health marker"); got != 1 {
		t.Errorf("want exactly 1 remove-failure Warn in one streak, got %d\nlog:\n%s", got, logged)
	}
}

// TestHealthMarker_SetChecked pins the checked variant's return contract:
// nil on a successful touch/remove, the underlying error on a filesystem
// failure, and nil again once the blocker clears (recovery), with the file
// state matching Set's semantics throughout.
func TestHealthMarker_SetChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}

	if err := m.SetChecked(true); err != nil {
		t.Fatalf("SetChecked(true) on writable dir = %v, want nil", err)
	}
	if !m.Healthy() {
		t.Fatal("marker should exist after successful SetChecked(true)")
	}
	if err := m.SetChecked(false); err != nil {
		t.Fatalf("SetChecked(false) = %v, want nil", err)
	}

	// Block the write: the marker path becomes a directory, so os.Create fails.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := m.SetChecked(true); err == nil {
		t.Fatal("SetChecked(true) on blocked path = nil, want the create error")
	}

	// Clear the blocker: the next call succeeds and reports nil.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove blocking dir: %v", err)
	}
	if err := m.SetChecked(true); err != nil {
		t.Fatalf("SetChecked(true) after recovery = %v, want nil", err)
	}
}

// TestHealthMarker_SetChecked_removeFailure covers the remove branch: a
// non-empty directory at the marker path fails os.Remove with a
// non-ErrNotExist error, which SetChecked must surface.
func TestHealthMarker_SetChecked_removeFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if m.degraded.Load() {
		t.Skip("parent dir not writable in this environment; skipping")
	}
	// Block the path after construction (see TestHealthMarker_SetWriteFailure).
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir marker-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	if err := m.SetChecked(false); err == nil {
		t.Fatal("SetChecked(false) on non-empty dir = nil, want the remove error")
	}
}

// TestHealthMarker_SetChecked_degradedReturnsNil pins the degraded-mode
// contract: the marker channel is deliberately inert, so SetChecked reports
// nil rather than handing callers an error that would turn a compose
// misconfiguration into a restart/alert loop.
func TestHealthMarker_SetChecked_degradedReturnsNil(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}
	path := filepath.Join(dir, ".healthy")
	m := NewMarker(path)
	if !m.degraded.Load() {
		t.Skip("test environment bypasses directory mode; skipping")
	}

	if err := m.SetChecked(true); err != nil {
		t.Errorf("SetChecked(true) in degraded mode = %v, want nil", err)
	}
	if err := m.SetChecked(false); err != nil {
		t.Errorf("SetChecked(false) in degraded mode = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("degraded SetChecked should never create the file: %v", err)
	}
}

// markerAged writes a marker backdated by age and returns its path.
func markerAged(t *testing.T, age time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".healthy")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}
	return path
}

// TestInspect_states covers what Inspect exposes: the pair the exit code
// cannot distinguish (stale = wedged loop, a restart may help; absent =
// cold start or wiped volume, a restart changes nothing), plus age.
func TestInspect_states(t *testing.T) {
	t.Run("fresh within an armed deadline", func(t *testing.T) {
		got := Inspect(markerAged(t, time.Second), WithMaxAge(time.Hour))
		if got.State != MarkerFresh {
			t.Errorf("state = %v, want MarkerFresh", got.State)
		}
		if !got.Healthy() {
			t.Error("a fresh marker must be Healthy")
		}
		if got.Reason() != "" {
			t.Errorf("reason = %q, want empty for a healthy reading", got.Reason())
		}
		if got.Age <= 0 || got.Age > time.Minute {
			t.Errorf("age = %s, want a small positive age", got.Age)
		}
		if got.MaxAge != time.Hour {
			t.Errorf("maxAge = %s, want the armed hour carried through", got.MaxAge)
		}
	})

	t.Run("present but past an armed deadline is stale, not absent", func(t *testing.T) {
		got := Inspect(markerAged(t, 2*time.Hour), WithMaxAge(time.Hour))
		if got.State != MarkerStale {
			t.Fatalf("state = %v, want MarkerStale", got.State)
		}
		if got.Healthy() {
			t.Error("a stale marker must not be Healthy")
		}
		if got.Age < 2*time.Hour {
			t.Errorf("age = %s, want at least the 2h backdate", got.Age)
		}
		if !strings.Contains(got.Reason(), "marker stale") {
			t.Errorf("reason = %q, want it to name staleness", got.Reason())
		}
	})

	t.Run("present with no deadline armed is always fresh", func(t *testing.T) {
		got := Inspect(markerAged(t, 100*time.Hour))
		if got.State != MarkerFresh {
			t.Errorf("state = %v, want MarkerFresh with no deadline armed", got.State)
		}
		if got.MaxAge != 0 {
			t.Errorf("maxAge = %s, want 0 when unarmed", got.MaxAge)
		}
	})

	t.Run("missing from a usable directory is absent", func(t *testing.T) {
		got := Inspect(filepath.Join(t.TempDir(), ".healthy"), WithMaxAge(time.Hour))
		if got.State != MarkerAbsent {
			t.Fatalf("state = %v, want MarkerAbsent", got.State)
		}
		if got.Healthy() {
			t.Error("an absent marker in a usable directory must not be Healthy")
		}
		if got.Age != 0 {
			t.Errorf("age = %s, want 0", got.Age)
		}
		if got.Reason() != "unhealthy: marker absent" {
			t.Errorf("reason = %q, want the absence diagnostic", got.Reason())
		}
	})

	t.Run("a stat failure that is not absence is unreadable", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), ".healthy")
		if err := os.Symlink(loop, loop); err != nil {
			t.Skipf("cannot create self-referencing symlink: %v", err)
		}
		got := Inspect(loop)
		if got.State != MarkerUnreadable {
			t.Fatalf("state = %v, want MarkerUnreadable", got.State)
		}
		if got.Err == nil {
			t.Error("an unreadable marker must carry the underlying stat error")
		}
		if strings.Contains(got.Reason(), "marker absent") {
			t.Errorf("reason = %q, must not claim absence for a non-ENOENT failure", got.Reason())
		}
	})

	t.Run("an unusable directory is degraded and stays healthy", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(dir, 0o500); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, ".healthy")
		if err := probeHealthDir(path); err == nil {
			t.Skip("test environment bypasses directory mode; skipping")
		}
		got := Inspect(path)
		if got.State != MarkerDirUnavailable {
			t.Fatalf("state = %v, want MarkerDirUnavailable", got.State)
		}
		if !got.Healthy() {
			t.Error("degraded mode must stay Healthy: silence is not evidence of ill health")
		}
		if got.Reason() != "" {
			t.Errorf("reason = %q, want empty", got.Reason())
		}
	})
}

// TestInspectAndProbeCannotDisagree pins that the exit code a container
// healthcheck sees and the structured Freshness a caller acts on are two
// views of one decision; a divergence would let an app restart itself on a
// verdict its own healthcheck disagrees with.
func TestInspectAndProbeCannotDisagree(t *testing.T) {
	loop := filepath.Join(t.TempDir(), "loop")
	_ = os.Symlink(loop, loop)

	cases := map[string]struct {
		path string
		opts []ProbeOption
	}{
		"fresh, unarmed":   {path: markerAged(t, time.Second)},
		"fresh, armed":     {path: markerAged(t, time.Second), opts: []ProbeOption{WithMaxAge(time.Hour)}},
		"stale":            {path: markerAged(t, 2*time.Hour), opts: []ProbeOption{WithMaxAge(time.Hour)}},
		"aged but unarmed": {path: markerAged(t, 100*time.Hour)},
		"absent":           {path: filepath.Join(t.TempDir(), ".healthy")},
		"unreadable":       {path: loop},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := Inspect(tc.path, tc.opts...)
			code, reason := probeCheck(tc.path, tc.opts...)
			wantCode := 1
			if f.Healthy() {
				wantCode = 0
			}
			if code != wantCode {
				t.Errorf("probeCheck = %d but Inspect says Healthy()=%v (state %v)", code, f.Healthy(), f.State)
			}
			if reason != f.Reason() {
				t.Errorf("probeCheck reason = %q, Inspect.Reason() = %q; the two presentations must carry the same text", reason, f.Reason())
			}
			if got := probeCode(tc.path, tc.opts...); got != code {
				t.Errorf("probeCode = %d, probeCheck = %d", got, code)
			}
		})
	}
}

// TestMarkerStateString pins the log vocabulary: these strings land in operator
// log lines as a `marker_state` attribute, so a rename is a consumer-visible
// change rather than a cosmetic one.
func TestMarkerStateString(t *testing.T) {
	want := map[MarkerState]string{
		MarkerFresh:          "fresh",
		MarkerStale:          "stale",
		MarkerAbsent:         "absent",
		MarkerUnreadable:     "unreadable",
		MarkerDirUnavailable: "dir-unavailable",
		MarkerState(99):      "unknown",
	}
	for state, str := range want {
		t.Run(str, func(t *testing.T) {
			if got := state.String(); got != str {
				t.Errorf("MarkerState(%d).String() = %q, want %q", int(state), got, str)
			}
		})
	}
}

// Healthy follows Set on a writable marker, and a nil marker reads unhealthy.
func TestHealthyFollowsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker")
	m := NewMarker(path)
	for _, set := range []bool{true, false, true} {
		m.Set(set)
		if got := m.Healthy(); got != set {
			t.Errorf("after Set(%v) on %s: Healthy() = %v, want %v", set, path, got, set)
		}
	}
	var nilM *Marker
	if nilM.Healthy() {
		t.Error("nil marker: Healthy() = true, want false")
	}
}

// The zero Freshness is not a healthy reading, and Reason never panics on a
// caller-built unreadable state with no error.
func TestFreshnessZeroValueIsUnknown(t *testing.T) {
	t.Parallel()
	var f Freshness
	if f.Healthy() || f.State != MarkerUnknown || f.State.String() != "unknown" {
		t.Errorf("zero Freshness = %+v (healthy %v), want MarkerUnknown and unhealthy", f, f.Healthy())
	}
	if f.Reason() == "" {
		t.Error("zero Freshness has an empty Reason, which reads as healthy")
	}
	if got := (Freshness{State: MarkerUnreadable}).Reason(); got != "unhealthy: marker stat failed" {
		t.Errorf("Reason(unreadable, nil Err) = %q", got)
	}
}

// WithLogger routes a Marker's lines to the given logger.
func TestNewMarker_WithLogger(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil)).With("svc", "x")
	path := filepath.Join(t.TempDir(), "marker")
	m := NewMarker(path, WithLogger(l))
	m.Set(true)
	if out := buf.String(); !strings.Contains(out, "health state changed") || !strings.Contains(out, "svc=x") {
		t.Errorf("logger got %q, want the attributed state change", out)
	}
}

// probeCode is the probe's exit code for path, the decision RunProbe exits
// with, without calling os.Exit.
func probeCode(path string, opts ...ProbeOption) int {
	code, _ := probeCheck(path, opts...)
	return code
}

// A stale reading's reason prints the age rounded up to the millisecond and
// the max-age as armed, so a stale marker never reads as two equal durations
// failing.
func TestFreshness_ReasonStaleShowsAgeAboveMaxAge(t *testing.T) {
	for name, tc := range map[string]struct {
		age, maxAge time.Duration
		want        string
	}{
		"sub-second":                  {1700 * time.Millisecond, 1500 * time.Millisecond, "unhealthy: marker stale: 1.7s old exceeds max-age 1.5s"},
		"sub-second over whole hours": {13*time.Hour + 400*time.Millisecond, 13 * time.Hour, "unhealthy: marker stale: 13h0m0.4s old exceeds max-age 13h0m0s"},
		"one nanosecond over":         {13*time.Hour + time.Nanosecond, 13 * time.Hour, "unhealthy: marker stale: 13h0m0.001s old exceeds max-age 13h0m0s"},
		"rounds up, not to nearest":   {2*time.Hour + 30*time.Minute + 1234321*time.Microsecond, 2 * time.Hour, "unhealthy: marker stale: 2h30m1.235s old exceeds max-age 2h0m0s"},
		"sub-millisecond max-age":     {700 * time.Microsecond, 500 * time.Microsecond, "unhealthy: marker stale: 1ms old exceeds max-age 500\u00b5s"},
		// time.Since clamps a marker older than about 292 years to
		// maxDuration, whose last partial millisecond has no whole one above it.
		"saturated age does not wrap":      {maxDuration, time.Hour, "unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 1h0m0s"},
		"last partial millisecond":         {maxDuration - maxDuration%time.Millisecond + 1, time.Hour, "unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 1h0m0s"},
		"saturated age, saturated max-age": {maxDuration, maxDuration - 1, "unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 2562047h47m16.854775806s"},
	} {
		t.Run(name, func(t *testing.T) {
			got := Freshness{State: MarkerStale, Age: tc.age, MaxAge: tc.maxAge}.Reason()
			if got != tc.want {
				t.Errorf("Reason(age %s, max-age %s) = %q, want %q", tc.age, tc.maxAge, got, tc.want)
			}
		})
	}
}
