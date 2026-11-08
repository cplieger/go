package textsafe_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/go/textsafe"
)

// The central cost property gated here: CLEAN text is sanitized without
// allocating. strings.Map builds no output buffer until the first rune changes and
// returns the ORIGINAL string when none does, and both presets are one Map call, so
// they inherit it. Losing it does not change what they RETURN, so no output-only
// test can see the regression.

// sinkString and sinkBool consume benchmark and AllocsPerRun results so the
// compiler cannot delete the call being measured. Shared by the other benchmark
// files in this package.
var (
	sinkString string
	sinkBool   bool
)

// Fixture sizes. typicalBytes is an upstream title or error message headed for one
// log attribute; the others span it so a size series can expose a super-linear scan.
const (
	typicalBytes = 4096
	smallBytes   = 256
	largeBytes   = 65536
)

// repeatTo builds a deterministic fixture of exactly n bytes by repeating unit and
// padding with spaces. The padding must stay a space: one byte, valid UTF-8, safe
// under both policies, so it can neither split a multi-byte rune at the tail nor
// change the fixture's class.
func repeatTo(unit string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n)
	for b.Len()+len(unit) <= n {
		b.WriteString(unit)
	}
	for b.Len() < n {
		b.WriteByte(' ')
	}
	return b.String()
}

// cleanASCII is the fast path: an attribute carrying nothing the policy touches.
func cleanASCII(n int) string {
	return repeatTo("Release.Name.S01E02.1080p.WEB-DL.x264-GROUP ", n)
}

// cleanUTF8 is the fast path over multi-byte runes. A separate class because
// strings.Map's no-change check runs per RUNE while the scan advances by bytes, so a
// regression on multi-byte input can be invisible on ASCII.
func cleanUTF8(n int) string {
	return repeatTo("Ясность — 日本語のタイトル — émission ", n)
}

// unsafeTail is the worst case for the fast path's bail-out: strings.Map scans every
// rune before finding the one that changes, then still copies the whole prefix.
func unsafeTail(n int) string {
	if n <= 1 {
		return "\x1b"
	}
	return cleanASCII(n-1) + "\x1b"
}

// unsafeDense is text the sanitizer must rewrite throughout: a C0, a C1, a bidi
// override and a line separator per unit. Every replaced rune here is multi-byte
// except the C0, so the rewritten form is SHORTER than the input and this class
// never grows the output buffer.
func unsafeDense(n int) string {
	return repeatTo("a\x1bb\u009cc\u202ed\u2028e ", n)
}

// invalidUTF8 is the one class whose sanitized form GROWS: each invalid byte becomes
// the three-byte U+FFFD, so a dense fixture expands roughly 3x and the output buffer
// has to be regrown.
func invalidUTF8(n int) string {
	return repeatTo("head\xff\xfe\x80tail ", n)
}

