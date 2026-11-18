package scheduler

import (
	"context"
	"fmt"
)

// Cancel ends a queued job cancelled without running, and cancels a running
// or yielded job's context so it ends cancelled at the phase it reached,
// committing nothing. stopping is true for a job that still has to return. It
// answers ErrCommitting once the job's Commit answered true, ErrNotCancellable
// for an ended job and ErrUnknownJob for an unknown id; a repeat answers as
// the first cancel did. A job of a kind that keeps no record is unknown once
// it has ended. ErrStoreUnavailable wraps a failed read of an ended job's
// record.
func (e *Executor[P]) Cancel(ctx context.Context, id ID) (stopping bool, err error) {
	e.mu.Lock()
	en := e.findLocked(id)
	switch {
	case en == nil:
		e.mu.Unlock()
		return e.cancelledBefore(ctx, id)
	case en.state == stateAdmitting:
		e.mu.Unlock()
		return false, ErrUnknownJob
	case en.state == stateEnded:
		// Left the queue; its end is being written.
		e.mu.Unlock()
		if en.stopEnd.Result == ResultCancelled {
			return false, nil
		}
		return false, ErrNotCancellable
	case en.state == stateQueued:
		end := End{Result: ResultCancelled}
		e.dequeueLocked(en, end)
		e.mu.Unlock()
		e.ended(ctx, en, end)
		return false, nil
	}
	defer e.mu.Unlock()
	switch {
	case en.stopped:
		return true, nil
	case en.returned:
		return false, ErrNotCancellable
	case en.committing:
		return false, ErrCommitting
	}
	e.stopLocked(en, &End{Result: ResultCancelled})
	return true, nil
}

// cancelledBefore answers a Cancel of a job that is no longer live from its
// record: a repeat of a cancel that ended it, else ErrNotCancellable.
func (e *Executor[P]) cancelledBefore(ctx context.Context, id ID) (stopping bool, err error) {
	if e.records == nil {
		return false, ErrUnknownJob
	}
	rec, ok, err := e.records.Get(ctx, id)
	switch {
	case err != nil:
		return false, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	case !ok:
		return false, ErrUnknownJob
	case rec.End != nil && rec.End.Result == ResultCancelled:
		return !rec.StartedAt.IsZero(), nil
	}
	return false, ErrNotCancellable
}

// CredentialEnded stops each queued or running job of an OnBehalf kind that
// credential started: it ends cancelled with reason credential_ended. A job
// in its commit step completes.
func (e *Executor[P]) CredentialEnded(credential string) {
	if credential == "" {
		return
	}
	end := End{Result: ResultCancelled, Reasons: []Reason{{Code: ReasonCredentialEnded}}}
	var dequeued []*job[P]
	e.mu.Lock()
	for _, en := range e.live {
		if !en.spec.OnBehalf || en.j.Owner != credential {
			continue
		}
		switch {
		case en.state == stateQueued:
			dequeued = append(dequeued, en)
		case en.state == stateRunning && !en.stopped && !en.returned && !en.committing:
			e.stopLocked(en, &end)
		}
	}
	for _, en := range dequeued {
		e.dequeueLocked(en, end)
	}
	e.mu.Unlock()
	for _, en := range dequeued {
		e.ended(context.Background(), en, end)
	}
}
