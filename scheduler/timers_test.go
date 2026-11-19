package scheduler_test

import (
	"context"
	"errors"
	"fmt"
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

// scripted is a Run that logs each job in log and ends the jobs it runs with
// results in order, then ok.
func scripted(log *runLog, results ...scheduler.Result) func(context.Context, *scheduler.Run[payload]) scheduler.End {
	var mu sync.Mutex
	return func(_ context.Context, r *scheduler.Run[payload]) scheduler.End {
		log.add(r.Job())
		mu.Lock()
		defer mu.Unlock()
		if len(results) == 0 {
			return scheduler.End{Result: scheduler.ResultOK}
		}
		res := results[0]
		results = results[1:]
		return scheduler.End{Result: res}
	}
}

func TestCadenceChangeDoesNotFire(t *testing.T) {
	t.Run("a change restarts the wait from the change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			t0 := time.Now()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
			sleepUntil(t0.Add(40 * time.Minute))
			changed := time.Now()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(2*time.Hour))
			synctest.Wait()
			if n := len(r.wakes("scan", "source:a")); n != 0 {
				t.Errorf("%d timer records at a cadence change, want 0: a change fires nothing", n)
			}
			r.nextWake(t, "scan", "source:a", changed, 2*time.Hour)
		})
	})

	t.Run("a shorter cadence wakes on its own interval from the change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}})
			t0 := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			sleepUntil(t0.Add(4 * time.Hour))
			if n := len(r.wakes("quick", "")); n != 0 {
				t.Fatalf("%d wakes in the first 4 hours of a 6-hour cadence, want 0", n)
			}
			changed := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(15*time.Minute))
			first := r.nextWake(t, "quick", "", changed, 15*time.Minute)
			r.nextWake(t, "quick", "", r.rec(t, first.ID).EndedAt, 15*time.Minute)
		})
	})

	t.Run("the deadline counts from the later of the last end and the change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}})
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			ran := r.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := r.rec(t, ran.Job).EndedAt
			if got, want := r.ex.Deadline("quick", ""), lastEnd.Add(18*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after an end = %v, want %v (the end plus three 6-hour intervals)", got, want)
			}
			sleepUntil(lastEnd.Add(4 * time.Hour))
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			if got, want := r.ex.Deadline("quick", ""), lastEnd.Add(18*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after scheduling the same cadence = %v, want it unmoved at %v", got, want)
			}
			changed := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(15*time.Minute))
			if got, want := r.ex.Deadline("quick", ""), changed.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a change to 15 minutes = %v, want %v (the change plus three 15-minute intervals), never already passed", got, want)
			}
			if !r.ex.Fresh("quick", "").Healthy() {
				t.Error("Fresh unhealthy right after a change to a shorter cadence, want healthy")
			}
			woke := r.nextWake(t, "quick", "", changed, 15*time.Minute)
			if got, want := r.ex.Deadline("quick", ""), r.rec(t, woke.ID).EndedAt.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after an end past the change = %v, want %v (that end plus the lease)", got, want)
			}
		})
	})

	t.Run("a change from trigger-only counts from the change, not the last end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{slowWakes("quick", "passes")}})
			r.schedule(t, "quick", "", scheduler.TriggerOnly())
			ran := r.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := r.rec(t, ran.Job).EndedAt
			sleepUntil(lastEnd.Add(4 * time.Hour))
			changed := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(15*time.Minute))
			synctest.Wait()
			if got, want := r.ex.Deadline("quick", ""), changed.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a change from trigger-only = %v, want %v (the change plus three 15-minute intervals)", got, want)
			}
			if !r.ex.Fresh("quick", "").Healthy() {
				t.Error("Fresh unhealthy right after a change from trigger-only, want healthy")
			}
			if n := len(r.wakes("quick", "")); n != 0 {
				t.Errorf("%d timer records at a change from trigger-only, want 0: a change fires nothing", n)
			}
			r.nextWake(t, "quick", "", changed, 15*time.Minute)
		})
	})

	t.Run("a change back to trigger-only stops the wakes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}})
			t0 := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(15*time.Minute))
			first := r.nextWake(t, "quick", "", t0, 15*time.Minute)
			synctest.Wait()
			r.schedule(t, "quick", "", scheduler.TriggerOnly())
			n := len(r.wakes("quick", ""))
			sleepUntil(r.rec(t, first.ID).EndedAt.Add(2 * time.Hour))
			if got := len(r.wakes("quick", "")); got != n {
				t.Errorf("%d wakes in the 2 hours after a change to trigger-only, want 0", got-n)
			}
			if got := r.ex.Deadline("quick", ""); !got.IsZero() {
				t.Errorf("Deadline after a change to trigger-only = %v, want zero", got)
			}
		})
	})

	t.Run("scheduling the same cadence keeps the wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}})
			t0 := time.Now()
			every := scheduler.Scheduled(time.Hour)
			r.schedule(t, "quick", "", every)
			for i := 1; i <= 5; i++ {
				sleepUntil(t0.Add(time.Duration(i) * 10 * time.Minute))
				r.schedule(t, "quick", "", every)
			}
			if got, want := r.ex.Deadline("quick", ""), t0.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after five saves of the same cadence = %v, want it unmoved at %v", got, want)
			}
			r.nextWake(t, "quick", "", t0, time.Hour)
		})
	})
}

