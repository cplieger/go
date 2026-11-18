package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

func TestQuietAllEndsQueuedInterrupted(t *testing.T) {
	t.Run("running, yielded and queued jobs end and the timers wait for resume", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			y := &yielder{until: func(now time.Time) time.Time { return now.Add(time.Hour) }}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{y.spec("publish", "heavy"), spec("scan", "heavy")}})
			t0 := time.Now()
			r.schedule(t, "scan", "source:t", scheduler.Scheduled(time.Hour))
			woke := r.rec(t, r.nextWake(t, "scan", "source:t", t0, time.Hour).ID)

			pub := r.start(t, api("publish", "", "alice", payload{}))
			synctest.Wait()
			running := r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q1 := r.start(t, api("scan", "source:b", "alice", payload{}))
			q2 := r.start(t, api("scan", "source:c", "bob", payload{}))

			heldAt := time.Now()
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			if took := time.Since(heldAt); took != 0 {
				t.Errorf("Hold(all) took %v, want it to answer once the cancelled jobs returned at once", took)
			}
			if !y.returned() {
				t.Fatal("the yielded job's Yield had not answered when Hold(all) answered")
			}
			if _, yerr := y.answer(); !errors.Is(yerr, context.Canceled) {
				t.Errorf("Yield at Hold(all) = %v, want context.Canceled", yerr)
			}
			for name, id := range map[string]scheduler.ID{"yielded": pub.Job, "running": running.Job, "first queued": q1.Job, "second queued": q2.Job} {
				t.Run(name, func(t *testing.T) { r.wantEnd(t, id, scheduler.ResultInterrupted) })
			}
			if r.log.ran(q1.Job) || r.log.ran(q2.Job) {
				t.Error("a job queued at the hold ran, want none")
			}
			if _, err := r.ex.Start(t.Context(), api("scan", "source:d", "alice", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("Start during the hold = %v, want ErrQueueFull", err)
			}
			ends, err := r.store.LastEnds(t.Context())
			if err != nil {
				t.Fatalf("LastEnds = %v", err)
			}
			if got := ends[scheduler.ScheduledScope{Kind: "scan", Scope: "source:a"}]; !got.At.IsZero() {
				t.Errorf("LastEnds[source:a] after the hold = %+v, want none: an interrupted end does not move", got)
			}

			_, hi := jitterBand(time.Hour)
			sleepUntil(woke.EndedAt.Add(hi + 4*time.Minute))
			if n := len(r.wakes("scan", "source:t")); n != 1 {
				t.Errorf("%d wakes by the end of the hold, want 1: the timers wait for resume", n)
			}
			resumedAt := time.Now()
			resume()
			synctest.Wait()
			wakes := r.wakes("scan", "source:t")
			if len(wakes) != 2 || !wakes[1].QueuedAt.Equal(resumedAt) {
				t.Errorf("wakes after resume = %d, want 2, the second at resume since its wake was due during the hold", len(wakes))
			}
			r.start(t, api("scan", "source:d", "alice", payload{}))
		})
	})

	t.Run("held proposals are dropped", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			first := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			if _, ok := r.ex.Proposal("alice", p.id(first.Job)); ok {
				t.Error("Proposal after Hold(all) = held, want dropped")
			}
			resume()
			again := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			if again.Joined || again.Job == first.Job || !p.runLog.ran(again.Job) {
				t.Errorf("Start of the dropped proposal's scope after resume = %+v, want a new job that ran", again)
			}
		})
	})

	t.Run("resume counts each wait from the scope's last end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			t0 := time.Now()
			r.schedule(t, "scan", "source:t", scheduler.Scheduled(time.Hour))
			woke := r.rec(t, r.nextWake(t, "scan", "source:t", t0, time.Hour).ID)
			sleepUntil(woke.EndedAt.Add(5 * time.Minute))
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			sleepUntil(woke.EndedAt.Add(20 * time.Minute))
			resume()
			r.nextWake(t, "scan", "source:t", woke.EndedAt, time.Hour)
		})
	})

	t.Run("a job that does not return is waited for 10 seconds", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			stuck := spec("stuck", "")
			stuck.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
				<-release
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{stuck}})
			t.Cleanup(func() { close(release) })
			r.start(t, api("stuck", "", "alice", payload{}))
			synctest.Wait()
			heldAt := time.Now()
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			if took := time.Since(heldAt); took != 10*time.Second {
				t.Errorf("Hold(all) over a job ignoring its ctx took %v, want 10s", took)
			}
			resume()
		})
	})

	t.Run("without all nothing changes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, api("scan", "source:a", "alice", payload{Gate: gate}))
			synctest.Wait()
			queued := r.start(t, api("scan", "source:b", "alice", payload{}))
			resume, err := r.ex.Hold(t.Context(), false)
			if err != nil {
				t.Fatalf("Hold(writes) = %v, want nil", err)
			}
			synctest.Wait()
			if rec := r.rec(t, running.Job); rec.State != scheduler.StateRunning {
				t.Errorf("running job after Hold(writes) is %s, want running", rec.State)
			}
			if rec := r.rec(t, queued.Job); rec.State != scheduler.StateQueued {
				t.Errorf("queued job after Hold(writes) is %s, want queued", rec.State)
			}
			r.start(t, api("scan", "source:c", "alice", payload{}))
			resume()
			close(gate)
			synctest.Wait()
			r.wantEnd(t, running.Job, scheduler.ResultOK)
			r.wantEnd(t, queued.Job, scheduler.ResultOK)
		})
	})
}

func TestShutdownCancelsAndWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &rig{store: memrecords.New(), log: &runLog{}}
		returned := make(chan time.Time, 1)
		scan := spec("scan", "heavy")
		scan.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
			base.log.add(run.Job())
			<-ctx.Done()
			time.Sleep(2 * time.Second)
			returned <- time.Now()
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r, stop := base.openUntil(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{scan}})
		running := r.start(t, api("scan", "source:a", "alice", payload{}))
		synctest.Wait()
		queued := r.start(t, api("scan", "source:b", "alice", payload{}))

		stoppedAt := time.Now()
		stop()
		if took := time.Since(stoppedAt); took != 2*time.Second {
			t.Errorf("Run returned %v after its ctx ended, want 2s: once its running job returned", took)
		}
		select {
		case at := <-returned:
			if !at.Equal(stoppedAt.Add(2 * time.Second)) {
				t.Errorf("the running job returned at %v, want %v", at, stoppedAt.Add(2*time.Second))
			}
		default:
			t.Fatal("the running job had not returned when Run returned")
		}
		if r.log.ran(queued.Job) {
			t.Error("the queued job started after shutdown, want never")
		}
		for name, id := range map[string]scheduler.ID{"running": running.Job, "queued": queued.Job} {
			if rec := r.rec(t, id); rec.End != nil && rec.End.Result != scheduler.ResultInterrupted {
				t.Errorf("the %s job cut short by shutdown ended %s, want it left for the next open or interrupted", name, rec.End.Result)
			}
		}

		reopened := base.store.Crash()
		reopened.Reopen()
		for name, id := range map[string]scheduler.ID{"running": running.Job, "queued": queued.Job} {
			rec, ok, err := reopened.Get(t.Context(), id)
			if err != nil || !ok || rec.End == nil || rec.End.Result != scheduler.ResultInterrupted {
				t.Errorf("the %s job after the next open = %+v (%t, %v), want ended interrupted", name, rec.End, ok, err)
			}
		}
		ends, err := reopened.LastEnds(t.Context())
		if err != nil {
			t.Fatalf("LastEnds = %v", err)
		}
		if len(ends) != 0 {
			t.Errorf("LastEnds after a shutdown = %v, want none: no job ended that started and finished", ends)
		}
	})
}
