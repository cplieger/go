package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
)

func TestCoalescingRules(t *testing.T) {
	t.Run("rule1", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, timer("scan", "source:a", payload{Gate: gate}))
			synctest.Wait()
			y := r.start(t, api("scan", "host:3/posture", "alice", payload{}))
			z := r.start(t, api("scan", "source:z", "alice", payload{}))
			if y.Joined || y.Position != 0 || z.Joined || z.Position != 1 {
				t.Errorf("two queued starts = %+v and %+v, want positions 0 and 1, neither joined", y, z)
			}
			againY := r.start(t, api("scan", "host:3/posture", "bob", payload{N: 9}))
			againZ := r.start(t, timer("scan", "source:z", payload{}))
			if want := (scheduler.Started{Job: y.Job, Position: 0, Joined: true}); againY != want {
				t.Errorf("a repeat of the first queued scope = %+v, want %+v", againY, want)
			}
			if want := (scheduler.Started{Job: z.Job, Position: 1, Joined: true}); againZ != want {
				t.Errorf("a repeat of the second queued scope = %+v, want %+v", againZ, want)
			}
			if n := len(r.store.All()); n != 3 {
				t.Errorf("%d records, want 3: a joined start writes none", n)
			}

			if _, err := r.ex.Cancel(t.Context(), z.Job); err != nil {
				t.Fatalf("Cancel(queued) = %v", err)
			}
			afterCancel := r.start(t, api("scan", "source:z", "alice", payload{}))
			if afterCancel.Joined || afterCancel.Job == z.Job {
				t.Errorf("a start after the queued job was cancelled = %+v, want a new job", afterCancel)
			}
			close(gate)
			synctest.Wait()
			if got, want := r.log.ids(), []scheduler.ID{running.Job, y.Job, afterCancel.Job}; !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
		})
	})

	t.Run("rule2", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			full := spec("full", "passes")
			full.Covers = func(_, other scheduler.Job[payload]) bool { return other.Kind == "quick" }
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes"), full}})
			gate := make(chan struct{})
			r.start(t, timer("quick", "", payload{Gate: gate}))
			synctest.Wait()
			queued := r.start(t, api("full", "", "alice", payload{}))
			quick := r.start(t, api("quick", "", "bob", payload{}))
			if want := (scheduler.Started{Job: queued.Job, Position: 0, Joined: true}); quick != want {
				t.Errorf("a quick check while a full check is queued = %+v, want %+v", quick, want)
			}
			if n := len(r.store.All()); n != 2 {
				t.Errorf("%d records, want 2: a covered start writes none", n)
			}
			close(gate)
		})
	})

	t.Run("rule3", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, api("scan", "source:a", "alice", payload{Gate: gate}))
			synctest.Wait()
			follow := r.start(t, api("scan", "source:a", "bob", payload{}))
			if follow.Joined || follow.Job == running.Job || follow.Position != 0 {
				t.Errorf("a start while its scope runs = %+v, want one follow-up queued at 0", follow)
			}
			again := r.start(t, api("scan", "source:a", "carol", payload{}))
			if want := (scheduler.Started{Job: follow.Job, Joined: true}); again != want {
				t.Errorf("a further start = %+v, want it to join the follow-up %+v", again, want)
			}
			close(gate)
			synctest.Wait()
			if got, want := r.log.ids(), []scheduler.ID{running.Job, follow.Job}; !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
		})
	})

	t.Run("rule3fresh", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			refresh := spec("refresh", "")
			refresh.Fresh = forgeFresh
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}})
			gate := make(chan struct{})
			running := r.start(t, api("refresh", "", "alice", payload{Gate: gate}))
			synctest.Wait()
			time.Sleep(59 * time.Second)
			joined := r.start(t, api("refresh", "", "bob", payload{}))
			if want := (scheduler.Started{Job: running.Job, Joined: true}); joined != want {
				t.Errorf("a start 59 s into a run under a 60 s window = %+v, want %+v", joined, want)
			}
			time.Sleep(time.Second)
			r.followUp(t, running.Job, api("refresh", "", "carol", payload{}), api("refresh", "", "dave", payload{}))
			close(gate)
		})
	})

	t.Run("rule3noslot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("refresh", "")}})
			gate := make(chan struct{})
			running := r.start(t, api("refresh", "connection:3", "alice", payload{Gate: gate}))
			synctest.Wait()
			follow := r.followUp(t, running.Job,
				api("refresh", "connection:3", "bob", payload{}), timer("refresh", "connection:3", payload{}))
			close(gate)
			synctest.Wait()
			if got, want := r.log.ids(), []scheduler.ID{running.Job, follow.Job}; !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
		})
	})

	t.Run("rule3batch", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			base := &rig{store: memrecords.New(), log: &runLog{}}
			r := base.open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{gatedBatch(base.log)}})
			gate := make(chan struct{})
			running := r.start(t, api("grab", "source:a", "alice", payload{Gate: gate, N: 2}))
			synctest.Wait()
			follow := r.followUp(t, running.Job,
				api("grab", "source:a", "bob", payload{N: 2}), api("grab", "source:a", "carol", payload{N: 3}))
			close(gate)
			synctest.Wait()
			if got, want := r.log.ids(), []scheduler.ID{running.Job, follow.Job}; !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
			r.wantEnd(t, follow.Job, scheduler.ResultOK)
		})
	})

	t.Run("rule3freshwindow", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			refresh := spec("refresh", "")
			refresh.Fresh = forgeFresh
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}})
			gate := make(chan struct{})
			scope := "repo:cplieger/forgeapi"
			running := r.start(t, api("refresh", scope, "alice", payload{Gate: gate}))
			synctest.Wait()
			time.Sleep(15 * time.Second)
			follow := r.followUp(t, running.Job, api("refresh", scope, "bob", payload{}), api("refresh", scope, "carol", payload{}))
			time.Sleep(time.Minute)
			if later := r.start(t, api("refresh", scope, "dave", payload{})); later != (scheduler.Started{Job: follow.Job, Joined: true}) {
				t.Errorf("a start a minute behind the follow-up = %+v, want it to join the follow-up %s", later, follow.Job)
			}
			synctest.Wait()
			if r.log.ran(follow.Job) {
				t.Errorf("the follow-up ran while %s runs, want it queued", running.Job)
			}
			close(gate)
			synctest.Wait()
			if got, want := r.log.ids(), []scheduler.ID{running.Job, follow.Job}; !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
		})
	})

	t.Run("rule3yield", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			yielded := make(chan struct{})
			refresh := spec("refresh", "")
			base := &rig{store: memrecords.New(), log: &runLog{}}
			refresh.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
				base.log.add(run.Job())
				if run.Job().Owner == "alice" {
					close(yielded)
					_ = run.Yield(ctx, time.Now().Add(time.Hour))
				}
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := base.open(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh}})
			first := r.start(t, api("refresh", "connection:3", "alice", payload{}))
			<-yielded
			synctest.Wait()
			follow := r.start(t, api("refresh", "connection:3", "bob", payload{}))
			synctest.Wait()
			if follow.Joined || !r.log.ran(follow.Job) {
				t.Errorf("a start while its scope's job yields = %+v, ran %t; want a new job that runs", follow, r.log.ran(follow.Job))
			}
			if !r.ex.Wake(first.Job) {
				t.Errorf("Wake(%s) = false, want the yielded job woken", first.Job)
			}
			synctest.Wait()
			r.wantEnd(t, first.Job, scheduler.ResultOK)
		})
	})

	t.Run("rule3followupleaves", func(t *testing.T) {
		kinds := func() []scheduler.KindSpec[payload] {
			refresh := spec("refresh", "")
			refresh.Covers = forgeCovers
			return []scheduler.KindSpec[payload]{refresh}
		}
		// queue runs a gated refresh of repo:a/b and queues its follow-up.
		queue := func(t *testing.T, r *rig, gate <-chan struct{}) (running, follow scheduler.Started) {
			t.Helper()
			running = r.start(t, api("refresh", "repo:a/b", "alice", payload{Gate: gate, Conn: 3}))
			synctest.Wait()
			follow = r.start(t, api("refresh", "repo:a/b", "bob", payload{Conn: 3}))
			synctest.Wait()
			if r.log.ran(follow.Job) {
				t.Fatalf("the follow-up %s ran while %s runs, want it queued", follow.Job, running.Job)
			}
			return running, follow
		}
		wantNeverRan := func(t *testing.T, r *rig, id scheduler.ID) {
			t.Helper()
			if rec := r.rec(t, id); !rec.StartedAt.IsZero() || r.log.ran(id) {
				t.Errorf("follow-up record = %+v, want it never started", rec)
			}
		}

		t.Run("cancel", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, scheduler.Config[payload]{Kinds: kinds()})
				gate := make(chan struct{})
				running, follow := queue(t, r, gate)
				if stopping, err := r.ex.Cancel(t.Context(), follow.Job); stopping || err != nil {
					t.Errorf("Cancel(follow-up) = (%t, %v), want (false, nil): it is queued", stopping, err)
				}
				synctest.Wait()
				r.wantEnd(t, follow.Job, scheduler.ResultCancelled)
				wantNeverRan(t, r, follow.Job)
				again := r.start(t, api("refresh", "repo:a/b", "carol", payload{Conn: 3}))
				if again.Joined || again.Job == follow.Job {
					t.Errorf("a start after the follow-up was cancelled = %+v, want a new follow-up", again)
				}
				close(gate)
				synctest.Wait()
				if got, want := r.log.ids(), []scheduler.ID{running.Job, again.Job}; !slices.Equal(got, want) {
					t.Errorf("ran %v, want %v", got, want)
				}
			})
		})

		t.Run("cover", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, scheduler.Config[payload]{Kinds: kinds()})
				gate := make(chan struct{})
				_, follow := queue(t, r, gate)
				conn := r.start(t, api("refresh", "connection:3", "carol", payload{Conn: 3}))
				synctest.Wait()
				r.wantEnd(t, follow.Job, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(conn.Job)})
				wantNeverRan(t, r, follow.Job)
				close(gate)
			})
		})

		t.Run("hold", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, scheduler.Config[payload]{Kinds: kinds()})
				gate := make(chan struct{})
				_, follow := queue(t, r, gate)
				resume, err := r.ex.Hold(t.Context(), true)
				if err != nil {
					t.Fatalf("Hold(all) = %v", err)
				}
				defer resume()
				r.wantEnd(t, follow.Job, scheduler.ResultInterrupted)
				wantNeverRan(t, r, follow.Job)
				close(gate)
			})
		})

		t.Run("shutdown", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, stop := (&rig{store: memrecords.New(), log: &runLog{}}).openUntil(t, scheduler.Config[payload]{Kinds: kinds()})
				gate := make(chan struct{})
				defer close(gate)
				_, follow := queue(t, r, gate)
				stop()
				r.wantEnd(t, follow.Job, scheduler.ResultInterrupted)
				wantNeverRan(t, r, follow.Job)
			})
		})
	})

	t.Run("rule4", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			refresh := spec("refresh", "")
			refresh.Fresh = forgeFresh
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{refresh, spec("scan", "")}})
			for _, tc := range []struct {
				scope  string
				window time.Duration
			}{{"", 60 * time.Second}, {"repo:cplieger/forgeapi", 15 * time.Second}} {
				done := r.start(t, api("refresh", tc.scope, "alice", payload{}))
				synctest.Wait()
				ended := r.rec(t, done.Job).EndedAt
				time.Sleep(5 * time.Second)
				before := len(r.store.All())
				_, err := r.ex.Start(t.Context(), api("refresh", tc.scope, "bob", payload{}))
				var recent *scheduler.RecentError
				if !errors.As(err, &recent) || !errors.Is(err, scheduler.ErrRecent) {
					t.Fatalf("scope %q 5 s after an end = %v, want *RecentError", tc.scope, err)
				}
				if want := ended.Add(tc.window); !recent.Until.Equal(want) {
					t.Errorf("scope %q Until = %v, want the end plus %v, %v", tc.scope, recent.Until, tc.window, want)
				}
				if n := len(r.store.All()); n != before {
					t.Errorf("scope %q: a refused start wrote %d records", tc.scope, n-before)
				}
				time.Sleep(time.Until(recent.Until))
				if s := r.start(t, api("refresh", tc.scope, "bob", payload{})); s.Joined {
					t.Errorf("scope %q at the window's end = %+v, want a new job", tc.scope, s)
				}
				synctest.Wait()
			}
			r.start(t, api("scan", "", "alice", payload{}))
			synctest.Wait()
			if _, err := r.ex.Start(t.Context(), api("scan", "", "alice", payload{})); err != nil {
				t.Errorf("a kind with no Fresh right after an end = %v, want nil", err)
			}
		})
	})

	t.Run("rule5", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy"), spec("grab", "heavy")}})
			gate := make(chan struct{})
			r.start(t, timer("scan", "source:a", payload{Gate: gate}))
			synctest.Wait()
			first := r.start(t, api("grab", "source:a", "alice", payload{N: 3}))
			second := r.start(t, api("scan", "source:b", "alice", payload{}))
			if first.Joined || first.Position != 0 || second.Joined || second.Position != 1 {
				t.Errorf("two new starts = %+v and %+v, want queued at 0 and 1", first, second)
			}
			rec := r.rec(t, first.Job)
			if rec.ID != first.Job || rec.Kind != "grab" || rec.Scope != "source:a" || rec.Trigger != scheduler.TriggerAPI ||
				rec.Owner != "alice" || rec.State != scheduler.StateQueued || rec.Phase != scheduler.PhaseQueued ||
				!rec.QueuedAt.Equal(time.Now()) || string(rec.Payload) != `{"n":3}` {
				t.Errorf("queued record = %+v, want alice's grab of source:a queued now with payload {\"n\":3}", rec)
			}
			close(gate)
		})
	})

	t.Run("noslot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("refresh", "")}})
			gate := make(chan struct{})
			a := r.start(t, api("refresh", "connection:3", "alice", payload{Gate: gate}))
			b := r.start(t, api("refresh", "connection:4", "alice", payload{Gate: gate}))
			synctest.Wait()
			if got := r.log.ids(); len(got) != 2 {
				t.Errorf("ran %v, want both jobs running at once", got)
			}
			for _, s := range []scheduler.Started{a, b} {
				if s.Position != 0 || s.Joined {
					t.Errorf("start = %+v, want position 0", s)
				}
				if rec := r.rec(t, s.Job); rec.State != scheduler.StateRunning || rec.StartedAt.IsZero() {
					t.Errorf("record = %+v, want running", rec)
				}
			}
			close(gate)
		})
	})

	t.Run("timer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy")}})
			gate := make(chan struct{})
			running := r.start(t, api("scan", "source:a", "alice", payload{Gate: gate}))
			synctest.Wait()
			if err := r.ex.Schedule("scan", "source:a", scheduler.Scheduled(time.Hour), time.Time{}); err != nil {
				t.Fatalf("Schedule = %v", err)
			}
			if s := r.start(t, timer("scan", "source:a", payload{})); s != (scheduler.Started{Job: running.Job, Joined: true}) {
				t.Errorf("a timer request while its scope runs = %+v, want the running job %s", s, running.Job)
			}
			time.Sleep(3 * time.Hour)
			synctest.Wait()
			if n := r.store.Count(memrecords.OpInsert); n != 1 {
				t.Errorf("%d record inserts over three hourly wakes, want 1: a busy wake submits nothing", n)
			}
			close(gate)
		})
	})
}

