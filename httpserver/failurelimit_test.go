package httpserver_test

import (
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver"
)

// knellLimits are the numbers knell v4 passes.
var knellLimits = httpserver.FailureLimits{
	PerKey: 10, PerKeyWindow: 15 * time.Minute,
	Global: 60, GlobalWindow: 15 * time.Minute,
	MaxKeys: 10000, TrustPerGroup: 16, TrustWindow: time.Hour,
}

// fakeClock is a settable clock for a limiter.
type fakeClock struct{ now time.Time }

func (c *fakeClock) read() time.Time         { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newLimiter(t *testing.T, l httpserver.FailureLimits) (*httpserver.FailureLimiter, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l.Now = clock.read
	f, err := httpserver.NewFailureLimiter(l)
	if err != nil {
		t.Fatal(err)
	}
	return f, clock
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// v4 is the n-th address of 198.51.100.0/24's neighbourhood, one key each.
func v4(n int) netip.Addr { return netip.AddrFrom4([4]byte{198, 51, byte(n / 256), byte(n % 256)}) }

func fail(t *testing.T, f *httpserver.FailureLimiter, a netip.Addr, n int) {
	t.Helper()
	for i := range n {
		if throttled, _ := f.Decide(a, true); throttled {
			t.Fatalf("failure %d of %d from %v was throttled", i+1, n, a)
		}
	}
}

func TestNewFailureLimiter_refuses(t *testing.T) {
	cases := []struct {
		name string
		edit func(*httpserver.FailureLimits)
		want string
	}{
		{"no per-key limit", func(l *httpserver.FailureLimits) { l.PerKey = 0 }, "PerKey"},
		{"no per-key window", func(l *httpserver.FailureLimits) { l.PerKeyWindow = 0 }, "PerKey"},
		{"a global limit with no window", func(l *httpserver.FailureLimits) { l.GlobalWindow = 0 }, "Global"},
		{"a global window with no limit", func(l *httpserver.FailureLimits) { l.Global = 0 }, "Global"},
		{"a negative global limit", func(l *httpserver.FailureLimits) { l.Global = -1 }, "Global"},
		{"no held keys", func(l *httpserver.FailureLimits) { l.MaxKeys = 0 }, "MaxKeys"},
		{"no trust slots", func(l *httpserver.FailureLimits) { l.TrustPerGroup = 0 }, "Trust"},
		{"no trust window", func(l *httpserver.FailureLimits) { l.TrustWindow = 0 }, "Trust"},
		{"an IPv6 prefix past 128", func(l *httpserver.FailureLimits) { l.IPv6Prefix = 129 }, "IPv6Prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := knellLimits
			tc.edit(&l)
			if _, err := httpserver.NewFailureLimiter(l); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewFailureLimiter error = %v, want one naming %s", err, tc.want)
			}
		})
	}
	t.Run("the global window off", func(t *testing.T) {
		l := knellLimits
		l.Global, l.GlobalWindow = 0, 0
		if _, err := httpserver.NewFailureLimiter(l); err != nil {
			t.Errorf("Global and GlobalWindow both zero: %v", err)
		}
	})
}

func TestFailureLimiter_perKeyWindow(t *testing.T) {
	f, clock := newLimiter(t, knellLimits)
	a := addr("203.0.113.7")
	for range 10 {
		fail(t, f, a, 1)
		clock.advance(time.Minute)
	}
	// Ten failures a minute apart, the first 10m ago: the window is full
	// until the first is 15m old.
	throttled, wait := f.Decide(a, true)
	if !throttled || wait != 5*time.Minute {
		t.Fatalf("the 11th failure = (%v, %v), want throttled for 5m", throttled, wait)
	}
	if throttled, _ := f.Decide(a, false); !throttled {
		t.Error("a success from a full key was not throttled")
	}
	if throttled, _ := f.Decide(addr("203.0.113.8"), true); throttled {
		t.Error("another key was throttled by one key's window")
	}
	clock.advance(5 * time.Minute)
	if throttled, _ := f.Decide(a, true); throttled {
		t.Error("the key was still throttled once its oldest failure left the window")
	}
	if throttled, wait := f.Decide(a, true); !throttled || wait != time.Minute {
		t.Errorf("after one slot freed and was used = (%v, %v), want throttled for 1m", throttled, wait)
	}
}

func TestFailureLimiter_globalWindow(t *testing.T) {
	f, clock := newLimiter(t, knellLimits)
	for k := range 6 {
		fail(t, f, v4(k), 10)
	}
	throttled, wait := f.Decide(v4(100), true)
	if !throttled || wait != 15*time.Minute {
		t.Fatalf("a fresh key past a full global window = (%v, %v), want throttled for 15m", throttled, wait)
	}
	if throttled, _ := f.Decide(v4(101), false); !throttled {
		t.Error("a success from a key that is not exempt passed a full global window")
	}
	clock.advance(15 * time.Minute)
	if throttled, _ := f.Decide(v4(100), true); throttled {
		t.Error("the global window still throttled once its failures expired")
	}
}

