package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

// grab is a batch kind of up to 500 items in slot "arr", Payload.N items per
// job, whose Run runs each item with lineOf.
func grab(lineOf func(ctx context.Context, i int) scheduler.Line) scheduler.KindSpec[payload] {
	k := spec("grab", "arr")
	k.MaxItems = 500
	k.Items = func(p payload) int { return p.N }
	k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		for i := range r.Job().Payload.N {
			_ = r.Item(ctx, i, func(ctx context.Context) scheduler.Line { return lineOf(ctx, i) })
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

func done(context.Context, int) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} }

func line(state scheduler.LineState, reason string) scheduler.Line {
	return scheduler.Line{State: state, Reason: reason}
}

// wantLines fails the test unless id's stored lines are exactly want.
func (r *rig) wantLines(t *testing.T, id scheduler.ID, want ...scheduler.Line) {
	t.Helper()
	got := r.rec(t, id).Lines
	if !slices.EqualFunc(got, want, sameLine) {
		t.Errorf("record %q lines %v, want %v", id, got, want)
	}
}

func TestBatchLinesAndResult(t *testing.T) {
	queued := line(scheduler.LineQueued, "")
	lineDone := line(scheduler.LineDone, "")

	t.Run("admission writes one queued line per item", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{grab(done), spec("align", "arr")}})
			gate := make(chan struct{})
			r.start(t, api("align", "", "bob", payload{Gate: gate}))
			synctest.Wait()
			s := r.start(t, api("grab", "", "alice", payload{N: 3}))
			r.wantLines(t, s.Job, queued, queued, queued)
			close(gate)
			synctest.Wait()
			r.wantLines(t, s.Job, lineDone, lineDone, lineDone)
			r.wantEnd(t, s.Job, scheduler.ResultOK)
		})
	})

	t.Run("an item's line reads running while it runs and is stored when it ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				r    *rig
				id   scheduler.ID
				seen [][]scheduler.LineState
			)
			look := func() {
				rec, _, err := r.store.Get(t.Context(), id)
				if err != nil {
					t.Errorf("Get = %v", err)
				}
				var states []scheduler.LineState
				for _, l := range rec.Lines {
					states = append(states, l.State)
				}
				seen = append(seen, states)
			}
			k := grab(func(context.Context, int) scheduler.Line {
				look()
				return scheduler.Line{State: scheduler.LineDone}
			})
			run := k.Run
			k.Run = func(ctx context.Context, rn *scheduler.Run[payload]) scheduler.End {
				id = rn.Job().ID
				end := run(ctx, rn)
				look()
				return end
			}
			r = newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			r.start(t, api("grab", "", "alice", payload{N: 2}))
			synctest.Wait()
			q, run1, d := scheduler.LineQueued, scheduler.LineRunning, scheduler.LineDone
			want := [][]scheduler.LineState{{run1, q}, {d, run1}, {d, d}}
			if !slices.EqualFunc(seen, want, slices.Equal) {
				t.Errorf("stored line states during the batch %v, want %v", seen, want)
			}
		})
	})

	for _, tc := range []struct {
		name   string
		lines  []scheduler.Line
		result scheduler.Result
	}{
		{name: "every line done reads ok", lines: []scheduler.Line{lineDone, lineDone}, result: scheduler.ResultOK},
		{name: "a skipped line makes an ok end partial", lines: []scheduler.Line{lineDone, line(scheduler.LineSkipped, "nothing_found")}, result: scheduler.ResultPartial},
		{name: "a review line makes an ok end partial", lines: []scheduler.Line{line(scheduler.LineReview, "copy_changed"), lineDone}, result: scheduler.ResultPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				facts := json.RawMessage(`{"title":"a release"}`)
				k := grab(func(_ context.Context, i int) scheduler.Line {
					l := tc.lines[i]
					l.Facts = facts
					return l
				})
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
				s := r.start(t, api("grab", "", "alice", payload{N: len(tc.lines)}))
				synctest.Wait()
				r.wantEnd(t, s.Job, tc.result)
				want := slices.Clone(tc.lines)
				for i := range want {
					want[i].Facts = facts
				}
				r.wantLines(t, s.Job, want...)
			})
		})
	}

	notRun := line(scheduler.LineReview, scheduler.ReasonNotRun)
	budget := scheduler.Reason{Code: "arr_unreachable", Subject: "source:sonarr"}
	for _, tc := range []struct {
		name    string
		end     scheduler.End
		items   int
		lines   []scheduler.Line
		reasons []scheduler.Reason
	}{
		{
			name: "a Run that returns ok early ends partial with its unrun lines not_run", items: 1,
			end:   scheduler.End{Result: scheduler.ResultOK},
			lines: []scheduler.Line{lineDone, notRun, notRun},
		},
		{
			name: "a Run that fails early stays failed with its unrun lines not_run", items: 1,
			end:   scheduler.End{Result: scheduler.ResultFailed, Reasons: []scheduler.Reason{budget}},
			lines: []scheduler.Line{lineDone, notRun, notRun}, reasons: []scheduler.Reason{budget},
		},
		{
			name:  "a Run that does nothing stays skipped with every line not_run",
			end:   scheduler.End{Result: scheduler.ResultSkipped, Reasons: []scheduler.Reason{budget}},
			lines: []scheduler.Line{notRun, notRun, notRun}, reasons: []scheduler.Reason{budget},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				k := grab(done)
				k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
					for i := range tc.items {
						if err := r.Item(ctx, i, func(context.Context) scheduler.Line { return lineDone }); err != nil {
							t.Errorf("Item(%d) = %v, want nil", i, err)
						}
					}
					return tc.end
				}
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
				s := r.start(t, api("grab", "", "alice", payload{N: 3}))
				synctest.Wait()
				want := tc.end.Result
				if want == scheduler.ResultOK {
					want = scheduler.ResultPartial
				}
				r.wantEnd(t, s.Job, want, tc.reasons...)
				r.wantLines(t, s.Job, tc.lines...)
			})
		})
	}

	t.Run("a cancel leaves the unrun lines in review as cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var after error
			k := grab(done)
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				_ = r.Item(ctx, 0, func(context.Context) scheduler.Line { return lineDone })
				<-ctx.Done()
				after = r.Item(ctx, 1, func(context.Context) scheduler.Line {
					t.Error("item 1 ran after the cancel")
					return lineDone
				})
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			s := r.start(t, api("grab", "", "alice", payload{N: 3}))
			synctest.Wait()
			if _, err := r.ex.Cancel(t.Context(), s.Job); err != nil {
				t.Fatalf("Cancel = %v", err)
			}
			synctest.Wait()
			if !errors.Is(after, context.Canceled) {
				t.Errorf("Item after the cancel = %v, want context.Canceled", after)
			}
			r.wantEnd(t, s.Job, scheduler.ResultCancelled)
			cancelled := line(scheduler.LineReview, scheduler.ReasonCancelled)
			r.wantLines(t, s.Job, lineDone, cancelled, cancelled)
		})
	})

	t.Run("a restart leaves the unrun lines in review as interrupted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			gate := make(chan struct{})
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{grab(func(ctx context.Context, i int) scheduler.Line {
				if i == 1 {
					select {
					case <-gate:
					case <-ctx.Done():
					}
				}
				return scheduler.Line{State: scheduler.LineDone}
			})}})
			s := r.start(t, api("grab", "", "alice", payload{N: 3}))
			synctest.Wait()
			after := r.store.Crash()
			after.Reopen()
			rec, ok, err := after.Get(t.Context(), s.Job)
			if err != nil || !ok {
				t.Fatalf("reopened record = (%t, %v), want stored", ok, err)
			}
			interrupted := line(scheduler.LineReview, scheduler.ReasonInterrupted)
			reopened := &rig{store: after}
			reopened.wantLines(t, s.Job, lineDone, interrupted, interrupted)
			if rec.End == nil || rec.End.Result != scheduler.ResultInterrupted {
				t.Errorf("reopened end %v, want interrupted", rec.End)
			}
			close(gate)
		})
	})

	t.Run("an item's facts over 2 KiB are dropped", func(t *testing.T) {
		dropped := scheduler.Reason{Code: scheduler.ReasonFactsTooLarge}
		for _, tc := range []struct {
			name  string
			facts json.RawMessage
			keep  bool
		}{
			{name: "2 KiB kept", facts: factsOf(2048), keep: true},
			{name: "over 2 KiB", facts: factsOf(2049)},
			{name: "an array", facts: json.RawMessage(`[1]`)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					lines := &logLines{}
					k := grab(func(context.Context, int) scheduler.Line {
						return scheduler.Line{State: scheduler.LineDone, Facts: tc.facts}
					})
					r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}, Logger: slog.New(lines)})
					s := r.start(t, api("grab", "", "alice", payload{N: 1}))
					synctest.Wait()
					if tc.keep {
						r.wantEnd(t, s.Job, scheduler.ResultOK)
						r.wantLines(t, s.Job, scheduler.Line{State: scheduler.LineDone, Facts: tc.facts})
						if n := lines.count(slog.LevelError); n != 0 {
							t.Errorf("%d ERROR lines, want none", n)
						}
						return
					}
					r.wantEnd(t, s.Job, scheduler.ResultOK, dropped)
					r.wantLines(t, s.Job, lineDone)
					if n := lines.count(slog.LevelError); n != 1 {
						t.Errorf("%d ERROR lines, want 1", n)
					}
				})
			})
		}
	})
}

