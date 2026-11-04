package keyenc

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// This file exists because keyenc SELLS two cost claims and, until it landed,
// nothing in the repo measured either. The README promises that a component
// carrying neither reserved character is emitted verbatim - the property that
// makes adoption at an existing key free, with no re-key and no cache
// invalidation - and it promises that a caller folding unbounded upstream data
// into a key cannot be made to allocate without limit, because above
// MaxComponentBytes the key collapses to a fixed 71 bytes. Both are statements
// about cost, and this package sits on cache, dedupe, singleflight and map-key
// paths where per-call cost multiplies by request rate.
//
// Two kinds of check here, doing different jobs:
//
//   - The Test* allocation contracts GATE a claim at merge time. AllocsPerRun
//     is exact, so each assertion is an equality against a number that was
//     MEASURED first, never a threshold picked to pass. The ones that matter
//     are the independence claims: an attacker chooses the size of a field's
//     content, so an allocation count that tracks that size is an
//     amplification vector inside the guard that exists to bound it.
//   - The Benchmark* series feed the weekly tracker. They parameterise
//     component COUNT and component SIZE independently, because both scale
//     cost and one mixed series cannot say which of them regressed.
//
// # The threshold, and why the size series has a step in it
//
// Read off Join, and confirmed by measurement (the fixtures below assert their
// own regime before timing anything, so a fixture that drifts to the wrong
// side of the bound fails rather than quietly charting the other regime):
//
//	total := sum of len(part) over the RAW components
//	total <= MaxComponentBytes (8192)  ->  escaped join; output grows with input
//	total >  MaxComponentBytes (8192)  ->  "sha256:" + 64 hex; output always 71 bytes
//
// So 8192 raw bytes is the largest input that still escapes, and 8193 is the
// first that hashes. The bound is measured on the RAW components and never on
// the escaped output, so an all-reserved-bytes set of exactly 8192 raw bytes
// still takes the escaped path - into 16387 bytes, twice the bound.
//
// A threshold makes the cost curve legitimately discontinuous, which is the one
// shape a size-parameterised benchmark reads wrong by default. Every
// sub-benchmark name therefore carries its regime (joined_* below or at the
// bound, hashed_* above it), and the step between the last joined_* case and
// the first hashed_* case is the threshold, not a regression. Within one regime
// the trend should be linear in size; a super-linear jump there is the finding.
//
// The step does not even have a fixed direction, which is the reason for saying
// all this rather than trusting a reader to infer it. Measured, crossing the
// bound makes Join SLOWER on verbatim components - hashing 8 KiB costs more than
// copying it - and several times FASTER on all-reserved components, where the
// escaped regime has to produce twice its input and the hashed regime does not
// look at the bytes at all. BenchmarkJoinTotalSize charts the first direction
// and BenchmarkJoinAllReservedBytes the second.

// benchPlain returns n bytes containing neither reserved character, so escaping
// is a no-op and Join emits them verbatim. This is the shape almost every real
// call site has, and the one the README's byte-identical-to-a-naive-
// concatenation claim covers.
func benchPlain(n int) string { return strings.Repeat("x", n) }

// benchReserved returns n bytes that are ALL reserved, alternating Separator
// and Escape, so every single byte expands to two. This is the maximum
// expansion the grammar permits and it is attacker-chosen: a field whose
// content comes from upstream text can be made to look exactly like this, so it
// is where an amplification regression appears first.
func benchReserved(n int) string {
	b := make([]byte, n)
	for i := range b {
		if i%2 == 0 {
			b[i] = Separator
		} else {
			b[i] = Escape
		}
	}
	return string(b)
}

// benchParts builds count components of size bytes each, so a benchmark can
// vary the two axes independently. The total is count*size, which is the
// quantity Join compares against MaxComponentBytes.
func benchParts(count, size int, fill func(int) string) []string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fill(size)
	}
	return parts
}

