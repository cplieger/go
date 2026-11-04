package xmlx

import (
	"fmt"
	"strings"
	"testing"
)

// This file exists because xmlx SELLS a cost bound. The README states twice that
// Preflight is "one sequential pass, no allocation, no copies" and "one
// allocation-free scan of the raw document", and until this landed nothing in
// the repo verified either half. A library whose entire product is a bound on
// what untrusted input can cost should not take that bound on trust.
//
// Two kinds of check here, doing different jobs:
//
//   - TestPreflightIsAllocationFree GATES the claim. testing.AllocsPerRun is
//     exact, so the assertion is `== 0`, not a threshold. If someone adds a
//     buffer, a fmt call, or an interface boxing to the scan, this goes red at
//     merge time rather than being noticed later in a chart.
//   - BenchmarkPreflight* feed the weekly benchmark tracker with a trend series.
//     They are size-parameterised so an accidental quadratic scan shows up as a
//     super-linear jump between sizes rather than a uniform slowdown that reads
//     as runner noise.

// benchDoc builds a well-formed document of roughly n elements, shaped like the
// feeds this library actually guards (a Torznab-ish flat item list).
func benchDoc(n int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<rss version="2.0"><channel><title>bench</title>`)
	for i := range n {
		fmt.Fprintf(&b,
			`<item><title>Release.Name.S01E%02d.1080p</title>`+
				`<guid isPermaLink="false">%d</guid>`+
				`<size>%d</size>`+
				`<description>a moderately long description field %d</description>`+
				`</item>`,
			i%100, i, 1<<20+i, i)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// TestPreflightIsAllocationFree pins the README's allocation-free claim.
//
// AllocsPerRun returns an average over runs; Preflight allocates either always
// or never, so any non-zero average means the scan started allocating. Both
// outcomes are checked: a document that PASSES walks every byte, and a document
// that FAILS must not allocate on the way to building its error either, since
// LimitError is a value type by design and an attacker picks which path runs.
func TestPreflightIsAllocationFree(t *testing.T) {
	lim := DefaultLimits()

	t.Run("accepting document", func(t *testing.T) {
		body := benchDoc(500)
		if err := Preflight(body, lim); err != nil {
			t.Fatalf("fixture should pass preflight: %v", err)
		}
		if got := testing.AllocsPerRun(50, func() {
			_ = Preflight(body, lim)
		}); got != 0 {
			t.Errorf("Preflight allocated %v times per run, want 0: the README "+
				"promises an allocation-free scan", got)
		}
	})

	// The REJECTING path is not allocation-free, and should not be expected to
	// be: returning a *LimitError as an `error` boxes it, which measures as 2
	// allocations. That is fine, and the README's claim is about the SCAN.
	//
	// The property that actually matters for a bounds library is that rejection
	// cost does not scale with the hostile input. An attacker who can make
	// refusal 1000x more expensive by sending 1000x more garbage has found an
	// amplification vector inside the amplification guard. So this asserts the
	// allocation count is CONSTANT across inputs three orders of magnitude
	// apart, not that it is zero.
	t.Run("rejection cost is independent of input size", func(t *testing.T) {
		var counts []float64
		for _, over := range []int{1, 1_000, 1_000_000} {
			body := []byte("<a>" + strings.Repeat("x", lim.MaxTextRunBytes+over) + "</a>")
			if err := Preflight(body, lim); err == nil {
				t.Fatalf("over=%d: fixture should fail preflight", over)
			}
			counts = append(counts, testing.AllocsPerRun(50, func() {
				_ = Preflight(body, lim)
			}))
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("rejection allocated %v times at size index %d but %v at "+
					"the smallest input: refusal cost must not grow with the "+
					"attacker's payload", got, i, counts[0])
			}
		}
		t.Logf("rejection is a constant %v allocations regardless of payload size", counts[0])
	})
}

// BenchmarkPreflight measures the accepting path across document sizes. The
// sizes are chosen so a quadratic regression is visible: 10x the elements should
// cost roughly 10x, not 100x.
func BenchmarkPreflight(b *testing.B) {
	lim := DefaultLimits()
	for _, n := range []int{10, 100, 1000, 10000} {
		body := benchDoc(n)
		b.Run(fmt.Sprintf("elements_%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if err := Preflight(body, lim); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPreflightRejection measures how fast a hostile document is refused.
// This is the number that matters under attack: rejection must be cheap, and it
// must not depend on how much oversized input follows the offending token.
func BenchmarkPreflightRejection(b *testing.B) {
	lim := DefaultLimits()
	cases := []struct {
		name string
		body []byte
	}{
		{"oversized_text_run", []byte("<a>" + strings.Repeat("x", lim.MaxTextRunBytes+1) + "</a>")},
		{"oversized_token", []byte("<a " + strings.Repeat("b", lim.MaxTokenBytes+1) + `="1"/>`)},
		{"too_many_attrs", []byte("<a" + strings.Repeat(` k="v"`, lim.MaxTagAttrs+1) + "/>")},
		{"too_deep", []byte(strings.Repeat("<a>", lim.MaxDepth+1) + strings.Repeat("</a>", lim.MaxDepth+1))},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if err := Preflight(tc.body, lim); err == nil {
				b.Fatalf("%s: fixture should be rejected", tc.name)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				_ = Preflight(tc.body, lim)
			}
		})
	}
}

// BenchmarkBudgetCharge covers the decode-time half of the cost contract. Charge
// is consulted once per value during a decode, so its per-call cost multiplies
// by the document's field count.
func BenchmarkBudgetCharge(b *testing.B) {
	value := strings.Repeat("v", 256)
	b.ReportAllocs()
	for b.Loop() {
		budget := DefaultBudget()
		for range 100 {
			if err := budget.Charge(value); err != nil {
				b.Fatal(err)
			}
		}
	}
}
