package httpwire

import (
	"context"
	"sync"
	"time"
)

// memStore is the default IdempotencyStore. It evicts only an expired
// answer and an answer past its row's MaxKept, so a retry inside the
// published policy is never run twice because its answer was dropped.
type memStore struct {
	entries   map[IdempotencyKey]*memEntry
	creds     map[string]*credKept
	maxKept   map[string]int
	nextSweep time.Time
	bytes     int
	maxBytes  int
	seq       uint64
	mu        sync.Mutex
}

type memEntry struct {
	a   KeptAnswer
	seq uint64
}

// credKept is one credential's kept answers, and per MaxKept row their
// order of saving.
type credKept struct {
	keys  map[IdempotencyKey]struct{}
	order map[string][]keptRef
	bytes int
}

type keptRef struct {
	k   IdempotencyKey
	seq uint64
}

const sweepEvery = time.Minute

var _ IdempotencyStore = (*memStore)(nil)

func newMemStore(maxBytes int, maxKept map[string]int) *memStore {
	return &memStore{entries: map[IdempotencyKey]*memEntry{}, creds: map[string]*credKept{}, maxKept: maxKept, maxBytes: maxBytes}
}

func (s *memStore) Load(_ context.Context, k IdempotencyKey) (KeptAnswer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[k]
	if e == nil || !time.Now().Before(e.a.Expires) {
		return KeptAnswer{}, false, nil
	}
	return e.a, true, nil
}

// Save keeps a, as Oversize when its body would take the kept bodies past
// maxBytes.
//
//nolint:gocritic // hugeParam: IdempotencyStore takes the answer by value, so no store can change the binder's copy.
func (s *memStore) Save(_ context.Context, k IdempotencyKey, a KeptAnswer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepDue(time.Now())
	if e := s.entries[k]; e != nil {
		s.remove(k, e)
	}
	if s.bytes+len(a.Body) > s.maxBytes {
		a.Body, a.Oversize = nil, true
	}
	s.seq++
	s.entries[k] = &memEntry{a: a, seq: s.seq}
	c := s.creds[k.Credential]
	if c == nil {
		c = &credKept{keys: map[IdempotencyKey]struct{}{}, order: map[string][]keptRef{}}
		s.creds[k.Credential] = c
	}
	c.keys[k] = struct{}{}
	c.bytes += len(a.Body)
	s.bytes += len(a.Body)
	if n := s.maxKept[k.Row]; n > 0 {
		s.keep(c, k, n)
	}
	return nil
}

// keep records k in its row's order and evicts the credential's oldest
// answers of the row past n.
func (s *memStore) keep(c *credKept, k IdempotencyKey, n int) {
	live := c.order[k.Row][:0]
	for _, ref := range c.order[k.Row] {
		if e := s.entries[ref.k]; e != nil && e.seq == ref.seq {
			live = append(live, ref)
		}
	}
	live = append(live, keptRef{k: k, seq: s.seq})
	for len(live) > n {
		s.remove(live[0].k, s.entries[live[0].k])
		live = live[1:]
	}
	c.order[k.Row] = live
}

func (s *memStore) remove(k IdempotencyKey, e *memEntry) {
	delete(s.entries, k)
	s.bytes -= len(e.a.Body)
	c := s.creds[k.Credential]
	delete(c.keys, k)
	c.bytes -= len(e.a.Body)
	if len(c.keys) == 0 {
		delete(s.creds, k.Credential)
	}
}

func (s *memStore) sweepDue(now time.Time) {
	if now.Before(s.nextSweep) {
		return
	}
	s.sweep(now)
}

func (s *memStore) sweep(now time.Time) {
	for k, e := range s.entries {
		if !now.Before(e.a.Expires) {
			s.remove(k, e)
		}
	}
	s.nextSweep = now.Add(sweepEvery)
}

func (s *memStore) sweepCred(cred string, now time.Time) {
	if c := s.creds[cred]; c != nil {
		for k := range c.keys {
			if e := s.entries[k]; !now.Before(e.a.Expires) {
				s.remove(k, e)
			}
		}
	}
}

// admit reports whether credential cred, with claims runs of its own in
// flight and all runs in flight in total, may start one more. Its share of
// kept bytes reserves maxAnswer for each run in flight, so a credential
// passes its share by at most one answer. With the store full of entries,
// only the credential holding the most live answers is refused.
func (s *memStore) admit(cred string, claims, all int, l *idemLimits) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.sweepDue(now)
	over := func() bool {
		c := s.creds[cred]
		var n, bytes int
		if c != nil {
			n, bytes = len(c.keys), c.bytes
		}
		return n+claims >= l.perCred || bytes+l.maxAnswer*claims >= l.perCredBytes
	}
	if over() {
		s.sweepCred(cred, now)
		if over() {
			return false
		}
	}
	if len(s.entries)+all < maxEntries {
		return true
	}
	s.sweep(now)
	if len(s.entries)+all < maxEntries {
		return true
	}
	held := 0
	if c := s.creds[cred]; c != nil {
		held = len(c.keys)
	}
	for _, c := range s.creds {
		if len(c.keys) > held {
			return true
		}
	}
	return false
}
