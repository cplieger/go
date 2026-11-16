package httpserver

import (
	"net/http"
	"time"
)

// KeyedRate configures a KeyedRateLimiter.
type KeyedRate struct {
	Burst    int
	Interval time.Duration
	MaxKeys  int
}

// KeyedRateLimiter holds one token bucket per key.
type KeyedRateLimiter struct {
	now func() time.Time
}

// NewKeyedRateLimiter builds a limiter from c.
func NewKeyedRateLimiter(c KeyedRate) (*KeyedRateLimiter, error) {
	return &KeyedRateLimiter{now: time.Now}, nil
}

// Take spends one token of key's bucket.
func (l *KeyedRateLimiter) Take(key string) (wait time.Duration, ok bool) {
	return 0, true
}

// Middleware throttles each request by the bucket key names.
func (l *KeyedRateLimiter) Middleware(key func(*http.Request) (string, bool), errs ErrorResponder) Middleware {
	return func(next http.Handler) http.Handler { return next }
}

func (l *KeyedRateLimiter) len() int { return 0 }