// TestSanitizeCleanTextIsAllocationFree gates the central cost claim: a value
// carrying no unsafe rune is returned as-is, not copied. Both presets are checked at
// both ends of the size range, because a copy introduced by a length check would be
// unmistakable only as an exact zero.
//
// The table holds values clean under BOTH policies so it can drive both loops. CR/LF
// gets its own case because it is where the policies diverge: newline-bearing text is
// clean under the multi-line policy and a rewrite under the strict one.
func TestSanitizeCleanTextIsAllocationFree(t *testing.T) {
	const runs = 20
	tests := []struct {
		name string
		desc string
		in   string
	}{
		{"ascii_small", "clean ASCII, 64 bytes", cleanASCII(64)},
		{"ascii_large", "clean ASCII, 256 KiB", cleanASCII(262144)},
		{"utf8_multibyte", "clean multi-byte UTF-8, 4 KiB", cleanUTF8(typicalBytes)},
		{"literal_replacement_char", "a literal U+FFFD, already a safe rune", "title \uFFFD tail"},
		{"empty", "the empty string", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, func() {
				sinkString = textsafe.Sanitize(tc.in)
			}); got != 0 {
				t.Errorf("Sanitize(%s) allocated %v times per run, want 0: strings.Map "+
					"returns the original string when the mapping changes no rune, so "+
					"sanitizing an honest value must cost nothing", tc.desc, got)
			}
		})
	}
	// The strict policy is gated rather than assumed: it is a second strings.Map
	// call, and a copy could be added to one preset and not the other.
	for _, tc := range tests {
		t.Run("single_line_"+tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, func() {
				sinkString = textsafe.SanitizeSingleLine(tc.in)
			}); got != 0 {
				t.Errorf("SanitizeSingleLine(%s) allocated %v times per run, want 0: the "+
					"strict preset is one strings.Map call too, so it shares the "+
					"no-change fast path", tc.desc, got)
			}
		})
	}
	t.Run("crlf_kept_by_multiline_policy", func(t *testing.T) {
		const in = "line one\r\nline two\r\n"
		if got := testing.AllocsPerRun(runs, func() {
			sinkString = textsafe.Sanitize(in)
		}); got != 0 {
			t.Errorf("Sanitize(%q) allocated %v times per run, want 0: CR and LF are safe "+
				"under the multi-line policy, so a newline-bearing value is a clean value "+
				"here and must not be copied", in, got)
		}
	})
}

// TestSanitizeIsIdempotentForFree pins the cost half of the README's claim that
// double-sanitizing at two layers is harmless: a sanitized string is by construction
// a clean one, so the second pass takes the no-change fast path. Without this, the
// advice to double-sanitize would be advice to double-allocate.
func TestSanitizeIsIdempotentForFree(t *testing.T) {
	const runs = 20
	for _, tc := range []struct {
		name string
		desc string
		in   string
	}{
		{"rewritten_unsafe", "rewritten dense unsafe text, 4 KiB", unsafeDense(typicalBytes)},
		{"repaired_invalid_utf8", "repaired invalid UTF-8, 4 KiB", invalidUTF8(typicalBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			once := textsafe.Sanitize(tc.in)
			if got := testing.AllocsPerRun(runs, func() {
				sinkString = textsafe.Sanitize(once)
			}); got != 0 {
				t.Errorf("Sanitize(Sanitize(%s)) allocated %v times per run on the second "+
					"pass, want 0: the first pass leaves no rune the policy touches, so "+
					"double-sanitizing at two layers must cost nothing", tc.desc, got)
			}
		})
	}
}

// TestSanitizeRewriteCostDoesNotScaleWithInput covers the path that legitimately
// allocates. The property is that cost is PROPORTIONATE to the input and cannot be
// amplified: 1024x more hostile text must not cost 1024x more ALLOCATIONS.
//
// strings.Map grows its buffer once to len(s)+utf8.UTFMax, and every rune this
// policy replaces becomes a single-byte space, so the rewritten form can only be
// shorter and one buffer is always enough.
func TestSanitizeRewriteCostDoesNotScaleWithInput(t *testing.T) {
	const runs = 10
	sizes := []int{256, 4096, 65536, 262144}
	counts := make([]float64, 0, len(sizes))
	for _, n := range sizes {
		in := unsafeDense(n)
		counts = append(counts, testing.AllocsPerRun(runs, func() {
			sinkString = textsafe.Sanitize(in)
		}))
	}
	for i, got := range counts {
		if got != 1 {
			t.Errorf("Sanitize(dense unsafe text, %d bytes) allocated %v times per run, "+
				"want 1: the rewritten form is never longer than the input, so one "+
				"buffer must suffice", sizes[i], got)
		}
		if got != counts[0] {
			t.Errorf("Sanitize(dense unsafe text, %d bytes) allocated %v times per run "+
				"but %v at %d bytes, want the same count: sanitizing cost must not be "+
				"amplifiable by sending more hostile text", sizes[i], got, counts[0], sizes[0])
		}
	}
}