// TestJoinAllocationCountIsIndependentOfComponentSize is the amplification
// contract, and the most valuable assertion in the file. A caller's field
// content is the part an attacker controls, so Join's allocation count must be
// a function of how many components it was handed and not of how many bytes
// they carry. All three regimes are checked, because an attacker picks which
// one runs: the verbatim path, the all-reserved path where every byte expands,
// and the hashed path where the input is already oversized and the only bound
// left is that refusal cost does not scale with the payload.
//
// The sizes span 32x to 128x within each regime, which is enough that a
// per-byte allocation cannot hide in the noise: a count that moves with them is
// how a bounded key builder becomes an unbounded one.
func TestJoinAllocationCountIsIndependentOfComponentSize(t *testing.T) {
	regimes := []struct {
		name       string
		fill       func(int) string
		sizes      []int
		wantHashed bool
	}{
		// 4 components, so the totals are 64, 4096 and 8192 raw bytes: the
		// largest still sits exactly at the bound.
		{"verbatim components", benchPlain, []int{16, 1024, 2048}, false},
		// Every byte expanding, so the escaped output of the last case is 16387
		// bytes, twice the bound, and still not hashed. The range starts at 64
		// rather than 16 on purpose: an escaped component of 32 bytes or fewer
		// has its conversion buffer kept off the heap by the compiler, so a
		// 16-byte case measures 6 where every larger one measures 10. That step
		// is a toolchain decision about tiny strings, not the library's
		// allocation count tracking size, and including it here would compare
		// two different compiler outcomes instead of two sizes.
		{"all reserved bytes", benchReserved, []int{64, 512, 2048}, false},
		// Oversized: 8196 bytes to 1 MiB, all reduced to the same 71 bytes.
		{"hashed identity", benchPlain, []int{2049, 16384, 262144}, true},
	}
	for _, r := range regimes {
		t.Run(r.name, func(t *testing.T) {
			counts := make([]float64, len(r.sizes))
			for i, size := range r.sizes {
				parts := benchParts(4, size, r.fill)
				if got := IsHashed(Join(parts...)); got != r.wantHashed {
					t.Fatalf("Join(4 components of %d bytes, %d raw total): IsHashed = %v, want %v; the fixture drifted out of the %s regime",
						size, 4*size, got, r.wantHashed, r.name)
				}
				counts[i] = testing.AllocsPerRun(50, func() {
					_ = Join(parts...)
				})
			}
			for i, got := range counts {
				if got != counts[0] {
					t.Errorf("Join(4 components of %d bytes) allocated %v times per run, want %v (its count at %d bytes each): the allocation count must not track a field's SIZE, or a caller who is fed more bytes pays more allocations",
						r.sizes[i], got, counts[0], r.sizes[0])
				}
			}
			t.Logf("%s: a constant %v allocations from %d to %d bytes per component",
				r.name, counts[0], r.sizes[0], r.sizes[len(r.sizes)-1])
		})
	}
}

