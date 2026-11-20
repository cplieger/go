package scheduler

import (
	"context"
	"time"
)

// maxYield is how far ahead a Yield may wait.
const maxYield = 24 * time.Hour

// yielding is a yielded job's wait. Its fields are guarded by the executor's
// mu; wake and covered are closed at most once, by Wake and by a covering
// start.
type yielding struct {
	until   time.Time     // when it takes its slot back; Wake moves it to now
	wake    chan struct{} // closed by Wake
	covered chan struct{} // closed once by is set
	by      ID            // the covering job
	woken   bool
}

// Yield frees the job's slot, keeps its record running at its phase and waits
// for until or a Wake, then takes the slot back after any waiting Interactive
// job and ahead of Automatic and Batch ones. It answers nil once the slot is
// the job's again, *CoveredError when a job that covers it started before any
// cancel, ctx's error on a cancel, and ErrYieldTooLong, still holding the
// slot, for an until more than 24 hours ahead. A job of a kind with no slot
// only waits, and so does a batch's Run, whose items take the slot.
func (r *Run[P]) Yield(ctx context.Context, until time.Time) error {
	if time.Until(until) > maxYield {
		return ErrYieldTooLong
	}
	e, en := r.e, r.en
	y := &yielding{until: until, wake: make(chan struct{}), covered: make(chan struct{})}
	e.mu.Lock()
	en.yield = y
	held := en.w
	en.w = nil
	e.freeLocked()
	e.moved()
	e.mu.Unlock()
	if held != nil {
		en.slot.drop(held)
	}

	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-t.C:
	case <-y.wake:
	case <-y.covered:
		return e.unyield(en, nil)
	case <-ctx.Done():
	}
	// select picks at random among ready arms, so a timer or Wake ready
	// together with a cancel must not answer nil.
	if ctx.Err() != nil {
		return e.cancelled(ctx, en, nil)
	}
	if !en.waits() {
		return e.unyield(en, nil)
	}
	w := en.slot.join(tierYielded)
	select {
	case <-w.ready:
	case <-y.covered:
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		return e.cancelled(ctx, en, w)
	}
	err := e.unyield(en, w)
	if err == nil && e.observer.SlotWait != nil {
		e.observer.SlotWait(en.spec.Slot, en.class, en.slot.waited(w))
	}
	return err
}

// unyield ends en's wait, giving it w as its slot, and answers *CoveredError
// when a covering start came first, in which case w is passed on: a grant and
// a cover that arrive together resolve to the cover, so a covered job never
// goes on to commit.
func (e *Executor[P]) unyield(en *job[P], w *waiter) error {
	e.mu.Lock()
	by := en.yield.by
	en.yield = nil
	if by == "" {
		en.w = w
	}
	e.moved()
	e.mu.Unlock()
	if by == "" {
		return nil
	}
	if w != nil {
		en.slot.drop(w)
	}
	return &CoveredError{By: by}
}

// cancelled ends en's wait once its context ended, giving up w, and answers
// ctx's error, or *CoveredError when a covering start came before the cancel.
func (e *Executor[P]) cancelled(ctx context.Context, en *job[P], w *waiter) error {
	if w != nil {
		en.slot.drop(w)
	}
	if err := e.unyield(en, nil); err != nil {
		return err
	}
	return ctx.Err()
}

// Wake brings a yielded job's return to its slot forward to now, which Live
// then answers as its Until. It answers false when id is not yielded, a
// cancelled or covered job's wait included.
func (e *Executor[P]) Wake(id ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	en := e.findLocked(id)
	if en == nil || en.yield == nil || en.yield.by != "" || en.stopped {
		return false
	}
	if y := en.yield; !y.woken {
		y.woken = true
		if now := time.Now(); now.Before(y.until) {
			y.until = now
		}
		close(y.wake)
		e.moved()
	}
	return true
}

// coverYieldedLocked ends the wait of each yielded job the starting job's kind
// covers; each Yield answers *CoveredError by it.
func (e *Executor[P]) coverYieldedLocked(en *job[P]) {
	for _, q := range e.live {
		if y := q.yield; y != nil && y.by == "" && !q.stopped && en.spec.Covers(en.j, q.j) {
			y.by = en.j.ID
			close(y.covered)
		}
	}
}
