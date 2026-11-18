package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/cplieger/go/health/v2"
)

// maxScheduled bounds the scheduled scopes of one executor.
const maxScheduled = 64

// jitter is the ±fraction each scheduled wait is drawn from.
const jitter = 0.10

// timed is one scheduled scope: its cadence, its watch and its timer. The
// executor's mu guards every field; the watch guards its own state.
type timed struct {
	watch *Watch
	timer *time.Timer
	// base is what the next wake counts from: the last moving end, the
	// cadence change, or the start when the scope has neither.
	base    time.Time
	since   time.Time // the Schedule call that added it
	cadence Cadence
	gen     uint64 // bumped at each arm, so a superseded timer's func does nothing
	done    bool   // base is an end whose work was done, or no end, so the wait is the interval
	// fromEnds is a first Schedule in the executor's life whose base comes
	// from Records.LastEnds, which Run has not read yet.
	fromEnds bool
}

// Schedule sets the live cadence of one scheduled scope of kind. A changed
// cadence restarts the wait and the deadline from the change and fires
// nothing at it; the same cadence changes neither, and TriggerOnly has
// Unschedule's effect. The first Schedule of a scope counts both from its last
// moving end in Records.LastEnds, else from the later of Run's start and the
// call. Before changing anything it refuses an unknown kind (ErrUnknownKind),
// a scope outside its grammar (ErrScope) and a new scope past 64
// (ErrTooManyScopes; re-scheduling one never counts).
func (e *Executor[P]) Schedule(kind Kind, scope string, c Cadence) error {
	spec, ok := e.kinds[kind]
	switch {
	case !ok:
		return ErrUnknownKind
	case !validScope(scope):
		return ErrScope
	}
	every, scheduled := c.Every()
	key := ScheduledScope{Kind: kind, Scope: scope}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !scheduled {
		e.unscheduleLocked(key)
		return nil
	}
	t := e.scheduled[key]
	if t != nil && t.cadence == c {
		return nil
	}
	if t == nil && len(e.scheduled) >= maxScheduled {
		return ErrTooManyScopes
	}
	lease := health.Lease{Interval: every, Cycles: 3}
	if spec.Watch != nil {
		lease = *spec.Watch
	}
	now := time.Now()
	_, again := e.seen[key]
	if t == nil {
		t = &timed{since: now, fromEnds: !again}
		e.scheduled[key] = t
		e.seen[key] = struct{}{}
	} else {
		t.fromEnds = false
	}
	if !t.fromEnds {
		t.base, t.done = now, true
	}
	t.cadence = c
	t.watch = NewWatch(lease)
	if t.fromEnds && e.endsRead {
		e.baseLocked(key, t)
	}
	e.armLocked(key, t)
	return nil
}

// Unschedule removes a scheduled scope's timer and watch, so Deadline answers
// zero for it and an app's deadline series drops it. It does nothing for a
// scope that is not scheduled.
func (e *Executor[P]) Unschedule(kind Kind, scope string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unscheduleLocked(ScheduledScope{Kind: kind, Scope: scope})
}

func (e *Executor[P]) unscheduleLocked(key ScheduledScope) {
	if t := e.scheduled[key]; t != nil {
		e.stopTimerLocked(t)
		delete(e.scheduled, key)
	}
}

