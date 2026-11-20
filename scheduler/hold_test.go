package scheduler_test

import (
	"context"
	"errors"
	"sync"
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
				if rec := r.rec(t, id); rec.End == nil || rec.End.Result != scheduler.ResultInterrupted || len(rec.End.Reasons) != 0 {
					t.Errorf("the %s job after Hold(all) is %s with end %+v, want ended interrupted with no reason", name, rec.State, rec.End)
				}
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

	t.Run("a job the hold stopped holds no proposal made after it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			late := r.start(t, api("align", "file:2/expect:3", "alice", payload{N: proposeLate, Gate: make(chan struct{})}))
			synctest.Wait()
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			if errs := p.errsOf(late.Job); len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
				t.Errorf("Propose after the hold's cancel = %v, want [context.Canceled]", errs)
			}
			if id := p.id(late.Job); id != "" {
				t.Errorf("Propose after the hold's cancel answered id %q, want none", id)
			}
			r.wantEnd(t, late.Job, scheduler.ResultInterrupted)
			resume()
			again := r.start(t, api("align", "file:2/expect:3", "alice", payload{}))
			synctest.Wait()
			if again.Joined || !p.runLog.ran(again.Job) {
				t.Errorf("Start of the scope after resume = %+v, want a new job that ran", again)
			}
		})
	})

	t.Run("resume keeps each Fresh window of the last ends it reads", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			refresh := spec("refresh", "")
			refresh.Fresh = forgeFresh
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}})
			done := r.start(t, api("refresh", "", "alice", payload{}))
			synctest.Wait()
			ended := r.rec(t, done.Job).EndedAt
			time.Sleep(5 * time.Second)
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			time.Sleep(5 * time.Second)
			resume()
			_, err = r.ex.Start(t.Context(), api("refresh", "", "bob", payload{}))
			var recent *scheduler.RecentError
			if !errors.As(err, &recent) || !recent.Until.Equal(ended.Add(60*time.Second)) {
				t.Errorf("Start 10 s after an end, across a hold = %v, want *RecentError until the end plus 60 s", err)
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

	t.Run("resume keeps a cadence change made since the last end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{slowWakes("quick", "passes")}})
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			ran := r.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			sleepUntil(r.rec(t, ran.Job).EndedAt.Add(4 * time.Hour))
			changed := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(15*time.Minute))
			sleepUntil(changed.Add(time.Minute))
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			resume()
			synctest.Wait()
			if n := len(r.wakes("quick", "")); n != 0 {
				t.Errorf("%d timer records at resume, want 0: the wait counts from the change", n)
			}
			sleepUntil(changed.Add(10 * time.Minute))
			if got, want := r.ex.Deadline("quick", ""), changed.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after resume = %v, want %v (the change plus three 15-minute intervals)", got, want)
			}
			if !r.ex.Fresh("quick", "").Healthy() {
				t.Error("Fresh unhealthy after resume inside the new cadence's lease, want healthy")
			}
			r.nextWake(t, "quick", "", changed, 15*time.Minute)
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

	t.Run("a job in its commit step keeps its own end and is waited for", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			publish := spec("publish", "")
			publish.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
				if !run.Commit() {
					return scheduler.End{Result: scheduler.ResultFailed}
				}
				time.Sleep(3 * time.Second)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{publish}})
			s := r.start(t, api("publish", "", "alice", payload{}))
			synctest.Wait()
			heldAt := time.Now()
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			if took := time.Since(heldAt); took != 3*time.Second {
				t.Errorf("Hold(all) over a job in its commit step took %v, want 3s: once it returned", took)
			}
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			resume()
		})
	})

	t.Run("an ended context holds nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, api("scan", "source:a", "alice", payload{Gate: gate}))
			synctest.Wait()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			resume, err := r.ex.Hold(ctx, true)
			if !errors.Is(err, context.Canceled) || resume != nil {
				t.Errorf("Hold(all) with an ended context = (resume set %t, %v), want (false, context.Canceled)", resume != nil, err)
			}
			synctest.Wait()
			if rec := r.rec(t, running.Job); rec.State != scheduler.StateRunning {
				t.Errorf("running job after a refused Hold(all) is %s, want running", rec.State)
			}
			r.start(t, api("scan", "source:b", "alice", payload{}))
			close(gate)
		})
	})

	t.Run("a context ending during the wait releases the hold", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			stuck := spec("stuck", "")
			stuck.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
				<-release
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{stuck, spec("scan", "")}})
			t.Cleanup(func() { close(release) })
			t0 := time.Now()
			r.schedule(t, "scan", "source:t", scheduler.Scheduled(time.Hour))
			woke := r.rec(t, r.nextWake(t, "scan", "source:t", t0, time.Hour).ID)
			r.start(t, api("stuck", "", "alice", payload{}))
			synctest.Wait()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			heldAt := time.Now()
			resume, err := r.ex.Hold(ctx, true)
			if !errors.Is(err, context.DeadlineExceeded) || resume != nil {
				t.Errorf("Hold(all) whose context ended in the wait = (resume set %t, %v), want (false, DeadlineExceeded)", resume != nil, err)
			}
			if took := time.Since(heldAt); took != 2*time.Second {
				t.Errorf("Hold(all) answered after %v, want 2s, at its context's end", took)
			}
			r.start(t, api("scan", "source:b", "alice", payload{}))
			r.nextWake(t, "scan", "source:t", woke.EndedAt, time.Hour)
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
	for _, answered := range []scheduler.Result{scheduler.ResultOK, scheduler.ResultFailed} {
		t.Run("a job that answers "+string(answered)+" when cut short", func(t *testing.T) {
			shutdownCancelsAndWaits(t, answered)
		})
	}
}

