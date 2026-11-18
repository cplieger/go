package scheduler

import (
	"time"

	"github.com/cplieger/go/health/v2"
)

// Fresh answers a Signal that turns unhealthy once the scheduled scope's
// deadline has passed; it is healthy for a scope that is not scheduled. It
// reads the scope at each call, so one Signal follows a later Schedule.
func (e *Executor[P]) Fresh(kind Kind, scope string) health.Signal {
	return health.SignalFunc(func() bool {
		d := e.Deadline(kind, scope)
		return d.IsZero() || !time.Now().After(d)
	})
}

// Deadline answers the time by which the scheduled scope's next job must end:
// its last moving end plus its lease. It answers zero for a scope that is not
// scheduled.
func (e *Executor[P]) Deadline(kind Kind, scope string) time.Time {
	e.mu.Lock()
	t := e.scheduled[ScheduledScope{Kind: kind, Scope: scope}]
	var w *Watch
	if t != nil {
		w = t.watch
	}
	e.mu.Unlock()
	if w == nil {
		return time.Time{}
	}
	return w.Deadline()
}