// TestJoinAllocationCountPerComponent pins the other axis, and it is the one
// where the obvious claim does NOT hold, so the two halves are asserted
// separately rather than folded into one comfortable number.
//
// A per-component allocation in a key builder is a real regression class: it
// turns a five-field key into five allocations on a hot path. On the verbatim
// path Join is genuinely flat - one buffer for the joined output, one for the
// intermediate slice, whatever the component count - and that is asserted as an
// equality. On the escaping path it is not flat and cannot be: each component
// carrying a reserved character has to be materialised in its expanded form
// before the join, so the cost is bounded PER COMPONENT instead. That bound is
// what is asserted, and together with the size-independence contract above it
// says the expansion is charged per component and never per byte.
func TestJoinAllocationCountPerComponent(t *testing.T) {
	t.Run("verbatim components allocate a constant", func(t *testing.T) {
		sizes := []int{8, 32, 128, 512}
		counts := make([]float64, len(sizes))
		for i, count := range sizes {
			parts := benchParts(count, 16, benchPlain)
			if IsHashed(Join(parts...)) {
				t.Fatalf("Join(%d components of 16 bytes) hashed at %d raw bytes; the case is meant to stay below the bound", count, count*16)
			}
			counts[i] = testing.AllocsPerRun(50, func() {
				_ = Join(parts...)
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("Join(%d verbatim components) allocated %v times per run, want %v (its count at %d components): a key builder that allocates per component pays for every field on every call",
					sizes[i], got, counts[0], sizes[0])
			}
		}
		t.Logf("verbatim: a constant %v allocations from %d to %d components", counts[0], sizes[0], sizes[len(sizes)-1])
	})

	// The per-component rate, measured as a slope between two component counts
	// so the join's fixed cost cancels out. The component size is 64 bytes
	// rather than a handful, because an escaped component of 32 bytes or fewer
	// has its conversion buffer kept off the heap and would measure a rate the
	// next caller with a real field would not see.
	t.Run("escaping is charged per component", func(t *testing.T) {
		const (
			low        = 8
			high       = 128
			partSize   = 64
			maxPerPart = 2.0
			runs       = 50
		)
		// Built outside the measured closure: benchParts allocates per
		// component itself, so building it inside would fold the fixture's own
		// per-component cost into the slope and report roughly double.
		lowParts := benchParts(low, partSize, benchReserved)
		highParts := benchParts(high, partSize, benchReserved)
		if IsHashed(Join(highParts...)) {
			t.Fatalf("Join(%d all-reserved components of %d bytes) hashed at %d raw bytes; the case is meant to stay below the bound", high, partSize, high*partSize)
		}
		lowCount := testing.AllocsPerRun(runs, func() {
			_ = Join(lowParts...)
		})
		highCount := testing.AllocsPerRun(runs, func() {
			_ = Join(highParts...)
		})
		rate := (highCount - lowCount) / float64(high-low)
		if rate > maxPerPart {
			t.Errorf("Join(all-reserved components of %d bytes) allocated %v times at %d components and %v at %d, a rate of %v per component, want at most %v: escaping must cost a small constant per component",
				partSize, lowCount, low, highCount, high, rate, maxPerPart)
		}
		t.Logf("all-reserved: %v allocations per additional component (%v at %d components, %v at %d)", rate, lowCount, low, highCount, high)
	})
}

// TestIsHashedAllocations pins that IsHashed, the predicate a call site runs
// before Split on every key it parses back, allocates nothing on any path. The
// digest check is a byte scan rather than a decode, so a key that reaches it is
// as cheap as one the prefix or length check rejects first - the parse path a
// site guarding Split takes is free whatever the key.
func TestIsHashedAllocations(t *testing.T) {
	cases := map[string]string{
		"no prefix":        "streams:u-42:1234:3:5",
		"prefix only":      hashedPrefix,
		"digest too short": hashedPrefix + strings.Repeat("0", hashedHexLen-1),
		"digest too long":  hashedPrefix + strings.Repeat("0", hashedHexLen+1),
		"valid digest":     Join(benchPlain(MaxComponentBytes + 1)),
		"non-hex digest":   hashedPrefix + strings.Repeat("z", hashedHexLen),
		"uppercase digest": hashedPrefix + strings.Repeat("A", hashedHexLen),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(100, func() {
				_ = IsHashed(key)
			}); got != 0 {
				t.Errorf("IsHashed(%.16q, %d bytes) allocated %v times per run, want 0",
					key, len(key), got)
			}
		})
	}
}

