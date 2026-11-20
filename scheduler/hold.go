package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"
)

// holdWait is how long Hold(all) waits for the jobs it cancelled and the
// record writes in flight.
const holdWait = 10 * time.Second

// Hold quiets the executor for maintenance. With all, it stops the timers,
// ends queued jobs interrupted, drops the held proposals and cancels each
// running or yielded job (interrupted unless committing), answering once they
// and each record write in flight ended, or 10 seconds passed. Until resume,
// Start answers ErrQueueFull with no record; resume, safe to call twice,
// re-reads Records.LastEnds (retried as at Run's start) and counts each Fresh
// window and scheduled scope from it or a later cadence change. Without all
// nothing changes; a ctx ending first or mid-wait answers its error, holding nothing.
func (e *Executor[P]) Hold(ctx context.Context, all bool) (resume func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !all {
		return func() {}, nil
	}
	e.mu.Lock()
	e.writeLocked()
	dequeued, waits := e.quietLocked(End{Result: ResultInterrupted})
	e.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { e.resume(context.WithoutCancel(ctx)) }) }
	handed := false
	defer func() {
		// On ctx's end, and on a panic out of app code in a dequeued job's
		// end: the caller holds no resume then.
		if !handed {
			release()
		}
	}()
	e.endDequeued(ctx, dequeued)

	wait := time.NewTimer(holdWait)
	defer wait.Stop()
	for _, done := range waits {
		select {
		case <-done:
		case <-wait.C:
			handed = true
			return release, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	handed = true
	return release, nil
}

// quietLocked opens a hold: it stops the timers, drops the proposals, stops
// each running job not in its commit step with end and takes every queued
// job out of the queue to end with end. It answers the dequeued jobs, whose
// ends the caller writes, and what the hold waits on: each running job's
// finished channel and idle.
func (e *Executor[P]) quietLocked(end End) (dequeued []*job[P], waits []<-chan struct{}) {
	e.holds++
	e.readGen++ // ends an earlier resume's read and its retryResume
	e.noted = nil
	e.stopTimersLocked()
	clear(e.proposals)
	for _, en := range e.live {
		switch en.state {
		case stateQueued:
			dequeued = append(dequeued, en)
		case stateRunning:
			if !en.stopped && !en.committing && !en.returned {
				e.stopLocked(en, end)
			}
			waits = append(waits, en.finished)
		case stateAdmitting, stateEnded:
		}
	}
	for _, en := range dequeued {
		e.dequeueLocked(en, end)
	}
	return dequeued, append(waits, e.idle)
}

// resume ends one hold. The last one re-reads the last ends, since the file
// they came from may have been replaced, and rebuilds the Fresh windows and
// every scheduled scope's count from them and from the ends noted during the
// read, which the reopened store holds too (resumeEndsLocked). A
// failed read arms no timer and counts each deadline from now until
// retryResume's read answers; a hold during the read discards it.
func (e *Executor[P]) resume(ctx context.Context) {
	gen, read := e.unhold()
	if !read {
		return
	}
	taken := false
	defer func() {
		if !taken {
			e.untaken(ctx, gen)
		}
	}()
	ends, err := e.lastEnds(ctx)
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readGen == gen {
		e.recountLocked(now)
		if err == nil {
			e.resumeEndsLocked(ends)
		} else {
			e.readFailedLocked(ctx, err)
		}
	}
	taken = true
}

// unhold ends one hold and answers whether resume reads the last ends: the
// last hold ended and Run's own read has answered.
func (e *Executor[P]) unhold() (gen uint64, read bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.holds--
	if e.holds > 0 || !e.endsRead {
		if e.holds == 0 {
			// readEnds is still retrying: the deadline checks go on meanwhile.
			for key, t := range e.scheduled {
				e.watchLocked(key, t)
			}
		}
		return 0, false
	}
	e.noted = map[ScheduledScope]LastEnd{}
	return e.readGen, true
}

// recountLocked drops the ends held before the hold but those noted during
// the read, and counts each Fresh window and deadline from now.
func (e *Executor[P]) recountLocked(now time.Time) {
	e.ends = map[ScheduledScope]LastEnd{}
	clear(e.recent)
	e.mergeEndsLocked(e.noted)
	e.noted = nil
	e.countFrom = now
	for _, t := range e.scheduled {
		t.watch = NewWatch(t.lease)
	}
}

// errResumePanicked stands for a resume's read that a panic out of
// Records.LastEnds, a kind's Fresh or its Covers left untaken.
var errResumePanicked = errors.New("scheduler: a call into the app panicked while the resume took the last ends")

// untaken retries as a failed one a resume's read that a panic left
// untaken, unless a later hold superseded it, so every scope is armed once a
// retry answers.
func (e *Executor[P]) untaken(ctx context.Context, gen uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readGen != gen {
		return
	}
	if e.noted != nil {
		e.recountLocked(time.Now())
	}
	e.stopTimersLocked()
	e.readFailedLocked(ctx, errResumePanicked)
}

// readFailedLocked arms no timer until retryResume's read answers, and
// checks each deadline meanwhile.
func (e *Executor[P]) readFailedLocked(ctx context.Context, err error) {
	e.stale = true
	e.log.Error("last job ends not read at resume; scheduled scopes wait for a read", "err", err, "retry_in", readBackoff(1))
	for key, t := range e.scheduled {
		e.watchLocked(key, t)
	}
	if !e.closed {
		e.wg.Add(1)
		go e.retryResume(ctx, e.readGen)
	}
}

// retryResume retries a resume's failed LastEnds read on readBackoff until
// one answers, Run ends or a later hold supersedes it (readGen moved).
func (e *Executor[P]) retryResume(ctx context.Context, gen uint64) {
	defer e.wg.Done()
	for n := 1; pause(e.stop, readBackoff(n)); n++ {
		if !e.resumeCurrent(gen) {
			return
		}
		ends, err := e.lastEnds(ctx)
		if !e.retried(gen, ends, err) {
			return
		}
		e.log.Error("last job ends not read at resume; scheduled scopes wait for a read", "err", err, "retry_in", readBackoff(n+1))
	}
}

// retried takes a retried read's ends while gen's resume is current, and
// reports whether the read is to be retried again. mu is released by defer,
// since Fresh and Covers are called under it.
func (e *Executor[P]) retried(gen uint64, ends map[ScheduledScope]LastEnd, err error) (again bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.readGen == gen && e.holds == 0 && !e.closed
	if current && err == nil {
		e.resumeEndsLocked(ends)
	}
	return current && err != nil
}

func (e *Executor[P]) resumeCurrent(gen uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readGen == gen && e.holds == 0 && !e.closed
}

// resumeEndsLocked takes a resume's LastEnds read (mergeEndsLocked) and
// counts every scheduled scope again (baseLocked) and arms it.
func (e *Executor[P]) resumeEndsLocked(ends map[ScheduledScope]LastEnd) {
	e.mergeEndsLocked(ends)
	e.stale = false
	for key, t := range e.scheduled {
		e.baseLocked(key, t)
		e.armLocked(key, t)
		e.watchLocked(key, t)
	}
}
