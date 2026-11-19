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
	// running its own work. Its moving end also counts for each scheduled
	// scope whose timer job it covers when later than the scope's own end;
	// for that it is asked with started's Kind and Scope only. The executor
	// calls it holding its lock, so it must not call the executor.
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
	// Slot "" runs beside any other job; a named slot runs one job at a time,
	// in class order. Either way a follow-up waits for the running job of its
	// scope.
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
	// ends is each kind and scope's last moving end, as Records.LastEnds
	// answers it: read at Run and at each resume, kept from each later end,
	// at most maxEnds.
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
	freed     chan struct{} // closed and replaced by freeLocked
	// idle closes once no record write writeLocked counted is in flight; a
	// hold and a shutdown wait on it, since those writes run off any job's run.
	idle     chan struct{}
	observer Observer
	live     []*job[P] // admitting, queued and running jobs, in admission order
	wg       sync.WaitGroup
	mu       sync.Mutex

	maxQueued, maxPerOwner int
	waiting                int
	holds                  int    // Hold(all) calls not yet resumed
	writing                int    // record writes counted by writeLocked, in flight
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
		freed:       make(chan struct{}),
		idle:        make(chan struct{}),
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
	close(e.idle)
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
// running job and returns once every one has returned and each record write
// in flight has landed. A job it cut short ends interrupted, unless its end
// committed with its work or it was in its commit step; a queued job, or one
// whose record was being written, ends interrupted without running. It reads
// Records.LastEnds first, retrying a failed read after 1, 2, 4, 8 minutes and
// then every 15; no scheduled scope wakes before a read answers, and each
// deadline counts from Run's start meanwhile. A second call errors at once.
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
	e.writeLocked()
	idle := e.idle
	e.mu.Unlock()
	e.endDequeued(ctx, dequeued)
	<-idle
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
// guarded by the executor's mu; rec, claimed and unwritten are guarded by
// recMu once the job runs, which also orders its record writes.
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
	unwritten   bool // its last record write failed
}

// stopLocked cancels a running job's ctx; it then commits nothing and ends
// with end, whatever its Run returns.
func (e *Executor[P]) stopLocked(en *job[P], end End) {
	en.stopped = true
	en.stopEnd = &end
	en.cancel()
	e.freeLocked()
}

// serves reports whether the job is the running job rule 3 reads for j: of
// j's scope (sameScope), running, not stopped, and not yielded unless its
// kind proposes, since a yielded proposing job still holds its key.
func (en *job[P]) serves(j *Job[P]) bool {
	return en.state == stateRunning && !en.stopped && (en.yield == nil || en.spec.Proposes) && en.sameScope(j)
}

// freeLocked wakes each follow-up waiting in begin; it is called whenever a
// job stops serving its scope: it yields, is stopped or ends.
func (e *Executor[P]) freeLocked() {
	close(e.freed)
	e.freed = make(chan struct{})
}

// behindLocked answers the channel to wait on while a running job serves
// en's scope, which en, its follow-up (rule 3), runs after; nil when none
// does. A named slot already orders them; a batch or a kind with no slot does
// not.
func (e *Executor[P]) behindLocked(en *job[P]) <-chan struct{} {
	for _, q := range e.live {
		if q.serves(&en.j) {
			return e.freed
		}
	}
	return nil
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

// begin runs a job that holds its slot once no running job serves its scope,
// unless it left the queue first.
func (e *Executor[P]) begin(en *job[P]) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	covered, ok := e.starting(en, cancel)
	if !ok {
		return
	}

	if en.w != nil && e.observer.SlotWait != nil {
		e.observer.SlotWait(en.spec.Slot, en.class, en.slot.waited(en.w))
	}
	now := en.startedAt
	e.write(en, &Transition{Phase: PhaseStarted, At: now}, func(rec *Record) bool {
		rec.State, rec.StartedAt, rec.Phase = StateRunning, now, PhaseStarted
		return true
	})
	e.flush(en)
	for _, q := range covered {
		e.ended(context.Background(), q, *q.stopEnd)
	}
	e.finish(en, e.guarded(ctx, en))
}

