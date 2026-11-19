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
	check *time.Timer // fires just after the watch's deadline
	// base is what the next wake counts from: the last moving end, the
	// cadence change, or the start when the scope has neither.
	base  time.Time
	since time.Time // the Schedule call that added it
	// changed is the last cadence change: Schedule's changed, at most the
	// call, else the call; zero for a first Schedule that gave none.
	changed time.Time
	lease   health.Lease // the kind's Watch(every), or {every, Cycles: 3}
	cadence Cadence
	gen     uint64 // bumped at each arm, so a superseded timer's func does nothing
	done    bool   // base is an end whose work was done, or no end, so the wait is the interval
	passed  bool   // the deadline passed and its ERROR line was logged
}

// coveringEnd is an end and the scope that ended it.
type coveringEnd struct {
	by  ScheduledScope
	end LastEnd
}

// Schedule sets a scheduled scope's live cadence c, which the app committed
// at changed: zero when unknown, a future one counts as the call. The wait
// and the deadline count from the later of the last moving end (read from
// Records.LastEnds at a first Schedule) and the change, or with no end from
// the latest of the change, the first call and Run's start. A different cadence,
// from TriggerOnly included, changes at changed or the call and fires nothing;
// the same one changes nothing; TriggerOnly unschedules. It refuses, changing
// nothing, with ErrUnknownKind, ErrScope, ErrLease or ErrTooManyScopes.
func (e *Executor[P]) Schedule(kind Kind, scope string, c Cadence, changed time.Time) error {
	spec, ok := e.kinds[kind]
	switch {
	case !ok:
		return ErrUnknownKind
	case !validScope(scope):
		return ErrScope
	}
	lease, err := leaseOf(spec, c)
	if err != nil {
		return err
	}
	_, scheduled := c.Every()
	key := ScheduledScope{Kind: kind, Scope: scope}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, again := e.seen[key]
	if !scheduled {
		e.seen[key] = struct{}{}
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
	e.seen[key] = struct{}{}
	now := time.Now()
	if changed.After(now) || again && changed.IsZero() {
		changed = now
	}
	if t == nil {
		t = &timed{since: now}
		e.scheduled[key] = t
	}
	t.cadence, t.lease, t.changed = c, lease, changed
	t.watch = NewWatch(lease)
	if e.endsRead {
		e.baseLocked(key, t)
	} else {
		t.base, t.done = e.noEndBaseLocked(t), true
		t.watch.from(t.base)
	}
	e.armLocked(key, t)
	e.watchLocked(key, t)
	return nil
}

// leaseOf answers the lease of a scope of spec at cadence c: none for
// TriggerOnly, else spec's Watch at its interval, refused with ErrLease when
// its Duration() is 0.
func leaseOf[P any](spec *KindSpec[P], c Cadence) (health.Lease, error) {
	every, scheduled := c.Every()
	switch {
	case !scheduled:
		return health.Lease{}, nil
	case spec.Watch == nil:
		return health.Lease{Interval: every, Cycles: 3}, nil
	}
	l := spec.Watch(every)
	if l.Duration() == 0 {
		return health.Lease{}, ErrLease
	}
	return l, nil
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
		e.unwatchLocked(key, t)
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

// readEnds reads Records.LastEnds once Run starts, retrying a failed read
// on readBackoff until one answers or ctx ends, then takes it
// (mergeEndsLocked), sets the base of each scheduled scope and arms every
// timer. Until a read answers no timer is armed and each deadline counts as
// if the scope had no end.
func (e *Executor[P]) readEnds(ctx context.Context, start time.Time) {
	ends, err := e.lastEnds(ctx)
	for n := 1; err != nil; n++ {
		if n == 1 {
			e.countFromStart(start)
		}
		e.log.Error("last job ends not read; scheduled scopes wait for a read", "err", err, "retry_in", readBackoff(n))
		if !pause(ctx.Done(), readBackoff(n)) {
			return
		}
		ends, err = e.lastEnds(ctx)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mergeEndsLocked(ends)
	e.endsRead, e.countFrom = true, start
	for key, t := range e.scheduled {
		e.baseLocked(key, t)
		e.armLocked(key, t)
		e.watchLocked(key, t)
	}
}

// mergeEndsLocked takes a LastEnds read: each scope keeps the later of its
// read end and one already noted, and each Fresh window a read end opens
// that is still ahead is opened, so rule 4 holds across a restart.
func (e *Executor[P]) mergeEndsLocked(ends map[ScheduledScope]LastEnd) {
	now := time.Now()
	for key, end := range ends {
		if have, ok := e.ends[key]; !ok || end.At.After(have.At) {
			e.ends[key] = end
		}
		if spec := e.kinds[key.Kind]; spec != nil && spec.Fresh != nil {
			if until := end.At.Add(spec.Fresh(key.Scope)); until.After(now) && until.After(e.recent[key]) {
				e.recent[key] = until
			}
		}
	}
}

// countFromStart counts each scheduled scope as if it had no end
// (noEndBaseLocked), from start on, while no LastEnds read has answered, and
// checks each deadline.
func (e *Executor[P]) countFromStart(start time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.countFrom = start
	for key, t := range e.scheduled {
		t.watch.from(e.noEndBaseLocked(t))
		e.watchLocked(key, t)
	}
}

// lastEnds reads Records.LastEnds; an executor with no Records has none.
func (e *Executor[P]) lastEnds(ctx context.Context) (map[ScheduledScope]LastEnd, error) {
	if e.records == nil {
		return nil, nil
	}
	return e.records.LastEnds(ctx)
}

// readBackoff answers the wait before the n-th retry of a failed
// Records.LastEnds read: 1, 2, 4 and 8 minutes, then every 15.
func readBackoff(n int) time.Duration {
	if n > 4 {
		return 15 * time.Minute
	}
	return time.Minute << (n - 1)
}

// pause waits d and reports true, or false when stop is closed first.
func pause(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	}
}

// baseLocked counts a scope from the later of its last moving end, its
// covering kinds' included, and its last cadence change, or with no end from
// noEndBaseLocked.
func (e *Executor[P]) baseLocked(key ScheduledScope, t *timed) {
	_, end, ok := e.leadEndLocked(key)
	if ok && !end.At.Before(t.changed) {
		t.base, t.done = end.At, workDone(end.Result)
		t.watch.Beat(end.At)
		return
	}
	from := t.changed
	if !ok {
		from = e.noEndBaseLocked(t)
	}
	t.base, t.done = from, true
	t.watch.from(from)
}

// noEndBaseLocked answers what a scope with no moving end counts from: the
// latest of its Schedule call, its last cadence change and countFrom, so a
// scope added long after start is not overdue at once.
func (e *Executor[P]) noEndBaseLocked(t *timed) time.Time {
	from := t.since
	for _, at := range []time.Time{t.changed, e.countFrom} {
		if at.After(from) {
			from = at
		}
	}
	return from
}

// armLocked sets t's timer to its base plus the cadence's wait, jittered.
// Nothing is armed while armableLocked answers false.
func (e *Executor[P]) armLocked(key ScheduledScope, t *timed) {
	e.armAtLocked(key, t, t.base.Add(JitteredDelay(t.cadence.wait(t.done), jitter)))
}

func (e *Executor[P]) armAtLocked(key ScheduledScope, t *timed, at time.Time) {
	e.stopTimerLocked(t)
	if !e.armableLocked() {
		return
	}
	gen := t.gen
	t.timer = time.AfterFunc(time.Until(at), func() { e.wake(key, t, gen) })
}

// lastEndLocked answers the moving end key counts from and the scope that
// ended it: the first in the order of countsOver among key's own end and
// those of scopes whose kind covers key's timer job.
func (e *Executor[P]) lastEndLocked(key ScheduledScope) (from ScheduledScope, end LastEnd, ok bool) {
	end, ok = e.ends[key]
	from = key
	for k, c := range e.ends {
		if k != key && e.coversLocked(k, key) && (!ok || countsOver(key, k, c, from, end)) {
			from, end, ok = k, c, true
		}
	}
	return from, end, ok
}

// leadEndLocked answers the moving end key counts from and the scope that
// ended it: lastEndLocked's, or the unkept end held for key when that one
// comes before it (countsOver).
func (e *Executor[P]) leadEndLocked(key ScheduledScope) (from ScheduledScope, end LastEnd, ok bool) {
	from, end, ok = e.lastEndLocked(key)
	if u, held := e.unkept[key]; held && (!ok || countsOver(key, u.by, u.end, from, end)) {
		return u.by, u.end, true
	}
	return from, end, ok
}

// countsOver reports whether end, of scope by, comes before have, of scope
// from, as what key counts from: the later At, then at one instant key's own
// end, then the least Kind, then Scope. It reads only what Records.LastEnds
// keeps, so no map order or note order decides, and a restart counts alike.
func countsOver(key, by ScheduledScope, end LastEnd, from ScheduledScope, have LastEnd) bool {
	switch {
	case !end.At.Equal(have.At):
		return end.At.After(have.At)
	case by == key || from == key:
		return by == key
	case by.Kind != from.Kind:
		return by.Kind < from.Kind
	}
	return by.Scope < from.Scope
}

// coversLocked reports whether by's kind covers key's timer job. It asks
// Covers with by's Kind and Scope only, which is all Records.LastEnds keeps,
// so a deadline reads the same before and after a restart.
func (e *Executor[P]) coversLocked(by, key ScheduledScope) bool {
	spec := e.kinds[by.Kind]
	return spec != nil && spec.Covers != nil &&
		spec.Covers(Job[P]{Kind: by.Kind, Scope: by.Scope}, Job[P]{Kind: key.Kind, Scope: key.Scope, Trigger: TriggerSchedule})
}

// noteEndLocked records a started job's moving end at at as its scope's last
// end, then moves the watch's beat and the next wake of its scheduled scope
// and of each one its kind covers whose count this end now leads, so an end
// noted after a later one moves nothing.
func (e *Executor[P]) noteEndLocked(en *job[P], r Result, at time.Time) {
	key := ScheduledScope{Kind: en.j.Kind, Scope: en.j.Scope}
	end := LastEnd{At: at, Result: r}
	// A proposing kind's scopes are digests of unbounded keys: only a
	// scheduled one is kept, so the map stays bounded.
	kept := e.scheduled[key] != nil || !en.spec.Proposes
	if kept {
		e.keepEndLocked(key, end)
	}
	for k := range e.seen {
		if k != key && !e.coversLocked(key, k) {
			continue
		}
		e.holdUnkeptLocked(k, key, end, kept)
		if t := e.scheduled[k]; t != nil && e.endsRead && e.leadsLocked(k, key, end) {
			e.movedLocked(k, t, at, workDone(r))
		}
	}
}

// keepEndLocked keeps end as key's last end, and as one noted during a
// resume's read, unless the one held is later.
func (e *Executor[P]) keepEndLocked(key ScheduledScope, end LastEnd) {
	if have, ok := e.ends[key]; !ok || !end.At.Before(have.At) {
		e.ends[key] = end
	}
	if have, ok := e.noted[key]; e.noted != nil && (!ok || !end.At.Before(have.At)) {
		e.noted[key] = end
	}
}

// holdUnkeptLocked holds, for a scope key ever scheduled, the end of scope by
// that comes first (countsOver) among the ends not kept in ends, so key
// counts from it as a restart does from Records.LastEnds; a kept end that
// comes before it drops it, since ends then holds the later one.
func (e *Executor[P]) holdUnkeptLocked(key, by ScheduledScope, end LastEnd, kept bool) {
	u, held := e.unkept[key]
	switch {
	case held && !countsOver(key, by, end, u.by, u.end):
	case kept:
		delete(e.unkept, key)
	default:
		e.unkept[key] = coveringEnd{by: by, end: end}
	}
}

// leadsLocked reports whether end, of scope by, is what key counts from
// (leadEndLocked).
func (e *Executor[P]) leadsLocked(key, by ScheduledScope, end LastEnd) bool {
	from, have, ok := e.leadEndLocked(key)
	return ok && from == by && have.At.Equal(end.At) && have.Result == end.Result
}

// movedLocked counts a scheduled scope from a moving end at at.
func (e *Executor[P]) movedLocked(key ScheduledScope, t *timed, at time.Time, done bool) {
	t.base, t.done = at, done
	t.watch.Beat(at)
	e.armLocked(key, t)
	e.watchLocked(key, t)
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

// stopTimersLocked stops every scheduled scope's timer and deadline check.
func (e *Executor[P]) stopTimersLocked() {
	for _, t := range e.scheduled {
		e.stopTimerLocked(t)
		e.stopCheckLocked(t)
	}
}

// armableLocked reports whether timers may be set: a LastEnds read answered
// since Run's start and the last resume, and no hold is open.
func (e *Executor[P]) armableLocked() bool {
	return e.endsRead && !e.stale && !e.closed && e.holds == 0
}

// watchableLocked reports whether deadline checks may be set. They run while
// a LastEnds read fails, since each deadline counts from the start or the
// resume meanwhile.
func (e *Executor[P]) watchableLocked() bool { return e.started && !e.closed && e.holds == 0 }

// workDone reports whether an end did its work, so the next wake waits the
// interval and not Retry's d.
func workDone(r Result) bool { return r == ResultOK || r == ResultPartial }
