package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// proposer is a kind whose Run calls each, then waits on its Gate.
func proposer(each func(*scheduler.Run[payload])) scheduler.KindSpec[payload] {
	k := spec("propose", "")
	k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		each(r)
		if g := r.Job().Payload.Gate; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
			}
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

func TestProposalsExpireAndBound(t *testing.T) {
	t.Run("held until 15 minutes after its job ends, for its owner only", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				id  scheduler.ProposalID
				err error
			)
			k := proposer(func(r *scheduler.Run[payload]) { id, err = r.Propose("alice", "file:1/expect:7", "fit +120ms") })
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			gate := make(chan struct{})
			r.start(t, api("propose", "", "alice", payload{Gate: gate}))
			synctest.Wait()
			if err != nil || len(id) != 32 {
				t.Fatalf("Propose = (%q, %v), want a 32-digit id", id, err)
			}
			wantHeld := func(when, owner string, id scheduler.ProposalID, held bool) {
				t.Helper()
				v, ok := r.ex.Proposal(owner, id)
				switch {
				case held && (!ok || v != "fit +120ms"):
					t.Errorf("%s: Proposal(%s, %s) = (%v, %t), want the held value", when, owner, id, v, ok)
				case !held && (ok || v != nil):
					t.Errorf("%s: Proposal(%s, %s) = (%v, %t), want (nil, false)", when, owner, id, v, ok)
				}
			}
			wantHeld("while the job runs", "alice", id, true)
			wantHeld("another owner", "bob", id, false)
			wantHeld("an id never held", "alice", "never", false)
			time.Sleep(time.Hour)
			wantHeld("an hour into the job", "alice", id, true)
			close(gate)
			synctest.Wait()
			time.Sleep(15*time.Minute - time.Nanosecond)
			wantHeld("just under 15 minutes after the end", "alice", id, true)
			time.Sleep(time.Nanosecond)
			wantHeld("15 minutes after the end", "alice", id, false)
		})
	})

	t.Run("a held key answers its proposal and counts nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			type answer struct {
				id  scheduler.ProposalID
				err error
			}
			var first, again, other, bob answer
			k := proposer(func(r *scheduler.Run[payload]) {
				first.id, first.err = r.Propose("alice", "file:1/expect:7", "first")
				again.id, again.err = r.Propose("alice", "file:1/expect:7", "second")
				other.id, other.err = r.Propose("alice", "file:1/expect:8", "other")
				bob.id, bob.err = r.Propose("bob", "file:1/expect:7", "bob's")
			})
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			r.start(t, api("propose", "", "alice", payload{}))
			synctest.Wait()
			for name, a := range map[string]answer{"first": first, "again": again, "other": other, "bob": bob} {
				if a.err != nil || a.id == "" {
					t.Errorf("Propose %s = (%q, %v), want an id", name, a.id, a.err)
				}
			}
			if again.id != first.id {
				t.Errorf("a second Propose of a held key = %s, want the held %s", again.id, first.id)
			}
			if other.id == first.id || bob.id == first.id {
				t.Errorf("another key %s and another owner %s, want ids apart from %s", other.id, bob.id, first.id)
			}
			if v, _ := r.ex.Proposal("alice", first.id); v != "first" {
				t.Errorf("Proposal(alice, held) = %v, want the first value kept", v)
			}
			if v, _ := r.ex.Proposal("bob", bob.id); v != "bob's" {
				t.Errorf("Proposal(bob) = %v, want bob's own", v)
			}
		})
	})

	t.Run("16 per owner and 256 in all", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				errs     []error
				full     [3]error
				repeat   error
				repeatID scheduler.ProposalID
				firstID  scheduler.ProposalID
				later    error
			)
			k := proposer(func(r *scheduler.Run[payload]) {
				if r.Job().Payload.N > 0 {
					_, later = r.Propose("owner:16", "file:0", 0)
					return
				}
				for o := range 16 {
					for i := range 16 {
						id, err := r.Propose(fmt.Sprintf("owner:%d", o), fmt.Sprintf("file:%d", i), i)
						if o == 0 && i == 0 {
							firstID = id
						}
						errs = append(errs, err)
					}
				}
				_, full[0] = r.Propose("owner:0", "file:16", 16)
				_, full[1] = r.Propose("owner:16", "file:0", 0)
				repeatID, repeat = r.Propose("owner:0", "file:0", "again")
			})
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			r.start(t, api("propose", "", "alice", payload{}))
			synctest.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("proposal %d of 256 = %v, want held", i, err)
				}
			}
			if !errors.Is(full[0], scheduler.ErrQueueFull) {
				t.Errorf("an owner's 17th proposal = %v, want ErrQueueFull", full[0])
			}
			if !errors.Is(full[1], scheduler.ErrQueueFull) {
				t.Errorf("the 257th proposal = %v, want ErrQueueFull", full[1])
			}
			if repeat != nil || repeatID != firstID {
				t.Errorf("a held key at the bound = (%s, %v), want the held %s", repeatID, repeat, firstID)
			}

			time.Sleep(15*time.Minute - time.Nanosecond)
			r.start(t, api("propose", "before", "alice", payload{N: 1}))
			synctest.Wait()
			if !errors.Is(later, scheduler.ErrQueueFull) {
				t.Errorf("the 257th proposal just before the others expire = %v, want ErrQueueFull", later)
			}
			time.Sleep(time.Nanosecond)
			r.start(t, api("propose", "after", "alice", payload{N: 1}))
			synctest.Wait()
			if later != nil {
				t.Errorf("a proposal once the others expired = %v, want held", later)
			}
		})
	})
}
