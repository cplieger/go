package scheduler

import "context"

// slot runs one job or batch item of a named slot at a time and never
// preempts it.
type slot struct{}

func newSlot() *slot { return &slot{} }

// acquire waits for the slot in class c and answers its release, which is
// safe to call more than once; a ctx that ends first answers its error.
func (s *slot) acquire(ctx context.Context, c Class) (func(), error) {
	return nil, errStub
}
