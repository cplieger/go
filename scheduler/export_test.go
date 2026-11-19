package scheduler

import (
	"context"
	"time"
)

// Slot is the class-ordered slot, for the black-box tests.
type Slot struct{ s *slot }

// NewSlot answers a free slot.
func NewSlot() *Slot { return &Slot{s: newSlot(func() {})} }

// Acquire waits for the slot in class c.
func (s *Slot) Acquire(ctx context.Context, c Class) (func(), error) { return s.s.acquire(ctx, c) }

// Timing is what a scheduled scope's next wake counts from.
type Timing struct {
	Base, Changed time.Time
	Wait          time.Duration
	FromEnd       bool // a moving end, its own or a covering one, leads its count
}

// TimingOf answers kind and scope's Timing, false when it is not scheduled.
func (e *Executor[P]) TimingOf(kind Kind, scope string) (Timing, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := ScheduledScope{Kind: kind, Scope: scope}
	t := e.scheduled[key]
	if t == nil {
		return Timing{}, false
	}
	_, _, fromEnd := e.leadEndLocked(key)
	return Timing{Base: t.base, Changed: t.changed, Wait: t.cadence.wait(t.done), FromEnd: fromEnd}, true
}
