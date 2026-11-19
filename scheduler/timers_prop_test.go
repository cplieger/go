package scheduler_test

import (
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

// heldEnd is a memrecords store whose End can hold one job's end after
// writing it until release closes, so the executor notes it after later ones.
type heldEnd struct {
	*memrecords.Store
	release chan struct{}
	id      scheduler.ID
	mu      sync.Mutex
	waiting bool // the held End wrote its end and waits for release
}

func (s *heldEnd) End(ctx context.Context, id scheduler.ID, end scheduler.End) (scheduler.Record, error) {
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

// diffResults are the results a job's payload N picks.
var diffResults = []scheduler.Result{scheduler.ResultOK, scheduler.ResultPartial, scheduler.ResultFailed, scheduler.ResultSkipped}

// diffKinds is own, the covered kind; cover, which covers own at its scope
// or every own scope from "all"; and prop, a proposing kind covering own
// alike. Each job waits on its Gate and ends with diffResults[N].
func diffKinds() []scheduler.KindSpec[payload] {
	run := func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		if g := r.Job().Payload.Gate; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: diffResults[r.Job().Payload.N]}
	}
	covers := func(j, other scheduler.Job[payload]) bool {
		return other.Kind == "own" && (j.Scope == "all" || j.Scope == other.Scope)
	}
	own, cover, prop := spec("own", ""), spec("cover", ""), spec("prop", "")
	cover.Covers, prop.Covers, prop.Proposes = covers, covers, true
	kinds := []scheduler.KindSpec[payload]{own, cover, prop}
	for i := range kinds {
		kinds[i].Run = run
	}
	return kinds
}

var (
	diffStarts = []scheduler.ScheduledScope{
		{Kind: "own", Scope: "a"},
		{Kind: "own", Scope: "b"},
		{Kind: "own", Scope: "c"},
		{Kind: "cover", Scope: "a"},
		{Kind: "cover", Scope: "all"},
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
	// The cadences wait at least 2 hours, longer than any sequence runs, so
	// no timer fires and the jitter cannot change a sequence.
	diffCadences = []scheduler.Cadence{
		scheduler.Scheduled(24 * time.Hour).Retry(2 * time.Hour),
		scheduler.Scheduled(12 * time.Hour).Retry(3 * time.Hour),
		scheduler.Scheduled(24 * time.Hour),
		scheduler.TriggerOnly(),
	}
	diffOwners = []string{"alice", "bob"}
)

// diffJob is a job the sequence started and has not ended.
type diffJob struct {
	gate chan struct{}
	id   scheduler.ID
	name string
}

// diffRun is one sequence over one executor and its store.
type diffRun struct {
	r         *rig
	store     *heldEnd
	scheduled map[scheduler.ScheduledScope]scheduler.Cadence
	steps     []string
	jobs      []diffJob
	rnd       *rand.Rand
}

// TestTimingMatchesRestart drives seeded random sequences of starts, ends
// (ties, an end written before a later one and noted after it), cancels,
// holds, cadence changes and new scopes, and after each step checks every
// scheduled scope that counts from an end against a new executor over the
// same store, scheduled at the same cadences and changes: the base, the wait
// after it and the deadline must match.
func TestTimingMatchesRestart(t *testing.T) {
	for seed := range uint64(300) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runDiff(t, seed) })
		})
	}
}

func runDiff(t *testing.T, seed uint64) {
	store := &heldEnd{Store: memrecords.New()}
	base := &rig{store: store.Store, log: &runLog{}}
	d := &diffRun{
		r:         base.open(t, scheduler.Config[payload]{Kinds: diffKinds(), Records: store}),
		store:     store,
		scheduled: map[scheduler.ScheduledScope]scheduler.Cadence{},
		rnd:       rand.New(rand.NewPCG(seed, 0)),
	}
	t.Cleanup(store.let) // a held End would keep Run from returning
	synctest.Wait()
	for _, k := range diffSchedulable[:2] {
		d.schedule(t, k, diffCadences[0], time.Time{})
	}
	for range 40 {
		d.step(t)
		synctest.Wait()
		if !store.holding() {
			d.compare(t)
		}
	}
	store.let()
	synctest.Wait()
	d.steps = append(d.steps, "release the held end")
	d.compare(t)
}

func (d *diffRun) schedule(t *testing.T, k scheduler.ScheduledScope, c scheduler.Cadence, changed time.Time) {
	t.Helper()
	if err := d.r.ex.Schedule(k.Kind, k.Scope, c, changed); err != nil {
		t.Fatalf("Schedule(%v) = %v", k, err)
	}
	if _, every := c.Every(); every {
		d.scheduled[k] = c
	} else {
		delete(d.scheduled, k)
	}
}

