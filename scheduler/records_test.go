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

// The executor writes a batch's lines before its end, since Records.End
// writes only the end: a line write the store refuses leaves no end, so the
// store's next open closes the lines and the end together.
func TestUnwrittenLinesLeaveTheEndUnwritten(t *testing.T) {
	lineDone, queued := line(scheduler.LineDone, ""), line(scheduler.LineQueued, "")
	itemDone := func(context.Context) scheduler.Line { return lineDone }

	t.Run("open lines the store refuses to close write no end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lines := &logLines{}
			seen := &transitions{}
			gate := make(chan struct{})
			k := grab(done)
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				_ = r.Item(ctx, 0, itemDone)
				<-gate
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{
				Kinds:    []scheduler.KindSpec[payload]{k},
				Logger:   slog.New(lines),
				Observer: seen.observer(),
			})
			s := r.start(t, api("grab", "", "alice", payload{N: 3}))
			synctest.Wait()
			r.store.Fail(memrecords.OpPut, errors.New("disk full"))
			close(gate)
			synctest.Wait()

			if rec := r.rec(t, s.Job); rec.State != scheduler.StateRunning || rec.End != nil {
				t.Errorf("record %s %v after an unwritten close, want it running with no end", rec.State, rec.End)
			}
			r.wantLines(t, s.Job, lineDone, queued, queued)
			if n := r.store.Count(memrecords.OpEnd); n != 0 {
				t.Errorf("End called %d times, want none after the lines' write failed", n)
			}
			if got := lines.text(slog.LevelError); len(got) != 1 {
				t.Errorf("ERROR lines %q, want one naming the unwritten record", got)
			}
			if ends := ended(seen.of(s.Job)); !slices.Equal(ends, []scheduler.Result{scheduler.ResultPartial}) {
				t.Errorf("Observer told the ends %v, want the closed end partial", ends)
			}

			after := r.store.Crash()
			after.Reopen()
			interrupted := line(scheduler.LineReview, scheduler.ReasonInterrupted)
			(&rig{store: after}).wantLines(t, s.Job, lineDone, interrupted, interrupted)
			(&rig{store: after}).wantEnd(t, s.Job, scheduler.ResultInterrupted)
		})
	})

	t.Run("a line write the store refused is written again before the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			faulted, cleared := make(chan struct{}), make(chan struct{})
			k := grab(done)
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				_ = r.Item(ctx, 0, itemDone)
				<-faulted
				_ = r.Item(ctx, 1, itemDone)
				<-cleared
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}, Logger: slog.New(&logLines{})})
			s := r.start(t, api("grab", "", "alice", payload{N: 2}))
			synctest.Wait()
			r.store.Fail(memrecords.OpPut, errors.New("disk full"))
			close(faulted)
			synctest.Wait()
			r.wantLines(t, s.Job, lineDone, queued)
			r.store.Fail(memrecords.OpPut, nil)
			close(cleared)
			synctest.Wait()

			r.wantEnd(t, s.Job, scheduler.ResultOK)
			r.wantLines(t, s.Job, lineDone, lineDone)
		})
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
// its timer wakes an interval after the cancel.
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
		r.nextWake(t, "scan", "source:a", time.Now(), time.Hour)
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

func TestPipelinesStepNOfM(t *testing.T) {
	type mark struct {
		phase scheduler.Phase
		step  struct{ N, Of int }
	}
	// marks runs one job of kind k, whose Run reaches each of reach in turn
	// (committing through Commit), and answers the record's phase and step
	// after each.
	marks := func(t *testing.T, kinds []scheduler.KindSpec[payload], kind scheduler.Kind, reach ...scheduler.Phase) []mark {
		t.Helper()
		var (
			r   *rig
			out []mark
		)
		for i := range kinds {
			if kinds[i].Kind != kind {
				continue
			}
			kinds[i].Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				for _, p := range reach {
					if p == scheduler.PhaseCommitting {
						run.Commit()
					} else {
						run.Reached(p)
					}
					rec, _, err := r.store.Get(t.Context(), run.Job().ID)
					if err != nil {
						t.Errorf("Get = %v", err)
					}
					out = append(out, mark{rec.Phase, rec.Step})
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
		}
		r = newRig(t, scheduler.Config[payload]{Kinds: kinds})
		r.start(t, api(kind, "", "alice", payload{}))
		synctest.Wait()
		return out
	}
	at := func(p scheduler.Phase, n, of int) mark { return mark{p, struct{ N, Of int }{n, of}} }

	t.Run("each kind counts step N of its own pipeline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full, quick := spec("full_check", "passes"), spec("quick_check", "passes")
			full.Phases = []scheduler.Phase{"walking", "matching", "publishing"}
			quick.Phases = []scheduler.Phase{"probing", "publishing"}
			kinds := []scheduler.KindSpec[payload]{full, quick}
			got := marks(t, kinds, "quick_check", "probing", "publishing")
			if want := []mark{at("probing", 1, 2), at("publishing", 2, 2)}; !slices.Equal(got, want) {
				t.Errorf("quick check marks %v, want %v", got, want)
			}
		})
	})

	t.Run("a kind with no pipeline records no step", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got := marks(t, []scheduler.KindSpec[payload]{spec("sweep", "")}, "sweep", scheduler.PhaseCommitting)
			if want := []mark{at(scheduler.PhaseCommitting, 0, 0)}; !slices.Equal(got, want) {
				t.Errorf("sweep marks %v, want %v", got, want)
			}
		})
	})

	t.Run("a pipeline that names started counts it as its first step", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			reeval := spec("reevaluate", "passes")
			reeval.Phases = []scheduler.Phase{scheduler.PhaseStarted, "publishing"}
			got := marks(t, []scheduler.KindSpec[payload]{reeval}, "reevaluate", scheduler.PhaseStarted, "publishing")
			if want := []mark{at(scheduler.PhaseStarted, 1, 2), at("publishing", 2, 2)}; !slices.Equal(got, want) {
				t.Errorf("marks %v, want %v", got, want)
			}
		})
	})

	t.Run("the commit step keeps the step the pipeline reached", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full := spec("full_check", "passes")
			full.Phases = []scheduler.Phase{"walking", "matching"}
			got := marks(t, []scheduler.KindSpec[payload]{full}, "full_check", "walking", "matching", scheduler.PhaseCommitting)
			if want := []mark{at("walking", 1, 2), at("matching", 2, 2), at(scheduler.PhaseCommitting, 2, 2)}; !slices.Equal(got, want) {
				t.Errorf("marks %v, want %v", got, want)
			}
		})
	})

	t.Run("the executor keeps its own copy of the pipeline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full := spec("full_check", "passes")
			full.Phases = []scheduler.Phase{"walking", "publishing"}
			full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				run.Reached("walking")
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
			full.Phases[0] = "changed"
			s := r.start(t, api("full_check", "", "alice", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			if rec := r.rec(t, s.Job); rec.Phase != "walking" || rec.Step.N != 1 {
				t.Errorf("record at %s step %v, want walking, step 1", rec.Phase, rec.Step)
			}
		})
	})

	t.Run("a phase listed twice is refused", func(t *testing.T) {
		full := spec("full_check", "passes")
		full.Phases = []scheduler.Phase{"walking", "matching", "walking"}
		full.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End { return scheduler.End{} }
		if _, err := scheduler.NewExecutor(scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}, Records: memrecords.New()}); err == nil {
			t.Error("NewExecutor = nil error, want one: step N of M is ambiguous for a repeated phase")
		}
	})
}

