package health

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// clock is a settable stand-in for now. Tests that use it swap a package
// global, so they never call t.Parallel.
type clock struct {
	t  time.Time
	mu sync.Mutex
}

var epoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func useClock(t *testing.T) *clock {
	t.Helper()
	c := &clock{t: epoch}
	prev := now
	now = c.now
	t.Cleanup(func() { now = prev })
	return c
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// jsonLog returns a logger writing JSON lines to the returned buffer.
func jsonLog() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// records decodes every JSON line in buf.
func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

var discard = slog.New(slog.DiscardHandler)

func TestNewStatusStartsUnhealthy(t *testing.T) {
	t.Parallel()
	if NewStatus().Healthy() {
		t.Error("NewStatus().Healthy() = true, want false")
	}
	var zero Status
	if zero.Healthy() {
		t.Error("Status{}.Healthy() = true, want false")
	}
}

func TestSetRecordsTheVerdict(t *testing.T) {
	t.Parallel()
	st := NewStatus(WithLogger(discard))
	st.Set(true)
	if !st.Healthy() {
		t.Error("Healthy() after Set(true) = false, want true")
	}
	st.Set(false)
	if st.Healthy() {
		t.Error("Healthy() after Set(false) = true, want false")
	}
}

func TestZeroStatusIsUsable(t *testing.T) {
	buf := captureLogs(t)
	var st Status
	st.Set(true)
	if !st.Healthy() {
		t.Error("zero Status: Healthy() after Set(true) = false, want true")
	}
	if !strings.Contains(buf.String(), "health state changed") {
		t.Errorf("zero Status logged %q, want the transition line on slog.Default", buf.String())
	}
}

func TestNilStatus(t *testing.T) {
	t.Parallel()
	var st *Status
	if st.Healthy() {
		t.Error("(*Status)(nil).Healthy() = true, want false")
	}
	if !st.Fresh(time.Minute).Healthy() {
		t.Error("(*Status)(nil).Fresh(time.Minute).Healthy() = false, want true: an unhealthy verdict passes Fresh")
	}
}

func TestSignalFunc(t *testing.T) {
	t.Parallel()
	if SignalFunc(nil).Healthy() {
		t.Error("SignalFunc(nil).Healthy() = true, want false")
	}
	if !SignalFunc(func() bool { return true }).Healthy() {
		t.Error("SignalFunc(true).Healthy() = false, want true")
	}
	if SignalFunc(func() bool { return false }).Healthy() {
		t.Error("SignalFunc(false).Healthy() = true, want false")
	}
	h := CheckHandler(Check{Signal: SignalFunc(nil), Reason: "nil func"})
	if rec := serve(t, h); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("CheckHandler over a nil SignalFunc = %d, want 503", rec.Code)
	}
}

func TestSetLogsEachChangeOnce(t *testing.T) {
	t.Parallel()
	l, buf := jsonLog()
	st := NewStatus(WithLogger(l))
	st.Set(false)
	if buf.Len() != 0 {
		t.Fatalf("a first Set(false) logged %q, want nothing: the Status starts unhealthy", buf.String())
	}
	st.Set(true)
	st.Set(true)
	st.Set(false)
	st.Set(false)
	got := records(t, buf)
	if len(got) != 2 {
		t.Fatalf("logged %d lines, want 2 (one per change): %q", len(got), buf.String())
	}
	for i, want := range []struct {
		level   string
		healthy bool
	}{{"INFO", true}, {"WARN", false}} {
		r := got[i]
		if r["msg"] != "health state changed" || r["level"] != want.level || r["healthy"] != want.healthy {
			t.Errorf("line %d = %v, want %s \"health state changed\" healthy=%v", i, r, want.level, want.healthy)
		}
	}
}

