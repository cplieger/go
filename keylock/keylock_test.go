package keylock_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/keylock"
)

// assertFree fails unless every key can be taken at once. Inside a synctest
// bubble the timeout fires as soon as every goroutine is blocked, so a held
// key fails fast instead of hanging the test.
func assertFree(t *testing.T, s *keylock.Set[string], keys ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := s.LockAll(ctx, keys)
	if err != nil {
		t.Fatalf("LockAll(%q) = %v, want every key free", keys, err)
	}
	unlock()
}

// assertHeld fails unless k is held by someone else.
func assertHeld(t *testing.T, s *keylock.Set[string], k string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := s.Lock(ctx, k)
	if err == nil {
		unlock()
		t.Fatalf("Lock(%q) succeeded, want it held", k)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock(%q) = %v, want context.DeadlineExceeded", k, err)
	}
}

func assertEntries[K comparable](t *testing.T, s *keylock.Set[K], want int) {
	t.Helper()
	if got := keylock.Entries(s); got != want {
		t.Fatalf("Entries = %d, want %d", got, want)
	}
}

func mustPanic(t *testing.T, f func()) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
		if recovered == nil {
			t.Fatal("did not panic")
		}
	}()
	f()
	return nil
}

func TestLockAllOrderedUnderContention(t *testing.T) {
	const (
		goroutines = 16
		rounds     = 2000
		nkeys      = 8
	)
	s := keylock.New(cmp.Compare[int])
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var holders [nkeys]atomic.Int32
	var overlapped atomic.Bool
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(g), 1))
			keys := make([]int, 0, nkeys+2)
			for i := range rounds {
				keys = keys[:0]
				for k := range nkeys {
					keys = append(keys, k)
				}
				keys = append(keys, i%nkeys, (i*3+g)%nkeys)
				if (i+g)%2 == 0 {
					slices.Reverse(keys)
				} else {
					rng.Shuffle(len(keys), func(a, b int) { keys[a], keys[b] = keys[b], keys[a] })
				}
				unlock, err := s.LockAll(ctx, keys)
				if err != nil {
					errs <- fmt.Errorf("goroutine %d, round %d: LockAll = %w", g, i, err)
					return
				}
				for k := range holders {
					if holders[k].Add(1) != 1 {
						overlapped.Store(true)
					}
				}
				for k := range holders {
					holders[k].Add(-1)
				}
				unlock()
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if overlapped.Load() {
		t.Error("two callers held the same key at once")
	}
	assertEntries(t, s, 0)
}

func TestLockAllPanicsOnEqualDistinctKeys(t *testing.T) {
	byTens := func(a, b int) int { return cmp.Compare(a/10, b/10) }
	s := keylock.New(byTens)
	r := mustPanic(t, func() { _, _ = s.LockAll(t.Context(), []int{12, 30, 11}) })
	if msg, _ := r.(string); !strings.Contains(msg, "distinct keys") {
		t.Errorf("panic = %v, want it to name the distinct keys", r)
	}
	assertEntries(t, s, 0)
}

func TestPanicsOnNaNKey(t *testing.T) {
	s := keylock.New(cmp.Compare[float64])
	for _, tc := range []struct {
		name string
		lock func()
	}{
		{"Lock", func() { _, _ = s.Lock(t.Context(), math.NaN()) }},
		{"LockAll", func() { _, _ = s.LockAll(t.Context(), []float64{1, math.NaN()}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := mustPanic(t, tc.lock)
			if msg, _ := r.(string); !strings.Contains(msg, "not equal to itself") {
				t.Errorf("panic = %v, want it to name the key", r)
			}
			assertEntries(t, s, 0)
		})
	}
}

// Without the check, LockAll takes "a" and then panics inside s.m[k] with
// s.mu held, so the Lock below would block on s.mu past its ctx.
func TestLockAllPanicsOnUncomparableKey(t *testing.T) {
	descending := func(a, b any) int { return strings.Compare(fmt.Sprint(b), fmt.Sprint(a)) }
	s := keylock.New(descending)
	mustPanic(t, func() { _, _ = s.LockAll(t.Context(), []any{"a", []int{1}}) })

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		unlock, err := s.Lock(ctx, "a")
		if err == nil {
			unlock()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf(`Lock("a") after the panic = %v, want it free`, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(`Lock("a") after the panic blocked past its ctx`)
	}
	assertEntries(t, s, 0)
}

func TestNewPanicsOnNilCmp(t *testing.T) {
	mustPanic(t, func() { keylock.New[string](nil) })
}

func TestLockCancelWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := keylock.New(strings.Compare)
		unlock, err := s.Lock(t.Context(), "a")
		if err != nil {
			t.Fatalf("Lock = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			u, err := s.Lock(ctx, "a")
			if u != nil {
				u()
			}
			done <- err
		}()
		synctest.Wait()
		assertEntries(t, s, 1)

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting Lock = %v, want context.Canceled", err)
		}
		assertHeld(t, s, "a")
		unlock()
		assertFree(t, s, "a")
		assertEntries(t, s, 0)
	})
}