func TestRetryWakesSooner(t *testing.T) {
	daily := scheduler.Scheduled(24 * time.Hour).Retry(15 * time.Minute)

	t.Run("after each end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			log := &runLog{}
			full := spec("full", "passes")
			full.Run = scripted(log, scheduler.ResultFailed, scheduler.ResultSkipped, scheduler.ResultPartial)
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
			t0 := time.Now()
			r.schedule(t, "full", "", daily)

			failed := r.rec(t, r.nextWake(t, "full", "", t0, 24*time.Hour).ID)
			r.wantEnd(t, failed.ID, scheduler.ResultFailed)
			if got, want := r.ex.Deadline("full", ""), failed.EndedAt.Add(72*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a failed end = %v, want %v: the deadline counts from the failed end", got, want)
			}
			skipped := r.rec(t, r.nextWake(t, "full", "", failed.EndedAt, 15*time.Minute).ID)
			r.wantEnd(t, skipped.ID, scheduler.ResultSkipped)
			partial := r.rec(t, r.nextWake(t, "full", "", skipped.EndedAt, 15*time.Minute).ID)
			r.wantEnd(t, partial.ID, scheduler.ResultPartial)
			ok := r.rec(t, r.nextWake(t, "full", "", partial.EndedAt, 24*time.Hour).ID)
			r.wantEnd(t, ok.ID, scheduler.ResultOK)
			r.nextWake(t, "full", "", ok.EndedAt, 24*time.Hour)
		})
	})

	for _, c := range []struct {
		name   string
		result scheduler.Result
		next   time.Duration
	}{
		{"at a start after a failed end", scheduler.ResultFailed, 15 * time.Minute},
		{"at a start after a skipped end", scheduler.ResultSkipped, 15 * time.Minute},
		{"at a start after an ok end", scheduler.ResultOK, 24 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &rig{store: memrecords.New(), log: &runLog{}}
				full := spec("full", "passes")
				full.Run = scripted(base.log, c.result)
				cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}}
				before, stop := base.openUntil(t, cfg)
				ran := before.start(t, api("full", "", "alice", payload{}))
				synctest.Wait()
				lastEnd := before.rec(t, ran.Job).EndedAt
				sleepUntil(lastEnd.Add(5 * time.Minute))
				stop()

				full.Run = scripted(base.log)
				r := base.open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
				r.schedule(t, "full", "", daily)
				r.nextWake(t, "full", "", lastEnd, c.next)
			})
		})
	}
}

// refusal is a timer wake the full queue refused: its record, the queued
// job holding the queue's one place, whose cancel frees it, and full's last
// end and deadline before the wake.
type refusal struct {
	lastEnd, deadline time.Time
	r                 *rig
	queued            scheduler.ID
	refused           scheduler.Record
}

// refusedWake fills the queue before the 24-hour timer of full wakes 24
// hours after a 03:00 full check, and answers the wake it refuses.
func refusedWake(t *testing.T, c scheduler.Cadence) refusal {
	t.Helper()
	r := newRig(t, scheduler.Config[payload]{
		MaxQueued:         1,
		MaxQueuedPerOwner: 1,
		Kinds:             []scheduler.KindSpec[payload]{spec("full", "passes"), spec("quick", "passes"), spec("hold", "passes")},
	})
	day := time.Now()
	r.schedule(t, "full", "", c)
	sleepUntil(day.Add(3 * time.Hour))
	last := r.start(t, api("full", "", "alice", payload{}))
	synctest.Wait()
	lastEnd := r.rec(t, last.Job).EndedAt
	deadline := r.ex.Deadline("full", "")

	sleepUntil(lastEnd.Add(21 * time.Hour))
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	r.start(t, api("hold", "", "bob", payload{Gate: gate}))
	synctest.Wait()
	queued := r.start(t, api("quick", "", "bob", payload{})).Job

	refused := r.nextWake(t, "full", "", lastEnd, 24*time.Hour)
	r.wantEnd(t, refused.ID, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonQueueFull})
	return refusal{r: r, refused: refused, queued: queued, lastEnd: lastEnd, deadline: deadline}
}

// wantUnmoved fails the test unless full's deadline is deadline and its last
// moving end is still the ok end at lastEnd.
func wantUnmoved(t *testing.T, r *rig, lastEnd, deadline time.Time) {
	t.Helper()
	if got := r.ex.Deadline("full", ""); !got.Equal(deadline) {
		t.Errorf("Deadline after a refused wake = %v, want it unmoved at %v", got, deadline)
	}
	ends, err := r.store.LastEnds(t.Context())
	if err != nil {
		t.Fatalf("LastEnds = %v", err)
	}
	if got := ends[scheduler.ScheduledScope{Kind: "full"}]; !got.At.Equal(lastEnd) || got.Result != scheduler.ResultOK {
		t.Errorf("LastEnds[full] after a refused wake = %+v, want the ok end at %v", got, lastEnd)
	}
}

func TestTimerRefusedAtAdmissionWakesAfterRetry(t *testing.T) {
	t.Run("with Retry the next wake is the refusal plus d", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := refusedWake(t, scheduler.Scheduled(24*time.Hour).Retry(15*time.Minute))
			r, refused := f.r, f.refused
			sleepUntil(refused.QueuedAt.Add(6 * time.Second))
			if n := len(r.wakes("full", "")); n != 1 {
				t.Errorf("%d timer records 6 s after the refusal, want 1: queue_full's Retry-After never sets the timer", n)
			}
			wantUnmoved(t, r, f.lastEnd, f.deadline)
			if _, err := r.ex.Cancel(t.Context(), f.queued); err != nil {
				t.Fatalf("Cancel(the queued job) = %v", err)
			}
			next := r.nextWake(t, "full", "", refused.QueuedAt, 15*time.Minute)
			if rec := r.rec(t, next.ID); rec.State != scheduler.StateQueued {
				t.Errorf("the wake after the refusal is %s, want queued behind the slot's job", rec.State)
			}
			skipped := 0
			for _, rec := range r.wakes("full", "") {
				if rec.End != nil && rec.End.Result == scheduler.ResultSkipped {
					skipped++
				}
			}
			if skipped != 1 {
				t.Errorf("%d skipped timer records, want 1: the refused wake's", skipped)
			}
			wantUnmoved(t, r, f.lastEnd, f.deadline)
		})
	})

	t.Run("without Retry the next wake is the refusal plus the interval", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := refusedWake(t, scheduler.Scheduled(24*time.Hour))
			if _, err := f.r.ex.Cancel(t.Context(), f.queued); err != nil {
				t.Fatalf("Cancel(the queued job) = %v", err)
			}
			f.r.nextWake(t, "full", "", f.refused.QueuedAt, 24*time.Hour)
			wantUnmoved(t, f.r, f.lastEnd, f.deadline)
		})
	})
}

