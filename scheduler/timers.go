package scheduler

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/cplieger/go/health/v2"
)

// maxScheduled bounds the scheduled scopes of one executor.
const maxScheduled = 64

// maxEnds bounds the last ends an executor holds: a store keeps at most
// 10,000 LastEnds rows, so a restart reads no more ends than that.
const maxEnds = 10_000

// jitter is the ±fraction each scheduled wait is drawn from.
const jitter = 0.10

// timed is one scheduled scope: its cadence, its watch and its timer. The
// executor's mu guards every field; the watch guards its own state.
type timed struct {
	// base is what the next wake counts from: the last moving end, the
	// cadence change, or the start when the scope has neither.
	base  time.Time
	since time.Time // the Schedule call that added it
	// changed is the last cadence change: Schedule's changed, at most the
	// call, else the call; zero for a first Schedule that gave none.
	changed time.Time
	watch   *Watch
	timer   *time.Timer
	check   *time.Timer    // fires just after the watch's deadline
	lead    ScheduledScope // the scope whose end base is; zero when base is no end
	standIn ID             // the job whose wake armed the timer, until a re-arm
	lease   health.Lease   // the kind's Watch(every), or {every, Cycles: 3}
	cadence Cadence
	gen     uint64 // bumped at each arm, so a superseded timer's func does nothing
	done    bool   // base is an end whose work was done, or no end, so the wait is the interval
	passed  bool   // the deadline passed and its ERROR line was logged
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
	set := false
	defer func() {
		// A panic out of Covers: unscheduled, the scope's next Schedule arms
		// it, where a half-set one would answer that nothing changed.
		if !set {
			e.unscheduleLocked(key)
		}
	}()
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
	set = true
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
	t.standIn = ""
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

// mergeEndsLocked takes a LastEnds read: each scope keeps the last
// (LastEnd.Supersedes) of its read end and one already noted, and each Fresh window a read end opens
// that is still ahead is opened, so rule 4 holds across a restart.
func (e *Executor[P]) mergeEndsLocked(ends map[ScheduledScope]LastEnd) {
	now := time.Now()
	for key, end := range ends {
		if have, ok := e.ends[key]; !ok || end.Supersedes(have) {
			e.ends[key] = end
		}
		if spec := e.kinds[key.Kind]; spec != nil && spec.Fresh != nil {
			if until := end.At.Add(spec.Fresh(key.Scope)); until.After(now) && until.After(e.recent[key]) {
				e.recent[key] = until
			}
		}
	}
	e.boundEndsLocked()
}

// boundEndsLocked drops ends past maxEnds, keeping each one a scheduled scope
// counts from: first those of a kind with Kept 0, which no restart reads,
// then the oldest, the least Kind then Scope first at one instant.
func (e *Executor[P]) boundEndsLocked() {
	over := len(e.ends) - maxEnds
	if over <= 0 {
		return
	}
	leads := map[ScheduledScope]bool{}
	for _, t := range e.scheduled {
		leads[t.lead] = true
	}
	drop := make([]ScheduledScope, 0, len(e.ends))
	for k := range e.ends {
		if !leads[k] {
			drop = append(drop, k)
		}
	}
	slices.SortFunc(drop, func(a, b ScheduledScope) int {
		return cmp.Or(cmp.Compare(e.storedRank(a.Kind), e.storedRank(b.Kind)),
			e.ends[a].At.Compare(e.ends[b].At), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Scope, b.Scope))
	})
	for _, k := range drop[:min(over, len(drop))] {
		delete(e.ends, k)
	}
}

