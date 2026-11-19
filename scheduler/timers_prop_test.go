package scheduler_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

var errEndRefused = errors.New("end refused")

// heldEnd is a memrecords store whose End can hold one job's end after
// writing it until release closes, so the executor notes it after later ones,
// or refuse one job's end once, writing nothing.
type heldEnd struct {
	*memrecords.Store
	release chan struct{}
	refuse  map[scheduler.ID]bool // ends to refuse once
	refused map[scheduler.ID]bool // ends refused
	id      scheduler.ID
	mu      sync.Mutex
	waiting bool // the held End wrote its end and waits for release
}

func (s *heldEnd) End(ctx context.Context, id scheduler.ID, end scheduler.End) (scheduler.Record, error) {
	s.mu.Lock()
	if s.refuse[id] {
		delete(s.refuse, id)
		s.refused[id] = true
		s.mu.Unlock()
		return scheduler.Record{}, errEndRefused
	}
	s.mu.Unlock()
	rec, err := s.Store.End(ctx, id, end)
	s.mu.Lock()
	var release chan struct{}
	if id != "" && id == s.id {
		release, s.waiting = s.release, true
	}
	s.mu.Unlock()
	if release != nil {
		<-release
	}
	return rec, err
}

// holdNext holds the End of id.
func (s *heldEnd) holdNext(id scheduler.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.release, s.waiting = id, make(chan struct{}), false
}

// refuseNext refuses the next End of id.
func (s *heldEnd) refuseNext(id scheduler.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse[id] = true
}

func (s *heldEnd) wasRefused(id scheduler.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[id]
}

// let releases a held End, if any; the caller waits for its note.
func (s *heldEnd) let() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		close(s.release)
	}
	s.id, s.release, s.waiting = "", nil, false
}

func (s *heldEnd) holding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id != ""
}

var errReadOnly = errors.New("read-only store")

// readOnly reads a store and writes nothing, so a restarted executor that
// only reads its timing leaves the store as it found it.
type readOnly struct{ *memrecords.Store }

func (readOnly) Insert(context.Context, scheduler.Record, *scheduler.Key) (scheduler.Record, bool, error) {
	return scheduler.Record{}, false, errReadOnly
}

func (readOnly) Put(context.Context, scheduler.Record) error { return errReadOnly }

func (readOnly) End(context.Context, scheduler.ID, scheduler.End) (scheduler.Record, error) {
	return scheduler.Record{}, errReadOnly
}

// diffResults are the results a job the sequence ends picks from.
var diffResults = []scheduler.Result{scheduler.ResultOK, scheduler.ResultPartial, scheduler.ResultFailed, scheduler.ResultSkipped}

// ownAt reports whether key is own at by's scope, or any own scope when by's
// scope is "all".
func ownAt(by, key scheduler.ScheduledScope) bool {
	return key.Kind == "own" && (by.Scope == "all" || by.Scope == key.Scope)
}

// diffCovers is cover's and prop's Covers, which read Kind and Scope only.
func diffCovers(by, key scheduler.ScheduledScope) bool {
	return (by.Kind == "cover" || by.Kind == "prop") && ownAt(by, key)
}

