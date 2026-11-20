package scheduler

import (
	"context"
	"math/rand/v2"
	"time"
)

// LoopOptions configures RunLoop. Interval must be positive; Jitter,
// FirstDelay, FireOnStart and Scheduled are optional.
type LoopOptions struct {
	// Scheduled, when set, is called with the time the job will run each time
	// RunLoop arms its timer, on RunLoop's goroutine after the arm, so a
	// Scheduled still running at that time delays that run only.
	Scheduled func(next time.Time)
	// Interval is the gap between ticks; RunLoop returns at once unless positive.
	Interval time.Duration
	// Jitter spreads each tick uniformly across ±(Jitter × Interval), so
	// instances restarted together do not reach a shared upstream at once.
	// 0 or less (NaN included) disables it; 1 or more uses the full band.
	Jitter float64
	// FirstDelay, when positive, replaces the first tick's delay, unjittered,
	// so a schedule's phase survives a restart: pass Stamp.Remaining. Ignored
	// when FireOnStart is true.
	FirstDelay time.Duration
	// FireOnStart runs the job at once, unjittered, before the first interval;
	// compute it from Stamp.Due to skip a startup run a recent run covers.
	FireOnStart bool
}

// RunLoop runs job, passing it ctx, on a schedule until ctx is cancelled, and
// returns once the running job has returned, so its return is a completed
// drain. Runs never overlap in-process; guard cross-process overlap with
// TryLock inside the job. A panic in job is not recovered. It covers
// ModeBuiltin only and returns at once if Interval is not positive.
func RunLoop(ctx context.Context, job func(ctx context.Context), opts LoopOptions) {
	if opts.Interval <= 0 {
		return
	}

	delay := JitteredDelay(opts.Interval, opts.Jitter)
	if opts.FirstDelay > 0 {
		delay = opts.FirstDelay
	}
	if opts.FireOnStart {
		delay = 0
	}
	for {
		next := time.Now().Add(delay)
		timer := time.NewTimer(delay)
		if opts.Scheduled != nil {
			opts.Scheduled(next)
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// A fired timer and a cancelled context can both be ready in the
			// same iteration (select picks a ready case at random), so re-check
			// the stop request before launching a run that would race the drain.
			if ctx.Err() != nil {
				return
			}
			job(ctx)
		}
		delay = JitteredDelay(opts.Interval, opts.Jitter)
	}
}

// JitteredDelay returns a delay drawn uniformly from
// [interval−fraction×interval, interval+fraction×interval), RunLoop's jitter.
// A fraction that is not positive (NaN included) or a non-positive interval
// returns interval; a fraction of 1 or more uses [0, 2×interval).
func JitteredDelay(interval time.Duration, fraction float64) time.Duration {
	if !(fraction > 0) || interval <= 0 {
		return interval
	}
	if fraction > 1 {
		fraction = 1
	}
	spread := time.Duration(fraction * float64(interval))
	span := max(2*spread, 1)
	// #nosec G404 -- scheduling jitter, not a security-sensitive value.
	return interval - spread + rand.N(span)
}