// shutdownCancelsAndWaits cuts a running job short at shutdown, whose Run then
// answers answered, beside a queued job.
func shutdownCancelsAndWaits(t *testing.T, answered scheduler.Result) {
	synctest.Test(t, func(t *testing.T) {
		base := &rig{store: memrecords.New(), log: &runLog{}}
		returned := make(chan time.Time, 1)
		scan := spec("scan", "heavy")
		scan.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
			base.log.add(run.Job())
			<-ctx.Done()
			time.Sleep(2 * time.Second)
			returned <- time.Now()
			return scheduler.End{Result: answered}
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

// A job in its commit step at shutdown keeps its own end, and Run waits for it.
func TestShutdownKeepsACommittingJobsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &rig{store: memrecords.New(), log: &runLog{}}
		gate := make(chan struct{})
		full := spec("full", "passes")
		full.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
			if !run.Commit() {
				return scheduler.End{Result: scheduler.ResultFailed}
			}
			<-gate
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r, stop := base.openUntil(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
		s := r.start(t, api("full", "", "alice", payload{}))
		synctest.Wait()

		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			stop()
		}()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("Run returned while a job was in its commit step, want it to wait")
		default:
		}
		close(gate)
		<-stopped
		r.wantEnd(t, s.Job, scheduler.ResultOK)
		ends, err := base.store.LastEnds(t.Context())
		if err != nil {
			t.Fatalf("LastEnds = %v", err)
		}
		if got := ends[scheduler.ScheduledScope{Kind: "full", Scope: ""}]; got.Result != scheduler.ResultOK {
			t.Errorf("LastEnds[full] after a shutdown that waited out its commit = %+v, want its ok end", got)
		}
	})
}

// pausedEnds is a store whose next LastEnds after pause reads the ends, then
// waits for the channel pause returned to close before answering them, so a
// job can end while a read is in flight.
type pausedEnds struct {
	*memrecords.Store
	release chan struct{}
	mu      sync.Mutex
}

func (p *pausedEnds) pause() chan<- struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release = make(chan struct{})
	return p.release
}

func (p *pausedEnds) LastEnds(ctx context.Context) (map[scheduler.ScheduledScope]scheduler.LastEnd, error) {
	ends, err := p.Store.LastEnds(ctx)
	p.mu.Lock()
	release := p.release
	p.release = nil
	p.mu.Unlock()
	if release != nil {
		<-release
	}
	return ends, err
}

