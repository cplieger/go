package httpserver_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// BenchmarkKeyedRateLimiterNewKeyIntoFullTable takes a fresh key into a table
// held full of buckets that are all still refilling, the client-chosen-key
// churn a per-address limit faces.
func BenchmarkKeyedRateLimiterNewKeyIntoFullTable(b *testing.B) {
	for _, maxKeys := range []int{10_000, 1_000_000} {
		b.Run(strconv.Itoa(maxKeys), func(b *testing.B) {
			l, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{Burst: 2, Interval: time.Hour, MaxKeys: maxKeys})
			if err != nil {
				b.Fatal(err)
			}
			clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
			httpserver.SetKeyedRateClock(l, clock.read)
			for i := range maxKeys {
				l.Take(strconv.Itoa(i))
			}
			b.ReportAllocs()
			i := maxKeys
			for b.Loop() {
				l.Take(strconv.Itoa(i))
				i++
			}
		})
	}
}