// starting waits until no running job serves en's scope, then marks it
// running with cancel as its stop and takes out the queued jobs it covers;
// false when en left the queue first. mu is released by defer, since Covers
// is called under it.
func (e *Executor[P]) starting(en *job[P], cancel context.CancelFunc) (covered []*job[P], ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for en.state == stateQueued {
		freed := e.behindLocked(en)
		if freed == nil {
			break
		}
		e.mu.Unlock()
		select {
		case <-freed:
		case <-en.gone:
		}
		e.mu.Lock()
	}
	if en.state != stateQueued {
		return nil, false
	}
	e.unwaitLocked(en)
	en.state = stateRunning
	en.startedAt = time.Now()
	en.cancel = cancel
	return e.coverLocked(en), true
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
	stored, at, kept := e.end(context.Background(), en, e.returnedEnd(en, end))
	e.retire(en, stored.Result, at, kept)
	if en.w != nil {
		en.slot.drop(en.w)
	}
	en.recMu.Lock()
	phase := en.rec.Phase
	en.recMu.Unlock()
	e.emit(en, &Transition{Phase: phase, Result: stored.Result, At: at})
	close(en.finished)
}

// returnedEnd marks en's Run returned and answers the end to write: a
// cancel's, a covering start's or a shutdown's when one stopped it, else
// end.
func (e *Executor[P]) returnedEnd(en *job[P], end End) End {
	e.mu.Lock()
	defer e.mu.Unlock()
	en.returned = true
	if en.stopEnd != nil {
		return *en.stopEnd
	}
	return end
}

// retire takes the run job en, whose end r was written at at, out of the
// live jobs and frees its scope. mu is released by defer, since Covers and
// Fresh are called under it.
func (e *Executor[P]) retire(en *job[P], r Result, at time.Time, kept bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en.state = stateEnded
	e.removeLocked(en)
	e.freeLocked()
	// An end the store does not hold moves nothing a restart would not read.
	if kept && moving(r) {
		e.noteEndLocked(en, r, at)
		if en.spec.Fresh != nil {
			e.noteRecentLocked(en, at)
		}
	}
	e.releaseStandInLocked(en.j.ID, at)
	e.keepLocked(en, at)
}

// end writes en's end and answers the stored one, which an end the job's own
// commit wrote wins over, and its time. kept is false when the store refused
// the write of the end or of its closed lines, leaving the record open for
// its next open to mark.
func (e *Executor[P]) end(ctx context.Context, en *job[P], end End) (stored End, at time.Time, kept bool) {
	end, written := e.closeEnd(en, end)
	if !written {
		return end, time.Now(), false
	}
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

// write applies change to en's record and, when change answers true, writes
// the record and posts tr unless it is nil; the caller flushes after. recMu
// is released by defer: a panic out of Records.Put on Run's stack must leave
// it free for finish's closeEnd.
func (e *Executor[P]) write(en *job[P], tr *Transition, change func(rec *Record) bool) bool {
	en.recMu.Lock()
	defer en.recMu.Unlock()
	if !change(&en.rec) {
		return false
	}
	e.put(en)
	if tr != nil {
		e.post(en, tr)
	}
	return true
}

// put writes the job's record after a change and logs a failed write, which
// leaves the record unwritten until a later put lands. It is called holding
// en's recMu.
func (e *Executor[P]) put(en *job[P]) {
	if en.spec.Kept == 0 {
		return
	}
	en.unwritten = true // until Put answers, so closeEnd writes again after a panic out of it
	err := e.records.Put(context.Background(), en.rec)
	en.unwritten = err != nil
	if err != nil {
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

// writeLocked counts a record write made off a job's run (an admission, a
// refusal, a dequeued job's end); wrote ends it.
func (e *Executor[P]) writeLocked() {
	if e.writing == 0 {
		e.idle = make(chan struct{})
	}
	e.writing++
}

func (e *Executor[P]) wrote() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writing--
	if e.writing == 0 {
		close(e.idle)
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
	delivered := false
	defer func() {
		// A Transition panic Run's recover takes must not leave busy set, or
		// no later flush delivers the job's end.
		if !delivered {
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
		}
	}()
	for len(r.pending) > 0 {
		tr := r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
		e.observer.Transition(tr)
		r.mu.Lock()
	}
	r.busy = false
	delivered = true
	r.mu.Unlock()
}
