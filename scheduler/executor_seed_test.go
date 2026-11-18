package scheduler_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

func TestRecordFollowsItsJob(t *testing.T) {
	t.Run("a finished job's record holds its kind, trigger and times, one write per change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full := spec("full_check", "passes")
			full.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
				time.Sleep(time.Minute)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
			queued := time.Now()
			s := r.start(t, api("full_check", "", "alice", payload{}))
			sleepUntil(queued.Add(2 * time.Minute))
			rec := r.rec(t, s.Job)
			if rec.Kind != "full_check" || rec.Trigger != scheduler.TriggerAPI || rec.Owner != "alice" {
				t.Errorf("record %s by %q from %s, want a full_check by alice from the API", rec.Kind, rec.Owner, rec.Trigger)
			}
			if !rec.QueuedAt.Equal(queued) || !rec.StartedAt.Equal(queued) || !rec.EndedAt.Equal(queued.Add(time.Minute)) {
				t.Errorf("record queued %v started %v ended %v, want queued and started at %v, ended a minute later",
					rec.QueuedAt, rec.StartedAt, rec.EndedAt, queued)
			}
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			for op, want := range map[memrecords.Op]int{memrecords.OpInsert: 1, memrecords.OpPut: 1, memrecords.OpEnd: 1} {
				if n := r.store.Count(op); n != want {
					t.Errorf("%d %s calls, want %d: queued, started and ended are one write each", n, op, want)
				}
			}
		})
	})

	degraded := scheduler.Reason{Code: "anilist_degraded"}
	walkFailed := scheduler.Reason{Code: "walk_failed"}
	for name, tc := range map[string]struct {
		run     func(context.Context, *scheduler.Run[payload]) scheduler.End
		result  scheduler.Result
		reasons []scheduler.Reason
	}{
		"a partial job": {
			run: func(context.Context, *scheduler.Run[payload]) scheduler.End {
				return scheduler.End{Result: scheduler.ResultPartial, Reasons: []scheduler.Reason{degraded}}
			},
			result: scheduler.ResultPartial, reasons: []scheduler.Reason{degraded},
		},
		"a failed job": {
			run: func(context.Context, *scheduler.Run[payload]) scheduler.End {
				return scheduler.End{Result: scheduler.ResultFailed, Reasons: []scheduler.Reason{walkFailed}}
			},
			result: scheduler.ResultFailed, reasons: []scheduler.Reason{walkFailed},
		},
		"a panicking job": {
			run:    func(context.Context, *scheduler.Run[payload]) scheduler.End { panic("boom") },
			result: scheduler.ResultFailed, reasons: []scheduler.Reason{{Code: scheduler.ReasonRunPanicked}},
		},
	} {
		t.Run(name+" ends its record so", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				quick := spec("quick_check", "passes")
				quick.Run = tc.run
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{quick}})
				s := r.start(t, api("quick_check", "", "alice", payload{}))
				synctest.Wait()
				r.wantEnd(t, s.Job, tc.result, tc.reasons...)
			})
		})
	}

	t.Run("a job cancelled while queued ends cancelled at phase queued, never started", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick_check", "passes"), spec("full_check", "passes")}})
			gate := make(chan struct{})
			r.start(t, api("quick_check", "", "alice", payload{Gate: gate}))
			synctest.Wait()
			queued := r.start(t, api("full_check", "", "alice", payload{}))
			if _, err := r.ex.Cancel(t.Context(), queued.Job); err != nil {
				t.Fatalf("Cancel(queued) = %v", err)
			}
			close(gate)
			synctest.Wait()
			r.wantEnd(t, queued.Job, scheduler.ResultCancelled)
			if rec := r.rec(t, queued.Job); rec.Phase != scheduler.PhaseQueued || !rec.StartedAt.IsZero() || r.log.ran(queued.Job) {
				t.Errorf("record at phase %s started %v, ran %t; want phase queued, never started", rec.Phase, rec.StartedAt, r.log.ran(queued.Job))
			}
		})
	})
}

