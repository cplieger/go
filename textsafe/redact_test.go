package textsafe_test

import (
	"math"
	"slices"
	"testing"

	"github.com/cplieger/go/textsafe"
)

// TestRedactSingleLineCapped pins the redaction, sanitizing and cap, one group
// per property.
func TestRedactSingleLineCapped(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		n       int
		marker  string
		secrets []string
		want    string
		wantCut bool
	}{
		// An occurrence in the sanitized form is redacted like one in s.
		{"CR split secret joined by sanitizing", "auth pass\rword ok", 100, "...", []string{"pass word"}, "auth [redacted] ok", false},
		{"invalid byte joined into a U+FFFD secret", "k=a\xffb;", 100, "...", []string{"a\ufffdb"}, "k=[redacted];", false},
		{"invalid-UTF-8 secret inside a rune redacts the whole rune", "a\xffb", 100, "...", []string{"\xef"}, "a[redacted]b", false},
		{"invalid-UTF-8 secret ending inside a rune redacts the whole rune", "a\xffb", 100, "...", []string{"\xbd"}, "a[redacted]b", false},
		{"unsafe rune inside a secret", "x to\x1bken y", 100, "...", []string{"to\x1bken"}, "x [redacted] y", false},
		{"stray continuation byte between ASCII keeps its neighbours", "A\x82B", 100, "...", []string{"\x82"}, "A[redacted]B", false},
		{"stray continuation byte at the start", "\x82B", 100, "...", []string{"\x82"}, "[redacted]B", false},
		{"stray continuation byte at the end", "A\x82", 100, "...", []string{"\x82"}, "A[redacted]", false},
		{"stray continuation byte after a valid rune keeps the rune", "é\x82", 100, "...", []string{"\x82"}, "é[redacted]", false},

		// Occurrences in s and in its sanitized form that overlap are one mark.
		{"raw prefix overlaps a CR-joined secret", "user=bob\rpw end", 100, "...", []string{"bob pw", "user=b"}, "[redacted] end", false},
		{"raw prefix inside a CR-joined secret", "auth pass\rword ok", 100, "...", []string{"pass word", "auth pas"}, "[redacted] ok", false},
		{"raw occurrence overlaps sanitized ones of the same secret", "a\ra a", 100, "...", []string{"a a"}, "[redacted]", false},

		// A cut leaves no part of a secret before the marker.
		{"cut through a redacted secret", "id SECRETVALUE tail text", 10, "...", []string{"SECRETVALUE"}, "id [red...", true},
		{"secret prefix at the cut dropped", "0123456789 tok_12345", 18, "...", []string{"tok_1234567"}, "0123456789 ...", true},
		{"longest secret prefix at the cut wins", "0123456789 tok_12345", 18, "...", []string{"_1x", "tok_1234567"}, "0123456789 ...", true},
		{"prefix drop backs off to a rune start", "x€abZZZZZ", 9, "...", []string{"\xacabQ"}, "x...", true},
		{"no prefix drop without a cut", "ends with tok_", 100, "...", []string{"tok_1234567"}, "ends with tok_", false},

		// Secrets are deduplicated, empty ones dropped, overlaps redacted whole.
		{"empty secret dropped", "abc", 100, "...", []string{""}, "abc", false},
		{"duplicate secrets", "a key b key", 100, "...", []string{"key", "key"}, "a [redacted] b [redacted]", false},
		{"shorter secret listed first", "x apikey123 y", 100, "...", []string{"key", "apikey123"}, "x [redacted] y", false},
		{"longer secret listed first", "x apikey123 y", 100, "...", []string{"apikey123", "key"}, "x [redacted] y", false},
		{"overlapping secrets", "<abcdef>", 100, "...", []string{"abcd", "cdef"}, "<[redacted]>", false},
		{"overlapping occurrences of one secret", "<aaaa>", 100, "...", []string{"aaa"}, "<[redacted]>", false},
		{"adjacent occurrences", "keykey", 100, "...", []string{"key"}, "[redacted]", false},
		{"adjacent different secrets", "<key1key2>", 100, "...", []string{"key2", "key1"}, "<[redacted]>", false},
		{"no secrets", "a\x1bb", 100, "...", nil, "a b", false},

		// One line, at most n bytes, the marker inside n, cut only when cut.
		{"within cap", "a\nb", 10, "...", nil, "a b", false},
		{"exactly at cap", "hello", 5, "...", nil, "hello", false},
		{"marker inside the cap", "hello world", 8, "...", nil, "hello...", true},
		{"cap below marker width drops the marker", "hello world", 2, "...", nil, "he", true},
		{"empty marker caps silently", "hello world", 5, "", nil, "hello", true},
		{"zero cap", "abc", 0, "...", nil, "", true},
		{"negative cap", "abc", -3, "...", nil, "", true},
		{"most negative cap", "secret text", math.MinInt, "...", []string{"secret"}, "", true},
		{"empty input with negative cap", "", -1, "...", []string{"k"}, "", false},
		{"cap measures the redacted form", "k=ab", 6, "...", []string{"ab"}, "k=[...", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := textsafe.RedactSingleLineCapped(tt.in, tt.n, textsafe.Marker(tt.marker), tt.secrets...)
			if got != tt.want || cut != tt.wantCut {
				t.Errorf("RedactSingleLineCapped(%q, %d, %q, %q) = (%q, %v), want (%q, %v)",
					tt.in, tt.n, tt.marker, tt.secrets, got, cut, tt.want, tt.wantCut)
			}
		})
	}
}

// TestRedactSingleLineCappedLeavesSecretsAlone pins that the caller's slice
// is neither reordered nor compacted.
func TestRedactSingleLineCappedLeavesSecretsAlone(t *testing.T) {
	secrets := []string{"b", "a", "", "b"}
	want := slices.Clone(secrets)
	textsafe.RedactSingleLineCapped("a b", 100, "...", secrets...)
	if !slices.Equal(secrets, want) {
		t.Errorf("secrets = %q after the call, want %q", secrets, want)
	}
}