// TestDismissSurvivesRunningBatch dismisses a running batch's review line:
// the executor's later writes, which still hold that line in review, keep it
// skipped, and the phase written right after the dismiss lands.
func TestDismissSurvivesRunningBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rejected := scheduler.Line{State: scheduler.LineReview, Reason: "arr_rejected", Facts: json.RawMessage(`{"code":400}`)}
		step := make(chan struct{})
		k := grab(done)
		k.Phases = []scheduler.Phase{"send"}
		k.Run = func(ctx context.Context, rn *scheduler.Run[payload]) scheduler.End {
			_ = rn.Item(ctx, 0, func(context.Context) scheduler.Line { return rejected })
			<-step
			rn.Reached("send")
			<-step
			_ = rn.Item(ctx, 1, func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} })
			<-step
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
		s := r.start(t, api("grab", "", "alice", payload{N: 3}))
		synctest.Wait()
		if err := r.store.Dismiss(t.Context(), s.Job, 0); err != nil {
			t.Fatalf("Dismiss(line 0) = %v, want nil", err)
		}
		skipped := rejected
		skipped.State = scheduler.LineSkipped
		queued := line(scheduler.LineQueued, "")
		lineDone := line(scheduler.LineDone, "")

		step <- struct{}{}
		synctest.Wait()
		if got := r.rec(t, s.Job); got.Phase != "send" {
			t.Errorf("phase after the write that follows the dismiss = %q, want send", got.Phase)
		}
		r.wantLines(t, s.Job, skipped, queued, queued)

		step <- struct{}{}
		synctest.Wait()
		r.wantLines(t, s.Job, skipped, lineDone, queued)

		step <- struct{}{}
		synctest.Wait()
		r.wantEnd(t, s.Job, scheduler.ResultPartial)
		r.wantLines(t, s.Job, skipped, lineDone, line(scheduler.LineReview, scheduler.ReasonNotRun))
	})
}

