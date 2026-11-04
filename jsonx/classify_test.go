package jsonx_test

import (
	"math"
	"runtime"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// TestClassify pins the syntactic core: shape taxonomy, fact flags, and
// exact values across the wire forms the three origin decoders see, plus
// the int64/float64 boundary cases the hardened conversion must get right.
func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want jsonx.Facts
	}{
		// Non-numeric shapes.
		{"empty input", ``, jsonx.Facts{Shape: jsonx.Empty}},
		{"whitespace only", " \t\r\n", jsonx.Facts{Shape: jsonx.Empty}},
		{"null", `null`, jsonx.Facts{Shape: jsonx.Null}},
		{"padded null", ` null `, jsonx.Facts{Shape: jsonx.Null}},
		{"empty string", `""`, jsonx.Facts{Shape: jsonx.EmptyString}},
		{"unterminated string", `"abc`, jsonx.Facts{Shape: jsonx.MalformedString}},
		{"invalid escape", `"\q"`, jsonx.Facts{Shape: jsonx.MalformedString}},
		{"string with trailing data", `"a" x`, jsonx.Facts{Shape: jsonx.MalformedString}},
		{"non-numeric string", `"unknown"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
		{"whitespace-only string", `" "`, jsonx.Facts{Shape: jsonx.NonNumericString, Padded: true}},
		{"padded garbage string", `" abc "`, jsonx.Facts{Shape: jsonx.NonNumericString, Padded: true}},
		{"object", `{}`, jsonx.Facts{Shape: jsonx.Other}},
		{"array", `[1]`, jsonx.Facts{Shape: jsonx.Other}},
		{"bool", `true`, jsonx.Facts{Shape: jsonx.Other}},
		{"garbage", `!!!`, jsonx.Facts{Shape: jsonx.Other}},
		{"lone minus", `-`, jsonx.Facts{Shape: jsonx.Other}},
		{"bare leading zeros", `007`, jsonx.Facts{Shape: jsonx.Other}},
		{"bare plus sign", `+5`, jsonx.Facts{Shape: jsonx.Other}},
		{"number with trailing data", `5 x`, jsonx.Facts{Shape: jsonx.Other}},

		// Hardened rejections: strconv would accept these, this package
		// deliberately does not.
		{"hex float string", `"0x1p2"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
		{"inf word string", `"Inf"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
		{"nan word string", `"NaN"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
		{"underscore digits string", `"1_000"`, jsonx.Facts{Shape: jsonx.NonNumericString}},

		// Bare numbers.
		{"bare int", `14`, jsonx.Facts{Shape: jsonx.Number, Value: 14}},
		{"bare zero", `0`, jsonx.Facts{Shape: jsonx.Number, Value: 0}},
		{"bare negative", `-3`, jsonx.Facts{Shape: jsonx.Number, Value: -3, Negative: true}},
		{"bare negative zero", `-0`, jsonx.Facts{Shape: jsonx.Number, Value: 0}},
		{"bare integral float", `9.0`, jsonx.Facts{Shape: jsonx.Number, Value: 9, FloatForm: true}},
		{"bare exponent", `1e3`, jsonx.Facts{Shape: jsonx.Number, Value: 1000, FloatForm: true}},
		{"bare integral via exponent", `50e-1`, jsonx.Facts{Shape: jsonx.Number, Value: 5, FloatForm: true}},
		{"bare fractional", `1.5`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},
		{"bare negative fractional", `-1.5`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true, Negative: true}},
		{"bare negative float zero", `-0.0`, jsonx.Facts{Shape: jsonx.Number, Value: 0, FloatForm: true}},
		{"bare float overflow", `1e999`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Overflow: true}},
		{"bare subnormal", `1e-310`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},

		// Exact-decimal classification: decided on digits, never through
		// float64. A full underflow is a nonzero value below one -
		// fractional - not the integral zero float64 collapses it to.
		{"bare full underflow", `1e-999`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},
		{"bare negative full underflow", `-1e-999`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true, Negative: true}},
		// 2^53+1 is the first integer binary64 cannot represent; the
		// digits path decodes it exactly instead of rounding to 2^53.
		{"bare float above 2^53", `9007199254740993.0`, jsonx.Facts{Shape: jsonx.Number, Value: 9007199254740993, FloatForm: true}},
		{"quoted float above 2^53", `"9007199254740993.0"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 9007199254740993, FloatForm: true}},
		{"bare fractional above 2^53", `9007199254740993.5`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},
		// A fractional offset far below float64's epsilon still counts.
		{"bare sub-epsilon fraction", `1.0000000000000000001`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},
		// Exponent normalization: exact value assembly and cancellation.
		{"bare max int64 via exponent", `9.223372036854775807e18`, jsonx.Facts{Shape: jsonx.Number, Value: math.MaxInt64, FloatForm: true}},
		{"bare exponent just past int64", `1e19`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Overflow: true}},
		// Adversarially long exponents saturate; work stays bounded.
		{"bare adversarial exponent", `1e999999999999999999999`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Overflow: true}},
		{"bare adversarial negative exponent", `1e-999999999999999999999`, jsonx.Facts{Shape: jsonx.Number, FloatForm: true, Fractional: true}},
		{"bare zero with huge exponent", `0e999999999999999999999`, jsonx.Facts{Shape: jsonx.Number, Value: 0, FloatForm: true}},
		{"bare max int64", `9223372036854775807`, jsonx.Facts{Shape: jsonx.Number, Value: math.MaxInt64}},
		{"bare min int64", `-9223372036854775808`, jsonx.Facts{Shape: jsonx.Number, Value: math.MinInt64, Negative: true}},
		{"bare int64 overflow", `9223372036854775808`, jsonx.Facts{Shape: jsonx.Number, Overflow: true}},
		{"bare negative int64 overflow", `-9223372036854775809`, jsonx.Facts{Shape: jsonx.Number, Overflow: true, Negative: true}},

		// Quoted numbers: decimal-relaxed grammar.
		{"quoted int", `"14"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 14}},
		{"quoted negative", `"-7"`, jsonx.Facts{Shape: jsonx.NumericString, Value: -7, Negative: true}},
		{"quoted negative zero", `"-0"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 0}},
		{"quoted leading zeros", `"007"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 7}},
		{"quoted plus sign", `"+5"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 5}},
		{"quoted padded int", `" 12 "`, jsonx.Facts{Shape: jsonx.NumericString, Value: 12, Padded: true}},
		{"quoted escaped digit", `"\u0035"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 5}},
		{"quoted integral float", `"9.0"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 9, FloatForm: true}},
		{"quoted exponent", `"1e3"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 1000, FloatForm: true}},
		{"quoted fractional", `"12.5"`, jsonx.Facts{Shape: jsonx.NumericString, FloatForm: true, Fractional: true}},
		{"quoted trailing dot", `"5."`, jsonx.Facts{Shape: jsonx.NumericString, Value: 5, FloatForm: true}},
		{"quoted leading dot", `".5"`, jsonx.Facts{Shape: jsonx.NumericString, FloatForm: true, Fractional: true}},
		{"quoted int32 overflow value", `"2147483648"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 2147483648}},
		{"quoted int64 overflow", `"9223372036854775808"`, jsonx.Facts{Shape: jsonx.NumericString, Overflow: true}},
		{"quoted float at 2^63", `"9223372036854775808.0"`, jsonx.Facts{Shape: jsonx.NumericString, FloatForm: true, Overflow: true}},
		// The digits path is exact at the int64 boundary: float64 would
		// round MaxInt64 up to 2^63, but the float FORM of MaxInt64 is a
		// representable value and decodes as itself.
		{"quoted float at max int64", `"9223372036854775807.0"`, jsonx.Facts{Shape: jsonx.NumericString, Value: math.MaxInt64, FloatForm: true}},
		{"quoted float at min int64", `"-9223372036854775808.0"`, jsonx.Facts{Shape: jsonx.NumericString, Value: math.MinInt64, FloatForm: true, Negative: true}},
		{"quoted float below min int64", `"-9223372036854775809.0"`, jsonx.Facts{Shape: jsonx.NumericString, FloatForm: true, Overflow: true, Negative: true}},
		{"quoted leading-zero float", `"007.5e2"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 750, FloatForm: true}},
		{"quoted signed positive exponent", `"1e+2"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 100, FloatForm: true}},
		{"quoted signed negative exponent", `"50e-1"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 5, FloatForm: true}},
		{"quoted uppercase exponent", `"5E3"`, jsonx.Facts{Shape: jsonx.NumericString, Value: 5000, FloatForm: true}},
		{"quoted trailing exponent marker", `"5e"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
		{"quoted exponent sign without digits", `"5e+"`, jsonx.Facts{Shape: jsonx.NonNumericString}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jsonx.Classify([]byte(tc.in))
			if got != tc.want {
				t.Errorf("Classify(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestFactsWasString pins the derived was-string fact across every shape.
func TestFactsWasString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{`"7"`, true},
		{`"x"`, true},
		{`""`, true},
		{`"broken`, true},
		{`7`, false},
		{`null`, false},
		{``, false},
		{`{}`, false},
	}
	for _, tc := range tests {
		if got := jsonx.Classify([]byte(tc.in)).WasString(); got != tc.want {
			t.Errorf("Classify(%q).WasString() = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestClassifyAdversarialExponentBoundedWork pins the documented
// bounded-work contract: a saturated adversarial exponent must classify
// without materializing the value's digit string. TotalAlloc is
// process-global, so this test stays serial (no t.Parallel).
func TestClassifyAdversarialExponentBoundedWork(t *testing.T) {
	in := []byte(`1e999999999999999999999`)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f := jsonx.Classify(in)
	runtime.ReadMemStats(&after)
	if !f.Overflow {
		t.Fatalf("Classify(%q) = %+v, want Overflow", in, f)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 1<<20 {
		t.Errorf("Classify(%q) allocated %d bytes, want bounded (< 1 MiB)", in, delta)
	}
}