// storedRank answers 0 for a kind with Kept 0, whose ends a store holds no
// row for, else 1. An end of a kind the executor does not know came from a
// LastEnds read, so a restart reads it too.
func (e *Executor[P]) storedRank(k Kind) int {
	if spec := e.kinds[k]; spec != nil && spec.Kept == 0 {
		return 0
	}
	return 1
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
// noEndBaseLocked. It reads only ends, which LastEnds answers alike, and the
// cadence, so a restart counts alike. It reports whether the base or the
// wait moved.
func (e *Executor[P]) baseLocked(key ScheduledScope, t *timed) (moved bool) {
	from, end, ok := e.lastEndLocked(key)
	base, done, lead := t.changed, true, ScheduledScope{}
	switch {
	case ok && !end.At.Before(t.changed):
		base, done, lead = end.At, workDone(end.Result), from
	case !ok:
		base = e.noEndBaseLocked(t)
	}
	moved = !base.Equal(t.base) || done != t.done
	t.base, t.done, t.lead = base, done, lead
	if lead != (ScheduledScope{}) {
		t.watch.Beat(base)
	} else {
		t.watch.from(base)
	}
	return moved
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

// noteEndLocked keeps a started job's moving end at at as its scope's last
// end, then counts again each scheduled scope it is or covers (baseLocked),
// re-arming one whose base or wait moved, so an end noted after a later one
// moves nothing.
func (e *Executor[P]) noteEndLocked(en *job[P], r Result, at time.Time) {
	key := ScheduledScope{Kind: en.j.Kind, Scope: en.j.Scope}
	e.keepEndLocked(key, LastEnd{At: at, Result: r})
	if !e.endsRead {
		return
	}
	for k, t := range e.scheduled {
		if (k == key || e.coversLocked(key, k)) && e.baseLocked(k, t) {
			e.armLocked(k, t)
			e.watchLocked(k, t)
		}
	}
}

// keepEndLocked keeps end as key's last end, and as one noted during a
// resume's read, when it supersedes the one held (LastEnd.Supersedes).
func (e *Executor[P]) keepEndLocked(key ScheduledScope, end LastEnd) {
	if have, ok := e.ends[key]; !ok || end.Supersedes(have) {
		e.ends[key] = end
	}
	if have, ok := e.noted[key]; e.noted != nil && (!ok || end.Supersedes(have)) {
		e.noted[key] = end
	}
	e.boundEndsLocked()
}

// wake submits a scheduled scope's timer request, then arms the next wake
// unless an end or a change armed it while Start ran. A refusal waits
// Retry's d, or the interval, from the refusal: a refusal code's Retry-After
// is for an API caller and never sets this timer.
func (e *Executor[P]) wake(key ScheduledScope, t *timed, gen uint64) {
	j := Job[P]{Kind: key.Kind, Scope: key.Scope, Trigger: TriggerSchedule}
	e.mu.Lock()
	if e.scheduled[key] != t || t.gen != gen || e.closed {
		e.mu.Unlock()
		return
	}
	served := e.endedProposalLocked(&j)
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()

	s, err := e.Start(context.Background(), j)
	var recent *RecentError
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scheduled[key] != t || t.gen != gen {
		return
	}
	now := time.Now()
	switch {
	case err == nil && s.Job == served:
		// A held proposal whose job had ended answered it: a served request,
		// not a refusal, so the wait is the one the last counted end set.
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(t.done), jitter)))
	case err == nil && e.findLocked(s.Job) == nil:
		// It ended after served was read, with an end that moved no base
		// (gen), so that end counts for nothing here, as in releaseStandInLocked.
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(false), jitter)))
	case err == nil:
		// While the job runs this wake stands in for the next, so a request
		// longer than the interval coalesces; its end replaces it
		// (noteEndLocked, or releaseStandInLocked for an end that moved no base).
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(true), jitter)))
		t.standIn = s.Job
	case errors.As(err, &recent):
		e.armAtLocked(key, t, recent.Until)
	case errors.Is(err, ErrQueueFull), errors.Is(err, ErrStoreUnavailable), errors.Is(err, ErrCapacity):
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(false), jitter)))
	default:
		e.log.Error("scheduled job not started", "kind", key.Kind, "scope", key.Scope, "err", err)
		e.armAtLocked(key, t, now.Add(JitteredDelay(t.cadence.wait(false), jitter)))
	}
}

// releaseStandInLocked re-arms each scheduled scope whose stand-in wake waits
// on job id, which ended at at. A caller notes a moving end first
// (noteEndLocked), which re-arms a scope whose base it moved and so clears its
// standIn, so the end moved no base of a scope left; it is held in memory
// only, like a refusal. The next wake is at plus Retry's d, or the interval,
// so a cancel or a covering end that counts for nothing never re-runs the job
// at once.
func (e *Executor[P]) releaseStandInLocked(id ID, at time.Time) {
	for key, t := range e.scheduled {
		if t.standIn == id {
			e.armAtLocked(key, t, at.Add(JitteredDelay(t.cadence.wait(false), jitter)))
		}
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