// forgeFresh is a refresh's window: 60 s for every connection, 15 s for one.
func forgeFresh(scope string) time.Duration {
	if scope == "" {
		return 60 * time.Second
	}
	return 15 * time.Second
}

// followUp starts second while the job running runs and wants it queued as
// that scope's follow-up, then wants third to join it, neither running.
func (r *rig) followUp(t *testing.T, running scheduler.ID, second, third scheduler.Job[payload]) scheduler.Started {
	t.Helper()
	follow := r.start(t, second)
	if follow.Joined || follow.Job == running || follow.Position != 0 {
		t.Errorf("a start while its scope runs = %+v, want one follow-up queued at 0", follow)
	}
	synctest.Wait()
	if rec := r.rec(t, follow.Job); rec.State != scheduler.StateQueued || r.log.ran(follow.Job) {
		t.Errorf("follow-up record state %s, ran %t; want it queued behind %s", rec.State, r.log.ran(follow.Job), running)
	}
	if l, ok := r.ex.Live()[follow.Job]; !ok || l.Position != 0 {
		t.Errorf("Live()[follow-up] = (%+v, %t), want it live at position 0", l, ok)
	}
	if again := r.start(t, third); again != (scheduler.Started{Job: follow.Job, Joined: true}) {
		t.Errorf("a start behind the follow-up = %+v, want it to join the follow-up %s", again, follow.Job)
	}
	synctest.Wait()
	if got := r.log.ids(); slices.ContainsFunc(got, func(id scheduler.ID) bool { return id != running }) {
		t.Errorf("ran %v while %s runs, want only it", got, running)
	}
	return follow
}

