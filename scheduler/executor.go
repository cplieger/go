package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
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
	// yielded job it covers ends without running its own work. The executor
	// calls it holding its lock, so it must not call the executor.
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

// The bounds NewExecutor checks.
const (
	maxPhases         = 32
	maxBatchItems     = 500
	minKept           = 15 * time.Minute
	maxKept           = 400 * 24 * time.Hour
	defaultMaxQueued  = 16
	maxMaxQueued      = 256
	defaultQueuedEach = 4
)

// Executor runs an app's jobs: its timers, its slots and its records. It is
// safe for concurrent use.
type Executor[P any] struct {
	records  Records
	log      *slog.Logger
	kinds    map[Kind]*KindSpec[P]
	slots    map[string]*slot
	owners   map[string]int // waiting jobs per credential
	ready    chan struct{}  // closed by Run
	observer Observer
	live     []*job[P] // admitting, queued and running jobs, in admission order
	wg       sync.WaitGroup
	mu       sync.Mutex

	maxQueued, maxPerOwner int
	waiting                int
	started, closed        bool
}

// NewExecutor checks cfg and answers an executor that does nothing until Run.
// It refuses a duplicate or ill-formed Kind, Kept outside its bound, a batch
// MaxItems outside 1 to 500, MaxItems without Items or Items without
// MaxItems, more than 32 phases, a queue bound outside its range, and a
// missing Records when a kind keeps records.
func NewExecutor[P any](cfg Config[P]) (*Executor[P], error) {
	maxQueued := cmp.Or(cfg.MaxQueued, defaultMaxQueued)
	if maxQueued < 1 || maxQueued > maxMaxQueued {
		return nil, fmt.Errorf("scheduler: MaxQueued %d outside 1 to %d", cfg.MaxQueued, maxMaxQueued)
	}
	perOwner := cmp.Or(cfg.MaxQueuedPerOwner, min(defaultQueuedEach, maxQueued))
	if perOwner < 1 || perOwner > maxQueued {
		return nil, fmt.Errorf("scheduler: MaxQueuedPerOwner %d outside 1 to %d", cfg.MaxQueuedPerOwner, maxQueued)
	}
	e := &Executor[P]{
		records:     cfg.Records,
		log:         cmp.Or(cfg.Logger, slog.Default()),
		kinds:       map[Kind]*KindSpec[P]{},
		slots:       map[string]*slot{},
		owners:      map[string]int{},
		ready:       make(chan struct{}),
		observer:    cfg.Observer,
		maxQueued:   maxQueued,
		maxPerOwner: perOwner,
	}
	for i := range cfg.Kinds {
		k := cfg.Kinds[i]
		if err := k.check(); err != nil {
			return nil, fmt.Errorf("scheduler: kind %q: %w", k.Kind, err)
		}
		if _, dup := e.kinds[k.Kind]; dup {
			return nil, fmt.Errorf("scheduler: kind %q declared twice", k.Kind)
		}
		if k.Kept > 0 && cfg.Records == nil {
			return nil, fmt.Errorf("scheduler: kind %q keeps records and Config.Records is nil", k.Kind)
		}
		k.Phases = slices.Clone(k.Phases)
		e.kinds[k.Kind] = &k
		if k.Slot != "" && e.slots[k.Slot] == nil {
			e.slots[k.Slot] = newSlot()
		}
	}
	return e, nil
}

func (k *KindSpec[P]) check() error {
	switch {
	case !validCode(string(k.Kind)):
		return errors.New("kind outside code grammar")
	case k.Run == nil:
		return errors.New("no Run")
	case len(k.Phases) > maxPhases:
		return fmt.Errorf("%d phases, more than %d", len(k.Phases), maxPhases)
	case k.Kept != 0 && (k.Kept < minKept || k.Kept > maxKept):
		return fmt.Errorf("kept %v, want 0 or %v to %v", k.Kept, minKept, maxKept)
	case k.MaxItems < 0 || k.MaxItems > maxBatchItems:
		return fmt.Errorf("MaxItems %d outside 0 to %d", k.MaxItems, maxBatchItems)
	case k.MaxItems > 0 && k.Items == nil:
		return errors.New("a batch (MaxItems > 0) with no Items")
	case k.MaxItems == 0 && k.Items != nil:
		return errors.New("an item count (Items) on a kind that is not a batch")
	}
	for _, p := range k.Phases {
		if !validCode(string(p)) {
			return fmt.Errorf("phase %q outside code grammar", p)
		}
	}
	return nil
}