// refusedInserts is a store that keeps the instant of each Insert it
// refuses, which a test stepping the clock a second at a time cannot see.
type refusedInserts struct {
	*memrecords.Store
	at []time.Time
	mu sync.Mutex
}

func (s *refusedInserts) Insert(ctx context.Context, r scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) {
	got, replayed, err := s.Store.Insert(ctx, r, key)
	if err != nil {
		s.mu.Lock()
		s.at = append(s.at, time.Now())
		s.mu.Unlock()
	}
	return got, replayed, err
}

func (s *refusedInserts) instants() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.at)
}

func TestTimerStoreUnavailableWakesAfterRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &rig{store: memrecords.New(), log: &runLog{}}
		ins := &refusedInserts{Store: base.store}
		r := base.open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("full", "passes")}, Records: ins})
		t0 := time.Now()
		r.schedule(t, "full", "", scheduler.Scheduled(24*time.Hour).Retry(15*time.Minute))
		deadline := r.ex.Deadline("full", "")
		if want := t0.Add(72 * time.Hour); !deadline.Equal(want) {
			t.Errorf("Deadline before any end = %v, want %v", deadline, want)
		}
		r.store.Fail(memrecords.OpInsert, errors.New("disk full"))

		_, hi := jitterBand(24 * time.Hour)
		for r.store.Count(memrecords.OpInsert) == 0 {
			if time.Since(t0) > hi {
				t.Fatalf("no wake within %v of the schedule", hi)
			}
			time.Sleep(time.Second)
			synctest.Wait()
		}
		refused := ins.instants()
		if len(refused) != 1 {
			t.Fatalf("%d refused inserts, want 1", len(refused))
		}
		refusedAt := refused[0]
		if off := refusedAt.Sub(t0); !inBand(off, 24*time.Hour) {
			t.Errorf("first wake %v after the schedule, want within the band of 24h", off)
		}
		if n := len(r.store.All()); n != 0 {
			t.Errorf("%d records after a failed insert, want 0", n)
		}
		r.store.Fail(memrecords.OpInsert, nil)
		if got := r.ex.Deadline("full", ""); !got.Equal(deadline) {
			t.Errorf("Deadline after a wake the store refused = %v, want it unmoved at %v", got, deadline)
		}

		rlo, _ := jitterBand(15 * time.Minute)
		sleepUntil(refusedAt.Add(rlo - time.Second))
		if n := r.store.Count(memrecords.OpInsert); n != 1 {
			t.Errorf("%d inserts before the retry's band, want 1: the refusal never re-fires at once", n)
		}
		next := r.nextWake(t, "full", "", refusedAt, 15*time.Minute)
		r.wantEnd(t, next.ID, scheduler.ResultOK)
	})
}

func TestRestartDoesNotRerunDaily(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &rig{store: memrecords.New(), log: &runLog{}}
		cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("daily", "")}}
		before, stop := base.openUntil(t, cfg)
		before.schedule(t, "daily", "", scheduler.Scheduled(24*time.Hour))
		time.Sleep(10 * time.Minute)
		ran := before.start(t, api("daily", "", "alice", payload{}))
		synctest.Wait()
		lastEnd := before.rec(t, ran.Job).EndedAt
		sleepUntil(lastEnd.Add(time.Hour))
		stop()

		r := base.open(t, cfg)
		synctest.Wait()
		r.schedule(t, "daily", "", scheduler.Scheduled(24*time.Hour))
		if got, want := r.ex.Deadline("daily", ""), lastEnd.Add(72*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline after a restart = %v, want %v: the last end plus the lease", got, want)
		}
		r.nextWake(t, "daily", "", lastEnd, 24*time.Hour)
	})
}

func TestRestartKeepsFreshWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		refresh := spec("refresh", "")
		refresh.Fresh = forgeFresh
		cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}}
		base := &rig{store: memrecords.New(), log: &runLog{}}
		before, stop := base.openUntil(t, cfg)
		ran := before.start(t, api("refresh", "", "alice", payload{}))
		synctest.Wait()
		ended := before.rec(t, ran.Job).EndedAt
		time.Sleep(time.Second)
		stop()

		r := base.open(t, cfg)
		synctest.Wait()
		sleepUntil(ended.Add(6 * time.Second))
		_, err := r.ex.Start(t.Context(), api("refresh", "", "bob", payload{}))
		var recent *scheduler.RecentError
		if !errors.As(err, &recent) || !recent.Until.Equal(ended.Add(60*time.Second)) {
			t.Fatalf("Start 6 s after a stored end, after a restart = %v, want *RecentError until the end plus 60 s", err)
		}
		sleepUntil(ended.Add(60 * time.Second))
		again := r.start(t, api("refresh", "", "bob", payload{}))
		synctest.Wait()
		if !r.log.ran(again.Job) {
			t.Errorf("Start at the end plus 60 s = %+v, want a job that ran", again)
		}
	})
}

