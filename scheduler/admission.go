package scheduler

import "context"

// Start admits j, writing its record before it is queued. A queued job of the
// same kind and scope, a queued job that covers it, or a running one inside
// the kind's Fresh window answers it instead (Joined). It answers *RecentError
// under Fresh after a recent end, ErrQueueFull past a queue bound (its record
// ended skipped, queue_full), ErrStoreUnavailable when the record cannot be
// written, ErrBatchSize for a batch whose item count is outside 1 to
// MaxItems, ErrUnknownKind and ErrScope. A batch's record holds one queued
// line per item.
func (e *Executor[P]) Start(ctx context.Context, j Job[P]) (Started, error) {
	return Started{}, errStub
}
