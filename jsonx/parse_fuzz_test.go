package jsonx_test

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// reporter is the failure-reporting surface shared by *testing.T and
// *rapid.T, so the invariant helpers below serve both the fuzz targets
// and the rapid properties.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// seedCorpus is the union of wire shapes the three origin decoders see,
// plus the adversarial forms the hardening targets. Shared by the fuzz
// seeds and the rapid input generator.
var seedCorpus = []string{
	`14`, `"14"`, `0`, `-0`, `-3`, `"-3"`, `null`, `""`, ``, ` `,
	`"abc"`, `"unknown"`, `1.5`, `"1.5"`, `9.0`, `"9.0"`, `1e3`, `"1e3"`,
	`50e-1`, `{}`, `[1]`, `true`, `"unterminated`, `"\q"`, `"\u0035"`,
	`2147483647`, `2147483648`, `"2147483648"`, `9223372036854775807`,
	`-9223372036854775808`, `9223372036854775808`, `" 12 "`, `"007"`,
	`"+5"`, `"0x1p2"`, `"NaN"`, `"Inf"`, `"1_000"`, `"5."`, `".5"`,
	`"1e-999"`, `"1e999"`, `007`, `+5`, `5 x`, `!!!`,
	`9007199254740993.0`, `"9007199254740993.0"`, `9007199254740993.5`,
	`9.223372036854775807e18`, `1.0000000000000000001`, `-1e-999`,
	`"9223372036854775807.0"`, `1e19`, `0e999999999999999999999`,
	`1e999999999999999999999`, `"007.5e2"`, `"1e+2"`, `"50e-1"`, `"5E3"`,
	`"5e"`, `"5e+"`, `1e+2`,
}

// checkFactsConsistency asserts the internal coherence of a
// classification, for any input.
func checkFactsConsistency(t reporter, data []byte, f jsonx.Facts) {
	t.Helper()
	numeric := f.Shape == jsonx.ShapeNumber || f.Shape == jsonx.ShapeNumericString
	if !numeric && (f.Value != 0 || f.FloatForm || f.Fractional || f.Overflow || f.Negative) {
		t.Errorf("Classify(%q): non-numeric shape %d carries numeric facts: %+v", data, f.Shape, f)
	}
	if f.Fractional && !f.FloatForm {
		t.Errorf("Classify(%q): Fractional without FloatForm: %+v", data, f)
	}
	if f.Padded && f.Shape != jsonx.ShapeNumericString && f.Shape != jsonx.ShapeNonNumericString {
		t.Errorf("Classify(%q): Padded on shape %d: %+v", data, f.Shape, f)
	}
	if (f.Overflow || f.Fractional) && f.Value != 0 {
		t.Errorf("Classify(%q): value set despite overflow/fractional: %+v", data, f)
	}
	if numeric && !f.Overflow && !f.Fractional && f.Negative != (f.Value < 0) {
		t.Errorf("Classify(%q): Negative=%v inconsistent with Value=%d", data, f.Negative, f.Value)
	}
}

// checkFloatFormExactness cross-checks a float-form classification
// against math/big.Rat, which parses decimal literals exactly: the
// integral/fractional/overflow decision, the exact Value, and the sign
// must all agree with the rational oracle. FloatForm AND integral
// implies Value is the exact decimal value. Inputs whose exponent body
// runs past four digits are skipped: the oracle materializes 10^|exp|,
// which is the unbounded work the production classifier avoids by
// saturating.
func checkFloatFormExactness(t reporter, data []byte, f jsonx.Facts) {
	t.Helper()
	if !f.FloatForm {
		return
	}
	lit, ok := numericLiteral(data, f)
	if !ok || longExponent(lit) {
		return
	}
	r, ok := new(big.Rat).SetString(lit)
	if !ok {
		t.Errorf("Classify(%q): float-form literal %q rejected by the big.Rat oracle", data, lit)
		return
	}
	switch {
	case f.Fractional:
		if r.IsInt() {
			t.Errorf("Classify(%q): Fractional, but %q is the integer %v", data, lit, r.Num())
		}
	case f.Overflow:
		if !r.IsInt() {
			t.Errorf("Classify(%q): Overflow, but %q is fractional", data, lit)
		} else if r.Num().IsInt64() {
			t.Errorf("Classify(%q): Overflow, but %q = %v fits int64", data, lit, r.Num())
		}
	default:
		if !r.IsInt() {
			t.Errorf("Classify(%q): claimed integral, but %q is fractional", data, lit)
		} else if !r.Num().IsInt64() {
			t.Errorf("Classify(%q): Value=%d, but %q overflows int64", data, f.Value, lit)
		} else if got := r.Num().Int64(); got != f.Value {
			t.Errorf("Classify(%q): Value=%d, oracle says %d", data, f.Value, got)
		}
	}
	if (r.Sign() < 0) != f.Negative {
		t.Errorf("Classify(%q): Negative=%v, oracle sign %d", data, f.Negative, r.Sign())
	}
}

