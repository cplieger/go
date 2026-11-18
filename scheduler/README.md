# scheduler

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/scheduler/v2.svg)](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=scheduler%2Fgo.mod)](https://github.com/cplieger/go/blob/main/scheduler/go.mod)

scheduler lets your Go container run its job on an interval or an outside trigger, with no overlapping runs and a clean shutdown. For an app with many kinds of job, its executor runs every job on one set of timers, slots and records.

It replaces the interval parsing, ticker loop, lock-file handling and graceful child-process shutdown you would otherwise write in each job runner's `main`, and the queue, cancel and deadline code an app with jobs would otherwise write for itself. Its one dependency at run time is the sibling [health](https://github.com/cplieger/go/tree/main/health) module. It works on Unix systems only because its locks use `flock(2)`, and is licensed under Apache-2.0.

## Why use it

scheduler is built for a long-running container with one job, such as a backup, sync or polling daemon, and for an app that runs many kinds of job and shows each one to a person.

- `ParseInterval` reads a setting such as `JOB_INTERVAL` as a cadence, external-trigger mode or run-once mode.
- `RunLoop` runs one job at a time on every tick, plus once at start with `FireOnStart`, and waits for it on shutdown.
- `Stamp` records the last run on a volume, so a recreated container keeps its schedule and runs at start only when due.
- `TryLock` lets a `docker exec` trigger skip its run during a timer run, and `Exclusive` queues it instead.
- `NewCommandRunner` stops a child process with SIGTERM, then SIGKILL after a default 5-second grace.
- With the `trigger` package, one daemon runs every job and each trigger waits for its own run's result. The daemon binds its socket with [ownersock](https://github.com/cplieger/go/tree/main/ownersock) in a private directory of its own and passes the listener to `Server.Serve`.
- `Executor` runs every job an app starts, whether its own timer, a person through the API or the CLI asked for it. It coalesces and covers requests, orders a slot's waiting jobs by class, cancels with a protected commit step, records batch lines and proposals, and turns a scheduled scope unhealthy once its deadline passes.

Consider [robfig/cron](https://github.com/robfig/cron) if you need cron expressions. It parses the standard cron format, with an optional seconds field and per-schedule time zones.

## Install

```sh
go get github.com/cplieger/go/scheduler/v2@latest
```

## Usage

A typical `main` reads the interval setting, picks a mode, and runs each pass under the overlap lock:

```go
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

const lockPath = "/tmp/.myjob.lock"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sched := scheduler.ParseInterval(os.Getenv("JOB_INTERVAL"), 6*time.Hour,
		scheduler.WithName("JOB_INTERVAL"))

	switch sched.Mode {
	case scheduler.ModeBuiltin:
		// Fire once now, then every interval with ±10% jitter, draining on SIGTERM.
		scheduler.RunLoop(ctx, runPass, scheduler.LoopOptions{
			Interval:    sched.Interval,
			FireOnStart: true,
			Jitter:      0.10,
		})
	case scheduler.ModeExternal:
		// Idle: runs arrive from outside, for example a docker exec of a
		// one-shot subcommand, and the lock keeps them from overlapping.
		<-ctx.Done()
	case scheduler.ModeOnce:
		runPass(ctx) // run exactly once, then exit
	}
}

// run builds context-cancellable subprocesses that get SIGTERM on shutdown
// and SIGKILL only after the grace period.
var run = scheduler.NewCommandRunner(scheduler.DefaultGrace)

func runPass(ctx context.Context) {
	lock, ok, err := scheduler.TryLock(lockPath)
	if err != nil {
		return // could not acquire; mark unhealthy in a real app
	}
	if !ok {
		return // another run is in flight; the overlap guard skips this one
	}
	defer lock.Unlock()

	cmd := run(ctx, "rsync", "-a", "/src/", "remote:/dst/")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run()
}
```

In `ModeExternal`, an external scheduler such as [Ofelia](https://github.com/mcuadros/ofelia) starts each run with `docker exec`. The package examples on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2#pkg-examples) show `ParseInterval`, `TryLock`, `Exclusive` and `Stamp` on their own, and `go test` keeps them true. Pass `WithRedactedValue(true)` when the interval setting can hold an expanded secret, so `ParseInterval` warnings never echo its value. The trigger server never logs request payloads, because a forwarded environment can carry secrets.

- [Scheduling a job](docs/scheduling.md) covers every interval value, restart-aware startup runs and subprocess shutdown.
- [Overlap guard and run coalescing](docs/coordination.md) covers `TryLock`, `Exclusive` and `SlotFile`, for a run that starts from more than one process.
- [Trigger broker](docs/trigger.md) covers the `trigger` package, where each trigger gets its own run and result.

## Run an app's work on one executor

An app with several kinds of job declares each as a `KindSpec` and runs them all on one `Executor`. Its own timer, its API rows and its CLI all call `Start`, so the rules for joining, waiting, cancelling and recording a job are the same whoever asked:

```go
ex, err := scheduler.NewExecutor(scheduler.Config[payload]{
	Records: store, // keeps one record per job; required when a kind has Kept > 0
	Kinds: []scheduler.KindSpec[payload]{{
		Kind:   "refresh",
		Slot:   "upstream", // one refresh at a time, people's first
		Phases: []scheduler.Phase{"fetch", "write"},
		Kept:   7 * 24 * time.Hour,
		Run: func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
			r.Reached("fetch")
			// ... read the upstream, returning once ctx is done ...
			r.Reached("write")
			if !r.Commit() {
				return scheduler.End{Result: scheduler.ResultCancelled}
			}
			// ... the job's one commit, which Cancel no longer interrupts ...
			return scheduler.End{Result: scheduler.ResultOK}
		},
	}},
})
if err != nil {
	return err
}
go func() { _ = ex.Run(ctx) }()

// Every 6 hours, and 15 minutes after a failed run.
_ = ex.Schedule("refresh", "", scheduler.Scheduled(6*time.Hour).Retry(15*time.Minute))

// A person's press, from an API handler.
started, err := ex.Start(ctx, scheduler.Job[payload]{
	Kind: "refresh", Scope: "connection:3", Trigger: scheduler.TriggerAPI, Owner: credential,
})

// /healthz turns unhealthy once a scheduled refresh is overdue.
check := health.Check{Signal: ex.Fresh("refresh", ""), Reason: "the refresh has not finished by its deadline"}
```

`ExampleNewExecutor` on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2#example-NewExecutor) runs this shape under `go test`. [The job executor](docs/executor.md) covers the rules a request follows, classes and the slot, cancel, batches, proposals, `Yield`, `Retry`, the watchdog and its stall alert, panics, `Hold` and shutdown.

## API

- Interval parsing: `ParseInterval`, `Schedule`, `Mode` and the `With*` interval options.
- Run loop: `RunLoop`, `LoopOptions` and `JitteredDelay`.
- Restart record: `Stamp`, `RunRecord` and `FailurePolicy`.
- Overlap guard: `TryLock`, `Lock` and `ReadHolder`.
- Run coalescing: `Exclusive` with `WithLogger`, `WithQueueCapacity` and `WithGate`, `Outcome`, and `SlotFile`.
- Subprocesses: `CommandRunner`, `NewCommandRunner` and `DefaultGrace`.
- Package `trigger`: `Queue`, `Job` with its `Result`, `Execute`, `Server` and its blocking `Serve`, `Submit`, `Event` with its `EventKind`, and their error values.
- Executor: `NewExecutor`, `Config`, `KindSpec`, `Executor` with `Run`, `Start`, `Cancel`, `CredentialEnded`, `Schedule`, `Unschedule`, `Fresh`, `Deadline`, `Wake`, `Proposal` and `Hold`, the job handle `Run`, and `Observer` with its `Transition`.
- Jobs and their records: `Job`, `Kind`, `Phase`, `Trigger`, `Class`, `End`, `Result`, `Reason`, `Line`, `LineState`, `ID`, `ProposalID`, `DigestScope`, the `Records` interface with `Record`, `PhaseAt`, `LastEnd`, `ScheduledScope` and `Key`, and the error values.
- Cadence and deadlines: `Cadence` with `Scheduled`, `TriggerOnly` and `Retry`, and `Watch` with `NewWatch`.
- Package `schedulertest`: `Records`, the contract test a `Records` implementation runs.

The full reference is on pkg.go.dev for [scheduler](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2), [trigger](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2/trigger) and [schedulertest](https://pkg.go.dev/github.com/cplieger/go/scheduler/v2/schedulertest).

## Unsupported by design

These are deliberate non-goals, which keep the library small.

| Feature | Rationale |
| --- | --- |
| Logging setup | Your `main` owns logging. The library logs through `slog.Default()` or a logger you pass, and never configures a handler. |
| Health signaling | Set health inside your job, with the companion [health](https://github.com/cplieger/go/tree/main/health) library. The executor answers a `health.Signal` per scheduled scope, which you wire into your own check. |
| Metrics | The package imports no metrics library. Your collector reads `Executor.Deadline` at scrape time and the `Observer` for transitions. |
| What a job does and its result type | `RunLoop` takes a `func(context.Context)`. Exit codes, health and log lines are your policy. |
| Cron expressions and calendar schedules | Run an external scheduler such as Ofelia or cron in `ModeExternal`. |
| Coordination across hosts | The `flock` guard works on one host. Leader election across nodes needs a lease store. |
| Concurrent runs in one process | `RunLoop` runs one job at a time by design. The executor runs one job per named slot, and jobs of slot `""` side by side. |
| Backoff of a failed run | Retrying outbound calls belongs to [httpclient](https://github.com/cplieger/go/tree/main/httpclient). `RunLoop` retries a failed pass on the next tick or trigger, and `Cadence.Retry` brings a scheduled scope back after one fixed delay, never a growing one. |
| Preempting a running job | A job holds its slot until it returns or yields. A person's job waits behind at most the job or batch item in progress. |

## Documentation

- [Scheduling a job](docs/scheduling.md) covers every interval value, the run loop, restart-aware startup runs and subprocess shutdown.
- [Overlap guard and run coalescing](docs/coordination.md) covers `TryLock`, `Exclusive` and `SlotFile`, for a job that starts from more than one process.
- [Trigger broker](docs/trigger.md) covers the `trigger` package, for a daemon that runs every job itself.
- [The job executor](docs/executor.md) covers `Executor`, for an app with many kinds of job and a record of each.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
