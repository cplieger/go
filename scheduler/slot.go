package scheduler

import (
	"context"
	"slices"
	"sync"
	"time"
)

// tier is a slot's queue. A yielded job whose until came waits between the
// Interactive and the alternating Automatic and Batch tiers.
type tier uint8

const (
	tierInteractive tier = iota
	tierYielded
	tierAutomatic
	tierBatch
	tiers
)

func tierOf(c Class) tier {
	switch c {
	case Automatic:
		return tierAutomatic
	case Batch:
		return tierBatch
	}
	return tierInteractive
}

// waiter is one take of a slot. state is guarded by the slot's mu.
type waiter struct {
	ready   chan struct{} // closed at the grant
	joined  time.Time
	granted time.Time
	state   waiterState
	tier    tier
}

type waiterState uint8

const (
	waiting waiterState = iota
	granted
	dropped
)

// slot runs one job or batch item of a named slot at a time and never
// preempts it.
type slot struct {
	queues [tiers][]*waiter // FIFO per tier
	mu     sync.Mutex
	busy   bool
	shared tier // the Automatic or Batch tier served last
}

func newSlot() *slot { return &slot{} }

// acquire waits for the slot in class c and answers its release, which is
// safe to call more than once; a ctx that ends first answers its error.
func (s *slot) acquire(ctx context.Context, c Class) (func(), error) {
	return s.wait(ctx, s.join(tierOf(c)))
}

// wait waits for w's grant and answers its release; a ctx that ends first
// drops w, passing on a grant that came as it ended.
func (s *slot) wait(ctx context.Context, w *waiter) (func(), error) {
	select {
	case <-w.ready:
		return func() { s.drop(w) }, nil
	case <-ctx.Done():
		s.drop(w)
		return nil, ctx.Err()
	}
}

// join queues a take in tier t, granted at once when the slot is free.
func (s *slot) join(t tier) *waiter {
	w := &waiter{ready: make(chan struct{}), joined: time.Now(), tier: t}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.busy {
		s.busy = true
		s.grantLocked(w)
		return w
	}
	s.queues[t] = append(s.queues[t], w)
	return w
}

// drop ends w: a waiting take leaves its queue and a granted one hands the
// slot to the next waiter. A second drop does nothing.
func (s *slot) drop(w *waiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch w.state {
	case waiting:
		q := s.queues[w.tier]
		if i := slices.Index(q, w); i >= 0 {
			s.queues[w.tier] = slices.Delete(q, i, i+1)
		}
	case granted:
		s.handLocked()
	case dropped:
	}
	w.state = dropped
}

// waited answers how long w waited for its grant.
func (s *slot) waited(w *waiter) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return w.granted.Sub(w.joined)
}

// ahead counts the takes the slot serves before w; 0 once w holds it.
func (s *slot) ahead(w *waiter) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.state != waiting {
		return 0
	}
	switch w.tier {
	case tierInteractive:
		return slices.Index(s.queues[tierInteractive], w)
	case tierYielded:
		return len(s.queues[tierInteractive]) + slices.Index(s.queues[tierYielded], w)
	}
	n := len(s.queues[tierInteractive]) + len(s.queues[tierYielded])
	auto, batch, shared := s.queues[tierAutomatic], s.queues[tierBatch], s.shared
	for {
		t, ok := alternate(len(auto) > 0, len(batch) > 0, shared)
		if !ok {
			return n
		}
		shared = t
		next := &auto
		if t == tierBatch {
			next = &batch
		}
		if (*next)[0] == w {
			return n
		}
		*next = (*next)[1:]
		n++
	}
}

// handLocked grants the slot to the next waiter in tier order, or frees it.
func (s *slot) handLocked() {
	t, ok := s.nextLocked()
	if !ok {
		s.busy = false
		return
	}
	w := s.queues[t][0]
	s.queues[t] = s.queues[t][1:]
	s.grantLocked(w)
}

func (s *slot) grantLocked(w *waiter) {
	w.state = granted
	w.granted = time.Now()
	if w.tier == tierAutomatic || w.tier == tierBatch {
		s.shared = w.tier
	}
	close(w.ready)
}

func (s *slot) nextLocked() (tier, bool) {
	for _, t := range []tier{tierInteractive, tierYielded} {
		if len(s.queues[t]) > 0 {
			return t, true
		}
	}
	return alternate(len(s.queues[tierAutomatic]) > 0, len(s.queues[tierBatch]) > 0, s.shared)
}

// alternate picks Automatic or Batch, one each while both wait.
func alternate(auto, batch bool, shared tier) (tier, bool) {
	switch {
	case auto && batch && shared == tierAutomatic:
		return tierBatch, true
	case auto:
		return tierAutomatic, true
	case batch:
		return tierBatch, true
	}
	return 0, false
}
