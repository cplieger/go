package textsafe_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/go/textsafe"
)

// composedCapFirst is the local composition textsafe directs a work-bounding
// caller to hand-roll out of the exported primitives — cap the RAW bytes on a
// rune boundary, sanitize the chunk, re-cap the growth sanitizing can cause,
// then charge the marker inside the cap — kept as the independent oracle for
// the packaged form. Written against the public API only, so it pins the
// primitive against the composition rather than against its own internals.
func composedCapFirst(s string, n int, marker string, sanitize func(string) string) (string, bool) {
	clean, cut := "", s != ""
	if n > 0 {
		chunk := textsafe.CapBytes(s, n)
		cut = len(chunk) < len(s)
		clean = sanitize(chunk)
		if len(clean) > n {
			clean, cut = textsafe.CapBytes(clean, n), true
		}
	}
	switch {
	case !cut:
		return clean, false
	case n < len(marker):
		return textsafe.CapBytes(clean, n), true
	default:
		return textsafe.CapBytes(clean, n-len(marker)) + marker, true
	}
}

// TestSanitizeCappedCapFirst covers the one-value cap-first form on both CR/LF
// policies at once (the two share every axis but that policy): the marker is
// charged against the cap so the total never exceeds max(n, 0) exactly as the
// Capped pair promises, a value that fits comes back untouched with cut false,
// a cap too small for the marker drops the marker rather than emitting a
// fragment, an empty marker caps silently, sanitization growth is re-capped,
// every truncation lands on a rune boundary, and the raw pre-cap shows up as
// the one documented divergence from sanitizing first (an oversized value
// that SHRINKS when sanitized is cut and marked here).
func TestSanitizeCappedCapFirst(t *testing.T) {
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
		{"unsafe runes replaced inside the cap", "a\x1b\u202eb", 10, "...", "a  b", "a  b", false},
		{"three-byte rune backoff", "葬送のフリーレン", 10, "...", "葬送...", "葬送...", true},
		{"two-byte rune backoff below marker width", "aé", 2, "...", "a", "a", true},
		{"four-byte rune backoff with one-byte marker", "a\U0001f600b", 5, "*", "a*", "a*", true},
		{"sanitizer growth is re-capped", "\xff\xff\xff", 8, "...", "\ufffd...", "\ufffd...", true},
		// The pre-cap's one observable divergence from SanitizeCapped: five
		// three-byte bidi overrides are 15 raw bytes but collapse to five
		// single-byte spaces, so the Capped pair returns the whole sanitized
		// form unmarked while this order cuts the raw input at the budget
		// first and marks what it dropped.
		{"oversized value that shrinks is cut and marked", "\u202e\u202e\u202e\u202e\u202e", 10, "...", "   ...", "   ...", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range []struct {
				name     string
				fn       func(string, int, string) (string, bool)
				sanitize func(string) string
				want     string
			}{
				{"SanitizeCapped", textsafe.SanitizeCapped, textsafe.Sanitize, tc.wantKeep},
				{"SanitizeSingleLineCapped", textsafe.SanitizeSingleLineCapped, textsafe.SanitizeSingleLine, tc.wantStrict},
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
				wantText, wantCut := composedCapFirst(tc.in, tc.n, tc.marker, p.sanitize)
				if got != wantText || cut != wantCut {
					t.Errorf("%s(%q, %d, %q) = (%q, %v), hand-composed form is (%q, %v)",
						p.name, tc.in, tc.n, tc.marker, got, cut, wantText, wantCut)
				}
			}
		})
	}
}

// TestSanitizeCappedBoundsSanitizerWork pins why the pair caps first: the
// cap bounds the sanitizer's WORK, not just its output. The input is 3 MiB of
// three-byte bidi overrides behind one plain letter, so sanitizing ALL of it
// would collapse to a megabyte of spaces and fill the 8-byte budget with five
// of them, which is what the sanitize-first form returns. The pair can only
// ever have looked at the first 8 bytes, and its result says so: two spaces.
// The assertion is on the cap's effect, not on how long the call took, so it
// holds on any machine.
func TestSanitizeCappedBoundsSanitizerWork(t *testing.T) {
	huge := "A" + strings.Repeat("\u202e", 1<<20)

	for _, p := range []struct {
		name     string
		capped   func(string, int, string) (string, bool)
		sanitize func(string) string
	}{
		{"Sanitize policy", textsafe.SanitizeCapped, textsafe.Sanitize},
		{"SingleLine policy", textsafe.SanitizeSingleLineCapped, textsafe.SanitizeSingleLine},
	} {
		t.Run(p.name, func(t *testing.T) {
			// "A" plus the two overrides that fit whole below byte 8.
			const wantPrecapped = "A  ..."

			got, cut := p.capped(huge, 8, "...")
			if got != wantPrecapped || !cut {
				t.Errorf("capped(3 MiB of overrides, 8, %q) = (%q, %v), want (%q, true)",
					"...", got, cut, wantPrecapped)
			}
			whole, _ := legacyCapped(huge, 8, "...", p.sanitize)
			if whole != "A    ..." {
				t.Fatalf("sanitize-first(3 MiB of overrides, 8, %q) = %q, want %q; the oracle for this test moved",
					"...", whole, "A    ...")
			}
			if got == whole {
				t.Errorf("cap-first and sanitize-first agree on %q, so the raw pre-cap did not happen", got)
			}
		})
	}
}

