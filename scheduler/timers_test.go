package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

func TestScheduledScopesBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, scheduler.Config[payload]{MaxQueued: 256, MaxQueuedPerOwner: 4, Kinds: []scheduler.KindSpec[payload]{spec("refresh", "")}})
		t0 := time.Now()
		for i := range 64 {
			r.schedule(t, "refresh", fmt.Sprintf("connection:%d", i), scheduler.Scheduled(time.Hour))
		}
		if err := r.ex.Schedule("refresh", "connection:64", scheduler.Scheduled(time.Hour)); !errors.Is(err, scheduler.ErrTooManyScopes) {
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
		if err := r.ex.Schedule("refresh", "connection:64", scheduler.Scheduled(time.Hour)); !errors.Is(err, scheduler.ErrTooManyScopes) {
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

			if err := r.ex.Schedule("scan", "source:a", scheduler.Scheduled(2*time.Hour)); !errors.Is(err, scheduler.ErrLease) {
				t.Errorf("Schedule(a cadence whose lease is zero) = %v, want ErrLease", err)
			}
			if got, want := r.ex.Deadline("scan", "source:a"), endA.Add(45*time.Minute); !got.Equal(want) {
				t.Errorf("Deadline after a refused change = %v, want %v: the 15-minute lease kept", got, want)
			}
			r.nextWake(t, "scan", "source:a", endA, 15*time.Minute)

			if err := r.ex.Schedule("scan", "source:b", scheduler.Scheduled(2*time.Hour)); !errors.Is(err, scheduler.ErrLease) {
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
