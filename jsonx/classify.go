package jsonx

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Shape classifies the JSON value handed to Classify. It is the coarse,
// policy-independent taxonomy a Policy dispatches on.
type Shape uint8

const (
	// Other is any token that is not a JSON number, string, or null: an
	// object, an array, a boolean, or a syntactically invalid token (for
	// example a number with a leading zero, which the JSON grammar
	// forbids). It is the zero value, so an unset Shape reads as the
	// least-trusted classification.
	Other Shape = iota
	// Null is the JSON null literal.
	Null
	// Empty is zero-length input (nothing but JSON whitespace). A real
	// encoding/json decode never produces it - decoders hand
	// UnmarshalJSON exact value bytes - but direct callers emulating the
	// "absent field" case do.
	Empty
	// EmptyString is exactly `""`.
	EmptyString
	// MalformedString is a token that starts with '"' but is not a valid
	// JSON string (unterminated, invalid escape, or trailing data).
	MalformedString
	// NonNumericString is a valid JSON string whose content (after ASCII
	// whitespace trimming) is not a decimal number: "unknown", "", "abc",
	// but also forms this package deliberately rejects even though some
	// strconv parsers accept them, such as hex floats ("0x1p2"),
	// "Inf"/"NaN" words, and digit-separator underscores.
	NonNumericString
	// Number is a bare, grammar-valid JSON number token.
	Number
	// NumericString is a valid JSON string whose content is a decimal
	// number. The string grammar is deliberately looser than JSON's own
	// number grammar - a leading '+' and leading zeros are accepted -
	// because quoted numbers are exactly where upstream serializers emit
	// such forms ("007", "+5") and the origin decoders accepted them.
	NumericString
)

// Facts records what Classify observed about one JSON value: the parsed
// integer (when there is one) plus the syntactic facts a Policy judges.
// Facts are policy-free; whether a fact is acceptable, tolerable, or fatal
// is the Policy's decision.
type Facts struct {
	// Value is the exactly-parsed integer. It is nonzero only when Shape
	// is Number or NumericString and the value is integral and fits
	// int64 (neither Fractional nor Overflow is set).
	Value int64
	// Shape is the coarse classification of the token.
	Shape Shape
	// FloatForm reports that the literal used float syntax: a decimal
	// point or an exponent ("9.0", "1e3", 1.5). The value may still be
	// integral - FloatForm is about the wire form, Fractional about the
	// value.
	FloatForm bool
	// Fractional reports that the numeric value is not integral (1.5,
	// "12.5", a full underflow like 1e-999). The decision is made on
	// the decimal digits, not a float64 approximation, so values a
	// float64 would round to an integer still classify as fractional.
	// Value stays 0: this package never truncates.
	Fractional bool
	// Negative reports a negative value. For Overflow tokens, whose
	// exact value is unknown, it reflects the literal's sign; for parsed
	// values it is Value < 0 (so "-0" is not negative).
	Negative bool
	// Overflow reports a numeric magnitude that does not fit int64.
	// Value stays 0.
	Overflow bool
	// Padded reports that the string content carried surrounding ASCII
	// whitespace (" 12 "). Only NumericString and NonNumericString
	// shapes can be padded.
	Padded bool
}

// WasString reports whether the value arrived quoted, i.e. as a JSON
// string in any of its shapes (numeric, non-numeric, empty, malformed).
func (f Facts) WasString() bool {
	switch f.Shape {
	case EmptyString, MalformedString, NonNumericString, NumericString:
		return true
	default:
		return false
	}
}

// jsonWhitespace is the JSON grammar's insignificant-whitespace set. Raw
// input and string content are trimmed against exactly this set - never
// the full Unicode space classes - so an NBSP-padded token stays garbage
// instead of silently parsing.
const jsonWhitespace = " \t\r\n"

// Classify inspects the raw bytes of one JSON value and returns its
// syntactic Facts. It is total: any input, including garbage, yields a
// classification and never a panic or an error. Callers wanting a
// policied integer use ParseInt64; Classify is the escape hatch for
// consumers composing their own logic on the facts.
func Classify(data []byte) Facts {
	data = bytes.Trim(data, jsonWhitespace)
	if len(data) == 0 {
		return Facts{Shape: Empty}
	}
	switch {
	case data[0] == '"':
		return classifyString(data)
	case string(data) == "null":
		return Facts{Shape: Null}
	case data[0] == '-' || isDigit(data[0]):
		// Only a number token can start with '-' or a digit; enforce
		// the exact JSON number grammar (json.Valid rejects leading
		// zeros, a lone '-', and trailing data).
		if !json.Valid(data) {
			return Facts{Shape: Other}
		}
		return classifyNumber(string(data), Number, false)
	default:
		return Facts{Shape: Other}
	}
}

// classifyString decodes a quoted token and classifies its content.
func classifyString(data []byte) Facts {
	var s string
	if json.Unmarshal(data, &s) != nil {
		return Facts{Shape: MalformedString}
	}
	if s == "" {
		return Facts{Shape: EmptyString}
	}
	content := strings.Trim(s, jsonWhitespace)
	padded := content != s
	if content == "" || !decimalNumberForm(content) {
		return Facts{Shape: NonNumericString, Padded: padded}
	}
	return classifyNumber(content, NumericString, padded)
}

// classifyNumber parses a grammar-validated numeric literal into Facts,
// routing float forms through the exact decimal classifier and integer
// forms through strconv.ParseInt. Both paths are exact across the whole
// int64 range - no float64 round trip anywhere that could corrupt large
// ids.
func classifyNumber(lit string, shape Shape, padded bool) Facts {
	facts := Facts{Shape: shape, Padded: padded}
	if strings.ContainsAny(lit, ".eE") {
		facts.FloatForm = true
		classifyFloatValue(lit, &facts)
		return facts
	}
	v, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		// Grammar-valid digits that exceed int64: magnitude overflow.
		facts.Overflow = true
		facts.Negative = lit[0] == '-'
		return facts
	}
	facts.Value = v
	facts.Negative = v < 0
	return facts
}