func TestScheduleCountsFromTheGivenChange(t *testing.T) {
	cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}}

	t.Run("a first schedule counts from a change after the last end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			before, stop := base.openUntil(t, cfg)
			before.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			ran := before.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := before.rec(t, ran.Job).EndedAt
			changed := lastEnd.Add(time.Hour)
			sleepUntil(lastEnd.Add(90 * time.Minute))
			stop()

			r := base.open(t, cfg)
			synctest.Wait()
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(6*time.Hour), changed)
			if got, want := r.ex.Deadline("quick", ""), changed.Add(18*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a restart = %v, want %v: the change, later than the last end, plus the lease", got, want)
			}
			r.nextWake(t, "quick", "", changed, 6*time.Hour)
		})
	})

	t.Run("a first schedule counts from the last end after the change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			before, stop := base.openUntil(t, cfg)
			before.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			time.Sleep(time.Hour)
			changed := time.Now()
			time.Sleep(time.Hour)
			ran := before.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := before.rec(t, ran.Job).EndedAt
			sleepUntil(lastEnd.Add(time.Hour))
			stop()

			r := base.open(t, cfg)
			synctest.Wait()
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(6*time.Hour), changed)
			if got, want := r.ex.Deadline("quick", ""), lastEnd.Add(18*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a restart = %v, want %v: the last end, later than the change, plus the lease", got, want)
			}
			r.nextWake(t, "quick", "", lastEnd, 6*time.Hour)
		})
	})

	t.Run("a restart after a shortening save reads fresh", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			before, stop := base.openUntil(t, cfg)
			before.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			ran := before.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := before.rec(t, ran.Job).EndedAt
			sleepUntil(lastEnd.Add(4 * time.Hour))
			changed := time.Now()
			before.scheduleAt(t, "quick", "", scheduler.Scheduled(15*time.Minute), changed)
			sleepUntil(changed.Add(10 * time.Minute))
			stop()

			r := base.open(t, cfg)
			synctest.Wait()
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(15*time.Minute), changed)
			if got, want := r.ex.Deadline("quick", ""), changed.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a restart = %v, want %v: the saved change plus three 15-minute intervals", got, want)
			}
			if !r.ex.Fresh("quick", "").Healthy() {
				t.Error("Fresh unhealthy after a restart 10 minutes past a save to a shorter cadence, want healthy")
			}
			r.nextWake(t, "quick", "", changed, 15*time.Minute)
		})
	})

	t.Run("a change given before a late call counts from the change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, cfg)
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			ran := r.start(t, api("quick", "", "alice", payload{}))
			synctest.Wait()
			sleepUntil(r.rec(t, ran.Job).EndedAt.Add(4 * time.Hour))
			changed := time.Now()
			time.Sleep(10 * time.Minute)
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(2*time.Hour), changed)
			if got, want := r.ex.Deadline("quick", ""), changed.Add(6*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a change saved 10 minutes before the call = %v, want %v: the change plus three 2-hour intervals", got, want)
			}
			r.nextWake(t, "quick", "", changed, 2*time.Hour)
		})
	})

	t.Run("a change in the future counts from the call", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, cfg)
			r.schedule(t, "quick", "", scheduler.Scheduled(6*time.Hour))
			time.Sleep(4 * time.Hour)
			call := time.Now()
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(2*time.Hour), call.Add(time.Hour))
			if got, want := r.ex.Deadline("quick", ""), call.Add(6*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a change dated an hour ahead = %v, want %v: the call plus three 2-hour intervals", got, want)
			}
			r.nextWake(t, "quick", "", call, 2*time.Hour)
		})
	})

	t.Run("the same cadence with a later change keeps the wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, cfg)
			t0 := time.Now()
			r.schedule(t, "quick", "", scheduler.Scheduled(time.Hour))
			time.Sleep(20 * time.Minute)
			r.scheduleAt(t, "quick", "", scheduler.Scheduled(time.Hour), time.Now())
			if got, want := r.ex.Deadline("quick", ""), t0.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after the same cadence with a later change = %v, want it unmoved at %v", got, want)
			}
			r.nextWake(t, "quick", "", t0, time.Hour)
		})
	})
}

func TestScheduledScopesBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, scheduler.Config[payload]{MaxQueued: 256, MaxQueuedPerOwner: 4, Kinds: []scheduler.KindSpec[payload]{spec("refresh", "")}})
		t0 := time.Now()
		for i := range 64 {
			r.schedule(t, "refresh", fmt.Sprintf("connection:%d", i), scheduler.Scheduled(time.Hour))
		}
		if err := r.ex.Schedule("refresh", "connection:64", scheduler.Scheduled(time.Hour), time.Time{}); !errors.Is(err, scheduler.ErrTooManyScopes) {
			t.Fatalf("Schedule(a 65th scope) = %v, want ErrTooManyScopes", err)
		}
		if got := r.ex.Deadline("refresh", "connection:64"); !got.IsZero() {
			t.Errorf("Deadline of the refused scope = %v, want zero", got)
		}
		_, hi := jitterBand(time.Hour)
		sleepUntil(t0.Add(hi + time.Second))
		for i := range 64 {
			if n := len(r.wakes("refresh", fmt.Sprintf("connection:%d", i))); n != 1 {
				t.Errorf("connection:%d woke %d times in its first band, want 1", i, n)
			}
		}
		if n := len(r.wakes("refresh", "connection:64")); n != 0 {
			t.Errorf("the refused scope woke %d times, want 0", n)
		}

		r.ex.Unschedule("refresh", "connection:0")
		added := time.Now()
		r.schedule(t, "refresh", "connection:64", scheduler.Scheduled(time.Hour))
		if got, want := r.ex.Deadline("refresh", "connection:64"), added.Add(3*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline of a scope scheduled into the freed place = %v, want %v", got, want)
		}
		r.nextWake(t, "refresh", "connection:64", added, time.Hour)
	})
}

func TestRefusedScheduleLeavesTheFirstSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("refresh", "")}})
		ran := r.start(t, api("refresh", "connection:64", "alice", payload{}))
		synctest.Wait()
		lastEnd := r.rec(t, ran.Job).EndedAt
		for i := range 64 {
			r.schedule(t, "refresh", fmt.Sprintf("connection:%d", i), scheduler.Scheduled(24*time.Hour))
		}
		sleepUntil(lastEnd.Add(time.Hour))
		if err := r.ex.Schedule("refresh", "connection:64", scheduler.Scheduled(time.Hour), time.Time{}); !errors.Is(err, scheduler.ErrTooManyScopes) {
			t.Fatalf("Schedule(a 65th scope) = %v, want ErrTooManyScopes", err)
		}
		r.ex.Unschedule("refresh", "connection:0")
		r.schedule(t, "refresh", "connection:64", scheduler.Scheduled(24*time.Hour))
		if got, want := r.ex.Deadline("refresh", "connection:64"), lastEnd.Add(72*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline after a refused Schedule = %v, want %v: the refusal changed nothing, so this first Schedule counts from the last end", got, want)
		}
	})
}