// An end the store refuses to write is logged, frees the job's slot and is
// reported, but moves nothing a restart would not read: the record stays
// open for the store's next open to mark.
func TestUnrecordedEndFreesTheJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lines := &logLines{}
		seen := &transitions{}
		r := newRig(t, scheduler.Config[payload]{
			Kinds:    []scheduler.KindSpec[payload]{spec("full_check", "passes")},
			Logger:   slog.New(lines),
			Observer: seen.observer(),
		})
		r.schedule(t, "full_check", "", scheduler.Scheduled(24*time.Hour))
		first := r.start(t, api("full_check", "", "alice", payload{}))
		synctest.Wait()
		r.wantEnd(t, first.Job, scheduler.ResultOK)
		deadline := r.ex.Deadline("full_check", "")
		ends, err := r.store.LastEnds(t.Context())
		if err != nil {
			t.Fatalf("LastEnds = %v", err)
		}

		r.store.Fail(memrecords.OpEnd, errors.New("disk full"))
		time.Sleep(time.Hour)
		lost := r.start(t, api("full_check", "", "alice", payload{}))
		synctest.Wait()
		if rec := r.rec(t, lost.Job); rec.State != scheduler.StateRunning || rec.End != nil {
			t.Errorf("unwritten end left the record %s %v, want it running for the next open", rec.State, rec.End)
		}
		if got := lines.text(slog.LevelError); len(got) != 1 {
			t.Errorf("ERROR lines %q, want one naming the unrecorded end", got)
		}
		if ends := ended(seen.of(lost.Job)); !slices.Equal(ends, []scheduler.Result{scheduler.ResultOK}) {
			t.Errorf("Observer told the ends %v, want the one Run returned", ends)
		}
		if got := r.ex.Deadline("full_check", ""); !got.Equal(deadline) {
			t.Errorf("Deadline = %v, want %v: an end the store does not hold moves no deadline", got, deadline)
		}
		if got, err := r.store.LastEnds(t.Context()); err != nil || !maps.Equal(got, ends) {
			t.Errorf("LastEnds = (%v, %v), want %v", got, err, ends)
		}

		r.store.Fail(memrecords.OpEnd, nil)
		next := r.start(t, api("full_check", "", "alice", payload{}))
		synctest.Wait()
		r.wantEnd(t, next.Job, scheduler.ResultOK)
		if got := r.ex.Deadline("full_check", ""); !got.After(deadline) {
			t.Errorf("Deadline = %v after a stored end, want past %v", got, deadline)
		}
	})
}

// heldRecords holds the first Insert until hold closes, answering fail
// from it when fail is set.
type heldRecords struct {
	*memrecords.Store
	entered chan struct{}
	hold    chan struct{}
	fail    error
	once    sync.Once
}

func (g *heldRecords) Insert(ctx context.Context, rec scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) {
	first := false
	g.once.Do(func() { first = true })
	if first {
		close(g.entered)
		<-g.hold
		if g.fail != nil {
			return scheduler.Record{}, false, g.fail
		}
	}
	return g.Store.Insert(ctx, rec, key)
}

// heldRig runs an executor of kind "quick_check" over g until the test ends.
func heldRig(t *testing.T, g *heldRecords, log *runLog) *scheduler.Executor[payload] {
	t.Helper()
	quick := spec("quick_check", "passes")
	quick.Run = work(log)
	ex, err := scheduler.NewExecutor(scheduler.Config[payload]{
		Kinds:   []scheduler.KindSpec[payload]{quick},
		Records: g,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewExecutor = %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ex.Run(t.Context())
	}()
	t.Cleanup(func() { <-done })
	return ex
}

type answer struct {
	err error
	s   scheduler.Started
}

func startAsync(ctx context.Context, ex *scheduler.Executor[payload], j scheduler.Job[payload]) <-chan answer {
	out := make(chan answer, 1)
	go func() {
		s, err := ex.Start(ctx, j)
		out <- answer{s: s, err: err}
	}()
	return out
}

// A request for a scope whose first request's record is still being written
// waits for that write and joins the one job: one record, one run.
func TestStartBehindARecordWriteJoinsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &heldRecords{Store: memrecords.New(), entered: make(chan struct{}), hold: make(chan struct{})}
		log := &runLog{}
		ex := heldRig(t, g, log)
		first := startAsync(t.Context(), ex, api("quick_check", "", "alice", payload{}))
		<-g.entered
		second := startAsync(t.Context(), ex, api("quick_check", "", "bob", payload{}))
		synctest.Wait()
		close(g.hold)
		a, b := <-first, <-second
		if a.err != nil || b.err != nil {
			t.Fatalf("Start = (%v, %v), want both admitted", a.err, b.err)
		}
		if b.s.Job != a.s.Job || !b.s.Joined || a.s.Joined {
			t.Errorf("second start %+v, first %+v; want the second joined to the first's job", b.s, a.s)
		}
		synctest.Wait()
		if got := log.ids(); !slices.Equal(got, []scheduler.ID{a.s.Job}) {
			t.Errorf("ran %v, want the one job %s once", got, a.s.Job)
		}
		if n := len(g.All()); n != 1 {
			t.Errorf("%d records, want the one the first start wrote", n)
		}
	})
}

