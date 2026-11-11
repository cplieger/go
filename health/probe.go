package health

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"time"
)

// RunProbe is the healthcheck side of WithMarker, run as the service's own
// `health` subcommand. It exits 0 when the marker at path exists (and, with
// WithMaxAge, is no older than its deadline) or when the marker cannot be
// written or is a symlink, which it never follows (degraded mode: the service
// still works, and a restart would not fix its health reporting). Otherwise it
// writes the reason to stderr and exits 1.
func RunProbe(path string, opts ...ProbeOption) {
	code, reason := probeCheck(path, opts...)
	if code != 0 {
		fmt.Fprintln(os.Stderr, reason)
	}
	os.Exit(code)
}

// probeCheck is RunProbe's decision and the reason it writes, without exiting.
func probeCheck(path string, opts ...ProbeOption) (code int, reason string) {
	f := inspect(path, opts...)
	if f.healthy() {
		return 0, ""
	}
	return 1, f.reason()
}

// ProbeOption configures RunProbe. Without options it checks that the marker
// exists.
type ProbeOption func(*probeConfig)

type probeConfig struct {
	maxAge time.Duration
}

// WithMaxAge fails a marker older than d, so a work loop that stops calling
// Set(true) turns the container unhealthy. Arm it only for a service that
// runs its own work at a known interval, and give Fresh the same d. A
// non-positive d disables it; build d with Lease, which cannot wrap negative.
func WithMaxAge(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.maxAge = d }
}

// stale is the lease rule Fresh and the probe share: a positive maxAge that
// the age exceeds.
func stale(age, maxAge time.Duration) bool { return maxAge > 0 && age > maxAge }

const maxDuration = time.Duration(math.MaxInt64)

// Lease derives a freshness deadline for WithMaxAge and Fresh from a service's
// own cadence: Cycles intervals plus Attempts timeouts, never shorter than
// Floor. A non-positive Interval disables it, whatever the other fields hold,
// so the zero Lease is disabled. Duration saturates instead of wrapping.
type Lease struct {
	// Interval is how often the service records a healthy verdict. A
	// non-positive Interval disables the lease.
	Interval time.Duration
	// Timeout is the longest one unit of work may legitimately take.
	Timeout time.Duration
	// Floor is the shortest deadline to arm. It applies only when Interval is
	// positive.
	Floor time.Duration
	// Cycles is how many missed refreshes to tolerate. Non-positive counts as none.
	Cycles int
	// Attempts is how many Timeout budgets one refresh may spend. Non-positive
	// counts as none.
	Attempts int
}

// Duration reports the deadline: 0 when Interval is non-positive, or when the
// scaled cycles, the scaled attempts and Floor are all non-positive; otherwise
// a positive duration that saturates at the largest time.Duration.
func (l Lease) Duration() time.Duration {
	if l.Interval <= 0 {
		return 0
	}
	d := addSaturating(scale(l.Interval, l.Cycles), scale(l.Timeout, l.Attempts))
	return max(d, l.Floor)
}

// scale multiplies d by n, saturating at maxDuration instead of wrapping. A
// non-positive d or n contributes nothing.
func scale(d time.Duration, n int) time.Duration {
	if d <= 0 || n <= 0 {
		return 0
	}
	if d > maxDuration/time.Duration(n) {
		return maxDuration
	}
	return d * time.Duration(n)
}

// addSaturating returns a+b for non-negative a and b, saturating at maxDuration.
func addSaturating(a, b time.Duration) time.Duration {
	if a > maxDuration-b {
		return maxDuration
	}
	return a + b
}

// markerState is what one look at a marker found. The zero value is unnamed
// and unhealthy, so a zero freshness on an error path cannot read as fresh.
type markerState int

const (
	// markerFresh is a marker present and, when a deadline is armed, inside it.
	markerFresh markerState = iota + 1
	// markerStale is a marker the service can write, older than the deadline.
	markerStale
	// markerAbsent is no marker in a folder the service can write.
	markerAbsent
	// markerUnreadable is a marker whose stat failed for a reason other than
	// absence, in a folder the service can write.
	markerUnreadable
	// markerDegraded is degraded mode: the folder cannot be written, a stale
	// marker cannot be refreshed, or a symlink is at the path. It is healthy.
	markerDegraded
)

// freshness is one look at a marker.
type freshness struct {
	// err is the stat error for markerUnreadable.
	err error
	// age and maxAge are set for markerStale.
	age    time.Duration
	maxAge time.Duration
	state  markerState
}

func (f freshness) healthy() bool {
	return f.state == markerFresh || f.state == markerDegraded
}

// ceilMillisecond rounds d up to a whole millisecond, saturating at
// maxDuration: an age past about 292 years is maxDuration, whose last partial
// millisecond has no whole one above it.
func ceilMillisecond(d time.Duration) time.Duration {
	if r := d % time.Millisecond; r > 0 {
		return addSaturating(d-r, time.Millisecond)
	}
	return d
}

// reason is the text RunProbe writes for an unhealthy reading, and empty for
// a healthy one.
func (f freshness) reason() string {
	switch f.state {
	case markerStale:
		// The age rounds up and the max-age prints as armed, so a stale age
		// never prints as equal to or below the max-age.
		return fmt.Sprintf("unhealthy: marker stale: %s old exceeds max-age %s",
			ceilMillisecond(f.age), f.maxAge)
	case markerAbsent:
		return "unhealthy: marker absent"
	case markerUnreadable:
		return "unhealthy: marker stat failed: " + f.err.Error()
	case markerFresh, markerDegraded:
		return ""
	}
	return "unhealthy: marker state unknown"
}

// inspect reads the marker at path without following a symlink.
func inspect(path string, opts ...ProbeOption) freshness {
	var cfg probeConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	info, statErr := os.Lstat(path) // #nosec G703 -- trusted caller-supplied marker path, existence check only
	if statErr == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return freshness{state: markerDegraded}
		}
		age := now().Sub(info.ModTime())
		if !stale(age, cfg.maxAge) {
			return freshness{state: markerFresh}
		}
		// Failing a stale marker nothing can refresh would restart a
		// container that a restart cannot fix.
		if cannotSignal(path) != nil {
			return freshness{state: markerDegraded}
		}
		return freshness{state: markerStale, age: age, maxAge: cfg.maxAge}
	}
	// A folder nothing can write to explains absence without implicating the
	// service, so it is checked before the stat error is classified.
	if probeHealthDir(path) != nil {
		return freshness{state: markerDegraded}
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return freshness{state: markerAbsent}
	}
	return freshness{state: markerUnreadable, err: statErr}
}
