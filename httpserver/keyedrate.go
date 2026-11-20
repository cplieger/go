package httpserver

import (
	"container/heap"
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
// its burst is forgotten wherever it sits, and past MaxKeys the least recently
// used bucket is dropped, so memory is bounded by MaxKeys. It is safe for
// concurrent use.
type KeyedRateLimiter struct {
	now      func() time.Time
	buckets  map[string]*keyBucket
	byFull   bucketHeap // earliest fullAt first
	lru      keyBucket  // ring sentinel: lru.next is the most recently used
	interval time.Duration
	burst    float64
	maxKeys  int
	mu       sync.Mutex
}

type keyBucket struct {
	last       time.Time
	fullAt     time.Time // when tokens reach the burst again
	prev, next *keyBucket
	key        string
	tokens     float64
	index      int // position in byFull
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

// bucketHeap is a container/heap of buckets ordered by fullAt.
type bucketHeap []*keyBucket

func (h bucketHeap) Len() int           { return len(h) }
func (h bucketHeap) Less(i, j int) bool { return h[i].fullAt.Before(h[j].fullAt) }
func (h bucketHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}

func (h *bucketHeap) Push(x any) {
	b, _ := x.(*keyBucket) // only bucket pushes, always a *keyBucket
	b.index = len(*h)
	*h = append(*h, b)
}

func (h *bucketHeap) Pop() any {
	old := *h
	b := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return b
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
		wait, ok = 0, true
	} else {
		wait = time.Duration(math.Ceil((1 - b.tokens) * float64(l.interval)))
	}
	// Rounded up, so a bucket is forgotten only once it is surely full.
	b.fullAt = now.Add(time.Duration(math.Ceil((l.burst - b.tokens) * float64(l.interval))))
	heap.Fix(&l.byFull, b.index)
	return wait, ok
}

// bucket answers key's bucket marked most recently used, creating a full one
// and dropping the least recently used at maxKeys. The caller holds l.mu.
func (l *KeyedRateLimiter) bucket(key string, now time.Time) *keyBucket {
	if b, ok := l.buckets[key]; ok {
		b.unlink()
		l.pushFront(b)
		return b
	}
	if len(l.buckets) >= l.maxKeys {
		l.drop(l.lru.prev)
	}
	b := &keyBucket{key: key, tokens: l.burst, last: now, fullAt: now}
	l.buckets[key] = b
	l.pushFront(b)
	heap.Push(&l.byFull, b)
	return b
}

// forgetRefilled drops every bucket full again at now, which bucket would
// rebuild identically. The caller holds l.mu.
func (l *KeyedRateLimiter) forgetRefilled(now time.Time) {
	for len(l.byFull) > 0 && !l.byFull[0].fullAt.After(now) {
		l.drop(l.byFull[0])
	}
}

func (l *KeyedRateLimiter) drop(b *keyBucket) {
	delete(l.buckets, b.key)
	b.unlink()
	heap.Remove(&l.byFull, b.index)
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
