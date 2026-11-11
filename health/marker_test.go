package health

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

const (
	wantDegradedEffect = "RunProbe reports healthy while the marker cannot be written; the in-memory verdict and every HTTP endpoint over it report the real verdict"
	wantDirHint        = "create the marker's folder or make it writable, for example with a tmpfs mount; /dev/shm is one in a container unless IPC is disabled. The next Set applies its value, so restart a service that sets it only at startup"
	wantMarkerHint     = "remove what is at the marker path, such as a file another user created, a symlink, a directory or a FIFO; the next Set applies its value, so restart a service that sets it only at startup"
	wantWriteEffect    = "the health subcommand may disagree with the in-memory verdict until a marker write succeeds"

	msgDirDegraded    = "health marker directory not writable, marker in degraded mode"
	msgMarkerDegraded = "health marker not writable, marker in degraded mode"
	msgLeftDegraded   = "health marker writable again, leaving degraded mode"
	msgCreateFailed   = "failed to create health marker"
	msgRemoveFailed   = "failed to remove health marker"
	msgRecovered      = "health marker write recovered"
	msgChanged        = "health state changed"
)

func TestNewStatusClearsALeftoverMarker(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, buf := jsonLog()
	st := NewStatus(WithLogger(l), WithMarker(p))
	if exists(t, p) {
		t.Error("a marker an earlier run left is still there after NewStatus, want it removed")
	}
	if st.mirror.degraded || buf.Len() != 0 {
		t.Errorf("degraded = %v, log %q; want neither", st.mirror.degraded, buf.String())
	}
}

func TestSetMirrorsTheVerdict(t *testing.T) {
	t.Parallel()
	t.Run("each write", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "app.healthy")
		st := NewStatus(WithLogger(discard), WithMarker(p))
		for i, step := range []struct {
			act  func()
			want bool
		}{
			{func() { st.Set(true) }, true},
			{func() { st.Set(false) }, false},
			{func() { st.Set(true) }, true},
			{st.BeginDrain, false},
		} {
			step.act()
			if got := exists(t, p); got != step.want {
				t.Errorf("step %d: marker present = %v, want %v", i, got, step.want)
			}
		}
	})
	t.Run("a repeated value logs nothing more", func(t *testing.T) {
		t.Parallel()
		l, buf := jsonLog()
		p := filepath.Join(t.TempDir(), "app.healthy")
		st := NewStatus(WithLogger(l), WithMarker(p))
		st.Set(false)
		st.Set(false)
		if buf.Len() != 0 {
			t.Errorf("Set(false) twice with no marker logged %q, want nothing", buf.String())
		}
		st.Set(true)
		st.Set(true)
		if got := records(t, buf); len(got) != 1 || got[0]["msg"] != msgChanged {
			t.Errorf("Set(true) twice logged %v, want one %q line", got, msgChanged)
		}
	})
}

func TestEveryHealthySetRefreshesTheMarker(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "app.healthy")
	st := NewStatus(WithLogger(discard), WithMarker(p))
	st.Set(true)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	st.Set(true)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old.Add(30 * time.Minute)) {
		t.Errorf("modification time after a repeated Set(true) = %v, want refreshed past %v", info.ModTime(), old)
	}
}

func TestMarkerFollowsEverySequenceOfSets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var n int
	rapid.Check(t, func(rt *rapid.T) {
		n++
		p := filepath.Join(dir, fmt.Sprintf("app-%d.healthy", n))
		st := NewStatus(WithLogger(discard), WithMarker(p))
		calls := rapid.SliceOfN(rapid.Bool(), 1, 30).Draw(rt, "calls")
		for _, ok := range calls {
			st.Set(ok)
		}
		last := calls[len(calls)-1]
		_, err := os.Stat(p)
		if present := err == nil; present != last || st.Healthy() != last {
			rt.Fatalf("after a last Set(%v): marker present = %v, Healthy() = %v", last, present, st.Healthy())
		}
	})
}

func TestMirrorFailureWarnsOncePerStreak(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	p := markerIn(t)
	st := NewStatus(WithLogger(l), WithMarker(p))
	blockFolder(t, p)
	for _, ok := range []bool{true, true, false, true, false, false} {
		st.Set(ok)
		if st.Healthy() != ok {
			t.Errorf("Healthy() after a failed Set(%v) = %v, want the value Set gave", ok, st.Healthy())
		}
	}
	for _, msg := range []string{msgCreateFailed, msgRemoveFailed} {
		got := withMsg(t, buf, msg)
		if len(got) != 1 {
			t.Errorf("%q lines = %d, want 1 per streak: %q", msg, len(got), buf.String())
			continue
		}
		r := got[0]
		if r["level"] != "WARN" || r["path"] != p || r["effect"] != wantWriteEffect || !strings.Contains(fmt.Sprint(r["error"]), "not a directory") {
			t.Errorf("%q line = %v, want WARN with path, the ENOTDIR error and effect %q", msg, r, wantWriteEffect)
		}
	}
}

