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
)

// The kinds the request rules are driven over: quick, full and prop (which
// proposes) share slot s, plain and refresh (Fresh, ruleFresh) run with no
// slot, and batch is a batch whose items take slot b and end at once.
var (
	ruleKinds    = []scheduler.Kind{"quick", "full", "prop", "plain", "refresh", "batch"}
	ruleScopes   = []string{"a", "b", "all"}
	ruleOwners   = []string{"alice", "bob"}
	ruleResults  = []scheduler.Result{scheduler.ResultOK, scheduler.ResultPartial, scheduler.ResultFailed, scheduler.ResultSkipped}
	ruleYields   = []time.Duration{30 * time.Second, 5 * time.Minute, 20 * time.Minute}
	ruleAdvances = []time.Duration{time.Second, 10 * time.Second, 30 * time.Second, 2 * time.Minute, 16 * time.Minute, 45 * time.Minute}
)

const (
	ruleMaxQueued   = 5
	ruleMaxPerOwner = 2
	ruleProposalTTL = 15 * time.Minute
)

func ruleSlotted(k scheduler.Kind) bool { return k == "quick" || k == "full" || k == "prop" }

// ruleFresh is refresh's window: 60 s for every scope, 15 s for one.
func ruleFresh(scope string) time.Duration {
	if scope == "all" {
		return 60 * time.Second
	}
	return 15 * time.Second
}

// ruleCovers is full's and refresh's Covers: full covers quick of its scope,
// and all covers every other scope of its own kind and, for full, quick.
func ruleCovers(started, other scheduler.Job[payload]) bool {
	switch started.Kind {
	case "full":
		return other.Kind == "quick" && (started.Scope == "all" || started.Scope == other.Scope) ||
			other.Kind == "full" && started.Scope == "all" && other.Scope != "all"
	case "refresh":
		return other.Kind == "refresh" && started.Scope == "all" && other.Scope != "all"
	}
	return false
}

type ruleState uint8

const (
	ruleQueued ruleState = iota
	ruleRunning
	ruleYielded // in its Yield, its until to come or waiting for slot s
	ruleEnded
)

func (s ruleState) String() string { return [...]string{"queued", "running", "yielded", "ended"}[s] }

// ruleJob is one admitted job as the model has it.
type ruleJob struct {
	until     time.Time // its Yield's until, moved to Wake's time when earlier
	startedAt time.Time
	yieldAns  string // its last Yield's answer, "" while none returned
	result    scheduler.Result
	reasons   []scheduler.Reason
	j         scheduler.Job[payload]
	n         int
	state     ruleState
	ran       bool
	woken     bool // its yield's until came or a Wake did
	proposed  bool
}

func (q *ruleJob) String() string {
	by := q.j.Owner
	if q.j.Trigger == scheduler.TriggerSchedule {
		by = "timer"
	}
	return fmt.Sprintf("#%d %s %q by %s", q.n, q.j.Kind, q.j.Scope, by)
}

type ruleProposal struct {
	until   time.Time // zero while its job runs
	job     *ruleJob
	cleared bool // a Hold(all) dropped it
}

// ruleModel answers requests by the rules of docs/executor.md ("What a
// request does", "Classes and the slot", "Yield and Wake", "Proposals"),
// written from those rules and not from the executor.
type ruleModel struct {
	jobs      map[scheduler.ID]*ruleJob
	lastEnd   map[scheduler.ScheduledScope]time.Time // the last moving end of a job that ran
	proposals map[scheduler.ProposalID]*ruleProposal
	holder    *ruleJob      // slot s's
	all       []*ruleJob    // every admitted job, in admission order
	live      []*ruleJob    // queued, running and yielded, in admission order
	queues    [3][]*ruleJob // slot s: Interactive, yielded, Automatic
	refused   int           // records ended skipped, queue_full
	held      bool
}

// ruleWant is a Start's answer as the model has it.
type ruleWant struct {
	until    time.Time // a *RecentError's
	err      error     // nil, ErrRecent or ErrQueueFull
	join     *ruleJob
	position int
}