// numericLiteral recovers the decimal literal Classify judged: the
// trimmed raw token for a bare number, the trimmed string content for a
// quoted one.
func numericLiteral(data []byte, f jsonx.Facts) (string, bool) {
	trimmed := bytes.Trim(data, " \t\r\n")
	if f.Shape == jsonx.ShapeNumber {
		return string(trimmed), true
	}
	var s string
	if json.Unmarshal(trimmed, &s) != nil {
		return "", false
	}
	return strings.Trim(s, " \t\r\n"), true
}

// longExponent reports an exponent body longer than four digits (see
// checkFloatFormExactness).
func longExponent(lit string) bool {
	i := strings.IndexAny(lit, "eE")
	if i < 0 {
		return false
	}
	body := lit[i+1:]
	if len(body) > 0 && (body[0] == '+' || body[0] == '-') {
		body = body[1:]
	}
	return len(body) > 4
}

// checkCrossPolicyInvariants asserts the relationships between the three
// shipped policies, for any input:
//
//  1. TolerantZero errors only on a malformed string, and its result
//     always stays inside [0, math.MaxInt32].
//  2. StrictAbsentZero agrees with Strict on every input Strict accepts,
//     and accepts beyond Strict only zero-length input.
//  3. Strict and TolerantZero never disagree about a nonzero value: an
//     input both accept as nonzero yields the same integer (no
//     truncation or re-interpretation divergence), and any nonzero
//     Strict-accepted value within tolerant range is accepted
//     identically by TolerantZero.
func checkCrossPolicyInvariants(t reporter, data []byte) {
	t.Helper()
	facts := jsonx.Classify(data)
	checkFactsConsistency(t, data, facts)
	checkFloatFormExactness(t, data, facts)

	tv, terr := jsonx.ParseInt64(data, jsonx.TolerantZero())
	sv, serr := jsonx.ParseInt64(data, jsonx.Strict())
	av, aerr := jsonx.ParseInt64(data, jsonx.StrictAbsentZero())

	// (1) Tolerant-zero soundness.
	if terr != nil && facts.Shape != jsonx.ShapeMalformedString {
		t.Errorf("TolerantZero(%q) errored on non-malformed input: %v", data, terr)
	}
	if terr == nil && (tv < 0 || tv > math.MaxInt32) {
		t.Errorf("TolerantZero(%q) = %d, outside [0, MaxInt32]", data, tv)
	}

	// (2) StrictAbsentZero is Strict plus empty-input tolerance.
	if serr == nil && (aerr != nil || av != sv) {
		t.Errorf("StrictAbsentZero(%q) = %d, %v diverges from Strict = %d", data, av, aerr, sv)
	}
	if aerr == nil && serr != nil && facts.Shape != jsonx.ShapeEmpty {
		t.Errorf("StrictAbsentZero(%q) accepted beyond Strict on non-empty input", data)
	}

	// (3) Strict/tolerant agreement on nonzero values.
	if serr == nil && terr == nil && sv != 0 && tv != 0 && sv != tv {
		t.Errorf("policies disagree on %q: strict %d vs tolerant %d", data, sv, tv)
	}
	if serr == nil && sv > 0 && sv <= math.MaxInt32 && (terr != nil || tv != sv) {
		t.Errorf("TolerantZero(%q) = %d, %v, want %d (a strict-accepted in-range value)", data, tv, terr, sv)
	}
}

// checkFormsEquivalence asserts the metamorphic dual-shape invariant for
// one integer under every shipped policy: the canonical bare-number form
// and its quoted form decode identically (same value, same error-ness).
// This generalizes seadex-scout's FuzzParseFribb_numericIDFormsEquivalent
// and plex-language-sync's FuzzFlexIntUnmarshal invariants.
func checkFormsEquivalence(t reporter, n int64) {
	t.Helper()
	bare := strconv.FormatInt(n, 10)
	quoted := `"` + bare + `"`
	for name, p := range map[string]jsonx.Policy{
		"TolerantZero":     jsonx.TolerantZero(),
		"Strict":           jsonx.Strict(),
		"StrictAbsentZero": jsonx.StrictAbsentZero(),
	} {
		bv, berr := jsonx.ParseInt64([]byte(bare), p)
		qv, qerr := jsonx.ParseInt64([]byte(quoted), p)
		if bv != qv || (berr == nil) != (qerr == nil) {
			t.Errorf("%s: forms diverge for %d: bare (%d, %v) vs quoted (%d, %v)",
				name, n, bv, berr, qv, qerr)
		}
	}
}

// FuzzParseInt64CrossPolicy drives Classify and all three shipped
// policies with arbitrary bytes, asserting facts coherence and the
// cross-policy invariants documented on checkCrossPolicyInvariants.
func FuzzParseInt64CrossPolicy(f *testing.F) {
	for _, s := range seedCorpus {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkCrossPolicyInvariants(t, data)
	})
}

// FuzzNumericFormsEquivalent pins the number-vs-string metamorphic
// equivalence for every int64 under every shipped policy.
func FuzzNumericFormsEquivalent(f *testing.F) {
	for _, n := range []int64{0, 1, -1, 9, 2147483647, 2147483648, -2147483648, math.MaxInt64, math.MinInt64} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, n int64) {
		checkFormsEquivalence(t, n)
	})
}
