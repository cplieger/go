package health

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"
)

// markerAged writes a marker backdated by age and returns its path.
func markerAged(t *testing.T, age time.Duration) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "app.healthy")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}
	return p
}

// overlongName is a marker path whose last element is longer than any
// filesystem allows, so Lstat fails with ENAMETOOLONG in a writable folder,
// for any user, root included.
func overlongName(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), strings.Repeat("a", 300))
}

func TestProbe_healthy(t *testing.T) {
	t.Parallel()
	if got := probeCode(markerAged(t, 0)); got != 0 {
		t.Errorf("probeCode(marker present) = %d, want 0", got)
	}
}

func TestProbe_unhealthy(t *testing.T) {
	t.Parallel()
	if got := probeCode(filepath.Join(t.TempDir(), "app.healthy")); got != 1 {
		t.Errorf("probeCode(marker absent, writable folder) = %d, want 1", got)
	}
}

func TestProbe_degraded(t *testing.T) {
	t.Parallel()
	if got := probeCode(filepath.Join(t.TempDir(), "absent", "app.healthy")); got != 0 {
		t.Errorf("probeCode(missing folder) = %d, want 0 (degraded)", got)
	}
}

// TestProbe_maxAge covers the opt-in freshness deadline: fresh markers pass,
// stale ones fail with a reason naming the max-age, a non-positive deadline
// disables the check, and absence keeps its own reason.
func TestProbe_maxAge(t *testing.T) {
	t.Parallel()
	t.Run("fresh_passes", func(t *testing.T) {
		t.Parallel()
		if got := probeCode(markerAged(t, 0), WithMaxAge(time.Hour)); got != 0 {
			t.Errorf("probeCode(fresh, 1h deadline) = %d, want 0", got)
		}
	})
	t.Run("stale_fails_with_reason", func(t *testing.T) {
		t.Parallel()
		code, reason := probeCheck(markerAged(t, 2*time.Hour), WithMaxAge(time.Hour))
		if code != 1 {
			t.Fatalf("probeCheck(2h old, 1h deadline) = %d, want 1", code)
		}
		if !strings.Contains(reason, "stale") || !strings.Contains(reason, "1h0m0s") {
			t.Errorf("reason = %q, want it to name staleness and the max-age", reason)
		}
	})
	t.Run("non_positive_disables", func(t *testing.T) {
		t.Parallel()
		p := markerAged(t, 24*time.Hour)
		for _, d := range []time.Duration{0, -time.Minute} {
			if got := probeCode(p, WithMaxAge(d)); got != 0 {
				t.Errorf("probeCode(old marker, WithMaxAge(%v)) = %d, want 0 (disabled)", d, got)
			}
		}
	})
	t.Run("absent_keeps_absence_reason", func(t *testing.T) {
		t.Parallel()
		code, reason := probeCheck(filepath.Join(t.TempDir(), "app.healthy"), WithMaxAge(time.Hour))
		if code != 1 || reason != "unhealthy: marker absent" {
			t.Errorf("probeCheck(absent, deadline armed) = %d %q, want 1 and the absence reason", code, reason)
		}
	})
}

// TestProbe_maxAge_boundary property-checks the deadline: younger than
// max-age passes, older fails. The fresh side keeps a second of headroom so
// the test's own run time cannot flip the verdict.
func TestProbe_maxAge_boundary(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxAgeSec := rapid.IntRange(2, 7200).Draw(rt, "maxAgeSec")
		deltaSec := rapid.IntRange(1, 3600).Draw(rt, "deltaSec")
		isStale := rapid.Bool().Draw(rt, "stale")
		ageSec := maxAgeSec + deltaSec
		if !isStale {
			ageSec = max(maxAgeSec-deltaSec, 0)
		}
		got := probeCode(markerAged(t, time.Duration(ageSec)*time.Second), WithMaxAge(time.Duration(maxAgeSec)*time.Second))
		want := 0
		if isStale {
			want = 1
		}
		if got != want {
			rt.Fatalf("probeCode(age %ds, max-age %ds) = %d, want %d", ageSec, maxAgeSec, got, want)
		}
	})
}