// TestSanitizeInvalidUTF8CostIsBounded covers the one input class whose sanitized
// form GROWS. Each invalid byte becomes the three-byte U+FFFD, so the buffer
// strings.Map sized at len(s)+utf8.UTFMax is genuinely too small and has to be
// regrown; this path cannot be a single allocation, and asserting one would be
// asserting a bug.
//
// The assertion is an upper BOUND, not constancy: the count is strings.Builder's
// amortized growth schedule crossing allocator size classes (observed 2,2,2,2,2,4,3
// over 512 bytes to 1 MiB), which is a dependency's implementation detail and the
// wrong thing to pin. What is worth gating is amortized growth against per-byte
// allocation — a rewrite allocating per invalid byte would report five figures at the
// largest fixture, four orders of magnitude clear of this bound.
func TestSanitizeInvalidUTF8CostIsBounded(t *testing.T) {
	const (
		runs     = 5
		maxAlloc = 8
	)
	sizes := []int{1024, 32768, 1048576}
	counts := make([]float64, 0, len(sizes))
	for _, n := range sizes {
		in := invalidUTF8(n)
		got := testing.AllocsPerRun(runs, func() {
			sinkString = textsafe.Sanitize(in)
		})
		counts = append(counts, got)
		if got > maxAlloc {
			t.Errorf("Sanitize(invalid UTF-8, %d bytes) allocated %v times per run, want at "+
				"most %d: repairing invalid bytes may regrow the output buffer a few times, "+
				"but the cost must be the buffer's amortized growth and not one allocation "+
				"per invalid byte", n, got, maxAlloc)
		}
	}
	t.Logf("repairing invalid UTF-8 costs %v allocations at %v bytes: amortized growth, "+
		"not per-byte", counts, sizes)
}

// TestCapBytesNeverAllocates pins CapBytes as a pure slicing operation: it returns a
// prefix of its argument, so it must never copy.
//
// The boundary cases are the point. A cut landing mid-rune walks backwards to the
// nearest rune start, and a plausible rewrite of that walk — re-encoding the prefix,
// or assembling the result through a Builder — would allocate while returning the
// same bytes.
func TestCapBytesNeverAllocates(t *testing.T) {
	const runs = 50
	ascii := cleanASCII(smallBytes)
	multi := cleanUTF8(smallBytes)
	for _, tc := range []struct {
		name string
		desc string
		in   string
		n    int
	}{
		{"ascii_under_cap", "ASCII, cap below the length", ascii, smallBytes / 2},
		{"ascii_at_cap", "ASCII, cap exactly the length", ascii, smallBytes},
		{"ascii_over_cap", "ASCII, cap above the length", ascii, smallBytes + 7},
		{"multibyte_backoff", "multi-byte, cut needing a rune-start backoff", multi, smallBytes / 2},
		{"multibyte_over_cap", "multi-byte, cap above the length", multi, smallBytes + 7},
		{"non_positive_cap", "ASCII, non-positive cap", ascii, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, func() {
				sinkString = textsafe.CapBytes(tc.in, tc.n)
			}); got != 0 {
				t.Errorf("CapBytes(%s, %d) allocated %v times per run, want 0: the result "+
					"is a prefix of the input, so capping must never copy", tc.desc, tc.n, got)
			}
		})
	}
}

// TestSanitizeSingleLineBoundedWithinCapIsAllocationFree gates the cost half of this
// preset's documented contract: a result within the cap is returned untouched. For a
// value both clean and within the cap the preset is cost-identical to doing nothing,
// which is what an added marker check or a defensive copy would silently take away.
func TestSanitizeSingleLineBoundedWithinCapIsAllocationFree(t *testing.T) {
	const runs = 50
	in := cleanASCII(smallBytes)
	for _, tc := range []struct {
		name string
		desc string
		in   string
		n    int
	}{
		{"under_cap", "clean value under the cap", in, smallBytes * 2},
		{"at_cap", "clean value exactly at the cap", in, smallBytes},
		{"empty_negative_cap", "empty input under a negative cap", "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, func() {
				sinkString = textsafe.SanitizeSingleLineBounded(tc.in, tc.n)
			}); got != 0 {
				t.Errorf("SanitizeSingleLineBounded(%s, %d) allocated %v times per run, "+
					"want 0: a clean within-cap value is returned as-is, so the bounded "+
					"preset must cost nothing on the common log-attribute case",
					tc.desc, tc.n, got)
			}
		})
	}
}

