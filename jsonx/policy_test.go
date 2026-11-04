package jsonx_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// parseCase is one policy-table row: input bytes, expected value, and
// whether an error is expected.
type parseCase struct {
	name    string
	in      string
	want    int64
	wantErr bool
}

// runPolicyTable asserts ParseInt64 outcomes for one policy.
func runPolicyTable(t *testing.T, p jsonx.Policy, cases []parseCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := jsonx.ParseInt64([]byte(tc.in), p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseInt64(%q) error = nil, want error", tc.in)
				}
				if _, ok := errors.AsType[*jsonx.ParseError](err); !ok {
					t.Fatalf("ParseInt64(%q) error = %T, want *jsonx.ParseError", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInt64(%q) error = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseInt64(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestTolerantZero replicates the seadex-scout flexInt acceptance spec
// (fribb.go setNumber invariants, fribb_tolerance_test.go pins, and the
// task-pinned equivalences "9.0" -> 9, "1e3" -> 1000, "1.5" -> 0): every
// odd shape or invalid value is 0, only a malformed string errors, and
// the range is [0, math.MaxInt32].
func TestTolerantZero(t *testing.T) {
	t.Parallel()
	runPolicyTable(t, jsonx.TolerantZero(), []parseCase{
		{"bare int", `14`, 14, false},
		{"quoted int", `"14"`, 14, false},
		{"zero", `0`, 0, false},
		{"null", `null`, 0, false},
		{"empty input", ``, 0, false},
		{"empty string", `""`, 0, false},
		{"placeholder string", `"unknown"`, 0, false},
		{"quoted integral float", `"9.0"`, 9, false},
		{"quoted exponent", `"1e3"`, 1000, false},
		{"quoted fractional", `"1.5"`, 0, false},
		{"quoted fractional 12.5", `"12.5"`, 0, false},
		{"bare integral float", `9.0`, 9, false},
		{"bare exponent", `1e3`, 1000, false},
		{"bare fractional", `1.5`, 0, false},
		{"bare fractional 9.9", `9.9`, 0, false},
		{"negative bare", `-5`, 0, false},
		{"negative quoted", `"-5"`, 0, false},
		{"negative zero", `-0`, 0, false},
		{"max int32", `2147483647`, math.MaxInt32, false},
		{"int32 overflow bare", `2147483648`, 0, false},
		{"int32 overflow quoted", `"2147483648"`, 0, false},
		{"int64 overflow", `9223372036854775808`, 0, false},
		{"float overflow", `1e999`, 0, false},
		{"object", `{}`, 0, false},
		{"array", `[1]`, 0, false},
		{"bool", `true`, 0, false},
		{"garbage", `!!!`, 0, false},
		{"padded quoted int", `" 12 "`, 12, false},
		{"quoted leading zeros", `"007"`, 7, false},
		{"nan word", `"NaN"`, 0, false},
		{"inf word", `"Inf"`, 0, false},
		{"hex float string", `"0x1p2"`, 0, false},
		{"malformed string errors", `"unterminated`, 0, true},
		{"invalid escape errors", `"\q"`, 0, true},
	})
}

// TestStrict replicates subflux's provider.ParseFlexInt acceptance spec
// (flexint_test.go TestParseFlexInt verbatim, plus the error paths its
// callers rely on): bare or quoted decimal integers anywhere in int64,
// null and "" as 0, everything else an error.
func TestStrict(t *testing.T) {
	t.Parallel()
	runPolicyTable(t, jsonx.Strict(), []parseCase{
		{"bare_number", `42`, 42, false},
		{"negative_number", `-5`, -5, false},
		{"quoted_number", `"123"`, 123, false},
		{"quoted_negative", `"-7"`, -7, false},
		{"empty_string_is_zero", `""`, 0, false},
		{"json_null_is_zero", `null`, 0, false},
		{"non_numeric_string", `"abc"`, 0, true},
		{"json_object", `{}`, 0, true},
		{"empty input rejected", ``, 0, true},
		{"bare fractional", `1.5`, 0, true},
		{"quoted fractional", `"1.5"`, 0, true},
		{"integral float form", `9.0`, 0, true},
		{"quoted integral float form", `"9.0"`, 0, true},
		{"exponent form", `1e3`, 0, true},
		{"quoted exponent form", `"1e3"`, 0, true},
		{"padded quoted int", `" 12 "`, 0, true},
		{"malformed string", `"unterminated`, 0, true},
		{"array", `[1]`, 0, true},
		{"bool", `true`, 0, true},
		{"max int64", `9223372036854775807`, math.MaxInt64, false},
		{"min int64", `-9223372036854775808`, math.MinInt64, false},
		{"int64 overflow", `9223372036854775808`, 0, true},
		{"quoted int64 overflow", `"9223372036854775808"`, 0, true},
		{"quoted leading zeros", `"007"`, 7, false},
		{"quoted plus sign", `"+5"`, 5, false},
		{"big id above int32", `2147483648`, 2147483648, false},
	})
}

// TestStrictAbsentZero replicates plex-language-sync's FlexInt acceptance
// spec (flex_test.go TestFlexInt_UnmarshalJSON and
// TestFlexInt_UnmarshalJSON_EmptyInput verbatim): Strict semantics with
// zero-length input decoding to 0.
func TestStrictAbsentZero(t *testing.T) {
	t.Parallel()
	runPolicyTable(t, jsonx.StrictAbsentZero(), []parseCase{
		{"bare number", `14`, 14, false},
		{"quoted number", `"14"`, 14, false},
		{"bare zero", `0`, 0, false},
		{"quoted zero", `"0"`, 0, false},
		{"negative bare", `-3`, -3, false},
		{"negative quoted", `"-3"`, -3, false},
		{"null", `null`, 0, false},
		{"empty quoted", `""`, 0, false},
		{"malformed quoted", `"abc"`, 0, true},
		{"float bare", `1.5`, 0, true},
		{"object", `{}`, 0, true},
		{"array", `[1]`, 0, true},
		{"empty input", ``, 0, false},
		{"whitespace-only input", " \t", 0, false},
		{"unterminated string", `"abc`, 0, true},
		{"invalid escape", `"\q"`, 0, true},
		{"lone escaped quote", `"\"`, 0, true},
	})
}

// TestStrictAbsentZeroDiffersOnlyOnEmptyInput is the drift guard: the two
// strict policies must stay identical in every field except EmptyInput.
func TestStrictAbsentZeroDiffersOnlyOnEmptyInput(t *testing.T) {
	t.Parallel()
	s, a := jsonx.Strict(), jsonx.StrictAbsentZero()
	if s.EmptyInput != jsonx.Reject || a.EmptyInput != jsonx.Zero {
		t.Errorf("EmptyInput dispositions = %v, %v, want Reject, Zero", s.EmptyInput, a.EmptyInput)
	}
	s.EmptyInput = jsonx.Zero
	if !reflect.DeepEqual(s, a) {
		t.Errorf("policies diverge beyond EmptyInput:\nStrict           = %+v\nStrictAbsentZero = %+v", s, a)
	}
}

// TestPolicyCompositionHDBits shows subflux's hdbits wrapper (reject null,
// reject non-positive ids) as a pure policy: Strict with null/"" rejected
// and MinValue 1, pinning the wrapper's outcomes (filter.go flexInt).
func TestPolicyCompositionHDBits(t *testing.T) {
	t.Parallel()
	p := jsonx.Strict()
	p.Null = jsonx.Reject
	p.EmptyString = jsonx.Reject
	p.MinValue = 1
	runPolicyTable(t, p, []parseCase{
		{"valid id", `42`, 42, false},
		{"quoted id", `"123"`, 123, false},
		{"null rejected", `null`, 0, true},
		{"empty string rejected", `""`, 0, true},
		{"zero rejected", `0`, 0, true},
		{"negative rejected", `-3`, 0, true},
		{"non-numeric rejected", `"abc"`, 0, true},
	})
}

// TestPolicyCompositionSubsource shows subflux's subsource wrapper
// (lenient: any parse error defaults to 0, year=0 means unknown) as a
// pure policy: every gate Zero, full int64 range.
func TestPolicyCompositionSubsource(t *testing.T) {
	t.Parallel()
	p := jsonx.Policy{
		MinValue:         math.MinInt64,
		MaxValue:         math.MaxInt64,
		Null:             jsonx.Zero,
		EmptyInput:       jsonx.Zero,
		EmptyString:      jsonx.Zero,
		MalformedString:  jsonx.Zero,
		NonNumericString: jsonx.Zero,
		OtherShape:       jsonx.Zero,
		PaddedString:     jsonx.Zero,
		FloatForm:        jsonx.Zero,
		Fractional:       jsonx.Zero,
		OutOfRange:       jsonx.Zero,
	}
	runPolicyTable(t, p, []parseCase{
		{"bare year", `2008`, 2008, false},
		{"quoted year", `"2008"`, 2008, false},
		{"negative kept", `-7`, -7, false},
		{"garbage zeroed", `"abc"`, 0, false},
		{"object zeroed", `{}`, 0, false},
		{"float zeroed", `1.5`, 0, false},
		{"malformed string zeroed", `"broken`, 0, false},
		{"padded zeroed", `" 7 "`, 0, false},
	})
}

// TestZeroValuePolicyFailsClosed pins the fail-closed contract: an unset
// Policy rejects every fact and every value outside its zero range [0, 0].
func TestZeroValuePolicyFailsClosed(t *testing.T) {
	t.Parallel()
	runPolicyTable(t, jsonx.Policy{}, []parseCase{
		{"nonzero value rejected", `5`, 0, true},
		{"null rejected", `null`, 0, true},
		{"string rejected", `"x"`, 0, true},
		{"literal zero accepted", `0`, 0, false},
	})
}

// TestAcceptOnValuelessGateFailsClosed pins the Disposition contract:
// Accept on a gate that carries no usable value (here Null and
// Fractional) is treated as Reject, never as silent acceptance.
func TestAcceptOnValuelessGateFailsClosed(t *testing.T) {
	t.Parallel()
	p := jsonx.TolerantZero()
	p.Null = jsonx.Accept
	p.Fractional = jsonx.Accept
	if _, err := jsonx.ParseInt64([]byte(`null`), p); err == nil {
		t.Error("ParseInt64(null) with Null: Accept = nil error, want reject (fail closed)")
	}
	if _, err := jsonx.ParseInt64([]byte(`1.5`), p); err == nil {
		t.Error("ParseInt64(1.5) with Fractional: Accept = nil error, want reject (no truncation)")
	}
}

// TestParseErrorTaxonomy pins the typed-error surface: Reason values per
// gate, Facts carried through, and the bounded snippet.
func TestParseErrorTaxonomy(t *testing.T) {
	t.Parallel()
	strict := jsonx.Strict()
	rejectAbsent := jsonx.Strict()
	rejectAbsent.Null = jsonx.Reject
	rejectAbsent.EmptyString = jsonx.Reject
	tests := []struct {
		name   string
		in     string
		p      jsonx.Policy
		reason jsonx.Reason
	}{
		{"empty input", ``, strict, jsonx.ReasonEmptyInput},
		{"malformed string", `"broken`, strict, jsonx.ReasonMalformedString},
		{"non-numeric string", `"abc"`, strict, jsonx.ReasonNonNumericString},
		{"other shape", `{}`, strict, jsonx.ReasonOtherShape},
		{"padded string", `" 7 "`, strict, jsonx.ReasonPaddedString},
		{"float form", `"9.0"`, strict, jsonx.ReasonFloatForm},
		{"out of range", `9223372036854775808`, strict, jsonx.ReasonOutOfRange},
		{"null rejected", `null`, rejectAbsent, jsonx.ReasonNull},
		{"empty string rejected", `""`, rejectAbsent, jsonx.ReasonEmptyString},
		{"malformed under tolerant", `"broken`, jsonx.TolerantZero(), jsonx.ReasonMalformedString},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonx.ParseInt64([]byte(tc.in), tc.p)
			perr, ok := errors.AsType[*jsonx.ParseError](err)
			if !ok {
				t.Fatalf("ParseInt64(%q) error = %v, want *jsonx.ParseError", tc.in, err)
			}
			if perr.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", perr.Reason, tc.reason)
			}
			if !strings.HasPrefix(perr.Error(), "jsonx: "+string(tc.reason)) {
				t.Errorf("Error() = %q, want prefix %q", perr.Error(), "jsonx: "+string(tc.reason))
			}
		})
	}
}

// TestParseErrorFractionalReason needs FloatForm accepted so the
// fractional gate is reachable; TolerantZero with Fractional switched to
// Reject exposes it.
func TestParseErrorFractionalReason(t *testing.T) {
	t.Parallel()
	p := jsonx.TolerantZero()
	p.Fractional = jsonx.Reject
	_, err := jsonx.ParseInt64([]byte(`1.5`), p)
	perr, ok := errors.AsType[*jsonx.ParseError](err)
	if !ok || perr.Reason != jsonx.ReasonFractional {
		t.Fatalf("ParseInt64(1.5) error = %v, want ReasonFractional", err)
	}
	if !perr.Facts.Fractional || !perr.Facts.FloatForm {
		t.Errorf("Facts = %+v, want Fractional and FloatForm set", perr.Facts)
	}
}

// TestParseErrorSnippetBounded pins the snippet cap: a huge offending
// token embeds a bounded excerpt, not the whole payload.
func TestParseErrorSnippetBounded(t *testing.T) {
	t.Parallel()
	huge := `"` + strings.Repeat("x", 5000)
	_, err := jsonx.ParseInt64([]byte(huge), jsonx.Strict())
	perr, ok := errors.AsType[*jsonx.ParseError](err)
	if !ok {
		t.Fatalf("error = %v, want *jsonx.ParseError", err)
	}
	if len(perr.Snippet) > 64 {
		t.Errorf("Snippet length = %d, want bounded (cap + ellipsis)", len(perr.Snippet))
	}
	if !strings.HasSuffix(perr.Snippet, "...") {
		t.Errorf("Snippet = %q, want trailing ellipsis on truncation", perr.Snippet)
	}
}

// TestParseErrorSnippetCapBoundary pins the snippet cap's exact boundary:
// a token of exactly snippetCap bytes embeds whole with no ellipsis, one
// byte more truncates to the cap plus "...".
func TestParseErrorSnippetCapBoundary(t *testing.T) {
	t.Parallel()
	atCap := `"` + strings.Repeat("x", 39) // 40 bytes, unterminated string
	_, err := jsonx.ParseInt64([]byte(atCap), jsonx.Strict())
	perr, ok := errors.AsType[*jsonx.ParseError](err)
	if !ok {
		t.Fatalf("error = %v, want *jsonx.ParseError", err)
	}
	if perr.Snippet != atCap {
		t.Errorf("Snippet at cap = %q, want the whole %d-byte token", perr.Snippet, len(atCap))
	}
	overCap := atCap + "x"
	_, err = jsonx.ParseInt64([]byte(overCap), jsonx.Strict())
	perr, ok = errors.AsType[*jsonx.ParseError](err)
	if !ok {
		t.Fatalf("error = %v, want *jsonx.ParseError", err)
	}
	if got, want := perr.Snippet, overCap[:40]+"..."; got != want {
		t.Errorf("Snippet over cap = %q, want %q", got, want)
	}
}