// TestSplitRefusesHashedPrefixWithoutAllocating pins Split's refusal of a
// "sha256:"-prefixed key. Join emits no raw key with that prefix, so such a key
// is either a hashed identity (a valid lowercase digest, ErrHashed) or a
// near-miss that never came from Join (ErrMalformed). Either refusal is
// allocation-free: the digest check is a byte scan over at most hashedHexLen
// characters, and a wrong-length digest is rejected in O(1) before the scan, so
// an attacker cannot make a refusal expensive by sending a longer key.
//
// A key refused for a dangling escape is a different path and gets no such
// assertion: Split is one forward pass, so that malformation is only found
// after the whole key is decoded. BenchmarkSplitRefusal charts both.
func TestSplitRefusesHashedPrefixWithoutAllocating(t *testing.T) {
	cases := map[string]struct {
		key     string
		wantErr error
	}{
		"hashed identity":        {Join(benchPlain(MaxComponentBytes + 1)), ErrHashed},
		"long non-digest prefix": {hashedPrefix + strings.Repeat("0", 100<<10), ErrMalformed},
		"uppercase digest":       {hashedPrefix + strings.Repeat("A", hashedHexLen), ErrMalformed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Split(c.key); !errors.Is(err, c.wantErr) {
				t.Fatalf("Split(%.16q..., %d bytes) error = %v, want %v", c.key, len(c.key), err, c.wantErr)
			}
			if got := testing.AllocsPerRun(100, func() {
				_, _ = Split(c.key)
			}); got != 0 {
				t.Errorf("Split(%.16q..., %d bytes) allocated %v times per run, want 0: a refusal must stay allocation-free whatever the key length",
					c.key, len(c.key), got)
			}
		})
	}
}

// TestSplitAllocationCountTracksWhatItRecovers pins the decoding half's
// amplification property. Escaping is the one place a key's length is not the
// caller's choice: a component full of reserved characters doubles, so the key
// Split is handed can be twice the size of the components inside it. The
// allocation count must follow the components it recovers rather than the
// escaped length, or an attacker doubles Split's allocations by choosing field
// content that escapes.
//
// It holds for the COUNT and not for the bytes: Split sizes its result slice
// from strings.Count of the raw separator, which counts escaped separators too,
// so the escape-dense key over-reserves that slice by the number of escaped
// separators. BenchmarkSplit charts it as B/op, which the tracker alerts on
// independently of ns/op.
func TestSplitAllocationCountTracksWhatItRecovers(t *testing.T) {
	const (
		components = 4
		perPart    = 512
	)
	keys := []struct {
		name string
		key  string
	}{
		{"verbatim", Join(benchParts(components, perPart, benchPlain)...)},
		{"every byte escaped", Join(benchParts(components, perPart, benchReserved)...)},
	}
	counts := make([]float64, len(keys))
	for i, k := range keys {
		parts, err := Split(k.key)
		if err != nil {
			t.Fatalf("Split(<%s key>, %d bytes): %v", k.name, len(k.key), err)
		}
		if len(parts) != components {
			t.Fatalf("Split(<%s key>, %d bytes) recovered %d components, want %d", k.name, len(k.key), len(parts), components)
		}
		if len(parts[0]) != perPart {
			t.Fatalf("Split(<%s key>, %d bytes) recovered a first component of %d bytes, want %d", k.name, len(k.key), len(parts[0]), perPart)
		}
		counts[i] = testing.AllocsPerRun(50, func() {
			_, _ = Split(k.key)
		})
	}
	if counts[0] != counts[1] {
		t.Errorf("Split allocated %v times per run on the %s key (%d bytes) and %v on the %s key (%d bytes), want the same count: both recover %d components of %d bytes, so the extra allocations are being charged for the escaping rather than for what was recovered",
			counts[0], keys[0].name, len(keys[0].key), counts[1], keys[1].name, len(keys[1].key), components, perPart)
	}
	t.Logf("recovering %d components of %d bytes costs %v allocations from a %d-byte key and %v from the %d-byte escaped key",
		components, perPart, counts[0], len(keys[0].key), counts[1], len(keys[1].key))

	// BYTES are the axis that carried the defect, and the count above could not
	// see it. Split used to size its result slice with strings.Count over the
	// whole key, which counts an escaped `\:` as a boundary, so an
	// escape-dense key reserved one slot per escaped separator while recovering
	// four components: 22496 B/op against 4128 for the verbatim key, 5.4x, with
	// the allocation count identical at 29 either way. The separators are bytes
	// the caller's data controls, so that was attacker-influenced memory
	// amplification with no signal in any metric the weekly tracker charts.
	//
	// Both keys now recover the same four components for the same bytes, and
	// this is what holds it: a hint that counts escaped separators again shows
	// up here as a multiple, whatever the count does.
	byteCounts := make([]uint64, len(keys))
	for i, k := range keys {
		byteCounts[i] = allocBytesPerRun(50, func() {
			_, _ = Split(k.key)
		})
	}
	if byteCounts[1] > byteCounts[0]+byteCounts[0]/4 {
		t.Errorf("Split allocated %d bytes per run on the %s key and %d on the %s key, want the second within 25%% of the first: both recover %d components of %d bytes, so a wider gap means the result slice is sized from escaped separators rather than from real boundaries",
			byteCounts[0], keys[0].name, byteCounts[1], keys[1].name, components, perPart)
	}
	t.Logf("recovering them costs %d bytes from the verbatim key and %d from the escaped key",
		byteCounts[0], byteCounts[1])
}