// gatedBatch is a batch kind of up to 3 items in slot "arr", Payload.N items
// per job, whose Run logs the job to log and whose items wait on its Gate.
func gatedBatch(log *runLog) scheduler.KindSpec[payload] {
	k := spec("grab", "arr")
	k.MaxItems = 3
	k.Items = func(p payload) int { return p.N }
	k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		j := r.Job()
		log.add(j)
		for i := range j.Payload.N {
			_ = r.Item(ctx, i, func(ctx context.Context) scheduler.Line {
				if j.Payload.Gate != nil {
					select {
					case <-j.Payload.Gate:
					case <-ctx.Done():
					}
				}
				return scheduler.Line{State: scheduler.LineDone}
			})
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

// forgeCovers is a refresh's covering: all covers a connection and a
// repository, and a connection covers its own repositories.
func forgeCovers(started, other scheduler.Job[payload]) bool {
	switch {
	case started.Scope == other.Scope:
		return false
	case started.Scope == "":
		return true
	case strings.HasPrefix(started.Scope, "connection:"):
		return strings.HasPrefix(other.Scope, "repo:") && started.Payload.Conn == other.Payload.Conn
	}
	return false
}

func TestCoveringEndsCoveredSkipped(t *testing.T) {
	kinds := func() []scheduler.KindSpec[payload] {
		refresh := spec("refresh", "net")
		refresh.Covers = forgeCovers
		return []scheduler.KindSpec[payload]{refresh, spec("hold", "net")}
	}

	t.Run("a connection covers its repositories", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds()})
			gate := make(chan struct{})
			r.start(t, timer("hold", "", payload{Gate: gate}))
			synctest.Wait()
			mine := r.start(t, timer("refresh", "repo:a/b", payload{Conn: 3}))
			other := r.start(t, timer("refresh", "repo:c/d", payload{Conn: 4}))
			conn := r.start(t, api("refresh", "connection:3", "alice", payload{Conn: 3}))
			close(gate)
			synctest.Wait()

			r.wantEnd(t, mine.Job, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(conn.Job)})
			if rec := r.rec(t, mine.Job); !rec.StartedAt.IsZero() || r.log.ran(mine.Job) {
				t.Errorf("covered record = %+v, want it never started", rec)
			}
			r.wantEnd(t, conn.Job, scheduler.ResultOK)
			r.wantEnd(t, other.Job, scheduler.ResultOK)
			if !r.log.ran(other.Job) {
				t.Error("another connection's repository did not run, want it run")
			}
		})
	})

	t.Run("all covers each connection and repository", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds()})
			gate := make(chan struct{})
			r.start(t, timer("hold", "", payload{Gate: gate}))
			synctest.Wait()
			repo := r.start(t, timer("refresh", "repo:a/b", payload{Conn: 3}))
			conn := r.start(t, timer("refresh", "connection:3", payload{Conn: 3}))
			if joined := r.start(t, timer("refresh", "repo:e/f", payload{Conn: 3})); joined != (scheduler.Started{Job: conn.Job, Position: 1, Joined: true}) {
				t.Errorf("a repository of a queued connection = %+v, want it joined to the connection", joined)
			}
			allGate := make(chan struct{})
			all := r.start(t, api("refresh", "", "alice", payload{Gate: allGate}))
			close(gate)
			synctest.Wait()

			by := scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(all.Job)}
			r.wantEnd(t, repo.Job, scheduler.ResultSkipped, by)
			r.wantEnd(t, conn.Job, scheduler.ResultSkipped, by)

			later := r.start(t, timer("refresh", "repo:a/b", payload{Conn: 3}))
			close(allGate)
			synctest.Wait()
			r.wantEnd(t, later.Job, scheduler.ResultOK)
			if got := r.log.ids(); !slices.Contains(got, later.Job) || slices.Contains(got, repo.Job) || slices.Contains(got, conn.Job) {
				t.Errorf("ran %v, want the request queued after all started run and the covered ones not", got)
			}
		})
	})
}