// Run drives the timers, slots and dispatch until ctx ends, then cancels each
// running job and returns once every one has returned. A second call answers
// an error at once.
func (e *Executor[P]) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("scheduler: Run called twice")
	}
	e.started = true
	close(e.ready)
	e.mu.Unlock()

	<-ctx.Done()
	e.mu.Lock()
	e.closed = true
	for _, en := range slices.Clone(e.live) {
		switch en.state {
		case stateQueued:
			e.dequeueLocked(en)
		case stateRunning:
			e.stopLocked(en)
		case stateAdmitting, stateEnded:
		}
	}
	e.mu.Unlock()
	e.wg.Wait()
	return nil
}

type jobState uint8

const (
	stateAdmitting jobState = iota // its record is being written
	stateQueued
	stateRunning
	stateEnded
)

// job is one admitted job. state, cancel, stopped, committing, the counts and
// w's ownership are guarded by the executor's mu; rec is guarded by recMu once
// the job runs, which also orders its record writes.
type job[P any] struct {
	spec        *KindSpec[P]
	slot        *slot
	w           *waiter            // its take of a named slot; nil when it waits for none
	admitted    chan struct{}      // closed once its record is written or refused
	gone        chan struct{}      // closed when it leaves the queue without running
	cancel      context.CancelFunc // set when it runs
	relay       relay
	j           Job[P]
	rec         Record
	done, total int // Run.Progress's counts
	recMu       sync.Mutex
	class       Class
	state       jobState
	stopped     bool // its ctx was cancelled, so Commit answers false
	committing  bool // Commit answered true, so Cancel answers ErrCommitting
}

// stopLocked cancels a running job's ctx; it then commits nothing.
func (e *Executor[P]) stopLocked(en *job[P]) {
	en.stopped = true
	en.cancel()
}

// waits reports whether the job takes its kind's slot before its Run starts:
// a batch's items take it instead.
func (en *job[P]) waits() bool { return en.slot != nil && en.spec.MaxItems == 0 }

// position counts the jobs its slot serves before it.
func (en *job[P]) position() int {
	if en.w == nil {
		return 0
	}
	return en.slot.ahead(en.w)
}

// classOf is a job's class: Automatic for the timer's, else Interactive.
func classOf(t Trigger) Class {
	if t == TriggerSchedule {
		return Automatic
	}
	return Interactive
}

// await waits for Run and the job's slot, then runs it; a job that leaves
// the queue first runs nothing.
func (e *Executor[P]) await(en *job[P]) {
	defer e.wg.Done()
	select {
	case <-e.ready:
	case <-en.gone:
		return
	}
	if en.w != nil {
		select {
		case <-en.w.ready:
		case <-en.gone:
			return
		}
	}
	e.begin(en)
}

// begin runs a job that holds its slot, unless it left the queue as its
// grant came.
func (e *Executor[P]) begin(en *job[P]) {
	e.mu.Lock()
	if en.state != stateQueued {
		e.mu.Unlock()
		return
	}
	e.unwaitLocked(en)
	en.state = stateRunning
	ctx, cancel := context.WithCancel(context.Background())
	en.cancel = cancel
	e.mu.Unlock()
	defer cancel()

	if en.w != nil && e.observer.SlotWait != nil {
		e.observer.SlotWait(en.spec.Slot, en.class, en.slot.waited(en.w))
	}
	now := time.Now()
	en.recMu.Lock()
	en.rec.State, en.rec.StartedAt, en.rec.Phase = StateRunning, now, PhaseStarted
	e.put(en)
	e.post(en, &Transition{Phase: PhaseStarted, At: now})
	en.recMu.Unlock()
	e.flush(en)
	e.finish(en, e.guarded(ctx, en))
}

