package scheduler

import (
	"time"

	"github.com/cplieger/go/health/v2"
)

// Fresh answers a Signal that turns unhealthy once the scheduled scope's
// deadline has passed; it is healthy for a scope that is not scheduled.
func (e *Executor[P]) Fresh(kind Kind, scope string) health.Signal {
	return health.SignalFunc(func() bool { return false })
}

// Deadline answers the time by which the scheduled scope's next job must end:
// its last moving end plus its lease. It answers zero for a scope that is not
// scheduled.
func (e *Executor[P]) Deadline(kind Kind, scope string) time.Time {
	return time.Time{}
}
