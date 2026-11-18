package scheduler_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// publishSuperseded is the app's own end for a yielded publish a newer pass
// covered.
var publishSuperseded = scheduler.Reason{Code: "publish_superseded"}

// yielder is a kind whose Run reaches publish_retry, yields until the time
// its until func answers, then logs "publish" in got. Its Yield's answer and
// the times around it are kept; a covered wait ends publish_superseded.
type yielder struct {
	err       error
	until     func(now time.Time) time.Time
	got       *order
	yieldAt   time.Time
	backAt    time.Time
	mu        sync.Mutex
	gateAfter <-chan struct{}
}

func (y *yielder) spec(kind scheduler.Kind, slot string) scheduler.KindSpec[payload] {
	k := spec(kind, slot)
	k.Phases = []scheduler.Phase{"publish_retry"}
	k.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
		run.Reached("publish_retry")
		at := time.Now()
		err := run.Yield(ctx, y.until(at))
		y.mu.Lock()
		y.yieldAt, y.err, y.backAt = at, err, time.Now()
		y.mu.Unlock()
		if y.got != nil {
			y.got.add("publish")
		}
		if errors.Is(err, scheduler.ErrCovered) {
			return scheduler.End{Result: scheduler.ResultSkipped, Reasons: []scheduler.Reason{publishSuperseded}}
		}
		if y.gateAfter != nil {
			select {
			case <-y.gateAfter:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

// answer is how long the yielded Run waited and its Yield's answer.
func (y *yielder) answer() (time.Duration, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.backAt.Sub(y.yieldAt), y.err
}

// returned reports whether the yielded Run's Yield has answered.
func (y *yielder) returned() bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	return !y.backAt.IsZero()
}

// logging is a kind whose Run logs its scope in got and waits on its Gate.
func logging(kind scheduler.Kind, slot string, got *order) scheduler.KindSpec[payload] {
	k := spec(kind, slot)
	k.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
		got.add(run.Job().Scope)
		if g := run.Job().Payload.Gate; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

// wantRunningAt fails the test unless id's record is running at phase.
func (r *rig) wantRunningAt(t *testing.T, id scheduler.ID, phase scheduler.Phase) {
	t.Helper()
	if rec := r.rec(t, id); rec.State != scheduler.StateRunning || rec.Phase != phase || rec.End != nil {
		t.Errorf("yielded record %s at %s with end %v, want running at %s", rec.State, rec.Phase, rec.End, phase)
	}
}

func TestYieldFreesAndRetakesSlot(t *testing.T) {
	t.Run("the slot is free while it waits and comes back at until after a person", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got := &order{}
			y := &yielder{got: got, until: func(now time.Time) time.Time { return now.Add(10 * time.Minute) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes"), logging("quick", "passes", got)}})
			p := r.start(t, timer("publish", "", payload{}))
			synctest.Wait()
			gate := make(chan struct{})
			r.start(t, timer("quick", "while", payload{Gate: gate}))
			synctest.Wait()
			if l := got.list(); !slices.Equal(l, []string{"while"}) {
				t.Errorf("ran %q while the publish yielded, want the queued automatic job %q", l, "while")
			}
			r.wantRunningAt(t, p.Job, "publish_retry")
			time.Sleep(11 * time.Minute)
			synctest.Wait()
			r.start(t, timer("quick", "automatic", payload{}))
			r.start(t, api("quick", "person", "alice", payload{}))
			close(gate)
			synctest.Wait()
			if l, want := got.list(), []string{"while", "person", "publish", "automatic"}; !slices.Equal(l, want) {
				t.Errorf("slot order %q, want %q: the yielded job after a person and before automatic work", l, want)
			}
			waited, err := y.answer()
			if err != nil || waited != 11*time.Minute {
				t.Errorf("Yield = %v after %v, want nil once the job in the slot ended at 11m", err, waited)
			}
			r.wantEnd(t, p.Job, scheduler.ResultOK)
		})
	})

	t.Run("Wake brings the return forward", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got := &order{}
			y := &yielder{got: got, until: func(now time.Time) time.Time { return now.Add(time.Hour) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes"), logging("quick", "passes", got)}})
			p := r.start(t, timer("publish", "", payload{}))
			synctest.Wait()
			gate := make(chan struct{})
			q := r.start(t, timer("quick", "holder", payload{Gate: gate}))
			synctest.Wait()
			time.Sleep(time.Minute)
			if r.ex.Wake(q.Job) {
				t.Error("Wake of a running job that is not yielded = true, want false")
			}
			if r.ex.Wake("00000000000000000000000000000000") {
				t.Error("Wake of an unknown id = true, want false")
			}
			if !r.ex.Wake(p.Job) {
				t.Fatal("Wake of the yielded job = false, want true")
			}
			synctest.Wait()
			if y.returned() {
				t.Error("Yield answered while another job held the slot, want it to wait for the slot")
			}
			r.start(t, timer("quick", "automatic", payload{}))
			close(gate)
			synctest.Wait()
			if l, want := got.list(), []string{"holder", "publish", "automatic"}; !slices.Equal(l, want) {
				t.Errorf("slot order %q, want %q", l, want)
			}
			if waited, err := y.answer(); err != nil || waited != time.Minute {
				t.Errorf("Yield = %v after %v, want nil at the woken minute", err, waited)
			}
			if r.ex.Wake(p.Job) {
				t.Error("Wake after the job took its slot back = true, want false")
			}
		})
	})

	t.Run("an app's retry curve keeps the record running at its phase", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				mu       sync.Mutex
				attempts []time.Duration
			)
			unwritable := 3
			delays := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
			publish := spec("publish", "passes")
			publish.Phases = []scheduler.Phase{"publishing", "publish_retry"}
			publish.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				start := time.Now()
				run.Reached("publishing")
				for i := 0; ; i++ {
					mu.Lock()
					attempts = append(attempts, time.Since(start))
					mu.Unlock()
					if unwritable == 0 {
						if !run.Commit() {
							return scheduler.End{Result: scheduler.ResultCancelled}
						}
						return scheduler.End{Result: scheduler.ResultOK}
					}
					unwritable--
					if i == len(delays) {
						return scheduler.End{Result: scheduler.ResultFailed, Reasons: []scheduler.Reason{{Code: "store_unwritable"}}}
					}
					run.Reached("publish_retry")
					if err := run.Yield(ctx, time.Now().Add(delays[i])); err != nil {
						if errors.Is(err, scheduler.ErrCovered) {
							return scheduler.End{Result: scheduler.ResultSkipped, Reasons: []scheduler.Reason{publishSuperseded}}
						}
						return scheduler.End{Result: scheduler.ResultFailed}
					}
				}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{publish}})
			p := r.start(t, timer("publish", "", payload{}))
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			r.wantRunningAt(t, p.Job, "publish_retry")
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			r.wantEnd(t, p.Job, scheduler.ResultOK)
			mu.Lock()
			defer mu.Unlock()
			if want := []time.Duration{0, time.Minute, 3 * time.Minute, 7 * time.Minute}; !slices.Equal(attempts, want) {
				t.Errorf("publish attempts at %v, want %v", attempts, want)
			}
		})
	})
}

func TestYieldRetakeReportsSlotWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu    sync.Mutex
			waits []time.Duration
		)
		obs := scheduler.Observer{SlotWait: func(_ string, c scheduler.Class, waited time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			if c == scheduler.Automatic {
				waits = append(waits, waited)
			}
		}}
		got := &order{}
		y := &yielder{got: got, until: func(now time.Time) time.Time { return now.Add(10 * time.Minute) }}
		r := newRig(t, scheduler.Config[payload]{Observer: obs, Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes"), logging("quick", "passes", got)}})
		r.start(t, timer("publish", "", payload{}))
		synctest.Wait()
		gate := make(chan struct{})
		r.start(t, timer("quick", "holder", payload{Gate: gate}))
		synctest.Wait()
		time.Sleep(12 * time.Minute)
		close(gate)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		// The publish's first take, the holder's, then the publish's retake,
		// which waited from its until at 10m to the holder's end at 12m.
		if want := []time.Duration{0, 0, 2 * time.Minute}; !slices.Equal(waits, want) {
			t.Errorf("automatic slot waits %v, want %v", waits, want)
		}
	})
}

func TestYieldEndsOnCoverWhileRetaking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := &order{}
		y := &yielder{until: func(now time.Time) time.Time { return now.Add(time.Minute) }}
		full := logging("full", "passes", got)
		full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "reevaluate" }
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("reevaluate", "passes"), full, logging("quick", "passes", got)}})
		p := r.start(t, timer("reevaluate", "", payload{}))
		synctest.Wait()
		gate := make(chan struct{})
		r.start(t, timer("quick", "holder", payload{Gate: gate}))
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if y.returned() {
			t.Fatal("Yield answered while another job held the slot, want it waiting for the slot")
		}
		fullGate := make(chan struct{})
		f := r.start(t, api("full", "all", "alice", payload{Gate: fullGate}))
		r.start(t, timer("quick", "after", payload{}))
		close(gate)
		synctest.Wait()
		var covered *scheduler.CoveredError
		if _, err := y.answer(); !errors.As(err, &covered) || covered.By != f.Job {
			t.Errorf("Yield = %v while the covering job runs, want *CoveredError by %s at its start", err, f.Job)
		}
		close(fullGate)
		synctest.Wait()
		if l, want := got.list(), []string{"holder", "all", "after"}; !slices.Equal(l, want) {
			t.Errorf("ran %q, want %q: the covered job leaves the slot to the queued work", l, want)
		}
		r.wantEnd(t, p.Job, scheduler.ResultSkipped, publishSuperseded)
	})
}