func TestWithLogger(t *testing.T) {
	t.Run("the last of two wins and a nil option is skipped", func(t *testing.T) {
		first, firstBuf := jsonLog()
		last, lastBuf := jsonLog()
		st := NewStatus(WithLogger(first), nil, WithLogger(last))
		st.Set(true)
		if firstBuf.Len() != 0 || len(records(t, lastBuf)) != 1 {
			t.Errorf("first logger got %q, last got %q; want only the last to log", firstBuf.String(), lastBuf.String())
		}
	})
	t.Run("a nil logger means slog.Default at each line", func(t *testing.T) {
		st := NewStatus(WithLogger(nil))
		buf := captureLogs(t)
		st.Set(true)
		if !strings.Contains(buf.String(), "health state changed") {
			t.Errorf("slog.Default got %q, want the transition line", buf.String())
		}
	})
}

func TestHealthySetAfterDrainIsDropped(t *testing.T) {
	t.Parallel()
	st := NewStatus(WithLogger(discard))
	st.Set(true)
	st.BeginDrain()
	if st.Healthy() {
		t.Fatal("Healthy() right after BeginDrain = true, want false")
	}
	st.Set(true)
	if st.Healthy() {
		t.Error("Set(true) after BeginDrain restored the verdict, want it dropped")
	}
}

func TestBeginDrainLogsOnceWithDraining(t *testing.T) {
	t.Parallel()
	t.Run("from healthy", func(t *testing.T) {
		t.Parallel()
		l, buf := jsonLog()
		st := NewStatus(WithLogger(l))
		st.Set(true)
		buf.Reset()
		st.BeginDrain()
		st.BeginDrain()
		st.Set(true)
		got := records(t, buf)
		if len(got) != 1 {
			t.Fatalf("logged %d lines, want 1: %q", len(got), buf.String())
		}
		r := got[0]
		if r["level"] != "WARN" || r["msg"] != "health state changed" || r["healthy"] != false || r["draining"] != true {
			t.Errorf("drain line = %v, want WARN \"health state changed\" healthy=false draining=true", r)
		}
	})
	t.Run("from unhealthy", func(t *testing.T) {
		t.Parallel()
		l, buf := jsonLog()
		st := NewStatus(WithLogger(l))
		st.BeginDrain()
		if buf.Len() != 0 {
			t.Errorf("BeginDrain on an unhealthy Status logged %q, want nothing", buf.String())
		}
	})
}

func TestBeginDrainWinsARacingHealthySet(t *testing.T) {
	t.Parallel()
	for i := range 200 {
		st := NewStatus(WithLogger(discard))
		st.Set(true)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
						st.Set(true)
					}
				}
			})
		}
		st.BeginDrain()
		for range 50 {
			if st.Healthy() {
				close(stop)
				wg.Wait()
				t.Fatalf("iteration %d: Healthy() = true while healthy writers race a finished BeginDrain", i)
			}
		}
		close(stop)
		wg.Wait()
		if st.Healthy() {
			t.Fatalf("iteration %d: Healthy() = true after the racing writers stopped", i)
		}
	}
}

// reasons serves h once and returns the status code and the body's reasons.
func reasons(t *testing.T, h http.Handler) (int, []string) {
	t.Helper()
	rec := serve(t, h)
	var b body
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, b.Reasons
}

type freshStep struct {
	act       func()
	name      string
	reasons   []string
	wantCode  int
	advanceBy time.Duration
}

// runFreshSteps runs dependent steps in order without t.Run, which a
// synctest bubble refuses; each failure names its step.
func runFreshSteps(t *testing.T, h http.Handler, advance func(time.Duration), steps []freshStep) {
	t.Helper()
	for _, s := range steps {
		if s.act != nil {
			s.act()
		}
		advance(s.advanceBy)
		code, got := reasons(t, h)
		if code != s.wantCode || !slices.Equal(got, s.reasons) {
			t.Errorf("%s: got %d %q, want %d %q", s.name, code, got, s.wantCode, s.reasons)
		}
	}
}

func freshChecks(st *Status) http.Handler {
	return CheckHandler(
		Check{Signal: st, Reason: "not serving"},
		Check{Signal: st.Fresh(time.Minute), Reason: "stale"},
	)
}

