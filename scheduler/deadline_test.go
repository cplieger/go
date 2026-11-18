package scheduler_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

// hourLease puts a scope's deadline an hour after its last moving end.
var hourLease = &health.Lease{Interval: time.Hour, Cycles: 1}

// endsBy is a Run that waits on the job's Gate, if any, then ends with
// results[Payload.N].
func endsBy(results []scheduler.Result) func(context.Context, *scheduler.Run[payload]) scheduler.End {
	return func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		p := r.Job().Payload
		if p.Gate != nil {
			select {
			case <-p.Gate:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: results[p.N]}
	}
}

func TestDeadlineMovesOnlyOnResults(t *testing.T) {
	t.Run("an end of a job that started moves it unless cancelled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			results := []scheduler.Result{scheduler.ResultOK, scheduler.ResultPartial, scheduler.ResultFailed, scheduler.ResultSkipped}
			scan := spec("scan", "heavy")
			scan.Watch = hourLease
			scan.Run = endsBy(results)
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}})
			t0 := time.Now()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(24*time.Hour))
			if got, want := r.ex.Deadline("scan", "source:a"), t0.Add(time.Hour); !got.Equal(want) {
				t.Errorf("Deadline before any end = %v, want %v: the start plus the lease", got, want)
			}
			for n, res := range results {
				time.Sleep(10 * time.Minute)
				s := r.start(t, api("scan", "source:a", "alice", payload{N: n}))
				synctest.Wait()
				r.wantEnd(t, s.Job, res)
				if got, want := r.ex.Deadline("scan", "source:a"), r.rec(t, s.Job).EndedAt.Add(time.Hour); !got.Equal(want) {
					t.Errorf("Deadline after a %s end = %v, want %v", res, got, want)
				}
			}
			before := r.ex.Deadline("scan", "source:a")
			time.Sleep(10 * time.Minute)
			gate := make(chan struct{})
			s := r.start(t, api("scan", "source:a", "alice", payload{Gate: gate}))
			synctest.Wait()
			if _, err := r.ex.Cancel(t.Context(), s.Job); err != nil {
				t.Fatalf("Cancel(running) = %v", err)
			}
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultCancelled)
			if got := r.ex.Deadline("scan", "source:a"); !got.Equal(before) {
				t.Errorf("Deadline after a cancelled end = %v, want it unmoved at %v", got, before)
			}
		})
	})

	t.Run("a job that never started moves nothing and its covering job's end moves it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			quick := spec("quick", "passes")
			quick.Watch = hourLease
			full := spec("full", "passes")
			full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "quick" }
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{quick, full, spec("hold", "passes")}})
			r.schedule(t, "quick", "", scheduler.Scheduled(24*time.Hour))
			before := r.ex.Deadline("quick", "")
			gate := make(chan struct{})
			r.start(t, api("hold", "", "alice", payload{Gate: gate}))
			synctest.Wait()

			time.Sleep(5 * time.Minute)
			c := r.start(t, timer("quick", "", payload{}))
			if _, err := r.ex.Cancel(t.Context(), c.Job); err != nil {
				t.Fatalf("Cancel(queued) = %v", err)
			}
			synctest.Wait()
			r.wantEnd(t, c.Job, scheduler.ResultCancelled)
			if got := r.ex.Deadline("quick", ""); !got.Equal(before) {
				t.Errorf("Deadline after a cancel while queued = %v, want it unmoved at %v", got, before)
			}

			q := r.start(t, timer("quick", "", payload{}))
			fullGate := make(chan struct{})
			f := r.start(t, api("full", "", "bob", payload{Gate: fullGate}))
			time.Sleep(5 * time.Minute)
			close(gate)
			synctest.Wait()
			r.wantEnd(t, q.Job, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(f.Job)})
			if got := r.ex.Deadline("quick", ""); !got.Equal(before) {
				t.Errorf("Deadline after a covered skip = %v, want it unmoved at %v", got, before)
			}
			time.Sleep(5 * time.Minute)
			close(fullGate)
			synctest.Wait()
			if got, want := r.ex.Deadline("quick", ""), r.rec(t, f.Job).EndedAt.Add(time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after the covering job ended = %v, want %v: its end moves the covered scope", got, want)
			}
		})
	})

	t.Run("a covering kind's end moves the covered scope with none queued and after a restart", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			quick := spec("quick", "passes")
			quick.Watch = hourLease
			full := spec("full", "passes")
			full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "quick" }
			cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{quick, full}}
			before, stop := base.openUntil(t, cfg)
			before.schedule(t, "quick", "", scheduler.Scheduled(24*time.Hour))
			time.Sleep(10 * time.Minute)
			before.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			time.Sleep(10 * time.Minute)
			f := before.start(t, api("full", "", "bob", payload{}))
			synctest.Wait()
			fullEnd := before.rec(t, f.Job).EndedAt
			if got, want := before.ex.Deadline("quick", ""), fullEnd.Add(time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after the covering kind ended with nothing queued = %v, want %v", got, want)
			}
			stop()

			reopened := &rig{store: base.store.Crash(), log: base.log}
			time.Sleep(10 * time.Minute)
			r := reopened.open(t, cfg)
			synctest.Wait()
			r.schedule(t, "quick", "", scheduler.Scheduled(24*time.Hour))
			if got, want := r.ex.Deadline("quick", ""), fullEnd.Add(time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a restart = %v, want %v: the covering kind's end is the later moving end", got, want)
			}
		})
	})

	t.Run("an interrupted job leaves the deadline at the end before it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			scan := spec("scan", "heavy")
			scan.Watch = hourLease
			cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}}
			before, stop := base.openUntil(t, cfg)
			before.schedule(t, "scan", "source:a", scheduler.Scheduled(24*time.Hour))
			time.Sleep(10 * time.Minute)
			ok := before.start(t, api("scan", "source:a", "alice", payload{}))
			synctest.Wait()
			lastEnd := before.rec(t, ok.Job).EndedAt
			time.Sleep(10 * time.Minute)
			cut := before.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			stop()

			reopened := &rig{store: base.store.Crash(), log: base.log}
			reopened.store.Reopen()
			time.Sleep(10 * time.Minute)
			r := reopened.open(t, cfg)
			synctest.Wait()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(24*time.Hour))
			r.wantEnd(t, cut.Job, scheduler.ResultInterrupted)
			if got, want := r.ex.Deadline("scan", "source:a"), lastEnd.Add(time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a restart = %v, want %v: the ok end before the interrupted job", got, want)
			}
		})
	})
}

func TestUnscheduleDeletesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lines := &logLines{}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}, Logger: slog.New(lines)})
		if got := r.ex.Deadline("scan", "source:a"); !got.IsZero() {
			t.Errorf("Deadline of a scope never scheduled = %v, want zero", got)
		}
		if !r.ex.Fresh("scan", "source:a").Healthy() {
			t.Error("Fresh of a scope never scheduled is unhealthy, want healthy")
		}
		t0 := time.Now()
		r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
		if got, want := r.ex.Deadline("scan", "source:a"), t0.Add(3*time.Hour); !got.Equal(want) {
			t.Fatalf("Deadline of a scheduled scope = %v, want %v", got, want)
		}
		fresh := r.ex.Fresh("scan", "source:a")
		time.Sleep(10 * time.Minute)
		r.ex.Unschedule("scan", "source:a")
		if got := r.ex.Deadline("scan", "source:a"); !got.IsZero() {
			t.Errorf("Deadline after Unschedule = %v, want zero, so the stall series drops the scope", got)
		}
		sleepUntil(t0.Add(5 * time.Hour))
		if !fresh.Healthy() || !r.ex.Fresh("scan", "source:a").Healthy() {
			t.Error("Fresh unhealthy past the removed scope's old deadline, want healthy")
		}
		if n := len(r.wakes("scan", "source:a")); n != 0 {
			t.Errorf("%d wakes of an unscheduled scope, want 0", n)
		}
		if n := lines.count(slog.LevelError); n != 0 {
			t.Errorf("%d ERROR lines for an unscheduled scope, want 0: %q", n, lines.text(slog.LevelError))
		}
	})
}

// latch holds each Run that waits on it until it is opened; reset makes it
// hold again.
type latch struct {
	ch chan struct{}
	mu sync.Mutex
}

func newLatch() *latch { return &latch{ch: make(chan struct{})} }

func (l *latch) wait(ctx context.Context) {
	l.mu.Lock()
	ch := l.ch
	l.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (l *latch) open() {
	l.mu.Lock()
	defer l.mu.Unlock()
	close(l.ch)
}

func (l *latch) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ch = make(chan struct{})
}

// deadlineLines answers the lines logged at level that name the deadline of
// kind and scope.
func deadlineLines(lines *logLines, level slog.Level, kind scheduler.Kind, scope string) []string {
	var out []string
	for _, l := range lines.text(level) {
		words := strings.Fields(l)
		if strings.Contains(l, "deadline") && slices.Contains(words, "kind="+string(kind)) && slices.Contains(words, "scope="+scope) {
			out = append(out, l)
		}
	}
	return out
}

func TestFreshLogsOncePerTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lines := &logLines{}
		hold := newLatch()
		scan := spec("scan", "")
		scan.Run = func(ctx context.Context, _ *scheduler.Run[payload]) scheduler.End {
			hold.wait(ctx)
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}, Logger: slog.New(lines)})
		t0 := time.Now()
		r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
		r.schedule(t, "scan", "source:b", scheduler.Scheduled(10*time.Hour))
		if got, want := r.ex.Deadline("scan", "source:a"), t0.Add(3*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline before the first end = %v, want %v: the executor's start plus three intervals", got, want)
		}
		a := r.ex.Fresh("scan", "source:a")

		sleepUntil(t0.Add(3*time.Hour - time.Second))
		if !a.Healthy() {
			t.Error("Fresh unhealthy before the deadline, want healthy")
		}
		if n := lines.count(slog.LevelError); n != 0 {
			t.Errorf("%d ERROR lines before the deadline, want 0: %q", n, lines.text(slog.LevelError))
		}
		sleepUntil(t0.Add(3*time.Hour + time.Second))
		if a.Healthy() {
			t.Error("Fresh healthy past the deadline with no end, want unhealthy")
		}
		if !r.ex.Fresh("scan", "source:b").Healthy() {
			t.Error("another scope's Fresh unhealthy, want healthy: each scope has its own deadline")
		}
		if got := deadlineLines(lines, slog.LevelError, "scan", "source:a"); len(got) != 1 || lines.count(slog.LevelError) != 1 {
			t.Errorf("ERROR lines once the deadline passed = %q, want one naming the deadline, kind and scope", lines.text(slog.LevelError))
		}
		sleepUntil(t0.Add(5 * time.Hour))
		if n := lines.count(slog.LevelError); n != 1 {
			t.Errorf("%d ERROR lines 2 h past the deadline, want 1: once per passing", n)
		}

		hold.open()
		synctest.Wait()
		hold.reset()
		ended := time.Now()
		time.Sleep(time.Minute)
		synctest.Wait()
		if !a.Healthy() {
			t.Error("Fresh unhealthy after an end, want healthy")
		}
		if got, want := r.ex.Deadline("scan", "source:a"), ended.Add(3*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline after the end = %v, want %v", got, want)
		}
		if got := deadlineLines(lines, slog.LevelInfo, "scan", "source:a"); len(got) != 1 {
			t.Errorf("INFO lines naming the deadline after it cleared = %q, want one", got)
		}

		sleepUntil(ended.Add(3*time.Hour + time.Second))
		if a.Healthy() {
			t.Error("Fresh healthy past the second deadline, want unhealthy")
		}
		if got := deadlineLines(lines, slog.LevelError, "scan", "source:a"); len(got) != 2 {
			t.Errorf("ERROR lines after the second passing = %q, want two: one per passing", got)
		}
		if got := deadlineLines(lines, slog.LevelError, "scan", "source:b"); len(got) != 0 {
			t.Errorf("ERROR lines for the scope whose deadline is ahead = %q, want none", got)
		}
	})
}

func TestPassedDeadlineClearsOnScheduleChange(t *testing.T) {
	cases := []struct {
		change func(r *rig) error
		name   string
	}{
		{name: "Unschedule", change: func(r *rig) error { r.ex.Unschedule("scan", "source:a"); return nil }},
		{name: "a TriggerOnly Schedule", change: func(r *rig) error { return r.ex.Schedule("scan", "source:a", scheduler.TriggerOnly()) }},
		{name: "a changed cadence", change: func(r *rig) error { return r.ex.Schedule("scan", "source:a", scheduler.Scheduled(2*time.Hour)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lines := &logLines{}
				hold := newLatch()
				scan := spec("scan", "")
				scan.Run = func(ctx context.Context, _ *scheduler.Run[payload]) scheduler.End {
					hold.wait(ctx)
					return scheduler.End{Result: scheduler.ResultOK}
				}
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}, Logger: slog.New(lines)})
				t0 := time.Now()
				r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
				sleepUntil(t0.Add(3*time.Hour + time.Second))
				if got := deadlineLines(lines, slog.LevelError, "scan", "source:a"); len(got) != 1 {
					t.Fatalf("ERROR lines once the deadline passed = %q, want one", got)
				}
				if err := c.change(r); err != nil {
					t.Fatalf("%s = %v, want nil", c.name, err)
				}
				synctest.Wait()
				if !r.ex.Fresh("scan", "source:a").Healthy() {
					t.Errorf("Fresh unhealthy after %s, want healthy", c.name)
				}
				if got := deadlineLines(lines, slog.LevelInfo, "scan", "source:a"); len(got) != 1 {
					t.Errorf("INFO lines naming the deadline after %s = %q, want one: the passed deadline cleared", c.name, got)
				}
				sleepUntil(time.Now().Add(time.Minute))
				if got := deadlineLines(lines, slog.LevelError, "scan", "source:a"); len(got) != 1 {
					t.Errorf("ERROR lines after %s = %q, want still one", c.name, got)
				}
				hold.open()
			})
		})
	}
}
