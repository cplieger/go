package scheduler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

func TestPhaseTimesKept(t *testing.T) {
	t.Run("each distinct phase keeps its first time, one write per change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			type seen struct {
				phase scheduler.Phase
				step  struct{ N, Of int }
				puts  int
			}
			var (
				r       *rig
				id      scheduler.ID
				during  []seen
				entered time.Time
			)
			full := spec("full", "passes")
			full.Phases = []scheduler.Phase{"walking_sonarr", "walking_radarr", "publishing"}
			ran := make(chan struct{})
			full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				defer close(ran)
				entered = time.Now()
				id = run.Job().ID
				puts := r.store.Count(memrecords.OpPut)
				look := func() {
					rec, _, err := r.store.Get(t.Context(), id)
					if err != nil {
						t.Errorf("Get = %v", err)
					}
					during = append(during, seen{rec.Phase, rec.Step, r.store.Count(memrecords.OpPut) - puts})
				}
				look()
				time.Sleep(time.Second)
				run.Reached("walking_sonarr")
				look()
				time.Sleep(2 * time.Second)
				run.Reached("walking_radarr")
				run.Reached("walking_radarr")
				look()
				time.Sleep(3 * time.Second)
				run.Reached("walking_sonarr")
				look()
				time.Sleep(time.Second)
				run.Reached("publishing")
				look()
				time.Sleep(4 * time.Second)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r = newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
			r.start(t, timer("full", "", payload{}))
			time.Sleep(time.Minute)
			<-ran
			step := func(n int) struct{ N, Of int } { return struct{ N, Of int }{n, 3} }
			want := []seen{
				{phase: scheduler.PhaseStarted, step: struct{ N, Of int }{0, 3}},
				{"walking_sonarr", step(1), 1},
				{"walking_radarr", step(2), 2},
				{"walking_sonarr", step(1), 3},
				{"publishing", step(3), 4},
			}
			if !slices.Equal(during, want) {
				t.Errorf("the record during the run = %v, want %v", during, want)
			}
			rec := r.rec(t, id)
			if !rec.StartedAt.Equal(entered) {
				t.Errorf("StartedAt = %v, want the time Run was called, %v", rec.StartedAt, entered)
			}
			wantPhases := []scheduler.PhaseAt{
				{Phase: "walking_sonarr", At: entered.Add(time.Second)},
				{Phase: "walking_radarr", At: entered.Add(3 * time.Second)},
				{Phase: "publishing", At: entered.Add(7 * time.Second)},
			}
			if !slices.EqualFunc(rec.Phases, wantPhases, func(a, b scheduler.PhaseAt) bool {
				return a.Phase == b.Phase && a.At.Equal(b.At)
			}) {
				t.Errorf("Phases = %v, want each distinct phase at its first time %v", rec.Phases, wantPhases)
			}
			if rec.State != scheduler.StateEnded || rec.Phase != "publishing" || rec.Step != step(3) {
				t.Errorf("ended record %s at %s %v, want ended at publishing, step 3 of 3", rec.State, rec.Phase, rec.Step)
			}
			if !rec.EndedAt.Equal(entered.Add(11 * time.Second)) {
				t.Errorf("EndedAt = %v, want %v", rec.EndedAt, entered.Add(11*time.Second))
			}
		})
	})

	t.Run("a phase outside the kind's Phases ends the job failed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			quick := spec("quick", "passes")
			quick.Phases = []scheduler.Phase{"probing"}
			quick.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				run.Reached("probing")
				run.Reached("unlisted")
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{quick}})
			s := r.start(t, timer("quick", "", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, scheduler.Reason{Code: scheduler.ReasonRunPanicked})
			if rec := r.rec(t, s.Job); rec.Phase != "probing" || len(rec.Phases) != 1 || rec.Phases[0].Phase != "probing" {
				t.Errorf("record at %s with %v, want it left at probing", rec.Phase, rec.Phases)
			}
		})
	})
}

