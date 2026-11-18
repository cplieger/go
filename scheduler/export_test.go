package scheduler

import "context"

// Slot is the class-ordered slot, for the black-box tests.
type Slot struct{ s *slot }

// NewSlot answers a free slot.
func NewSlot() *Slot { return &Slot{s: newSlot()} }

// Acquire waits for the slot in class c.
func (s *Slot) Acquire(ctx context.Context, c Class) (func(), error) { return s.s.acquire(ctx, c) }