func ruleSame(q *ruleJob, j scheduler.Job[payload]) bool {
	return q.j.Kind == j.Kind && q.j.Scope == j.Scope && (q.j.Kind != "prop" || q.j.Owner == j.Owner)
}

func (*ruleModel) kept(p *ruleProposal) bool {
	return !p.cleared && (p.until.IsZero() || time.Now().Before(p.until))
}

// heldKey answers the job whose held proposal has j's owner, kind and scope.
func (m *ruleModel) heldKey(j scheduler.Job[payload]) *ruleJob {
	for _, p := range m.proposals {
		if m.kept(p) && p.job.j.Kind == j.Kind && p.job.j.Scope == j.Scope && p.job.j.Owner == j.Owner {
			return p.job
		}
	}
	return nil
}

func (m *ruleModel) position(q *ruleJob) int {
	n := 0
	for _, tier := range m.queues {
		if i := slices.Index(tier, q); i >= 0 {
			return n + i
		}
		n += len(tier)
	}
	return 0
}

// request is rules 1 to 5, a held proposal answering first.
func (m *ruleModel) request(j scheduler.Job[payload]) ruleWant {
	now := time.Now()
	if j.Kind == "prop" {
		if q := m.heldKey(j); q != nil {
			return ruleWant{join: q}
		}
	}
	for _, q := range m.live {
		if q.state == ruleQueued && ruleSame(q, j) {
			return ruleWant{join: q, position: m.position(q)}
		}
	}
	for _, q := range m.live {
		if q.state == ruleQueued && ruleCovers(q.j, j) {
			return ruleWant{join: q, position: m.position(q)}
		}
	}
	if m.held {
		return ruleWant{err: scheduler.ErrQueueFull}
	}
	var run *ruleJob
	for _, q := range m.live {
		serves := q.state == ruleRunning || q.state == ruleYielded && j.Kind == "prop"
		if serves && ruleSame(q, j) && (run == nil || q.startedAt.After(run.startedAt)) {
			run = q
		}
	}
	key := scheduler.ScheduledScope{Kind: j.Kind, Scope: j.Scope}
	switch {
	case run != nil && (j.Trigger == scheduler.TriggerSchedule || j.Kind == "prop" ||
		j.Kind == "refresh" && now.Sub(run.startedAt) < ruleFresh(j.Scope)):
		return ruleWant{join: run}
	case run == nil && j.Kind == "refresh":
		if end, ok := m.lastEnd[key]; ok && now.Before(end.Add(ruleFresh(j.Scope))) {
			return ruleWant{err: scheduler.ErrRecent, until: end.Add(ruleFresh(j.Scope))}
		}
	}
	waiting, mine := 0, 0
	for _, q := range m.live {
		if q.state == ruleQueued {
			waiting++
			if j.Owner != "" && q.j.Owner == j.Owner {
				mine++
			}
		}
	}
	if waiting >= ruleMaxQueued || mine >= ruleMaxPerOwner {
		m.refused++
		return ruleWant{err: scheduler.ErrQueueFull}
	}
	return ruleWant{}
}

// admit queues a new job and answers its position.
func (m *ruleModel) admit(id scheduler.ID, j scheduler.Job[payload]) (*ruleJob, int) {
	j.ID = id
	q := &ruleJob{j: j, n: len(m.jobs) + 1}
	m.jobs[id] = q
	m.all = append(m.all, q)
	m.live = append(m.live, q)
	pos := 0
	if ruleSlotted(j.Kind) {
		tier := 0
		if j.Trigger == scheduler.TriggerSchedule {
			tier = 2
		}
		if m.holder == nil {
			m.holder = q
		} else {
			m.queues[tier] = append(m.queues[tier], q)
			pos = m.position(q)
		}
	}
	m.settle()
	return q, pos
}

// settle starts what may start: slot s's next waiter in class order, and
// each queued job with no slot and no running job of its scope to wait
// behind.
func (m *ruleModel) settle() {
	for {
		switch {
		case m.holder == nil && m.grant():
		case m.holder != nil && m.holder.state == ruleQueued:
			m.begin(m.holder)
		default:
			q := m.startable()
			if q == nil {
				return
			}
			m.begin(q)
		}
	}
}