// allocBytesPerRun is the bytes twin of [testing.AllocsPerRun]. It exists
// because a capacity-hint defect moves BYTES while leaving the allocation count
// untouched, so a contract written only on the count cannot see the whole class.
// Same shape as AllocsPerRun: run once to settle any lazy initialisation, then
// measure the delta over n runs with the GC held still.
func allocBytesPerRun(n int, f func()) uint64 {
	f()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// BenchmarkJoinComponentCount varies how many components are joined at a fixed
// 16 bytes each, which is the axis a real call site grows along: a key gains a
// field. Every case stays well below the bound (128 components is 2048 raw
// bytes), so nothing here hashes and the trend should be linear in count. A
// super-linear jump between the last two cases is the finding - it means the
// join stopped being one pass, and a quadratic key builder is invisible at the
// three-field sizes a unit test uses.
//
// No b.SetBytes: the axis is component count and the fixture's byte size is a
// side effect of it, so a byte rate would only restate ns/op with more noise.
func BenchmarkJoinComponentCount(b *testing.B) {
	for _, count := range []int{8, 32, 128} {
		parts := benchParts(count, 16, benchPlain)
		want := Join(parts...)
		if IsHashed(want) {
			b.Fatalf("Join(%d components of 16 bytes) hashed at %d raw bytes; this series must stay below the bound", count, count*16)
		}
		b.Run(fmt.Sprintf("parts_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			var key string
			for b.Loop() {
				key = Join(parts...)
			}
			if key != want {
				b.Fatalf("Join(%d components of 16 bytes) = %d bytes, want %d", count, len(key), len(want))
			}
		})
	}
}

// BenchmarkJoinTotalSize is the series that straddles the hash threshold. It
// holds the component count at 4 and grows each component, so the only thing
// changing is the total raw size the bound is measured on.
//
// Read the step, do not alert on it. joined_8192B_at_bound is the largest input
// that still escapes; hashed_8196B is four bytes of input later and returns 71
// bytes for any input at all. 8193 is the true first hashed total, and 8196 is
// the nearest a four-equal-component fixture can reach, which is why the name
// carries the number rather than the word "boundary". On verbatim components the
// step is upward - hashing 8 KiB costs more than copying it - so this is the
// series where the discontinuity is easiest to mistake for a regression.
func BenchmarkJoinTotalSize(b *testing.B) {
	cases := []struct {
		name       string
		size       int // bytes per component; 4 components, so the total is 4x
		wantHashed bool
	}{
		{"joined_64B", 16, false},              // well below the bound
		{"joined_8188B", 2047, false},          // just below: four raw bytes short
		{"joined_8192B_at_bound", 2048, false}, // exactly at: the largest escaped input
		{"hashed_8196B", 2049, true},           // just above: the first hashed total for this shape
		{"hashed_65536B", 16384, true},         // well above: 8x the bound, same 71-byte output
	}
	for _, tc := range cases {
		parts := benchParts(4, tc.size, benchPlain)
		want := Join(parts...)
		if got := IsHashed(want); got != tc.wantHashed {
			b.Fatalf("%s: Join(4 components of %d bytes, %d raw total): IsHashed = %v, want %v",
				tc.name, tc.size, 4*tc.size, got, tc.wantHashed)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(4 * tc.size))
			var key string
			for b.Loop() {
				key = Join(parts...)
			}
			if key != want {
				b.Fatalf("%s: Join returned %d bytes, want %d", tc.name, len(key), len(want))
			}
		})
	}
}

