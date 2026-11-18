package scheduler_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

// payload is the test app's request type. Gate holds a job's Run until it is
// closed or the job's ctx ends; a nil Gate returns at once.
type payload struct {
	Gate <-chan struct{} `json:"-"`
	Conn int             `json:"conn,omitempty"`
	N    int             `json:"n,omitempty"`
}

// runLog is every job whose Run was called, in call order.
type runLog struct {
	jobs []scheduler.Job[payload]
	mu   sync.Mutex
}

func (l *runLog) add(j scheduler.Job[payload]) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.jobs = append(l.jobs, j)
}

func (l *runLog) ids() []scheduler.ID {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]scheduler.ID, 0, len(l.jobs))
	for _, j := range l.jobs {
		out = append(out, j.ID)
	}
	return out
}

func (l *runLog) ran(id scheduler.ID) bool { return slices.Contains(l.ids(), id) }

// work is the default Run: it logs the job, waits on its Gate and ends ok.
func work(log *runLog) func(context.Context, *scheduler.Run[payload]) scheduler.End {
	return func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		j := r.Job()
		log.add(j)
		if j.Payload.Gate != nil {
			select {
			case <-j.Payload.Gate:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
}

// spec is a kind that keeps its records for an hour; newRig gives it work
// as its Run when it has none.
func spec(kind scheduler.Kind, slot string) scheduler.KindSpec[payload] {
	return scheduler.KindSpec[payload]{Kind: kind, Slot: slot, Kept: time.Hour}
}

type rig struct {
	ex    *scheduler.Executor[payload]
	store *memrecords.Store
	log   *runLog
}

// newRig builds an executor over a fresh memrecords store and runs it until
// the test ends. Call it inside a synctest bubble.
func newRig(t *testing.T, cfg scheduler.Config[payload]) *rig {
	t.Helper()
	r := &rig{store: memrecords.New(), log: &runLog{}}
	return r.open(t, cfg)
}

// open runs a new executor of cfg over r's store and log until the test
// ends.
func (r *rig) open(t *testing.T, cfg scheduler.Config[payload]) *rig {
	t.Helper()
	opened, done := r.run(t, t.Context(), cfg)
	t.Cleanup(func() { <-done }) // t.Context ends before cleanups run
	return opened
}

// openUntil runs a new executor of cfg over r's store and log until stop,
// which cancels its Run and answers once Run has returned.
func (r *rig) openUntil(t *testing.T, cfg scheduler.Config[payload]) (opened *rig, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	opened, done := r.run(t, ctx, cfg)
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return opened, func() {
		cancel()
		<-done
	}
}

// run starts Run of a new executor of cfg over r's store and log; done
// closes when Run returns.
func (r *rig) run(t *testing.T, ctx context.Context, cfg scheduler.Config[payload]) (opened *rig, done <-chan struct{}) {
	t.Helper()
	cfg.Kinds = slices.Clone(cfg.Kinds)
	for i := range cfg.Kinds {
		if cfg.Kinds[i].Run == nil {
			cfg.Kinds[i].Run = work(r.log)
		}
	}
	cfg.Records = r.store
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	ex, err := scheduler.NewExecutor(cfg)
	if err != nil || ex == nil {
		t.Fatalf("NewExecutor = (%v, %v), want an executor", ex, err)
	}
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_ = ex.Run(ctx)
	}()
	return &rig{ex: ex, store: r.store, log: r.log}, ran
}

// schedule sets kind and scope's cadence and fails the test on an error.
func (r *rig) schedule(t *testing.T, kind scheduler.Kind, scope string, c scheduler.Cadence) {
	t.Helper()
	if err := r.ex.Schedule(kind, scope, c); err != nil {
		t.Fatalf("Schedule(%s %q) = %v, want nil", kind, scope, err)
	}
}

// wakes answers the records the timer of kind and scope wrote, in the order
// it wrote them.
func (r *rig) wakes(kind scheduler.Kind, scope string) []scheduler.Record {
	var out []scheduler.Record
	for _, rec := range r.store.All() {
		if rec.Kind == kind && rec.Scope == scope && rec.Trigger == scheduler.TriggerSchedule {
			out = append(out, rec)
		}
	}
	return out
}

// nextWake steps the clock a second at a time from just before the jitter
// band of a wake d after base, and answers the first timer record of kind and
// scope queued after base. It fails the test unless that record lies in the
// band, so a wake before it is caught too.
func (r *rig) nextWake(t *testing.T, kind scheduler.Kind, scope string, base time.Time, d time.Duration) scheduler.Record {
	t.Helper()
	lo, hi := jitterBand(d)
	sleepUntil(base.Add(lo - time.Second))
	for {
		for _, rec := range r.wakes(kind, scope) {
			if !rec.QueuedAt.After(base) {
				continue
			}
			if off := rec.QueuedAt.Sub(base); !inBand(off, d) {
				t.Fatalf("timer of %s %q woke %v after %v, want within [%v, %v]", kind, scope, off, base, lo, hi)
			}
			return rec
		}
		if time.Since(base) > hi {
			t.Fatalf("timer of %s %q did not wake within %v of %v, want a wake in [%v, %v]", kind, scope, hi, base, lo, hi)
		}
		time.Sleep(time.Second)
		synctest.Wait()
	}
}

// sleepUntil advances the bubble's clock to at and waits for every goroutine
// to block.
func sleepUntil(at time.Time) {
	time.Sleep(time.Until(at))
	synctest.Wait()
}

// jitterBand is the range a wake d after its base falls in: Scheduled
// jitters each wait by ±10 %.
func jitterBand(d time.Duration) (lo, hi time.Duration) { return d - d/10, d + d/10 }

// inBand reports whether got lies in d's jitter band.
func inBand(got, d time.Duration) bool {
	lo, hi := jitterBand(d)
	return got >= lo && got <= hi
}

// start answers Start's Started and fails the test on an error.
func (r *rig) start(t *testing.T, j scheduler.Job[payload]) scheduler.Started {
	t.Helper()
	s, err := r.ex.Start(t.Context(), j)
	if err != nil {
		t.Fatalf("Start(%s %q by %q) = %v, want nil", j.Kind, j.Scope, j.Owner, err)
	}
	return s
}

// rec answers the stored record of id and fails the test when there is none.
func (r *rig) rec(t *testing.T, id scheduler.ID) scheduler.Record {
	t.Helper()
	rec, ok, err := r.store.Get(t.Context(), id)
	if err != nil || !ok {
		t.Fatalf("record %q = (%t, %v), want stored", id, ok, err)
	}
	return rec
}

// wantEnd fails the test unless the record of id ended with result and
// exactly reasons.
func (r *rig) wantEnd(t *testing.T, id scheduler.ID, result scheduler.Result, reasons ...scheduler.Reason) {
	t.Helper()
	rec := r.rec(t, id)
	if rec.State != scheduler.StateEnded || rec.End == nil {
		t.Fatalf("record %q state %q end %v, want ended %s", id, rec.State, rec.End, result)
	}
	if rec.End.Result != result || !slices.Equal(rec.End.Reasons, reasons) {
		t.Errorf("record %q ended %s %v, want %s %v", id, rec.End.Result, rec.End.Reasons, result, reasons)
	}
}

// api is a person's request; owner is the starting credential.
func api(kind scheduler.Kind, scope, owner string, p payload) scheduler.Job[payload] {
	return scheduler.Job[payload]{Kind: kind, Scope: scope, Trigger: scheduler.TriggerAPI, Owner: owner, Payload: p}
}

// timer is the executor's own request, with no owner.
func timer(kind scheduler.Kind, scope string, p payload) scheduler.Job[payload] {
	return scheduler.Job[payload]{Kind: kind, Scope: scope, Trigger: scheduler.TriggerSchedule, Payload: p}
}

// order is the names a test's jobs log as they hold their slot.
type order struct {
	names []string
	mu    sync.Mutex
}

func (o *order) add(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.names = append(o.names, name)
}

func (o *order) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.names)
}

