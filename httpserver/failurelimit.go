package httpserver

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// FailureLimits configures a FailureLimiter.
type FailureLimits struct {
	// Now is the limiter's clock; nil means time.Now. A test passes a fake
	// clock here to step through the windows.
	Now func() time.Time
	// PerKey is how many failures one client key may make per PerKeyWindow
	// before Decide throttles it. Both must be positive.
	PerKey       int
	PerKeyWindow time.Duration
	// Global is how many failures every key together may make per
	// GlobalWindow before Decide throttles keys that are not exempt. Zero
	// turns the global window off, and GlobalWindow must then be zero too.
	Global       int
	GlobalWindow time.Duration
	// MaxKeys is how many keys' failures the limiter holds; charging a new
	// key past it forgets the least recently charged one. It must be
	// positive.
	MaxKeys int
	// TrustPerGroup is how many keys one group trusts at once, and
	// TrustWindow how long a mark lasts. Both must be positive.
	TrustPerGroup int
	TrustWindow   time.Duration
	// IPv6Prefix is how many leading bits of an IPv6 address make its key,
	// from 1 to 128; zero means 64 (RFC 6177's end-site /64).
	IPv6Prefix int
}

// FailureLimiter throttles clients that keep failing to authenticate, by
// client key and across every key, without ever being moved by a request it
// throttles. It is for a handler that must check a credential before it can
// tell the client anything: the handler asks Decide once per request and
// answers a throttled request the same way whatever it presented.
//
// A key is a client address, an IPv4 address whole and an IPv6 address
// masked to FailureLimits.IPv6Prefix bits; an invalid address is one shared
// key. A key the application marks good, under a group it names, is exempt
// from the global window while the mark lasts and the key's own window holds
// no failure, so a spray across many addresses cannot throttle a client that
// has recently succeeded.
//
// All state is in memory and resets at a restart. It logs nothing and
// returns no error after construction, so a group name never leaves it. It
// is safe for concurrent use; every method runs under one lock.
type FailureLimiter struct {
	now      func() time.Time
	keys     map[netip.Prefix]*keyFailures
	groups   map[string][]trustMark
	trusted  map[netip.Prefix]map[string]time.Time
	global   failureLog
	lru      keyList
	limits   FailureLimits
	v6Prefix int
	mu       sync.Mutex
}

// keyFailures is one key's failure log, a node of the LRU.
type keyFailures struct {
	prev *keyFailures
	next *keyFailures
	key  netip.Prefix
	log  failureLog
}

// keyList is the LRU, most recently charged first: a ring through a sentinel
// node, so no end is a special case.
type keyList struct {
	root keyFailures
	n    int
}

func (l *keyList) init() { l.root.prev, l.root.next = &l.root, &l.root }

func (l *keyList) pushFront(k *keyFailures) {
	k.prev, k.next = &l.root, l.root.next
	l.root.next.prev = k
	l.root.next = k
	l.n++
}

func (l *keyList) remove(k *keyFailures) {
	k.prev.next, k.next.prev = k.next, k.prev
	k.prev, k.next = nil, nil
	l.n--
}

func (l *keyList) back() *keyFailures { return l.root.prev }

// trustMark is one key a group trusts, with the time of its latest mark.
type trustMark struct {
	at  time.Time
	key netip.Prefix
}

