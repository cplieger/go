package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

func newKeyed(t *testing.T, c httpserver.KeyedRate) (*httpserver.KeyedRateLimiter, *fakeClock) {
	t.Helper()
	l, err := httpserver.NewKeyedRateLimiter(c)
	if err != nil {
		t.Fatalf("NewKeyedRateLimiter(%+v): %v", c, err)
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	httpserver.SetKeyedRateClock(l, clock.read)
	return l, clock
}

// drain spends n tokens of key, failing on any refusal.
func drain(t *testing.T, l *httpserver.KeyedRateLimiter, key string, n int) {
	t.Helper()
	for i := range n {
		if _, ok := l.Take(key); !ok {
			t.Fatalf("Take(%q) refused at token %d of %d", key, i+1, n)
		}
	}
}

func TestKeyedRateLimiterDefaults(t *testing.T) {
	l, _ := newKeyed(t, httpserver.KeyedRate{})
	drain(t, l, "k", 40)
	wait, ok := l.Take("k")
	if ok {
		t.Fatal("token 41 admitted, want the default burst of 40")
	}
	if wait != 50*time.Millisecond {
		t.Errorf("wait = %v, want the default interval of 50ms", wait)
	}
}

func TestNewKeyedRateLimiterRefusesOutOfRange(t *testing.T) {
	cases := []struct {
		c     httpserver.KeyedRate
		field string
	}{
		{httpserver.KeyedRate{Burst: -1}, "Burst"},
		{httpserver.KeyedRate{Burst: 10_001}, "Burst"},
		{httpserver.KeyedRate{Interval: -time.Second}, "Interval"},
		{httpserver.KeyedRate{Interval: time.Microsecond}, "Interval"},
		{httpserver.KeyedRate{Interval: time.Hour + time.Nanosecond}, "Interval"},
		{httpserver.KeyedRate{MaxKeys: -1}, "MaxKeys"},
		{httpserver.KeyedRate{MaxKeys: 15}, "MaxKeys"},
		{httpserver.KeyedRate{MaxKeys: 1_000_001}, "MaxKeys"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%+v", tc.c), func(t *testing.T) {
			l, err := httpserver.NewKeyedRateLimiter(tc.c)
			if err == nil || l != nil {
				t.Fatalf("NewKeyedRateLimiter = %v, %v; want an error", l, err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %s", err, tc.field)
			}
		})
	}
	for _, c := range []httpserver.KeyedRate{
		{Burst: 1, Interval: time.Millisecond, MaxKeys: 16},
		{Burst: 10_000, Interval: time.Hour, MaxKeys: 1_000_000},
	} {
		if _, err := httpserver.NewKeyedRateLimiter(c); err != nil {
			t.Errorf("NewKeyedRateLimiter(%+v): %v, want the bound accepted", c, err)
		}
	}
}

func TestKeyedRateLimiterBucketPerKey(t *testing.T) {
	l, _ := newKeyed(t, httpserver.KeyedRate{Burst: 3, Interval: time.Second})
	drain(t, l, "a", 3)
	if _, ok := l.Take("a"); ok {
		t.Fatal("key a admitted past its burst")
	}
	drain(t, l, "b", 3)
}

func TestKeyedRateLimiterRefusalSpendsNothing(t *testing.T) {
	l, clock := newKeyed(t, httpserver.KeyedRate{Burst: 2, Interval: time.Second})
	drain(t, l, "k", 2)
	clock.advance(400 * time.Millisecond)
	for range 5 {
		wait, ok := l.Take("k")
		if ok || wait != 600*time.Millisecond {
			t.Fatalf("Take = %v, %v; want refused with 600ms to wait", wait, ok)
		}
	}
	clock.advance(600 * time.Millisecond)
	drain(t, l, "k", 1)
	if _, ok := l.Take("k"); ok {
		t.Fatal("a second token appeared after one interval")
	}
}

func TestKeyedRateLimiterForgetsRefilledBucket(t *testing.T) {
	l, clock := newKeyed(t, httpserver.KeyedRate{Burst: 4, Interval: time.Second})
	drain(t, l, "a", 4)
	clock.advance(3 * time.Second)
	drain(t, l, "b", 4)
	if n := httpserver.KeyedRateBuckets(l); n != 2 {
		t.Fatalf("buckets = %d, want 2 while a is still refilling", n)
	}
	clock.advance(time.Second)
	drain(t, l, "c", 1)
	if n := httpserver.KeyedRateBuckets(l); n != 2 {
		t.Fatalf("buckets = %d, want 2 (b and c) after a refilled", n)
	}
	drain(t, l, "a", 4)
}

func TestKeyedRateLimiterHoldsAtMostMaxKeys(t *testing.T) {
	l, _ := newKeyed(t, httpserver.KeyedRate{Burst: 2, Interval: time.Hour, MaxKeys: 16})
	drain(t, l, "kept", 2)
	for i := range 1000 {
		drain(t, l, fmt.Sprintf("churn-%d", i), 1)
		if i%10 == 0 {
			if _, ok := l.Take("kept"); ok {
				t.Fatalf("after %d churned keys the recently used key lost its empty bucket", i+1)
			}
		}
		if n := httpserver.KeyedRateBuckets(l); n > 16 {
			t.Fatalf("buckets = %d after %d keys, want at most MaxKeys 16", n, i+1)
		}
	}
	drain(t, l, "churn-0", 2)
}

func TestKeyedRateLimiterConcurrentTakes(t *testing.T) {
	l, _ := newKeyed(t, httpserver.KeyedRate{Burst: 100, Interval: time.Hour, MaxKeys: 16})
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if _, ok := l.Take("shared"); ok {
					mu.Lock()
					admitted++
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	if admitted != 100 {
		t.Errorf("admitted = %d over 400 concurrent takes, want the burst of 100", admitted)
	}
}

func TestKeyedRateLimiterMiddlewareAnswersRateLimited(t *testing.T) {
	cases := []struct {
		interval  time.Duration
		retryWant string
	}{
		{1500 * time.Millisecond, "2"},
		{time.Millisecond, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.interval.String(), func(t *testing.T) {
			l, _ := newKeyed(t, httpserver.KeyedRate{Burst: 1, Interval: tc.interval})
			key := func(r *http.Request) (string, bool) {
				k := r.Header.Get("X-Key")
				return k, k != ""
			}
			hits := 0
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ })
			h := l.Middleware(key, nil)(next)
			serve := func(k string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
				if k != "" {
					r.Header.Set("X-Key", k)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				return rec
			}
			if rec := serve("a"); rec.Code != http.StatusOK || hits != 1 {
				t.Fatalf("first request: status %d, hits %d; want admitted", rec.Code, hits)
			}
			rec := serve("a")
			if rec.Code != http.StatusTooManyRequests || hits != 1 {
				t.Fatalf("second request: status %d, hits %d; want 429 without reaching next", rec.Code, hits)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.retryWant {
				t.Errorf("Retry-After = %q, want %q", got, tc.retryWant)
			}
			if env := strictEnvelope(t, rec); env.Code != "rate_limited" {
				t.Errorf("code = %q, want rate_limited", env.Code)
			}
			for range 5 {
				if rec := serve(""); rec.Code != http.StatusOK {
					t.Fatalf("a request with no key answered %d, want it passed untouched", rec.Code)
				}
			}
			if rec := serve("b"); rec.Code != http.StatusOK {
				t.Errorf("key b answered %d, want its own full bucket", rec.Code)
			}
		})
	}
}

func TestKeyedRateLimiterMiddlewareUsesResponder(t *testing.T) {
	l, _ := newKeyed(t, httpserver.KeyedRate{Burst: 1, Interval: time.Second})
	var got []httpserver.ErrorCode
	errs := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		got = append(got, code)
		httpserver.WriteError(w, r, status, code, msg)
	}
	h := l.Middleware(func(*http.Request) (string, bool) { return "k", true }, errs)(http.NotFoundHandler())
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	}
	if len(got) != 1 || got[0] != "rate_limited" {
		t.Errorf("responder codes = %v, want one rate_limited", got)
	}
}
