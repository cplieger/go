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

// now is the clock a Status and the probe read; tests replace it.
var now = time.Now

// Status is a service's health verdict, kept in memory. It starts unhealthy.
// Set records a verdict, BeginDrain makes it unhealthy for good, and Healthy
// and Fresh read it without blocking on a write. It logs each change of
// verdict: Info when it turns healthy, Warn when it turns unhealthy.
//
// The zero Status is ready to use, with slog.Default. A Status is safe for
// concurrent use and must not be copied after first use.
type Status struct {
	unhealthySince time.Time
	logger         *slog.Logger
	// verdict is read without mu, so a reader never waits on a writer.
	verdict  atomic.Pointer[verdict]
	mu       sync.Mutex
	draining bool
}

var _ Signal = (*Status)(nil)

// verdict is one immutable reading: at is the time of the last healthy Set.
type verdict struct {
	at      time.Time
	healthy bool
}

// NewStatus returns an unhealthy Status configured by opts; a nil option is
// skipped.
func NewStatus(opts ...StatusOption) *Status {
	s := &Status{}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}
	s.unhealthySince = now()
	return s
}

// StatusOption configures NewStatus. The last of two options of one kind wins.
type StatusOption func(*Status)

// WithLogger sets the logger a Status reports to. A nil logger, like none,
// means slog.Default at the time of each line.
func WithLogger(l *slog.Logger) StatusOption {
	return func(s *Status) { s.logger = l }
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
// still lands. Set may be called from any goroutine.
func (s *Status) Set(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining && ok {
		return
	}
	prev := s.verdict.Load()
	next := &verdict{healthy: ok}
	switch {
	case ok:
		next.at = now()
	case prev != nil:
		next.at = prev.at
	}
	s.verdict.Store(next)
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
	if prev == nil || !prev.healthy {
		return
	}
	s.verdict.Store(&verdict{at: prev.at})
	s.logChange(false, "draining", true)
}

// logChange logs a change of verdict. The healthy line carries how long the
// service was unhealthy, which a zero Status that never ran NewStatus cannot
// know before its first write. Caller holds mu.
func (s *Status) logChange(healthy bool, attrs ...any) {
	t := now()
	if !healthy {
		s.unhealthySince = t
		s.log().Warn("health state changed", append([]any{"healthy", false}, attrs...)...)
		return
	}
	attrs = append([]any{"healthy", true}, attrs...)
	if !s.unhealthySince.IsZero() {
		attrs = append(attrs, "duration", t.Sub(s.unhealthySince).Round(time.Millisecond))
	}
	s.log().Info("health state changed", attrs...)
}

// Healthy reports the verdict. A nil *Status is unhealthy.
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