// A request waiting on another's record write that then fails writes its
// own record and runs once; the failed request answers ErrStoreUnavailable.
// A timer wake refused so retries at its next wake
// (TestTimerStoreUnavailableWakesAfterRetry).
func TestFailedAdmissionLeavesTheWaiterItsOwnRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &heldRecords{Store: memrecords.New(), entered: make(chan struct{}), hold: make(chan struct{}), fail: errors.New("disk full")}
		log := &runLog{}
		ex := heldRig(t, g, log)
		first := startAsync(t.Context(), ex, api("quick_check", "", "alice", payload{}))
		<-g.entered
		second := startAsync(t.Context(), ex, api("quick_check", "", "bob", payload{}))
		synctest.Wait()
		close(g.hold)
		a, b := <-first, <-second
		if !errors.Is(a.err, scheduler.ErrStoreUnavailable) {
			t.Errorf("failed start = %v, want ErrStoreUnavailable", a.err)
		}
		if b.err != nil || b.s.Joined || b.s.Job == "" {
			t.Fatalf("waiting start = (%+v, %v), want its own job admitted", b.s, b.err)
		}
		synctest.Wait()
		if got := log.ids(); !slices.Equal(got, []scheduler.ID{b.s.Job}) {
			t.Errorf("ran %v, want the waiter's job %s once", got, b.s.Job)
		}
		recs := g.All()
		if len(recs) != 1 || recs[0].ID != b.s.Job || recs[0].End == nil || recs[0].End.Result != scheduler.ResultOK {
			t.Errorf("records %+v, want the waiter's one, ended ok", recs)
		}
	})
}

// Every end a job's Run gives, panics included, is a moving end: it moves the
// scope's last end and its deadline whatever the result.
func TestEveryRunEndMovesTheScope(t *testing.T) {
	for name, run := range map[string]func(context.Context, *scheduler.Run[payload]) scheduler.End{
		"partial": func(context.Context, *scheduler.Run[payload]) scheduler.End {
			return scheduler.End{Result: scheduler.ResultPartial}
		},
		"failed": func(context.Context, *scheduler.Run[payload]) scheduler.End {
			return scheduler.End{Result: scheduler.ResultFailed}
		},
		"skipped": func(context.Context, *scheduler.Run[payload]) scheduler.End {
			return scheduler.End{Result: scheduler.ResultSkipped}
		},
		"panicked": func(context.Context, *scheduler.Run[payload]) scheduler.End { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				full := spec("full_check", "passes")
				full.Run = run
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
				r.schedule(t, "full_check", "", scheduler.Scheduled(time.Hour))
				time.Sleep(10 * time.Minute)
				s := r.start(t, api("full_check", "", "alice", payload{}))
				synctest.Wait()
				rec := r.rec(t, s.Job)
				ends, err := r.store.LastEnds(t.Context())
				if got := ends[scheduler.ScheduledScope{Kind: "full_check"}]; err != nil || !got.At.Equal(rec.EndedAt) {
					t.Errorf("LastEnds = (%v, %v), want the scope's end at %v", ends, err, rec.EndedAt)
				}
				if got, want := r.ex.Deadline("full_check", ""), rec.EndedAt.Add(3*time.Hour); !got.Equal(want) {
					t.Errorf("Deadline = %v, want the end plus the lease %v", got, want)
				}
			})
		})
	}
}

