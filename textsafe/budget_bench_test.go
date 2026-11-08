package textsafe_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/cplieger/go/textsafe"
)

// Budget and the Capped pair are sold entirely on a COST property: for a valid-UTF-8
// value carrying no unsafe rune they are byte-identical to sanitizing first and then
// capping, cut for cut, but the cap sits AHEAD of the sanitizer, so walking a
// multi-megabyte upstream value inside a memory-limited process (CWE-400 work
// amplification) stops being possible. The two bounds gated here are that the
// cap-first order's cost does not grow with the raw value's size, and that once the
// allowance is spent a further write costs nothing at all — which is what stops a
// hostile piece COUNT amplifying the aggregate.
//
// One thing the doc comments do not claim, and a reader could assume the other way
// round: for a handful of small HONEST values the streaming Budget is the DEARER shape
// (8 values of 16 bytes under a 200-byte budget cost 7 allocations and 544 bytes
// streaming against 2 and 304 joined). The shapes cross over as values grow — by
// 64 KiB each it is 1.5 us against 2.5 ms. The Budget is a bound on the hostile case,
// not an optimization for the honest one, and BenchmarkAggregate shows both halves.

// benchGroups is the aggregate fixture: several upstream-controlled values headed for
// ONE log attribute under ONE shared byte budget. Two carry unsafe runes and one of
// those is FIRST, so the measured path includes a rewrite; with a clean value leading,
// the whole first write is absorbed by the sanitizer's fast path and the series would
// describe the wrong work.
func benchGroups(each int) []string {
	units := []string{
		"Group\x1bBeta-",
		"Group-Alpha-",
		"Grüppe-Gamma-",
		"Group\u202eDelta-",
		"Group-Epsilon-",
		"Group-Zeta-",
		"Group-Eta-",
		"Group-Theta-",
	}
	groups := make([]string, len(units))
	for i, u := range units {
		groups[i] = repeatTo(u, each)
	}
	return groups
}

// writeGroups runs the loop the README documents for Budget: the separator goes
// through Write too, and the caller stops when a write is REFUSED rather than when the
// budget looks full, because the refusal is what latches the truncation fact.
// TestBudgetSpentAllowanceChargesNothingPerValue deliberately does not use it, for the
// reason given there.
func writeGroups(b *textsafe.Budget, groups []string) (string, bool) {
	for i, g := range groups {
		if i > 0 && !b.Write(", ") {
			break
		}
		if !b.Write(g) {
			break
		}
	}
	return b.Result()
}