// BenchmarkSanitize is the primary trend series: one sanitizer call over the input
// classes a real emit boundary meets. clean_ascii carries the full size sweep so a
// fixed per-call overhead (worst at 256 bytes) and a super-linear scan (a jump
// between 4 KiB and 64 KiB) both show; unsafe_dense carries the upper two so the
// rewrite path's slope reads against the fast path's. The other three are measured
// at one size, for their RATIO to clean_ascii. SetBytes is set because these calls
// scan every byte; the shared benchmark reducer drops MB/s from the published
// series (smaller is worse there), so it serves the local reader only.
func BenchmarkSanitize(b *testing.B) {
	for _, class := range []struct {
		name  string
		build func(int) string
		sizes []int
	}{
		{"clean_ascii", cleanASCII, []int{smallBytes, typicalBytes, largeBytes}},
		{"unsafe_dense", unsafeDense, []int{typicalBytes, largeBytes}},
		{"unsafe_tail", unsafeTail, []int{typicalBytes}},
		{"clean_utf8", cleanUTF8, []int{typicalBytes}},
		{"invalid_utf8", invalidUTF8, []int{typicalBytes}},
	} {
		b.Run(class.name, func(b *testing.B) {
			for _, n := range class.sizes {
				in := class.build(n)
				b.Run(fmt.Sprintf("bytes_%d", n), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(in)))
					for b.Loop() {
						sinkString = textsafe.Sanitize(in)
					}
				})
			}
		})
	}
}

// BenchmarkCapBytes measures the rune-boundary cap around its cut. One iteration
// performs four caps because a single cap costs one to two nanoseconds, at the timer's
// resolution, so a one-call iteration would publish a series whose movement is noise.
// The four calls also let one series carry the boundary: a cut needing a multi-byte
// backoff, a cut already on a boundary, the cap at the length, and a cap past it.
//
// Read it as a step function: CapBytes compares two lengths and steps back at most
// three bytes, so its cost is independent of input size by construction. The
// regression it catches is a change making the cap proportional to the input.
//
// No SetBytes: the call does not scan its input.
func BenchmarkCapBytes(b *testing.B) {
	ascii := cleanASCII(typicalBytes)
	multi := cleanUTF8(typicalBytes)
	b.ReportAllocs()
	for b.Loop() {
		sinkString = textsafe.CapBytes(multi, typicalBytes/2)
		sinkString = textsafe.CapBytes(ascii, typicalBytes/2)
		sinkString = textsafe.CapBytes(ascii, typicalBytes)
		sinkString = textsafe.CapBytes(ascii, typicalBytes+7)
	}
}

// BenchmarkSanitizeSingleLineBounded measures the log-attribute preset on the side of
// its cap that does the work: sanitize, cap on a rune boundary, append the marker.
//
// Only the truncating case is a series. The within-cap case is the same strings.Map
// call plus a length comparison, already measured by BenchmarkSanitize/clean_ascii,
// and its property is an exact zero a benchmark cannot assert.
//
// No SetBytes: the result is a fixed 200-odd bytes whatever the input size.
func BenchmarkSanitizeSingleLineBounded(b *testing.B) {
	in := cleanASCII(typicalBytes)
	b.ReportAllocs()
	for b.Loop() {
		sinkString = textsafe.SanitizeSingleLineBounded(in, 200)
	}
}
