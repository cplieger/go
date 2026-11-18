package scheduler

import (
	"sync"
	"time"

	"github.com/cplieger/go/health/v2"
)

// Watch is one deadline: the latest beat plus a lease, counted from NewWatch
// until the first Beat. The executor holds one per scheduled scope; a loop
// that is not a job holds its own. A Watch is safe for concurrent use.
type Watch struct {
	start time.Time
	last  time.Time
	lease time.Duration
	mu    sync.Mutex
}

// NewWatch starts a watch whose deadline is now plus l.Duration(). A lease
// whose Duration is 0 puts the deadline at the beat itself.
func NewWatch(l health.Lease) *Watch {
	return &Watch{start: time.Now(), lease: l.Duration()}
}

// Beat records that a pass which committed ended at at, so the deadline
// becomes at plus the lease. Call it only after the pass's last write
// committed; a beat older than the latest one changes nothing.
func (w *Watch) Beat(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last.IsZero() || at.After(w.last) {
		w.last = at
	}
}

// Fresh answers a Signal that is healthy until the deadline has passed. It
// reads the watch at each call, so one Signal follows every later Beat.
func (w *Watch) Fresh() health.Signal {
	return health.SignalFunc(func() bool { return !time.Now().After(w.Deadline()) })
}

// Deadline answers the time by which the next pass must have ended.
func (w *Watch) Deadline() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	from := w.last
	if from.IsZero() {
		from = w.start
	}
	return from.Add(w.lease)
}