func (m *ruleModel) grant() bool {
	for t := range m.queues {
		if len(m.queues[t]) > 0 {
			q := m.queues[t][0]
			m.queues[t] = m.queues[t][1:]
			m.holder = q
			if q.state == ruleYielded {
				q.state, q.yieldAns = ruleRunning, "nil"
			}
			return true
		}
	}
	return false
}

func (m *ruleModel) startable() *ruleJob {
	for _, q := range m.live {
		if q.state != ruleQueued || ruleSlotted(q.j.Kind) {
			continue
		}
		if !slices.ContainsFunc(m.live, func(p *ruleJob) bool { return p.state == ruleRunning && ruleSame(p, q.j) }) {
			return q
		}
	}
	return nil
}

// begin starts q; each yielded job it covers has its Yield answer covered,
// its Run then ending skipped, and each queued one ends skipped as covered.
func (m *ruleModel) begin(q *ruleJob) {
	q.state, q.ran, q.startedAt = ruleRunning, true, time.Now()
	for _, p := range slices.Clone(m.live) {
		switch {
		case p.state == ruleYielded && ruleCovers(q.j, p.j):
			p.yieldAns = "covered by " + string(q.j.ID)
			m.end(p, scheduler.ResultSkipped)
		case p.state == ruleQueued && ruleCovers(q.j, p.j):
			m.end(p, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(q.j.ID)})
		}
	}
}

// end ends q; the caller settles.
func (m *ruleModel) end(q *ruleJob, r scheduler.Result, reasons ...scheduler.Reason) {
	now := time.Now()
	q.state, q.result, q.reasons = ruleEnded, r, reasons
	drop := func(p *ruleJob) bool { return p == q }
	m.live = slices.DeleteFunc(m.live, drop)
	for t := range m.queues {
		m.queues[t] = slices.DeleteFunc(m.queues[t], drop)
	}
	if m.holder == q {
		m.holder = nil
	}
	if q.ran && r != scheduler.ResultCancelled && r != scheduler.ResultInterrupted {
		m.lastEnd[scheduler.ScheduledScope{Kind: q.j.Kind, Scope: q.j.Scope}] = now
	}
	for _, p := range m.proposals {
		if p.job == q {
			p.until = now.Add(ruleProposalTTL)
		}
	}
}

func (m *ruleModel) yield(q *ruleJob, until time.Time) {
	q.state, q.until, q.woken, q.yieldAns = ruleYielded, until, false, ""
	if m.holder == q {
		m.holder = nil
	}
	m.settle()
}

// wakeYield ends q's wait: a job with no slot runs on, one of slot s waits
// for it behind the Interactive jobs.
func (m *ruleModel) wakeYield(q *ruleJob) {
	if !ruleSlotted(q.j.Kind) {
		q.state, q.yieldAns = ruleRunning, "nil"
	} else {
		q.woken = true
		m.queues[1] = append(m.queues[1], q)
	}
	m.settle()
}

func (m *ruleModel) wake(q *ruleJob) bool {
	if q.state != ruleYielded {
		return false
	}
	if !q.woken {
		if now := time.Now(); now.Before(q.until) {
			q.until = now
		}
		m.wakeYield(q)
	}
	return true
}

// advance ends each yield whose until came by now, in until order.
func (m *ruleModel) advance() {
	now := time.Now()
	for {
		var next *ruleJob
		for _, q := range m.live {
			if q.state == ruleYielded && !q.woken && !q.until.After(now) && (next == nil || q.until.Before(next.until)) {
				next = q
			}
		}
		if next == nil {
			return
		}
		m.wakeYield(next)
	}
}

func (m *ruleModel) cancel(q *ruleJob) (stopping bool, err error) {
	switch q.state {
	case ruleEnded:
		if q.result == scheduler.ResultCancelled {
			return q.ran, nil
		}
		return false, scheduler.ErrNotCancellable
	case ruleQueued:
		m.end(q, scheduler.ResultCancelled)
		m.settle()
		return false, nil
	case ruleYielded:
		q.yieldAns = "cancelled"
	case ruleRunning:
	}
	m.end(q, scheduler.ResultCancelled)
	m.settle()
	return true, nil
}