// A request whose record is still being written when a job covering it
// starts ends covered once the write returns, and never runs.
func TestCoverEndsJobBeingAdmitted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &heldRecords{Store: memrecords.New(), entered: make(chan struct{}), hold: make(chan struct{})}
		refresh := spec("refresh", "net")
		refresh.Covers = forgeCovers
		r := (&rig{store: g.Store, log: &runLog{}}).open(t, scheduler.Config[payload]{
			Kinds:   []scheduler.KindSpec[payload]{refresh},
			Records: g,
		})
		mine := startAsync(t.Context(), r.ex, api("refresh", "repo:a/b", "alice", payload{Conn: 3}))
		<-g.entered
		gate := make(chan struct{})
		conn := r.start(t, api("refresh", "connection:3", "bob", payload{Conn: 3, Gate: gate}))
		synctest.Wait()
		if !r.log.ran(conn.Job) {
			t.Fatal("the covering refresh did not start while the covered record was being written")
		}
		close(g.hold)
		a := <-mine
		synctest.Wait()
		if a.err != nil || a.s.Job == "" || a.s.Joined {
			t.Fatalf("covered Start = (%+v, %v), want its own job with a nil error", a.s, a.err)
		}
		r.wantEnd(t, a.s.Job, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonCovered, Subject: string(conn.Job)})
		if rec := r.rec(t, a.s.Job); !rec.StartedAt.IsZero() || r.log.ran(a.s.Job) {
			t.Errorf("covered record = %+v, want it never started", rec)
		}
		close(gate)
		synctest.Wait()
		r.wantEnd(t, conn.Job, scheduler.ResultOK)
		if r.log.ran(a.s.Job) {
			t.Error("the covered refresh ran after the covering one, want it never run")
		}
	})
}