func TestBatchMaxItems(t *testing.T) {
	t.Run("a count outside 1 to MaxItems is refused before any write", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{grab(done)}})
			for _, n := range []int{0, -1, 501} {
				if _, err := r.ex.Start(t.Context(), api("grab", "", "alice", payload{N: n})); !errors.Is(err, scheduler.ErrBatchSize) {
					t.Errorf("Start(%d items) = %v, want ErrBatchSize", n, err)
				}
			}
			if n := r.store.Count(memrecords.OpInsert); n != 0 {
				t.Errorf("%d inserts for refused batches, want 0", n)
			}
			s := r.start(t, api("grab", "", "alice", payload{N: 500}))
			synctest.Wait()
			if rec := r.rec(t, s.Job); len(rec.Lines) != 500 {
				t.Errorf("a batch of 500 has %d lines, want 500", len(rec.Lines))
			}
			r.wantEnd(t, s.Job, scheduler.ResultOK)
		})
	})

	t.Run("NewExecutor refuses 501 items", func(t *testing.T) {
		k := grab(done)
		k.MaxItems = 501
		if ex, err := scheduler.NewExecutor(scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}, Records: memrecords.New()}); err == nil || ex != nil {
			t.Errorf("NewExecutor(MaxItems 501) = (executor %t, %v), want a refusal", ex != nil, err)
		}
	})

	t.Run("an index outside the batch answers ErrUnknownLine and runs nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var ran atomic.Int32
			k := grab(done)
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				for _, i := range []int{-1, 2, 500} {
					err := r.Item(ctx, i, func(context.Context) scheduler.Line {
						ran.Add(1)
						return scheduler.Line{State: scheduler.LineDone}
					})
					if !errors.Is(err, scheduler.ErrUnknownLine) {
						t.Errorf("Item(%d) of 2 = %v, want ErrUnknownLine", i, err)
					}
				}
				for i := range 2 {
					if err := r.Item(ctx, i, func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} }); err != nil {
						t.Errorf("Item(%d) = %v, want nil", i, err)
					}
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			s := r.start(t, api("grab", "", "alice", payload{N: 2}))
			synctest.Wait()
			if n := ran.Load(); n != 0 {
				t.Errorf("%d out-of-range items ran, want 0", n)
			}
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			r.wantLines(t, s.Job, line(scheduler.LineDone, ""), line(scheduler.LineDone, ""))
		})
	})

	t.Run("a repeat of an ended line runs nothing and answers nil", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var repeat error
			k := grab(done)
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				_ = r.Item(ctx, 0, func(context.Context) scheduler.Line { return line(scheduler.LineReview, "copy_changed") })
				repeat = r.Item(ctx, 0, func(context.Context) scheduler.Line {
					t.Error("a repeat of line 0 ran")
					return scheduler.Line{State: scheduler.LineDone}
				})
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			s := r.start(t, api("grab", "", "alice", payload{N: 1}))
			synctest.Wait()
			if repeat != nil {
				t.Errorf("a repeat Item(0) = %v, want nil", repeat)
			}
			r.wantLines(t, s.Job, line(scheduler.LineReview, "copy_changed"))
			r.wantEnd(t, s.Job, scheduler.ResultPartial)
		})
	})

	t.Run("a kind that is not a batch has no lines", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var err error
			scan := spec("scan", "arr")
			scan.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				err = r.Item(ctx, 0, func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} })
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}})
			s := r.start(t, api("scan", "", "alice", payload{N: 1}))
			synctest.Wait()
			if !errors.Is(err, scheduler.ErrUnknownLine) {
				t.Errorf("Item(0) on a kind that is not a batch = %v, want ErrUnknownLine", err)
			}
			if rec := r.rec(t, s.Job); len(rec.Lines) != 0 {
				t.Errorf("lines %v, want none", rec.Lines)
			}
		})
	})
}

