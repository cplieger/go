package health

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestLeaseDurationNeverNegative pins the invariant WithMaxAge depends on: no
// field combination may produce a negative deadline, which WithMaxAge would
// read as the deliberate disable.
func TestLeaseDurationNeverNegative(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		l := Lease{
			Interval: time.Duration(rapid.Int64().Draw(rt, "interval")),
			Timeout:  time.Duration(rapid.Int64().Draw(rt, "timeout")),
			Floor:    time.Duration(rapid.Int64().Draw(rt, "floor")),
			Cycles:   rapid.IntRange(-3, 1<<30).Draw(rt, "cycles"),
			Attempts: rapid.IntRange(-3, 1<<30).Draw(rt, "attempts"),
		}
		if got := l.Duration(); got < 0 {
			rt.Fatalf("Lease%+v.Duration() = %d, want >= 0", l, int64(got))
		}
	})
}

// TestLeaseDurationDisabledOnlyByInterval pins that the disable is Interval's
// alone, so a Floor cannot arm a lease an external mode turned off. The zero
// value is one of the cases: Lease{}.Duration() must be the documented disable.
func TestLeaseDurationDisabledOnlyByInterval(t *testing.T) {
	for name, l := range map[string]Lease{
		"zero value is disabled":  {},
		"zero interval, floor":    {Cycles: 3, Floor: 3 * time.Hour},
		"negative interval":       {Interval: -time.Hour, Cycles: 3},
		"negative interval+floor": {Interval: -time.Hour, Cycles: 3, Floor: 3 * time.Hour},
	} {
		if got := l.Duration(); got != 0 {
			t.Errorf("%s: Lease%+v.Duration() = %s, want 0 (disabled)", name, l, got)
		}
	}
}

// TestLeaseDurationReproducesEveryConsumerFormula checks each distinct
// deadline formula a consumer writes inline today against the shape that
// replaces it, at ordinary inputs.
func TestLeaseDurationReproducesEveryConsumerFormula(t *testing.T) {
	const iv = 6 * time.Hour
	const to = 10 * time.Minute
	tests := map[string]struct {
		lease Lease
		want  time.Duration
	}{
		"3x interval": {
			Lease{Interval: iv, Cycles: 3}, 3 * iv,
		},
		"2x interval + timeout": {
			Lease{Interval: iv, Cycles: 2, Timeout: to, Attempts: 1}, 2*iv + to,
		},
		"2x interval + 2x timeout": {
			Lease{Interval: iv, Cycles: 2, Timeout: to, Attempts: 2}, 2*iv + 2*to,
		},
		"2x interval + jobs x timeout": {
			Lease{Interval: iv, Cycles: 2, Timeout: to, Attempts: 7}, 2*iv + 7*to,
		},
		"interval + progress lease": {
			Lease{Interval: iv, Cycles: 1, Timeout: to, Attempts: 1}, iv + to,
		},
		"3x interval floored, floor binds": {
			Lease{Interval: 15 * time.Minute, Cycles: 3, Floor: 3 * time.Hour}, 3 * time.Hour,
		},
		"3x interval floored, interval binds": {
			Lease{Interval: 3 * time.Hour, Cycles: 3, Floor: 3 * time.Hour}, 9 * time.Hour,
		},
	}
	for name, tc := range tests {
		if got := tc.lease.Duration(); got != tc.want {
			t.Errorf("%s: Lease%+v.Duration() = %s, want %s", name, tc.lease, got, tc.want)
		}
	}
}

// TestLeaseDurationSaturatesWhereInlineArithmeticWraps drives the inputs
// measured to disarm the probe today. It is the load-bearing test for the
// saturation: TestLeaseDurationNeverNegative cannot catch a regression here,
// because Duration's floor turns a wrapped negative into 0 and 0 is not
// negative. Every wrapped value is computed through variables, since the same
// expression written as a constant is rejected by the compiler.
func TestLeaseDurationSaturatesWhereInlineArithmeticWraps(t *testing.T) {
	tests := map[string]struct {
		lease  Lease
		cycles int
	}{
		"3x interval past maxDuration/3":             {Lease{Interval: maxDuration/3 + 1, Cycles: 3}, 3},
		"2x interval past maxDuration/2":             {Lease{Interval: 1281024 * time.Hour, Cycles: 2, Timeout: time.Hour, Attempts: 1}, 2},
		"timeout alone at the representable ceiling": {Lease{Interval: 6 * time.Hour, Cycles: 2, Timeout: 2562047 * time.Hour, Attempts: 1}, 2},
		"interval and timeout both large":            {Lease{Interval: 1000000 * time.Hour, Cycles: 2, Timeout: 1000000 * time.Hour, Attempts: 1}, 2},
		"3x interval at a 97-year cadence":           {Lease{Interval: 900000 * time.Hour, Cycles: 3}, 3},
	}
	for name, tc := range tests {
		if got := tc.lease.Duration(); got != maxDuration {
			t.Errorf("%s: Lease%+v.Duration() = %d, want maxDuration (%d)",
				name, tc.lease, int64(got), int64(maxDuration))
		}
		iv, to := tc.lease.Interval, tc.lease.Timeout
		wrapped := time.Duration(tc.cycles)*iv + time.Duration(tc.lease.Attempts)*to
		if wrapped > 0 {
			t.Errorf("%s: the inline arithmetic yielded %d (> 0), so this case does not"+
				" reproduce the disarm and proves nothing", name, int64(wrapped))
		}
	}
}