func TestFreshFailsOnlyAStaleHealthyVerdict(t *testing.T) {
	c := useClock(t)
	st := NewStatus(WithLogger(discard))
	set := func(ok bool) func() { return func() { st.Set(ok) } }
	runFreshSteps(t, freshChecks(st), c.advance, []freshStep{
		{name: "zero state", wantCode: 503, reasons: []string{"not serving"}},
		{name: "59s after Set(true)", act: set(true), advanceBy: 59 * time.Second, wantCode: 200},
		{name: "at the lease", advanceBy: time.Second, wantCode: 200},
		{name: "one nanosecond past the lease", advanceBy: time.Nanosecond, wantCode: 503, reasons: []string{"stale"}},
		{name: "a healthy Set restarts the age", act: set(true), wantCode: 200},
		{name: "Set(false) past the lease", advanceBy: 2 * time.Minute, act: set(false), wantCode: 503, reasons: []string{"not serving"}},
		{name: "stale then drained", act: func() {
			st.Set(true)
			c.advance(2 * time.Minute)
			st.BeginDrain()
		}, wantCode: 503, reasons: []string{"not serving"}},
	})
}

func TestFreshWithANonPositiveLeaseNeverFails(t *testing.T) {
	c := useClock(t)
	st := NewStatus(WithLogger(discard))
	st.Set(true)
	c.advance(1000 * time.Hour)
	for _, d := range []time.Duration{0, -1, -time.Hour} {
		if !st.Fresh(d).Healthy() {
			t.Errorf("Fresh(%v).Healthy() = false on a verdict 1000h old, want true", d)
		}
	}
}

func TestFreshInsideASyncTestBubble(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := NewStatus(WithLogger(discard))
		set := func(ok bool) func() { return func() { st.Set(ok) } }
		runFreshSteps(t, freshChecks(st), time.Sleep, []freshStep{
			{name: "zero state", wantCode: 503, reasons: []string{"not serving"}},
			{name: "at the lease", act: set(true), advanceBy: time.Minute, wantCode: 200},
			{name: "one nanosecond past the lease", advanceBy: time.Nanosecond, wantCode: 503, reasons: []string{"stale"}},
			{name: "a healthy Set restarts the age", act: set(true), wantCode: 200},
			{name: "Set(false)", act: set(false), wantCode: 503, reasons: []string{"not serving"}},
		})
	})
}

func TestEndLinesCarryTheirDuration(t *testing.T) {
	t.Run("from NewStatus", func(t *testing.T) {
		c := useClock(t)
		l, buf := jsonLog()
		st := NewStatus(WithLogger(l))
		c.advance(5 * time.Second)
		st.Set(true)
		c.advance(4 * time.Second)
		st.Set(false)
		c.advance(3 * time.Second)
		st.Set(true)
		st.BeginDrain()
		st.Set(false)
		got := records(t, buf)
		if len(got) != 4 {
			t.Fatalf("logged %d lines, want 4: %q", len(got), buf.String())
		}
		for i, want := range []any{float64(5 * time.Second), nil, float64(3 * time.Second), nil} {
			if d, ok := got[i]["duration"]; d != want || (want == nil && ok) {
				t.Errorf("line %d (%v): duration = %v, want %v", i, got[i], d, want)
			}
		}
	})
	t.Run("a zero Status's first healthy line has none", func(t *testing.T) {
		c := useClock(t)
		buf := captureLogs(t)
		var st Status
		st.Set(true)
		c.advance(time.Second)
		st.Set(false)
		c.advance(2 * time.Second)
		st.Set(true)
		lines := slices.Collect(strings.Lines(buf.String()))
		if len(lines) != 3 {
			t.Fatalf("logged %d lines, want 3: %q", len(lines), buf.String())
		}
		if strings.Contains(lines[0], "duration=") {
			t.Errorf("first healthy line %q carries a duration, want none", lines[0])
		}
		if !strings.Contains(lines[2], "duration=2s") {
			t.Errorf("second healthy line %q, want duration=2s", lines[2])
		}
	})
	t.Run("rounded to the millisecond", func(t *testing.T) {
		c := useClock(t)
		l, buf := jsonLog()
		st := NewStatus(WithLogger(l))
		c.advance(1500*time.Microsecond + 1)
		st.Set(true)
		got := records(t, buf)
		if len(got) != 1 || got[0]["duration"] != float64(2*time.Millisecond) {
			t.Errorf("logged %q, want one line with duration 2ms", buf.String())
		}
	})
}