// BenchmarkJoinAllReservedBytes is the escaping worst case: every byte of every
// component is a reserved character, so every byte expands to two and the
// escaped output is exactly twice the input plus the joining separators. This is
// the attacker-chosen input - a field widened from an enum to free-form
// upstream text can be made to look like this - and it is where an
// amplification regression shows up, because a change that starts copying the
// expanded form twice doubles cost only on inputs that expand.
//
// The setup asserts the expansion rather than assuming it, so the series cannot
// silently become a plain-bytes benchmark if the escaper changes.
//
// The step here inverts the intuition worth naming: at the bound this is the
// most expensive input the escaped regime accepts, and four bytes of input later
// the hashed regime does not escape at all, so the cost DROPS by several times.
// Compare hashed_8196B against BenchmarkJoinTotalSize's identically-sized case:
// the two should track each other, because hashing never looks at which bytes it
// was given. A persistent gap between them, rather than one noisy run, would
// mean the escaper is somehow still on the path above the bound.
func BenchmarkJoinAllReservedBytes(b *testing.B) {
	cases := []struct {
		name       string
		size       int
		wantHashed bool
	}{
		{"joined_4096B", 1024, false},          // below the bound; escapes to 8195 bytes
		{"joined_8192B_at_bound", 2048, false}, // at the bound; escapes to 16387 bytes, twice it
		{"hashed_8196B", 2049, true},           // over: no escaping happens at all
	}
	for _, tc := range cases {
		parts := benchParts(4, tc.size, benchReserved)
		want := Join(parts...)
		if got := IsHashed(want); got != tc.wantHashed {
			b.Fatalf("%s: Join(4 all-reserved components of %d bytes, %d raw total): IsHashed = %v, want %v",
				tc.name, tc.size, 4*tc.size, got, tc.wantHashed)
		}
		if !tc.wantHashed {
			// 2 bytes out per byte in, plus the three joining separators.
			if wantLen := 2*4*tc.size + 3; len(want) != wantLen {
				b.Fatalf("%s: Join(4 all-reserved components of %d bytes) = %d bytes, want %d; the fixture is meant to be the maximum-expansion input",
					tc.name, tc.size, len(want), wantLen)
			}
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(4 * tc.size))
			var key string
			for b.Loop() {
				key = Join(parts...)
			}
			if key != want {
				b.Fatalf("%s: Join returned %d bytes, want %d", tc.name, len(key), len(want))
			}
		})
	}
}