// diffKinds is own, the covered kind, which writes for its owner; cover,
// queued in own's slot; full, in the same slot, whose Covers holds only for a
// job whose payload N is 1, so its end never moves own's base; prop, a
// proposing kind beside them; and sweep, a kind that writes no record and
// ends at once. Every kind but sweep runs run.
func diffKinds(run func(context.Context, *scheduler.Run[payload]) scheduler.End) []scheduler.KindSpec[payload] {
	scope := func(j scheduler.Job[payload]) scheduler.ScheduledScope {
		return scheduler.ScheduledScope{Kind: j.Kind, Scope: j.Scope}
	}
	covers := func(j, other scheduler.Job[payload]) bool { return diffCovers(scope(j), scope(other)) }
	own, cover, full, prop, sweep := spec("own", "s"), spec("cover", "s"), spec("full", "s"), spec("prop", ""), spec("sweep", "")
	own.OnBehalf = true
	cover.Covers, prop.Covers, prop.Proposes = covers, covers, true
	full.Covers = func(j, other scheduler.Job[payload]) bool { return j.Payload.N == 1 && ownAt(scope(j), scope(other)) }
	own.Run, cover.Run, full.Run, prop.Run = run, run, run, run
	sweep.Kept = 0
	sweep.Run = func(context.Context, *scheduler.Run[payload]) scheduler.End {
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return []scheduler.KindSpec[payload]{own, cover, full, prop, sweep}
}

// diffCadence is a cadence and the waits it was built from.
type diffCadence struct {
	c            scheduler.Cadence
	every, retry time.Duration
}

// notDone is the wait after an end whose work was not done, or after an end
// held in memory only: Retry's d, else the interval.
func (c diffCadence) notDone() time.Duration {
	if c.retry > 0 {
		return c.retry
	}
	return c.every
}

var (
	diffStarts = []scheduler.ScheduledScope{
		{Kind: "own", Scope: "a"},
		{Kind: "own", Scope: "b"},
		{Kind: "own", Scope: "c"},
		{Kind: "cover", Scope: "a"},
		{Kind: "cover", Scope: "all"},
		{Kind: "full", Scope: "a"},
		{Kind: "full", Scope: "all"},
		{Kind: "prop", Scope: "a"},
		{Kind: "prop", Scope: "all"},
		{Kind: "prop", Scope: "p1"},
		{Kind: "prop", Scope: "p2"},
	}
	diffSchedulable = []scheduler.ScheduledScope{
		{Kind: "own", Scope: "a"},
		{Kind: "own", Scope: "b"},
		{Kind: "own", Scope: "c"},
		{Kind: "prop", Scope: "a"},
		{Kind: "cover", Scope: "all"},
	}
	// The cadences are short enough for timers to fire inside a sequence.
	diffCadences = []diffCadence{
		{scheduler.Scheduled(time.Hour).Retry(10 * time.Minute), time.Hour, 10 * time.Minute},
		{scheduler.Scheduled(30 * time.Minute).Retry(5 * time.Minute), 30 * time.Minute, 5 * time.Minute},
		{scheduler.Scheduled(45 * time.Minute), 45 * time.Minute, 0},
		{scheduler.TriggerOnly(), 0, 0},
	}
	diffAdvances = []time.Duration{time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	diffOwners   = []string{"alice", "bob"}
)

// diffGate holds a running job until the sequence ends it.
type diffGate struct {
	done    chan struct{}
	job     scheduler.Job[payload]
	end     scheduler.Result
	propose bool
}

// diffJob is what the Observer told of one job.
type diffJob struct {
	endAt   time.Time
	result  scheduler.Result
	key     scheduler.ScheduledScope
	started bool
	ended   bool
}

// window is a span a scope's next wake may fall in.
type window struct {
	lo, hi time.Time
	from   string
}

func (w window) String() string {
	return fmt.Sprintf("[%s, %s] from %s", w.lo.Format(time.TimeOnly), w.hi.Format(time.TimeOnly), w.from)
}

func band(at time.Time, d time.Duration, from string) window {
	lo, hi := jitterBand(d)
	return window{lo: at.Add(lo), hi: at.Add(hi), from: from}
}

func inAny(at time.Time, ws []window) bool {
	return slices.ContainsFunc(ws, func(w window) bool { return !at.Before(w.lo) && !at.After(w.hi) })
}

// diffRun is one sequence over one store, its executor replaced at each
// shutdown, and what the sequence saw of its jobs and timers.
type diffRun struct {
	base      *rig
	r         *rig
	stop      func()
	store     *heldEnd
	scheduled map[scheduler.ScheduledScope]diffCadence
	rnd       *rand.Rand
	running   map[scheduler.ID]*diffGate // jobs whose Run waits for the sequence
	jobs      map[scheduler.ID]*diffJob
	endStep   map[scheduler.ID]int                          // the step after which the job was first seen ended
	rearmed   map[scheduler.ScheduledScope]int              // the last step that counted the scope again
	timing    map[scheduler.ScheduledScope]scheduler.Timing // the scope's timing after the last step
	windows   map[scheduler.ScheduledScope][]window         // where an idle scope's next wake may fall
	seen      map[scheduler.ID]bool                         // timer records already checked
	payloads  map[scheduler.ID]bool                         // full jobs whose payload covers own
	steps     []string
	mu        sync.Mutex
	bound     bool // the sequence passes the held ends' bound halfway
}

// TestTimingMatchesRestart drives seeded random sequences of starts, ends
// (ties, late notes, refused Ends, proposals), timer wakes joining a queued
// job whose Covers reads its payload, cancels, CredentialEnded, admission
// refusals, holds, shutdowns mid-run with or without a hold open, cadence
// changes and ends of a kind that writes no record. After each step every
// scope that counts from an end must count as a restart over the same store
// does (base, wait, deadline), and each idle scope's next wake must fall where
// a restart puts it or at an end that moved nothing for it plus notDone.
func TestTimingMatchesRestart(t *testing.T) {
	for seed := range uint64(300) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runDiff(t, seed, false) })
		})
	}
	// More ends than the executor holds, in the kind that writes no record.
	for seed := range uint64(10) {
		t.Run(fmt.Sprintf("past the held bound seed %d", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runDiff(t, seed, true) })
		})
	}
}

