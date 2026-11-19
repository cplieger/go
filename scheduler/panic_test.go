package scheduler_test

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

var panicked = scheduler.Reason{Code: scheduler.ReasonRunPanicked}

// wantPanicLogged fails the test unless exactly one ERROR line was logged and
// it holds each of want: the panic's value and a frame of its stack.
func wantPanicLogged(t *testing.T, lines *logLines, want ...string) {
	t.Helper()
	got := lines.text(slog.LevelError)
	if len(got) != 1 {
		t.Fatalf("ERROR lines %q, want exactly 1", got)
	}
	for _, w := range want {
		if !strings.Contains(got[0], w) {
			t.Errorf("ERROR line %q, want it to hold %q", got[0], w)
		}
	}
}

// panicRig runs kinds plus a "next" kind in slot "passes", and answers the
// rig, its ERROR log and its transitions.
func panicRig(t *testing.T, kinds ...scheduler.KindSpec[payload]) (*rig, *logLines, *transitions) {
	t.Helper()
	lines, seen := &logLines{}, &transitions{}
	kinds = append(kinds, spec("next", "passes"))
	r := newRig(t, scheduler.Config[payload]{Kinds: kinds, Logger: slog.New(lines), Observer: seen.observer()})
	return r, lines, seen
}

// wantSlotFreed fails the test unless a job of kind next, started now in the
// slot the panicked job held, runs.
func (r *rig) wantSlotFreed(t *testing.T) {
	t.Helper()
	s := r.start(t, api("next", "", "bob", payload{}))
	synctest.Wait()
	if !r.log.ran(s.Job) {
		t.Error("the next job in the slot did not run: the panicked job kept it")
	}
	r.wantEnd(t, s.Job, scheduler.ResultOK)
}

func wantLastResult(t *testing.T, seen *transitions, id scheduler.ID, want scheduler.Result) {
	t.Helper()
	trs := seen.of(id)
	if len(trs) == 0 || trs[len(trs)-1].Result != want {
		t.Errorf("transitions %v, want the last to report %s", trs, want)
	}
}

func TestRunPanicEndsJobFailed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		full := spec("full", "passes")
		full.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
			panic("judge index out of range")
		}
		r, lines, seen := panicRig(t, full)
		s := r.start(t, timer("full", "", payload{}))
		synctest.Wait()
		r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
		if rec := r.rec(t, s.Job); rec.EndedAt.IsZero() {
			t.Error("EndedAt is zero, want the panic's time")
		}
		wantPanicLogged(t, lines, "judge index out of range", "TestRunPanicEndsJobFailed")
		wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
		r.wantSlotFreed(t)
	})
}

func TestItemPanicEndsBatch(t *testing.T) {
	for _, tc := range []struct {
		lineOf func(int) scheduler.Line
		name   string
		want   []string
	}{
		{
			name: "a panic in an item's fn",
			lineOf: func(i int) scheduler.Line {
				if i == 1 {
					panic("offer without a title")
				}
				return scheduler.Line{State: scheduler.LineDone}
			},
			want: []string{"offer without a title", "TestItemPanicEndsBatch"},
		},
		{
			name: "an item's fn answering a line that is not an end",
			lineOf: func(i int) scheduler.Line {
				if i == 1 {
					return scheduler.Line{State: scheduler.LineRunning}
				}
				return scheduler.Line{State: scheduler.LineDone}
			},
			want: []string{string(scheduler.LineRunning)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var ranAfter atomic.Bool
				k := grab(func(_ context.Context, i int) scheduler.Line {
					if i == 2 {
						ranAfter.Store(true)
					}
					return tc.lineOf(i)
				})
				k.Slot = "passes"
				r, lines, seen := panicRig(t, k)
				s := r.start(t, api("grab", "", "alice", payload{N: 3}))
				synctest.Wait()
				r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
				if ranAfter.Load() {
					t.Error("item 2 ran after item 1 failed, want the batch ended")
				}
				rec := r.rec(t, s.Job)
				if len(rec.Lines) != 3 {
					t.Fatalf("lines %v, want 3", rec.Lines)
				}
				if rec.Lines[0].State != scheduler.LineDone {
					t.Errorf("line 0 = %v, want done", rec.Lines[0])
				}
				for i, l := range rec.Lines {
					if l.State == scheduler.LineQueued || l.State == scheduler.LineRunning {
						t.Errorf("line %d = %v on an ended batch, want an end state", i, l)
					}
				}
				if l := rec.Lines[1]; l.State != scheduler.LineReview || l.Reason != scheduler.ReasonRunPanicked {
					t.Errorf("line 1 = %v, want review run_panicked: the item that broke", l)
				}
				if l := rec.Lines[2]; l.State != scheduler.LineReview || l.Reason != scheduler.ReasonNotRun {
					t.Errorf("line 2 = %v, want review not_run", l)
				}
				wantPanicLogged(t, lines, tc.want...)
				wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
				r.wantSlotFreed(t)
			})
		})
	}
}

func TestReachedUnknownPhaseEndsJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var past atomic.Bool
		full := spec("full", "passes")
		full.Phases = []scheduler.Phase{"fetched", "published"}
		full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
			run.Reached("fetched")
			run.Reached("judged")
			past.Store(true)
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r, lines, seen := panicRig(t, full)
		s := r.start(t, timer("full", "", payload{}))
		synctest.Wait()
		if past.Load() {
			t.Error("Run went on past Reached of an unknown phase, want a panic there")
		}
		r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
		if rec := r.rec(t, s.Job); rec.Phase != "fetched" {
			t.Errorf("record at phase %q, want the last listed phase reached, fetched", rec.Phase)
		}
		wantPanicLogged(t, lines, "judged", "TestReachedUnknownPhaseEndsJob")
		wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
		r.wantSlotFreed(t)
	})
}

// TestObserverPanicInRunEndsJob pins that an Observer callback panicking on
// Run's stack ends the job as a panic in Run does: the slot is freed and the
// Observer gets the end.
func TestObserverPanicInRunEndsJob(t *testing.T) {
	t.Run("a batch item's slot wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lines, seen := &logLines{}, &transitions{}
			obs := seen.observer()
			var panicked1 atomic.Bool
			obs.SlotWait = func(_ string, c scheduler.Class, _ time.Duration) {
				if c == scheduler.Batch && panicked1.CompareAndSwap(false, true) {
					panic("slot wait gauge")
				}
			}
			k := grab(done)
			k.Slot = "passes"
			r := newRig(t, scheduler.Config[payload]{
				Kinds:  []scheduler.KindSpec[payload]{k, spec("next", "passes")},
				Logger: slog.New(lines), Observer: obs,
			})
			s := r.start(t, api("grab", "", "alice", payload{N: 2}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
			wantPanicLogged(t, lines, "slot wait gauge", "TestObserverPanicInRunEndsJob")
			wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
			r.wantSlotFreed(t)
		})
	})
	t.Run("a batch item whose slot wait panicked runs when Run recovers and calls it again", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var panicked1 atomic.Bool
			obs := scheduler.Observer{SlotWait: func(string, scheduler.Class, time.Duration) {
				if panicked1.CompareAndSwap(false, true) {
					panic("slot wait gauge")
				}
			}}
			k := spec("grab", "passes")
			k.MaxItems = 1
			k.Items = func(payload) int { return 1 }
			k.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				fn := func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} }
				func() {
					defer func() { _ = recover() }()
					_ = run.Item(ctx, 0, fn)
				}()
				if err := run.Item(ctx, 0, fn); err != nil {
					t.Errorf("second Item(0) = %v, want nil", err)
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{
				Kinds: []scheduler.KindSpec[payload]{k, spec("next", "passes")}, Observer: obs,
			})
			s := r.start(t, api("grab", "", "alice", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			r.wantLines(t, s.Job, scheduler.Line{State: scheduler.LineDone})
			r.wantSlotFreed(t)
		})
	})
	t.Run("the slot wait of a yield taking its slot back", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lines, seen := &logLines{}, &transitions{}
			obs := seen.observer()
			var calls atomic.Int32
			obs.SlotWait = func(string, scheduler.Class, time.Duration) {
				if calls.Add(1) == 2 {
					panic("slot wait gauge")
				}
			}
			full := spec("full", "passes")
			full.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				_ = run.Yield(ctx, time.Now().Add(time.Minute))
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{
				Kinds:  []scheduler.KindSpec[payload]{full, spec("next", "passes")},
				Logger: slog.New(lines), Observer: obs,
			})
			s := r.start(t, timer("full", "", payload{}))
			time.Sleep(time.Minute)
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
			wantPanicLogged(t, lines, "slot wait gauge", "TestObserverPanicInRunEndsJob")
			wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
			r.wantSlotFreed(t)
		})
	})
	t.Run("a transition of a phase Run reached", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lines, seen := &logLines{}, &transitions{}
			record := seen.observer().Transition
			obs := scheduler.Observer{Transition: func(tr scheduler.Transition) {
				record(tr)
				if tr.Phase == "fetched" && tr.Result == "" {
					panic("transition gauge")
				}
			}}
			full := spec("full", "passes")
			full.Phases = []scheduler.Phase{"fetched"}
			full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				run.Reached("fetched")
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{
				Kinds:  []scheduler.KindSpec[payload]{full, spec("next", "passes")},
				Logger: slog.New(lines), Observer: obs,
			})
			s := r.start(t, timer("full", "", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, panicked)
			wantPanicLogged(t, lines, "transition gauge", "TestObserverPanicInRunEndsJob")
			wantLastResult(t, seen, s.Job, scheduler.ResultFailed)
			r.wantSlotFreed(t)
		})
	})
}