func TestMirrorFailureRecoveryLogsInfo(t *testing.T) {
	t.Parallel()
	t.Run("on Set(true)", func(t *testing.T) {
		t.Parallel()
		l, buf := jsonLog()
		p := markerIn(t)
		st := NewStatus(WithLogger(l), WithMarker(p))
		blockFolder(t, p)
		st.Set(true)
		unblockFolder(t, p)
		buf.Reset()
		st.Set(true)
		if !exists(t, p) {
			t.Error("marker absent after the write recovered")
		}
		got := records(t, buf)
		if len(got) != 1 || got[0]["msg"] != msgRecovered || got[0]["level"] != "INFO" || got[0]["healthy"] != true {
			t.Errorf("logged %v, want one INFO %q healthy=true and no transition (it logged with the failed write)", got, msgRecovered)
		}
	})
	t.Run("on Set(false)", func(t *testing.T) {
		t.Parallel()
		l, buf := jsonLog()
		p := markerIn(t)
		st := NewStatus(WithLogger(l), WithMarker(p))
		st.Set(false)
		blockFolder(t, p)
		st.Set(false)
		unblockFolder(t, p)
		buf.Reset()
		st.Set(false)
		got := records(t, buf)
		if len(got) != 1 || got[0]["msg"] != msgRecovered || got[0]["level"] != "INFO" || got[0]["healthy"] != false {
			t.Errorf("logged %v, want one INFO %q healthy=false and no state change", got, msgRecovered)
		}
	})
}

func TestDegradedStartOnAMissingFolder(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	p := filepath.Join(t.TempDir(), "absent", "app.healthy")
	st := NewStatus(WithLogger(l), WithMarker(p))
	got := withMsg(t, buf, msgDirDegraded)
	if len(got) != 1 {
		t.Fatalf("%q lines = %d, want 1: %q", msgDirDegraded, len(got), buf.String())
	}
	if r := got[0]; r["level"] != "WARN" || r["dir"] != filepath.Dir(p) || r["effect"] != wantDegradedEffect || r["hint"] != wantDirHint {
		t.Errorf("degraded line = %v, want WARN with dir %q, effect %q and hint %q", r, filepath.Dir(p), wantDegradedEffect, wantDirHint)
	}
	buf.Reset()
	st.Set(true)
	if !st.Healthy() {
		t.Error("Healthy() after Set(true) in degraded mode = false, want true")
	}
	st.Set(false)
	st.Set(true)
	for _, r := range records(t, buf) {
		if r["msg"] != msgChanged {
			t.Errorf("later Sets logged %v, want only transition lines", r)
		}
	}
	if len(withMsg(t, buf, msgChanged)) != 3 {
		t.Errorf("transition lines = %q, want 3", buf.String())
	}
	if got := probeCode(p); got != 0 {
		t.Errorf("probe on a missing folder = %d, want 0 (degraded)", got)
	}
}

func TestDegradedStartOnADirectoryAtTheMarkerPath(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	st := NewStatus(WithLogger(l), WithMarker(p))
	got := withMsg(t, buf, msgMarkerDegraded)
	if len(got) != 1 {
		t.Fatalf("%q lines = %d, want 1: %q", msgMarkerDegraded, len(got), buf.String())
	}
	if r := got[0]; r["level"] != "WARN" || r["path"] != p || r["effect"] != wantDegradedEffect || r["hint"] != wantMarkerHint {
		t.Errorf("degraded line = %v, want WARN with path %q, effect %q and hint %q", r, p, wantDegradedEffect, wantMarkerHint)
	}
	if got := probeCode(p); got != 0 {
		t.Errorf("probe on a directory at the marker path = %d, want 0 (degraded)", got)
	}
	for _, ok := range []bool{true, false, true} {
		st.Set(ok)
		if st.Healthy() != ok {
			t.Errorf("Healthy() after Set(%v) = %v", ok, st.Healthy())
		}
	}
}