func runDiff(t *testing.T, seed uint64, bound bool) {
	store := &heldEnd{Store: memrecords.New(), refuse: map[scheduler.ID]bool{}, refused: map[scheduler.ID]bool{}}
	d := &diffRun{
		base:      &rig{store: store.Store, log: &runLog{}},
		store:     store,
		scheduled: map[scheduler.ScheduledScope]diffCadence{},
		rnd:       rand.New(rand.NewPCG(seed, 0)),
		running:   map[scheduler.ID]*diffGate{},
		jobs:      map[scheduler.ID]*diffJob{},
		endStep:   map[scheduler.ID]int{},
		rearmed:   map[scheduler.ScheduledScope]int{},
		timing:    map[scheduler.ScheduledScope]scheduler.Timing{},
		windows:   map[scheduler.ScheduledScope][]window{},
		seen:      map[scheduler.ID]bool{},
		payloads:  map[scheduler.ID]bool{},
		bound:     bound,
	}
	d.open(t)
	for _, k := range diffSchedulable[:2] {
		d.schedule(t, k, diffCadences[0], time.Time{})
	}
	d.steps = append(d.steps, "schedule own \"a\" and own \"b\"")
	d.check(t, false)
	for i := range 80 {
		advanced := d.step(t, i)
		synctest.Wait()
		d.check(t, advanced)
		if !store.holding() {
			d.compare(t)
		}
	}
	store.let()
	synctest.Wait()
	d.steps = append(d.steps, "release the held end")
	d.check(t, false)
	d.compare(t)
}

func (d *diffRun) config(records scheduler.Records, run func(context.Context, *scheduler.Run[payload]) scheduler.End, obs scheduler.Observer) scheduler.Config[payload] {
	return scheduler.Config[payload]{Kinds: diffKinds(run), Records: records, Observer: obs, MaxQueued: 3, MaxQueuedPerOwner: 2}
}

// open runs a new executor over the store and waits for its LastEnds read.
func (d *diffRun) open(t *testing.T) {
	t.Helper()
	d.r, d.stop = d.base.openUntil(t, d.config(d.store, d.work, scheduler.Observer{Transition: d.observe}))
	t.Cleanup(d.store.let) // a held End would keep Run from returning
	synctest.Wait()
}

func (d *diffRun) observe(tr scheduler.Transition) {
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.jobs[tr.Job]
	if j == nil {
		j = &diffJob{key: scheduler.ScheduledScope{Kind: tr.Kind, Scope: tr.Scope}}
		d.jobs[tr.Job] = j
	}
	switch {
	case tr.Result != "":
		j.ended, j.result, j.endAt = true, tr.Result, tr.At
	case tr.Phase == scheduler.PhaseStarted:
		j.started = true
	}
}