// endDuringResumeRead runs a refresh job ended ok 30 minutes before a second
// one that is in its commit step through a Hold(all), then resumes with a
// LastEnds read that answers the ends held before the second job ended, and
// answers that job's stored end.
func endDuringResumeRead(t *testing.T) (*rig, time.Time) {
	t.Helper()
	committed := make(chan struct{})
	refresh := spec("refresh", "")
	refresh.Fresh = forgeFresh
	refresh.Run = func(_ context.Context, run *scheduler.Run[payload]) scheduler.End {
		if gate := run.Job().Payload.Gate; gate != nil {
			if !run.Commit() {
				return scheduler.End{Result: scheduler.ResultFailed}
			}
			<-gate
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	base := &rig{store: memrecords.New(), log: &runLog{}}
	store := &pausedEnds{Store: base.store}
	r := base.open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}, Records: store})
	synctest.Wait()
	r.schedule(t, "refresh", "", scheduler.Scheduled(time.Hour))
	r.start(t, api("refresh", "", "alice", payload{}))
	synctest.Wait()
	time.Sleep(30 * time.Minute)
	committing := r.start(t, api("refresh", "", "alice", payload{Gate: committed}))
	synctest.Wait()
	resume, err := r.ex.Hold(t.Context(), true)
	if err != nil {
		t.Fatalf("Hold(all) = %v, want nil", err)
	}

	release := store.pause()
	resumed := make(chan struct{})
	go func() {
		defer close(resumed)
		resume()
	}()
	synctest.Wait()
	close(committed)
	synctest.Wait()
	ended := r.rec(t, committing.Job).EndedAt
	if ended.IsZero() {
		t.Fatal("the committing job did not end during the resume's read")
	}
	close(release)
	<-resumed
	synctest.Wait()
	return r, ended
}

func TestResumeKeepsEndStoredDuringRead(t *testing.T) {
	t.Run("a Start inside the end's Fresh window is refused", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r, ended := endDuringResumeRead(t)
			_, err := r.ex.Start(t.Context(), api("refresh", "", "bob", payload{}))
			var recent *scheduler.RecentError
			if !errors.As(err, &recent) || !recent.Until.Equal(ended.Add(60*time.Second)) {
				t.Errorf("Start just after an end stored during the resume's read = %v, want *RecentError until the end plus 60 s", err)
			}
		})
	})

	t.Run("the deadline and the next wake count from the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r, ended := endDuringResumeRead(t)
			if got, want := r.ex.Deadline("refresh", ""), ended.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after the resume = %v, want %v: the end stored during the read plus the lease", got, want)
			}
			r.nextWake(t, "refresh", "", ended, time.Hour)
		})
	})
}

