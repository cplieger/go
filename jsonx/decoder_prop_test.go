package jsonx_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
	"pgregory.net/rapid"
)

// TestPropPreflightCollisionMatchesEqualFold is the every-PR property behind
// Preflight's duplicate-key rule (the weekly fuzz corpus does not persist, so
// rapid is the durable net). The fold canonicalization inside Preflight is an
// optimization - it makes the per-object duplicate test O(keys) instead of
// the O(keys^2) an EqualFold scan would cost - so its whole contract is that
// it decides collisions exactly as strings.EqualFold does.
func TestPropPreflightCollisionMatchesEqualFold(t *testing.T) {
	t.Parallel()
	// Runes chosen to exercise the simple-fold orbits that make this
	// non-trivial: ASCII pairs, the two codepoints whose full-Unicode fold
	// reaches ASCII (U+017F long s -> 's', U+212A kelvin -> 'k'), a dotless i
	// that does NOT, and a non-ASCII pair with a real case mapping.
	runes := []rune{'a', 'A', 'k', 'K', 's', 'S', '0', '_', '\u017f', '\u0131', '\u0130', '\u212a', '\u00e9', '\u00c9'}
	keyGen := rapid.SliceOfN(rapid.SampledFrom(runes), 0, 6)
	rapid.Check(t, func(rt *rapid.T) {
		a := string(keyGen.Draw(rt, "a"))
		b := string(keyGen.Draw(rt, "b"))

		body := twoKeyObject(rt, a, b)
		err := jsonx.Preflight(strings.NewReader(body))
		collides := err != nil

		if want := strings.EqualFold(a, b); collides != want {
			rt.Fatalf("Preflight(%q) rejected = %v (%v), want strings.EqualFold(%q, %q) = %v",
				body, collides, err, a, b, want)
		}
	})
}

// twoKeyObject renders a JSON object carrying exactly the two given keys, with
// both key strings encoded by encoding/json so the property never depends on
// hand-rolled escaping.
func twoKeyObject(rt *rapid.T, a, b string) string {
	first, err := json.Marshal(map[string]int{a: 1})
	if err != nil {
		rt.Fatalf("marshal key %q: %v", a, err)
	}
	second, err := json.Marshal(map[string]int{b: 2})
	if err != nil {
		rt.Fatalf("marshal key %q: %v", b, err)
	}
	return string(first[:len(first)-1]) + "," + string(second[1:])
}

// TestPropPreflightNeverLooserThanValid pins the containment that makes
// Preflight safe to run AHEAD of a decode: anything it accepts is syntactically
// valid, whole-input JSON, so a body that passes the preflight and then fails
// to decode failed on the caller's schema, never on structure the preflight
// waved through. The generator mixes real JSON shapes with byte noise so both
// directions get exercised.
func TestPropPreflightNeverLooserThanValid(t *testing.T) {
	t.Parallel()
	shapes := []string{
		`null`, `1`, `"s"`, `{}`, `[]`,
		`{"a":1}`, `{"a":1,"A":2}`, `{"a":{"b":[1,2]}}`, `[{"a":1},{"a":2}]`,
		`{"big":1e1000}`, `{} {}`, `{"a":1,`, `[[[`, `tru`, `{"a":1}x`,
	}
	gen := rapid.OneOf(
		rapid.SampledFrom(shapes),
		rapid.StringOfN(rapid.SampledFrom([]rune(`{}[]",:0aA \n`)), 0, 24, -1),
	)
	rapid.Check(t, func(rt *rapid.T) {
		body := gen.Draw(rt, "body")
		if jsonx.Preflight(strings.NewReader(body)) != nil {
			return
		}
		if !json.Valid([]byte(body)) {
			rt.Fatalf("Preflight accepted %q but json.Valid rejects it", body)
		}
	})
}
