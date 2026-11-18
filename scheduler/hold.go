package scheduler

import (
	"context"
	"maps"
	"sync"
	"time"
)

// holdWait is how long Hold(all) waits for the running jobs it cancelled.
const holdWait = 10 * time.Second

// Hold quiets the executor for maintenance. With all, it stops the timers, ends
// each queued job interrupted, drops the held proposals, cancels each running
// or yielded job, which ends interrupted unless in its commit step, and answers
// once they have ended or 10 seconds have passed. Start answers ErrQueueFull
// with no record until resume, which re-reads Records.LastEnds and counts each
// Fresh window and scheduled scope from it, or from a later cadence change.
// Without all nothing changes. An ended ctx, or one ending in the wait, answers
// its error and holds nothing; resume is safe to call twice.
func (e *Executor[P]) Hold(ctx context.Context, all bool) (resume func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !all {
		return func() {}, nil
	}
	end := End{Result: ResultInterrupted}
	e.mu.Lock()
	dequeued, running := e.quietLocked(end)
	e.mu.Unlock()
	for _, en := range dequeued {
		e.ended(ctx, en, end)
	}

	var once sync.Once
	resume = func() { once.Do(func() { e.resume(context.WithoutCancel(ctx)) }) }
	wait := time.NewTimer(holdWait)
	defer wait.Stop()
	for _, finished := range running {
		select {
		case <-finished:
		case <-wait.C:
			return resume, nil
		case <-ctx.Done():
			resume()
			return nil, ctx.Err()
		}
	}
	return resume, nil
}

// quietLocked opens a hold: it stops the timers, drops the proposals, stops
// each running job not in its commit step with end and takes every queued
// job out of the queue to end with end. It answers the dequeued jobs, whose
// ends the caller writes, and each running job's finished channel.
func (e *Executor[P]) quietLocked(end End) (dequeued []*job[P], running []<-chan struct{}) {
	e.holds++
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
			running = append(running, en.finished)
		case stateAdmitting, stateEnded:
		}
	}
	for _, en := range dequeued {
		e.dequeueLocked(en, end)
	}
	return dequeued, running
}

// resume ends one hold. The last one re-reads the last ends, since the file
// they came from may have been replaced, and rebuilds the Fresh windows and
// every scheduled scope's count from them (baseLocked).
func (e *Executor[P]) resume(ctx context.Context) {
	e.mu.Lock()
	e.holds--
	if e.holds > 0 || !e.endsRead {
		// readEnds arms the timers once Run has read the ends.
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	ends := e.lastEnds(ctx)
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.holds > 0 {
		return
	}
	e.ends = map[ScheduledScope]LastEnd{}
	maps.Copy(e.ends, ends)
	clear(e.recent)
	for key, end := range ends {
		if spec := e.kinds[key.Kind]; spec != nil && spec.Fresh != nil {
			if until := end.At.Add(spec.Fresh(key.Scope)); until.After(now) {
				e.recent[key] = until
			}
		}
	}
	e.countFrom = now
	for key, t := range e.scheduled {
		t.watch = NewWatch(t.lease)
		e.baseLocked(key, t)
		e.armLocked(key, t)
		e.watchLocked(key, t)
	}
}
