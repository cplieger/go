package scheduler_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

// keyed is j carrying its owner's idempotency key with a day's window.
func keyed(j scheduler.Job[payload], key string) scheduler.Job[payload] {
	j.Key = &scheduler.Key{Credential: j.Owner, Row: "scan.start", Key: key, Fingerprint: [32]byte{1}, Window: 24 * time.Hour}
	return j
}

func TestKeyedStartAnswersTheStoredJob(t *testing.T) {
	t.Run("a keyed start writes its key, valid until its queued time plus the window", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			s := r.start(t, keyed(api("scan", "source:a", "alice", payload{}), "k1"))
			synctest.Wait()
			rec := r.rec(t, s.Job)
			if rec.Key == nil {
				t.Fatal("the record holds no key, want the start's")
			}
			k := *rec.Key
			if k.Credential != "alice" || k.Row != "scan.start" || k.Key != "k1" || k.Fingerprint != [32]byte{1} || k.Window != 24*time.Hour {
				t.Errorf("the stored key = %+v, want alice's k1 on scan.start with its fingerprint and a day's window", k)
			}
			if want := rec.QueuedAt.Add(24 * time.Hour); !k.Until.Equal(want) {
				t.Errorf("the key's Until = %v, want %v: its record's QueuedAt plus the window", k.Until, want)
			}
		})
	})

	t.Run("a resend after a restart answers the stored job and runs nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			cfg := scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}}
			before, stop := base.openUntil(t, cfg)
			gate := make(chan struct{})
			first := before.start(t, keyed(api("scan", "source:a", "alice", payload{Gate: gate}), "k1"))
			synctest.Wait()
			stop() // the restart finds the first job running

			reopened := &rig{store: base.store.Crash(), log: &runLog{}}
			reopened.store.Reopen()
			r := reopened.open(t, cfg)
			s := r.start(t, keyed(api("scan", "source:a", "alice", payload{}), "k1"))
			synctest.Wait()
			if s.Job != first.Job || !s.Joined || s.Position != 0 {
				t.Errorf("the resend = %+v, want the first job %s, joined", s, first.Job)
			}
			if ids := r.log.ids(); len(ids) != 0 {
				t.Errorf("Run called for %v after the restart, want never", ids)
			}
			if all := r.store.All(); len(all) != 1 {
				t.Errorf("%d records after the resend, want the first job's alone", len(all))
			}
			r.wantEnd(t, first.Job, scheduler.ResultInterrupted)
		})
	})

	t.Run("a resend while its job runs queues no follow-up", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			first := r.start(t, keyed(api("scan", "source:a", "alice", payload{Gate: gate}), "k1"))
			synctest.Wait()
			s := r.start(t, keyed(api("scan", "source:a", "alice", payload{}), "k1"))
			if s.Job != first.Job || !s.Joined {
				t.Errorf("the resend = %+v, want the running job %s, joined", s, first.Job)
			}
			if live := r.ex.Live(); len(live) != 1 {
				t.Errorf("%d live jobs after the resend, want the running one alone", len(live))
			}
			close(gate)
			synctest.Wait()
			if ids := r.log.ids(); len(ids) != 1 || ids[0] != first.Job {
				t.Errorf("Run called for %v, want the first job once", ids)
			}
			if all := r.store.All(); len(all) != 1 {
				t.Errorf("%d records, want the first job's alone", len(all))
			}
		})
	})

	t.Run("a start under another key runs its own job", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			first := r.start(t, keyed(api("scan", "source:a", "alice", payload{}), "k1"))
			synctest.Wait()
			s := r.start(t, keyed(api("scan", "source:a", "alice", payload{}), "k2"))
			synctest.Wait()
			if s.Job == first.Job || s.Joined {
				t.Errorf("a start under k2 = %+v, want a job of its own", s)
			}
			if ids := r.log.ids(); len(ids) != 2 {
				t.Errorf("Run called for %v, want both jobs", ids)
			}
		})
	})
}
