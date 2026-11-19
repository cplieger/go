package scheduler_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// The proposer's payload N.
const (
	proposeTwice   = 2 // Propose a second value after the first
	proposeLate    = 1 // wait on the Gate before proposing
	proposeNothing = 3 // wait on the Gate and end without proposing
)

// proposals keeps what each job's Propose calls answered.
type proposals struct {
	ids    map[scheduler.ID]scheduler.ProposalID
	errs   map[scheduler.ID][]error
	mu     sync.Mutex
	runLog runLog
}

func (p *proposals) id(j scheduler.ID) scheduler.ProposalID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ids[j]
}

func (p *proposals) errsOf(j scheduler.ID) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errs[j]
}

// ranFor reports whether a job of owner ran.
func (p *proposals) ranFor(owner string) bool {
	p.runLog.mu.Lock()
	defer p.runLog.mu.Unlock()
	return slices.ContainsFunc(p.runLog.jobs, func(j scheduler.Job[payload]) bool { return j.Owner == owner })
}

func (p *proposals) propose(r *scheduler.Run[payload], v any) {
	id, err := r.Propose(v)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.ids[r.Job().ID]; !ok {
		p.ids[r.Job().ID] = id
	}
	p.errs[r.Job().ID] = append(p.errs[r.Job().ID], err)
}

// spec is a proposing kind in slot: its Run proposes "fit <scope>" and then
// waits on its Gate, or as its payload's N says.
func (p *proposals) spec(slot string) scheduler.KindSpec[payload] {
	if p.ids == nil {
		p.ids, p.errs = map[scheduler.ID]scheduler.ProposalID{}, map[scheduler.ID][]error{}
	}
	k := spec("align", slot)
	k.Proposes = true
	k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
		j := r.Job()
		p.runLog.add(j)
		wait := func() {
			if j.Payload.Gate != nil {
				select {
				case <-j.Payload.Gate:
				case <-ctx.Done():
				}
			}
		}
		switch j.Payload.N {
		case proposeLate:
			wait()
			p.propose(r, "fit "+j.Scope)
		case proposeNothing:
			wait()
		case proposeTwice:
			p.propose(r, "fit "+j.Scope)
			p.propose(r, "second")
			wait()
		default:
			p.propose(r, "fit "+j.Scope)
			wait()
		}
		return scheduler.End{Result: scheduler.ResultOK}
	}
	return k
}