// floorWatch is a kind's Watch with a 3-hour floor, so a short cadence's
// lease is the floor and a long one's is three intervals.
func floorWatch(calls *[]time.Duration) func(time.Duration) health.Lease {
	return func(every time.Duration) health.Lease {
		*calls = append(*calls, every)
		return health.Lease{Interval: every, Cycles: 3, Floor: 3 * time.Hour}
	}
}

func TestWatchLeaseFollowsTheCadence(t *testing.T) {
	t.Run("a kind's Watch is read with the scope's interval at each change", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls []time.Duration
			s := spec("scan", "")
			s.Watch = floorWatch(&calls)
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{s}})
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(15*time.Minute))
			ran := r.start(t, api("scan", "source:a", "alice", payload{}))
			synctest.Wait()
			lastEnd := r.rec(t, ran.Job).EndedAt
			if got, want := r.ex.Deadline("scan", "source:a"), lastEnd.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline on a 15-minute cadence = %v, want %v: the lease's 3-hour floor", got, want)
			}
			sleepUntil(lastEnd.Add(time.Minute))
			changed := time.Now()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(6*time.Hour))
			if got, want := r.ex.Deadline("scan", "source:a"), changed.Add(18*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline after a swap to 6 hours = %v, want %v: three 6-hour intervals from the change", got, want)
			}
			if want := []time.Duration{15 * time.Minute, 6 * time.Hour}; !slices.Equal(calls, want) {
				t.Errorf("Watch called with %v, want %v", calls, want)
			}
		})
	})

	t.Run("the default lease follows a swap to a longer cadence", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}})
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(15*time.Minute))
			ran := r.start(t, api("scan", "source:a", "alice", payload{}))
			synctest.Wait()
			sleepUntil(r.rec(t, ran.Job).EndedAt.Add(time.Minute))
			changed := time.Now()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(6*time.Hour))
			sleepUntil(changed.Add(2 * time.Hour))
			if !r.ex.Fresh("scan", "source:a").Healthy() {
				t.Errorf("Fresh 2 hours after a swap from 15 minutes to 6 hours = false, want true: the lease is three 6-hour intervals")
			}
		})
	})

	t.Run("a zero lease is refused and changes nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := spec("scan", "")
			s.Watch = func(every time.Duration) health.Lease {
				if every >= time.Hour {
					return health.Lease{}
				}
				return health.Lease{Interval: every, Cycles: 3}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{s}})
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(15*time.Minute))
			ranA := r.start(t, api("scan", "source:a", "alice", payload{}))
			ranB := r.start(t, api("scan", "source:b", "alice", payload{}))
			synctest.Wait()
			endA, endB := r.rec(t, ranA.Job).EndedAt, r.rec(t, ranB.Job).EndedAt
			sleepUntil(endA.Add(time.Minute))

			if err := r.ex.Schedule("scan", "source:a", scheduler.Scheduled(2*time.Hour), time.Time{}); !errors.Is(err, scheduler.ErrLease) {
				t.Errorf("Schedule(a cadence whose lease is zero) = %v, want ErrLease", err)
			}
			if got, want := r.ex.Deadline("scan", "source:a"), endA.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a refused change = %v, want %v: the 15-minute lease kept", got, want)
			}
			r.nextWake(t, "scan", "source:a", endA, 15*time.Minute)

			if err := r.ex.Schedule("scan", "source:b", scheduler.Scheduled(2*time.Hour), time.Time{}); !errors.Is(err, scheduler.ErrLease) {
				t.Errorf("first Schedule with a zero lease = %v, want ErrLease", err)
			}
			if got := r.ex.Deadline("scan", "source:b"); !got.IsZero() {
				t.Errorf("Deadline of a scope whose Schedule was refused = %v, want zero: not scheduled", got)
			}
			r.schedule(t, "scan", "source:b", scheduler.Scheduled(15*time.Minute))
			if got, want := r.ex.Deadline("scan", "source:b"), endB.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a refused first Schedule = %v, want %v: the refusal changed nothing, so this first Schedule counts from the last end", got, want)
			}
		})
	})
}

// A daily scope whose jobs all end, ok or failed, stays fresh minute by
// minute for 80 hours, and each wake comes an interval after the end before
// it: a failed end with no Retry waits the interval, never the next minute.
func TestScheduledScopeStaysFreshAcrossWakes(t *testing.T) {
	for _, res := range []scheduler.Result{scheduler.ResultOK, scheduler.ResultFailed} {
		t.Run("jobs that end "+string(res), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				daily := spec("daily", "passes")
				daily.Run = func(ctx context.Context, _ *scheduler.Run[payload]) scheduler.End {
					select {
					case <-time.After(50 * time.Second):
					case <-ctx.Done():
					}
					return scheduler.End{Result: res}
				}
				r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{daily}})
				t0 := time.Now()
				r.schedule(t, "daily", "", scheduler.Scheduled(24*time.Hour))
				fresh := r.ex.Fresh("daily", "")
				for minute := range 80 * 60 {
					time.Sleep(time.Minute)
					if !fresh.Healthy() {
						t.Fatalf("Fresh unhealthy %d minutes into a daily scope whose jobs end %s, deadline %v", minute+1, res, r.ex.Deadline("daily", ""))
					}
				}
				wakes := r.wakes("daily", "")
				if len(wakes) != 3 {
					t.Fatalf("%d wakes in 80 hours of a daily cadence, want 3", len(wakes))
				}
				base := t0
				for i, w := range wakes {
					if off := w.QueuedAt.Sub(base); !inBand(off, 24*time.Hour) {
						t.Errorf("wake %d came %v after the end before it, want a day, jittered", i+1, off)
					}
					base = r.rec(t, w.ID).EndedAt
				}
			})
		})
	}
}

