package httpserver

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// KeyedRate configures a KeyedRateLimiter. A zero field takes its default.
type KeyedRate struct {
	Burst    int           // tokens a key may spend at once and its initial fill; default 40, 1 to 10,000
	Interval time.Duration // the time to accrue one token; default 50 ms (20 a second), 1 ms to 1 hour
	MaxKeys  int           // buckets held at once; default 10,000, 16 to 1,000,000
}

// KeyedRateLimiter gives each key its own token bucket. A bucket refilled to
// its burst is forgotten, and past MaxKeys the least recently used bucket is
// dropped first, so memory is bounded by MaxKeys. It is safe for concurrent use.
type KeyedRateLimiter struct {
	now      func() time.Time
	buckets  map[string]*keyBucket
	lru      keyBucket // ring sentinel: lru.next is the most recently used
	interval time.Duration
	burst    float64
	maxKeys  int
	mu       sync.Mutex
}

type keyBucket struct {
	last       time.Time
	prev, next *keyBucket
	key        string
	tokens     float64
}

func (b *keyBucket) unlink() {
	b.prev.next, b.next.prev = b.next, b.prev
}

// pushFront links b just after the sentinel l.lru.
func (l *KeyedRateLimiter) pushFront(b *keyBucket) {
	b.prev, b.next = &l.lru, l.lru.next
	l.lru.next.prev = b
	l.lru.next = b
}

// NewKeyedRateLimiter builds a limiter from c, refusing a field outside its
// range and naming it.
func NewKeyedRateLimiter(c KeyedRate) (*KeyedRateLimiter, error) {
	if c.Burst == 0 {
		c.Burst = 40
	}
	if c.Interval == 0 {
		c.Interval = 50 * time.Millisecond
	}
	if c.MaxKeys == 0 {
		c.MaxKeys = 10_000
	}
	var errs []error
	if c.Burst < 1 || c.Burst > 10_000 {
		errs = append(errs, fmt.Errorf("KeyedRate.Burst %d is outside 1 to 10000", c.Burst))
	}
	if c.Interval < time.Millisecond || c.Interval > time.Hour {
		errs = append(errs, fmt.Errorf("KeyedRate.Interval %v is outside 1ms to 1h", c.Interval))
	}
	if c.MaxKeys < 16 || c.MaxKeys > 1_000_000 {
		errs = append(errs, fmt.Errorf("KeyedRate.MaxKeys %d is outside 16 to 1000000", c.MaxKeys))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	l := &KeyedRateLimiter{
		now:      time.Now,
		buckets:  make(map[string]*keyBucket),
		interval: c.Interval,
		burst:    float64(c.Burst),
		maxKeys:  c.MaxKeys,
	}
	l.lru.prev, l.lru.next = &l.lru, &l.lru
	return l, nil
}

// Take spends one token of key's bucket, or answers how long until one is
// back; a refused take spends nothing.
func (l *KeyedRateLimiter) Take(key string) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.forgetRefilled(now)
	b := l.bucket(key, now)
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.interval))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration(math.Ceil((1 - b.tokens) * float64(l.interval))), false
}

// bucket answers key's bucket marked most recently used, creating a full one.
// A new key at maxKeys first forgets every refilled bucket, since one can sit
// behind an older bucket still refilling, and only then drops the least
// recently used. The caller holds l.mu.
func (l *KeyedRateLimiter) bucket(key string, now time.Time) *keyBucket {
	if b, ok := l.buckets[key]; ok {
		b.unlink()
		l.pushFront(b)
		return b
	}
	if len(l.buckets) >= l.maxKeys {
		for b := l.lru.prev; b != &l.lru; {
			prev := b.prev
			if l.refilled(b, now) {
				l.drop(b)
			}
			b = prev
		}
	}
	if len(l.buckets) >= l.maxKeys {
		l.drop(l.lru.prev)
	}
	b := &keyBucket{key: key, tokens: l.burst, last: now}
	l.buckets[key] = b
	l.pushFront(b)
	return b
}

// forgetRefilled drops least recently used buckets that are full again at
// now, which bucket would rebuild identically. The caller holds l.mu.
func (l *KeyedRateLimiter) forgetRefilled(now time.Time) {
	for b := l.lru.prev; b != &l.lru && l.refilled(b, now); b = l.lru.prev {
		l.drop(b)
	}
}

func (l *KeyedRateLimiter) refilled(b *keyBucket, now time.Time) bool {
	return b.tokens+float64(now.Sub(b.last))/float64(l.interval) >= l.burst
}

func (l *KeyedRateLimiter) drop(b *keyBucket) {
	delete(l.buckets, b.key)
	b.unlink()
}

func (l *KeyedRateLimiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Middleware takes one token of the bucket key names for each request. A
// request whose key answers false passes untouched; a refused one answers
// rate_limited (429) through errs (nil: WriteError) with Retry-After in whole
// seconds rounded up, at least 1, and does not reach next. key must not be nil.
func (l *KeyedRateLimiter) Middleware(key func(*http.Request) (string, bool), errs ErrorResponder) Middleware {
	if key == nil {
		panic("httpserver: KeyedRateLimiter.Middleware with a nil key func")
	}
	if errs == nil {
		errs = WriteError
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k, ok := key(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if wait, ok := l.Take(k); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
				errs(w, r, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
