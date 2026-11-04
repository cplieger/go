package textsafe_test

import (
	"math"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/go/textsafe"
)

// The sentinels hold neither bracket and are no substring of "[redacted]", so
// no mark can help spell one and a substring check on the output is exact.
// rawUnsafeSentinel is unsafeSentinel with a CR for its space: inserted raw,
// it occurs whole only in the sanitized form.
const (
	sentinel          = "Zq7SnTlXk"
	unsafeSentinel    = "Zq7S nTlXk"
	rawUnsafeSentinel = "Zq7S\rnTlXk"
)

func FuzzRedactSingleLineCapped(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s, 8, "...", "line", "\ufffd", 3)
	}
	f.Add("auth pass\rword ok", 100, "...", "pass word", "auth pas", 0)
	f.Add("user=bob\rpw end", 100, "...", "bob pw", "user=b", 0)
	f.Add("a\ra a", 100, "...", "a a", "", 0)
	f.Add("0123456789 tok_12345", 18, "...", "tok_1234567", "_1x", 11)
	f.Add("x€abZZZZZ", 9, "...", "\xacabQ", "", 2)
	f.Add("<abcdef>", 6, "*", "abcd", "cdef", 4)
	f.Add("<aaaa>", 3, "...", "aaa", "aaa", 1)
	f.Add("id SECRETVALUE tail", 10, "...", "SECRETVALUE", "VALUE tail", 3)
	f.Add("k=[redacted]x", 12, "", "d]x", "[", 5)
	f.Add("abc", -1, "...", "", "b", -7)
	f.Add("\xe8\xae\xe3\xe3", -66, "0", "0", "\xef", 2)
	f.Add("A\x82B", 100, "...", "\x82", "", 1)
	f.Add("é\x82", 100, "...", "\x82", "", 1)
	f.Add("secret text", math.MinInt, "...", "secret", "", 6)
	f.Add("PXAYQ", 100, "...", "A", "X[redacted]Y", 2)
	f.Fuzz(func(t *testing.T, s string, n int, marker, a, b string, at int) {
		checkRedact(t, s, n, marker, []string{a, b})
		pos := int(uint(at) % uint(len(s)+1))
		checkSentinel(t, s[:pos]+sentinel+s[pos:], n, marker, sentinel, a, b)
		// The raw match of the last secret ends inside the CR-split sentinel.
		checkSentinel(t, s[:pos]+rawUnsafeSentinel+s[pos:], n, marker, unsafeSentinel, a, b, s[max(pos-1, 0):pos]+"Zq7")
	})
}

func checkSentinel(t *testing.T, s string, n int, marker, sentinel string, secrets ...string) {
	t.Helper()
	head, full := checkRedact(t, s, n, marker, append(secrets, sentinel))
	if strings.Contains(full, sentinel) || strings.Contains(head, sentinel) {
		t.Fatalf("sentinel %q survives in %q (uncapped %q) from %q", sentinel, head, full, s)
	}
}

// checkRedact returns the capped result without its marker, and the uncapped
// result.
func checkRedact(t *testing.T, s string, n int, marker string, secrets []string) (head, full string) {
	t.Helper()
	out, cut := textsafe.RedactSingleLineCapped(s, n, marker, secrets...)
	full, fullCut := textsafe.RedactSingleLineCapped(s, math.MaxInt, marker, secrets...)
	if fullCut {
		t.Fatalf("uncapped call reported a cut for %q", s)
	}
	if want := reference(s, secrets); full != want {
		t.Fatalf("uncapped result %q for %q, %q, want %q", full, s, secrets, want)
	}
	assertSafeLine(t, full)
	if len(out) > max(n, 0) {
		t.Fatalf("len(%q) = %d, over n = %d", out, len(out), n)
	}
	if !cut {
		if out != full {
			t.Fatalf("uncut result %q differs from uncapped %q", out, full)
		}
		return out, full
	}
	if full == "" || len(full) <= n {
		t.Fatalf("cut reported for %q, which fits n = %d", full, n)
	}
	head, room := out, n
	if n >= len(marker) {
		if !strings.HasSuffix(out, marker) {
			t.Fatalf("cut result %q lacks marker %q", out, marker)
		}
		head, room = strings.TrimSuffix(out, marker), n-len(marker)
	}
	assertSafeLine(t, head)
	end := max(room, 0)
	for end > 0 && !utf8.RuneStart(full[end]) {
		end--
	}
	atCut := full[:end]
	if !strings.HasPrefix(atCut, head) {
		t.Fatalf("head %q is not a prefix of the capped uncapped result %q", head, atCut)
	}
	for _, secret := range secrets {
		for k := 1; k < len(secret); k++ {
			if strings.HasSuffix(atCut, secret[:k]) && len(head) > len(atCut)-k {
				t.Fatalf("head %q keeps the prefix %q of secret %q at the cut", head, secret[:k], secret)
			}
		}
	}
	return head, full
}

// reference is the redaction guarantee by brute force. It shares no code with
// the package, so a flaw in how the package composes its steps cannot hide in
// both.
func reference(s string, secrets []string) string {
	type unit struct {
		src, san [2]int
		text     string
	}
	var units []unit
	sanitized := ""
	for i, r := range s {
		text := string(r)
		if unsafeRune(r) {
			text = " "
		}
		if k := len(units) - 1; k >= 0 {
			units[k].src[1] = i
		}
		units = append(units, unit{src: [2]int{i, len(s)}, san: [2]int{len(sanitized), len(sanitized) + len(text)}, text: text})
		sanitized += text
	}
	hidden := make([]bool, len(units))
	for k, u := range units {
		for _, secret := range secrets {
			if secret != "" && (overlapsOccurrence(s, secret, u.src) || overlapsOccurrence(sanitized, secret, u.san)) {
				hidden[k] = true
			}
		}
	}
	var b strings.Builder
	for k, u := range units {
		switch {
		case !hidden[k]:
			b.WriteString(u.text)
		case k == 0 || !hidden[k-1]:
			b.WriteString("[redacted]")
		}
	}
	return b.String()
}

func overlapsOccurrence(text, secret string, span [2]int) bool {
	for i := max(span[0]-len(secret)+1, 0); i < span[1] && i+len(secret) <= len(text); i++ {
		if text[i:i+len(secret)] == secret {
			return true
		}
	}
	return false
}

// unsafeRune is the single-line policy written from its definition, with the
// standard library's bidi table.
func unsafeRune(r rune) bool {
	return r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || r == '\u2028' || r == '\u2029' ||
		unicode.Is(unicode.Bidi_Control, r)
}

func assertSafeLine(t *testing.T, text string) {
	t.Helper()
	if !utf8.ValidString(text) || strings.ContainsFunc(text, unsafeRune) {
		t.Fatalf("%q is not one safe valid-UTF-8 line", text)
	}
}
