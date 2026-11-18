package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// The bounds on held proposals.
const (
	proposalTTL          = 15 * time.Minute // after the job's end
	maxProposalsPerOwner = 16
	maxProposals         = 256
	// maxDigestPrefix keeps DigestScope's answer, prefix + "/" + 64 hex
	// digits, within maxScope.
	maxDigestPrefix = maxScope - 1 - 2*sha256.Size
)

// proposal is one held proposal. until is zero while its job runs, then its
// end plus proposalTTL.
type proposal struct {
	until time.Time
	v     any
	key   proposalKey
	id    ProposalID
	job   ID
}

// proposalKey is what makes two proposals the same.
type proposalKey struct {
	owner, scope string
	kind         Kind
}

func keyOf[P any](j *Job[P]) proposalKey {
	return proposalKey{owner: j.Owner, kind: j.Kind, scope: j.Scope}
}

// Propose holds v as the job's proposal, keyed by its Owner, Kind and Scope,
// until 15 minutes after the job ends, and names it on the job's record. It
// answers ErrProposed when the job already proposed and context.Canceled,
// holding nothing, once a cancel, a Hold(all) or a shutdown stopped the job.
// A kind without Proposes panics, and the job ends failed with run_panicked.
func (r *Run[P]) Propose(v any) (ProposalID, error) {
	e, en := r.e, r.en
	if !en.spec.Proposes {
		panic(fmt.Sprintf("scheduler: kind %s proposed, and its KindSpec does not set Proposes", en.j.Kind))
	}
	e.mu.Lock()
	switch {
	case en.proposal != nil:
		e.mu.Unlock()
		return "", ErrProposed
	case en.stopped:
		e.mu.Unlock()
		return "", context.Canceled
	}
	p := &proposal{id: ProposalID(newID()), job: en.j.ID, key: keyOf(&en.j), v: v}
	en.proposal = p
	e.proposals[p.id] = p
	e.mu.Unlock()

	en.recMu.Lock()
	en.rec.Proposal = p.id
	e.put(en)
	en.recMu.Unlock()
	return p.id, nil
}

// Proposal answers owner's held proposal id; false for another owner's id, an
// expired one and one never held.
func (e *Executor[P]) Proposal(owner string, id ProposalID) (v any, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked(time.Now())
	p := e.proposals[id]
	if p == nil || p.key.owner != owner {
		return nil, false
	}
	return p.v, true
}

// heldLocked answers j, of a proposing kind, from its owner's held proposal
// of its kind and scope.
func (e *Executor[P]) heldLocked(spec *KindSpec[P], j *Job[P]) (Started, bool) {
	if !spec.Proposes {
		return Started{}, false
	}
	e.expireLocked(time.Now())
	k := keyOf(j)
	for _, p := range e.proposals {
		if p.key == k {
			return Started{Job: p.job, Joined: true}, true
		}
	}
	return Started{}, false
}

// proposalsFullLocked reports whether j, of a proposing kind, is past the
// proposal bounds: its owner's held and pending proposals, or everyone's. A
// pending proposal is a live proposing job that has not proposed yet.
func (e *Executor[P]) proposalsFullLocked(spec *KindSpec[P], j *Job[P]) bool {
	if !spec.Proposes {
		return false
	}
	e.expireLocked(time.Now())
	all, owner := len(e.proposals), 0
	for _, p := range e.proposals {
		if p.key.owner == j.Owner {
			owner++
		}
	}
	for _, q := range e.live {
		if q.spec.Proposes && q.proposal == nil && q.state != stateEnded {
			all++
			if q.j.Owner == j.Owner {
				owner++
			}
		}
	}
	return owner >= maxProposalsPerOwner || all >= maxProposals
}

// keepLocked starts the expiry of en's proposal, if it holds one, from its
// end at at.
func (e *Executor[P]) keepLocked(en *job[P], at time.Time) {
	if en.proposal != nil {
		en.proposal.until = at.Add(proposalTTL)
	}
	e.expireLocked(time.Now())
}

// expireLocked drops the proposals whose time has passed.
func (e *Executor[P]) expireLocked(now time.Time) {
	for id, p := range e.proposals {
		if !p.until.IsZero() && !now.Before(p.until) {
			delete(e.proposals, id)
		}
	}
}

// DigestScope answers a scope for a key too long to spell in one: prefix, a
// slash and the lowercase hex SHA-256 of parts, each part led by its length as
// a uvarint so no two part lists collide. A prefix outside the scope grammar
// or over 63 bytes panics.
func DigestScope(prefix string, parts ...[]byte) string {
	if len(prefix) > maxDigestPrefix || !validScope(prefix) {
		panic(fmt.Sprintf("scheduler: DigestScope prefix %q outside the scope grammar or over %d bytes", prefix, maxDigestPrefix))
	}
	h := sha256.New()
	var n [binary.MaxVarintLen64]byte
	for _, p := range parts {
		h.Write(binary.AppendUvarint(n[:0], uint64(len(p))))
		h.Write(p)
	}
	return prefix + "/" + hex.EncodeToString(h.Sum(nil))
}