// TestBudgetLatchesOneTruncationFactAcrossWrites covers the aggregate half:
// several values spend ONE shared budget, the truncation fact latches once for
// the whole aggregate (one marker, not one per value), an aggregate that fits —
// including one landing exactly on the budget — comes back whole and unmarked,
// and once anything has been cut no later write can add to the aggregate or
// change what Result reports.
func TestBudgetLatchesOneTruncationFactAcrossWrites(t *testing.T) {
	groups := []string{"alpha", "beta", "gamma-delta"}

	join := func(b *textsafe.Budget) {
		for i, g := range groups {
			if i > 0 && !b.Write(", ") {
				break
			}
			if !b.Write(g) {
				break
			}
		}
	}

	t.Run("an aggregate that fits carries no marker", func(t *testing.T) {
		b := textsafe.NewBudget(64, "...")
		join(b)
		got, cut := b.Result()
		if want := "alpha, beta, gamma-delta"; got != want || cut {
			t.Errorf("Result() = (%q, %v), want (%q, false)", got, cut, want)
		}
	})

	t.Run("an aggregate landing exactly on the budget carries no marker", func(t *testing.T) {
		b := textsafe.NewBudget(len("alpha, beta, gamma-delta"), "...")
		join(b)
		got, cut := b.Result()
		if want := "alpha, beta, gamma-delta"; got != want || cut {
			t.Errorf("Result() = (%q, %v), want (%q, false)", got, cut, want)
		}
	})

	t.Run("one marker covers every cut source", func(t *testing.T) {
		b := textsafe.NewBudget(12, "...")
		join(b)
		got, cut := b.Result()
		// 12 bytes of content ("alpha, beta,") cut back to 9 to charge the
		// marker inside the budget, marked ONCE however many writes lost bytes.
		if want := "alpha, be..."; got != want || !cut {
			t.Errorf("Result() = (%q, %v), want (%q, true)", got, cut, want)
		}
		if len(got) > 12 {
			t.Errorf("Result() = %q, %d bytes exceeds the shared budget 12", got, len(got))
		}
		if n := strings.Count(got, "..."); n != 1 {
			t.Errorf("Result() = %q carries %d markers, want exactly 1 for the whole aggregate", got, n)
		}
	})

	t.Run("a spent but uncut budget still reports whole", func(t *testing.T) {
		// The return value must not become false the moment the budget is
		// spent: a caller looping on it would then stop without ever attempting
		// the write it cannot fit, and Result would call the aggregate whole
		// while silently omitting every remaining value.
		b := textsafe.NewBudget(4, "...")
		if !b.Write("abcd") {
			t.Fatal("Write(exactly the budget) = false, want true (nothing was dropped)")
		}
		if got, cut := b.Result(); got != "abcd" || cut {
			t.Errorf("Result() = (%q, %v), want (%q, false)", got, cut, "abcd")
		}
		if b.Write("e") {
			t.Error("Write past a spent budget = true, want false")
		}
		if got, cut := b.Result(); got != "a..." || !cut {
			t.Errorf("Result() = (%q, %v), want (%q, true)", got, cut, "a...")
		}
	})

	t.Run("a cut budget accepts nothing more", func(t *testing.T) {
		b := textsafe.NewBudget(5, "...")
		if b.Write("hello world") {
			t.Error("Write(over-budget) = true, want false")
		}
		before, cutBefore := b.Result()
		if b.Write("more") {
			t.Error("Write after a cut = true, want false")
		}
		after, cutAfter := b.Result()
		if after != before || cutAfter != cutBefore {
			t.Errorf("Result() moved after a refused write: (%q, %v) -> (%q, %v)",
				before, cutBefore, after, cutAfter)
		}
	})

	t.Run("an empty write never sets the fact", func(t *testing.T) {
		b := textsafe.NewBudget(4, "...")
		b.Write("")
		b.Write("ab")
		b.Write("")
		b.Write("cd")
		b.Write("")
		got, cut := b.Result()
		if want := "abcd"; got != want || cut {
			t.Errorf("Result() = (%q, %v), want (%q, false)", got, cut, want)
		}
		// The budget is now spent, so an empty write still must not mark it.
		b.Write("")
		if got, cut = b.Result(); got != "abcd" || cut {
			t.Errorf("Result() after an empty write on a spent budget = (%q, %v), want (%q, false)", got, cut, "abcd")
		}
	})
}