// factsOf answers a JSON object of exactly n bytes.
func factsOf(n int) json.RawMessage {
	return json.RawMessage(`{"k":"` + strings.Repeat("a", n-len(`{"k":""}`)) + `"}`)
}

func TestFactsOverCapDropped(t *testing.T) {
	budget := scheduler.Reason{Code: "budget_reserve", Subject: "connection:3"}
	dropped := scheduler.Reason{Code: scheduler.ReasonFactsTooLarge}
	for _, tc := range []struct {
		name  string
		facts json.RawMessage
		keep  bool
	}{
		{name: "an object of 2 KiB is kept", facts: factsOf(2048), keep: true},
		{name: "an object over 2 KiB is dropped", facts: factsOf(2049)},
		{name: "an array is dropped", facts: json.RawMessage(`[1,2]`)},
		{name: "a string is dropped", facts: json.RawMessage(`"judged"`)},
		{name: "a broken object is dropped", facts: json.RawMessage(`{"judged":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lines := &logLines{}
				check := spec("check", "")
				check.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
					return scheduler.End{Result: scheduler.ResultPartial, Reasons: []scheduler.Reason{budget}, Facts: tc.facts}
				}
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{check}, Logger: slog.New(lines)})
				s := r.start(t, api("check", "", "alice", payload{}))
				synctest.Wait()
				rec := r.rec(t, s.Job)
				if rec.State != scheduler.StateEnded || rec.End == nil {
					t.Fatalf("record %s with end %v, want the end committed", rec.State, rec.End)
				}
				if rec.End.Result != scheduler.ResultPartial {
					t.Errorf("Result = %s, want partial: dropped facts leave the result", rec.End.Result)
				}
				if tc.keep {
					if string(rec.End.Facts) != string(tc.facts) || !slices.Equal(rec.End.Reasons, []scheduler.Reason{budget}) {
						t.Errorf("end = %d bytes of facts with %v, want the facts kept and only %v", len(rec.End.Facts), rec.End.Reasons, budget)
					}
					if n := lines.count(slog.LevelError); n != 0 {
						t.Errorf("%d ERROR lines, want none", n)
					}
					return
				}
				if len(rec.End.Facts) != 0 {
					t.Errorf("Facts = %s, want dropped", rec.End.Facts)
				}
				if got := rec.End.Reasons; len(got) != 2 || !slices.Contains(got, budget) || !slices.Contains(got, dropped) {
					t.Errorf("Reasons = %v, want %v and %v", got, budget, dropped)
				}
				if n := lines.count(slog.LevelError); n != 1 {
					t.Errorf("%d ERROR lines, want 1", n)
				}
			})
		})
	}
}

func TestEndReasonsCappedAt32(t *testing.T) {
	reasons := func(n int) []scheduler.Reason {
		out := make([]scheduler.Reason, n)
		for i := range out {
			out[i] = scheduler.Reason{Code: "missing", Subject: fmt.Sprintf("connection:%d", i)}
		}
		return out
	}
	dropped := scheduler.Reason{Code: scheduler.ReasonFactsTooLarge}
	for _, tc := range []struct {
		name  string
		end   scheduler.End
		want  []scheduler.Reason
		error int
	}{
		{name: "32 reasons are kept", end: scheduler.End{Result: scheduler.ResultPartial, Reasons: reasons(32)}, want: reasons(32)},
		{name: "the most severe 32 of 40 are kept", end: scheduler.End{Result: scheduler.ResultPartial, Reasons: reasons(40)}, want: reasons(32), error: 1},
		{
			name:  "dropped facts keep their reason within the 32",
			end:   scheduler.End{Result: scheduler.ResultPartial, Reasons: reasons(32), Facts: factsOf(2049)},
			want:  append(reasons(31), dropped),
			error: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lines := &logLines{}
				check := spec("check", "")
				check.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End { return tc.end }
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{check}, Logger: slog.New(lines)})
				s := r.start(t, api("check", "", "alice", payload{}))
				synctest.Wait()
				r.wantEnd(t, s.Job, scheduler.ResultPartial, tc.want...)
				if n := lines.count(slog.LevelError); n != tc.error {
					t.Errorf("%d ERROR lines, want %d", n, tc.error)
				}
			})
		})
	}
}

func TestExecutorReportsStoredEnd(t *testing.T) {
	unwritable := scheduler.Reason{Code: "store_unwritable"}

	t.Run("an end written in the job's commit is the one reported", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				r         *rig
				committed time.Time
			)
			facts := json.RawMessage(`{"judged":7}`)
			full := spec("full", "passes")
			full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				if !run.Commit() {
					t.Error("Commit = false for a job nobody cancelled")
				}
				if err := r.store.EndIn(run.Job().ID, scheduler.End{Result: scheduler.ResultOK, Facts: facts}); err != nil {
					t.Errorf("EndIn = %v", err)
				}
				committed = time.Now()
				time.Sleep(time.Second)
				return scheduler.End{Result: scheduler.ResultFailed, Reasons: []scheduler.Reason{unwritable}}
			}
			seen := &transitions{}
			r = newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}, Observer: seen.observer()})
			if err := r.ex.Schedule("full", "", scheduler.Scheduled(time.Hour), time.Time{}); err != nil {
				t.Fatalf("Schedule = %v", err)
			}
			s := r.start(t, api("full", "", "alice", payload{}))
			time.Sleep(2 * time.Second)
			synctest.Wait()

			rec := r.rec(t, s.Job)
			if rec.End == nil || rec.End.Result != scheduler.ResultOK || string(rec.End.Facts) != string(facts) || len(rec.End.Reasons) != 0 {
				t.Errorf("stored end = %+v, want the commit's ok with its facts", rec.End)
			}
			if !rec.EndedAt.Equal(committed) {
				t.Errorf("EndedAt = %v, want the commit's %v", rec.EndedAt, committed)
			}
			lease := health.Lease{Interval: time.Hour, Cycles: 3}.Duration()
			if got, want := r.ex.Deadline("full", ""), committed.Add(lease); !got.Equal(want) {
				t.Errorf("Deadline = %v, want the commit's end plus the lease, %v", got, want)
			}
			trs := seen.of(s.Job)
			if len(trs) == 0 || trs[len(trs)-1].Result != scheduler.ResultOK {
				t.Errorf("transitions %v, want the last to report ok", trs)
			}
			if n := r.store.Count(memrecords.OpInsert); n != 1 {
				t.Errorf("%d inserts, want 1", n)
			}
		})
	})

	t.Run("a batch whose commit ends it with lines open reads partial", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var r *rig
			early := grab(done)
			early.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				_ = run.Item(ctx, 0, func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} })
				if !run.Commit() {
					t.Error("Commit = false for a job nobody cancelled")
				}
				if err := r.store.EndIn(run.Job().ID, scheduler.End{Result: scheduler.ResultOK}); err != nil {
					t.Errorf("EndIn = %v", err)
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
			seen := &transitions{}
			r = newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{early}, Observer: seen.observer()})
			s := r.start(t, api("grab", "", "alice", payload{N: 3}))
			synctest.Wait()

			r.wantEnd(t, s.Job, scheduler.ResultPartial)
			notRun := line(scheduler.LineReview, scheduler.ReasonNotRun)
			r.wantLines(t, s.Job, line(scheduler.LineDone, ""), notRun, notRun)
			if trs := seen.of(s.Job); len(trs) == 0 || trs[len(trs)-1].Result != scheduler.ResultPartial {
				t.Errorf("transitions %v, want the last to report partial", trs)
			}
		})
	})

	t.Run("with no end in its commit the returned end is written", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full := spec("full", "passes")
			full.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
				return scheduler.End{Result: scheduler.ResultFailed, Reasons: []scheduler.Reason{unwritable}}
			}
			seen := &transitions{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}, Observer: seen.observer()})
			s := r.start(t, api("full", "", "alice", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, unwritable)
			if trs := seen.of(s.Job); len(trs) == 0 || trs[len(trs)-1].Result != scheduler.ResultFailed {
				t.Errorf("transitions %v, want the last to report failed", trs)
			}
		})
	})
}