func (m *ruleModel) hold() {
	m.held = true
	for _, p := range m.proposals {
		p.cleared = true
	}
	for _, q := range slices.Clone(m.live) {
		if q.state == ruleYielded {
			q.yieldAns = "cancelled"
		}
		m.end(q, scheduler.ResultInterrupted)
	}
}

type ruleOp uint8

const (
	ruleEndOp ruleOp = iota
	ruleYieldOp
	ruleProposeOp
)

type ruleCmd struct {
	until  time.Time
	result scheduler.Result
	op     ruleOp
}

// ruleRunCtl is one job's Run as the sequence drives it.
type ruleRunCtl struct {
	cmds       chan ruleCmd
	yieldAns   string
	proposeAns string
	proposal   scheduler.ProposalID
	yielding   bool
}

// ruleSeq is one sequence: the executor, the model and what the Runs saw.
type ruleSeq struct {
	r      *rig
	m      *ruleModel
	rnd    *rand.Rand
	runs   map[scheduler.ID]*ruleRunCtl
	resume func()
	steps  []string
	seed   uint64
	mu     sync.Mutex
}

func ruleErr(err error) string {
	var covered *scheduler.CoveredError
	switch {
	case err == nil:
		return "nil"
	case errors.As(err, &covered):
		return "covered by " + string(covered.By)
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return err.Error()
}

// TestRequestRulesMatchModel drives seeded random sequences of starts of each
// kind and trigger, ends of every result, yields and wakes, proposals,
// cancels, holds and resumes and clock advances. After each step every answer
// and the executor's state (each job's state and end, which request joined
// which job, which start covered which job, queue positions, yields' untils,
// the held proposals) must match ruleModel.
func TestRequestRulesMatchModel(t *testing.T) {
	for seed := range uint64(1000) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runRules(t, seed) })
		})
	}
}

func runRules(t *testing.T, seed uint64) {
	s := &ruleSeq{
		m: &ruleModel{
			jobs:      map[scheduler.ID]*ruleJob{},
			lastEnd:   map[scheduler.ScheduledScope]time.Time{},
			proposals: map[scheduler.ProposalID]*ruleProposal{},
		},
		rnd:  rand.New(rand.NewPCG(seed, 1)),
		runs: map[scheduler.ID]*ruleRunCtl{},
		seed: seed,
	}
	s.r = newRig(t, scheduler.Config[payload]{Kinds: s.kinds(), MaxQueued: ruleMaxQueued, MaxQueuedPerOwner: ruleMaxPerOwner})
	synctest.Wait()
	for range 120 {
		s.step(t)
		s.compare(t)
	}
	if s.resume != nil {
		s.resume()
	}
}

func (s *ruleSeq) kinds() []scheduler.KindSpec[payload] {
	quick, full, prop := spec("quick", "s"), spec("full", "s"), spec("prop", "s")
	plain, refresh, batch := spec("plain", ""), spec("refresh", ""), spec("batch", "b")
	full.Covers, refresh.Covers, refresh.Fresh = ruleCovers, ruleCovers, ruleFresh
	prop.Proposes = true
	batch.MaxItems, batch.Items = 3, func(p payload) int { return p.N }
	ks := []scheduler.KindSpec[payload]{quick, full, prop, plain, refresh, batch}
	for i := range ks {
		ks[i].Run = s.work
	}
	return ks
}

// work runs a batch's items, then does what the sequence sends until it
// sends an end or the job's context ends.
func (s *ruleSeq) work(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
	j := r.Job()
	c := &ruleRunCtl{cmds: make(chan ruleCmd, 1)}
	s.set(func() { s.runs[j.ID] = c })
	for i := range j.Payload.N {
		if err := r.Item(ctx, i, func(context.Context) scheduler.Line { return scheduler.Line{State: scheduler.LineDone} }); err != nil {
			return scheduler.End{Result: scheduler.ResultOK}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return scheduler.End{Result: scheduler.ResultOK} // the executor's stop end wins
		case cmd := <-c.cmds:
			switch cmd.op {
			case ruleEndOp:
				return scheduler.End{Result: cmd.result}
			case ruleYieldOp:
				s.set(func() { c.yielding, c.yieldAns = true, "" })
				err := r.Yield(ctx, cmd.until)
				s.set(func() { c.yielding, c.yieldAns = false, ruleErr(err) })
				switch {
				case errors.Is(err, scheduler.ErrCovered):
					return scheduler.End{Result: scheduler.ResultSkipped}
				case err != nil:
					return scheduler.End{Result: scheduler.ResultOK}
				}
			case ruleProposeOp:
				id, err := r.Propose(j.Scope)
				s.set(func() { c.proposal, c.proposeAns = id, ruleErr(err) })
			}
		}
	}
}

