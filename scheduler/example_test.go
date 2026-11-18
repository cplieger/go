package scheduler_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler/v2"
)

func ExampleParseInterval() {
	built := scheduler.ParseInterval("30m", time.Hour)
	fmt.Printf("%s every %s\n", built.Mode, built.Interval)

	fmt.Println(scheduler.ParseInterval("off", time.Hour).Mode)
	fmt.Println(scheduler.ParseInterval("0", time.Hour, scheduler.WithZeroAsOnce(true)).Mode)
	// Output:
	// built-in every 30m0s
	// external
	// once
}

func ExampleExclusive() {
	dir, err := os.MkdirTemp("", "scheduler_example_exclusive")
	if err != nil {
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ex := scheduler.NewExclusive(dir, scheduler.WithLogger(slog.New(slog.DiscardHandler)))
	outcome, _ := ex.Run(func() error {
		fmt.Println("cycle ran")
		return nil
	})
	fmt.Println("outcome:", outcome)
	// Output:
	// cycle ran
	// outcome: ran
}

func ExampleTryLock() {
	path := filepath.Join(os.TempDir(), "scheduler_example.lock")
	defer func() { _ = os.Remove(path) }()

	lock, ok, _ := scheduler.TryLock(path)
	fmt.Println("first acquire:", ok)

	_, contended, _ := scheduler.TryLock(path)
	fmt.Println("second acquire while held:", contended)

	lock.Unlock()
	// Output:
	// first acquire: true
	// second acquire while held: false
}

func ExampleStamp() {
	dir, err := os.MkdirTemp("", "scheduler_example_stamp")
	if err != nil {
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	stamp := scheduler.NewStamp(filepath.Join(dir, "last-run"))
	interval := 6 * time.Hour

	// At boot: fire the startup run only when no recent run survived the
	// restart (wire the result into LoopOptions.FireOnStart).
	fmt.Println("startup run due:", stamp.Due(interval, time.Now(), scheduler.RetryFailed))

	// The executor records each completed scheduled run and its outcome.
	if err := stamp.Record(true); err == nil {
		fmt.Println("due after a fresh success:", stamp.Due(interval, time.Now(), scheduler.RetryFailed))
	}
	// Output:
	// startup run due: true
	// due after a fresh success: false
}

// One kind of job on a named slot: a refresh scheduled every 6 hours for
// every connection, and one started for connection 3 the way an API row
// would. The Observer prints each transition of the started job.
func ExampleNewExecutor() {
	ended := make(chan struct{})
	ex, err := scheduler.NewExecutor(scheduler.Config[struct{}]{
		Logger: slog.New(slog.DiscardHandler),
		Kinds: []scheduler.KindSpec[struct{}]{{
			Kind:   "refresh",
			Slot:   "upstream",
			Phases: []scheduler.Phase{"fetch", "write"},
			Run: func(ctx context.Context, r *scheduler.Run[struct{}]) scheduler.End {
				r.Reached("fetch")
				// ... read the upstream, returning once ctx is done ...
				r.Reached("write")
				if !r.Commit() {
					return scheduler.End{Result: scheduler.ResultCancelled}
				}
				// ... the job's one commit ...
				return scheduler.End{Result: scheduler.ResultOK}
			},
		}},
		Observer: scheduler.Observer{Transition: func(t scheduler.Transition) {
			if t.Result == "" {
				fmt.Println(t.Scope, t.Phase)
				return
			}
			fmt.Println(t.Scope, "ended", t.Result)
			close(ended)
		}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- ex.Run(ctx) }()

	if err := ex.Schedule("refresh", "", scheduler.Scheduled(6*time.Hour).Retry(15*time.Minute)); err != nil {
		fmt.Println(err)
	}
	if _, err := ex.Start(ctx, scheduler.Job[struct{}]{Kind: "refresh", Scope: "connection:3", Trigger: scheduler.TriggerAPI, Owner: "key:7"}); err != nil {
		fmt.Println(err)
	}
	<-ended
	fmt.Println("all connections fresh:", ex.Fresh("refresh", "").Healthy())

	stop()
	<-stopped
	// Output:
	// connection:3 queued
	// connection:3 started
	// connection:3 fetch
	// connection:3 write
	// connection:3 committing
	// connection:3 ended ok
	// all connections fresh: true
}

// A loop that is not a job holds its own Watch: here a sweep every 15
// seconds that is stalled after four missed passes.
func ExampleNewWatch() {
	w := scheduler.NewWatch(health.Lease{Interval: 15 * time.Second, Cycles: 4})
	fresh := w.Fresh() // for a health.Check on /healthz

	// After each pass whose last write committed:
	w.Beat(time.Now())

	fmt.Println(fresh.Healthy(), time.Until(w.Deadline()).Round(time.Second))
	// Output: true 1m0s
}
