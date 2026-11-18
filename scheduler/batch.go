package scheduler

import "context"

// Item runs a batch's item i: it takes the kind's slot in Batch class, runs
// fn and records the Line fn answers. It answers ctx's error when the job is
// cancelled before the item runs, and refuses an i outside the batch.
func (r *Run[P]) Item(ctx context.Context, i int, fn func(ctx context.Context) Line) error {
	return errStub
}
