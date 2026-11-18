package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/go/health/v2"
)

// KindSpec declares one kind of job: where it runs, what it records and the
// work it does.
type KindSpec[P any] struct {
	// Fresh is optional: a new request for the scope joins a run that started
	// less than Fresh(scope) ago, and is refused with *RecentError after a
	// run that ended less than that ago, cancelled and interrupted runs
	// aside. The executor calls it holding its lock, so it must not call the
	// executor.
	Fresh func(scope string) time.Duration
	// Covers is optional: when a job of this kind starts, each queued or
	// yielded job it covers, its record being written included, ends without
	// running its own work, and its moving end moves the deadline of each
	// scheduled scope whose timer job it covers. For that deadline it is
	// asked with started's Kind and Scope only. The executor calls it holding
	// its lock, so it must not call the executor.
	Covers func(started, other Job[P]) bool
	// Items is a batch kind's item count, read from the payload at Start;
	// required with MaxItems > 0 and refused without it.
	Items func(p P) int
	// Watch answers the lease a scheduled scope of this kind must end a job
	// by, given the scope's interval. Schedule calls it each time it is given
	// a scheduled cadence, outside the executor's lock; nil is {Interval:
	// every, Cycles: 3}, and a lease whose Duration() is 0 makes Schedule
	// answer ErrLease.
	Watch func(every time.Duration) health.Lease
	// Run does the job's work and must return once ctx is done.
	Run  func(ctx context.Context, r *Run[P]) End
	Kind Kind
	// Slot "" runs at once beside anything; a named slot runs one job at a
	// time, in class order.
	Slot   string
	Phases []Phase // the pipeline, for step N of M; each phase once, at most 32
	// Kept is how long an ended record is kept: 0 writes no record, else 15
	// minutes to 400 days.
	Kept     time.Duration
	MaxItems int  // a batch kind's most items per job, 1 to 500; 0 for a kind that is not a batch
	OnBehalf bool // the job writes for its starting credential, so CredentialEnded stops it
	// Proposes marks a kind whose result is a proposal: its Scope is the
	// proposal key, expectation included, so Start answers the owner's held
	// proposal of that scope without running again, and its jobs coalesce
	// only with the same owner's.
	Proposes bool
}

// Job is one request for work.
type Job[P any] struct {
	Payload P
	// Key is a keyed start's idempotency slot, written in the record's insert
	// so a resend after a crash answers the stored job (Start); nil otherwise.
	Key  *Key
	ID   ID // set by Start
	Kind Kind
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

// Observer is told of each transition, each slot wait and each move of a
// live count. Each func is optional, is called outside the executor's lock
// and must return promptly; Transition and SlotWait are called synchronously
// and in order for one job.
type Observer struct {
	Transition func(Transition)
	// SlotWait is the time a job or a batch item waited for a named slot;
	// it is never called for slot "".
	SlotWait func(slot string, c Class, waited time.Duration)
	// Moved is called when a count Live answers moved: a Progress, a queue
	// position, a yield or its wake. It is called at most once a second
	// while counts move, with one trailing call after the last move, from
	// one goroutine Run starts for it, and never once Run has returned.
	Moved func()
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
	records Records
	log     *slog.Logger
	kinds   map[Kind]*KindSpec[P]
	slots   map[string]*slot
	owners  map[string]int // waiting jobs per credential
	// recent is when each Fresh kind's scope may run again after its last
	// moving end (rule 4); an entry goes once its time has passed.
	recent    map[ScheduledScope]time.Time
	proposals map[ProposalID]*proposal
	scheduled map[ScheduledScope]*timed
	seen      map[ScheduledScope]struct{} // every scope scheduled in this executor's life
	// ends is each scope's last moving end, read from Records at Run and
	// kept from each later end.
	ends map[ScheduledScope]LastEnd
	// noted is each scope's last moving end noted during a resume's LastEnds
	// read, nil outside one: resume drops every other end it held.
	noted map[ScheduledScope]LastEnd
	// countFrom is when Run started or the last hold resumed: a scheduled
	// scope with no last end counts from it.
	countFrom time.Time
	ready     chan struct{} // closed by Run
	stop      chan struct{} // closed once Run's context ended
	kick      chan struct{} // a Live count changed; relayMoves drains it
	observer  Observer
	live      []*job[P] // admitting, queued and running jobs, in admission order
	wg        sync.WaitGroup
	mu        sync.Mutex

	maxQueued, maxPerOwner int
	waiting                int
	holds                  int    // Hold(all) calls not yet resumed
	readGen                uint64 // moved by each hold, so a superseded resume read stops
	started, closed        bool
	endsRead               bool // Run has read Records.LastEnds, so timers may be armed
	stale                  bool // the last resume's LastEnds read has not answered yet
}

// NewExecutor checks cfg and answers an executor that does nothing until Run.
// It refuses a duplicate or ill-formed Kind, Kept outside its bound, a batch
// MaxItems outside 1 to 500, MaxItems without Items or Items without
// MaxItems, more than 32 phases or a phase listed twice, a queue bound
// outside its range, and a missing Records when a kind keeps records.
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
		recent:      map[ScheduledScope]time.Time{},
		proposals:   map[ProposalID]*proposal{},
		scheduled:   map[ScheduledScope]*timed{},
		seen:        map[ScheduledScope]struct{}{},
		ends:        map[ScheduledScope]LastEnd{},
		ready:       make(chan struct{}),
		stop:        make(chan struct{}),
		kick:        make(chan struct{}, 1),
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
			e.slots[k.Slot] = newSlot(e.moved)
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
	case k.Kept != 0 && (k.Kept < minKept || k.Kept > maxKept):
		return fmt.Errorf("kept %v, want 0 or %v to %v", k.Kept, minKept, maxKept)
	case k.MaxItems < 0 || k.MaxItems > maxBatchItems:
		return fmt.Errorf("MaxItems %d outside 0 to %d", k.MaxItems, maxBatchItems)
	case k.MaxItems > 0 && k.Items == nil:
		return errors.New("a batch (MaxItems > 0) with no Items")
	case k.MaxItems == 0 && k.Items != nil:
		return errors.New("an item count (Items) on a kind that is not a batch")
	}
	return checkPhases(k.Phases)
}