// A done ctx on a free key makes both of acquire's select arms ready, the
// state a waiter reaches when its ctx ends just before the holder unlocks, so
// each iteration is an independent coin flip against a missing ctx check.
func TestLockDoneContextTakesNothing(t *testing.T) {
	s := keylock.New(strings.Compare)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		if unlock, err := s.Lock(ctx, "a"); !errors.Is(err, context.Canceled) || unlock != nil {
			t.Fatalf("Lock on a done context = (%v, %v), want (nil, context.Canceled)", unlock != nil, err)
		}
		if unlock, err := s.LockAll(ctx, []string{"a", "b"}); !errors.Is(err, context.Canceled) || unlock != nil {
			t.Fatalf("LockAll on a done context = (%v, %v), want (nil, context.Canceled)", unlock != nil, err)
		}
	}
	assertEntries(t, s, 0)
}

func TestEntriesReclaimed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := keylock.New(strings.Compare)
		unlockA, err := s.Lock(t.Context(), "a")
		if err != nil {
			t.Fatalf("Lock = %v", err)
		}
		unlockBC, err := s.LockAll(t.Context(), []string{"c", "b"})
		if err != nil {
			t.Fatalf("LockAll = %v", err)
		}
		assertEntries(t, s, 3)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := s.LockAll(ctx, []string{"a", "0"})
			done <- err
		}()
		synctest.Wait()
		assertEntries(t, s, 4)
		cancel()
		<-done
		assertEntries(t, s, 3)

		unlockA()
		assertEntries(t, s, 2)
		unlockBC()
		assertEntries(t, s, 0)
	})
}

func TestUnlockIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := keylock.New(strings.Compare)
		first, err := s.Lock(t.Context(), "a")
		if err != nil {
			t.Fatalf("Lock = %v", err)
		}
		firstAll, err := s.LockAll(t.Context(), []string{"b", "c"})
		if err != nil {
			t.Fatalf("LockAll = %v", err)
		}
		first()
		firstAll()

		second, err := s.LockAll(t.Context(), []string{"a", "b", "c"})
		if err != nil {
			t.Fatalf("LockAll after unlock = %v", err)
		}
		first()
		firstAll()
		for _, k := range []string{"a", "b", "c"} {
			assertHeld(t, s, k)
		}
		second()
		second()
		assertFree(t, s, "a", "b", "c")
		assertEntries(t, s, 0)
	})
}

func TestLockAllReleasesOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := keylock.New(strings.Compare)
		unlockE, err := s.Lock(t.Context(), "e")
		if err != nil {
			t.Fatalf("Lock = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			u, err := s.LockAll(ctx, []string{"h", "c", "e", "a", "g", "b", "f", "d"})
			if u != nil {
				u()
			}
			done <- err
		}()
		synctest.Wait()
		// Parked on "e" holding "a" to "d", in the Set's order.
		assertEntries(t, s, 5)

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("LockAll = %v, want context.Canceled", err)
		}
		assertFree(t, s, "a", "b", "c", "d", "f", "g", "h")
		assertHeld(t, s, "e")
		unlockE()
		assertEntries(t, s, 0)
	})
}

func TestLockAllDedups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := keylock.New(strings.Compare)
		keys := []string{"b", "a", "b", "a", "a"}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		unlock, err := s.LockAll(ctx, keys)
		if err != nil {
			t.Fatalf("LockAll(%q) = %v, want each key taken once", keys, err)
		}
		assertEntries(t, s, 2)
		if want := []string{"b", "a", "b", "a", "a"}; !slices.Equal(keys, want) {
			t.Errorf("keys = %q after LockAll, want %q unmodified", keys, want)
		}
		unlock()
		assertEntries(t, s, 0)
	})
}

func TestLockAllNoKeys(t *testing.T) {
	s := keylock.New(strings.Compare)
	unlock, err := s.LockAll(t.Context(), nil)
	if err != nil {
		t.Fatalf("LockAll(nil) = %v", err)
	}
	unlock()
	assertEntries(t, s, 0)
}
