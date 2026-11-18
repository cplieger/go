package scheduler_test

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

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
