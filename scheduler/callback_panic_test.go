package scheduler_test

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

// injected is the panic faults raises, naming the call it raised it in.
type injected struct{ site string }

// faults panics in the nth call the executor makes into app code (Records,
// Observer, a KindSpec func) on a stack the app recovers: a kind's Run, or a
// call try makes, as net/http recovers a handler. Calls on the executor's own
// goroutines are not counted: a panic there ends the process.
type faults struct {
	calls       []string       // each counted call's site, in call order
	runPanicked []scheduler.ID // the jobs whose Run the injected panic left
	n           int            // the call to panic in; 0 never
	mu          sync.Mutex
	done        bool // the scenario is over: count nothing more
}

// call counts a call into app code named name, and panics when it is the
// nth.
func (f *faults) call(name string) {
	where, ok := recovering()
	if !ok {
		return
	}
	site := name + " in " + where
	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.calls = append(f.calls, site)
	fire := len(f.calls) == f.n
	f.mu.Unlock()
	if fire {
		panic(injected{site})
	}
}

// recovering answers where the calling goroutine's app frame is, "Run" or
// "a call", and false on a stack with neither.
func recovering() (string, bool) {
	pcs := make([]uintptr, 128)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	for {
		fr, more := frames.Next()
		switch {
		case strings.HasSuffix(fr.Function, ".(*faults).run.func1"):
			return "Run", true
		case strings.HasSuffix(fr.Function, ".(*faults).try"):
			return "a call", true
		}
		if !more {
			return "", false
		}
	}
}

// try makes call, recovering the injected panic as an app's server would.
func (f *faults) try(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(injected); !ok {
				t.Errorf("panic %v, want none but the injected one", v)
			}
		}
	}()
	call()
}

// run wraps a kind's Run, noting the job when the injected panic leaves it.
func (f *faults) run(body func(context.Context, *scheduler.Run[payload]) scheduler.End) func(context.Context, *scheduler.Run[payload]) scheduler.End {
	return func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(injected); ok {
					f.mu.Lock()
					f.runPanicked = append(f.runPanicked, r.Job().ID)
					f.mu.Unlock()
				}
				panic(v)
			}
		}()
		return body(ctx, r)
	}
}

func (f *faults) observer(seen *transitions) scheduler.Observer {
	record := seen.observer().Transition
	return scheduler.Observer{
		Transition: func(tr scheduler.Transition) {
			record(tr)
			f.call("Observer.Transition")
		},
		SlotWait: func(string, scheduler.Class, time.Duration) { f.call("Observer.SlotWait") },
	}
}

// faultStore is a memrecords store whose methods the executor calls go
// through faults first.
type faultStore struct {
	*memrecords.Store
	f *faults
}

var _ scheduler.Records = faultStore{}

func (s faultStore) Insert(ctx context.Context, r scheduler.Record, key *scheduler.Key) (scheduler.Record, bool, error) {
	s.f.call("Records.Insert")
	return s.Store.Insert(ctx, r, key)
}

func (s faultStore) Put(ctx context.Context, r scheduler.Record) error {
	s.f.call("Records.Put")
	return s.Store.Put(ctx, r)
}

func (s faultStore) End(ctx context.Context, id scheduler.ID, e scheduler.End) (scheduler.Record, error) {
	s.f.call("Records.End")
	return s.Store.End(ctx, id, e)
}

func (s faultStore) Get(ctx context.Context, id scheduler.ID) (scheduler.Record, bool, error) {
	s.f.call("Records.Get")
	return s.Store.Get(ctx, id)
}

func (s faultStore) LastEnds(ctx context.Context) (map[scheduler.ScheduledScope]scheduler.LastEnd, error) {
	s.f.call("Records.LastEnds")
	return s.Store.LastEnds(ctx)
}

