package scheduler_test

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
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

// open runs a new executor of cfg over r's store and log.
func (r *rig) open(t *testing.T, cfg scheduler.Config[payload]) *rig {
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
	ctx := t.Context()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ex.Run(ctx)
	}()
	t.Cleanup(func() { <-done }) // t.Context ends before cleanups run
	return &rig{ex: ex, store: r.store, log: r.log}
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