// A hold and a shutdown answer only once every record write in flight when
// they came has landed, and write nothing after.
func TestQuietWaitsForRecordWrites(t *testing.T) {
	t.Run("a hold ends a record being written interrupted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r := gatedRig(t, 0)
			g := s.gate(memrecords.OpInsert)
			started := startAsync(t.Context(), r.ex, api("scan", "source:a", "alice", payload{}))
			<-g.entered
			h := s.quietWhile(t, g, holdAsync(t.Context(), r.ex), "Hold(all)")
			if h.err != nil {
				t.Fatalf("Hold(all) = %v, want nil", h.err)
			}
			a := <-started
			if a.err != nil || a.s.Job == "" || a.s.Joined {
				t.Fatalf("Start whose record a hold overtook = (%+v, %v), want its own job", a.s, a.err)
			}
			r.wantEnd(t, a.s.Job, scheduler.ResultInterrupted)
			s.wantNoWrite(t, "Hold(all)")
			if r.log.ran(a.s.Job) {
				t.Error("the job whose record a hold overtook ran, want never")
			}
		})
	})

	t.Run("a hold waits for a refused request's record", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r := gatedRig(t, 1)
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gate(memrecords.OpInsert)
			refused := startAsync(t.Context(), r.ex, api("scan", "source:c", "bob", payload{}))
			<-g.entered
			if h := s.quietWhile(t, g, holdAsync(t.Context(), r.ex), "Hold(all)"); h.err != nil {
				t.Fatalf("Hold(all) = %v, want nil", h.err)
			}
			if a := <-refused; !errors.Is(a.err, scheduler.ErrQueueFull) {
				t.Errorf("Start past MaxQueued = (%+v, %v), want ErrQueueFull", a.s, a.err)
			}
			s.wantNoWrite(t, "Hold(all)")
		})
	})

	t.Run("a hold waits for a cancelled job's end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r := gatedRig(t, 0)
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q := r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gate(memrecords.OpEnd)
			go func() { _, _ = r.ex.Cancel(t.Context(), q.Job) }()
			<-g.entered
			if h := s.quietWhile(t, g, holdAsync(t.Context(), r.ex), "Hold(all)"); h.err != nil {
				t.Fatalf("Hold(all) = %v, want nil", h.err)
			}
			r.wantEnd(t, q.Job, scheduler.ResultCancelled)
			s.wantNoWrite(t, "Hold(all)")
		})
	})

	t.Run("a shutdown ends a record being written interrupted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newGatedStore()
			r, stop := (&rig{store: s.Store, log: &runLog{}}).openUntil(t, scheduler.Config[payload]{
				Kinds:   []scheduler.KindSpec[payload]{spec("scan", "heavy")},
				Records: s,
			})
			g := s.gate(memrecords.OpInsert)
			started := startAsync(t.Context(), r.ex, api("scan", "source:a", "alice", payload{}))
			<-g.entered
			s.quietWhile(t, g, stopAsync(stop), "Run")
			a := <-started
			if a.err != nil || a.s.Job == "" || a.s.Joined {
				t.Fatalf("Start whose record a shutdown overtook = (%+v, %v), want its own job", a.s, a.err)
			}
			r.wantEnd(t, a.s.Job, scheduler.ResultInterrupted)
			s.wantNoWrite(t, "Run")
		})
	})

	t.Run("a shutdown waits for a cancelled job's end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newGatedStore()
			r, stop := (&rig{store: s.Store, log: &runLog{}}).openUntil(t, scheduler.Config[payload]{
				Kinds:   []scheduler.KindSpec[payload]{spec("scan", "heavy")},
				Records: s,
			})
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q := r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gate(memrecords.OpEnd)
			go func() { _, _ = r.ex.Cancel(t.Context(), q.Job) }()
			<-g.entered
			s.quietWhile(t, g, stopAsync(stop), "Run")
			r.wantEnd(t, q.Job, scheduler.ResultCancelled)
			s.wantNoWrite(t, "Run")
		})
	})

	t.Run("a shutdown waits for a hold's queued-job end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newGatedStore()
			r, stop := (&rig{store: s.Store, log: &runLog{}}).openUntil(t, scheduler.Config[payload]{
				Kinds:   []scheduler.KindSpec[payload]{spec("scan", "heavy")},
				Records: s,
			})
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q := r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gateEnd(q.Job)
			_ = holdAsync(t.Context(), r.ex)
			<-g.entered
			s.quietWhile(t, g, stopAsync(stop), "Run")
			r.wantEnd(t, q.Job, scheduler.ResultInterrupted)
			s.wantNoWrite(t, "Run")
		})
	})

	t.Run("a hold waits for a shutdown's queued-job end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newGatedStore()
			r, stop := (&rig{store: s.Store, log: &runLog{}}).openUntil(t, scheduler.Config[payload]{
				Kinds:   []scheduler.KindSpec[payload]{spec("scan", "heavy")},
				Records: s,
			})
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q := r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gateEnd(q.Job)
			_ = stopAsync(stop)
			<-g.entered
			if h := s.quietWhile(t, g, holdAsync(t.Context(), r.ex), "Hold(all)"); h.err != nil {
				t.Fatalf("Hold(all) = %v, want nil", h.err)
			}
			r.wantEnd(t, q.Job, scheduler.ResultInterrupted)
			s.wantNoWrite(t, "Hold(all)")
		})
	})

	t.Run("a second hold waits for the first hold's queued-job end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, r := gatedRig(t, 0)
			r.start(t, api("scan", "source:a", "alice", payload{Gate: make(chan struct{})}))
			synctest.Wait()
			q := r.start(t, api("scan", "source:b", "alice", payload{}))
			g := s.gateEnd(q.Job)
			_ = holdAsync(t.Context(), r.ex)
			<-g.entered
			if h := s.quietWhile(t, g, holdAsync(t.Context(), r.ex), "second Hold(all)"); h.err != nil {
				t.Fatalf("second Hold(all) = %v, want nil", h.err)
			}
			r.wantEnd(t, q.Job, scheduler.ResultInterrupted)
			s.wantNoWrite(t, "second Hold(all)")
		})
	})
}