func checkPhases(phases []Phase) error {
	if len(phases) > maxPhases {
		return fmt.Errorf("%d phases, more than %d", len(phases), maxPhases)
	}
	for i, p := range phases {
		if !validCode(string(p)) {
			return fmt.Errorf("phase %q outside code grammar", p)
		}
		if slices.Contains(phases[:i], p) {
			return fmt.Errorf("phase %q listed twice", p)
		}
	}
	return nil
}

// Run drives the timers, slots and dispatch until ctx ends, then cancels each
// running job and returns once every one has returned. A job it cut short
// ends interrupted, unless its end committed with its work or it was in its
// commit step, and a queued job ends interrupted without running. It reads
// Records.LastEnds first, retrying a failed read after 1, 2, 4 and 8 minutes
// and then every 15; no scheduled scope wakes before a read answers, and
// each deadline counts from Run's start meanwhile. A second call answers an
// error at once.
func (e *Executor[P]) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("scheduler: Run called twice")
	}
	e.started = true
	close(e.ready)
	e.mu.Unlock()
	stopMoves := make(chan struct{})
	var relay sync.WaitGroup
	if e.observer.Moved != nil {
		relay.Go(func() { e.relayMoves(stopMoves) })
	}
	e.readEnds(ctx, time.Now())

	<-ctx.Done()
	e.mu.Lock()
	e.closed = true
	close(e.stop)
	e.stopTimersLocked()
	var dequeued []*job[P]
	for _, en := range slices.Clone(e.live) {
		switch en.state {
		case stateQueued:
			e.dequeueLocked(en, End{Result: ResultInterrupted})
			dequeued = append(dequeued, en)
		case stateRunning:
			// A job in its commit step finishes and keeps its own end.
			if !en.committing && !en.returned {
				e.stopLocked(en, End{Result: ResultInterrupted})
			}
		case stateAdmitting, stateEnded:
		}
	}
	e.mu.Unlock()
	for _, en := range dequeued {
		e.ended(ctx, en, *en.stopEnd)
	}
	e.wg.Wait()
	close(stopMoves)
	relay.Wait()
	return nil
}

type jobState uint8

const (
	stateAdmitting jobState = iota // its record is being written
	stateQueued
	stateRunning
	stateEnded
)

// job is one admitted job. state, cancel, startedAt, stopEnd, stopped,
// returned, committing, yield, proposal, the counts and w's ownership are
// guarded by the executor's mu; rec, claimed and factsDropped are guarded by recMu once the
// job runs, which also orders its record writes.
type job[P any] struct {
	spec      *KindSpec[P]
	slot      *slot
	w         *waiter            // its take of a named slot; nil when it waits for none
	admitted  chan struct{}      // closed once its record is written or refused
	gone      chan struct{}      // closed when it leaves the queue without running
	finished  chan struct{}      // closed once it ran and its end is written
	cancel    context.CancelFunc // set when it runs
	startedAt time.Time          // when it took its slot
	// stopEnd is the end a cancel, a covering start or a shutdown gave it,
	// written in place of its Run's; nil when nothing stopped it.
	stopEnd     *End
	claimed     []bool    // a batch's lines an Item call has taken
	yield       *yielding // its Yield's wait; nil when it is not yielded
	proposal    *proposal // its Run.Propose's; nil until it proposes
	relay       relay
	j           Job[P]
	rec         Record
	done, total int // Run.Progress's counts
	recMu       sync.Mutex
	class       Class
	state       jobState
	stopped     bool // its ctx was cancelled, so Commit answers false
	returned    bool // its Run returned, so a cancel has nothing left to stop
	committing  bool // Commit answered true, so Cancel answers ErrCommitting
	inserted    bool // its admission wrote its record and queued it
	// factsDropped is a line's facts dropped, so the end carries
	// facts_too_large.
	factsDropped bool
}