func TestYieldEndsOnCover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		y := &yielder{until: func(now time.Time) time.Time { return now.Add(6 * time.Hour) }}
		full := spec("full", "passes")
		full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "reevaluate" }
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("reevaluate", "passes"), full, spec("quick", "passes")}})
		p := r.start(t, timer("reevaluate", "", payload{}))
		synctest.Wait()
		r.start(t, timer("quick", "", payload{}))
		synctest.Wait()
		if y.returned() {
			t.Fatal("Yield answered at the start of a kind that does not cover it")
		}
		r.wantRunningAt(t, p.Job, "publish_retry")
		f := r.start(t, api("full", "", "alice", payload{}))
		synctest.Wait()
		waited, err := y.answer()
		var covered *scheduler.CoveredError
		if !errors.Is(err, scheduler.ErrCovered) || !errors.As(err, &covered) || covered.By != f.Job {
			t.Fatalf("Yield = %v, want *CoveredError by %s", err, f.Job)
		}
		if waited != 0 {
			t.Errorf("Yield answered after %v, want at the covering start", waited)
		}
		r.wantEnd(t, p.Job, scheduler.ResultSkipped, publishSuperseded)
		r.wantEnd(t, f.Job, scheduler.ResultOK)
	})
}

func TestYieldDoesNotBlockNextPass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fresh bool
		job   func() scheduler.Job[payload]
	}{
		{name: "a person's request under Fresh", fresh: true, job: func() scheduler.Job[payload] { return api("full", "", "alice", payload{}) }},
		{name: "a timer request", job: func() scheduler.Job[payload] { return timer("full", "", payload{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				y := &yielder{until: func(now time.Time) time.Time { return now.Add(6 * time.Hour) }}
				publish := y.spec("full", "passes")
				run := publish.Run
				log := &runLog{}
				publish.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
					if r.Job().Payload.N == 1 {
						return run(ctx, r)
					}
					return work(log)(ctx, r)
				}
				if tc.fresh {
					publish.Fresh = func(string) time.Duration { return time.Hour }
				}
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{publish}})
				yielded := timer("full", "", payload{N: 1})
				p := r.start(t, yielded)
				synctest.Wait()
				next := r.start(t, tc.job())
				if next.Joined || next.Job == p.Job {
					t.Errorf("a request while its scope's job yields = %+v, want a new job", next)
				}
				synctest.Wait()
				if !slices.Equal(log.ids(), []scheduler.ID{next.Job}) {
					t.Errorf("ran %v, want the new job %s to run while the other yields", log.ids(), next.Job)
				}
				r.wantEnd(t, next.Job, scheduler.ResultOK)
				r.wantRunningAt(t, p.Job, "publish_retry")
			})
		})
	}
}