// TestBudgetRecapsSanitizerGrowth pins the second cap: sanitizing can GROW a
// chunk (each invalid UTF-8 byte becomes the three-byte U+FFFD), so the
// pre-capped chunk is re-capped on a rune boundary against what is left of the
// budget. Growth that still fits is left alone, growth that does not is cut and
// latches the fact, and neither case can push the aggregate past the budget or
// end it in a partial rune.
func TestBudgetRecapsSanitizerGrowth(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		writes  []string
		want    string
		wantCut bool
	}{
		{"growth that fits is untouched", 6, []string{"\xff\xff"}, "\ufffd\ufffd", false},
		{"growth accumulating exactly to the budget", 12, []string{"\xff\xff", "\xff\xff"}, "\ufffd\ufffd\ufffd\ufffd", false},
		{"growth past the remainder is re-capped", 8, []string{"ab", "\xff\xff\xff"}, "ab\ufffd...", true},
		{"growth on a spent remainder adds nothing", 2, []string{"ab", "\xff"}, "ab", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := textsafe.NewBudget(tc.n, "...")
			for _, w := range tc.writes {
				b.Write(w)
			}
			got, cut := b.Result()
			if got != tc.want || cut != tc.wantCut {
				t.Errorf("Result() = (%q, %v), want (%q, %v)", got, cut, tc.want, tc.wantCut)
			}
			if len(got) > tc.n {
				t.Errorf("Result() = %q, %d bytes exceeds the budget %d", got, len(got), tc.n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Result() = %q, not valid UTF-8", got)
			}
		})
	}
}

// TestBudgetMatchesCappedForHonestValues pins the adoption promise: for a
// valid-UTF-8 value carrying no unsafe rune the two orders are byte-identical,
// cut for cut. The corpus deliberately holds no CR or LF, so every value is
// honest under BOTH CR/LF policies.
func TestBudgetMatchesCappedForHonestValues(t *testing.T) {
	inputs := []string{
		"", "a", "hello world", "already ends in...", "葬送のフリーレン",
		"a\U0001f600b", "aé", strings.Repeat("ab", 40),
	}
	markers := []string{"...", "", "*", "...(truncated)"}
	caps := []int{-5, -1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 13, 14, 15, 100}

	for _, in := range inputs {
		for _, marker := range markers {
			for _, n := range caps {
				gotKeep, gotKeepCut := textsafe.SanitizeCapped(in, n, marker)
				wantKeep, wantKeepCut := legacyCapped(in, n, marker, textsafe.Sanitize)
				if gotKeep != wantKeep || gotKeepCut != wantKeepCut {
					t.Errorf("SanitizeCapped(%q, %d, %q) = (%q, %v), sanitizing first says (%q, %v)",
						in, n, marker, gotKeep, gotKeepCut, wantKeep, wantKeepCut)
				}
				gotStrict, gotStrictCut := textsafe.SanitizeSingleLineCapped(in, n, marker)
				wantStrict, wantStrictCut := legacyCapped(in, n, marker, textsafe.SanitizeSingleLine)
				if gotStrict != wantStrict || gotStrictCut != wantStrictCut {
					t.Errorf("SanitizeSingleLineCapped(%q, %d, %q) = (%q, %v), sanitizing first says (%q, %v)",
						in, n, marker, gotStrict, gotStrictCut, wantStrict, wantStrictCut)
				}
			}
		}
	}
}

// TestBudgetZeroValueAndPolicy covers the two remaining contract corners: the
// zero value has no budget at all (so it accepts nothing and reports the fact),
// and the constructor pair carries the CR/LF policy the way the Sanitize /
// SanitizeSingleLine pair does.
func TestBudgetZeroValueAndPolicy(t *testing.T) {
	t.Run("the zero value accepts nothing", func(t *testing.T) {
		var b textsafe.Budget
		if b.Write("x") {
			t.Error("Write on the zero value = true, want false")
		}
		if got, cut := b.Result(); got != "" || !cut {
			t.Errorf("Result() = (%q, %v), want (\"\", true)", got, cut)
		}
	})

	t.Run("an untouched budget is empty and uncut", func(t *testing.T) {
		if got, cut := textsafe.NewBudget(16, "...").Result(); got != "" || cut {
			t.Errorf("Result() = (%q, %v), want (\"\", false)", got, cut)
		}
	})

	t.Run("the constructors carry the CR/LF policy", func(t *testing.T) {
		keep := textsafe.NewBudget(16, "...")
		keep.Write("a\r\nb")
		strict := textsafe.NewSingleLineBudget(16, "...")
		strict.Write("a\r\nb")
		gotKeep, _ := keep.Result()
		gotStrict, _ := strict.Result()
		if gotKeep != "a\r\nb" {
			t.Errorf("NewBudget kept form = %q, want %q", gotKeep, "a\r\nb")
		}
		if gotStrict != "a  b" {
			t.Errorf("NewSingleLineBudget strict form = %q, want %q", gotStrict, "a  b")
		}
	})
}