// work is the Run of every kind but sweep: it waits until the sequence ends
// it with the result it picked, proposing first when asked.
func (d *diffRun) work(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
	g := &diffGate{done: make(chan struct{}), job: r.Job()}
	d.mu.Lock()
	d.running[g.job.ID] = g
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.running, g.job.ID)
		d.mu.Unlock()
	}()
	select {
	case <-g.done:
	case <-ctx.Done():
		return scheduler.End{Result: scheduler.ResultOK} // the executor's stop end wins
	}
	if g.propose {
		_, _ = r.Propose(g.job.Scope)
	}
	return scheduler.End{Result: g.end}
}

func (d *diffRun) schedule(t *testing.T, k scheduler.ScheduledScope, c diffCadence, changed time.Time) {
	t.Helper()
	if err := d.r.ex.Schedule(k.Kind, k.Scope, c.c, changed); err != nil {
		t.Fatalf("Schedule(%v) = %v", k, err)
	}
	if _, every := c.c.Every(); every {
		d.scheduled[k] = c
	} else {
		delete(d.scheduled, k)
		delete(d.timing, k)
		delete(d.windows, k)
	}
}

// runningIDs answers the jobs whose Run waits for the sequence, in ID order.
func (d *diffRun) runningIDs() []scheduler.ID {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]scheduler.ID, 0, len(d.running))
	for id := range d.running {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (d *diffRun) name(id scheduler.ID) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if j := d.jobs[id]; j != nil {
		return fmt.Sprintf("%s %q %s", j.key.Kind, j.key.Scope, id)
	}
	return string(id)
}

// step takes step i, a random one, logs it, and reports whether it only
// advanced the clock.
func (d *diffRun) step(t *testing.T, i int) (advanced bool) {
	t.Helper()
	held := d.store.holding()
	if d.bound && i == 40 {
		d.passBound(t)
		return false
	}
	switch n := d.rnd.IntN(100); {
	case n < 22:
		d.start(t)
	case n < 42:
		d.end(t, held)
	case n < 48:
		d.cancel(t)
	case n < 51:
		owner := diffOwners[d.rnd.IntN(len(diffOwners))]
		d.r.ex.CredentialEnded(owner)
		d.steps = append(d.steps, "credential "+owner+" ended")
	case n < 70:
		step := diffAdvances[d.rnd.IntN(len(diffAdvances))]
		time.Sleep(step)
		d.steps = append(d.steps, fmt.Sprintf("advance %v to %v", step, time.Now()))
		return true
	case n < 80:
		k := diffSchedulable[d.rnd.IntN(len(diffSchedulable))]
		c := diffCadences[d.rnd.IntN(len(diffCadences))]
		var changed time.Time
		switch d.rnd.IntN(3) {
		case 1:
			changed = time.Now()
		case 2:
			changed = time.Now().Add(-3 * time.Minute)
		}
		d.schedule(t, k, c, changed)
		d.steps = append(d.steps, fmt.Sprintf("schedule %s %q every %v retry %v, changed %v", k.Kind, k.Scope, c.every, c.retry, changed))
	case n < 84 && held:
		d.store.let()
		d.steps = append(d.steps, "release the held end")
	case n < 84:
		d.sweep(t, 1+d.rnd.IntN(3))
	case n < 88 && !held:
		resume, err := d.r.ex.Hold(t.Context(), true)
		if err != nil {
			t.Fatalf("Hold = %v", err)
		}
		synctest.Wait()
		resume()
		d.rearmAll()
		d.steps = append(d.steps, "hold and resume")
	case n < 92:
		d.shutdown(t, d.rnd.IntN(2) == 0)
	default:
		d.steps = append(d.steps, "nothing")
	}
	return false
}