// slowRecords takes a second over each Insert and Put before writing, as a
// store waiting on its disk, so its stamps fall a second after the
// executor's own clock reading.
type slowRecords struct{ *memrecords.Store }

func (s slowRecords) Insert(ctx context.Context, rec scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) {
	time.Sleep(time.Second)
	return s.Store.Insert(ctx, rec, key)
}

func (s slowRecords) Put(ctx context.Context, rec scheduler.Record) error {
	time.Sleep(time.Second)
	return s.Store.Put(ctx, rec)
}

func TestRecordTimesAreTheStores(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		full := spec("full_check", "passes")
		full.Phases = []scheduler.Phase{"walking"}
		var entered time.Time
		full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
			entered = time.Now()
			run.Reached("walking")
			return scheduler.End{Result: scheduler.ResultOK}
		}
		seen := &transitions{}
		store := memrecords.New()
		r := newRig(t, scheduler.Config[payload]{
			Kinds:    []scheduler.KindSpec[payload]{full},
			Records:  slowRecords{store},
			Observer: seen.observer(),
		})
		r.store = store
		asked := time.Now()
		s := r.start(t, api("full_check", "", "alice", payload{}))
		time.Sleep(time.Minute)
		r.wantEnd(t, s.Job, scheduler.ResultOK)
		rec := r.rec(t, s.Job)
		if want := asked.Add(time.Second); !rec.QueuedAt.Equal(want) {
			t.Errorf("QueuedAt = %v, want the store's stamp %v, not the executor's %v", rec.QueuedAt, want, asked)
		}
		if trs := seen.of(s.Job); len(trs) == 0 || trs[0].Phase != scheduler.PhaseQueued || !trs[0].At.Equal(rec.QueuedAt) {
			t.Errorf("transitions %+v; want the first queued at the stored %v", trs, rec.QueuedAt)
		}
		if !rec.StartedAt.Equal(entered) {
			t.Errorf("StartedAt = %v, want the store's stamp %v, a second after the executor's start", rec.StartedAt, entered)
		}
		if len(rec.Phases) != 1 || !rec.Phases[0].At.Equal(entered.Add(time.Second)) {
			t.Errorf("Phases = %+v, want walking at the store's stamp %v", rec.Phases, entered.Add(time.Second))
		}
	})
}

// A request of another scope does not wait on a record write in flight: it
// is admitted with its own job while the first write is held.
func TestStartOfAnotherScopeBesideARecordWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &heldRecords{Store: memrecords.New(), entered: make(chan struct{}), hold: make(chan struct{})}
		log := &runLog{}
		ex := heldRig(t, g, log)
		first := startAsync(t.Context(), ex, api("quick_check", "source:a", "alice", payload{}))
		<-g.entered
		second := startAsync(t.Context(), ex, api("quick_check", "source:b", "bob", payload{}))
		synctest.Wait()
		var b answer
		select {
		case b = <-second:
		default:
			close(g.hold)
			t.Fatal("a start of another scope waited on the held record write, want it admitted beside it")
		}
		close(g.hold)
		a := <-first
		if a.err != nil || b.err != nil {
			t.Fatalf("Start = (%v, %v), want both admitted", a.err, b.err)
		}
		if b.s.Joined || b.s.Job == a.s.Job {
			t.Errorf("the start beside the write = %+v, first %+v; want a job of its own", b.s, a.s)
		}
		synctest.Wait()
		if got := log.ids(); !slices.Contains(got, a.s.Job) || !slices.Contains(got, b.s.Job) {
			t.Errorf("ran %v, want both jobs", got)
		}
	})
}