// TestProbe_statFailure_reason: a stat failure that is not absence exits 1
// with a reason naming the stat error, never "marker absent".
func TestProbe_statFailure_reason(t *testing.T) {
	t.Parallel()
	code, reason := probeCheck(overlongName(t))
	if code != 1 {
		t.Fatalf("probeCheck(overlong name) = %d, want 1", code)
	}
	if !strings.HasPrefix(reason, "unhealthy: marker stat failed: ") || !strings.Contains(reason, "file name too long") {
		t.Errorf("reason = %q, want the stat failure with its error", reason)
	}
}

// TestFreshAndRunProbeAgree pins that Fresh and the probe read one age from
// one healthy Set, at the deadline's edges and for any lease.
func TestFreshAndRunProbeAgree(t *testing.T) {
	c := useClock(t)
	p := filepath.Join(t.TempDir(), "app.healthy")
	st := NewStatus(WithLogger(discard), WithMarker(p))
	st.Set(true)
	if err := os.Chtimes(p, epoch, epoch); err != nil {
		t.Fatal(err)
	}
	t.Run("at the deadline's edges", func(t *testing.T) {
		const d = time.Minute
		for _, age := range []time.Duration{0, d - 1, d, d + 1, 3 * d} {
			c.set(epoch.Add(age))
			fresh, probe := st.Fresh(d).Healthy(), probeCode(p, WithMaxAge(d)) == 0
			if want := age <= d; fresh != want || probe != want {
				t.Errorf("age %v, lease %v: Fresh = %v, probe healthy = %v, want both %v", age, d, fresh, probe, want)
			}
		}
	})
	t.Run("for any lease and age", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			lease := time.Duration(rapid.Int64Range(-int64(time.Hour), int64(24*time.Hour)).Draw(rt, "lease"))
			age := time.Duration(rapid.Int64Range(-int64(time.Hour), int64(48*time.Hour)).Draw(rt, "age"))
			c.set(epoch.Add(age))
			fresh, probe := st.Fresh(lease).Healthy(), probeCode(p, WithMaxAge(lease)) == 0
			if want := lease <= 0 || age <= lease; fresh != want || probe != want {
				rt.Fatalf("age %v, lease %v: Fresh = %v, probe healthy = %v, want both %v", age, lease, fresh, probe, want)
			}
		})
	})
	st.Set(false)
	if probeCode(p) != 1 || st.Healthy() {
		t.Errorf("after Set(false): probe = %d, Healthy() = %v; want 1 and false", probeCode(p), st.Healthy())
	}
}

func TestRunProbeExits(t *testing.T) {
	if os.Getenv("HEALTH_RUNPROBE_CASE") != "" {
		RunProbe(os.Getenv("HEALTH_RUNPROBE_PATH"))
		return
	}
	t.Parallel()
	written := filepath.Join(t.TempDir(), "app.healthy")
	NewStatus(WithLogger(discard), WithMarker(written)).Set(true)
	for name, tc := range map[string]struct {
		path     string
		wantExit int
	}{
		"a marker a Status wrote exits 0":        {written, 0},
		"no marker in a writable folder exits 1": {filepath.Join(t.TempDir(), "app.healthy"), 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRunProbeExits$")
			cmd.Env = append(os.Environ(), "HEALTH_RUNPROBE_CASE=1", "HEALTH_RUNPROBE_PATH="+tc.path)
			err := cmd.Run()
			code := 0
			if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("RunProbe(%q): %v", tc.path, err)
			}
			if code != tc.wantExit {
				t.Errorf("RunProbe(%q) exit = %d, want %d", tc.path, code, tc.wantExit)
			}
		})
	}
}

