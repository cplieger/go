package scheduler

import "context"

// Item runs a batch's item i: it takes the kind's slot in Batch class, runs
// fn and records the Line fn answers, which must be done, skipped or review.
// It answers ctx's error when the job is cancelled before the item runs and
// ErrUnknownLine for an i outside the batch; a repeat of an ended line runs
// nothing and answers nil. A line still queued at the job's end goes to
// review: cancelled or interrupted with such an end, else not_run, and an ok
// end then reads partial.
func (r *Run[P]) Item(ctx context.Context, i int, fn func(ctx context.Context) Line) error {
	return errStub
}