// A failed Records.LastEnds read, at Run's start or at a Hold(all)'s resume,
// is retried on 1, 2, 4 and 8 minutes, then every 15. No timer is armed until
// a read answers, and meanwhile each deadline counts from the start or the
// resume.
func TestLastEndsReadRetried(t *testing.T) {
	errDown := errors.New("store down")

	t.Run("a failed read at start arms nothing until the retry answers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("daily", "")}}
			before, stop := base.openUntil(t, cfg)
			ran := before.start(t, api("daily", "", "alice", payload{}))
			synctest.Wait()
			lastEnd := before.rec(t, ran.Job).EndedAt
			stop()
			// Past the 24-hour wait's jitter band, so the scope is overdue.
			sleepUntil(lastEnd.Add(27 * time.Hour))

			base.store.Fail(memrecords.OpLastEnds, errDown)
			start := time.Now()
			r := base.open(t, cfg)
			r.schedule(t, "daily", "", scheduler.Scheduled(24*time.Hour))
			sleepUntil(start.Add(59 * time.Second))
			if n := len(r.wakes("daily", "")); n != 0 {
				t.Errorf("%d timer records before the read answered, want 0", n)
			}
			if got, want := r.ex.Deadline("daily", ""), start.Add(72*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline while the read fails = %v, want %v: the start plus the lease", got, want)
			}
			base.store.Fail(memrecords.OpLastEnds, nil)
			sleepUntil(start.Add(time.Minute))
			w := r.wakes("daily", "")
			if len(w) != 1 || !w[0].QueuedAt.Equal(start.Add(time.Minute)) {
				t.Fatalf("timer records after the 1-minute retry answered = %+v, want one queued at %v: the scope was overdue", w, start.Add(time.Minute))
			}
		})
	})

	t.Run("the read is retried on 1, 2, 4 and 8 minutes, then every 15", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := memrecords.New()
			store.Fail(memrecords.OpLastEnds, errDown)
			lines := &logLines{}
			start := time.Now()
			(&rig{store: store, log: &runLog{}}).open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}, Logger: slog.New(lines)})
			for _, tc := range []struct {
				at    time.Duration
				calls int
			}{
				{0, 1},
				{time.Minute - time.Second, 1},
				{time.Minute, 2},
				{3*time.Minute - time.Second, 2},
				{3 * time.Minute, 3},
				{7 * time.Minute, 4},
				{15 * time.Minute, 5},
				{30*time.Minute - time.Second, 5},
				{30 * time.Minute, 6},
				{45 * time.Minute, 7},
			} {
				sleepUntil(start.Add(tc.at))
				if n := store.Count(memrecords.OpLastEnds); n != tc.calls {
					t.Errorf("LastEnds calls %v after start = %d, want %d", tc.at, n, tc.calls)
				}
			}
			if n := lines.count(slog.LevelError); n != 7 {
				t.Errorf("%d ERROR lines after seven failed reads, want 7", n)
			}
		})
	})

	t.Run("a store failing at start for longer than the lease reads stale", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := memrecords.New()
			store.Fail(memrecords.OpLastEnds, errDown)
			lines := &logLines{}
			start := time.Now()
			r := (&rig{store: store, log: &runLog{}}).open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}, Logger: slog.New(lines)})
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
			sleepUntil(start.Add(3 * time.Hour))
			if !r.ex.Fresh("scan", "source:a").Healthy() {
				t.Error("Fresh at the start plus the lease = false, want true")
			}
			sleepUntil(start.Add(3*time.Hour + time.Second))
			if r.ex.Fresh("scan", "source:a").Healthy() {
				t.Error("Fresh past the start plus the lease with the store unreadable = true, want false")
			}
			if n := len(r.wakes("scan", "source:a")); n != 0 {
				t.Errorf("%d timer records while the read fails, want 0", n)
			}
			if n := countContaining(lines.text(slog.LevelError), "not ended by its deadline"); n != 1 {
				t.Errorf("%d ERROR lines for the passed deadline, want 1", n)
			}
		})
	})

	t.Run("a failed read at resume arms nothing until the retry answers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			end, resume := heldAfterAnEnd(t, r, "source:t")
			sleepUntil(end.Add(2 * time.Hour))
			r.store.Fail(memrecords.OpLastEnds, errDown)
			resumed := time.Now()
			resume()
			sleepUntil(resumed.Add(59 * time.Second))
			if n := len(r.wakes("scan", "source:t")); n != 0 {
				t.Errorf("%d timer records before the resume's read answered, want 0", n)
			}
			if got, want := r.ex.Deadline("scan", "source:t"), resumed.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline while the resume's read fails = %v, want %v: the resume plus the lease", got, want)
			}
			r.store.Fail(memrecords.OpLastEnds, nil)
			sleepUntil(resumed.Add(time.Minute))
			w := r.wakes("scan", "source:t")
			if len(w) != 1 || !w[0].QueuedAt.Equal(resumed.Add(time.Minute)) {
				t.Fatalf("timer records after the 1-minute retry answered = %+v, want one queued at %v: the scope was overdue", w, resumed.Add(time.Minute))
			}
		})
	})

	t.Run("a store failing at resume for longer than the lease reads stale", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			_, resume := heldAfterAnEnd(t, r, "source:t")
			time.Sleep(time.Minute)
			r.store.Fail(memrecords.OpLastEnds, errDown)
			resumed := time.Now()
			resume()
			sleepUntil(resumed.Add(3 * time.Hour))
			if !r.ex.Fresh("scan", "source:t").Healthy() {
				t.Error("Fresh at the resume plus the lease = false, want true")
			}
			sleepUntil(resumed.Add(3*time.Hour + time.Second))
			if r.ex.Fresh("scan", "source:t").Healthy() {
				t.Error("Fresh past the resume plus the lease with the store unreadable = true, want false")
			}
			if n := len(r.wakes("scan", "source:t")); n != 0 {
				t.Errorf("%d timer records while the resume's read fails, want 0", n)
			}
		})
	})

	t.Run("a later hold ends an earlier resume's retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			_, resume := heldAfterAnEnd(t, r, "source:t")
			r.store.Fail(memrecords.OpLastEnds, errDown)
			resume()
			calls := r.store.Count(memrecords.OpLastEnds)
			time.Sleep(10 * time.Second)
			resume, err := r.ex.Hold(t.Context(), true)
			if err != nil {
				t.Fatalf("Hold(all) = %v, want nil", err)
			}
			time.Sleep(10 * time.Second)
			resumed := time.Now()
			resume()
			sleepUntil(resumed.Add(15 * time.Minute))
			// The second resume's own read, then its retries at 1, 3, 7 and 15
			// minutes; the first resume's retry, due at 1 minute, never reads.
			if n := r.store.Count(memrecords.OpLastEnds); n != calls+5 {
				t.Errorf("LastEnds calls 15 minutes after the second resume = %d, want %d: one retry, the earlier resume's ended", n, calls+5)
			}
		})
	})
}

