package keylock

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Set holds one lock per key and is safe for concurrent use. Construct it
// with [New]; the zero value is not usable, and a Set must not be copied
// after first use. Lock and LockAll panic, holding nothing, on a key that is
// not equal to itself: a floating-point NaN, or an interface value whose
// dynamic type is not comparable.
type Set[K comparable] struct {
	m   map[K]*entry
	cmp func(a, b K) int
	mu  sync.Mutex
}

// entry is one key's lock: holding it is holding the token in ch.
type entry struct {
	ch chan struct{}
	// refs counts holders and waiters; the entry leaves the map at zero.
	refs int
}

// held is a key LockAll took, kept with its entry so a release never looks
// the key up again.
type held[K comparable] struct {
	e *entry
	k K
}

// New returns an empty Set whose [Set.LockAll] orders keys by cmp. cmp must
// be a strict total order consistent with ==: cmp(a, b) == 0 exactly when
// a == b. New panics on a nil cmp.
func New[K comparable](cmp func(a, b K) int) *Set[K] {
	if cmp == nil {
		panic("keylock: New with a nil cmp")
	}
	return &Set[K]{m: make(map[K]*entry), cmp: cmp}
}

// Lock blocks until k is free or ctx is done. On success the caller holds k
// until it calls unlock, which is safe to call more than once. On failure it
// returns ctx.Err() and holds nothing; a ctx already done never takes k, even
// a free one. It panics, holding nothing, on a k not equal to itself (see
// [Set]).
func (s *Set[K]) Lock(ctx context.Context, k K) (unlock func(), err error) {
	mustReflexive(k)
	e, err := s.acquire(ctx, k)
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() { s.release(k, e) }), nil
}

// LockAll takes every key in keys or none of them. It takes each distinct key
// once, in the Set's order, so callers naming the same keys in any order never
// deadlock, and it leaves keys unmodified. On failure it returns ctx.Err()
// with every key it took released. unlock releases them all and is safe to
// call more than once; with no keys there is nothing to take and LockAll
// succeeds. LockAll panics when the order calls two distinct keys equal,
// because it could not order them the same way for every caller, and on any
// key not equal to itself (see [Set]); it checks every key before it takes
// one, so either panic leaves nothing held.
func (s *Set[K]) LockAll(ctx context.Context, keys []K) (unlock func(), err error) {
	ordered := s.order(keys)
	taken := make([]held[K], 0, len(ordered))
	for _, k := range ordered {
		e, err := s.acquire(ctx, k)
		if err != nil {
			s.releaseAll(taken)
			return nil, err
		}
		taken = append(taken, held[K]{e: e, k: k})
	}
	return sync.OnceFunc(func() { s.releaseAll(taken) }), nil
}

func (s *Set[K]) order(keys []K) []K {
	for _, k := range keys {
		mustReflexive(k)
	}
	ordered := slices.SortedFunc(slices.Values(keys), s.cmp)
	return slices.CompactFunc(ordered, func(a, b K) bool {
		if s.cmp(a, b) != 0 {
			return false
		}
		if a != b {
			panic(fmt.Sprintf("keylock: the Set's order calls distinct keys %v and %v equal", a, b))
		}
		return true
	})
}

// mustReflexive runs before s.mu or any token is taken: s.m[k] on such a key
// either never finds its entry again (NaN) or panics with s.mu held.
func mustReflexive[K comparable](k K) {
	if !same(k, k) {
		panic(fmt.Sprintf("keylock: key %v is not equal to itself", k))
	}
}

func same[K comparable](a, b K) bool { return a == b }

func (s *Set[K]) acquire(ctx context.Context, k K) (*entry, error) {
	s.mu.Lock()
	e := s.m[k]
	if e == nil {
		e = &entry{ch: make(chan struct{}, 1)}
		s.m[k] = e
	}
	e.refs++
	s.mu.Unlock()
	select {
	case e.ch <- struct{}{}:
		// select picks at random among ready cases
		// (https://go.dev/ref/spec#Select_statements), so a ctx that ended
		// before the key came free can still lose to the send.
		if err := ctx.Err(); err != nil {
			s.release(k, e)
			return nil, err
		}
		return e, nil
	case <-ctx.Done():
		s.leave(k, e)
		return nil, ctx.Err()
	}
}

func (s *Set[K]) release(k K, e *entry) {
	<-e.ch
	s.leave(k, e)
}

func (s *Set[K]) releaseAll(taken []held[K]) {
	for _, h := range slices.Backward(taken) {
		s.release(h.k, h.e)
	}
}

func (s *Set[K]) leave(k K, e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(s.m, k)
	}
}
