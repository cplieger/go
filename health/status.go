package health

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Signal is one health condition a Check reports. *Status and SignalFunc
// implement it; any type the service keeps can too.
type Signal interface {
	Healthy() bool
}

// SignalFunc adapts a function to a Signal, as http.HandlerFunc adapts one to
// an http.Handler. A nil SignalFunc is unhealthy.
type SignalFunc func() bool

// Healthy reports f(), or false for a nil f.
func (f SignalFunc) Healthy() bool { return f != nil && f() }

// now is the clock a Status, its marker and the probe read; tests replace it.
var now = time.Now

// since is how long ago t was, as the end of a standing state logs it.
func since(t time.Time) time.Duration { return now().Sub(t).Round(time.Millisecond) }

// Status is a service's health verdict, kept in memory. It starts unhealthy.
// Set records a verdict, BeginDrain makes it unhealthy for good, and Healthy
// and Fresh read it without blocking on a write. With WithMarker, every write
// is mirrored to a marker file for RunProbe. It logs each change of verdict:
// Info when it turns healthy, Warn when it turns unhealthy.
//
// The zero Status is ready to use, with no marker and slog.Default. A Status
// is safe for concurrent use and must not be copied after first use.
type Status struct {
	unhealthySince time.Time
	logger         *slog.Logger
	// mirror is written under mu, so the file follows the order of the calls.
	mirror *marker
	// verdict is read without mu, so a reader never waits on a writer.
	verdict  atomic.Pointer[verdict]
	mu       sync.Mutex
	draining bool
}

var _ Signal = (*Status)(nil)

// verdict is one immutable reading; a healthy one records in at when its Set ran.
type verdict struct {
	at      time.Time
	healthy bool
}

// NewStatus returns an unhealthy Status configured by opts; a nil option is
// skipped. With WithMarker it checks that the marker can be written and
// removes any marker an earlier run left (and warns if it cannot). A marker
// it cannot write puts the Status in degraded mode and logs one warning
// saying so. Create a Status with a marker only in the long-running process,
// after the health subcommand is dispatched: a second process that creates
// one removes the marker and turns the running service unhealthy.
func NewStatus(opts ...StatusOption) *Status {
	s := &Status{}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}
	s.unhealthySince = now()
	if s.mirror != nil {
		s.mirror.open()
	}
	return s
}

// StatusOption configures NewStatus. The last of two options of one kind wins.
type StatusOption func(*Status)

// WithLogger sets the logger a Status reports to. A nil logger, like none,
// means slog.Default at the time of each line.
func WithLogger(l *slog.Logger) StatusOption {
	return func(s *Status) { s.logger = l }
}

// WithMarker mirrors the verdict to a marker file at path, for a service whose
// healthcheck runs RunProbe on the same path. Set(true) creates the marker or
// refreshes its modification time, so RunProbe with WithMaxAge measures the
// age Fresh does unless the wall clock steps; Set(false) and BeginDrain remove
// it. A failed write is logged and leaves the verdict unchanged. A symlink at
// path when NewStatus runs is degraded mode; one planted later fails the write
// and, where the platform has O_NOFOLLOW, is not followed. NewStatus panics on
// an empty path. Give each Status its own path.
func WithMarker(path string) StatusOption {
	return func(s *Status) {
		if path == "" {
			panic("health: WithMarker with an empty path")
		}
		s.mirror = &marker{path: path, log: s.log}
	}
}

func (s *Status) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Set records the verdict: healthy when ok, unhealthy otherwise. A healthy Set
// also restarts the age Fresh measures, even when the verdict was already
// healthy. After BeginDrain a healthy Set is dropped and an unhealthy one
// still lands. Set may be called from any goroutine. A Status with a marker
// writes it under the same lock, so the file follows the order of the calls.
func (s *Status) Set(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining && ok {
		return
	}
	prev := s.verdict.Load()
	next := &verdict{healthy: ok}
	if ok {
		next.at = now()
	}
	s.verdict.Store(next)
	if s.mirror != nil {
		s.mirror.write(ok)
	}
	if wasHealthy := prev != nil && prev.healthy; wasHealthy != ok {
		s.logChange(ok)
	}
}

// BeginDrain makes the verdict unhealthy at once and for good: every later
// healthy Set is dropped. Call it when shutdown begins, before waiting for
// work in flight, so work that finishes during the drain cannot report the
// service healthy. It is safe to call more than once.
func (s *Status) BeginDrain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = true
	prev := s.verdict.Load()
	wasHealthy := prev != nil && prev.healthy
	if wasHealthy {
		s.verdict.Store(&verdict{})
	}
	if s.mirror != nil {
		s.mirror.write(false)
	}
	if wasHealthy {
		s.logChange(false, "draining", true)
	}
}

// logChange logs a change of verdict. The healthy line carries how long the
// service was unhealthy, which a zero Status that never ran NewStatus cannot
// know before its first write. Caller holds mu.
func (s *Status) logChange(healthy bool, attrs ...any) {
	if !healthy {
		s.unhealthySince = now()
		s.log().Warn("health state changed", append([]any{"healthy", false}, attrs...)...)
		return
	}
	attrs = append([]any{"healthy", true}, attrs...)
	if !s.unhealthySince.IsZero() {
		attrs = append(attrs, "duration", since(s.unhealthySince))
	}
	s.log().Info("health state changed", attrs...)
}

// Healthy reports the verdict. It reads memory only, never the marker. A nil
// *Status is unhealthy.
func (s *Status) Healthy() bool {
	if s == nil {
		return false
	}
	v := s.verdict.Load()
	return v != nil && v.healthy
}

// Fresh returns a Signal that fails only while the verdict is healthy and its
// last healthy Set is older than d; a non-positive d never fails. An unhealthy
// verdict passes it, so list Fresh beside the Status itself and each failure
// gives its own reason: the Status's when the service reports a fault, Fresh's
// when a work loop stopped refreshing a healthy verdict. Pass the d the
// healthcheck gives WithMaxAge.
func (s *Status) Fresh(d time.Duration) Signal {
	return SignalFunc(func() bool {
		if s == nil {
			return true
		}
		v := s.verdict.Load()
		return v == nil || !v.healthy || !stale(now().Sub(v.at), d)
	})
}