// TestRunProbe_unhealthy_writesTheReason: RunProbe writes the reason to
// stderr before it exits 1.
func TestRunProbe_unhealthy_writesTheReason(t *testing.T) {
	if os.Getenv("HEALTH_DIAG_CASE") != "" {
		RunProbe(os.Getenv("HEALTH_DIAG_PATH"))
		return
	}
	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunProbe_unhealthy_writesTheReason$")
	cmd.Env = append(os.Environ(), "HEALTH_DIAG_CASE=1", "HEALTH_DIAG_PATH="+filepath.Join(t.TempDir(), "app.healthy"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("RunProbe(absent marker) = %v, want exit 1", err)
	}
	if !strings.Contains(stderr.String(), "unhealthy: marker absent") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "unhealthy: marker absent")
	}
}

func TestInspect_states(t *testing.T) {
	t.Parallel()
	t.Run("fresh within an armed deadline", func(t *testing.T) {
		t.Parallel()
		got := inspect(markerAged(t, time.Second), WithMaxAge(time.Hour))
		if got.state != markerFresh || !got.healthy() || got.reason() != "" {
			t.Errorf("inspect = %+v (healthy %v, reason %q), want markerFresh, healthy, no reason", got, got.healthy(), got.reason())
		}
	})
	t.Run("present but past an armed deadline is stale, not absent", func(t *testing.T) {
		t.Parallel()
		got := inspect(markerAged(t, 2*time.Hour), WithMaxAge(time.Hour))
		if got.state != markerStale || got.healthy() || got.age < 2*time.Hour || !strings.Contains(got.reason(), "marker stale") {
			t.Errorf("inspect = %+v (healthy %v, reason %q), want markerStale, unhealthy, aged at least 2h", got, got.healthy(), got.reason())
		}
	})
	t.Run("present with no deadline armed is always fresh", func(t *testing.T) {
		t.Parallel()
		if got := inspect(markerAged(t, 100*time.Hour)); got.state != markerFresh {
			t.Errorf("inspect = %+v, want markerFresh with no deadline armed", got)
		}
	})
	t.Run("missing from a usable folder is absent", func(t *testing.T) {
		t.Parallel()
		got := inspect(filepath.Join(t.TempDir(), "app.healthy"), WithMaxAge(time.Hour))
		if got.state != markerAbsent || got.healthy() || got.reason() != "unhealthy: marker absent" {
			t.Errorf("inspect = %+v (reason %q), want markerAbsent with the absence reason", got, got.reason())
		}
	})
	t.Run("a stat failure that is not absence is unreadable", func(t *testing.T) {
		t.Parallel()
		got := inspect(overlongName(t))
		if got.state != markerUnreadable || got.err == nil || got.healthy() {
			t.Errorf("inspect = %+v, want markerUnreadable carrying the stat error", got)
		}
	})
	t.Run("a symlink loop is degraded and stays healthy", func(t *testing.T) {
		t.Parallel()
		loop := filepath.Join(t.TempDir(), "app.healthy")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		if got := inspect(loop); got.state != markerDegraded || !got.healthy() || got.reason() != "" {
			t.Errorf("inspect = %+v, want markerDegraded, healthy, no reason", got)
		}
	})
	t.Run("a folder that cannot be written is degraded and stays healthy", func(t *testing.T) {
		t.Parallel()
		got := inspect(filepath.Join(t.TempDir(), "absent", "app.healthy"))
		if got.state != markerDegraded || !got.healthy() || got.reason() != "" {
			t.Errorf("inspect = %+v, want markerDegraded, healthy, no reason", got)
		}
	})
	t.Run("the zero reading is unknown and unhealthy", func(t *testing.T) {
		t.Parallel()
		var f freshness
		if f.healthy() || f.reason() != "unhealthy: marker state unknown" {
			t.Errorf("zero freshness: healthy %v, reason %q; want unhealthy and %q", f.healthy(), f.reason(), "unhealthy: marker state unknown")
		}
	})
}