func (s *ruleSeq) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *ruleSeq) ctl(id scheduler.ID) *ruleRunCtl {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *ruleSeq) logf(format string, args ...any) {
	s.steps = append(s.steps, fmt.Sprintf(format, args...))
}

func (s *ruleSeq) failf(t *testing.T, format string, args ...any) {
	t.Helper()
	tail := s.steps[max(0, len(s.steps)-25):]
	t.Fatalf("seed %d step %d: %s\nsteps (last %d):\n  %s", s.seed, len(s.steps), fmt.Sprintf(format, args...),
		len(tail), strings.Join(tail, "\n  "))
}

func (s *ruleSeq) name(id scheduler.ID) string {
	if q := s.m.jobs[id]; q != nil {
		return q.String()
	}
	return string(id)
}

// any answers a random admitted job, or nil.
func (s *ruleSeq) any() *ruleJob {
	if len(s.m.all) == 0 {
		return nil
	}
	return s.m.all[s.rnd.IntN(len(s.m.all))]
}

// pick answers a random live job in state st, or nil.
func (s *ruleSeq) pick(st ruleState, kind scheduler.Kind) *ruleJob {
	var in []*ruleJob
	for _, q := range s.m.live {
		if q.state == st && (kind == "" || q.j.Kind == kind) {
			in = append(in, q)
		}
	}
	if len(in) == 0 {
		return nil
	}
	return in[s.rnd.IntN(len(in))]
}

func (s *ruleSeq) step(t *testing.T) {
	t.Helper()
	if s.resume != nil && s.rnd.IntN(3) == 0 {
		s.resume()
		s.resume = nil
		synctest.Wait()
		s.m.held = false
		s.logf("resume")
		return
	}
	switch n := s.rnd.IntN(100); {
	case n < 38:
		s.start(t)
	case n < 52:
		if q := s.pick(ruleRunning, ""); q != nil {
			res := ruleResults[s.rnd.IntN(len(ruleResults))]
			s.logf("%v ends %s", q, res)
			s.send(q, ruleCmd{op: ruleEndOp, result: res})
			s.m.end(q, res)
			s.m.settle()
		}
	case n < 60:
		// A proposing job's yield is picked as often as all the others'.
		q := s.pick(ruleRunning, "")
		if p := s.pick(ruleRunning, "prop"); p != nil && s.rnd.IntN(2) == 0 {
			q = p
		}
		if q != nil {
			until := time.Now().Add(ruleYields[s.rnd.IntN(len(ruleYields))] + time.Duration(len(s.steps))*time.Millisecond)
			s.logf("%v yields until %s", q, until.Format(time.TimeOnly))
			s.send(q, ruleCmd{op: ruleYieldOp, until: until})
			s.m.yield(q, until)
		}
	case n < 66:
		s.wake(t)
	case n < 73:
		s.propose(t)
	case n < 81:
		s.cancel(t)
	case n < 95:
		d := ruleAdvances[s.rnd.IntN(len(ruleAdvances))]
		time.Sleep(d)
		synctest.Wait()
		s.logf("advance %v to %s", d, time.Now().Format(time.TimeOnly))
		s.m.advance()
	case s.resume != nil:
		s.logf("nothing")
	default:
		resume, err := s.r.ex.Hold(t.Context(), true)
		synctest.Wait()
		s.logf("hold all")
		if err != nil {
			s.failf(t, "Hold(all) = %v, want nil", err)
		}
		s.resume = resume
		s.m.hold()
	}
}

