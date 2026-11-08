package textsafe_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/go/textsafe"
)

// TestSanitize pins the shared unsafe-rune policy for the slog/JSON sinks:
// every C0 control except CR/LF (which JSON encoders escape), DEL, C1
// controls, the Unicode Bidi_Control set, and the U+2028/U+2029 line
// separators become spaces, while plain ASCII and non-control Unicode pass
// through unchanged.
func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"C0 escape introducer", "a\x1b[2Jb", "a [2Jb"},
		{"C0 NUL", "a\x00b", "a b"},
		{"C0 BEL", "a\x07b", "a b"},
		{"tab", "a\tb", "a b"},
		{"LF preserved", "a\nb", "a\nb"},
		{"CR preserved", "a\rb", "a\rb"},
		{"DEL", "a\x7fb", "a b"},
		{"C1 CSI", "a\u009bb", "a b"},
		{"C1 OSC", "a\u009db", "a b"},
		{"C1 range start", "a\u0080b", "a b"},
		{"C1 range end", "a\u009fb", "a b"},
		{"bidi ALM", "a\u061cb", "a b"},
		{"bidi LRM", "a\u200eb", "a b"},
		{"bidi RLO", "a\u202eb", "a b"},
		{"bidi isolate FSI", "a\u2068b", "a b"},
		{"line separator", "a\u2028b", "a b"},
		{"paragraph separator", "a\u2029b", "a b"},
		{"adjacent unsafe runes", "a\x1b\u202e\u009bb", "a   b"},
		{"plain ASCII unchanged", "Frieren: Beyond Journey's End", "Frieren: Beyond Journey's End"},
		{"plain unicode unchanged", "葬送のフリーレン", "葬送のフリーレン"},
		{"boundary neighbors unchanged", " ~\u00a0\u2027\u206a", " ~\u00a0\u2027\u206a"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textsafe.Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeSingleLine pins the strict single-line preset: CR and LF
// become spaces like every other C0 control, the other rune classes match
// Sanitize, and safe text passes through unchanged.
func TestSanitizeSingleLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"LF replaced", "a\nb", "a b"},
		{"CR replaced", "a\rb", "a b"},
		{"CRLF replaced", "line1\r\nline2", "line1  line2"},
		{"C0 escape introducer", "a\x1b[2Jb", "a [2Jb"},
		{"C1 CSI", "a\u009bb", "a b"},
		{"bidi RLO", "a\u202eb", "a b"},
		{"line separator", "a\u2028b", "a b"},
		{"forged log record", "ok\nlevel=ERROR fake", "ok level=ERROR fake"},
		{"plain unicode unchanged", "葬送のフリーレン", "葬送のフリーレン"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textsafe.SanitizeSingleLine(tt.in); got != tt.want {
				t.Errorf("SanitizeSingleLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeInvalidUTF8 pins the strings.Map property the doc comment
// promises for both presets: each invalid UTF-8 byte decodes as U+FFFD
// (safe, kept as the replacement rune), so the output is always valid UTF-8
// and never carries raw invalid bytes into a JSON encoder.
func TestSanitizeInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lone continuation byte", "a\xffb", "a\ufffdb"},
		{"truncated multibyte", "a\xe8\x91b", "a\ufffd\ufffdb"},
		{"surrogate half encoding", "a\xed\xa0\x80b", "a\ufffd\ufffd\ufffdb"},
		{"invalid byte beside unsafe rune", "\xff\x1b", "\ufffd "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for fn, got := range map[string]string{
				"Sanitize":           textsafe.Sanitize(tt.in),
				"SanitizeSingleLine": textsafe.SanitizeSingleLine(tt.in),
			} {
				if got != tt.want {
					t.Errorf("%s(%q) = %q, want %q", fn, tt.in, got, tt.want)
				}
				if !utf8.ValidString(got) {
					t.Errorf("%s(%q) = %q is not valid UTF-8", fn, tt.in, got)
				}
			}
		})
	}
}

