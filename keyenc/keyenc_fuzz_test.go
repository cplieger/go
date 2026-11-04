package keyenc

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// FuzzJoinSplitInjective is the coverage-guided complement to the rapid
// properties: those draw from a 4-rune alphabet, this reaches arbitrary bytes
// including invalid UTF-8, embedded NULs and adversarial hashed-identity
// spellings. The invariants are the ones a collision would break at a real
// call site - a cache returning another key's entry, a dedupe token
// suppressing a distinct event - so a finding here is a live defect rather
// than a curiosity.
func FuzzJoinSplitInjective(f *testing.F) {
	f.Add("a", "b", "c")
	f.Add("a:b", "c", "")
	f.Add(`x\`, ":y", "z")
	f.Add("sha256", strings.Repeat("0", hashedHexLen), "")
	f.Add("", "", "")
	f.Add("\xff\xfe:|", `a\`, "\x00")
	f.Add(strings.Repeat("x", 5000), strings.Repeat("y", 4000), "z")
	f.Add("gitea", "git.example.com:3000", "")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		parts := []string{a, b, c}
		key := Join(parts...)

		// Oversized sets must reduce to the fixed-size identity: the bound is
		// what stops a caller folding unbounded upstream data into a key.
		if len(a)+len(b)+len(c) > MaxComponentBytes {
			if !IsHashed(key) {
				t.Fatalf("oversized input not reduced: %d bytes", len(key))
			}
			if len(key) != len(hashedPrefix)+hashedHexLen {
				t.Errorf("hashed identity is %d bytes, want %d", len(key), len(hashedPrefix)+hashedHexLen)
			}
		}

		if IsHashed(key) {
			// Hashing is one-way, and Split must say so rather than guess.
			if _, err := Split(key); !errors.Is(err, ErrHashed) {
				t.Errorf("Split(<hashed>) error = %v, want ErrHashed", err)
			}
		} else {
			// Below the bound the encoding is invertible, which is the same
			// statement as injective.
			got, err := Split(key)
			if err != nil {
				t.Fatalf("Split(%q) from %q: %v", key, parts, err)
			}
			if !slices.Equal(got, parts) {
				t.Errorf("round trip lost boundaries: %q -> %q -> %q", parts, key, got)
			}
		}

		// Shifting a boundary into a component must change the key, at both
		// boundaries of the triple.
		if Join(a+string(Separator)+b, c) == key {
			t.Errorf("merging the first boundary did not change the key %q", key)
		}
		if Join(a, b+string(Separator)+c) == key {
			t.Errorf("merging the second boundary did not change the key %q", key)
		}

		// The raw and hashed output domains stay disjoint, so no component
		// content can spell another set's identity.
		forged := hashParts([]string{a})
		if Join(forged) == forged {
			t.Errorf("Join(%.24q) returned the raw hashed-identity spelling", forged)
		}

		// Length-prefixed hashing keeps boundaries in the hashed regime too.
		if hashParts(parts) == hashParts([]string{a + b, c}) {
			t.Errorf("hashParts collapsed a boundary for %q", parts)
		}
	})
}

// FuzzSplitAcceptsArbitraryInput pins that Split is total over untrusted
// strings: a key read back out of a persisted file, a URL path segment or a
// localStorage value was not necessarily written by this package, so Split must
// answer with a value or an error and never panic. Anything it accepts must
// re-encode to itself, which is the inverse direction of the round-trip
// property and the one a hostile input exercises.
func FuzzSplitAcceptsArbitraryInput(f *testing.F) {
	f.Add("a:b")
	f.Add(`a\`)
	f.Add(`\\`)
	f.Add(`\:`)
	f.Add("sha256:" + strings.Repeat("0", hashedHexLen))
	f.Add("sha256:short")
	f.Add("")
	f.Add(":::")
	f.Add("\x00\xff")
	f.Fuzz(func(t *testing.T, key string) {
		parts, err := Split(key)
		if err != nil {
			if !errors.Is(err, ErrHashed) && !errors.Is(err, ErrMalformed) {
				t.Errorf("Split(%q) returned an undocumented error: %v", key, err)
			}
			return
		}
		if parts == nil {
			if key != "" {
				t.Errorf("Split(%q) returned no components without an error", key)
			}
			return
		}
		// Every accepted key is canonical: re-encoding its components
		// reproduces it exactly. This catches an encoder and decoder that
		// disagree about which escapes are significant.
		if got := Join(parts...); got != key {
			t.Errorf("Split/Join disagree: %q -> %q -> %q", key, parts, got)
		}
	})
}