func TestProposalsExpireAndBound(t *testing.T) {
	t.Run("held until 15 minutes after its job ends, for its owner only", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			gate := make(chan struct{})
			s := r.start(t, api("align", "file:1/expect:7", "alice", payload{Gate: gate}))
			synctest.Wait()
			id := p.id(s.Job)
			if errs := p.errsOf(s.Job); len(errs) != 1 || errs[0] != nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(string(id)) {
				t.Fatalf("Propose = (%q, %v), want a 32-digit lowercase hex id", id, errs)
			}
			if got := r.rec(t, s.Job).Proposal; got != id {
				t.Errorf("the record's Proposal = %q, want %q", got, id)
			}
			wantHeld := func(when, owner string, id scheduler.ProposalID, held bool) {
				t.Helper()
				v, ok := r.ex.Proposal(owner, id)
				switch {
				case held && (!ok || v != "fit file:1/expect:7"):
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
			r.wantEnd(t, s.Job, scheduler.ResultOK)
			time.Sleep(15*time.Minute - time.Nanosecond)
			wantHeld("just under 15 minutes after the end", "alice", id, true)
			time.Sleep(time.Nanosecond)
			wantHeld("15 minutes after the end", "alice", id, false)
		})
	})

	t.Run("a start for a held proposal joins its job and runs nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			first := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			r.wantEnd(t, first.Job, scheduler.ResultOK)
			time.Sleep(10 * time.Minute)

			again := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			if !again.Joined || again.Job != first.Job {
				t.Errorf("Start of a held proposal's owner, kind and scope = %+v, want joined to %s", again, first.Job)
			}
			if n := len(p.runLog.ids()); n != 1 {
				t.Errorf("%d runs after a start for a held proposal, want 1", n)
			}

			bob := r.start(t, api("align", "file:1/expect:7", "bob", payload{}))
			other := r.start(t, api("align", "file:1/expect:8", "alice", payload{}))
			synctest.Wait()
			for name, s := range map[string]scheduler.Started{"another owner": bob, "another scope": other} {
				if s.Joined || s.Job == first.Job || !p.runLog.ran(s.Job) {
					t.Errorf("Start for %s = %+v, want a job of its own that ran", name, s)
				}
			}
			if v, ok := r.ex.Proposal("bob", p.id(bob.Job)); !ok || v != "fit file:1/expect:7" {
				t.Errorf("Proposal(bob, his own) = (%v, %t), want held", v, ok)
			}
			if _, ok := r.ex.Proposal("alice", p.id(bob.Job)); ok {
				t.Error("Proposal(alice, bob's) = held, want false")
			}

			time.Sleep(5 * time.Minute)
			expired := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			if expired.Joined || expired.Job == first.Job || !p.runLog.ran(expired.Job) {
				t.Errorf("Start once the proposal expired = %+v, want a new job that ran", expired)
			}
		})
	})

	t.Run("a second Propose in one job answers ErrProposed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			s := r.start(t, api("align", "file:1/expect:7", "alice", payload{N: proposeTwice}))
			synctest.Wait()
			errs := p.errsOf(s.Job)
			if len(errs) != 2 || errs[0] != nil || !errors.Is(errs[1], scheduler.ErrProposed) {
				t.Fatalf("Propose twice = %v, want nil then ErrProposed", errs)
			}
			if v, ok := r.ex.Proposal("alice", p.id(s.Job)); !ok || v != "fit file:1/expect:7" {
				t.Errorf("Proposal after a second Propose = (%v, %t), want the first value", v, ok)
			}
		})
	})

	t.Run("Propose in a kind that does not propose ends the job failed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			k := spec("scan", "")
			k.Run = func(_ context.Context, r *scheduler.Run[payload]) scheduler.End {
				_, _ = r.Propose("fit")
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			s := r.start(t, api("scan", "", "alice", payload{}))
			synctest.Wait()
			r.wantEnd(t, s.Job, scheduler.ResultFailed, scheduler.Reason{Code: scheduler.ReasonRunPanicked})
		})
	})

	t.Run("requests of one owner coalesce and another owner's do not", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("heavy"), spec("scan", "heavy")}})
			scanGate, gate := make(chan struct{}), make(chan struct{})
			r.start(t, api("scan", "", "carol", payload{Gate: scanGate}))
			synctest.Wait()
			alice := r.start(t, api("align", "file:1/expect:7", "alice", payload{Gate: gate}))
			bob := r.start(t, api("align", "file:1/expect:7", "bob", payload{Gate: gate}))
			if bob.Joined || bob.Job == alice.Job {
				t.Errorf("bob's Start while alice's job of the scope is queued = %+v, want a job of his own", bob)
			}
			if s := r.start(t, api("align", "file:1/expect:7", "alice", payload{})); !s.Joined || s.Job != alice.Job {
				t.Errorf("alice's second Start while hers is queued = %+v, want joined to %s", s, alice.Job)
			}
			close(scanGate)
			synctest.Wait()
			if s := r.start(t, api("align", "file:1/expect:7", "alice", payload{})); !s.Joined || s.Job != alice.Job {
				t.Errorf("alice's Start while hers runs = %+v, want joined to %s with no follow-up", s, alice.Job)
			}
			close(gate)
			synctest.Wait()
			if got := p.runLog.ids(); len(got) != 2 || got[0] != alice.Job || got[1] != bob.Job {
				t.Errorf("proposing runs = %v, want alice's %s then bob's %s", got, alice.Job, bob.Job)
			}
			if _, ok := r.ex.Proposal("bob", p.id(bob.Job)); !ok {
				t.Error("bob's own proposal is not held, want held")
			}
		})
	})

	t.Run("a request while its owner's job yields joins it and runs nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			k := p.spec("heavy")
			k.Run = func(ctx context.Context, r *scheduler.Run[payload]) scheduler.End {
				p.runLog.add(r.Job())
				if err := r.Yield(ctx, time.Now().Add(time.Minute)); err != nil {
					return scheduler.End{Result: scheduler.ResultFailed}
				}
				p.propose(r, "fit "+r.Job().Scope)
				return scheduler.End{Result: scheduler.ResultOK}
			}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
			alice := r.start(t, api("align", "file:1/expect:7", "alice", payload{}))
			synctest.Wait()
			if s := r.start(t, api("align", "file:1/expect:7", "alice", payload{})); !s.Joined || s.Job != alice.Job {
				t.Errorf("alice's Start while hers yields = %+v, want joined to %s", s, alice.Job)
			}
			time.Sleep(time.Minute)
			synctest.Wait()
			if got := p.runLog.ids(); len(got) != 1 || got[0] != alice.Job {
				t.Errorf("proposing runs = %v, want alice's %s only", got, alice.Job)
			}
			r.wantEnd(t, alice.Job, scheduler.ResultOK)
			if _, ok := r.ex.Proposal("alice", p.id(alice.Job)); !ok {
				t.Error("alice's proposal is not held, want held")
			}
		})
	})

	t.Run("16 held or pending per owner and 256 in all", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &proposals{}
			r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{p.spec("")}})
			start := func(owner string, i int, pl payload) (scheduler.Started, error) {
				t.Helper()
				s, err := r.ex.Start(t.Context(), api("align", fmt.Sprintf("file:%d", i), owner, pl))
				synctest.Wait()
				return s, err
			}
			mustStart := func(owner string, i int, pl payload) scheduler.Started {
				t.Helper()
				s, err := start(owner, i, pl)
				if err != nil {
					t.Fatalf("Start(%s file:%d) = %v, want nil", owner, i, err)
				}
				return s
			}
			for i := range 15 {
				mustStart("owner:0", i, payload{})
			}
			gate := make(chan struct{})
			pending := mustStart("owner:0", 15, payload{N: proposeNothing, Gate: gate})
			if _, err := start("owner:0", 16, payload{}); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("owner:0's 17th with 15 held and 1 pending = %v, want ErrQueueFull", err)
			}
			refused := 0
			for _, rec := range r.store.All() {
				if rec.Scope == "file:16" {
					refused++
					if rec.End == nil || rec.End.Result != scheduler.ResultSkipped ||
						len(rec.End.Reasons) != 1 || rec.End.Reasons[0].Code != scheduler.ReasonQueueFull {
						t.Errorf("the refused start's record ended %+v, want skipped, queue_full", rec.End)
					}
				}
			}
			if refused != 1 {
				t.Errorf("%d records of the refused start, want 1", refused)
			}
			if s, err := start("owner:0", 0, payload{}); err != nil || !s.Joined {
				t.Errorf("owner:0's held scope at the bound = (%+v, %v), want joined to its held proposal", s, err)
			}

			close(gate)
			synctest.Wait()
			r.wantEnd(t, pending.Job, scheduler.ResultOK)
			mustStart("owner:0", 16, payload{})
			if _, err := start("owner:0", 17, payload{}); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("owner:0's 17th with 16 held = %v, want ErrQueueFull", err)
			}

			for o := 1; o < 16; o++ {
				for i := range 16 {
					mustStart(fmt.Sprintf("owner:%d", o), i, payload{})
				}
			}
			if _, err := start("owner:16", 0, payload{}); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("the 257th proposal = %v, want ErrQueueFull", err)
			}
			if p.ranFor("owner:16") {
				t.Error("a start refused at the bound ran, want none")
			}

			time.Sleep(15*time.Minute - time.Nanosecond)
			if _, err := start("owner:16", 1, payload{}); !errors.Is(err, scheduler.ErrQueueFull) {
				t.Errorf("the 257th just before the others expire = %v, want ErrQueueFull", err)
			}
			time.Sleep(time.Nanosecond)
			if _, err := start("owner:16", 2, payload{}); err != nil {
				t.Errorf("a proposal once the others expired = %v, want admitted", err)
			}
		})
	})
}