// Each way a job ends tells the Observer of that end exactly once.
func TestEveryJobEndIsReportedOnce(t *testing.T) {
	type script func(t *testing.T, r *rig) scheduler.ID
	gateKind := func(t *testing.T, r *rig) chan struct{} {
		t.Helper()
		gate := make(chan struct{})
		r.start(t, api("hold", "", "carol", payload{Gate: gate}))
		synctest.Wait()
		return gate
	}
	for name, tc := range map[string]struct {
		run  func(context.Context, *scheduler.Run[payload]) scheduler.End
		do   script
		want scheduler.Result
	}{
		"ok": {want: scheduler.ResultOK},
		"partial": {
			run: func(context.Context, *scheduler.Run[payload]) scheduler.End {
				return scheduler.End{Result: scheduler.ResultPartial}
			},
			want: scheduler.ResultPartial,
		},
		"panicked": {
			run:  func(context.Context, *scheduler.Run[payload]) scheduler.End { panic("boom") },
			want: scheduler.ResultFailed,
		},
		"an end that cannot be written": {
			do: func(t *testing.T, r *rig) scheduler.ID {
				t.Helper()
				r.store.Fail(memrecords.OpEnd, errors.New("disk full"))
				return r.start(t, api("work", "", "alice", payload{})).Job
			},
			want: scheduler.ResultOK,
		},
		"cancelled while it runs": {
			do: func(t *testing.T, r *rig) scheduler.ID {
				t.Helper()
				s := r.start(t, api("work", "", "alice", payload{Gate: make(chan struct{})}))
				synctest.Wait()
				if _, err := r.ex.Cancel(t.Context(), s.Job); err != nil {
					t.Fatalf("Cancel = %v", err)
				}
				return s.Job
			},
			want: scheduler.ResultCancelled,
		},
		"cancelled while queued": {
			do: func(t *testing.T, r *rig) scheduler.ID {
				t.Helper()
				gate := gateKind(t, r)
				s := r.start(t, api("work", "", "alice", payload{}))
				if _, err := r.ex.Cancel(t.Context(), s.Job); err != nil {
					t.Fatalf("Cancel = %v", err)
				}
				close(gate)
				return s.Job
			},
			want: scheduler.ResultCancelled,
		},
		"refused by a full queue after its record": {
			do: func(t *testing.T, r *rig) scheduler.ID {
				t.Helper()
				gate := gateKind(t, r)
				defer close(gate)
				r.start(t, api("work", "a", "alice", payload{}))
				_, err := r.ex.Start(t.Context(), api("work", "b", "bob", payload{}))
				if !errors.Is(err, scheduler.ErrQueueFull) {
					t.Fatalf("Start past MaxQueued = %v, want ErrQueueFull", err)
				}
				for _, rec := range r.store.All() {
					if rec.Scope == "b" {
						return rec.ID
					}
				}
				t.Fatal("no record for the refused start")
				return ""
			},
			want: scheduler.ResultSkipped,
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := spec("work", "passes")
				w.Run = tc.run
				seen := &transitions{}
				r := newRig(t, scheduler.Config[payload]{
					Kinds:     []scheduler.KindSpec[payload]{w, spec("hold", "passes")},
					Observer:  seen.observer(),
					MaxQueued: 1,
				})
				do := tc.do
				if do == nil {
					do = func(t *testing.T, r *rig) scheduler.ID {
						t.Helper()
						return r.start(t, api("work", "", "alice", payload{})).Job
					}
				}
				id := do(t, r)
				synctest.Wait()
				if got := ended(seen.of(id)); !slices.Equal(got, []scheduler.Result{tc.want}) {
					t.Errorf("Observer told the ends %v, want [%s] once", got, tc.want)
				}
			})
		})
	}

	t.Run("interrupted at shutdown", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			seen := &transitions{}
			r, stop := (&rig{store: memrecords.New(), log: &runLog{}}).openUntil(t, scheduler.Config[payload]{
				Kinds:    []scheduler.KindSpec[payload]{spec("work", "passes")},
				Observer: seen.observer(),
			})
			running := r.start(t, api("work", "a", "alice", payload{Gate: make(chan struct{})}))
			queued := r.start(t, api("work", "b", "alice", payload{}))
			synctest.Wait()
			stop()
			for _, id := range []scheduler.ID{running.Job, queued.Job} {
				if got := ended(seen.of(id)); !slices.Equal(got, []scheduler.Result{scheduler.ResultInterrupted}) {
					t.Errorf("Observer told job %s the ends %v, want [interrupted] once", id, got)
				}
			}
		})
	})
}

// A cancelled timer job is not a moving end: the scope's deadline stays and
// its timer wakes an interval after the cancelled wake.
func TestCancelledTimerJobWakesAtItsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scan := spec("scan", "passes")
		scan.Run = func(ctx context.Context, _ *scheduler.Run[payload]) scheduler.End {
			<-ctx.Done()
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}})
		start := time.Now()
		r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
		deadline := r.ex.Deadline("scan", "source:a")
		woke := r.nextWake(t, "scan", "source:a", start, time.Hour)
		if _, err := r.ex.Cancel(t.Context(), woke.ID); err != nil {
			t.Fatalf("Cancel = %v", err)
		}
		synctest.Wait()
		r.wantEnd(t, woke.ID, scheduler.ResultCancelled)
		if got := r.ex.Deadline("scan", "source:a"); !got.Equal(deadline) {
			t.Errorf("Deadline = %v after a cancel, want %v", got, deadline)
		}
		r.nextWake(t, "scan", "source:a", woke.QueuedAt, time.Hour)
	})
}

// ended answers the results of the transitions that end a job, in order.
func ended(trs []scheduler.Transition) []scheduler.Result {
	var out []scheduler.Result
	for _, tr := range trs {
		if tr.Result != "" {
			out = append(out, tr.Result)
		}
	}
	return out
}