// TestCapBytes pins the rune-boundary byte cap: the result is a prefix of
// the input no longer than n bytes that never ends in a partial rune, so a
// cap applied to sanitized output cannot re-introduce raw 0x80-0x9F tail
// bytes.
func TestCapBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"under cap", "abc", 10, "abc"},
		{"exact cap", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 4, "abcd"},
		{"two-byte rune backoff", "aé", 2, "a"},
		{"three-byte rune backoff", "葬送", 4, "葬"},
		{"three-byte rune exact boundary", "葬送", 3, "葬"},
		{"four-byte rune backoff", "a\U0001f600", 3, "a"},
		{"replacement-rune growth capped", "\ufffd\ufffd", 4, "\ufffd"},
		{"zero cap", "abc", 0, ""},
		{"negative cap", "abc", -1, ""},
		{"empty input", "", 5, ""},
		{"all continuation bytes", "\x80\x81\x82", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textsafe.CapBytes(tt.in, tt.n); got != tt.want {
				t.Errorf("CapBytes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

// TestCapBytesTail pins the rune-boundary tail cap: the result is a suffix of
// the input no longer than n bytes that never BEGINS in a partial rune, so a
// capped field cannot open on the U+FFFD encoding/json substitutes for a split
// leading fragment. The combining-mark case pins the package's Unicode
// non-goal: the cut is rune-safe, not grapheme-safe.
func TestCapBytesTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"under cap", "abc", 10, "abc"},
		{"exact cap", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 4, "cdef"},
		{"two-byte rune advance", "éa", 2, "a"},
		{"three-byte rune advance", "葬送", 4, "送"},
		{"three-byte rune exact boundary", "葬送", 3, "送"},
		{"four-byte rune advance", "\U0001f600a", 3, "a"},
		{"all multi-byte input", "葬送のフリーレン", 7, "レン"},
		{"combining mark survives without its base", "e\u0301", 2, "\u0301"},
		{"replacement-rune growth capped", "\ufffd\ufffd", 4, "\ufffd"},
		{"zero cap", "abc", 0, ""},
		{"negative cap", "abc", -1, ""},
		{"empty input", "", 5, ""},
		{"all continuation bytes", "\x80\x81\x82", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textsafe.CapBytesTail(tt.in, tt.n); got != tt.want {
				t.Errorf("CapBytesTail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

// TestIsUnsafeCRLFPolicy pins the CR/LF split across the two predicates: CR
// and LF are safe only for a sink whose encoder escapes them
// ([textsafe.IsUnsafeMultiLine]); a single-line sink
// ([textsafe.IsUnsafeSingleLine]) treats them as unsafe like every other C0
// control, and the other rune classes are unsafe under both policies.
func TestIsUnsafeCRLFPolicy(t *testing.T) {
	for _, r := range []rune{'\n', '\r'} {
		if textsafe.IsUnsafeMultiLine(r) {
			t.Errorf("IsUnsafeMultiLine(%U) = true, want false", r)
		}
		if !textsafe.IsUnsafeSingleLine(r) {
			t.Errorf("IsUnsafeSingleLine(%U) = false, want true", r)
		}
	}
	for _, r := range []rune{0x00, 0x1b, '\t', 0x7f, '\u009b', '\u202e', '\u2028'} {
		if !textsafe.IsUnsafeMultiLine(r) || !textsafe.IsUnsafeSingleLine(r) {
			t.Errorf("IsUnsafeMultiLine(%U) or IsUnsafeSingleLine(%U) = false, want true under both policies", r, r)
		}
	}
	for _, r := range []rune{'a', ' ', 'é', '葬'} {
		if textsafe.IsUnsafeMultiLine(r) || textsafe.IsUnsafeSingleLine(r) {
			t.Errorf("IsUnsafeMultiLine(%U) or IsUnsafeSingleLine(%U) = true, want false under both policies", r, r)
		}
	}
}

// TestIsBidiControl pins the complete Bidi_Control set (the U+061C/U+200E/
// U+200F singleton marks plus the override and isolate ranges) and the
// adjacent near-miss code points that must stay safe.
func TestIsBidiControl(t *testing.T) {
	unsafe := []rune{
		'\u061c', '\u200e', '\u200f',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069',
	}
	for _, r := range unsafe {
		if !textsafe.IsBidiControl(r) {
			t.Errorf("IsBidiControl(%U) = false, want true", r)
		}
	}
	safe := []rune{'\u061b', '\u061d', '\u200d', '\u2029', '\u2065', '\u206a', 'a'}
	for _, r := range safe {
		if textsafe.IsBidiControl(r) {
			t.Errorf("IsBidiControl(%U) = true, want false", r)
		}
	}
}

// TestClassifierUnicodeConformance sweeps every valid rune and checks the
// classifiers against the standard library's Unicode tables as an
// independent oracle: IsBidiControl must equal unicode.Is(Bidi_Control, r)
// exactly, IsUnsafe must equal the documented union — Cc controls
// (unicode.IsControl covers C0, DEL, and C1 exactly), Bidi_Control, and the
// U+2028/U+2029 separators — with the two policies diverging on CR/LF alone,
// and IsUnsafeNonASCII must equal both the above-ASCII restriction of that
// union and the literal C1|bidi|separator enumeration a percent-escaper
// composes. The drift guard against a future hand-edit of the hardcoded ranges.
func TestClassifierUnicodeConformance(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		bidi := unicode.Is(unicode.Bidi_Control, r)
		if got := textsafe.IsBidiControl(r); got != bidi {
			t.Fatalf("IsBidiControl(%U) = %v, unicode.Bidi_Control says %v", r, got, bidi)
		}
		unsafeStrict := unicode.IsControl(r) || bidi || r == '\u2028' || r == '\u2029'
		if got := textsafe.IsUnsafeSingleLine(r); got != unsafeStrict {
			t.Fatalf("IsUnsafe(%U, false) = %v, oracle says %v", r, got, unsafeStrict)
		}
		unsafeKeep := unsafeStrict && r != '\n' && r != '\r'
		if got := textsafe.IsUnsafeMultiLine(r); got != unsafeKeep {
			t.Fatalf("IsUnsafe(%U, true) = %v, oracle says %v", r, got, unsafeKeep)
		}
		nonASCII := unsafeStrict && r > unicode.MaxASCII
		if got := textsafe.IsUnsafeNonASCII(r); got != nonASCII {
			t.Fatalf("IsUnsafeNonASCII(%U) = %v, oracle says %v", r, got, nonASCII)
		}
		enumerated := (r >= 0x80 && r <= 0x9f) || bidi || r == '\u2028' || r == '\u2029'
		if got := textsafe.IsUnsafeNonASCII(r); got != enumerated {
			t.Fatalf("IsUnsafeNonASCII(%U) = %v, C1|bidi|separator enumeration says %v", r, got, enumerated)
		}
	}
}

// TestPolicyCardinalityPinned fixes the SIZE of each rune class, which the
// conformance sweep above deliberately cannot: that sweep compares the
// hardcoded ranges against the toolchain's own tables, so a Unicode release
// that adds a Bidi_Control or a Cc — together with an edit here mirroring it —
// would keep the sweep green while silently widening what every consumer's
// sinks replace. These four counts make that a reviewed change instead.
//
// Identical on go1.26.7 (Unicode 15.0.0) and go1.27.0 (Unicode 17.0.0),
// because the policy is pinned to code points rather than to table lookups:
// Cc(65) + Bidi_Control(12) + U+2028/U+2029 = 79 under the single-line
// policy, minus CR and LF = 77 under the multi-line one, and C1(32) +
// Bidi_Control(12) + the two separators = 46 above ASCII.
func TestPolicyCardinalityPinned(t *testing.T) {
	var bidi, single, multi, nonASCII int
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if textsafe.IsBidiControl(r) {
			bidi++
		}
		if textsafe.IsUnsafeSingleLine(r) {
			single++
		}
		if textsafe.IsUnsafeMultiLine(r) {
			multi++
		}
		if textsafe.IsUnsafeNonASCII(r) {
			nonASCII++
		}
	}
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"IsBidiControl", bidi, 12},
		{"IsUnsafeSingleLine", single, 79},
		{"IsUnsafeMultiLine", multi, 77},
		{"IsUnsafeNonASCII", nonASCII, 46},
	} {
		if c.got != c.want {
			t.Errorf("%s classifies %d runes, want %d; the policy's size changed under Unicode %s -- widen or narrow this pin only with the change reviewed",
				c.name, c.got, c.want, unicode.Version)
		}
	}
}

// TestIsUnsafeNonASCII pins the above-ASCII subset predicate: C1 controls,
// the Bidi_Control set, and the U+2028/U+2029 separators are flagged, while
// every ASCII rune — including the C0 controls, CR/LF, and DEL that IsUnsafe
// classifies — and safe non-ASCII neighbors stay false.
func TestIsUnsafeNonASCII(t *testing.T) {
	unsafe := []rune{
		'\u0080', '\u009b', '\u009d', '\u009f',
		'\u061c', '\u200e', '\u202e', '\u2066', '\u2069',
		'\u2028', '\u2029',
	}
	for _, r := range unsafe {
		if !textsafe.IsUnsafeNonASCII(r) {
			t.Errorf("IsUnsafeNonASCII(%U) = false, want true", r)
		}
	}
	safe := []rune{0x00, '\x1b', '\t', '\n', '\r', 0x7f, 'a', '~', '\u00a0', '\u2027', '\u206a', 'é', '葬'}
	for _, r := range safe {
		if textsafe.IsUnsafeNonASCII(r) {
			t.Errorf("IsUnsafeNonASCII(%U) = true, want false", r)
		}
	}
}

// TestSanitizeSingleLineBounded covers the log-bound preset: within-cap
// input is byte-identical to SanitizeSingleLine (no marker), an over-cap
// result truncates on a rune boundary with the "..." marker outside the cap,
// the cap measures the SANITIZED form (invalid bytes grow into U+FFFD), and
// the non-positive-cap and empty-input edges hold.
func TestSanitizeSingleLineBounded(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"within cap untouched", "hello", 10, "hello"},
		{"exactly at cap untouched", "hello", 5, "hello"},
		{"over cap truncated with marker", "hello world", 5, "hello..."},
		{"sanitizes before capping", "a\nb\x1bc", 10, "a b c"},
		{"rune boundary respected", "aé", 2, "a..."},
		{"cap measures sanitized growth", "\xff\xff", 3, "\uFFFD..."},
		{"non-positive cap yields the marker alone", "abc", 0, "..."},
		{"negative cap yields the marker alone", "abc", -2, "..."},
		{"empty input stays empty", "", 0, ""},
		{"empty input with negative cap stays empty", "", -1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := textsafe.SanitizeSingleLineBounded(tc.in, tc.n); got != tc.want {
				t.Errorf("SanitizeSingleLineBounded(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestSanitizeCappedPair covers the caller-marker primitive on both CR/LF
// policies at once (the two functions share every axis but that policy, and
// sanitizing is 1:1 per rune so the cut decision is identical for both): the
// marker is charged against the cap so the total never exceeds max(n, 0), a
// within-cap value comes back untouched with cut false, a cap too small for
// the marker drops the marker rather than emitting a fragment, an empty
// marker caps silently, the cut is measured on the SANITIZED form (invalid
// bytes grow into U+FFFD), and every truncation lands on a rune boundary.
func TestSanitizeCappedPair(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		n          int
		marker     string
		wantKeep   string
		wantStrict string
		wantCut    bool
	}{
		{"within cap untouched", "hello", 10, "...", "hello", "hello", false},
		{"exactly at cap untouched", "hello", 5, "...", "hello", "hello", false},
		{"marker charged against the cap", "hello world", 8, "...", "hello...", "hello...", true},
		{"total never exceeds the cap", "hello world", 5, "...", "he...", "he...", true},
		{"cap equals marker width yields the marker alone", "hello", 3, "...", "...", "...", true},
		{"cap below marker width drops the marker", "hello world", 2, "...", "he", "he", true},
		{"louder caller marker fills the cap", "hello world again", 14, "...(truncated)", "...(truncated)", "...(truncated)", true},
		{"louder marker over budget is dropped", "hello world", 10, "...(truncated)", "hello worl", "hello worl", true},
		{"empty marker caps silently", "hello world", 5, "", "hello", "hello", true},
		{"non-positive cap yields empty", "abc", 0, "...", "", "", true},
		{"negative cap yields empty", "abc", -3, "...", "", "", true},
		{"empty input stays empty", "", 5, "...", "", "", false},
		{"empty input with negative cap stays empty", "", -1, "...", "", "", false},
		{"CRLF policy divergence within cap", "a\r\nb", 10, "...", "a\r\nb", "a  b", false},
		{"CRLF policy divergence over cap", "ab\ncd", 4, "...", "a...", "a...", true},
		{"unsafe runes replaced before capping", "a\x1b\u202eb", 10, "...", "a  b", "a  b", false},
		{"three-byte rune backoff", "葬送のフリーレン", 10, "...", "葬送...", "葬送...", true},
		{"two-byte rune backoff below marker width", "aé", 2, "...", "a", "a", true},
		{"four-byte rune backoff with one-byte marker", "a\U0001f600b", 5, "*", "a*", "a*", true},
		{"cut measured on sanitized growth", "\xff\xff\xff", 8, "...", "\ufffd...", "\ufffd...", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range []struct {
				name string
				fn   func(string, int, string) (string, bool)
				want string
			}{
				{"SanitizeCapped", textsafe.SanitizeCapped, tc.wantKeep},
				{"SanitizeSingleLineCapped", textsafe.SanitizeSingleLineCapped, tc.wantStrict},
			} {
				got, cut := p.fn(tc.in, tc.n, tc.marker)
				if got != p.want || cut != tc.wantCut {
					t.Errorf("%s(%q, %d, %q) = (%q, %v), want (%q, %v)",
						p.name, tc.in, tc.n, tc.marker, got, cut, p.want, tc.wantCut)
				}
				if bound := max(tc.n, 0); len(got) > bound {
					t.Errorf("%s(%q, %d, %q) = %q, %d bytes exceeds the hard cap %d",
						p.name, tc.in, tc.n, tc.marker, got, len(got), bound)
				}
				if !utf8.ValidString(got) {
					t.Errorf("%s(%q, %d, %q) = %q, not valid UTF-8", p.name, tc.in, tc.n, tc.marker, got)
				}
			}
		})
	}
}

// legacySanitizeSingleLineBounded is the byte-parity oracle for the ~15 plain
// call sites across the cplieger repos whose output must not move by a byte.
func legacySanitizeSingleLineBounded(s string, n int) string {
	s = textsafe.SanitizeSingleLine(s)
	if s == "" || len(s) <= n {
		return s
	}
	return textsafe.CapBytes(s, n) + "..."
}

// legacyCapped is the sanitize-first form: it sanitizes ALL of the value and
// bounds the SANITIZED form. The Capped pair caps first; this oracle pins that
// the two agree on every valid-UTF-8 value with no unsafe rune, and it is the
// sanitize-first arm the work-bound test and benchmark measure against.
func legacyCapped(s string, n int, marker string, sanitize func(string) string) (string, bool) {
	s = sanitize(s)
	if s == "" || len(s) <= n {
		return s, false
	}
	if n < len(marker) {
		return textsafe.CapBytes(s, n), true
	}
	return textsafe.CapBytes(s, n-len(marker)) + marker, true
}

// TestSanitizeCappedParity holds the pair byte-for-byte against the
// hand-composed cap-first form across the adversarial corpus, every
// interesting cap and several marker widths. The corpus carries both classes
// where cap-first differs from sanitizing first: a value over the cap holding
// multi-byte unsafe runes, which collapse to single-byte spaces, so this pair
// can keep fewer bytes and cuts and marks it even when its sanitized form
// fits; and invalid UTF-8, where the raw cut can back off over a stray
// continuation byte and keep fewer.
func TestSanitizeCappedParity(t *testing.T) {
	inputs := []string{
		"", "a", "hello world", "already ends in...", "a\r\nb", "ab\ncd",
		"a\x1b[2Jb", "a\u009b2Jb", "a\u202evil\u202cb", "葬送のフリーレン",
		"a\U0001f600b", "aé", "\xff\xff\xff", "\xed\xa0\x80", "0\xa4\xa4",
		strings.Repeat("\u202e", 5), "A" + strings.Repeat("\u202e", 64),
	}
	markers := []string{"...", "", "*", "…", "...(truncated)"}
	caps := []int{-5, -1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 20, 100}
	policies := []struct {
		name     string
		capped   func(string, int, string) (string, bool)
		sanitize func(string) string
	}{
		{"SanitizeCapped", textsafe.SanitizeCapped, textsafe.Sanitize},
		{"SanitizeSingleLineCapped", textsafe.SanitizeSingleLineCapped, textsafe.SanitizeSingleLine},
	}
	for _, in := range inputs {
		for _, marker := range markers {
			for _, n := range caps {
				for _, p := range policies {
					got, cut := p.capped(in, n, marker)
					wantText, wantCut := composedCapFirst(in, n, marker, p.sanitize)
					if got != wantText || cut != wantCut {
						t.Errorf("%s(%q, %d, %q) = (%q, %v), the cap-first form is (%q, %v)",
							p.name, in, n, marker, got, cut, wantText, wantCut)
					}
				}
			}
		}
	}
}

// TestSanitizeSingleLineBoundedParity pins the rebuilt preset byte-for-byte
// against that oracle across the adversarial corpus and every interesting
// cap, including the caps just below the marker's own width and the
// n < len(s) <= n+3 window where a marker-inside-cap primitive would decide
// differently. The preset's marker stays OUTSIDE its cap; that is its
// contract, not a defect the rebuild corrects.
func TestSanitizeSingleLineBoundedParity(t *testing.T) {
	inputs := []string{
		"", "a", "hello world", "already ends in...", "a\nb\r\nc", "a\x1b[2Jb",
		"a\u202evil\u202cb", "葬送のフリーレン", "a\U0001f600b", "\xff\xff\xff", "\xed\xa0\x80",
	}
	caps := []int{-5, -1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 11, 12, 13, 14, 100}
	for _, in := range inputs {
		for _, n := range caps {
			got := textsafe.SanitizeSingleLineBounded(in, n)
			if want := legacySanitizeSingleLineBounded(in, n); got != want {
				t.Errorf("SanitizeSingleLineBounded(%q, %d) = %q, pre-rebuild form was %q", in, n, got, want)
			}
		}
	}
}