// step takes one random step and logs it.
func (d *diffRun) step(t *testing.T) {
	t.Helper()
	held := d.store.holding()
	switch n := d.rnd.IntN(100); {
	case n < 30:
		k := diffStarts[d.rnd.IntN(len(diffStarts))]
		owner := diffOwners[d.rnd.IntN(len(diffOwners))]
		res := d.rnd.IntN(len(diffResults))
		gate := make(chan struct{})
		s, err := d.r.ex.Start(t.Context(), api(k.Kind, k.Scope, owner, payload{Gate: gate, N: res}))
		name := fmt.Sprintf("%s %q by %s to end %s", k.Kind, k.Scope, owner, diffResults[res])
		switch {
		case err != nil:
			d.steps = append(d.steps, fmt.Sprintf("start %s: %v", name, err))
		case s.Joined:
			d.steps = append(d.steps, fmt.Sprintf("start %s: joined %d", name, d.index(s.Job)))
		default:
			d.jobs = append(d.jobs, diffJob{gate: gate, id: s.Job, name: name})
			d.steps = append(d.steps, fmt.Sprintf("start %s: job %d", name, len(d.jobs)-1))
		}
	case n < 60 && len(d.jobs) > 0:
		i := d.rnd.IntN(len(d.jobs))
		hold := !held && d.rnd.IntN(4) == 0
		if hold {
			d.store.holdNext(d.jobs[i].id)
		}
		close(d.jobs[i].gate)
		d.steps = append(d.steps, fmt.Sprintf("end job %s (held %v)", d.jobs[i].name, hold))
		d.jobs = slices.Delete(d.jobs, i, i+1)
	case n < 65 && len(d.jobs) > 0:
		i := d.rnd.IntN(len(d.jobs))
		_, err := d.r.ex.Cancel(t.Context(), d.jobs[i].id)
		d.steps = append(d.steps, fmt.Sprintf("cancel job %s: %v", d.jobs[i].name, err))
		d.jobs = slices.Delete(d.jobs, i, i+1)
	case n < 80:
		step := []time.Duration{time.Second, time.Minute, 5 * time.Minute}[d.rnd.IntN(3)]
		time.Sleep(step)
		d.steps = append(d.steps, fmt.Sprintf("advance %v", step))
	case n < 90:
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
		d.steps = append(d.steps, fmt.Sprintf("schedule %s %q every %v, changed %v", k.Kind, k.Scope, c, changed))
	case n < 95 && held:
		d.store.let()
		d.steps = append(d.steps, "release the held end")
	case n < 95:
		resume, err := d.r.ex.Hold(t.Context(), true)
		if err != nil {
			t.Fatalf("Hold = %v", err)
		}
		synctest.Wait()
		resume()
		d.jobs = nil
		d.steps = append(d.steps, "hold and resume")
	default:
		d.steps = append(d.steps, "nothing")
	}
}

func (d *diffRun) index(id scheduler.ID) int {
	return slices.IndexFunc(d.jobs, func(j diffJob) bool { return j.id == id })
}

// compare restarts a read-only executor over the store, schedules every
// scope at its cadence and change, and checks each one an end leads.
func (d *diffRun) compare(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	re, done := d.r.run(t, ctx, scheduler.Config[payload]{Kinds: diffKinds(), Records: readOnly{d.store.Store}})
	synctest.Wait()
	for k, c := range d.scheduled {
		in, _ := d.r.ex.TimingOf(k.Kind, k.Scope)
		if err := re.ex.Schedule(k.Kind, k.Scope, c, in.Changed); err != nil {
			t.Fatalf("restarted Schedule(%v) = %v", k, err)
		}
		got, _ := re.ex.TimingOf(k.Kind, k.Scope)
		if !in.FromEnd && !got.FromEnd {
			continue // both count from no end: a restart counts from its start
		}
		inDeadline, gotDeadline := d.r.ex.Deadline(k.Kind, k.Scope), re.ex.Deadline(k.Kind, k.Scope)
		if in.FromEnd != got.FromEnd || !in.Base.Equal(got.Base) || in.Wait != got.Wait || !inDeadline.Equal(gotDeadline) {
			t.Fatalf("%s %q after step %d counts in process from %v (end %v), wait %v, deadline %v; after a restart from %v (end %v), wait %v, deadline %v\nsteps:\n%s",
				k.Kind, k.Scope, len(d.steps)-1, in.Base, in.FromEnd, in.Wait, inDeadline,
				got.Base, got.FromEnd, got.Wait, gotDeadline, strings.Join(d.steps, "\n"))
		}
	}
	cancel()
	<-done
}