func TestAdmissionWritesRecordFirst(t *testing.T) {
	errDisk := errors.New("disk full")

	t.Run("the record exists when Start answers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("check", "")}})
			s := r.start(t, api("check", "", "alice", payload{}))
			if rec, ok, err := r.store.Get(t.Context(), s.Job); err != nil || !ok || rec.ID != s.Job {
				t.Errorf("record of the answered start = (%+v, %t, %v), want it stored", rec, ok, err)
			}
		})
	})

	t.Run("a failing write answers store_unavailable and queues nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("check", "")}})
			r.store.Fail(memrecords.OpInsert, errDisk)
			if _, err := r.ex.Start(t.Context(), api("check", "", "alice", payload{})); !errors.Is(err, scheduler.ErrStoreUnavailable) {
				t.Errorf("Start with its record write failing = %v, want ErrStoreUnavailable", err)
			}
			synctest.Wait()
			if got := r.log.ids(); len(got) != 0 {
				t.Errorf("ran %v, want nothing", got)
			}
			if n := len(r.store.All()); n != 0 {
				t.Errorf("%d records, want none", n)
			}
			r.store.Fail(memrecords.OpInsert, nil)
			s := r.start(t, api("check", "", "alice", payload{}))
			synctest.Wait()
			if got := r.log.ids(); !slices.Equal(got, []scheduler.ID{s.Job}) {
				t.Errorf("ran %v after the store recovered, want only %s", got, s.Job)
			}
		})
	})

	t.Run("a write the caller's context ended answers that context's error", func(t *testing.T) {
		for name, cause := range map[string]error{
			"cancelled":         context.Canceled,
			"deadline, wrapped": fmt.Errorf("insert: %w", context.DeadlineExceeded),
		} {
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("check", "")}})
					r.store.Fail(memrecords.OpInsert, cause)
					_, err := r.ex.Start(t.Context(), api("check", "", "alice", payload{}))
					if !errors.Is(err, cause) || errors.Is(err, scheduler.ErrStoreUnavailable) {
						t.Errorf("Start with its record write ending on %v = %v, want that error and not ErrStoreUnavailable", cause, err)
					}
				})
			})
		}
	})

	t.Run("a store with no room answers ErrCapacity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("check", "")}})
			r.store.Capacity(1)
			r.start(t, api("check", "a", "alice", payload{}))
			synctest.Wait()
			_, err := r.ex.Start(t.Context(), api("check", "b", "alice", payload{}))
			if !errors.Is(err, scheduler.ErrCapacity) || errors.Is(err, scheduler.ErrStoreUnavailable) {
				t.Errorf("Start on a full store = %v, want ErrCapacity and not ErrStoreUnavailable", err)
			}
			synctest.Wait()
			if n := len(r.log.ids()); n != 1 {
				t.Errorf("ran %d jobs, want 1", n)
			}
		})
	})

	t.Run("a kind that keeps no record writes none", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sweep := spec("sweep", "")
			sweep.Kept = 0
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{sweep}})
			r.store.Fail(memrecords.OpInsert, errDisk)
			s := r.start(t, timer("sweep", "", payload{}))
			synctest.Wait()
			if !r.log.ran(s.Job) {
				t.Error("the sweep did not run, want it run")
			}
			if n := r.store.Count(memrecords.OpInsert); n != 0 {
				t.Errorf("%d record inserts for a kind with Kept 0, want none", n)
			}
		})
	})
}

