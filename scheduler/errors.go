package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The executor's errors.
var (
	ErrUnknownKind   = errors.New("scheduler: unknown kind")
	ErrScope         = errors.New("scheduler: scope outside its grammar")
	ErrTooManyScopes = errors.New("scheduler: more than 64 scheduled scopes")
	ErrLease         = errors.New("scheduler: a kind's watch lease is zero")  // Schedule
	ErrCovered       = errors.New("scheduler: covered by a job that started") // matched through *CoveredError
	ErrYieldTooLong  = errors.New("scheduler: yield more than 24 hours ahead")
	ErrBatchSize     = errors.New("scheduler: batch item count outside 1 to MaxItems") // Start; the wire refuses the count first
	ErrProposed      = errors.New("scheduler: job already proposed")                   // Run.Propose a second time in one job

	ErrQueueFull        = errors.New("scheduler: queue full")               // queue_full
	ErrCapacity         = errors.New("scheduler: no room for a record")     // job_capacity; Records.Insert
	ErrRecent           = errors.New("scheduler: ran recently")             // job_recent, matched through *RecentError
	ErrUnknownJob       = errors.New("scheduler: unknown job")              // job_unknown; Cancel, Records.Get, Records.Dismiss
	ErrNotCancellable   = errors.New("scheduler: job not cancellable")      // job_not_cancellable
	ErrCommitting       = errors.New("scheduler: job is committing")        // job_committing
	ErrUnknownLine      = errors.New("scheduler: unknown line")             // line_unknown; Records.Dismiss
	ErrStoreUnavailable = errors.New("scheduler: record store unavailable") // wraps a failed Records call: an admission write, or Cancel's read of a job no longer live; store_unavailable
)

// storeFault wraps a failed Records call as ErrStoreUnavailable. A context
// error stays as it is: it is the caller's own ending, not a store fault.
func storeFault(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
}

// CoveredError is Yield's answer when a job whose kind covers the yielded one
// started. errors.Is(err, ErrCovered) holds for it.
type CoveredError struct{ By ID }

func (e *CoveredError) Error() string { return ErrCovered.Error() + ": " + string(e.By) }

// Is reports whether target is ErrCovered.
func (*CoveredError) Is(target error) bool { return target == ErrCovered }

// RecentError refuses a request under its kind's Fresh window, which ends at
// Until. errors.Is(err, ErrRecent) holds for it.
type RecentError struct{ Until time.Time }

func (e *RecentError) Error() string {
	return ErrRecent.Error() + " until " + e.Until.UTC().Format(time.RFC3339Nano)
}

// Is reports whether target is ErrRecent.
func (*RecentError) Is(target error) bool { return target == ErrRecent }