// maxInt64Digits is the decimal digit count of math.MaxInt64
// (9223372036854775807). An integer with more significant digits
// overflows int64 outright; at exactly this count strconv.ParseInt
// decides the boundary precisely.
const maxInt64Digits = 19

// classifyFloatValue fills facts for a grammar-validated float-form
// literal. The decision is made on the decimal digits themselves - the
// literal is never converted through float64, whose 53-bit significand
// silently rounds integers above 2^53 (9007199254740993.0 would decode
// as ...992) and collapses full underflows ("1e-999") to an integral
// zero. Work is bounded by the input length: adversarially long
// exponents saturate, and a value is materialized for parsing only once
// known to fit maxInt64Digits.
func classifyFloatValue(lit string, facts *Facts) {
	sig, exp, neg := splitDecimal(lit)
	if sig == "" {
		// All-zero significand: exactly zero whatever the exponent
		// ("0.0", "-0.0", "0e999999"). Zero is not negative.
		return
	}
	if exp < 0 {
		// A nonzero digit remains below the decimal point: the value
		// is not integral. Decided from digits, so full underflows
		// ("1e-999") and sub-epsilon offsets ("1.0000000000000000001")
		// classify as fractional instead of rounding to an integer.
		facts.Fractional = true
		facts.Negative = neg
		return
	}
	if len(sig)+exp > maxInt64Digits {
		facts.Overflow = true
		facts.Negative = neg
		return
	}
	num := sig + strings.Repeat("0", exp)
	if neg {
		num = "-" + num
	}
	v, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		// Nineteen digits can still exceed the boundary; ParseInt owns
		// the exact int64 range check (accepting -9223372036854775808
		// while rejecting 9223372036854775808).
		facts.Overflow = true
		facts.Negative = neg
		return
	}
	facts.Value = v
	facts.Negative = v < 0
}

// splitDecimal decomposes a grammar-validated decimal literal into its
// minimal exact form: the value is sig x 10^exp, negated when neg is
// set, where sig is the significand's digits stripped of leading and
// trailing zeros. A literal whose digits are all zero yields sig == "".
func splitDecimal(lit string) (sig string, exp int, neg bool) {
	i := 0
	if lit[i] == '+' || lit[i] == '-' {
		neg = lit[i] == '-'
		i++
	}
	start := i
	i, _ = scanDigits(lit, i)
	digits := lit[start:i]
	if i < len(lit) && lit[i] == '.' {
		start = i + 1
		i, _ = scanDigits(lit, start)
		frac := lit[start:i]
		exp = -len(frac)
		digits += frac
	}
	if i < len(lit) {
		// The grammar guarantees the remainder is a complete exponent
		// part: (e|E), an optional sign, and at least one digit.
		exp += parseExponent(lit[i+1:])
	}
	sig = strings.TrimLeft(digits, "0")
	trimmed := strings.TrimRight(sig, "0")
	exp += len(sig) - len(trimmed)
	return trimmed, exp, neg
}

// expSaturation caps a parsed exponent's magnitude. Beyond it the exact
// exponent is irrelevant: the significand carries at most len(input)
// digits, so a saturated positive exponent always classifies as overflow
// and a saturated negative one as fractional (or the significand was all
// zeros and the value is exactly zero). The cap keeps the accumulation
// below overflow even for 32-bit int while staying far above any real
// digit count.
const expSaturation = 1 << 27

// parseExponent evaluates a grammar-validated exponent body (an optional
// sign, then one or more digits) with magnitude saturation.
func parseExponent(s string) int {
	i := 0
	neg := false
	if s[i] == '+' || s[i] == '-' {
		neg = s[i] == '-'
		i++
	}
	e := 0
	for ; i < len(s); i++ {
		e = e*10 + int(s[i]-'0')
		if e >= expSaturation {
			// The rest of s is digits; the cap already decides the
			// classification, so the exact value no longer matters.
			e = expSaturation
			break
		}
	}
	if neg {
		return -e
	}
	return e
}

// isDigit reports whether b is an ASCII decimal digit.
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// scanDigits advances past a run of ASCII digits starting at i, returning
// the index after the run and how many digits it scanned.
func scanDigits(s string, i int) (next, count int) {
	for i < len(s) && isDigit(s[i]) {
		i++
		count++
	}
	return i, count
}

// decimalNumberForm reports whether s (non-empty, pre-trimmed) is a
// decimal integer or float: an optional single sign, digits with an
// optional fraction part (at least one digit overall), and an optional
// exponent. This is the decimal subset of strconv syntax: leading '+' and
// leading zeros are allowed (quoted-number reality), while hex floats,
// "Inf"/"NaN" words, and underscores - which strconv.ParseFloat would
// accept - are not.
func decimalNumberForm(s string) bool {
	i := 0
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	i, digits := scanDigits(s, i)
	if i < len(s) && s[i] == '.' {
		var frac int
		i, frac = scanDigits(s, i+1)
		digits += frac
	}
	if digits == 0 {
		return false
	}
	if i == len(s) {
		return true
	}
	return validExponent(s, i)
}

// validExponent reports whether s[i:] is a complete exponent part:
// (e|E) followed by an optional sign and at least one digit.
func validExponent(s string, i int) bool {
	if s[i] != 'e' && s[i] != 'E' {
		return false
	}
	i++
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	next, count := scanDigits(s, i)
	return count > 0 && next == len(s)
}
