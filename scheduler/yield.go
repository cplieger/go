package scheduler

import (
	"context"
	"time"
)

// Yield frees the job's slot, keeps its record running at its phase and waits
// for until or a Wake. It answers nil once the slot is the job's again,
// *CoveredError when a job that covers it started, ctx's error on a cancel,
// and ErrYieldTooLong, still holding the slot, for an until more than 24
// hours ahead. A job of a kind with no slot only waits.
func (r *Run[P]) Yield(ctx context.Context, until time.Time) error {
	return errStub
}

// Wake brings a yielded job's return to its slot forward to now. It answers
// false when id is not yielded.
func (e *Executor[P]) Wake(id ID) bool { return false }