func (e *Executor[P]) stopTimerLocked(t *timed) {
	t.gen++
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

// readEnds loads Records.LastEnds once Run starts, then sets the base of
// each scope first scheduled before it and arms every timer. A failed read
// is logged and counts each such scope from the start.
func (e *Executor[P]) readEnds(ctx context.Context, start time.Time) {
	var ends map[ScheduledScope]LastEnd
	if e.records != nil {
		var err error
		if ends, err = e.records.LastEnds(ctx); err != nil {
			e.log.Error("last job ends not read; scheduled scopes count from the start", "err", err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, end := range ends {
		if have, ok := e.ends[key]; !ok || end.At.After(have.At) {
			e.ends[key] = end
		}
	}
	e.endsRead, e.runAt = true, start
	for key, t := range e.scheduled {
		if t.fromEnds {
			e.baseLocked(key, t)
		}
		e.armLocked(key, t)
	}
}

// baseLocked counts a first-scheduled scope from its last moving end, else
// from the later of Run's start and its Schedule call.
func (e *Executor[P]) baseLocked(key ScheduledScope, t *timed) {
	t.fromEnds = false
	if end, ok := e.ends[key]; ok {
		t.base, t.done = end.At, workDone(end.Result)
		t.watch.Beat(end.At)
		return
	}
	t.base, t.done = t.since, true
	if e.runAt.After(t.since) {
		t.base = e.runAt
		t.watch.from(e.runAt)
	}
}

// armLocked sets t's timer to its base plus the cadence's wait, jittered.
// Nothing is armed before Run has read the last ends or after it ended.
func (e *Executor[P]) armLocked(key ScheduledScope, t *timed) {
	e.armAtLocked(key, t, t.base.Add(JitteredDelay(t.cadence.wait(t.done), jitter)))
}

func (e *Executor[P]) armAtLocked(key ScheduledScope, t *timed, at time.Time) {
	e.stopTimerLocked(t)
	if !e.endsRead || e.closed {
		return
	}
	gen := t.gen
	t.timer = time.AfterFunc(time.Until(at), func() { e.wake(key, t, gen) })
}

// noteEndLocked records a started job's moving end at at: its scope's last
// end, and for a scheduled scope its watch's beat and its next wake.
func (e *Executor[P]) noteEndLocked(en *job[P], r Result, at time.Time) {
	key := ScheduledScope{Kind: en.j.Kind, Scope: en.j.Scope}
	t := e.scheduled[key]
	// A proposing kind's scopes are digests of unbounded keys: only a
	// scheduled one is kept, so the map stays bounded.
	if t == nil && en.spec.Proposes {
		return
	}
	if have, ok := e.ends[key]; !ok || !at.Before(have.At) {
		e.ends[key] = LastEnd{At: at, Result: r}
	}
	if t == nil || t.fromEnds {
		return
	}
	t.base, t.done = at, workDone(r)
	t.watch.Beat(at)
	e.armLocked(key, t)
}

// wake submits a scheduled scope's timer request, then arms the next wake
// unless an end or a change armed it while Start ran. A refusal waits
// Retry's d, or the interval, from the refusal: a refusal code's Retry-After
// is for an API caller and never sets this timer.
func (e *Executor[P]) wake(key ScheduledScope, t *timed, gen uint64) {
	e.mu.Lock()
	if e.scheduled[key] != t || t.gen != gen || e.closed {
		e.mu.Unlock()
		return
	}
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()

	_, err := e.Start(context.Background(), Job[P]{Kind: key.Kind, Scope: key.Scope, Trigger: TriggerSchedule})
	var recent *RecentError
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scheduled[key] != t || t.gen != gen {
		return
	}
	now := time.Now()
	switch {
	case err == nil:
		// Admitted or joined: the job's moving end re-arms from itself, and
		// this wake stands in when it ends without one.
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(true), jitter)))
	case errors.As(err, &recent):
		e.armAtLocked(key, t, recent.Until)
	case errors.Is(err, ErrQueueFull), errors.Is(err, ErrStoreUnavailable), errors.Is(err, ErrCapacity):
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(false), jitter)))
	default:
		e.log.Error("scheduled job not started", "kind", key.Kind, "scope", key.Scope, "err", err)
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(false), jitter)))
	}
}

// stopTimersLocked stops every scheduled scope's timer.
func (e *Executor[P]) stopTimersLocked() {
	for _, t := range e.scheduled {
		e.stopTimerLocked(t)
	}
}

// workDone reports whether an end did its work, so the next wake waits the
// interval and not Retry's d.
func workDone(r Result) bool { return r == ResultOK || r == ResultPartial }