// NewFailureLimiter checks l and returns a limiter, or an error naming the
// first field out of range.
func NewFailureLimiter(l FailureLimits) (*FailureLimiter, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	bits := l.IPv6Prefix
	if bits == 0 {
		bits = 64
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	f := &FailureLimiter{
		now:      now,
		keys:     make(map[netip.Prefix]*keyFailures),
		groups:   make(map[string][]trustMark),
		trusted:  make(map[netip.Prefix]map[string]time.Time),
		global:   newFailureLog(l.Global),
		limits:   l,
		v6Prefix: bits,
	}
	f.lru.init()
	return f, nil
}

func (l FailureLimits) validate() error {
	switch {
	case l.PerKey <= 0 || l.PerKeyWindow <= 0:
		return errors.New("httpserver: FailureLimits.PerKey and PerKeyWindow must be positive")
	case l.Global < 0 || l.GlobalWindow < 0 || (l.Global == 0) != (l.GlobalWindow == 0):
		return errors.New("httpserver: FailureLimits.Global and GlobalWindow must both be positive, or both zero to turn the global window off")
	case l.MaxKeys <= 0:
		return errors.New("httpserver: FailureLimits.MaxKeys must be positive")
	case l.TrustPerGroup <= 0 || l.TrustWindow <= 0:
		return errors.New("httpserver: FailureLimits.TrustPerGroup and TrustWindow must be positive")
	case l.IPv6Prefix < 0 || l.IPv6Prefix > 128:
		return fmt.Errorf("httpserver: FailureLimits.IPv6Prefix %d is not 0 to 128", l.IPv6Prefix)
	}
	return nil
}

// Decide is the limiter's one question per request. It reports whether
// client is throttled, and for how long, from the state before this request:
// when its key's window is full, or when the global window is full and the
// key is not exempt. wait is the longer of the waits that apply. A throttled
// answer changes nothing, so a throttled request can neither charge a window
// nor move a key in or out of the held set. When the answer is not throttled
// and failure is true, the failure is charged to the key's window and the
// global window. Asking once per request, before anything is written, is
// what keeps concurrent requests from passing a full window together.
func (f *FailureLimiter) Decide(client netip.Addr, failure bool) (throttled bool, wait time.Duration) {
	key := f.keyOf(client)
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	own := f.keys[key]
	ownCount := 0
	if own != nil {
		ownCount = own.log.count(now, f.limits.PerKeyWindow)
	}
	if ownCount >= f.limits.PerKey {
		throttled, wait = true, own.log.wait(now, f.limits.PerKeyWindow)
	}
	exempt := ownCount == 0 && f.trustedNow(key, now)
	if !exempt && f.limits.Global > 0 && f.global.count(now, f.limits.GlobalWindow) >= f.limits.Global {
		throttled, wait = true, max(wait, f.global.wait(now, f.limits.GlobalWindow))
	}
	if throttled || !failure {
		return throttled, wait
	}
	f.charge(key, own, now)
	return false, 0
}

// MarkGood records that client succeeded for group, a name the application
// takes from its configuration, never from the request. A key the group
// already holds has its mark refreshed; a new key joins the group, replacing
// the group's own oldest mark when it already holds TrustPerGroup keys and
// never touching another group's. Call it only after a request the limiter
// did not throttle succeeded.
func (f *FailureLimiter) MarkGood(group string, client netip.Addr) {
	key := f.keyOf(client)
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	marks := f.groups[group]
	for i := range marks {
		if marks[i].key == key {
			marks[i].at = now
			f.trusted[key][group] = now
			return
		}
	}
	if len(marks) >= f.limits.TrustPerGroup {
		oldest := 0
		for i := range marks {
			if marks[i].at.Before(marks[oldest].at) {
				oldest = i
			}
		}
		f.untrust(marks[oldest].key, group)
		marks = append(marks[:oldest], marks[oldest+1:]...)
	}
	f.groups[group] = append(marks, trustMark{key: key, at: now})
	if f.trusted[key] == nil {
		f.trusted[key] = make(map[string]time.Time)
	}
	f.trusted[key][group] = now
}

// keyOf is client's key: an IPv4 address whole, an IPv6 address masked to
// the configured prefix (Prefix drops a zone); any invalid address maps to the
// zero Prefix, one key shared by every such client.
func (f *FailureLimiter) keyOf(client netip.Addr) netip.Prefix {
	client = client.Unmap()
	switch {
	case client.Is4():
		return netip.PrefixFrom(client, 32)
	case client.Is6():
		p, _ := client.Prefix(f.v6Prefix)
		return p
	}
	return netip.Prefix{}
}

// trustedNow reports whether some group's mark for key is within the trust
// window at now.
func (f *FailureLimiter) trustedNow(key netip.Prefix, now time.Time) bool {
	for _, at := range f.trusted[key] {
		if now.Sub(at) < f.limits.TrustWindow {
			return true
		}
	}
	return false
}

// charge records one failure for key at now, in its own log and the global
// log, and makes key the most recently charged, forgetting the least
// recently charged key when the held set is full.
func (f *FailureLimiter) charge(key netip.Prefix, own *keyFailures, now time.Time) {
	if own == nil {
		if f.lru.n >= f.limits.MaxKeys {
			last := f.lru.back()
			delete(f.keys, last.key)
			f.lru.remove(last)
		}
		own = &keyFailures{key: key, log: newFailureLog(f.limits.PerKey)}
		f.keys[key] = own
	} else {
		f.lru.remove(own)
	}
	f.lru.pushFront(own)
	own.log.add(now)
	if f.limits.Global > 0 {
		f.global.add(now)
	}
}

func (f *FailureLimiter) untrust(key netip.Prefix, group string) {
	delete(f.trusted[key], group)
	if len(f.trusted[key]) == 0 {
		delete(f.trusted, key)
	}
}

// failureLog is a ring holding the times of the latest failures, as many as
// the window's limit, so a full window is one whose oldest held failure is
// still inside it.
type failureLog struct {
	times []time.Time
	next  int
	held  int
}

func newFailureLog(limit int) failureLog { return failureLog{times: make([]time.Time, limit)} }

func (l *failureLog) add(at time.Time) {
	l.times[l.next] = at
	l.next = (l.next + 1) % len(l.times)
	l.held = min(l.held+1, len(l.times))
}

// count is how many held failures are inside window at now.
func (l *failureLog) count(now time.Time, window time.Duration) int {
	n := 0
	for i := range l.held {
		if now.Sub(l.times[i]) < window {
			n++
		}
	}
	return n
}

// wait is how long until the oldest held failure leaves window, at least a
// nanosecond, for a full log.
func (l *failureLog) wait(now time.Time, window time.Duration) time.Duration {
	oldest := l.times[l.next%len(l.times)]
	return max(window-now.Sub(oldest), time.Nanosecond)
}