func TestDegradedModeRecoversOnTheFirstWritableSet(t *testing.T) {
	c := useClock(t)
	l, buf := jsonLog()
	p := markerIn(t)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	st := NewStatus(WithLogger(l), WithMarker(p))
	st.Set(true)
	if info, err := os.Lstat(p); err != nil || !info.IsDir() {
		t.Fatalf("a Set while degraded touched the path: %v, %v", info, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	c.advance(7 * time.Second)
	st.Set(true)
	got := withMsg(t, buf, msgLeftDegraded)
	if len(got) != 1 || got[0]["level"] != "INFO" || got[0]["path"] != p || got[0]["duration"] != float64(7*time.Second) {
		t.Errorf("%q lines = %v, want one INFO with path and duration 7s", msgLeftDegraded, got)
	}
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		t.Errorf("after leaving degraded mode: %v, %v; want a regular marker", info, err)
	}
	if got := probeCode(p); got != 0 {
		t.Errorf("probe after leaving degraded mode on Set(true) = %d, want 0", got)
	}
	st.Set(false)
	if exists(t, p) || probeCode(p) != 1 {
		t.Errorf("after Set(false): marker present = %v, probe = %d; want absent and 1", exists(t, p), probeCode(p))
	}
	blockFolder(t, p)
	st.Set(true)
	if n := len(withMsg(t, buf, msgCreateFailed)); n != 1 {
		t.Errorf("%q lines once the folder is a file = %d, want 1", msgCreateFailed, n)
	}
	if n := len(withMsg(t, buf, msgDirDegraded)) + len(withMsg(t, buf, msgMarkerDegraded)); n != 1 {
		t.Errorf("degraded warnings = %d, want the one from NewStatus: leaving is one-way", n)
	}
}

func TestDegradedSetOnceNeedsARestart(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	dir := filepath.Join(t.TempDir(), "absent")
	p := filepath.Join(dir, "app.healthy")
	st := NewStatus(WithLogger(l), WithMarker(p))
	st.Set(true)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	code, reason := probeCheck(p)
	if code != 1 || reason != "unhealthy: marker absent" {
		t.Errorf("probe once the folder exists and no Set ran = %d %q, want 1 %q", code, reason, "unhealthy: marker absent")
	}
	if exists(t, p) {
		t.Error("marker present with no Set since the folder appeared")
	}
	if n := len(withMsg(t, buf, msgLeftDegraded)); n != 0 {
		t.Errorf("%q lines = %d with no Set since the folder appeared, want 0", msgLeftDegraded, n)
	}
}

func TestDegradedRecoveryOnAFalseSetRemovesNothing(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	st := NewStatus(WithLogger(l), WithMarker(p))
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	st.Set(false)
	st.Set(false)
	if exists(t, p) {
		t.Error("marker present after leaving degraded mode on Set(false)")
	}
	if n := len(withMsg(t, buf, msgLeftDegraded)); n != 1 {
		t.Errorf("%q lines = %d, want 1", msgLeftDegraded, n)
	}
}

// TestDegradedStartKeepsALeftoverInAReadOnlyFolder: a marker an earlier run
// left in a folder that cannot be written stays, and the probe reports it
// healthy at any age rather than restarting a container a restart cannot fix.
func TestDegradedStartKeepsALeftoverInAReadOnlyFolder(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "app.healthy")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if probeHealthDir(p) == nil {
		t.Skip("this user writes a 0500 folder (root); the missing-folder tests cover degraded mode")
	}
	st := NewStatus(WithLogger(discard), WithMarker(p))
	if !st.mirror.degraded || !exists(t, p) {
		t.Fatalf("degraded = %v, leftover present = %v; want both", st.mirror.degraded, exists(t, p))
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if got := inspect(p, WithMaxAge(time.Hour)); got.state != markerDegraded {
		t.Errorf("stale leftover in a read-only folder: inspect = %+v, want markerDegraded", got)
	}
	if st.Healthy() {
		t.Error("Healthy() with a leftover marker and no Set = true, want the in-memory verdict, false")
	}
}

// TestDegradedStartOnAMarkerThisUserCannotOpen: a marker the service cannot
// open for writing (here 0444; in production another user's file) is degraded
// mode, and once it can be written a stale one reads as stale again.
func TestDegradedStartOnAMarkerThisUserCannotOpen(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.WriteFile(p, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Skip("this user opens a 0444 file for writing (root); the directory tests cover degraded mode")
	}
	l, buf := jsonLog()
	st := NewStatus(WithLogger(l), WithMarker(p))
	got := withMsg(t, buf, msgMarkerDegraded)
	if !st.mirror.degraded || len(got) != 1 || !strings.Contains(fmt.Sprint(got[0]["error"]), "permission denied") {
		t.Fatalf("degraded = %v, lines %v; want one %q naming permission denied", st.mirror.degraded, got, msgMarkerDegraded)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if got := inspect(p, WithMaxAge(time.Hour)).state; got != markerDegraded {
		t.Errorf("stale marker this user cannot open: inspect = %v, want markerDegraded", got)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := inspect(p, WithMaxAge(time.Hour)).state; got != markerStale {
		t.Errorf("stale marker this user can write: inspect = %v, want markerStale", got)
	}
}

// TestDegradedMarkerMeansAHealthyProbe: whatever is at the marker path, when
// NewStatus is degraded the probe reports healthy, even past its lease, so the
// warning's effect is true.
func TestDegradedMarkerMeansAHealthyProbe(t *testing.T) {
	t.Parallel()
	old := time.Now().Add(-2 * time.Hour)
	for name, setup := range map[string]func(path string) error{
		"directory":    func(path string) error { return os.Mkdir(path, 0o755) },
		"read-only":    func(path string) error { return os.WriteFile(path, nil, 0o444) },
		"symlink loop": func(path string) error { return os.Symlink(path, path) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), "app.healthy")
			if err := setup(p); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(p, old, old) // fails on the loop, which nothing follows
			st := NewStatus(WithLogger(discard), WithMarker(p))
			f := inspect(p, WithMaxAge(time.Hour))
			if st.mirror.degraded && !f.healthy() {
				t.Errorf("NewStatus is degraded but inspect = %+v, so the probe would restart the container", f)
			}
			if name == "symlink loop" && (!st.mirror.degraded || f.state != markerDegraded) {
				t.Errorf("symlink loop: degraded %v, inspect %v; want degraded and markerDegraded", st.mirror.degraded, f.state)
			}
		})
	}
}