func TestQueueBounds(t *testing.T) {
	kinds := []scheduler.KindSpec[payload]{spec("scan", "heavy")}

	// hold starts a job that keeps the slot and answers its gate.
	hold := func(t *testing.T, r *rig, owner string) chan struct{} {
		t.Helper()
		gate := make(chan struct{})
		r.start(t, api("scan", "running", owner, payload{Gate: gate}))
		synctest.Wait()
		return gate
	}
	wantRefusedRecord := func(t *testing.T, r *rig) {
		t.Helper()
		all := r.store.All()
		last := all[len(all)-1]
		r.wantEnd(t, last.ID, scheduler.ResultSkipped, scheduler.Reason{Code: scheduler.ReasonQueueFull})
		if !last.StartedAt.IsZero() {
			t.Errorf("refused record started at %v, want never", last.StartedAt)
		}
	}

	t.Run("the seventeenth waiting job is refused", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds})
			gate := hold(t, r, "o9")
			for i := range 16 {
				r.start(t, api("scan", fmt.Sprintf("q:%d", i), fmt.Sprintf("o%d", i%4), payload{}))
			}
			if _, err := r.ex.Start(t.Context(), api("scan", "q:16", "o4", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("a 17th waiting job = %v, want ErrQueueFull", err)
			}
			if n := len(r.store.All()); n != 18 {
				t.Fatalf("%d records, want 18: the refused start's record is written", n)
			}
			wantRefusedRecord(t, r)
			close(gate)
		})
	})

	t.Run("four per owner, and joining takes no place", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds})
			gate := hold(t, r, "alice")
			for i := range 4 {
				r.start(t, api("scan", fmt.Sprintf("q:%d", i), "alice", payload{}))
			}
			if _, err := r.ex.Start(t.Context(), api("scan", "q:4", "alice", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("alice's 5th waiting job = %v, want ErrQueueFull", err)
			}
			wantRefusedRecord(t, r)
			if s, err := r.ex.Start(t.Context(), api("scan", "q:0", "alice", payload{})); err != nil || !s.Joined {
				t.Errorf("alice joining her queued job = (%+v, %v), want joined", s, err)
			}
			if _, err := r.ex.Start(t.Context(), api("scan", "q:5", "bob", payload{})); err != nil {
				t.Errorf("bob's 1st waiting job = %v, want nil", err)
			}
			close(gate)
		})
	})

	t.Run("a scheduled job counts against the total only", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds})
			gate := hold(t, r, "alice")
			for i := range 16 {
				if _, err := r.ex.Start(t.Context(), timer("scan", fmt.Sprintf("q:%d", i), payload{})); err != nil {
					t.Fatalf("scheduled job %d = %v, want room for 16", i+1, err)
				}
			}
			if _, err := r.ex.Start(t.Context(), timer("scan", "q:16", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("a 17th scheduled job = %v, want ErrQueueFull", err)
			}
			close(gate)
		})
	})

	t.Run("custom bounds", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(t, scheduler.Config[payload]{Kinds: kinds, MaxQueued: 2, MaxQueuedPerOwner: 1})
			gate := hold(t, r, "alice")
			r.start(t, api("scan", "q:0", "alice", payload{}))
			if _, err := r.ex.Start(t.Context(), api("scan", "q:1", "alice", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("alice's 2nd waiting job under MaxQueuedPerOwner 1 = %v, want ErrQueueFull", err)
			}
			r.start(t, api("scan", "q:2", "bob", payload{}))
			if _, err := r.ex.Start(t.Context(), api("scan", "q:3", "carol", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("a 3rd waiting job under MaxQueued 2 = %v, want ErrQueueFull", err)
			}
			close(gate)
		})
	})

	for _, tc := range []struct {
		name  string
		owner string
		fill  func(i int) (scope, owner string)
		held  int
	}{
		{"concurrent starts for the last place", "o50", func(i int) (string, string) { return fmt.Sprintf("q:%d", i), fmt.Sprintf("o%d", i) }, 15},
		{"concurrent starts for an owner's last place", "alice", func(i int) (string, string) { return fmt.Sprintf("q:%d", i), "alice" }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, scheduler.Config[payload]{Kinds: kinds})
				gate := hold(t, r, "running")
				for i := range tc.held {
					scope, owner := tc.fill(i)
					r.start(t, api("scan", scope, owner, payload{}))
				}
				before := len(r.store.All())
				var wg sync.WaitGroup
				errs := make([]error, 2)
				for i := range errs {
					wg.Go(func() {
						_, errs[i] = r.ex.Start(t.Context(), api("scan", fmt.Sprintf("c:%d", i), tc.owner, payload{}))
					})
				}
				wg.Wait()
				accepted, refused := 0, 0
				for _, err := range errs {
					switch {
					case err == nil:
						accepted++
					case errors.Is(err, scheduler.ErrQueueFull):
						refused++
					default:
						t.Errorf("Start = %v", err)
					}
				}
				if accepted != 1 || refused != 1 {
					t.Errorf("%d accepted and %d refused, want one each", accepted, refused)
				}
				states := map[string]int{}
				for _, rec := range r.store.All()[before:] {
					states[rec.State]++
				}
				if states[scheduler.StateQueued] != 1 || states[scheduler.StateEnded] != 1 || len(r.store.All())-before != 2 {
					t.Errorf("new records by state %v, want one queued and one ended queue_full", states)
				}
				close(gate)
			})
		})
	}
}