func (d *diffRun) start(t *testing.T) {
	t.Helper()
	k := diffStarts[d.rnd.IntN(len(diffStarts))]
	owner := diffOwners[d.rnd.IntN(len(diffOwners))]
	var p payload
	if k.Kind == "full" {
		p.N = d.rnd.IntN(2)
	}
	s, err := d.r.ex.Start(t.Context(), api(k.Kind, k.Scope, owner, p))
	switch name := fmt.Sprintf("%s %q N %d by %s", k.Kind, k.Scope, p.N, owner); {
	case err != nil:
		d.steps = append(d.steps, fmt.Sprintf("start %s: %v", name, err))
	case s.Joined:
		d.steps = append(d.steps, fmt.Sprintf("start %s: joined %s", name, s.Job))
	default:
		d.mu.Lock()
		d.payloads[s.Job] = p.N == 1
		d.mu.Unlock()
		d.steps = append(d.steps, fmt.Sprintf("start %s: job %s", name, s.Job))
	}
}

// end ends a running job, timer jobs included, with a random result; its End
// may be refused, or held when no End is held yet.
func (d *diffRun) end(t *testing.T, held bool) {
	t.Helper()
	ids := d.runningIDs()
	if len(ids) == 0 {
		d.steps = append(d.steps, "nothing to end")
		return
	}
	id := ids[d.rnd.IntN(len(ids))]
	d.mu.Lock()
	g := d.running[id]
	d.mu.Unlock()
	g.end = diffResults[d.rnd.IntN(len(diffResults))]
	g.propose = g.job.Kind == "prop" && g.job.Owner != "" && d.rnd.IntN(2) == 0
	refuse := d.rnd.IntN(6) == 0
	hold := !refuse && !held && d.rnd.IntN(4) == 0
	switch {
	case refuse:
		d.store.refuseNext(id)
	case hold:
		d.store.holdNext(id)
	}
	close(g.done)
	d.steps = append(d.steps, fmt.Sprintf("end %s (%s) %s, proposing %v, refused %v, held %v", d.name(id), g.job.Trigger, g.end, g.propose, refuse, hold))
}

// cancel cancels a live job, queued or running, timer jobs included.
func (d *diffRun) cancel(t *testing.T) {
	t.Helper()
	live := d.r.ex.Live()
	ids := make([]scheduler.ID, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		d.steps = append(d.steps, "nothing to cancel")
		return
	}
	slices.Sort(ids)
	id := ids[d.rnd.IntN(len(ids))]
	stopping, err := d.r.ex.Cancel(t.Context(), id)
	d.steps = append(d.steps, fmt.Sprintf("cancel %s (position %d): %v, %v", d.name(id), live[id].Position, stopping, err))
}

// sweep ends n scopes of the kind that writes no record.
func (d *diffRun) sweep(t *testing.T, n int) {
	t.Helper()
	first := d.rnd.IntN(1000)
	var errs []error
	for i := range n {
		_, err := d.r.ex.Start(t.Context(), api("sweep", fmt.Sprintf("s%d", first+i), "sweeper", payload{}))
		errs = append(errs, err)
		synctest.Wait()
	}
	d.steps = append(d.steps, fmt.Sprintf("sweep %d scopes from s%d: %v", n, first, errs))
}

// passBound empties the queue with a hold, ends more scopes of the kind that
// writes no record than the executor holds ends, then schedules every scope
// that is not scheduled.
func (d *diffRun) passBound(t *testing.T) {
	t.Helper()
	d.store.let()
	synctest.Wait()
	resume, err := d.r.ex.Hold(t.Context(), true)
	if err != nil {
		t.Fatalf("Hold = %v", err)
	}
	synctest.Wait()
	resume()
	synctest.Wait()
	d.rearmAll()
	for i := range 10_001 {
		if _, err := d.r.ex.Start(t.Context(), api("sweep", fmt.Sprintf("bound%d", i), "sweeper", payload{})); err != nil {
			t.Fatalf("Start(sweep) = %v", err)
		}
		synctest.Wait()
	}
	for _, k := range diffSchedulable {
		if _, ok := d.scheduled[k]; !ok {
			d.schedule(t, k, diffCadences[0], time.Time{})
		}
	}
	d.steps = append(d.steps, "hold and resume, sweep 10,001 scopes, then schedule every scope hourly")
}