func TestProbeHealthDir(t *testing.T) {
	t.Parallel()
	t.Run("a writable folder, leaving nothing behind", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := probeHealthDir(filepath.Join(dir, "app.healthy")); err != nil {
			t.Fatalf("probeHealthDir on a writable folder: %v", err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("probeHealthDir left %v (%v), want nothing", entries, err)
		}
	})
	t.Run("a missing folder", func(t *testing.T) {
		t.Parallel()
		if probeHealthDir(filepath.Join(t.TempDir(), "absent", "app.healthy")) == nil {
			t.Error("probeHealthDir on a missing folder = nil, want an error")
		}
	})
}

// TestMarkerWritableLeavesTheMarkerAlone: the check opens an existing marker
// without truncating it or changing its time, and an absent one is writable.
func TestMarkerWritableLeavesTheMarkerAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := markerWritable(filepath.Join(dir, "absent")); err != nil {
		t.Errorf("absent marker: %v, want nil", err)
	}
	p := filepath.Join(dir, "app.healthy")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := markerWritable(p); err != nil {
		t.Fatalf("writable marker: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil || !info.ModTime().Equal(old) || info.Size() != 1 {
		t.Errorf("after the check: %v, mtime %v size %d; want %v and the content kept", err, info.ModTime(), info.Size(), old)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if markerWritable(blocked) == nil {
		t.Error("a directory at the marker path: want an error, for root too")
	}
}

// TestWriteRefreshesAWriteOnlyMarker: writeMarker opens an existing marker
// write-only, so a marker this process may write but not read (0222) is
// refreshed rather than refused, and markerWritable accepts it.
func TestWriteRefreshesAWriteOnlyMarker(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.WriteFile(p, nil, 0o222); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := markerWritable(p); err != nil {
		t.Fatalf("markerWritable on a 0222 marker: %v", err)
	}
	if err := writeMarker(p); err != nil {
		t.Fatalf("writeMarker on a 0222 marker: %v", err)
	}
	if got := inspect(p, WithMaxAge(time.Hour)).state; got != markerFresh {
		t.Errorf("after writeMarker: inspect = %v, want markerFresh (the time refreshed)", got)
	}
}

func TestMarkerPath(t *testing.T) {
	t.Parallel()
	for app, want := range map[string]string{
		"app":    "/dev/shm/app.healthy",
		"my.app": "/dev/shm/my.app.healthy",
	} {
		t.Run(app, func(t *testing.T) {
			t.Parallel()
			if got := MarkerPath(app); got != want {
				t.Errorf("MarkerPath(%q) = %q, want %q", app, got, want)
			}
		})
	}
	for name, app := range map[string]string{
		"empty": "", "hidden": ".hidden", "dot": ".", "dot dot": "..",
		"inner slash": "a/b", "absolute": "/abs", "trailing slash": "a/", "NUL": "a\x00b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("MarkerPath(%q) returned, want a panic", app)
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, fmt.Sprintf("%q", app)) {
					t.Errorf("panic %q does not name the argument %q", msg, app)
				}
			}()
			MarkerPath(app)
		})
	}
}

func TestNewStatusPanicsOnAnEmptyMarkerPath(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("NewStatus(WithMarker(\"\")) returned, want a panic")
		}
	}()
	NewStatus(WithMarker(""))
}