// gatedStore holds the next call of each gated op, or of a gated job's End,
// until its gate's release closes.
type gatedStore struct {
	*memrecords.Store
	gates  map[memrecords.Op]*storeGate
	writes int // the record writes counted when quietWhile's call answered
	mu     sync.Mutex
}

type storeGate struct {
	entered, release chan struct{}
	id               scheduler.ID // the only job whose call it holds, when set
}

func newGatedStore() *gatedStore {
	return &gatedStore{Store: memrecords.New(), gates: map[memrecords.Op]*storeGate{}}
}

// gatedRig runs an executor of kind "scan" on slot "heavy" over a gated store
// until the test ends; maxQueued 0 keeps the default bound.
func gatedRig(t *testing.T, maxQueued int) (*gatedStore, *rig) {
	t.Helper()
	s := newGatedStore()
	r := (&rig{store: s.Store, log: &runLog{}}).open(t, scheduler.Config[payload]{
		Kinds:     []scheduler.KindSpec[payload]{spec("scan", "heavy")},
		Records:   s,
		MaxQueued: maxQueued,
	})
	return s, r
}

func (s *gatedStore) gate(op memrecords.Op) *storeGate { return s.gateFor(op, "") }

func (s *gatedStore) gateEnd(id scheduler.ID) *storeGate { return s.gateFor(memrecords.OpEnd, id) }

func (s *gatedStore) gateFor(op memrecords.Op, id scheduler.ID) *storeGate {
	g := &storeGate{entered: make(chan struct{}), release: make(chan struct{}), id: id}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gates[op] = g
	return g
}

func (s *gatedStore) pass(op memrecords.Op, id scheduler.ID) {
	s.mu.Lock()
	g, ok := s.gates[op]
	if ok && (g.id == "" || g.id == id) {
		delete(s.gates, op)
	} else {
		g = nil
	}
	s.mu.Unlock()
	if g != nil {
		close(g.entered)
		<-g.release
	}
}

func (s *gatedStore) Insert(ctx context.Context, rec scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) {
	s.pass(memrecords.OpInsert, rec.ID)
	return s.Store.Insert(ctx, rec, key)
}

func (s *gatedStore) End(ctx context.Context, id scheduler.ID, end scheduler.End) (scheduler.Record, error) {
	s.pass(memrecords.OpEnd, id)
	return s.Store.End(ctx, id, end)
}

func (s *gatedStore) count() int {
	return s.Count(memrecords.OpInsert) + s.Count(memrecords.OpPut) + s.Count(memrecords.OpEnd)
}

// quietWhile wants call to wait while g holds its write, releases g and
// answers what call answered, counting the writes made by then.
func (s *gatedStore) quietWhile(t *testing.T, g *storeGate, call <-chan held, name string) held {
	t.Helper()
	synctest.Wait()
	var h held
	select {
	case h = <-call:
		t.Errorf("%s answered while a record write was held, want it to wait for the write", name)
		s.writes = s.count()
		close(g.release)
		return h
	default:
	}
	close(g.release)
	h = <-call
	s.writes = s.count()
	return h
}

// wantNoWrite wants no record write since quietWhile's call answered.
func (s *gatedStore) wantNoWrite(t *testing.T, name string) {
	t.Helper()
	synctest.Wait()
	if n := s.count() - s.writes; n != 0 {
		t.Errorf("%d record writes after %s answered, want none", n, name)
	}
}

type held struct {
	resume func()
	err    error
}

// holdAsync calls Hold(ctx, true) on its own goroutine and answers what it
// answered.
func holdAsync(ctx context.Context, ex *scheduler.Executor[payload]) <-chan held {
	out := make(chan held, 1)
	go func() {
		resume, err := ex.Hold(ctx, true)
		out <- held{resume: resume, err: err}
	}()
	return out
}

// stopAsync calls stop on its own goroutine and answers once it returned.
func stopAsync(stop func()) <-chan held {
	out := make(chan held, 1)
	go func() {
		stop()
		out <- held{}
	}()
	return out
}