// finish writes a run job's end, frees its slot and reports the stored end.
func (e *Executor[P]) finish(en *job[P], end End) {
	stored, at := e.end(context.Background(), en, e.bounded(en, end))
	e.mu.Lock()
	en.state = stateEnded
	e.removeLocked(en)
	e.mu.Unlock()
	if en.w != nil {
		en.slot.drop(en.w)
	}
	en.recMu.Lock()
	phase := en.rec.Phase
	en.recMu.Unlock()
	e.emit(en, &Transition{Phase: phase, Result: stored.Result, At: at})
}

// end writes en's end and answers the stored one, which an end the job's own
// commit wrote wins over, and its time.
func (e *Executor[P]) end(ctx context.Context, en *job[P], end End) (End, time.Time) {
	if en.spec.Kept == 0 {
		return end, time.Now()
	}
	rec, err := e.records.End(context.WithoutCancel(ctx), en.j.ID, end)
	if err != nil || rec.End == nil {
		e.log.Error("job end not recorded", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID, "err", err)
		return end, time.Now()
	}
	return *rec.End, rec.EndedAt
}

// put writes the job's record after a change.
func (e *Executor[P]) put(en *job[P]) {
	if en.spec.Kept == 0 {
		return
	}
	en.rec.Rev++
	if err := e.records.Put(context.Background(), en.rec); err != nil {
		e.log.Error("job record not written", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID, "err", err)
	}
}

// dequeueLocked takes a queued job out of the queue without running it; its
// record is the caller's to end.
func (e *Executor[P]) dequeueLocked(en *job[P]) {
	e.unwaitLocked(en)
	en.state = stateEnded
	e.removeLocked(en)
	close(en.gone)
	if en.w != nil {
		en.slot.drop(en.w)
	}
}

func (e *Executor[P]) waitLocked(en *job[P]) {
	e.waiting++
	if o := en.j.Owner; o != "" {
		e.owners[o]++
	}
}

// unwaitLocked frees an admitting or queued job's place in the bounds.
func (e *Executor[P]) unwaitLocked(en *job[P]) {
	if en.state != stateAdmitting && en.state != stateQueued {
		return
	}
	e.waiting--
	if o := en.j.Owner; o != "" {
		e.owners[o]--
		if e.owners[o] == 0 {
			delete(e.owners, o)
		}
	}
}

func (e *Executor[P]) removeLocked(en *job[P]) {
	if i := slices.Index(e.live, en); i >= 0 {
		e.live = slices.Delete(e.live, i, i+1)
	}
}

func (e *Executor[P]) findLocked(id ID) *job[P] {
	for _, en := range e.live {
		if en.j.ID == id {
			return en
		}
	}
	return nil
}

// relay delivers one job's transitions in order without holding a lock
// across the Observer: a transition posted while another is being delivered
// is delivered by that caller after it.
type relay struct {
	pending []Transition
	mu      sync.Mutex
	busy    bool
}

// emit posts tr for en and delivers what is pending.
func (e *Executor[P]) emit(en *job[P], tr *Transition) {
	e.post(en, tr)
	e.flush(en)
}

func (e *Executor[P]) post(en *job[P], tr *Transition) {
	if e.observer.Transition == nil {
		return
	}
	tr.Job, tr.Kind, tr.Scope = en.j.ID, en.j.Kind, en.j.Scope
	en.relay.mu.Lock()
	defer en.relay.mu.Unlock()
	en.relay.pending = append(en.relay.pending, *tr)
}

func (e *Executor[P]) flush(en *job[P]) {
	r := &en.relay
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return
	}
	r.busy = true
	for len(r.pending) > 0 {
		tr := r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
		e.observer.Transition(tr)
		r.mu.Lock()
	}
	r.busy = false
	r.mu.Unlock()
}
