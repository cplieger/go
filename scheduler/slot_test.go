package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/cplieger/go/scheduler/v2"
)

func TestSlotOrder(t *testing.T) {
	t.Run("executor", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var got order
			align := spec("align", "heavy")
			align.Run = func(_ context.Context, r *scheduler.Run[payload]) scheduler.End {
				got.add(r.Job().Scope)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			first := make(chan struct{})
			resync := spec("resync", "heavy")
			resync.MaxItems = 3
			resync.Items = func(p payload) int { return p.N }
			resync.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				for i := range r.Job().Payload.N {
					err := r.Item(ctx, i, func(context.Context) scheduler.Line {
						got.add(fmt.Sprintf("item_%d", i))
						if i == 0 {
							<-first
						}
						return scheduler.Line{State: scheduler.LineDone}
					})
					if err != nil {
						t.Errorf("Item(%d) = %v, want nil", i, err)
					}
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{align, resync}})
			r.start(t, api("resync", "", "alice", payload{N: 3}))
			synctest.Wait()
			r.start(t, timer("align", "auto_1", payload{}))
			r.start(t, timer("align", "auto_2", payload{}))
			r.start(t, api("align", "person", "bob", payload{}))
			close(first)
			synctest.Wait()
			want := []string{"item_0", "person", "auto_1", "item_1", "auto_2", "item_2"}
			if l := got.list(); !slices.Equal(l, want) {
				t.Errorf("slot order %q, want %q: the person first, behind one item, then automatic and batch alternating", l, want)
			}
			r.start(t, api("align", "again", "bob", payload{}))
			synctest.Wait()
			if l := got.list(); len(l) != len(want)+1 || l[len(want)] != "again" {
				t.Errorf("slot order %q, want the drained slot to run %q at once", l, "again")
			}
		})
	})

	t.Run("waiter_cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := scheduler.NewSlot()
			release, err := s.Acquire(t.Context(), scheduler.Automatic)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error)
			go func() {
				_, err := s.Acquire(ctx, scheduler.Interactive)
				done <- err
			}()
			synctest.Wait()
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("a cancelled waiter: %v, want context.Canceled", err)
			}
			release()
			if !free(t, s) {
				t.Error("the slot stayed taken after its holder released and its waiter left")
			}
		})
	})

	// A waiter granted the slot as its ctx ends either takes it or passes it
	// on; the slot is never left held by no one.
	t.Run("granted_as_waiter_cancels", func(t *testing.T) {
		for range 20 {
			synctest.Test(t, func(t *testing.T) {
				s := scheduler.NewSlot()
				release, err := s.Acquire(t.Context(), scheduler.Automatic)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan func())
				go func() {
					rel, _ := s.Acquire(ctx, scheduler.Interactive)
					done <- rel
				}()
				synctest.Wait()
				cancel()
				release()
				if rel := <-done; rel != nil {
					rel()
				}
				if !free(t, s) {
					t.Fatal("the slot is held by no one")
				}
			})
		}
	})

	t.Run("release_once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := scheduler.NewSlot()
			first, err := s.Acquire(t.Context(), scheduler.Automatic)
			if err != nil {
				t.Fatal(err)
			}
			second := make(chan func())
			go func() {
				rel, err := s.Acquire(t.Context(), scheduler.Automatic)
				if err != nil {
					t.Error(err)
				}
				second <- rel
			}()
			synctest.Wait()
			first()
			rel := <-second
			first()
			if free(t, s) {
				t.Error("a second release by the first holder freed the slot the second holds")
			}
			if rel != nil {
				rel()
			}
			synctest.Wait()
		})
	})
}

// free reports whether s grants a batch item at once.
func free(t *testing.T, s *scheduler.Slot) bool {
	t.Helper()
	got := make(chan struct{})
	go func() {
		rel, err := s.Acquire(t.Context(), scheduler.Batch)
		if err != nil {
			t.Error(err)
			return
		}
		rel()
		close(got)
	}()
	synctest.Wait()
	select {
	case <-got:
		return true
	default:
		return false
	}
}
