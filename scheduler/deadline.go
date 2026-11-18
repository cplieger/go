package scheduler

import (
	"time"

	"github.com/cplieger/go/health/v2"
)

// Fresh answers a Signal that turns unhealthy once the scheduled scope's
// deadline has passed; it is healthy for a scope that is not scheduled. It
// reads the scope at each call, so one Signal follows a later Schedule. The
// executor logs a passed deadline once at ERROR and its clearing once at INFO.
func (e *Executor[P]) Fresh(kind Kind, scope string) health.Signal {
	return health.SignalFunc(func() bool {
		d := e.Deadline(kind, scope)
		return d.IsZero() || !time.Now().After(d)
	})
}

// Deadline answers the time by which the scheduled scope's next job must end:
// its last moving end, or its covering kind's, plus its lease. It answers zero
// for a scope that is not scheduled.
//
// An app exports it as the gauge <app>_job_deadline_timestamp_seconds{kind,
// scope}, read at scrape time for each scheduled scope and dropped when
// Deadline answers zero, and ships the rule <App>JobStalled as
// time() > <app>_job_deadline_timestamp_seconds held for 5 minutes.
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

// watchLocked logs t's deadline once at ERROR when it has passed and once at
// INFO when it is ahead again, and checks it again just after it passes.
// Nothing is checked when armableLocked answers false.
func (e *Executor[P]) watchLocked(key ScheduledScope, t *timed) {
	e.stopCheckLocked(t)
	if !e.armableLocked() {
		return
	}
	d := t.watch.Deadline()
	now := time.Now()
	if now.After(d) {
		if !t.passed {
			t.passed = true
			e.log.Error("scheduled job not ended by its deadline", "kind", key.Kind, "scope", key.Scope, "deadline", d)
		}
		return
	}
	if t.passed {
		t.passed = false
		e.log.Info("scheduled job deadline ahead again", "kind", key.Kind, "scope", key.Scope, "deadline", d)
	}
	var check *time.Timer
	// One past d, since Fresh holds healthy at d itself.
	check = time.AfterFunc(d.Sub(now)+time.Nanosecond, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.scheduled[key] == t && t.check == check {
			e.watchLocked(key, t)
		}
	})
	t.check = check
}

// unwatchLocked stops t's check as its scope leaves the schedule, logging a
// passed deadline's clearing.
func (e *Executor[P]) unwatchLocked(key ScheduledScope, t *timed) {
	e.stopCheckLocked(t)
	if t.passed {
		t.passed = false
		e.log.Info("scheduled job deadline cleared: scope unscheduled", "kind", key.Kind, "scope", key.Scope)
	}
}

func (e *Executor[P]) stopCheckLocked(t *timed) {
	if t.check != nil {
		t.check.Stop()
		t.check = nil
	}
}
