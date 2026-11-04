package jsonx_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// This file pins the cost claim the rest of the suite leaves to prose: the
// walk's allocation is bounded by the caller's caps, not by the cardinality
// on the wire.
//
// Allocation COUNT is the metric, not bytes: json.Unmarshal can allocate
// fewer, larger chunks than the bounded walk while still amplifying far more
// bytes, so a byte comparison belongs in the benchmarks below, not here.
//
// An exact count cannot be asserted: since Go 1.27 encoding/json is backed
// by a pooled coder, so under -race a single testing.AllocsPerRun sample is
// nondeterministic. minAllocsPerRun below takes the minimum over several
// trials as the steady-state cost, and the assertions use a ratio ceiling
// rather than an equality so a real regression is still caught.
//
// These tests do not call t.Parallel: AllocsPerRun pins GOMAXPROCS to 1 for
// its window, so a concurrent sibling would only add noise.

// hostileArray renders the amplification shape the package exists to bound:
// n compact serialized elements, cheap on the wire and expensive to decode.
func hostileArray(n int) []byte {
	var b strings.Builder
	b.Grow(3*n + 16)
	b.WriteString(`{"parts":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// minAllocsPerRun is testing.AllocsPerRun's steady-state cost: the minimum
// over several trials, discarding the pooled coder's cold-start samples and
// any allocation a concurrently running test contributed to the window.
func minAllocsPerRun(runs, trials int, f func()) float64 {
	best := testing.AllocsPerRun(runs, f)
	for range trials - 1 {
		if got := testing.AllocsPerRun(runs, f); got < best {
			best = got
		}
	}
	return best
}

// TestBoundedAllocationDoesNotScaleWithCardinality is the package's reason to
// exist, as an assertion: a caller whose per-array cap is 16 pays for 16
// elements whatever the body claims. The two bodies differ 1000-fold in
// cardinality and wire size, and the walk must reject both at the same cost.
// The ceiling is a ratio rather than an absolute count because a mutant that
// breaks the bound inflates the baseline too; growth is logarithmic rather
// than linear because the regrow goes through append.
func TestBoundedAllocationDoesNotScaleWithCardinality(t *testing.T) {
	const cap = 16
	small, large := hostileArray(1000), hostileArray(1_000_000)

	// Both bodies must actually reach the cap, or the test measures nothing.
	if _, err := budgetWidget(small, 0, cap, 0); !errorIsArrayCap(err) {
		t.Fatalf("small body: err = %v, want ErrArrayCap (the measurement needs the cap to fire)", err)
	}
	if _, err := budgetWidget(large, 0, cap, 0); !errorIsArrayCap(err) {
		t.Fatalf("large body: err = %v, want ErrArrayCap (the measurement needs the cap to fire)", err)
	}

	lo := minAllocsPerRun(5, 4, func() { _, _ = budgetWidget(small, 0, cap, 0) })
	hi := minAllocsPerRun(5, 4, func() { _, _ = budgetWidget(large, 0, cap, 0) })

	if ceiling := 1.5 * lo; hi > ceiling {
		t.Errorf("allocations grew with cardinality: %.0f at 1000 elements (%d bytes) -> %.0f at 1000000 (%d bytes), want no growth (ceiling %.0f)",
			lo, len(small), hi, len(large), ceiling)
	}
}

// TestPreflightAllocationBoundedByDepthCeiling pins the cost half of the
// nesting bound: the depth guard stops the walk at MaxDepth frames rather
// than at the end of the input, so a much larger all-opens body costs the
// same as a small one. TestPreflightBoundsAllOpensBody asserts only that the
// body IS refused, which a walk that recursed once per byte before failing
// would also satisfy.
func TestPreflightAllocationBoundedByDepthCeiling(t *testing.T) {
	small := bytes.Repeat([]byte("["), 1<<16)
	large := bytes.Repeat([]byte("["), 1<<22)

	if err := jsonx.Preflight(bytes.NewReader(large)); err == nil {
		t.Fatal("Preflight(4 MiB of open brackets) = nil, want it rejected")
	}

	lo := minAllocsPerRun(3, 4, func() { _ = jsonx.Preflight(bytes.NewReader(small)) })
	hi := minAllocsPerRun(3, 4, func() { _ = jsonx.Preflight(bytes.NewReader(large)) })

	if ceiling := 1.5 * lo; hi > ceiling {
		t.Errorf("allocations grew with input length: %.0f at %d bytes -> %.0f at %d bytes, want the cost bounded by MaxDepth (ceiling %.0f)",
			lo, len(small), hi, len(large), ceiling)
	}
}

// errorIsArrayCap keeps the cap check above readable without importing errors
// for one call.
func errorIsArrayCap(err error) bool {
	return err != nil && strings.Contains(err.Error(), jsonx.ErrArrayCap.Error())
}

// BenchmarkHostileArray is the amplification claim as a trend rather than a
// threshold: run it and read B/op.
//
// Neither arm calls b.SetBytes, deliberately: the bounded arm stops at
// element 16 and never reads the rest of the body, so a MB/s figure would
// divide work that did not happen by time that did.
func BenchmarkHostileArray(b *testing.B) {
	body := hostileArray(100_000)

	b.Run("unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var w widget
			_ = json.Unmarshal(body, &w)
		}
	})

	b.Run("bounded-cap16", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = budgetWidget(body, 0, 16, 0)
		}
	})
}

// BenchmarkPreflight covers the two shapes whose cost is upstream-controlled:
// a wide object, where every key pays a foldKey canonicalization and a set
// insert, and the all-opens body the depth ceiling has to stop.
func BenchmarkPreflight(b *testing.B) {
	var wide strings.Builder
	wide.WriteByte('{')
	for i := range 1000 {
		if i > 0 {
			wide.WriteByte(',')
		}
		wide.WriteString(`"key`)
		wide.WriteString(strings.Repeat("x", 8))
		wide.WriteString(string(rune('a' + i%26)))
		wide.WriteString(`":1`)
	}
	wide.WriteByte('}')
	wideBody := []byte(wide.String())
	opens := bytes.Repeat([]byte("["), 1<<20)

	b.Run("wide-object", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(wideBody)))
		for b.Loop() {
			_ = jsonx.Preflight(bytes.NewReader(wideBody))
		}
	})

	// No SetBytes on this arm: the depth ceiling stops the walk after
	// MaxDepth of the brackets, so throughput over the whole body is fiction.
	b.Run("all-opens", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = jsonx.Preflight(bytes.NewReader(opens))
		}
	})
}

// BenchmarkSkip is the unknown-field path. Skip builds no Go value, which is
// the claim its doc makes, but it is NOT allocation-free: json.Decoder.Token
// returns each token as an any, so the cost is proportional to the token
// count of the skipped value.
func BenchmarkSkip(b *testing.B) {
	var body strings.Builder
	body.WriteString(`{"unknown":[`)
	for i := range 1000 {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"a":1,"b":"xx"}`)
	}
	body.WriteString(`],"name":"kept"}`)
	raw := []byte(body.String())

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		d := jsonx.NewDecoder(bytes.NewReader(raw), 0)
		var w widget
		_ = decodeWidget(d, &w, 0, 0)
		_ = d.End()
	}
}