func TestNewExecutorRefuses(t *testing.T) {
	noop := func(context.Context, *scheduler.Run[payload]) scheduler.End {
		return scheduler.End{Result: scheduler.ResultOK}
	}
	base := func() scheduler.Config[payload] {
		return scheduler.Config[payload]{
			Kinds:   []scheduler.KindSpec[payload]{{Kind: "scan", Slot: "heavy", Kept: time.Hour, Run: noop}},
			Records: memrecords.New(),
		}
	}
	phases := func(n int) []scheduler.Phase {
		out := make([]scheduler.Phase, n)
		for i := range out {
			out[i] = scheduler.Phase(fmt.Sprintf("p%d", i))
		}
		return out
	}
	const day = 24 * time.Hour
	items := func(p payload) int { return p.N }

	for _, tc := range []struct {
		edit func(*scheduler.Config[payload])
		name string
		ok   bool
	}{
		{name: "the defaults", ok: true, edit: func(*scheduler.Config[payload]) {}},
		{name: "a kind keeping no record with no store", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = 0; c.Records = nil }},
		{name: "kept 15 minutes", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = 15 * time.Minute }},
		{name: "kept 400 days", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = 400 * day }},
		{name: "32 phases", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Phases = phases(32) }},
		{name: "a batch of 1", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].MaxItems, c.Kinds[0].Items = 1, items }},
		{name: "a batch of 500", ok: true, edit: func(c *scheduler.Config[payload]) { c.Kinds[0].MaxItems, c.Kinds[0].Items = 500, items }},
		{name: "queue bounds at their top", ok: true, edit: func(c *scheduler.Config[payload]) { c.MaxQueued, c.MaxQueuedPerOwner = 256, 256 }},
		{name: "queue bounds at their bottom", ok: true, edit: func(c *scheduler.Config[payload]) { c.MaxQueued, c.MaxQueuedPerOwner = 1, 1 }},

		{name: "a duplicate kind", edit: func(c *scheduler.Config[payload]) { c.Kinds = append(c.Kinds, c.Kinds[0]) }},
		{name: "an empty kind", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kind = "" }},
		{name: "an uppercase kind", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kind = "Scan" }},
		{name: "a hyphenated kind", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kind = "full-check" }},
		{name: "a phase outside code grammar", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Phases = []scheduler.Phase{"Fetched"} }},
		{name: "33 phases", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Phases = phases(33) }},
		{name: "kept under 15 minutes", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = 15*time.Minute - time.Second }},
		{name: "kept over 400 days", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = 400*day + time.Second }},
		{name: "a negative kept", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Kept = -time.Hour }},
		{name: "a batch of 501", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].MaxItems, c.Kinds[0].Items = 501, items }},
		{name: "a negative batch", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].MaxItems, c.Kinds[0].Items = -1, items }},
		{name: "a batch with no item count", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].MaxItems = 3 }},
		{name: "an item count on a kind that is not a batch", edit: func(c *scheduler.Config[payload]) { c.Kinds[0].Items = items }},
		{name: "33 phases on a second kind", edit: func(c *scheduler.Config[payload]) {
			c.Kinds = append(c.Kinds, scheduler.KindSpec[payload]{Kind: "grab", Kept: time.Hour, Phases: phases(33), Run: noop})
		}},
		{name: "max queued over 256", edit: func(c *scheduler.Config[payload]) { c.MaxQueued = 257 }},
		{name: "a negative max queued", edit: func(c *scheduler.Config[payload]) { c.MaxQueued = -1 }},
		{name: "per owner above max queued", edit: func(c *scheduler.Config[payload]) { c.MaxQueued, c.MaxQueuedPerOwner = 8, 9 }},
		{name: "per owner above the default max queued", edit: func(c *scheduler.Config[payload]) { c.MaxQueuedPerOwner = 17 }},
		{name: "a negative per owner", edit: func(c *scheduler.Config[payload]) { c.MaxQueuedPerOwner = -1 }},
		{name: "no store for a kind that keeps records", edit: func(c *scheduler.Config[payload]) { c.Records = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.edit(&cfg)
			ex, err := scheduler.NewExecutor(cfg)
			switch {
			case tc.ok && (err != nil || ex == nil):
				t.Errorf("NewExecutor = (executor %t, %v), want an executor", ex != nil, err)
			case !tc.ok && (err == nil || ex != nil):
				t.Errorf("NewExecutor = (executor %t, %v), want no executor and an error", ex != nil, err)
			}
		})
	}
}

