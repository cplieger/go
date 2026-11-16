package httpserver

import (
	"net"
	"net/http"
	"net/netip"
	"time"
)

// ForwardingFields projects parseForwarding's one parse of a request's
// forwarding fields, so black-box tests can derive ResolveClient's answer
// from it without parsing a header themselves.
type ForwardingFields struct {
	XFF, RealIP []ListEntry
	Forwarded   []ForwardedElement
}

type ListEntry struct {
	Addr netip.Addr
	Bare bool
}

type ForwardedElement struct {
	For   netip.Addr
	Valid bool
}

func ParseForwarding(h http.Header) ForwardingFields {
	f := parseForwarding(h)
	project := func(entries []listEntry) []ListEntry {
		var out []ListEntry
		for _, e := range entries {
			out = append(out, ListEntry{Addr: e.addr, Bare: e.bare})
		}
		return out
	}
	out := ForwardingFields{XFF: project(f.xff), RealIP: project(f.realIP)}
	for _, e := range f.forwarded {
		out.Forwarded = append(out.Forwarded, ForwardedElement{For: e.forNode, Valid: e.valid})
	}
	return out
}

// RouteMetricLabelsForTest exposes the label derivation WithRecordRouteMetric
// documents, so the external tests can pin it directly.
var RouteMetricLabelsForTest = routeMetricLabels

// CountDigestCompares counts Match's digest comparisons until the returned
// restore runs.
func CountDigestCompares() (calls *int, restore func()) {
	n, prev := 0, compareDigest
	compareDigest = func(x, y []byte) int { n++; return prev(x, y) }
	return &n, func() { compareDigest = prev }
}

// SetKeyedRateClock replaces l's clock, so a test moves time without sleeping.
func SetKeyedRateClock(l *KeyedRateLimiter, now func() time.Time) { l.now = now }

// KeyedRateBuckets answers how many buckets l holds.
func KeyedRateBuckets(l *KeyedRateLimiter) int { return l.len() }

// OpsServer and OpsListener answer the server and listener ListenOps built.
func OpsServer(o *Ops) *http.Server   { return o.srv }
func OpsListener(o *Ops) net.Listener { return o.ln }

// SetOpsBindRetry shortens ListenOps's bind retry for the test.
func SetOpsBindRetry(t interface{ Cleanup(func()) }, interval, window time.Duration) {
	prev := opsBindRetry
	opsBindRetry.interval, opsBindRetry.window = interval, window
	t.Cleanup(func() { opsBindRetry = prev })
}
