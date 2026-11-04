package keyenc

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// reservedHeavy draws components from an alphabet dominated by the two
// reserved characters, so the generator spends its budget on the inputs the
// escaping exists for rather than on text that never exercises it.
var reservedHeavy = rapid.StringOfN(rapid.RuneFrom([]rune{'a', 'b', ':', '\\'}), 0, 6, -1)

// TestJoinSplitRoundTripProperty is the package's central invariant expressed
// as a property: Split inverts Join. An invertible encoding is injective, so
// this simultaneously pins that no two distinct component sets can collide -
// the naive concatenation this package replaces fails it on the first input
// carrying a separator.
func TestJoinSplitRoundTripProperty(t *testing.T) {
	gen := rapid.SliceOfN(reservedHeavy, 2, 6)
	rapid.Check(t, func(t *rapid.T) {
		parts := gen.Draw(t, "parts")
		key := Join(parts...)
		if IsHashed(key) {
			t.Skip("routed through the hash; covered by the hashing property")
		}
		got, err := Split(key)
		if err != nil {
			t.Fatalf("Split(%q) from %q: %v", key, parts, err)
		}
		if !slices.Equal(got, parts) {
			t.Errorf("round trip lost boundaries: %q -> %q -> %q", parts, key, got)
		}
	})
}

// TestShiftedSplitsNeverCollideProperty pins the forgery the package prevents,
// directly: taking the separator that Join would place between two components
// and moving it inside the left one must change the key. This is the exact
// edit an attacker makes when a field's content carries the delimiter, and the
// property holds it for arbitrary neighbours rather than for a hand-picked pair.
func TestShiftedSplitsNeverCollideProperty(t *testing.T) {
	gen := rapid.SliceOfN(reservedHeavy, 2, 5)
	rapid.Check(t, func(t *rapid.T) {
		parts := gen.Draw(t, "parts")
		i := rapid.IntRange(0, len(parts)-2).Draw(t, "boundary")
		merged := slices.Clone(parts)
		merged[i] = merged[i] + string(Separator) + merged[i+1]
		merged = slices.Delete(merged, i+1, i+2)
		if Join(parts...) == Join(merged...) {
			t.Errorf("shifting the boundary at %d did not change the key: %q and %q both encode to %q",
				i, parts, merged, Join(parts...))
		}
	})
}

// TestSeparatorFreeComponentsEncodeVerbatimProperty pins the migration
// property every currently-safe call site depends on: when no component
// carries a reserved character the encoding IS the naive concatenation, so
// adopting this package at such a site does not change the key bytes and
// therefore does not invalidate persisted state.
func TestSeparatorFreeComponentsEncodeVerbatimProperty(t *testing.T) {
	clean := rapid.StringOfN(rapid.RuneFrom([]rune{'a', 'b', '-', '_', '.', '0'}), 0, 8, -1)
	gen := rapid.SliceOfN(clean, 2, 6)
	rapid.Check(t, func(t *rapid.T) {
		parts := gen.Draw(t, "parts")
		want := strings.Join(parts, string(Separator))
		if strings.HasPrefix(want, hashedPrefix) {
			t.Skip("the disjointness rule owns this shape")
		}
		if got := Join(parts...); got != want {
			t.Errorf("Join(%q) = %q, want the naive concatenation %q", parts, got, want)
		}
	})
}

// TestHashPreservesElementBoundariesProperty pins the length-prefixed hashing
// of oversized sets: merging two adjacent components always changes the hash,
// so ["a:b"] and ["a", "b"] cannot collide in the hashed regime any more than
// in the escaped one. A naive join-then-hash collapses exactly this boundary
// and would reintroduce the collision class the bound exists to keep out.
func TestHashPreservesElementBoundariesProperty(t *testing.T) {
	gen := rapid.SliceOfN(reservedHeavy, 2, 5)
	rapid.Check(t, func(t *rapid.T) {
		parts := gen.Draw(t, "parts")
		merged := append([]string{parts[0] + parts[1]}, parts[2:]...)
		if hashParts(parts) == hashParts(merged) {
			t.Errorf("hashParts collapsed element boundaries: %q and %q share a hash", parts, merged)
		}
	})
}

// TestNestingRoundTripsProperty pins the composition rule: an inner join
// carried as one outer component survives the outer round trip byte-for-byte
// at arbitrary depth, which is what makes a second reserved character
// unnecessary.
func TestNestingRoundTripsProperty(t *testing.T) {
	gen := rapid.SliceOfN(reservedHeavy, 1, 4)
	rapid.Check(t, func(t *rapid.T) {
		inner := gen.Draw(t, "inner")
		outerHead := reservedHeavy.Draw(t, "head")
		innerKey := Join(inner...)
		key := Join(outerHead, innerKey)
		if IsHashed(key) {
			t.Skip("routed through the hash")
		}
		outer, err := Split(key)
		if err != nil {
			t.Fatalf("Split(%q): %v", key, err)
		}
		if len(outer) != 2 || outer[1] != innerKey {
			t.Fatalf("outer split = %q, want the inner key intact at index 1 (%q)", outer, innerKey)
		}
		if IsHashed(innerKey) {
			return
		}
		back, err := Split(outer[1])
		if err != nil {
			t.Fatalf("Split inner %q: %v", outer[1], err)
		}
		if !slices.Equal(back, inner) {
			t.Errorf("inner round trip lost boundaries: %q -> %q", inner, back)
		}
	})
}