// heldAfterAnEnd schedules scan's scope hourly, runs one job of it and holds
// the executor once it ended; it answers the end and the hold's resume.
func heldAfterAnEnd(t *testing.T, r *rig, scope string) (time.Time, func()) {
	t.Helper()
	r.schedule(t, "scan", scope, scheduler.Scheduled(time.Hour))
	ran := r.start(t, api("scan", scope, "alice", payload{}))
	synctest.Wait()
	end := r.rec(t, ran.Job).EndedAt
	resume, err := r.ex.Hold(t.Context(), true)
	if err != nil {
		t.Fatalf("Hold(all) = %v, want nil", err)
	}
	return end, resume
}

// countContaining answers how many of lines contain s.
func countContaining(lines []string, s string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

// dailyRetry is quick's cadence in the covering-end tests.
var dailyRetry = scheduler.Scheduled(24 * time.Hour).Retry(15 * time.Minute)

// coveredKinds is quick and full, whose Covers covers quick; neither has a
// slot, and full's jobs wait on their Gate and end failed.
func coveredKinds() []scheduler.KindSpec[payload] {
	full := spec("full", "")
	full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "quick" }
	full.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		select {
		case <-r.Job().Payload.Gate:
		case <-ctx.Done():
		}
		return scheduler.End{Result: scheduler.ResultFailed}
	}
	return []scheduler.KindSpec[payload]{spec("quick", ""), full}
}

// tiedEnds schedules quick, runs a quick job that ends ok, then a full job
// that ends failed, and lets them end at one instant, the kinds in order one
// after the other; it answers the instant.
func tiedEnds(t *testing.T, r *rig, order []scheduler.Kind) time.Time {
	t.Helper()
	r.schedule(t, "quick", "", dailyRetry)
	time.Sleep(10 * time.Minute)
	gates := map[scheduler.Kind]chan struct{}{}
	ids := map[scheduler.Kind]scheduler.ID{}
	// quick runs before full starts, so full's Covers finds nothing queued.
	for _, k := range []scheduler.Kind{"quick", "full"} {
		gates[k] = make(chan struct{})
		ids[k] = r.start(t, api(k, "", "alice", payload{Gate: gates[k]})).Job
		synctest.Wait()
	}
	at := time.Now()
	for _, k := range order {
		close(gates[k])
		synctest.Wait()
	}
	for k, id := range ids {
		if rec := r.rec(t, id); rec.State != scheduler.StateEnded || !rec.EndedAt.Equal(at) {
			t.Fatalf("%s %s at %v, want ended at %v: the two ends must tie", k, rec.State, rec.EndedAt, at)
		}
	}
	return at
}

func TestCoveringEndTiedWithOwnEnd(t *testing.T) {
	for _, c := range []struct {
		name  string
		order []scheduler.Kind
	}{
		{"own ok end noted first", []scheduler.Kind{"quick", "full"}},
		{"covering failed end noted first", []scheduler.Kind{"full", "quick"}},
	} {
		t.Run(c.name+", in process", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, scheduler.Config[payload]{Kinds: coveredKinds()})
				at := tiedEnds(t, r, c.order)
				r.nextWake(t, "quick", "", at, 24*time.Hour)
			})
		})

		t.Run(c.name+", after a restart", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &rig{store: memrecords.New(), log: &runLog{}}
				cfg := scheduler.Config[payload]{Kinds: coveredKinds()}
				before, stop := base.openUntil(t, cfg)
				at := tiedEnds(t, before, c.order)
				stop()

				r := base.open(t, cfg)
				synctest.Wait()
				r.schedule(t, "quick", "", dailyRetry)
				r.nextWake(t, "quick", "", at, 24*time.Hour)
			})
		})
	}
}

// heldEnds holds the End of one job after the store wrote it, until release
// closes, so the executor notes that end after a later one.
type heldEnds struct {
	*memrecords.Store
	entered, release chan struct{}
	id               scheduler.ID
	mu               sync.Mutex
}

func (s *heldEnds) hold(id scheduler.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *heldEnds) End(ctx context.Context, id scheduler.ID, end scheduler.End) (scheduler.Record, error) {
	rec, err := s.Store.End(ctx, id, end)
	s.mu.Lock()
	held := id == s.id
	s.mu.Unlock()
	if held {
		close(s.entered)
		<-s.release
	}
	return rec, err
}

func TestOlderCoveringEndNotedLate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &heldEnds{Store: memrecords.New(), entered: make(chan struct{}), release: make(chan struct{})}
		r := (&rig{store: s.Store, log: &runLog{}}).open(t, scheduler.Config[payload]{Kinds: coveredKinds(), Records: s})
		r.schedule(t, "quick", "", dailyRetry)
		time.Sleep(10 * time.Minute)
		quickGate, fullGate := make(chan struct{}), make(chan struct{})
		q := r.start(t, api("quick", "", "alice", payload{Gate: quickGate}))
		synctest.Wait()
		f := r.start(t, api("full", "", "bob", payload{Gate: fullGate}))
		s.hold(f.Job)
		close(fullGate)
		<-s.entered
		time.Sleep(time.Minute)
		close(quickGate)
		synctest.Wait()
		own := r.rec(t, q.Job).EndedAt
		close(s.release)
		synctest.Wait()

		if got := r.rec(t, f.Job).EndedAt; !got.Before(own) {
			t.Fatalf("full ended at %v, want before quick's own end at %v", got, own)
		}
		if got, want := r.ex.Deadline("quick", ""), own.Add(72*time.Hour); !got.Equal(want) {
			t.Errorf("Deadline after an older covering end noted late = %v, want %v: quick's own later end plus the lease", got, want)
		}
		r.nextWake(t, "quick", "", own, 24*time.Hour)
	})
}