func (s *ruleSeq) send(q *ruleJob, cmd ruleCmd) {
	s.ctl(q.j.ID).cmds <- cmd
	synctest.Wait()
}

func (s *ruleSeq) start(t *testing.T) {
	t.Helper()
	kind := ruleKinds[s.rnd.IntN(len(ruleKinds))]
	scope := ruleScopes[s.rnd.IntN(len(ruleScopes))]
	var p payload
	if kind == "batch" {
		p.N = 1 + s.rnd.IntN(3)
	}
	j := api(kind, scope, ruleOwners[s.rnd.IntN(len(ruleOwners))], p)
	if kind != "prop" && s.rnd.IntN(4) == 0 {
		j = timer(kind, scope, p)
	}
	// A repeat of a live job's request, so the rules that answer one are met.
	if len(s.m.live) > 0 && s.rnd.IntN(5) < 2 {
		q := s.m.live[s.rnd.IntN(len(s.m.live))]
		j = q.j
		j.ID, j.Payload = "", p
		if j.Kind == "batch" {
			j.Payload.N = 1 + s.rnd.IntN(3)
		} else {
			j.Payload.N = 0
		}
		kind, scope = j.Kind, j.Scope
	}
	want := s.m.request(j)
	got, err := s.r.ex.Start(t.Context(), j)
	synctest.Wait()
	by := j.Owner
	if j.Trigger == scheduler.TriggerSchedule {
		by = "timer"
	}
	s.logf("start %s %q by %s: %+v, %v", kind, scope, by, got, err)
	var recent *scheduler.RecentError
	switch {
	case want.err == nil && want.join != nil:
		if w := (scheduler.Started{Job: want.join.j.ID, Position: want.position, Joined: true}); got != w || err != nil {
			s.failf(t, "start answered (%+v, %v), want (%+v, nil): joined to %v", got, err, w, want.join)
		}
	case want.err == nil:
		if err != nil || got.Joined || got.Job == "" {
			s.failf(t, "start answered (%+v %s, %v), want a new job", got, s.name(got.Job), err)
		}
		q, pos := s.m.admit(got.Job, j)
		s.steps[len(s.steps)-1] += " as " + q.String()
		if got.Position != pos {
			s.failf(t, "new job %v answered position %d, want %d", q, got.Position, pos)
		}
	case errors.Is(want.err, scheduler.ErrRecent):
		if !errors.As(err, &recent) || !recent.Until.Equal(want.until) || got != (scheduler.Started{}) {
			s.failf(t, "start answered (%+v %s, %v), want *RecentError until %v", got, s.name(got.Job), err, want.until)
		}
	default:
		if !errors.Is(err, scheduler.ErrQueueFull) || got != (scheduler.Started{}) {
			s.failf(t, "start answered (%+v %s, %v), want ErrQueueFull", got, s.name(got.Job), err)
		}
	}
}

func (s *ruleSeq) wake(t *testing.T) {
	t.Helper()
	q := s.pick(ruleYielded, "")
	if q == nil || s.rnd.IntN(5) == 0 {
		if q = s.any(); q == nil {
			return
		}
	}
	was := q.state
	got := s.r.ex.Wake(q.j.ID)
	want := s.m.wake(q)
	synctest.Wait()
	s.logf("wake %v (%s): %t", q, was, got)
	if got != want {
		s.failf(t, "Wake(%v) = %t, want %t", q, got, want)
	}
}

func (s *ruleSeq) propose(t *testing.T) {
	t.Helper()
	q := s.pick(ruleRunning, "prop")
	if q == nil {
		return
	}
	s.send(q, ruleCmd{op: ruleProposeOp})
	c := s.ctl(q.j.ID)
	s.mu.Lock()
	id, ans := c.proposal, c.proposeAns
	s.mu.Unlock()
	s.logf("%v proposes: %s %s", q, id, ans)
	if q.proposed {
		if want := ruleErr(scheduler.ErrProposed); ans != want {
			s.failf(t, "a second Propose of %v answered %s, want %s", q, ans, want)
		}
		return
	}
	if other := s.m.heldKey(q.j); other != nil {
		s.failf(t, "%v proposed (%s %s) a key the held proposal of %v holds; one key answers one job", q, id, ans, other)
	}
	if ans != "nil" {
		s.failf(t, "Propose of %v answered %s, want nil", q, ans)
	}
	q.proposed = true
	s.m.proposals[id] = &ruleProposal{job: q}
}

