package keyenc

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestJoinSplitRoundTrip pins the encoder/decoder pair on the shapes that
// motivate the package: a component carrying the separator, a component
// carrying the escape, an empty component in a multi-component set, and the
// adjacent-delimiter-capable-fields case a naive join collapses.
func TestJoinSplitRoundTrip(t *testing.T) {
	cases := map[string][]string{
		"plain":                {"a", "b", "c"},
		"separator in head":    {"a:b", "c"},
		"separator in tail":    {"a", "b:c"},
		"escape in component":  {`a\b`, "c"},
		"escape then sep":      {`a\:b`, "c"},
		"empty middle":         {"a", "", "c"},
		"empty head":           {"", "a"},
		"empty tail":           {"a", ""},
		"two empties":          {"", ""},
		"only separators":      {"::", ":"},
		"host with port":       {"gitea", "git.example.com:3000"},
		"invalid utf8":         {"\xff\xfe", "a"},
		"newline and nul":      {"a\nb", "c\x00d"},
		"single non-empty":     {"solo"},
		"windows-ish path":     {`C:\dir`, "file"},
		"trailing lone escape": {`a\`, "b"},
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			key := Join(parts...)
			if IsHashed(key) {
				t.Fatalf("Join(%q) = %q unexpectedly hashed; this case is meant to stay raw", parts, key)
			}
			got, err := Split(key)
			if err != nil {
				t.Fatalf("Split(%q) from parts %q: %v", key, parts, err)
			}
			if !slices.Equal(got, parts) {
				t.Errorf("round trip lost components: %q -> %q -> %q", parts, key, got)
			}
		})
	}
}

// TestJoinIsByteIdenticalToNaiveConcatenation pins the property that lets an
// existing key adopt this package without changing its bytes: when no
// component carries a reserved character, the encoding IS the naive
// concatenation. Every currently-safe call site relies on this to migrate
// without re-keying persisted state, so a change here is a breaking change to
// every consumer, not an implementation detail.
func TestJoinIsByteIdenticalToNaiveConcatenation(t *testing.T) {
	cases := [][]string{
		{"streams", "u-42", "1234", "3", "5"}, // a persisted dedupe key
		{"github", "github.com"},              // a forge id
		{"pending", "c-1", "call-abc"},        // a virtual path
		{"tool", "ripgrep", "14.1.0", "ok"},
		{"a", "", "c"},
	}
	for _, parts := range cases {
		want := strings.Join(parts, string(Separator))
		if got := Join(parts...); got != want {
			t.Errorf("Join(%q) = %q, want the naive concatenation %q", parts, got, want)
		}
	}
}

// TestJoinDistinguishesShiftedSplits is the package's reason to exist: moving
// the separator from between two components into one of them must change the
// key. A naive concatenation makes these pairs identical, which is what
// silently merges two identities at a cache, a dedupe token or a map.
func TestJoinDistinguishesShiftedSplits(t *testing.T) {
	pairs := [][2][]string{
		{{"a:b", "c"}, {"a", "b:c"}},
		{{"a", "b", "c"}, {"a:b", "c"}},
		{{"a", "b", "c"}, {"a", "b:c"}},
		{{"files.rename", "dir/x->y", "z"}, {"files.rename", "dir/x", "y->z"}},
		{{"", "a"}, {"a", ""}},
		{{":"}, {"", ""}},
	}
	for _, p := range pairs {
		if left, right := Join(p[0]...), Join(p[1]...); left == right {
			t.Errorf("%q and %q share encoding %q", p[0], p[1], left)
		}
	}
}

// TestBoundaryAtMaxComponentBytes pins the size threshold: a set exactly at
// the bound keeps its escaped form, one raw byte over reduces to the hashed
// identity, and distinct oversized sets keep distinct identities.
func TestBoundaryAtMaxComponentBytes(t *testing.T) {
	atLimit := strings.Repeat("x", MaxComponentBytes)
	if got := Join(atLimit); got != atLimit {
		t.Errorf("Join at the limit produced %d bytes starting %.16q, want the escaped form", len(got), got)
	}
	over := Join(atLimit + "x")
	if !IsHashed(over) {
		t.Errorf("Join one byte over the limit = %.16q, want a hashed identity", over)
	}
	if other := Join(atLimit + "y"); other == over {
		t.Error("distinct oversized component sets must not share a hashed identity")
	}
	if again := Join(atLimit + "x"); again != over {
		t.Errorf("Join must be deterministic: %q vs %q", over, again)
	}
}

// TestBoundIsMeasuredOnRawSize pins that the threshold reads the RAW component
// sizes rather than the escaped output: an honest separator-heavy set whose
// escaped form is twice the bound still keeps its exact escaped
// representation, so a key's shape never flips because escaping grew it.
func TestBoundIsMeasuredOnRawSize(t *testing.T) {
	half := MaxComponentBytes / 2
	parts := []string{strings.Repeat(":", half), strings.Repeat(`\`, half)}
	got := Join(parts...)
	if IsHashed(got) {
		t.Fatal("a raw size at the bound must keep the escaped join even when escaping doubles it")
	}
	if len(got) <= MaxComponentBytes {
		t.Errorf("escaped form is %d bytes; the case is meant to exceed the bound after escaping", len(got))
	}
	over := []string{strings.Repeat(":", half), strings.Repeat(`\`, half+1)}
	if !IsHashed(Join(over...)) {
		t.Error("one raw byte over the bound must reduce to the hashed identity")
	}
}

// TestRawAndHashedDomainsAreDisjoint pins injectivity ACROSS the size
// boundary: a small component set that literally spells a hashed identity must
// not encode to that identity's bytes, or two distinct keys collide through
// the prefix.
func TestRawAndHashedDomainsAreDisjoint(t *testing.T) {
	forged := hashParts([]string{strings.Repeat("x", MaxComponentBytes+1)})
	if got := Join(forged); got == forged {
		t.Errorf("Join(%.20q...) returned the raw hashed-identity spelling; the domains must stay disjoint", forged)
	}
	// The natural two-component route to the same spelling.
	digest := strings.TrimPrefix(forged, hashedPrefix)
	if got := Join("sha256", digest); got == forged {
		t.Error(`Join("sha256", <digest>) must not encode to a hashed identity's bytes`)
	}
	// An honest component keeps its raw form.
	if got := Join("PMR", "x"); got != "PMR:x" {
		t.Errorf(`Join("PMR", "x") = %q, want the raw form`, got)
	}
}

// TestSingleEmptyDoesNotAliasNoComponents pins injectivity at the degenerate
// end: escaping preserves boundaries only once the join emits a byte, so the
// one-empty-component set must not share the zero-component set's encoding.
func TestSingleEmptyDoesNotAliasNoComponents(t *testing.T) {
	none := Join()
	if none != "" {
		t.Errorf("Join() = %q, want the empty encoding", none)
	}
	single := Join("")
	if single == none {
		t.Error(`Join("") must not share Join()'s encoding`)
	}
	if !IsHashed(single) {
		t.Errorf(`Join("") = %q, want a hashed identity`, single)
	}
	if Join("", "") == single {
		t.Error("the one- and two-empty-component sets must not share an encoding")
	}
}

// TestSplitErrors pins the two refusals. Both matter to a caller that parses
// keys back: a hashed key has no components to recover, and a dangling escape
// means the input did not come from Join.
func TestSplitErrors(t *testing.T) {
	t.Run("hashed", func(t *testing.T) {
		key := Join(strings.Repeat("x", MaxComponentBytes+1))
		if _, err := Split(key); !errors.Is(err, ErrHashed) {
			t.Errorf("Split(<hashed>) error = %v, want ErrHashed", err)
		}
	})
	t.Run("dangling escape", func(t *testing.T) {
		if _, err := Split(`a\`); !errors.Is(err, ErrMalformed) {
			t.Errorf(`Split("a\\") error = %v, want ErrMalformed`, err)
		}
	})
	t.Run("uppercase sha256 digest is malformed", func(t *testing.T) {
		// Join hashes any set whose escaped join would start with the prefix, so
		// an uppercase digest is in neither its hashed image (lowercase only) nor
		// its raw image, and matches IsHashed, which also rejects it.
		key := hashedPrefix + strings.Repeat("A", hashedHexLen)
		if _, err := Split(key); !errors.Is(err, ErrMalformed) {
			t.Errorf("Split(%q) error = %v, want ErrMalformed", key, err)
		}
	})
	t.Run("wrong-length sha256 digest is malformed", func(t *testing.T) {
		key := hashedPrefix + strings.Repeat("0", hashedHexLen-1)
		if _, err := Split(key); !errors.Is(err, ErrMalformed) {
			t.Errorf("Split(%q) error = %v, want ErrMalformed", key, err)
		}
	})
	t.Run("empty splits to nothing", func(t *testing.T) {
		got, err := Split("")
		if err != nil || got != nil {
			t.Errorf(`Split("") = %q, %v; want nil, nil`, got, err)
		}
	})
}

// TestIsHashedIsPrecise pins that IsHashed identifies the encoder's own output
// shape and nothing near it: a prefix with the wrong digest length or a
// non-hex body is a raw key that merely starts with the same word.
func TestIsHashedIsPrecise(t *testing.T) {
	cases := map[string]bool{
		hashParts([]string{"a"}):                           true,
		hashedPrefix + strings.Repeat("0", hashedHexLen):   true,
		hashedPrefix + strings.Repeat("0", hashedHexLen-1): false,
		hashedPrefix + strings.Repeat("0", hashedHexLen+1): false,
		hashedPrefix + strings.Repeat("g", hashedHexLen):   false,
		hashedPrefix + strings.Repeat("A", hashedHexLen):   false,
		"sha256": false,
		"":       false,
		`sha256\:` + strings.Repeat("0", hashedHexLen): false,
		"SHA256:" + strings.Repeat("0", hashedHexLen):  false,
	}
	for key, want := range cases {
		if got := IsHashed(key); got != want {
			t.Errorf("IsHashed(%.24q) = %v, want %v", key, got, want)
		}
	}
}

// TestNestingIsSafeAtEveryDepth pins the composition rule the package
// documents in place of a second reserved character: an inner join becomes one
// outer component, and a separator inside it cannot be read as an outer
// boundary. Three levels is the depth a two-separator grammar fails at.
func TestNestingIsSafeAtEveryDepth(t *testing.T) {
	// Two different groupings of the same leaves must not collide.
	leftInner := Join("a", "b")
	left := Join("x", leftInner, "y")
	right := Join("x", Join("a"), Join("b", "y"))
	if left == right {
		t.Errorf("distinct nestings share encoding %q", left)
	}
	// The inner sequence survives an outer round trip.
	outer, err := Split(left)
	if err != nil {
		t.Fatalf("Split outer: %v", err)
	}
	if len(outer) != 3 || outer[1] != leftInner {
		t.Fatalf("outer split = %q, want the inner join intact at index 1", outer)
	}
	inner, err := Split(outer[1])
	if err != nil {
		t.Fatalf("Split inner: %v", err)
	}
	if !slices.Equal(inner, []string{"a", "b"}) {
		t.Errorf("inner split = %q, want [a b]", inner)
	}
	// Depth three.
	deep := Join("l3", Join("l2", Join("l1", "leaf:with:colons")))
	l3, err := Split(deep)
	if err != nil {
		t.Fatalf("Split depth 3: %v", err)
	}
	l2, err := Split(l3[1])
	if err != nil {
		t.Fatalf("Split depth 2: %v", err)
	}
	l1, err := Split(l2[1])
	if err != nil {
		t.Fatalf("Split depth 1: %v", err)
	}
	if !slices.Equal(l1, []string{"l1", "leaf:with:colons"}) {
		t.Errorf("depth-3 leaf = %q, want the colons preserved", l1)
	}
}

// TestHashPreservesElementBoundaries pins the length prefix: merging two
// adjacent components must change the hash, so the hashed regime is as
// injective as the escaped one. A naive join-then-hash collapses exactly this.
func TestHashPreservesElementBoundaries(t *testing.T) {
	if hashParts([]string{"a", "b"}) == hashParts([]string{"ab"}) {
		t.Error(`hashParts collapsed the boundary between "a" and "b"`)
	}
	if hashParts([]string{"a:b"}) == hashParts([]string{"a", "b"}) {
		t.Error(`hashParts must distinguish ["a:b"] from ["a", "b"]`)
	}
	if hashParts(nil) == hashParts([]string{""}) {
		t.Error("hashParts must distinguish no components from one empty component")
	}
}