func TestYieldTooLong(t *testing.T) {
	t.Run("more than 24 hours ahead is refused and the slot kept", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got := &order{}
			gate := make(chan struct{})
			y := &yielder{got: got, gateAfter: gate, until: func(now time.Time) time.Time { return now.Add(24*time.Hour + time.Nanosecond) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes"), logging("quick", "passes", got)}})
			p := r.start(t, timer("publish", "", payload{}))
			synctest.Wait()
			if waited, err := y.answer(); !errors.Is(err, scheduler.ErrYieldTooLong) || waited != 0 {
				t.Errorf("Yield 24h and 1ns ahead = %v after %v, want ErrYieldTooLong at once", err, waited)
			}
			r.start(t, timer("quick", "waiting", payload{}))
			synctest.Wait()
			if l := got.list(); !slices.Equal(l, []string{"publish"}) {
				t.Errorf("ran %q, want the queued job to wait while the refused job keeps its slot", l)
			}
			close(gate)
			synctest.Wait()
			if l := got.list(); !slices.Equal(l, []string{"publish", "waiting"}) {
				t.Errorf("ran %q, want the queued job once the refused one ended", l)
			}
			r.wantEnd(t, p.Job, scheduler.ResultOK)
		})
	})

	t.Run("24 hours ahead is accepted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got := &order{}
			y := &yielder{got: got, until: func(now time.Time) time.Time { return now.Add(24 * time.Hour) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes"), logging("quick", "passes", got)}})
			p := r.start(t, timer("publish", "", payload{}))
			synctest.Wait()
			r.start(t, timer("quick", "meanwhile", payload{}))
			synctest.Wait()
			if l := got.list(); !slices.Equal(l, []string{"meanwhile"}) {
				t.Errorf("ran %q, want the queued job while the publish yields", l)
			}
			time.Sleep(24 * time.Hour)
			synctest.Wait()
			if waited, err := y.answer(); err != nil || waited != 24*time.Hour {
				t.Errorf("Yield 24h ahead = %v after %v, want nil at 24h", err, waited)
			}
			r.wantEnd(t, p.Job, scheduler.ResultOK)
		})
	})
}

func TestYieldCancelled(t *testing.T) {
	t.Run("a cancel answers the ctx error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			y := &yielder{until: func(now time.Time) time.Time { return now.Add(time.Hour) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "passes")}})
			p := r.start(t, timer("publish", "", payload{}))
			synctest.Wait()
			time.Sleep(time.Minute)
			if stopping, err := r.ex.Cancel(t.Context(), p.Job); err != nil || !stopping {
				t.Fatalf("Cancel(yielded) = (%t, %v), want stopping", stopping, err)
			}
			synctest.Wait()
			if waited, err := y.answer(); !errors.Is(err, context.Canceled) || waited != time.Minute {
				t.Errorf("Yield = %v after %v, want context.Canceled at the cancel", err, waited)
			}
			r.wantEnd(t, p.Job, scheduler.ResultCancelled)
		})
	})

	t.Run("a kind with no slot only waits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			y := &yielder{until: func(now time.Time) time.Time { return now.Add(5 * time.Minute) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("refresh", "")}})
			p := r.start(t, api("refresh", "connection:3", "alice", payload{}))
			synctest.Wait()
			r.wantRunningAt(t, p.Job, "publish_retry")
			time.Sleep(5 * time.Minute)
			synctest.Wait()
			if waited, err := y.answer(); err != nil || waited != 5*time.Minute {
				t.Errorf("Yield = %v after %v, want nil at until", err, waited)
			}
			r.wantEnd(t, p.Job, scheduler.ResultOK)
		})
	})
}
