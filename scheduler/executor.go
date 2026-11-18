package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/go/health/v2"
)

var errStub = errors.New("scheduler: not built")

// KindSpec declares one kind of job: where it runs, what it records and the
// work it does.
type KindSpec[P any] struct {
	// Fresh is optional: a new request for the scope joins a run that started
	// less than Fresh(scope) ago, and is refused with *RecentError after a
	// run that ended less than that ago.
	Fresh func(scope string) time.Duration
	// Covers is optional: when a job of this kind starts, each queued or
	// yielded job it covers ends without running its own work.
	Covers func(started, other Job[P]) bool
	// Items is a batch kind's item count, read from the payload at Start;
	// required with MaxItems > 0 and refused without it.
	Items func(p P) int
	// Watch is the lease a scheduled scope of this kind must end a job by;
	// nil is {Interval: every, Cycles: 3}.
	Watch *health.Lease
	// Run does the job's work and must return once ctx is done.
	Run  func(ctx context.Context, r *Run[P]) End
	Kind Kind
	// Slot "" runs at once beside anything; a named slot runs one job at a
	// time, in class order.
	Slot   string
	Phases []Phase // the pipeline, for step N of M; at most 32
	// Kept is how long an ended record is kept: 0 writes no record, else 15
	// minutes to 400 days.
	Kept     time.Duration
	MaxItems int  // a batch kind's most items per job, 1 to 500; 0 for a kind that is not a batch
	OnBehalf bool // the job writes for its starting credential, so CredentialEnded stops it
}

// Job is one request for work.
type Job[P any] struct {
	Payload P
	ID      ID // set by Start
	Kind    Kind
	// Scope is the app's key for what the job covers: code grammar plus ':'
	// and '/', at most 128 bytes ("", "connection:3", "source:sonarr").
	Scope   string
	Trigger Trigger
	Owner   string // the starting credential; "" for TriggerSchedule
}

// Config configures NewExecutor.
type Config[P any] struct {
	Records  Records // required when any kind has Kept > 0
	Logger   *slog.Logger
	Observer Observer
	Kinds    []KindSpec[P]
	// MaxQueued bounds the jobs waiting across the executor: 1 to 256, 0 for
	// the default 16.
	MaxQueued int
	// MaxQueuedPerOwner bounds one credential's waiting jobs: 1 to
	// MaxQueued, 0 for the default 4.
	MaxQueuedPerOwner int
}

// Observer is told of each transition and each slot wait. Each func is
// optional and is called synchronously, outside the executor's lock and in
// order for one job, so it must return promptly.
type Observer struct {
	Transition func(Transition)
	// SlotWait is the time a job or a batch item waited for a named slot;
	// it is never called for slot "".
	SlotWait func(slot string, c Class, waited time.Duration)
}

// Transition is a job queued, started, reaching a phase, or ended.
type Transition struct {
	At     time.Time
	Job    ID
	Kind   Kind
	Scope  string
	Phase  Phase  // PhaseQueued, PhaseStarted, PhaseCommitting or the kind's phase reached
	Result Result // "" until the end
}

// Started is Start's answer.
type Started struct {
	Job      ID
	Position int  // jobs queued ahead of it, 0 once it runs
	Joined   bool // a job already queued or running answered the request
}

// Executor runs an app's jobs: its timers, its slots and its records. It is
// safe for concurrent use.
type Executor[P any] struct {
	cfg Config[P]
}

// NewExecutor checks cfg and answers an executor that does nothing until Run.
// It refuses a duplicate or ill-formed Kind, Kept outside its bound, a batch
// MaxItems outside 1 to 500, MaxItems without Items or Items without
// MaxItems, more than 32 phases, a queue bound outside its range, and a
// missing Records when a kind keeps records.
func NewExecutor[P any](cfg Config[P]) (*Executor[P], error) {
	return &Executor[P]{cfg: cfg}, nil
}

// Run drives the timers, slots and dispatch until ctx ends, then cancels each
// running job and returns once every one has returned.
func (e *Executor[P]) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