func (s *ruleSeq) cancel(t *testing.T) {
	t.Helper()
	q := s.any()
	if q == nil {
		return
	}
	was := q.state
	stopping, err := s.r.ex.Cancel(t.Context(), q.j.ID)
	synctest.Wait()
	wantStopping, wantErr := s.m.cancel(q)
	s.logf("cancel %v (%s): %t, %v", q, was, stopping, err)
	if stopping != wantStopping || !errors.Is(err, wantErr) {
		s.failf(t, "Cancel(%v) = (%t, %v), want (%t, %v)", q, stopping, err, wantStopping, wantErr)
	}
}

// compare checks each job's state, end, position and yield against the
// model, the records a queue refusal wrote and the held proposals.
func (s *ruleSeq) compare(t *testing.T) {
	t.Helper()
	live := s.r.ex.Live()
	for id := range live {
		if s.m.jobs[id] == nil {
			s.failf(t, "Live has %s, a job the model never admitted", id)
		}
	}
	for _, q := range s.m.all {
		id := q.j.ID
		rec, ok, err := s.r.store.Get(t.Context(), id)
		if err != nil || !ok {
			s.failf(t, "record of %v = (%t, %v), want stored", q, ok, err)
		}
		c := s.ctl(id)
		s.mu.Lock()
		got := "queued"
		var yieldAns string
		switch {
		case rec.State == scheduler.StateEnded:
			got = "ended"
		case c != nil && c.yielding:
			got = "yielded"
		case c != nil:
			got = "running"
		}
		if c != nil {
			yieldAns = c.yieldAns
		}
		s.mu.Unlock()
		if got != q.state.String() {
			s.failf(t, "%v is %s, want %s", q, got, q.state)
		}
		if (c != nil) != q.ran {
			s.failf(t, "%v ran %t, want %t", q, c != nil, q.ran)
		}
		if yieldAns != q.yieldAns {
			s.failf(t, "%v's Yield answered %q, want %q", q, yieldAns, q.yieldAns)
		}
		l, isLive := live[id]
		if isLive != (q.state != ruleEnded) {
			s.failf(t, "%v in Live %t, want %t", q, isLive, q.state != ruleEnded)
		}
		switch q.state {
		case ruleQueued:
			if want := s.m.position(q); l.Position != want {
				s.failf(t, "%v at position %d, want %d", q, l.Position, want)
			}
		case ruleYielded:
			if !l.Until.Equal(q.until) {
				s.failf(t, "%v yielded until %v, want %v", q, l.Until, q.until)
			}
		case ruleEnded:
			if rec.End.Result != q.result || !slices.Equal(rec.End.Reasons, q.reasons) {
				s.failf(t, "%v ended %s %v, want %s %v", q, rec.End.Result, rec.End.Reasons, q.result, q.reasons)
			}
		case ruleRunning:
		}
	}
	refused := 0
	for _, rec := range s.r.store.All() {
		if s.m.jobs[rec.ID] != nil {
			continue
		}
		refused++
		if rec.End == nil || rec.End.Result != scheduler.ResultSkipped ||
			!slices.Equal(rec.End.Reasons, []scheduler.Reason{{Code: scheduler.ReasonQueueFull}}) {
			s.failf(t, "record %s %s %q of no admitted job = %+v, want ended skipped queue_full", rec.ID, rec.Kind, rec.Scope, rec.End)
		}
	}
	if refused != s.m.refused {
		s.failf(t, "%d records of refused starts, want %d", refused, s.m.refused)
	}
	for id, p := range s.m.proposals {
		if _, ok := s.r.ex.Proposal(p.job.j.Owner, id); ok != s.m.kept(p) {
			s.failf(t, "Proposal(%s, %s of %v) held %t, want %t", p.job.j.Owner, id, p.job, ok, s.m.kept(p))
		}
	}
}
