package scheduler_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// step is one Transition without its job's identity.
type step struct {
	at     time.Duration // since the test's start
	phase  scheduler.Phase
	result scheduler.Result
}

// steps answers id's transitions as steps since t0, failing the test when
// one names another kind or scope.
func steps(t *testing.T, seen *transitions, id scheduler.ID, kind scheduler.Kind, scope string, t0 time.Time) []step {
	t.Helper()
	var out []step
	for _, tr := range seen.of(id) {
		if tr.Kind != kind || tr.Scope != scope {
			t.Errorf("transition %+v, want kind %s scope %q", tr, kind, scope)
		}
		out = append(out, step{tr.At.Sub(t0), tr.Phase, tr.Result})
	}
	return out
}

func TestObserverTransitionsInOrder(t *testing.T) {
	t.Run("queued, started, each phase, committing and the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			t0 := time.Now()
			full := spec("full", "passes")
			full.Phases = []scheduler.Phase{"fetched", "judged"}
			full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				run.Reached("fetched")
				time.Sleep(time.Second)
				run.Reached("judged")
				run.Reached("judged")
				time.Sleep(time.Second)
				run.Commit()
				time.Sleep(time.Second)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			blocker := spec("quick", "passes")
			blocker.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
				time.Sleep(5 * time.Second)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			seen := &transitions{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full, blocker}, Observer: seen.observer()})
			r.start(t, timer("quick", "", payload{}))
			synctest.Wait()
			s := r.start(t, api("full", "source:a", "alice", payload{}))
			if again := r.start(t, api("full", "source:a", "bob", payload{})); !again.Joined || again.Job != s.Job {
				t.Fatalf("a second request = %+v, want it joined to %s", again, s.Job)
			}
			time.Sleep(time.Minute)
			want := []step{
				{0, scheduler.PhaseQueued, ""},
				{5 * time.Second, scheduler.PhaseStarted, ""},
				{5 * time.Second, "fetched", ""},
				{6 * time.Second, "judged", ""},
				{7 * time.Second, scheduler.PhaseCommitting, ""},
				{8 * time.Second, scheduler.PhaseCommitting, scheduler.ResultOK},
			}
			if got := steps(t, seen, s.Job, "full", "source:a", t0); !slices.Equal(got, want) {
				t.Errorf("transitions %v, want %v", got, want)
			}
		})
	})

	t.Run("a queued job cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			t0 := time.Now()
			seen := &transitions{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}, Observer: seen.observer()})
			gate := make(chan struct{})
			r.start(t, api("scan", "a", "alice", payload{Gate: gate}))
			synctest.Wait()
			s := r.start(t, api("scan", "b", "alice", payload{}))
			time.Sleep(time.Second)
			if _, err := r.ex.Cancel(t.Context(), s.Job); err != nil {
				t.Fatalf("Cancel = %v", err)
			}
			synctest.Wait()
			want := []step{{0, scheduler.PhaseQueued, ""}, {time.Second, scheduler.PhaseQueued, scheduler.ResultCancelled}}
			if got := steps(t, seen, s.Job, "scan", "b", t0); !slices.Equal(got, want) {
				t.Errorf("transitions %v, want %v", got, want)
			}
			close(gate)
		})
	})

	t.Run("a kind that keeps no record is observed alike", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			t0 := time.Now()
			sweep := spec("sweep", "")
			sweep.Kept = 0
			seen := &transitions{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{sweep}, Observer: seen.observer()})
			s := r.start(t, timer("sweep", "", payload{}))
			synctest.Wait()
			want := []step{
				{0, scheduler.PhaseQueued, ""},
				{0, scheduler.PhaseStarted, ""},
				{0, scheduler.PhaseStarted, scheduler.ResultOK},
			}
			if got := steps(t, seen, s.Job, "sweep", "", t0); !slices.Equal(got, want) {
				t.Errorf("transitions %v, want %v", got, want)
			}
		})
	})
}

// TestObserverHoldsNoLock runs on the real clock: a goroutine waiting on a
// held mutex is not durably blocked, so inside a synctest bubble a lock held
// across the Observer would hang the test instead of failing it.
func TestObserverHoldsNoLock(t *testing.T) {
	var (
		ex      *scheduler.Executor[payload]
		mu      sync.Mutex
		blocked []string
		n       int
	)
	ended := make(chan struct{})
	// call runs Start, Cancel and Deadline on another goroutine and waits for
	// them, as an Observer that waits on another part of the app would.
	call := func(where string) {
		mu.Lock()
		n++
		scope := fmt.Sprintf("side:%d", n)
		mu.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = ex.Start(t.Context(), api("side", scope, "bob", payload{}))
			_, _ = ex.Cancel(t.Context(), "unknown")
			_ = ex.Deadline("full", "")
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			mu.Lock()
			blocked = append(blocked, where)
			mu.Unlock()
		}
	}
	obs := scheduler.Observer{
		Transition: func(tr scheduler.Transition) {
			if tr.Kind != "full" {
				return
			}
			call(fmt.Sprintf("transition %s %s", tr.Phase, tr.Result))
			if tr.Result != "" {
				close(ended)
			}
		},
		SlotWait: func(slot string, _ scheduler.Class, _ time.Duration) {
			if slot == "passes" {
				call("slot wait")
			}
		},
	}
	full := spec("full", "passes")
	full.Phases = []scheduler.Phase{"fetched"}
	full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
		run.Reached("fetched")
		run.Commit()
		return scheduler.End{Result: scheduler.ResultOK}
	}
	r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full, spec("side", "")}, Observer: obs})
	ex = r.ex
	r.start(t, api("full", "", "alice", payload{}))
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the job's end was not observed within 30 seconds")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(blocked) != 0 {
		t.Errorf("calls into the executor from the Observer blocked at %q, want none", blocked)
	}
	if n < 5 {
		t.Errorf("the Observer was called %d times for the job, want queued, started, fetched, committing, the end and its slot wait", n)
	}
}

func TestSlotWaitNamedSlotOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type wait struct {
			slot   string
			class  scheduler.Class
			waited time.Duration
		}
		var (
			mu    sync.Mutex
			waits []wait
		)
		obs := scheduler.Observer{SlotWait: func(slot string, c scheduler.Class, waited time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			waits = append(waits, wait{slot, c, waited})
		}}
		align := spec("align", "heavy")
		align.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
			time.Sleep(10 * time.Second)
			return scheduler.End{Result: scheduler.ResultOK}
		}
		resync := grab(done)
		resync.Kind, resync.Slot = "resync", "heavy"
		refresh := spec("refresh", "")
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{align, resync, refresh}, Observer: obs})

		r.start(t, api("align", "a", "alice", payload{}))
		r.start(t, timer("align", "b", payload{}))
		time.Sleep(25 * time.Second)
		r.start(t, api("resync", "", "alice", payload{N: 2}))
		r.start(t, api("refresh", "", "alice", payload{}))
		r.start(t, timer("refresh", "x", payload{}))
		synctest.Wait()

		want := []wait{
			{"heavy", scheduler.Interactive, 0},
			{"heavy", scheduler.Automatic, 10 * time.Second},
			{"heavy", scheduler.Batch, 0},
			{"heavy", scheduler.Batch, 0},
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(waits, want) {
			t.Errorf("slot waits %v, want %v: one per take of a named slot, none for slot \"\"", waits, want)
		}
	})
}