func TestFailureLimiter_waitIsTheLonger(t *testing.T) {
	f, clock := newLimiter(t, httpserver.FailureLimits{
		PerKey: 2, PerKeyWindow: time.Minute, Global: 2, GlobalWindow: 10 * time.Minute,
		MaxKeys: 10, TrustPerGroup: 1, TrustWindow: time.Hour,
	})
	a := addr("192.0.2.1")
	fail(t, f, a, 2)
	clock.advance(30 * time.Second)
	if _, wait := f.Decide(a, true); wait != 10*time.Minute-30*time.Second {
		t.Errorf("wait = %v, want the global window's 9m30s, the longer of the two", wait)
	}
}

func TestFailureLimiter_waitIsThePerKeyWhenLonger(t *testing.T) {
	f, clock := newLimiter(t, httpserver.FailureLimits{
		PerKey: 2, PerKeyWindow: 10 * time.Minute, Global: 2, GlobalWindow: time.Minute,
		MaxKeys: 10, TrustPerGroup: 1, TrustWindow: time.Hour,
	})
	a := addr("192.0.2.1")
	fail(t, f, a, 2)
	clock.advance(30 * time.Second)
	if _, wait := f.Decide(a, true); wait != 10*time.Minute-30*time.Second {
		t.Errorf("wait = %v, want the key's 9m30s, the longer of the two", wait)
	}
}

func TestFailureLimiter_chargeRefreshesTheKey(t *testing.T) {
	l := knellLimits
	l.PerKey, l.MaxKeys = 2, 2
	f, _ := newLimiter(t, l)
	a, b, c := addr("192.0.2.1"), addr("192.0.2.2"), addr("192.0.2.3")
	fail(t, f, a, 1)
	fail(t, f, b, 1)
	fail(t, f, a, 1) // a is now the most recently charged
	fail(t, f, c, 1) // evicts b, the least recently charged
	if throttled, _ := f.Decide(a, true); !throttled {
		t.Error("a recharged key was evicted in place of the least recently charged one")
	}
	if throttled, _ := f.Decide(b, true); throttled {
		t.Error("the least recently charged key kept its failures")
	}
}

func TestFailureLimiter_holdsMaxKeys(t *testing.T) {
	l := knellLimits
	l.PerKey, l.MaxKeys = 1, 2
	f, _ := newLimiter(t, l)
	a, b, c := addr("192.0.2.1"), addr("192.0.2.2"), addr("192.0.2.3")
	fail(t, f, a, 1)
	fail(t, f, b, 1)
	fail(t, f, c, 1) // a third key evicts a
	for _, k := range []netip.Addr{b, c} {
		if throttled, _ := f.Decide(k, false); !throttled {
			t.Errorf("%v lost its failure with only MaxKeys keys held", k)
		}
	}
	if throttled, _ := f.Decide(a, false); throttled {
		t.Error("MaxKeys+1 keys were held")
	}
}

func TestFailureLimiter_throttledChangesNothing(t *testing.T) {
	l := knellLimits
	l.MaxKeys = 2
	f, _ := newLimiter(t, l)
	trustedKey := addr("192.0.2.10")
	f.MarkGood("job", trustedKey)
	fail(t, f, trustedKey, 1)
	fail(t, f, v4(1), 10)
	// v4(1) is full: a throttled flood from it must neither charge the
	// global window nor take an LRU slot, so trustedKey's failure stays held.
	for range 1000 {
		if throttled, _ := f.Decide(v4(1), true); !throttled {
			t.Fatal("a full key was not throttled")
		}
	}
	for k := 2; k <= 6; k++ {
		fail(t, f, v4(k), 9)
	}
	// 1 + 10 + 45 = 56 charged: a throttled flood that charged would be past 60.
	if throttled, _ := f.Decide(v4(50), true); throttled {
		t.Error("throttled requests were charged to the global window")
	}
}

func TestFailureLimiter_throttledSprayCannotEvictTrustedFailure(t *testing.T) {
	l := knellLimits
	l.MaxKeys = 3
	l.Global, l.GlobalWindow = 5, 15*time.Minute
	f, _ := newLimiter(t, l)
	trustedKey := addr("192.0.2.10")
	f.MarkGood("job", trustedKey)
	fail(t, f, trustedKey, 1)
	fail(t, f, v4(1), 4)
	// The global window is full; trustedKey's failure removes its exemption.
	for k := 2; k < 500; k++ {
		if throttled, _ := f.Decide(v4(k), true); !throttled {
			t.Fatalf("spray key %d passed a full global window", k)
		}
	}
	if throttled, _ := f.Decide(trustedKey, false); !throttled {
		t.Error("a throttled spray evicted the trusted key's failure and restored its exemption")
	}
}