// faultKinds are the scenario's kinds, each Run and KindSpec func through f.
// Slot maint runs park, slot passes every other kind.
func faultKinds(f *faults) []scheduler.KindSpec[payload] {
	gated := func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		if g := r.Job().Payload.Gate; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	hold := spec("hold", "passes")
	hold.Fresh = func(string) time.Duration {
		f.call("KindSpec.Fresh")
		return 0
	}
	hold.Run = gated
	pipe := spec("pipe", "passes")
	pipe.Phases = []scheduler.Phase{"fetched"}
	pipe.Proposes = true
	pipe.Run = func(_ context.Context, r *scheduler.Run[payload]) scheduler.End {
		r.Reached("fetched")
		_, _ = r.Propose("plan")
		if !r.Commit() {
			return scheduler.End{Result: scheduler.ResultCancelled}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	batch := grab(done)
	batch.Slot = "passes"
	batch.Items = func(p payload) int {
		f.call("KindSpec.Items")
		return p.N
	}
	nap := spec("nap", "passes")
	nap.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		_ = r.Yield(ctx, time.Now().Add(time.Minute))
		return scheduler.End{Result: scheduler.ResultOK}
	}
	full := spec("full", "passes")
	full.OnBehalf = true
	full.Covers = func(_, other scheduler.Job[payload]) bool {
		f.call("KindSpec.Covers")
		return other.Kind == "part"
	}
	full.Run = gated
	part := spec("part", "passes")
	part.Watch = func(every time.Duration) health.Lease {
		f.call("KindSpec.Watch")
		return health.Lease{Interval: every, Cycles: 3}
	}
	part.Run = gated
	park := spec("park", "maint")
	park.Run = gated
	kinds := []scheduler.KindSpec[payload]{hold, pipe, batch, nap, full, part, park}
	for i := range kinds {
		kinds[i].Run = f.run(kinds[i].Run)
	}
	return kinds
}

// faultRun drives the scenario with a panic injected in its nth counted
// call, 0 for none, then checks the executor is still usable, and answers
// the counted calls. Call it inside a synctest bubble.
func faultRun(t *testing.T, n int) []string {
	t.Helper()
	f := &faults{n: n}
	seen := &transitions{}
	store := memrecords.New()
	ex, err := scheduler.NewExecutor(scheduler.Config[payload]{
		Kinds: faultKinds(f), Records: faultStore{Store: store, f: f},
		Observer: f.observer(seen), MaxQueued: 4,
	})
	if err != nil {
		t.Fatalf("NewExecutor = %v, want nil", err)
	}
	r := &rig{ex: ex, store: store}
	ctx, stop := context.WithCancel(t.Context())
	ran := make(chan struct{})
	t.Cleanup(func() {
		stop()
		<-ran
	})
	start := func(j scheduler.Job[payload]) (id scheduler.ID) {
		admit, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		f.try(t, func() {
			s, _ := ex.Start(admit, j)
			id = s.Job
		})
		synctest.Wait()
		return id
	}
	passes, maint := make(chan struct{}), make(chan struct{})

	// Started before Run, so no job begins while its Start is still
	// delivering: the counted calls keep one order.
	start(api("hold", "h", "carol", payload{Gate: passes}))
	start(api("grab", "", "alice", payload{N: 2}))
	start(api("park", "k", "gina", payload{Gate: maint}))
	go func() {
		defer close(ran)
		_ = ex.Run(ctx)
	}()
	synctest.Wait()

	followUp := start(api("hold", "h", "dave", payload{}))
	start(api("pipe", "p", "alice", payload{}))
	start(api("nap", "", "erin", payload{}))
	start(api("full", "", "bob", payload{}))
	start(api("part", "", "bob", payload{}))
	start(api("grab", "", "bob", payload{N: 1})) // past MaxQueued
	f.try(t, func() { _, _ = ex.Cancel(t.Context(), followUp) })
	f.try(t, func() { _, _ = ex.Cancel(t.Context(), followUp) })
	f.try(t, func() { ex.CredentialEnded("bob") })
	scheduled := false
	f.try(t, func() { scheduled = ex.Schedule("part", "tick", scheduler.Scheduled(time.Hour), time.Time{}) == nil })
	close(passes)
	synctest.Wait()
	time.Sleep(time.Minute)
	synctest.Wait()
	start(api("park", "k", "hank", payload{}))
	f.try(t, func() {
		resume, err := ex.Hold(t.Context(), true)
		if err == nil {
			resume()
		}
	})
	resumed := time.Now()
	close(maint)
	synctest.Wait()

	f.mu.Lock()
	f.done = true
	calls := slices.Clone(f.calls)
	f.mu.Unlock()
	if n > 0 && len(calls) < n {
		t.Fatalf("the scenario made %d counted calls, want at least %d", len(calls), n)
	}
	if live := ex.Live(); len(live) != 0 {
		t.Errorf("Live() = %v after the scenario, want no job left", live)
	}
	for _, id := range f.runPanicked {
		r.wantEnd(t, id, scheduler.ResultFailed, panicked)
		wantLastResult(t, seen, id, scheduler.ResultFailed)
	}
	for _, j := range []scheduler.Job[payload]{
		api("hold", "h", "zed", payload{}), api("pipe", "p", "zed", payload{}),
		api("grab", "", "zed", payload{N: 1}), api("nap", "", "zed", payload{}),
		api("full", "", "zed", payload{}), api("part", "", "zed", payload{}),
		api("park", "k", "zed", payload{}),
	} {
		admit, cancel := context.WithTimeout(t.Context(), time.Minute)
		s, err := ex.Start(admit, j)
		cancel()
		if err != nil || s.Joined {
			t.Errorf("Start(%s %q) after the scenario = (%+v, %v), want a new job", j.Kind, j.Scope, s, err)
			continue
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		r.wantEnd(t, s.Job, scheduler.ResultOK)
	}
	if scheduled {
		woke := func() bool {
			return slices.ContainsFunc(r.wakes("part", "tick"), func(rec scheduler.Record) bool {
				return rec.QueuedAt.After(resumed)
			})
		}
		for !woke() && time.Since(resumed) < 2*time.Hour {
			time.Sleep(time.Minute)
			synctest.Wait()
		}
		if !woke() {
			t.Error("part \"tick\" did not wake within 2 hours of the resume, want its timer armed")
		}
	}
	began := time.Now()
	resume, err := ex.Hold(t.Context(), true)
	if err != nil || time.Since(began) != 0 {
		t.Errorf("Hold(all) = %v after %v, want nil at once", err, time.Since(began))
	}
	if resume != nil {
		resume()
	}
	stop()
	select {
	case <-ran:
	case <-time.After(time.Minute):
		t.Error("Run did not return within a minute of its ctx ending")
	}
	return calls
}

// TestCallbackPanicLeavesExecutorUsable injects a panic into each call the
// executor makes into app code on a stack the app recovers, one call per
// case, and pins that the executor stays usable after it: no job is left
// queued or running, a job whose Run took the panic ends failed with
// run_panicked and the Observer gets that end, a new request of every kind
// runs, a scheduled scope still wakes, and Hold(all) and shutdown return.
func TestCallbackPanicLeavesExecutorUsable(t *testing.T) {
	var sites []string
	synctest.Test(t, func(t *testing.T) { sites = faultRun(t, 0) })
	for _, want := range []string{
		"Records.Insert in a call", "Records.Put in a call", "Records.End in a call",
		"Records.Get in a call", "Records.LastEnds in a call", "Records.Put in Run",
		"Observer.Transition in a call", "Observer.Transition in Run", "Observer.SlotWait in Run",
		"KindSpec.Fresh in a call", "KindSpec.Covers in a call", "KindSpec.Items in a call",
		"KindSpec.Watch in a call",
	} {
		if !slices.Contains(sites, want) {
			t.Errorf("the scenario reached %q, want it to reach %s", sites, want)
		}
	}
	for i, site := range sites {
		t.Run(fmt.Sprintf("call %02d %s", i+1, site), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if got := faultRun(t, i+1); got[i] != site {
					t.Errorf("call %d was %s, want %s as with no panic", i+1, got[i], site)
				}
			})
		})
	}
}
