package scheduler_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler/v2"
)

func TestWatchFreshAndDeadline(t *testing.T) {
	lease := health.Lease{Interval: time.Hour, Cycles: 3}

	t.Run("a beat moves the deadline and clears a passed one", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			w := scheduler.NewWatch(lease)
			fresh := w.Fresh()
			if got, want := w.Deadline(), start.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline() before any Beat = %v, want the start + 3h %v", got, want)
			}
			if !fresh.Healthy() {
				t.Error("a new watch: Fresh().Healthy() = false, want true")
			}

			time.Sleep(3 * time.Hour)
			if !fresh.Healthy() {
				t.Error("at the deadline itself: Healthy() = false, want true until it has passed")
			}
			time.Sleep(time.Nanosecond)
			if fresh.Healthy() {
				t.Error("past the deadline: Healthy() = true, want false")
			}

			beat := time.Now()
			w.Beat(beat)
			if got, want := w.Deadline(), beat.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline() after Beat = %v, want the beat + 3h %v", got, want)
			}
			if !fresh.Healthy() {
				t.Error("after a Beat: the Signal taken before it reads Healthy() = false, want true")
			}

			time.Sleep(3*time.Hour + time.Nanosecond)
			if fresh.Healthy() {
				t.Error("3h after the Beat: Healthy() = true, want false")
			}
		})
	})

	t.Run("an older beat changes nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := scheduler.NewWatch(lease)
			time.Sleep(time.Hour)
			beat := time.Now()
			w.Beat(beat)
			w.Beat(beat.Add(-30 * time.Minute))
			if got, want := w.Deadline(), beat.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline() after an older Beat = %v, want the latest beat + 3h %v", got, want)
			}
		})
	})

	t.Run("a first beat before the start counts from the beat", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			w := scheduler.NewWatch(lease)
			w.Beat(start.Add(-time.Hour))
			if got, want := w.Deadline(), start.Add(2*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline() after a Beat 1h before the start = %v, want %v", got, want)
			}
		})
	})

	t.Run("a zero lease puts the deadline at the beat", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			w := scheduler.NewWatch(health.Lease{})
			if got := w.Deadline(); !got.Equal(start) {
				t.Errorf("Deadline() with a zero lease = %v, want the start %v", got, start)
			}
			time.Sleep(time.Nanosecond)
			if w.Fresh().Healthy() {
				t.Error("a zero lease once time moved: Healthy() = true, want false")
			}
		})
	})
}

// The lease is a scheduled quick check's: three intervals, never under 3 hours.
func TestWatchCountsFromItsStart(t *testing.T) {
	leaseFor := func(q time.Duration) health.Lease {
		return health.Lease{Interval: q, Cycles: 3, Floor: 3 * time.Hour}
	}

	t.Run("before any beat", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			w := scheduler.NewWatch(leaseFor(15 * time.Minute))
			time.Sleep(time.Hour)
			if got, want := w.Deadline(), start.Add(3*time.Hour); !got.Equal(want) {
				t.Errorf("Deadline() 1h after the start with no Beat = %v, want the start + 3h %v", got, want)
			}
		})
	})

	t.Run("after a beat", func(t *testing.T) {
		for name, tc := range map[string]struct {
			interval, want time.Duration
		}{
			"a 15-minute interval takes the floor": {15 * time.Minute, 3 * time.Hour},
			"the crossover point":                  {time.Hour, 3 * time.Hour},
			"just above the crossover scales":      {time.Hour + time.Minute, 3*time.Hour + 3*time.Minute},
			"a 3-hour interval keeps three":        {3 * time.Hour, 9 * time.Hour},
			"a long interval scales":               {12 * time.Hour, 36 * time.Hour},
			"a short interval takes the floor":     {5 * time.Minute, 3 * time.Hour},
		} {
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w := scheduler.NewWatch(leaseFor(tc.interval))
					time.Sleep(11 * time.Hour)
					beat := time.Now()
					w.Beat(beat)
					if got, want := w.Deadline(), beat.Add(tc.want); !got.Equal(want) {
						t.Errorf("Deadline() = %v, want the beat + %v %v", got, tc.want, want)
					}
				})
			})
		}
	})
}