// shutdown stops the executor mid-run, a Hold(all) open across it when
// hold, and runs a new one over the store, scheduled as the old one was.
func (d *diffRun) shutdown(t *testing.T, hold bool) {
	t.Helper()
	d.store.let()
	synctest.Wait()
	changed := map[scheduler.ScheduledScope]time.Time{}
	for k := range d.scheduled {
		in, _ := d.r.ex.TimingOf(k.Kind, k.Scope)
		changed[k] = in.Changed
	}
	if hold {
		if _, err := d.r.ex.Hold(t.Context(), true); err != nil {
			t.Fatalf("Hold = %v", err)
		}
		synctest.Wait()
	}
	d.stop()
	d.open(t)
	for k, c := range d.scheduled {
		if err := d.r.ex.Schedule(k.Kind, k.Scope, c.c, changed[k]); err != nil {
			t.Fatalf("Schedule(%v) after the shutdown = %v", k, err)
		}
	}
	synctest.Wait()
	d.rearmAll()
	d.steps = append(d.steps, fmt.Sprintf("shut down (hold open %v) and run again", hold))
}

// rearmAll marks every scheduled scope counted again from its base, as a
// resume and a start do.
func (d *diffRun) rearmAll() {
	for k := range d.scheduled {
		d.rearmed[k] = len(d.steps)
	}
}

// relevant reports whether a job of by moves key's base with an end the
// executor counts: its own scope, or one whose kind covers it.
func relevant(by, key scheduler.ScheduledScope) bool { return by == key || diffCovers(by, key) }

// joinableLocked reports whether job id may stand in for key's timer job: a
// relevant one, or a full job whose payload covers key, which a wake joins
// while it is queued though its end moves nothing for key.
func (d *diffRun) joinableLocked(id scheduler.ID, j *diffJob, key scheduler.ScheduledScope) bool {
	return relevant(j.key, key) || j.key.Kind == "full" && d.payloads[id] && ownAt(j.key, key)
}

// check runs after each step. A timer record first seen after an advance
// must fall in a window its scope had after the step before; one seen after
// another step, a wake at that instant, needs a base already due counted again
// in the step. Each idle scheduled scope, with no live job that may stand in
// for its timer job, then gets its windows: its base plus its wait, and each
// end held in memory only (held) since it was counted again plus the wait
// after work not done. An idle scope whose windows have all passed missed its
// wake.
func (d *diffRun) check(t *testing.T, advanced bool) {
	t.Helper()
	k, now := len(d.steps)-1, time.Now()
	d.mu.Lock()
	for id, j := range d.jobs {
		if _, ok := d.endStep[id]; j.ended && !ok {
			d.endStep[id] = k
		}
	}
	d.mu.Unlock()
	fresh := map[scheduler.ScheduledScope][]scheduler.Record{}
	for _, rec := range d.store.All() {
		if rec.Trigger == scheduler.TriggerSchedule && !d.seen[rec.ID] {
			d.seen[rec.ID] = true
			key := scheduler.ScheduledScope{Kind: rec.Kind, Scope: rec.Scope}
			fresh[key] = append(fresh[key], rec)
		}
	}
	live := d.r.ex.Live()
	for key, c := range d.scheduled {
		tm, _ := d.r.ex.TimingOf(key.Kind, key.Scope)
		if p, ok := d.timing[key]; !ok || !p.Base.Equal(tm.Base) || !p.Changed.Equal(tm.Changed) || p.Wait != tm.Wait {
			d.rearmed[key] = k
		}
		d.timing[key] = tm
		counted := band(tm.Base, tm.Wait, "its base")
		if recs := fresh[key]; len(recs) > 0 {
			woke := slices.MinFunc(recs, func(a, b scheduler.Record) int { return a.QueuedAt.Compare(b.QueuedAt) }).QueuedAt
			ws, idle := d.windows[key]
			switch {
			case advanced && idle && !inAny(woke, ws):
				d.fail(t, "%s %q woke at %v, outside its windows %v", key.Kind, key.Scope, woke, ws)
			case !advanced && (woke.Before(counted.lo) || d.rearmed[key] != k && !d.countedEnd(key, k)):
				d.fail(t, "%s %q woke at %v when the step did not count it again from a base already due (%v)", key.Kind, key.Scope, woke, counted)
			}
		}
		if d.standsIn(key, live) {
			delete(d.windows, key)
			continue
		}
		ws := append([]window{counted}, d.held(key, c)...)
		if !slices.ContainsFunc(ws, func(w window) bool { return !w.hi.Before(now) }) {
			d.fail(t, "%s %q is idle at %v and its windows %v have passed", key.Kind, key.Scope, now, ws)
		}
		d.windows[key] = ws
	}
}