// logLines is a slog.Handler keeping each line the executor logs, without
// the attributes a With call adds.
type logLines struct {
	recs []slog.Record
	mu   sync.Mutex
}

func (l *logLines) Enabled(context.Context, slog.Level) bool { return true }

func (l *logLines) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r.Clone())
	return nil
}

func (l *logLines) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logLines) WithGroup(string) slog.Handler      { return l }

// count answers how many lines were logged at level.
func (l *logLines) count(level slog.Level) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.recs {
		if r.Level == level {
			n++
		}
	}
	return n
}

// text answers each line logged at level as its message and its attributes'
// values, space-separated.
func (l *logLines) text(level slog.Level) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.recs {
		if r.Level != level {
			continue
		}
		parts := []string{r.Message}
		r.Attrs(func(a slog.Attr) bool {
			parts = append(parts, a.Key+"="+a.Value.String())
			return true
		})
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

// transitions keeps each Transition the Observer is told of.
type transitions struct {
	all []scheduler.Transition
	mu  sync.Mutex
}

func (o *transitions) observer() scheduler.Observer {
	return scheduler.Observer{Transition: func(tr scheduler.Transition) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.all = append(o.all, tr)
	}}
}

// of answers id's transitions in the order the Observer was told of them.
func (o *transitions) of(id scheduler.ID) []scheduler.Transition {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []scheduler.Transition
	for _, tr := range o.all {
		if tr.Job == id {
			out = append(out, tr)
		}
	}
	return out
}