func TestCloseLines(t *testing.T) {
	t.Run("answers a copy and leaves the lines it was given", func(t *testing.T) {
		in := []scheduler.Line{line(scheduler.LineDone, ""), line(scheduler.LineQueued, "")}
		got, end := scheduler.CloseLines(in, scheduler.End{Result: scheduler.ResultOK})
		if want := []scheduler.Line{line(scheduler.LineDone, ""), line(scheduler.LineReview, scheduler.ReasonNotRun)}; !slices.EqualFunc(got, want, sameLine) {
			t.Errorf("lines %v, want %v", got, want)
		}
		if end.Result != scheduler.ResultPartial {
			t.Errorf("result %s, want partial", end.Result)
		}
		if in[1].State != scheduler.LineQueued {
			t.Errorf("the given line 1 reads %s, want it left queued", in[1].State)
		}
	})
	t.Run("no lines answers the end unchanged", func(t *testing.T) {
		in := scheduler.End{Result: scheduler.ResultOK, Reasons: []scheduler.Reason{{Code: "slow"}}, Facts: json.RawMessage(`{"n":1}`)}
		lines, end := scheduler.CloseLines(nil, in)
		if len(lines) != 0 || end.Result != scheduler.ResultOK || !slices.Equal(end.Reasons, in.Reasons) || string(end.Facts) != `{"n":1}` {
			t.Errorf("CloseLines(nil, ok) = %v, %+v; want no lines and %+v", lines, end, in)
		}
	})
}

func sameLine(a, b scheduler.Line) bool {
	return a.State == b.State && a.Reason == b.Reason && string(a.Facts) == string(b.Facts)
}