// allocBytesPerRun is testing.AllocsPerRun's missing twin: mean BYTES allocated per
// call rather than mean allocations. The work bound this file gates is a bytes
// property, and the allocation COUNT is a poor witness for it — deleting Write's
// pre-cap entirely leaves the count unchanged for every input class but one, since a
// value the sanitizer merely rewrites needs exactly one buffer whatever its size.
// Bytes see it immediately: 368 against four million.
//
// TotalAlloc is cumulative and monotonic, so the delta over a known number of calls is
// the mean directly. Background allocation from the runtime can add to it, which is why
// every assertion built on this compares magnitudes with wide headroom rather than
// exact equality.
func allocBytesPerRun(runs int, f func()) float64 {
	f() // Warm up, so first-call lazy initialization is not charged to the mean.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

// TestSanitizeCappedWorkBoundDoesNotScaleWithInput gates the reason the Capped
// pair exists: it caps the RAW bytes on a rune boundary before the sanitizer walks
// them, so its cost is a function of the CAP and not of the value an upstream chose to
// send.
//
// The assertion is on allocated BYTES, because that is what the claim is about and the
// allocation count barely moves — see allocBytesPerRun. With a 64-byte cap over
// invalid UTF-8 the cap-first order allocates 368 bytes per call at every raw size from
// 1 KiB to 1 MiB, while sanitizing first (legacyCapped) runs about 3.0 KB, 98 KB, then
// 4.0 MB. Deleting the pre-cap puts the Capped pair on the sanitize-first numbers,
// which is how this assertion was seen to fail.
//
// The tolerance is a factor of two across a 1024x increase in input, not equality:
// TotalAlloc picks up whatever else the runtime did during the loop. The count is
// checked for constancy too, but as the weaker of the two.
//
// The class is invalid UTF-8 because it is where the two orders separate furthest:
// sanitizing grows each invalid byte into the three-byte U+FFFD.
func TestSanitizeCappedWorkBoundDoesNotScaleWithInput(t *testing.T) {
	const (
		runs      = 5
		limit     = 64
		tolerance = 2.0
	)
	sizes := []int{1024, 32768, 1048576}
	capFirstBytes := make([]float64, 0, len(sizes))
	sanitizeFirstBytes := make([]float64, 0, len(sizes))
	capFirstAllocs := make([]float64, 0, len(sizes))
	for _, n := range sizes {
		in := invalidUTF8(n)
		capFirst := func() {
			sinkString, sinkBool = textsafe.SanitizeSingleLineCapped(in, limit, "...")
		}
		capFirstBytes = append(capFirstBytes, allocBytesPerRun(runs, capFirst))
		capFirstAllocs = append(capFirstAllocs, testing.AllocsPerRun(runs, capFirst))
		sanitizeFirstBytes = append(sanitizeFirstBytes, allocBytesPerRun(runs, func() {
			sinkString, sinkBool = legacyCapped(in, limit, "...", textsafe.SanitizeSingleLine)
		}))
	}
	for i, got := range capFirstBytes {
		if got > capFirstBytes[0]*tolerance {
			t.Errorf("SanitizeSingleLineCapped(invalid UTF-8, %d bytes, %d, \"...\") "+
				"allocated %.0f bytes per run but %.0f at %d bytes, want no more than %.0fx: "+
				"the cap sits ahead of the sanitizer, so the raw value's size must not "+
				"reach the cost", sizes[i], limit, got, capFirstBytes[0], sizes[0], tolerance)
		}
		if capFirstAllocs[i] != capFirstAllocs[0] {
			t.Errorf("SanitizeSingleLineCapped(invalid UTF-8, %d bytes, %d, \"...\") "+
				"allocated %v times per run but %v at %d bytes, want the same count: a "+
				"bounded chunk cannot need more buffer regrowths than a smaller one",
				sizes[i], limit, capFirstAllocs[i], capFirstAllocs[0], sizes[0])
		}
	}
	t.Logf("cap %d over %v bytes of raw invalid UTF-8: cap-first %.0f bytes/run, "+
		"sanitize-first %.0f — sanitizing first pays for the whole value",
		limit, sizes, capFirstBytes, sanitizeFirstBytes)
}

// TestBudgetSpentAllowanceChargesNothingPerValue gates the aggregate's other bound.
// Once anything has been dropped, or the allowance is spent, Budget.Write appends
// nothing and refuses the write whole, so the sanitizer is never entered for it. That
// is what makes a hostile piece COUNT harmless: 8000 groups instead of 8 must not cost
// 1000x more.
//
// Two things about the shape of this test were found by breaking the guard and watching
// it stay green. It writes every value unconditionally instead of going through
// writeGroups, because that loop's early exit would measure itself rather than Write's
// guard — the guard exists for the caller that does NOT stop. And the values are 1 KiB
// each rather than 20 bytes: sanitizing a 20-byte value produces a buffer the compiler
// can prove does not escape, so it lands on the stack and a refused write doing full
// work measures as free.
//
// With the guard intact: a constant 3 allocations for a 32-byte budget whether 8 or
// 8000 values are written. With the sanitize call moved above the guard — an edit no
// functional test would notice, since the OUTPUT is identical — it becomes 10, 82, 802,
// 8002: one allocation per value.
func TestBudgetSpentAllowanceChargesNothingPerValue(t *testing.T) {
	const runs = 20
	writeCounts := []int{8, 80, 800, 8000}
	counts := make([]float64, 0, len(writeCounts))
	value := repeatTo("Group\x1bName\u202eSuffix ", 1024)
	for _, writes := range writeCounts {
		counts = append(counts, testing.AllocsPerRun(runs, func() {
			b := textsafe.NewSingleLineBudget(32, "...")
			for range writes {
				sinkBool = b.Write(value) // deliberately ignoring the refusal
			}
			sinkString, sinkBool = b.Result()
		}))
	}
	for i, got := range counts {
		if got != counts[0] {
			t.Errorf("Budget(32, \"...\") over %d values of 1 KiB each allocated %v times per run "+
				"but %v over %d values, want the same count: a spent allowance must refuse "+
				"a value without sanitizing it, so the piece count cannot amplify the cost",
				writeCounts[i], got, counts[0], writeCounts[0])
		}
	}
	t.Logf("a spent 32-byte budget is a constant %v allocations from %d to %d written values",
		counts[0], writeCounts[0], writeCounts[len(writeCounts)-1])
}

// TestBudgetCostIsSetByTheBudgetNotTheValues is the aggregate twin of the work-bound
// test above: eight writes into one budget, with each value three orders of magnitude
// larger, must cost the same. Every Write caps its argument to the REMAINING budget
// before mapping it, so what the sanitizer sees is bounded by the budget and not by the
// values.
//
// Bytes again rather than counts, for the reason on allocBytesPerRun: with Write's
// pre-cap deleted the allocation count stayed constant for both a clean fixture and one
// the sanitizer merely rewrites. Only the byte figure moves, and by four orders of
// magnitude.
//
// With a 200-byte budget over eight invalid-UTF-8 values: 1,040 bytes per call whether
// each value is 1 KiB or 1 MiB, because the first write fills the budget and the other
// seven are refused outright. The same call over eight 16-byte values that all fit costs
// 552 bytes in 12 allocations — fewer bytes but four times as many, since the aggregate
// is grown across all eight writes.
func TestBudgetCostIsSetByTheBudgetNotTheValues(t *testing.T) {
	const (
		runs      = 10
		limit     = 200
		tolerance = 2.0
	)
	// Eight invalid-UTF-8 values per case; see above for why the class matters.
	values := func(each int) []string {
		out := make([]string, 8)
		for i := range out {
			out[i] = invalidUTF8(each)
		}
		return out
	}
	sizes := []int{1024, 32768, 1048576}
	bytesPerRun := make([]float64, 0, len(sizes))
	for _, each := range sizes {
		groups := values(each)
		bytesPerRun = append(bytesPerRun, allocBytesPerRun(runs, func() {
			sinkString, sinkBool = writeGroups(textsafe.NewSingleLineBudget(limit, "..."), groups)
		}))
	}
	for i, got := range bytesPerRun {
		if got > bytesPerRun[0]*tolerance {
			t.Errorf("Budget(%d, \"...\") over 8 values of %d bytes each allocated %.0f bytes "+
				"per run but %.0f at %d bytes each, want no more than %.0fx: each write caps "+
				"to the remaining budget before sanitizing, so value size must not reach the "+
				"cost", limit, sizes[i], got, bytesPerRun[0], sizes[0], tolerance)
		}
	}
	fitting := values(16)
	t.Logf("a %d-byte budget over 8 values allocates %.0f bytes/run at %v bytes each, "+
		"against %.0f when all eight fit at 16 bytes each",
		limit, bytesPerRun, sizes,
		allocBytesPerRun(runs, func() {
			sinkString, sinkBool = writeGroups(textsafe.NewSingleLineBudget(limit, "..."), fitting)
		}))
}

// BenchmarkWorkBound makes the pre-cap order's reason for existing visible: the same
// cap, marker and input in the two orders. sanitize_first sanitizes the whole value
// then bounds it (legacyCapped, the order the package no longer offers); cap_first is
// SanitizeSingleLineCapped, which bounds the raw bytes then sanitizes the chunk.
//
// Read it as two slopes, not four numbers. cap_first should be FLAT across the two
// sizes — it never walks past the cap — while sanitize_first scales with the input,
// starkest in the B/op series. A change moving the cap back behind the sanitizer would
// put cap_first on the sanitize_first slope.
//
// invalid UTF-8 is the worst case for sanitizing first (three output bytes per input
// byte). No SetBytes: cap_first deliberately does not process the bytes it is handed.
func BenchmarkWorkBound(b *testing.B) {
	const (
		limit  = 64
		marker = "..."
	)
	for _, order := range []struct {
		name string
		call func(string) (string, bool)
	}{
		{"sanitize_first", func(s string) (string, bool) {
			return legacyCapped(s, limit, marker, textsafe.SanitizeSingleLine)
		}},
		{"cap_first", func(s string) (string, bool) {
			return textsafe.SanitizeSingleLineCapped(s, limit, marker)
		}},
	} {
		b.Run(order.name, func(b *testing.B) {
			for _, n := range []int{typicalBytes, 1048576} {
				in := invalidUTF8(n)
				b.Run(fmt.Sprintf("bytes_%d", n), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						sinkString, sinkBool = order.call(in)
					}
				})
			}
		})
	}
}

// BenchmarkAggregate puts the streaming Budget beside the one-shot call it replaces —
// join the untrusted values, then sanitize and cap the result.
//
// Streaming wins here by three orders of magnitude (1.5 us against 2.5 ms, 624 bytes
// against 533 KB) because join_then_capped materializes the whole untrusted aggregate
// before any bound applies. Read the two series together: streaming's numbers do not
// move when the values grow, join_then_capped's track whatever the upstream sent.
//
// The fixture sits at the hostile end deliberately; the header comment has the other
// end, where streaming is the dearer shape.
func BenchmarkAggregate(b *testing.B) {
	const (
		limit  = 200
		marker = "..."
	)
	groups := benchGroups(largeBytes)
	joined := strings.Join(groups, ", ")
	b.Run("streaming", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkString, sinkBool = writeGroups(textsafe.NewSingleLineBudget(limit, marker), groups)
		}
	})
	b.Run("join_then_capped", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkString, sinkBool = textsafe.SanitizeSingleLineCapped(joined, limit, marker)
		}
	})
}