// proposingKinds is quick and prop, a proposing kind whose Covers covers
// quick; neither has a slot, and prop's jobs wait on their Gate and end
// failed at the scope p:fail, ok at any other.
func proposingKinds() []scheduler.KindSpec[payload] {
	prop := spec("prop", "")
	prop.Proposes = true
	prop.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "quick" }
	prop.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		select {
		case <-r.Job().Payload.Gate:
		case <-ctx.Done():
		}
		if r.Job().Scope == "p:fail" {
			return scheduler.End{Result: scheduler.ResultFailed}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return []scheduler.KindSpec[payload]{spec("quick", ""), prop}
}

// gated builds a job that waits on gate.
type gated func(gate <-chan struct{}) scheduler.Job[payload]

// endsOutOfOrder schedules quick, starts older then later, ends older with
// its End held after the store wrote it, ends later a minute after, then
// releases older's End, so the executor notes the later end first. It
// answers the later end.
func endsOutOfOrder(t *testing.T, r *rig, s *heldEnds, older, later gated) scheduler.Record {
	t.Helper()
	r.schedule(t, "quick", "", dailyRetry)
	time.Sleep(10 * time.Minute)
	og, lg := make(chan struct{}), make(chan struct{})
	o := r.start(t, older(og))
	synctest.Wait()
	l := r.start(t, later(lg))
	synctest.Wait()
	s.hold(o.Job)
	close(og)
	<-s.entered
	time.Sleep(time.Minute)
	close(lg)
	synctest.Wait()
	close(s.release)
	synctest.Wait()
	end := r.rec(t, l.Job)
	if got := r.rec(t, o.Job).EndedAt; !got.Before(end.EndedAt) {
		t.Fatalf("held job ended at %v, want before the later end at %v", got, end.EndedAt)
	}
	return end
}

func TestProposingKindCoveringEndNotedLate(t *testing.T) {
	for _, c := range []struct {
		name         string
		older, later gated
		wait         time.Duration // from the later end
	}{
		{
			name: "an older failed covering end after a later ok one",
			older: func(g <-chan struct{}) scheduler.Job[payload] {
				return api("prop", "p:fail", "alice", payload{Gate: g})
			},
			later: func(g <-chan struct{}) scheduler.Job[payload] { return api("prop", "p:ok", "alice", payload{Gate: g}) },
			wait:  24 * time.Hour,
		},
		{
			name:  "an older own ok end after a later failed covering one",
			older: func(g <-chan struct{}) scheduler.Job[payload] { return api("quick", "", "bob", payload{Gate: g}) },
			later: func(g <-chan struct{}) scheduler.Job[payload] {
				return api("prop", "p:fail", "alice", payload{Gate: g})
			},
			wait: 15 * time.Minute,
		},
	} {
		run := func(t *testing.T, restart bool) {
			synctest.Test(t, func(t *testing.T) {
				s := &heldEnds{Store: memrecords.New(), entered: make(chan struct{}), release: make(chan struct{})}
				base := &rig{store: s.Store, log: &runLog{}}
				r, stop := base.openUntil(t, scheduler.Config[payload]{Kinds: proposingKinds(), Records: s})
				later := endsOutOfOrder(t, r, s, c.older, c.later)
				if restart {
					stop()
					r = base.open(t, scheduler.Config[payload]{Kinds: proposingKinds()})
					synctest.Wait()
					r.schedule(t, "quick", "", dailyRetry)
				}
				if got, want := r.ex.Deadline("quick", ""), later.EndedAt.Add(72*time.Hour); !got.Equal(want) {
					t.Errorf("Deadline = %v, want %v: the later end plus the lease", got, want)
				}
				r.nextWake(t, "quick", "", later.EndedAt, c.wait)
			})
		}
		t.Run(c.name+", in process", func(t *testing.T) { run(t, false) })
		t.Run(c.name+", after a restart", func(t *testing.T) { run(t, true) })
	}
}

func TestLastEndsHeldBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sweep := spec("sweep", "")
		sweep.Kept = 0
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("own", ""), sweep}})
		synctest.Wait()
		r.schedule(t, "own", "a", scheduler.Scheduled(24*time.Hour))
		time.Sleep(time.Minute)
		counted := r.start(t, api("own", "a", "alice", payload{})).Job
		synctest.Wait()
		time.Sleep(time.Minute)
		r.start(t, api("own", "old", "alice", payload{}))
		synctest.Wait()
		time.Sleep(time.Minute)
		// A store keeps at most 10,000 records in all, so a restart reads no
		// more last ends than that.
		for i := range 10_000 {
			r.start(t, api("sweep", fmt.Sprintf("s%d", i), "alice", payload{}))
			synctest.Wait()
		}

		if got := r.ex.EndsHeld(); got != 10_000 {
			t.Errorf("EndsHeld after 10,002 scopes ended = %d, want 10,000", got)
		}
		end := r.rec(t, counted).EndedAt
		r.scheduleAt(t, "own", "a", scheduler.Scheduled(12*time.Hour), end.Add(-time.Second))
		if got, _ := r.ex.TimingOf("own", "a"); !got.FromEnd || !got.Base.Equal(end) {
			t.Errorf("own a after the bound counts from %v (from an end %v), want its own end at %v, the oldest one held", got.Base, got.FromEnd, end)
		}
		r.schedule(t, "own", "old", scheduler.Scheduled(24*time.Hour))
		if got, _ := r.ex.TimingOf("own", "old"); got.FromEnd {
			t.Errorf("own old after the bound counts from an end at %v, want none: the oldest end no scheduled scope counts from goes first", got.Base)
		}
	})
}