// stopLocked cancels a running job's ctx; it then commits nothing and ends
// with end, whatever its Run returns.
func (e *Executor[P]) stopLocked(en *job[P], end End) {
	en.stopped = true
	en.stopEnd = &end
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
	now := time.Now()
	en.startedAt = now
	ctx, cancel := context.WithCancel(context.Background())
	en.cancel = cancel
	covered := e.coverLocked(en)
	e.mu.Unlock()
	defer cancel()

	if en.w != nil && e.observer.SlotWait != nil {
		e.observer.SlotWait(en.spec.Slot, en.class, en.slot.waited(en.w))
	}
	en.recMu.Lock()
	en.rec.State, en.rec.StartedAt, en.rec.Phase = StateRunning, now, PhaseStarted
	e.put(en)
	e.post(en, &Transition{Phase: PhaseStarted, At: now})
	en.recMu.Unlock()
	e.flush(en)
	for _, q := range covered {
		e.ended(context.Background(), q, *q.stopEnd)
	}
	e.finish(en, e.guarded(ctx, en))
}

// coverLocked takes out of the queue each queued job the starting job's kind
// covers, each to end skipped as covered by it, and answers them; one whose
// record is being written gets that end for admit to write, and a yielded
// job it covers has its wait ended.
func (e *Executor[P]) coverLocked(en *job[P]) []*job[P] {
	if en.spec.Covers == nil {
		return nil
	}
	e.coverYieldedLocked(en)
	end := End{Result: ResultSkipped, Reasons: []Reason{{Code: ReasonCovered, Subject: string(en.j.ID)}}}
	var covered []*job[P]
	for _, q := range e.live {
		switch {
		case q.state == stateQueued && en.spec.Covers(en.j, q.j):
			covered = append(covered, q)
		case q.state == stateAdmitting && q.stopEnd == nil && en.spec.Covers(en.j, q.j):
			stop := end
			q.stopEnd = &stop
		}
	}
	for _, q := range covered {
		e.dequeueLocked(q, end)
	}
	return covered
}

// finish writes a run job's end, frees its slot and reports the stored end.
// A job a cancel stopped ends as the cancel said, whatever its Run returned.
func (e *Executor[P]) finish(en *job[P], end End) {
	e.mu.Lock()
	en.returned = true
	if en.stopEnd != nil {
		end = *en.stopEnd
	}
	e.mu.Unlock()
	stored, at, kept := e.end(context.Background(), en, e.bounded(en, end))
	e.mu.Lock()
	en.state = stateEnded
	e.removeLocked(en)
	// An end the store does not hold moves nothing a restart would not read.
	if kept && moving(stored.Result) {
		e.noteEndLocked(en, stored.Result, at)
		if en.spec.Fresh != nil {
			e.noteRecentLocked(en, at)
		}
	}
	e.keepLocked(en, at)
	e.mu.Unlock()
	if en.w != nil {
		en.slot.drop(en.w)
	}
	en.recMu.Lock()
	phase := en.rec.Phase
	en.recMu.Unlock()
	e.emit(en, &Transition{Phase: phase, Result: stored.Result, At: at})
	close(en.finished)
}

// end writes en's end and answers the stored one, which an end the job's own
// commit wrote wins over, and its time. kept is false when the store refused
// the write, leaving the record open for its next open to mark.
func (e *Executor[P]) end(ctx context.Context, en *job[P], end End) (stored End, at time.Time, kept bool) {
	end = e.closeLines(en, end)
	if en.spec.Kept == 0 {
		return end, time.Now(), true
	}
	rec, err := e.records.End(context.WithoutCancel(ctx), en.j.ID, end)
	if err != nil || rec.End == nil {
		e.log.Error("job end not recorded", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID, "err", err)
		return end, time.Now(), false
	}
	return *rec.End, rec.EndedAt, true
}

// put writes the job's record after a change.
func (e *Executor[P]) put(en *job[P]) {
	if en.spec.Kept == 0 {
		return
	}
	if err := e.records.Put(context.Background(), en.rec); err != nil {
		e.log.Error("job record not written", "kind", en.j.Kind, "scope", en.j.Scope, "job", en.j.ID, "err", err)
	}
}

// moving reports whether the end of a job that started moves its scope's
// last end: a cancel or a cut-short run did not finish its work.
func moving(r Result) bool { return r != ResultCancelled && r != ResultInterrupted }

// noteRecentLocked opens the Fresh window of en's scope from its end at at,
// and drops the windows that have closed.
func (e *Executor[P]) noteRecentLocked(en *job[P], at time.Time) {
	now := time.Now()
	maps.DeleteFunc(e.recent, func(_ ScheduledScope, until time.Time) bool { return !until.After(now) })
	if until := at.Add(en.spec.Fresh(en.j.Scope)); until.After(now) {
		e.recent[ScheduledScope{Kind: en.j.Kind, Scope: en.j.Scope}] = until
	}
}

// dequeueLocked takes a queued job out of the queue without running it, to
// end with end. It stays live, so a repeat Cancel finds it, until ended has
// written that end.
func (e *Executor[P]) dequeueLocked(en *job[P], end End) {
	e.unwaitLocked(en)
	en.state = stateEnded
	en.stopEnd = &end
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
