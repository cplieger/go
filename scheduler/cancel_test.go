package scheduler_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

func TestCancel(t *testing.T) {
	t.Run("a queued job ends cancelled without running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, api("scan", "a", "alice", payload{Gate: gate}))
			synctest.Wait()
			queued := r.start(t, api("scan", "b", "alice", payload{}))
			for i := range 2 {
				if stopping, err := r.ex.Cancel(t.Context(), queued.Job); err != nil || stopping {
					t.Errorf("cancel %d of a queued job = (%t, %v), want (false, nil)", i+1, stopping, err)
				}
			}
			synctest.Wait()
			r.wantEnd(t, queued.Job, scheduler.ResultCancelled)
			if rec := r.rec(t, queued.Job); !rec.StartedAt.IsZero() {
				t.Errorf("cancelled queued record started at %v, want never", rec.StartedAt)
			}
			close(gate)
			synctest.Wait()
			if got := r.log.ids(); !slices.Equal(got, []scheduler.ID{running.Job}) {
				t.Errorf("ran %v, want only %s", got, running.Job)
			}
			if stopping, err := r.ex.Cancel(t.Context(), queued.Job); err != nil || stopping {
				t.Errorf("a repeat after the end = (%t, %v), want the first answer (false, nil)", stopping, err)
			}
		})
	})

	t.Run("a running job ends cancelled at its phase and commits nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var committed atomic.Bool
			full := spec("full", "passes")
			full.Phases = []scheduler.Phase{"fetched", "published"}
			full.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				run.Reached("fetched")
				<-ctx.Done()
				committed.Store(run.Commit())
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full, spec("quick", "passes")}})
			running := r.start(t, api("full", "", "alice", payload{}))
			synctest.Wait()
			next := r.start(t, api("quick", "", "alice", payload{}))
			for i := range 2 {
				if stopping, err := r.ex.Cancel(t.Context(), running.Job); err != nil || !stopping {
					t.Errorf("cancel %d of a running job = (%t, %v), want (true, nil)", i+1, stopping, err)
				}
			}
			synctest.Wait()
			r.wantEnd(t, running.Job, scheduler.ResultCancelled)
			if rec := r.rec(t, running.Job); rec.Phase != "fetched" || rec.Step.N != 1 || rec.EndedAt.IsZero() {
				t.Errorf("cancelled record at phase %q step %v ended %v, want ended at phase fetched, step 1", rec.Phase, rec.Step, rec.EndedAt)
			}
			if committed.Load() {
				t.Error("Commit() = true after Cancel, want false")
			}
			r.wantEnd(t, next.Job, scheduler.ResultOK)
			if stopping, err := r.ex.Cancel(t.Context(), running.Job); err != nil || !stopping {
				t.Errorf("a repeat after the end = (%t, %v), want the first answer (true, nil)", stopping, err)
			}
		})
	})

	t.Run("an ended job is not cancellable", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}})
			done := r.start(t, api("scan", "", "alice", payload{}))
			synctest.Wait()
			if _, err := r.ex.Cancel(t.Context(), done.Job); !errors.Is(err, scheduler.ErrNotCancellable) {
				t.Errorf("Cancel(an ended job) = %v, want ErrNotCancellable", err)
			}
		})
	})

	t.Run("a failed read of an ended job's record", func(t *testing.T) {
		for name, tc := range map[string]struct {
			cause, want, notWant error
		}{
			"answers store_unavailable":      {cause: errors.New("disk gone"), want: scheduler.ErrStoreUnavailable},
			"of a context answers its error": {cause: context.Canceled, want: context.Canceled, notWant: scheduler.ErrStoreUnavailable},
		} {
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}})
					done := r.start(t, api("scan", "", "alice", payload{}))
					synctest.Wait()
					r.store.Fail(memrecords.OpGet, tc.cause)
					_, err := r.ex.Cancel(t.Context(), done.Job)
					if !errors.Is(err, tc.want) || (tc.notWant != nil && errors.Is(err, tc.notWant)) {
						t.Errorf("Cancel(an ended job) with its record read failing on %v = %v, want %v and not %v", tc.cause, err, tc.want, tc.notWant)
					}
				})
			})
		}
	})

	t.Run("an unknown id", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "")}})
			for _, id := range []scheduler.ID{"", scheduler.ID(strings.Repeat("ab", 16))} {
				if _, err := r.ex.Cancel(t.Context(), id); !errors.Is(err, scheduler.ErrUnknownJob) {
					t.Errorf("Cancel(%q) = %v, want ErrUnknownJob", id, err)
				}
			}
		})
	})
}

func TestCancelRefusedDuringCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		var stopped atomic.Bool
		full := spec("full", "passes")
		full.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
			if !run.Commit() {
				return scheduler.End{Result: scheduler.ResultFailed}
			}
			<-gate
			stopped.Store(ctx.Err() != nil)
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{full}})
		s := r.start(t, api("full", "", "alice", payload{}))
		synctest.Wait()
		if stopping, err := r.ex.Cancel(t.Context(), s.Job); !errors.Is(err, scheduler.ErrCommitting) || stopping {
			t.Errorf("Cancel during the commit = (%t, %v), want (false, ErrCommitting)", stopping, err)
		}
		close(gate)
		synctest.Wait()
		if stopped.Load() {
			t.Error("the committing job's ctx was cancelled, want it left to complete")
		}
		r.wantEnd(t, s.Job, scheduler.ResultOK)
		if rec := r.rec(t, s.Job); rec.Phase != scheduler.PhaseCommitting {
			t.Errorf("record phase = %q, want %q", rec.Phase, scheduler.PhaseCommitting)
		}
		if _, err := r.ex.Cancel(t.Context(), s.Job); !errors.Is(err, scheduler.ErrNotCancellable) {
			t.Errorf("Cancel after the commit ended = %v, want ErrNotCancellable", err)
		}
	})
}

func TestCredentialEndedStopsOnBehalfOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		grab := spec("grab", "arr")
		grab.OnBehalf = true
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{grab, spec("scan", "arr")}})
		gate := make(chan struct{})
		running := r.start(t, api("grab", "a", "alice", payload{Gate: gate}))
		synctest.Wait()
		queued := r.start(t, api("grab", "b", "alice", payload{}))
		bobs := r.start(t, api("grab", "c", "bob", payload{}))
		scan := r.start(t, api("scan", "d", "alice", payload{}))

		r.ex.CredentialEnded("alice")
		synctest.Wait()
		ended := scheduler.Reason{Code: scheduler.ReasonCredentialEnded}
		r.wantEnd(t, running.Job, scheduler.ResultCancelled, ended)
		r.wantEnd(t, queued.Job, scheduler.ResultCancelled, ended)
		r.wantEnd(t, bobs.Job, scheduler.ResultOK)
		r.wantEnd(t, scan.Job, scheduler.ResultOK)
		if r.log.ran(queued.Job) {
			t.Error("alice's queued grab ran, want it ended without running")
		}
		close(gate)
	})
}

// A job whose record is still being written when its credential ends never
// runs: its admission ends it as the credential's end would have.
func TestCredentialEndedDuringAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &heldRecords{Store: memrecords.New(), entered: make(chan struct{}), hold: make(chan struct{})}
		grab := spec("grab", "arr")
		grab.OnBehalf = true
		r := (&rig{store: g.Store, log: &runLog{}}).open(t, scheduler.Config[payload]{
			Kinds:   []scheduler.KindSpec[payload]{grab},
			Records: g,
		})
		started := startAsync(t.Context(), r.ex, api("grab", "a", "alice", payload{}))
		<-g.entered
		r.ex.CredentialEnded("alice")
		close(g.hold)
		a := <-started
		synctest.Wait()
		if a.err != nil || a.s.Job == "" {
			t.Fatalf("Start = (%+v, %v), want the admitted job with a nil error", a.s, a.err)
		}
		if r.log.ran(a.s.Job) {
			t.Error("the grab ran after its credential ended, want it ended without running")
		}
		r.wantEnd(t, a.s.Job, scheduler.ResultCancelled, scheduler.Reason{Code: scheduler.ReasonCredentialEnded})
		if rec := r.rec(t, a.s.Job); !rec.StartedAt.IsZero() {
			t.Errorf("record StartedAt = %v, want zero for a job that never started", rec.StartedAt)
		}
		if n := len(r.ex.Live()); n != 0 {
			t.Errorf("%d live jobs, want none", n)
		}
		if stopping, err := r.ex.Cancel(t.Context(), a.s.Job); stopping || err != nil {
			t.Errorf("Cancel after the credential's end = (%t, %v), want (false, nil) as a repeat cancel", stopping, err)
		}
	})
}