func TestScheduleRefuses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sweep := spec("sweep", "")
		sweep.Kept = 0
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("scan", "heavy"), sweep}})
		every := scheduler.Scheduled(time.Hour)
		long := "s" + strings.Repeat("a", 127)

		if err := r.ex.Schedule("nope", "", every, time.Time{}); !errors.Is(err, scheduler.ErrUnknownKind) {
			t.Errorf("Schedule(an unknown kind) = %v, want ErrUnknownKind", err)
		}
		for _, scope := range []string{"Source:A", "source a", long + "a"} {
			if err := r.ex.Schedule("scan", scope, every, time.Time{}); !errors.Is(err, scheduler.ErrScope) {
				t.Errorf("Schedule(scope %q) = %v, want ErrScope", scope, err)
			}
		}
		if err := r.ex.Schedule("scan", long, every, time.Time{}); err != nil {
			t.Errorf("Schedule(a 128-byte scope) = %v, want nil", err)
		}
		if err := r.ex.Schedule("scan", "", every, time.Time{}); err != nil {
			t.Errorf("Schedule(scope \"\") = %v, want nil", err)
		}
		for i := range 31 {
			if err := r.ex.Schedule("scan", fmt.Sprintf("source:%d", i), every, time.Time{}); err != nil {
				t.Fatalf("Schedule(scan scope %d) = %v", i, err)
			}
		}
		for i := range 31 {
			if err := r.ex.Schedule("sweep", fmt.Sprintf("store:%d", i), every, time.Time{}); err != nil {
				t.Fatalf("Schedule(sweep scope %d) = %v", i, err)
			}
		}
		// 64 scheduled across both kinds; the refusals above counted nothing.
		if err := r.ex.Schedule("sweep", "store:31", every, time.Time{}); !errors.Is(err, scheduler.ErrTooManyScopes) {
			t.Errorf("Schedule(a 65th scope) = %v, want ErrTooManyScopes", err)
		}
		if err := r.ex.Schedule("scan", "source:0", scheduler.Scheduled(2*time.Hour).Retry(time.Minute), time.Time{}); err != nil {
			t.Errorf("re-scheduling a scope at the bound = %v, want nil", err)
		}
		r.ex.Unschedule("scan", "source:99")
		if err := r.ex.Schedule("sweep", "store:31", every, time.Time{}); !errors.Is(err, scheduler.ErrTooManyScopes) {
			t.Errorf("Schedule after unscheduling a scope never scheduled = %v, want ErrTooManyScopes", err)
		}
		if err := r.ex.Schedule("scan", "source:0", scheduler.TriggerOnly(), time.Time{}); err != nil {
			t.Errorf("Schedule(TriggerOnly) = %v, want nil", err)
		}
		if err := r.ex.Schedule("sweep", "store:31", every, time.Time{}); err != nil {
			t.Errorf("Schedule after TriggerOnly freed a scope = %v, want nil", err)
		}
		if err := r.ex.Schedule("sweep", "store:32", every, time.Time{}); !errors.Is(err, scheduler.ErrTooManyScopes) {
			t.Errorf("Schedule(a 65th scope) = %v, want ErrTooManyScopes", err)
		}
		r.ex.Unschedule("sweep", "store:31")
		if err := r.ex.Schedule("sweep", "store:32", every, time.Time{}); err != nil {
			t.Errorf("Schedule after Unschedule freed a scope = %v, want nil", err)
		}
	})
}

func TestStartAfterShutdownWritesNoRecord(t *testing.T) {
	t.Run("a timer start waiting on an admission when Run's context ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newGatedStore()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, done := (&rig{store: s.Store, log: &runLog{}}).run(t, ctx, scheduler.Config[payload]{
				Kinds:   []scheduler.KindSpec[payload]{spec("scan", "heavy")},
				Records: s,
			})
			synctest.Wait()
			r.schedule(t, "scan", "source:a", scheduler.Scheduled(time.Hour))
			g := s.gate(memrecords.OpInsert)
			type answer struct {
				s   scheduler.Started
				err error
			}
			admitted := make(chan answer, 1)
			go func() {
				st, err := r.ex.Start(t.Context(), api("scan", "source:a", "alice", payload{}))
				admitted <- answer{st, err}
			}()
			<-g.entered
			_, hi := jitterBand(time.Hour)
			time.Sleep(hi + time.Minute) // the timer's start waits on the held write
			synctest.Wait()
			cancel()
			synctest.Wait()
			close(g.release)
			<-done
			a := <-admitted
			if a.err != nil {
				t.Fatalf("the API start whose write was in flight = %v, want nil", a.err)
			}
			synctest.Wait()
			r.wantEnd(t, a.s.Job, scheduler.ResultInterrupted)
			if w := r.wakes("scan", "source:a"); len(w) != 0 {
				t.Errorf("the timer's start refused at shutdown wrote %d records, the first ended %+v, want none", len(w), w[0].End)
			}
		})
	})

	t.Run("a start after Run returned", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r, stop := (&rig{store: memrecords.New(), log: &runLog{}}).openUntil(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("check", "")}})
			stop()
			if _, err := r.ex.Start(t.Context(), api("check", "", "alice", payload{})); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("Start after Run returned = %v, want ErrQueueFull", err)
			}
			synctest.Wait()
			if all := r.store.All(); len(all) != 0 {
				t.Errorf("Start after Run returned wrote %d records, the first ended %+v, want none", len(all), all[0].End)
			}
			if len(r.log.ids()) != 0 {
				t.Errorf("jobs run after Run returned = %v, want none", r.log.ids())
			}
		})
	})
}