func TestDigestScope(t *testing.T) {
	t.Parallel()
	scope := regexp.MustCompile(`^[a-z0-9_:/]*$`)
	digest := func(b string) string {
		sum := sha256.Sum256([]byte(b))
		return hex.EncodeToString(sum[:])
	}
	t.Run("length-prefixed parts", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			name, prefix, encoded string
			parts                 []string
		}{
			{"two parts", "align:x", "\x02ab\x01c", []string{"ab", "c"}},
			{"no parts", "align:x", "", nil},
			{"one empty part", "p", "\x00", []string{""}},
			{"a part over 127 bytes", "p", "\xc8\x01" + strings.Repeat("a", 200), []string{strings.Repeat("a", 200)}},
			{"an empty prefix", "", "\x01a", []string{"a"}},
		} {
			parts := make([][]byte, 0, len(c.parts))
			for _, s := range c.parts {
				parts = append(parts, []byte(s))
			}
			if got, want := scheduler.DigestScope(c.prefix, parts...), c.prefix+"/"+digest(c.encoded); got != want {
				t.Errorf("%s: DigestScope(%q, %q) = %q, want %q", c.name, c.prefix, c.parts, got, want)
			}
		}
	})
	t.Run("no two part lists collide", func(t *testing.T) {
		t.Parallel()
		lists := [][]string{{"ab", "c"}, {"a", "bc"}, {"abc"}, {}, {""}, {"", ""}, {"a", ""}, {"", "a"}}
		seen := map[string][]string{}
		for _, l := range lists {
			parts := make([][]byte, 0, len(l))
			for _, s := range l {
				parts = append(parts, []byte(s))
			}
			got := scheduler.DigestScope("p", parts...)
			if prev, dup := seen[got]; dup {
				t.Errorf("DigestScope of %q and of %q = %q, want them apart", prev, l, got)
			}
			seen[got] = l
		}
	})
	t.Run("a 63-byte prefix stays within the scope grammar and 128 bytes", func(t *testing.T) {
		t.Parallel()
		got := scheduler.DigestScope(strings.Repeat("a", 62)+":", []byte("Name With Spaces.mkv"))
		if len(got) != 128 || !scope.MatchString(got) {
			t.Errorf("DigestScope with a 63-byte prefix = %q (%d bytes), want 128 bytes in the scope grammar", got, len(got))
		}
	})
	t.Run("a bad prefix panics", func(t *testing.T) {
		t.Parallel()
		for _, prefix := range []string{strings.Repeat("a", 64), "Align", "a b", "é"} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("DigestScope(%q) did not panic, want a panic", prefix)
					}
				}()
				_ = scheduler.DigestScope(prefix, []byte("x"))
			}()
		}
	})
}