// TestInspectAndProbeCannotDisagree pins that the exit code and the reason
// RunProbe writes are two views of one reading.
func TestInspectAndProbeCannotDisagree(t *testing.T) {
	t.Parallel()
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	aged := func(age time.Duration) func(*testing.T) string {
		return func(t *testing.T) string { return markerAged(t, age) }
	}
	for name, tc := range map[string]struct {
		marker     func(*testing.T) string
		wantReason string
		opts       []ProbeOption
		wantExit   int
	}{
		"fresh, unarmed": {marker: aged(time.Second)},
		"fresh, armed":   {marker: aged(time.Second), opts: []ProbeOption{WithMaxAge(time.Hour)}},
		// Under half a millisecond past a whole one, so only rounding up
		// prints 0.002s; truncating or rounding to nearest prints 0.001s.
		"stale": {
			marker: aged(2*time.Hour + 1200*time.Microsecond), opts: []ProbeOption{WithMaxAge(time.Hour)}, wantExit: 1,
			wantReason: "unhealthy: marker stale: 2h0m0.002s old exceeds max-age 1h0m0s",
		},
		"aged but unarmed": {marker: aged(100 * time.Hour)},
		"absent": {
			marker: func(t *testing.T) string { return filepath.Join(t.TempDir(), "app.healthy") }, wantExit: 1,
			wantReason: "unhealthy: marker absent",
		},
		"unreadable": {marker: overlongName, wantExit: 1},
		"symlink":    {marker: func(*testing.T) string { return loop }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The stale reason prints the age, so both readings must share one
			// instant: the bubble's clock stands still while nothing blocks, and
			// the marker is backdated from that clock.
			synctest.Test(t, func(t *testing.T) {
				path := tc.marker(t)
				f := inspect(path, tc.opts...)
				code, reason := probeCheck(path, tc.opts...)
				if code != tc.wantExit || f.healthy() != (tc.wantExit == 0) {
					t.Errorf("probeCheck = %d, inspect healthy = %v (state %v); want exit %d from both", code, f.healthy(), f.state, tc.wantExit)
				}
				if reason != f.reason() {
					t.Errorf("probeCheck reason = %q, inspect reason = %q; want the same text", reason, f.reason())
				}
				if tc.wantReason != "" && reason != tc.wantReason {
					t.Errorf("reason = %q, want %q", reason, tc.wantReason)
				}
			})
		})
	}
}

// A stale reading's reason prints the age rounded up to the millisecond and
// the max-age as armed, so a stale marker never reads as two equal durations.
func TestFreshness_ReasonStaleShowsAgeAboveMaxAge(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		want        string
		age, maxAge time.Duration
	}{
		"sub-second":                  {"unhealthy: marker stale: 1.7s old exceeds max-age 1.5s", 1700 * time.Millisecond, 1500 * time.Millisecond},
		"sub-second over whole hours": {"unhealthy: marker stale: 13h0m0.4s old exceeds max-age 13h0m0s", 13*time.Hour + 400*time.Millisecond, 13 * time.Hour},
		"one nanosecond over":         {"unhealthy: marker stale: 13h0m0.001s old exceeds max-age 13h0m0s", 13*time.Hour + time.Nanosecond, 13 * time.Hour},
		"rounds up, not to nearest":   {"unhealthy: marker stale: 2h30m1.235s old exceeds max-age 2h0m0s", 2*time.Hour + 30*time.Minute + 1234321*time.Microsecond, 2 * time.Hour},
		"sub-millisecond max-age":     {"unhealthy: marker stale: 1ms old exceeds max-age 500\u00b5s", 700 * time.Microsecond, 500 * time.Microsecond},
		// time.Time.Sub clamps an age past about 292 years to maxDuration,
		// whose last partial millisecond has no whole one above it.
		"saturated age does not wrap":      {"unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 1h0m0s", maxDuration, time.Hour},
		"last partial millisecond":         {"unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 1h0m0s", maxDuration - maxDuration%time.Millisecond + 1, time.Hour},
		"saturated age, saturated max-age": {"unhealthy: marker stale: 2562047h47m16.854775807s old exceeds max-age 2562047h47m16.854775806s", maxDuration, maxDuration - 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := (freshness{state: markerStale, age: tc.age, maxAge: tc.maxAge}).reason(); got != tc.want {
				t.Errorf("reason(age %s, max-age %s) = %q, want %q", tc.age, tc.maxAge, got, tc.want)
			}
		})
	}
}
