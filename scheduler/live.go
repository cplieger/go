package scheduler

import "time"

// LiveJob is the in-memory state of a queued, running or yielded job, which
// its record does not hold.
type LiveJob struct {
	Until       time.Time // a yielded job's until, now once woken; zero otherwise
	Position    int       // jobs queued ahead of it; 0 once it runs
	Done, Total int       // its Run.Progress; 0 and 0 until it reports
}

// Live answers the in-memory state of each queued, running or yielded job;
// a job that is not live is absent.
func (e *Executor[P]) Live() map[ID]LiveJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[ID]LiveJob, len(e.live))
	for _, en := range e.live {
		l := LiveJob{Done: en.done, Total: en.total}
		switch en.state {
		case stateQueued:
			l.Position = en.position()
		case stateRunning:
			if en.yield != nil {
				l.Until = en.yield.until
			}
		case stateAdmitting, stateEnded:
			continue
		}
		out[en.j.ID] = l
	}
	return out
}

// movedEvery is the shortest gap between two Observer.Moved calls.
const movedEvery = time.Second

// moved tells relayMoves that a count Live answers changed. It never blocks,
// so it may be called holding any lock.
func (e *Executor[P]) moved() {
	if e.observer.Moved == nil {
		return
	}
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// relayMoves calls Observer.Moved for each change kicked, then waits a
// second; a change kicked meanwhile is the trailing call. It returns when
// stop closes.
func (e *Executor[P]) relayMoves(stop <-chan struct{}) {
	for {
		select {
		case <-e.kick:
		case <-stop:
			return
		}
		e.observer.Moved()
		t := time.NewTimer(movedEvery)
		select {
		case <-t.C:
		case <-stop:
			t.Stop()
			return
		}
	}
}