// standsIn reports whether a live job may stand in for key's timer job. A
// live job the Observer has not told of yet counts.
func (d *diffRun) standsIn(key scheduler.ScheduledScope, live map[scheduler.ID]scheduler.LiveJob) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range live {
		if j := d.jobs[id]; j == nil || d.joinableLocked(id, j, key) {
			return true
		}
	}
	return false
}

// countedEnd reports whether a job that may have stood in for key's timer
// job ended after step k with an end the executor counts, which arms key's
// timer from its base, as a restart would, even when the base did not move.
func (d *diffRun) countedEnd(key scheduler.ScheduledScope, k int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, j := range d.jobs {
		if j.ended && d.endStep[id] == k && d.countedLocked(id, j) && relevant(j.key, key) {
			return true
		}
	}
	return false
}

// countedLocked reports whether the executor counts j's end: it started,
// its result moves a base and the store kept its End.
func (d *diffRun) countedLocked(id scheduler.ID, j *diffJob) bool {
	moving := j.result != scheduler.ResultCancelled && j.result != scheduler.ResultInterrupted
	return j.started && moving && !d.store.wasRefused(id)
}

// held answers the windows of each end that may have set key's timer since it
// was last counted again and that moved nothing for key: held in memory only,
// or a full job's whose payload covers key.
func (d *diffRun) held(key scheduler.ScheduledScope, c diffCadence) []window {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ws []window
	for id, j := range d.jobs {
		moved := relevant(j.key, key) && d.countedLocked(id, j)
		if j.ended && !moved && d.joinableLocked(id, j, key) && d.endStep[id] >= d.rearmed[key] {
			ws = append(ws, band(j.endAt, c.notDone(), fmt.Sprintf("%s %s %s", j.key.Kind, id, j.result)))
		}
	}
	slices.SortFunc(ws, func(a, b window) int { return cmp.Compare(a.from, b.from) })
	return ws
}

func (d *diffRun) fail(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format+"\nsteps:\n%s", append(args, strings.Join(d.steps, "\n"))...)
}

// compare restarts a read-only executor over the store, schedules every
// scope at its cadence and change, and checks each one an end leads.
func (d *diffRun) compare(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ends := func(context.Context, *scheduler.Run[payload]) scheduler.End {
		return scheduler.End{Result: scheduler.ResultOK}
	}
	re, done := d.r.run(t, ctx, d.config(readOnly{d.store.Store}, ends, scheduler.Observer{}))
	synctest.Wait()
	for k, c := range d.scheduled {
		in, _ := d.r.ex.TimingOf(k.Kind, k.Scope)
		if err := re.ex.Schedule(k.Kind, k.Scope, c.c, in.Changed); err != nil {
			t.Fatalf("restarted Schedule(%v) = %v", k, err)
		}
		got, _ := re.ex.TimingOf(k.Kind, k.Scope)
		if !in.FromEnd && !got.FromEnd {
			continue // both count from no end: a restart counts from its start
		}
		inDeadline, gotDeadline := d.r.ex.Deadline(k.Kind, k.Scope), re.ex.Deadline(k.Kind, k.Scope)
		if in.FromEnd != got.FromEnd || !in.Base.Equal(got.Base) || in.Wait != got.Wait || !inDeadline.Equal(gotDeadline) {
			d.fail(t, "%s %q after step %d counts in process from %v (end %v), wait %v, deadline %v; after a restart from %v (end %v), wait %v, deadline %v",
				k.Kind, k.Scope, len(d.steps)-1, in.Base, in.FromEnd, in.Wait, inDeadline,
				got.Base, got.FromEnd, got.Wait, gotDeadline)
		}
	}
	cancel()
	<-done
}