// BenchmarkSplit charts the decoding half on the pair that isolates unescaping
// from scanning. Both keys recover the same thing - four components of 512
// bytes - so ns/op is directly comparable, and the escaped one is twice as long
// because that is what escaping every byte costs. The names carry the RECOVERED
// size for that reason; b.SetBytes carries the key length, which is what Split
// actually walks.
//
// What this catches: unescaping that stops being one pass, and the result-slice
// sizing losing its hint. Watch B/op on escaped_2048B in particular - the hint
// is computed from the raw separator count, so it over-reserves in proportion
// to the escaped separators an attacker put in the components, and that is the
// one cost here that is not bounded by what the key recovers.
func BenchmarkSplit(b *testing.B) {
	cases := []struct {
		name string
		key  string
	}{
		{"verbatim_2048B", Join(benchParts(4, 512, benchPlain)...)},
		{"escaped_2048B", Join(benchParts(4, 512, benchReserved)...)},
	}
	for _, tc := range cases {
		want, err := Split(tc.key)
		if err != nil {
			b.Fatalf("%s: Split(%d-byte key): %v", tc.name, len(tc.key), err)
		}
		if len(want) != 4 {
			b.Fatalf("%s: Split(%d-byte key) recovered %d components, want 4", tc.name, len(tc.key), len(want))
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.key)))
			var parts []string
			for b.Loop() {
				parts, _ = Split(tc.key)
			}
			if len(parts) != len(want) {
				b.Fatalf("%s: Split recovered %d components, want %d", tc.name, len(parts), len(want))
			}
		})
	}
}

// BenchmarkSplitRefusal charts what a key that will never parse costs, which is
// the number that matters when the keys arrive from a persisted file, a URL
// segment or client storage. A "sha256:"-prefixed refusal is decided by a byte
// scan over the digest (at most hashedHexLen characters) and allocates nothing,
// and a wrong-length digest is rejected in O(1) before the scan, so this cost
// does not grow with the key length (allocation-free refusal pinned by
// TestSplitRefusesHashedPrefixWithoutAllocating).
//
// The other refusal is deliberately not a series here. Split is one forward
// pass, so a dangling escape at the end of an otherwise valid key is only
// discovered after the whole key has been decoded, and it measures within a few
// percent of a successful decode of that same key - a second series that would
// track BenchmarkSplit forever without adding a fact. What it costs is
// therefore already charted; that its cost scales with the key while the
// prefixed refusal's does not is the asymmetry to keep in mind when reading
// these two.
func BenchmarkSplitRefusal(b *testing.B) {
	cases := []struct {
		name string
		key  string
		want error
	}{
		{"hashed", Join(benchPlain(MaxComponentBytes + 1)), ErrHashed},
	}
	for _, tc := range cases {
		if _, err := Split(tc.key); !errors.Is(err, tc.want) {
			b.Fatalf("%s: Split(%d-byte key) error = %v, want %v", tc.name, len(tc.key), err, tc.want)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.key)))
			var err error
			for b.Loop() {
				_, err = Split(tc.key)
			}
			if !errors.Is(err, tc.want) {
				b.Fatalf("%s: Split error = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// BenchmarkIsHashed charts the predicate the README tells a parsing call site to
// run on every key, so its cost multiplies by that site's key rate. The two
// cases are the two paths: raw is answered by the prefix test and hashed scans
// the digest, and both allocate nothing (pinned by TestIsHashedAllocations).
//
// Neither case gets b.SetBytes. A hashed key is always 71 bytes and the raw one
// is answered without reading past the prefix, so there is no meaningful byte
// count on either path.
//
// There is deliberately no round-trip (Join then Split) benchmark in this file:
// composing them measures the sum of two series that are already charted
// separately, so a regression in either would alert twice and localise to
// neither.
func BenchmarkIsHashed(b *testing.B) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"hashed", Join(benchPlain(MaxComponentBytes + 1)), true},
		{"raw", Join("streams", "u-42", "1234", "3", "5"), false},
	}
	for _, tc := range cases {
		if got := IsHashed(tc.key); got != tc.want {
			b.Fatalf("%s: IsHashed(%.16q) = %v, want %v", tc.name, tc.key, got, tc.want)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var got bool
			for b.Loop() {
				got = IsHashed(tc.key)
			}
			if got != tc.want {
				b.Fatalf("%s: IsHashed = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
