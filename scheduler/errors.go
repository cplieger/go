package scheduler

import (
	"errors"
	"time"
)

// The executor's errors. jobhttp maps each to its wire code one to one.
var (
	ErrUnknownKind   = errors.New("scheduler: unknown kind")
	ErrScope         = errors.New("scheduler: scope outside its grammar")
	ErrTooManyScopes = errors.New("scheduler: more than 64 scheduled scopes")
	ErrCovered       = errors.New("scheduler: covered by a job that started") // matched through *CoveredError
	ErrYieldTooLong  = errors.New("scheduler: yield more than 24 hours ahead")
	ErrBatchSize     = errors.New("scheduler: batch item count outside 1 to MaxItems") // Start; the wire refuses the count first

	ErrQueueFull        = errors.New("scheduler: queue full")               // queue_full
	ErrCapacity         = errors.New("scheduler: no room for a record")     // job_capacity; Records.Insert
	ErrRecent           = errors.New("scheduler: ran recently")             // job_recent, matched through *RecentError
	ErrUnknownJob       = errors.New("scheduler: unknown job")              // job_unknown; Cancel, Records.Get, Records.Dismiss
	ErrNotCancellable   = errors.New("scheduler: job not cancellable")      // job_not_cancellable
	ErrCommitting       = errors.New("scheduler: job is committing")        // job_committing
	ErrUnknownLine      = errors.New("scheduler: unknown line")             // line_unknown; Records.Dismiss
	ErrStoreUnavailable = errors.New("scheduler: record store unavailable") // wraps a failed Records write at admission; store_unavailable
)

// CoveredError is Yield's answer when a job whose kind covers the yielded one
// started. errors.Is(err, ErrCovered) holds for it.
type CoveredError struct{ By ID }

func (e *CoveredError) Error() string { return ErrCovered.Error() + ": " + string(e.By) }

// Is reports whether target is ErrCovered.
func (e *CoveredError) Is(target error) bool { return target == ErrCovered }

// RecentError refuses a request under its kind's Fresh window, which ends at
// Until. errors.Is(err, ErrRecent) holds for it.
type RecentError struct{ Until time.Time }

func (e *RecentError) Error() string {
	return ErrRecent.Error() + " until " + e.Until.UTC().Format(time.RFC3339Nano)
}

// Is reports whether target is ErrRecent.
func (e *RecentError) Is(target error) bool { return target == ErrRecent }