func TestFailureLimiter_trust(t *testing.T) {
	t.Run("a trusted key with no failure passes a full global window", func(t *testing.T) {
		f, _ := newLimiter(t, knellLimits)
		job := addr("192.0.2.10")
		f.MarkGood("entry-1", job)
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(job, false); throttled {
			t.Error("a trusted key was throttled by the global window")
		}
	})
	t.Run("the first failure ends the exemption", func(t *testing.T) {
		f, _ := newLimiter(t, knellLimits)
		job := addr("192.0.2.10")
		f.MarkGood("entry-1", job)
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(job, true); throttled {
			t.Fatal("the trusted key's first failure was throttled")
		}
		if throttled, _ := f.Decide(job, false); !throttled {
			t.Error("a trusted key with a failure in its window stayed exempt")
		}
	})
	t.Run("a mark lasts the trust window", func(t *testing.T) {
		f, clock := newLimiter(t, knellLimits)
		job := addr("192.0.2.10")
		f.MarkGood("entry-1", job)
		clock.advance(time.Hour)
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(job, false); !throttled {
			t.Error("a mark older than the trust window still exempted its key")
		}
	})
	t.Run("a repeat mark refreshes without taking a slot", func(t *testing.T) {
		l := knellLimits
		l.TrustPerGroup = 2
		f, clock := newLimiter(t, l)
		a, b, c := addr("192.0.2.1"), addr("192.0.2.2"), addr("192.0.2.3")
		f.MarkGood("entry-1", a)
		clock.advance(time.Minute)
		f.MarkGood("entry-1", b)
		clock.advance(time.Minute)
		f.MarkGood("entry-1", a)
		clock.advance(time.Minute)
		f.MarkGood("entry-1", c) // replaces b, now the oldest
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(a, false); throttled {
			t.Error("a refreshed key was replaced")
		}
		if throttled, _ := f.Decide(c, false); throttled {
			t.Error("the new key was not trusted")
		}
		if throttled, _ := f.Decide(b, false); !throttled {
			t.Error("the group's oldest key was not the one replaced")
		}
	})
	t.Run("a group replaces only its own keys", func(t *testing.T) {
		l := knellLimits
		l.TrustPerGroup = 1
		f, _ := newLimiter(t, l)
		other, mine, next := addr("192.0.2.1"), addr("192.0.2.2"), addr("192.0.2.3")
		f.MarkGood("entry-2", other)
		f.MarkGood("entry-1", mine)
		f.MarkGood("entry-1", next)
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(other, false); throttled {
			t.Error("entry-1's marks replaced entry-2's key")
		}
		if throttled, _ := f.Decide(mine, false); !throttled {
			t.Error("entry-1 kept more keys than TrustPerGroup")
		}
	})
	t.Run("an LRU eviction keeps trust", func(t *testing.T) {
		l := knellLimits
		l.MaxKeys = 1
		f, _ := newLimiter(t, l)
		job := addr("192.0.2.10")
		f.MarkGood("entry-1", job)
		for k := range 6 {
			fail(t, f, v4(k), 10)
		}
		if throttled, _ := f.Decide(job, false); throttled {
			t.Error("evicting failure records dropped a key's trust")
		}
	})
}

func TestFailureLimiter_keys(t *testing.T) {
	cases := []struct {
		name     string
		first    string
		second   string
		sameKey  bool
		v6Prefix int
	}{
		{"one IPv6 /64", "2001:db8:1:2::1", "2001:db8:1:2:ffff::9", true, 0},
		{"two IPv6 /64s", "2001:db8:1:2::1", "2001:db8:1:3::1", false, 0},
		{"one IPv6 /48 at a /48 prefix", "2001:db8:1:2::1", "2001:db8:1:3::1", true, 48},
		{"an IPv4-mapped address and its IPv4", "::ffff:192.0.2.1", "192.0.2.1", true, 0},
		{"a zone dropped", "fe80::1%eth0", "fe80::1", true, 0},
		{"two IPv4 addresses", "192.0.2.1", "192.0.2.2", false, 0},
		{"two invalid addresses share one key", "", "", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := knellLimits
			l.PerKey, l.IPv6Prefix = 1, tc.v6Prefix
			f, _ := newLimiter(t, l)
			parse := func(s string) netip.Addr {
				if s == "" {
					return netip.Addr{}
				}
				return addr(s)
			}
			fail(t, f, parse(tc.first), 1)
			throttled, _ := f.Decide(parse(tc.second), true)
			if throttled != tc.sameKey {
				t.Errorf("second address throttled = %v, want %v (same key)", throttled, tc.sameKey)
			}
		})
	}
}

func TestFailureLimiter_ConcurrentFailuresNeverPassAFullWindow(t *testing.T) {
	f, err := httpserver.NewFailureLimiter(knellLimits)
	if err != nil {
		t.Fatal(err)
	}
	a := addr("203.0.113.7")
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 1000 {
		wg.Go(func() {
			if throttled, _ := f.Decide(a, true); !throttled {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	if n := admitted.Load(); n != 10 {
		t.Errorf("concurrent failures admitted %d, want exactly the per-key limit 10", n)
	}
}
