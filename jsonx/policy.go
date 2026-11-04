package jsonx

import "math"

// Disposition is what a Policy does when a parse fact holds. The zero
// value is Reject, so an unset Policy field fails closed.
type Disposition uint8

const (
	// Reject returns a *ParseError for the fact.
	Reject Disposition = iota
	// Zero tolerates the fact: the value decodes to 0 with no error,
	// treating the oddity as an absent value.
	Zero
	// Accept uses the parsed value despite the fact. It is meaningful
	// only for the facts that still carry a usable integer -
	// Policy.PaddedString and Policy.FloatForm. On every other field a
	// value does not exist (null, garbage) or would have to be corrupted
	// to produce one (fractional, out of range), so Accept there is
	// treated as Reject: misuse fails loudly instead of inventing data.
	Accept
)

// Policy decides, fact by fact, how ParseInt64 treats one number-or-string
// JSON value. Policies are plain values: copy a ready-made one and adjust
// fields to compose a variant (see the package examples).
//
// The zero-value Policy rejects every fact and accepts only values inside
// its zero range [0, 0] - i.e. the literal 0 - which is fail-closed by
// construction. MinValue and MaxValue are never defaulted: every policy
// states its accepted range explicitly (use math.MinInt64/math.MaxInt64
// for "unbounded").
//
// Gate order for numeric values: PaddedString, then FloatForm, then
// Fractional, then the range check. Non-numeric shapes are dispatched on
// their Shape field before any of those gates.
type Policy struct {
	// MinValue is the smallest accepted value; anything below is
	// OutOfRange.
	MinValue int64
	// MaxValue is the largest accepted value; anything above (including
	// any int64 overflow) is OutOfRange.
	MaxValue int64
	// Null handles the JSON null literal (Zero or Reject).
	Null Disposition
	// EmptyInput handles zero-length input (Zero or Reject). Real
	// decoders never produce it; it exists for direct callers emulating
	// the "absent field" case.
	EmptyInput Disposition
	// EmptyString handles exactly `""` (Zero or Reject).
	EmptyString Disposition
	// MalformedString handles tokens that start with '"' but are not
	// valid JSON strings (Zero or Reject).
	MalformedString Disposition
	// NonNumericString handles valid strings whose content is not a
	// decimal number, e.g. "unknown" (Zero or Reject).
	NonNumericString Disposition
	// OtherShape handles objects, arrays, booleans, and invalid tokens
	// (Zero or Reject).
	OtherShape Disposition
	// PaddedString handles numeric string content with surrounding ASCII
	// whitespace, e.g. " 12 " (Accept, Zero, or Reject).
	PaddedString Disposition
	// FloatForm handles float-syntax literals, e.g. "9.0" or 1e3
	// (Accept, Zero, or Reject). When accepted, the Fractional and range
	// gates still apply to the value.
	FloatForm Disposition
	// Fractional handles non-integral values, e.g. 1.5 (Zero or Reject).
	// There is no Accept: this package never truncates.
	Fractional Disposition
	// OutOfRange handles values outside [MinValue, MaxValue], including
	// int64 overflows (Zero or Reject).
	OutOfRange Disposition
}

// TolerantZero returns the tolerant-zero policy (origin: seadex-scout's
// Fribb id decoder). Any odd shape or invalid value decodes to 0 - an
// upstream placeholder must neither fail the record nor masquerade as a
// valid id - with two deliberate exceptions: a malformed JSON string is
// still an error (document corruption, not shape variance), and a
// fractional value zeroes rather than truncates (9.9 truncated to 9 would
// silently point at a different entity). Integral float forms are
// accepted ("9.0" -> 9, "1e3" -> 1000), padded numeric strings are
// accepted (" 12 " -> 12), and the accepted range is pinned to
// [0, math.MaxInt32] - real-world ids are non-negative int32s, so
// negatives and larger values decode to 0. Widen the bound by copying the
// policy and raising MaxValue.
func TolerantZero() Policy {
	return Policy{
		MinValue:         0,
		MaxValue:         math.MaxInt32,
		Null:             Zero,
		EmptyInput:       Zero,
		EmptyString:      Zero,
		MalformedString:  Reject,
		NonNumericString: Zero,
		OtherShape:       Zero,
		PaddedString:     Accept,
		FloatForm:        Accept,
		Fractional:       Zero,
		OutOfRange:       Zero,
	}
}

// Strict returns the strict policy (origin: subflux's provider
// ParseFlexInt core). It accepts a bare integer or a quoted decimal
// integer anywhere in int64, tolerates null and "" as 0 (an absent value
// in either representation), and rejects everything else - float forms
// (even integral ones), padded strings, non-numeric strings, non-number
// shapes, overflows, and zero-length input.
func Strict() Policy {
	return Policy{
		MinValue:         math.MinInt64,
		MaxValue:         math.MaxInt64,
		Null:             Zero,
		EmptyInput:       Reject,
		EmptyString:      Zero,
		MalformedString:  Reject,
		NonNumericString: Reject,
		OtherShape:       Reject,
		PaddedString:     Reject,
		FloatForm:        Reject,
		Fractional:       Reject,
		OutOfRange:       Reject,
	}
}

// StrictAbsentZero returns the strict policy with zero-length input
// tolerated as 0 (origin: plex-language-sync's FlexInt, which pins the
// pre-flex json.Number behaviour where an absent field produced 0). It is
// identical to Strict in every other field - a drift-guard test enforces
// that.
func StrictAbsentZero() Policy {
	p := Strict()
	p.EmptyInput = Zero
	return p
}

// ParseInt64 classifies one JSON value's raw bytes and applies the policy:
// the parsed value on acceptance, 0 on a tolerated fact, or a *ParseError
// naming the rejecting gate. This is the functional core; the ready-made
// field types (TolerantInt, StrictInt, StrictAbsentZeroInt) wrap it for
// struct-tag use.
func ParseInt64(data []byte, p Policy) (int64, error) {
	facts := Classify(data)
	if facts.Shape != Number && facts.Shape != NumericString {
		d, r := p.shapeGate(facts.Shape)
		return resolve(d, r, data, facts)
	}
	if facts.Padded && p.PaddedString != Accept {
		return resolve(p.PaddedString, ReasonPaddedString, data, facts)
	}
	if facts.FloatForm && p.FloatForm != Accept {
		return resolve(p.FloatForm, ReasonFloatForm, data, facts)
	}
	if facts.Fractional {
		return resolve(p.Fractional, ReasonFractional, data, facts)
	}
	if facts.Overflow || facts.Value < p.MinValue || facts.Value > p.MaxValue {
		return resolve(p.OutOfRange, ReasonOutOfRange, data, facts)
	}
	return facts.Value, nil
}

// shapeGate maps a non-numeric shape to its policy disposition and
// rejection reason.
func (p Policy) shapeGate(s Shape) (Disposition, Reason) {
	switch s {
	case Empty:
		return p.EmptyInput, ReasonEmptyInput
	case Null:
		return p.Null, ReasonNull
	case EmptyString:
		return p.EmptyString, ReasonEmptyString
	case MalformedString:
		return p.MalformedString, ReasonMalformedString
	case NonNumericString:
		return p.NonNumericString, ReasonNonNumericString
	default: // Other (the numeric shapes never reach here)
		return p.OtherShape, ReasonOtherShape
	}
}

// resolve maps a non-Accept disposition to its outcome: Zero tolerates as
// 0, anything else (Reject, or a meaningless Accept - see Disposition)
// rejects with a typed error.
func resolve(d Disposition, r Reason, data []byte, facts Facts) (int64, error) {
	if d == Zero {
		return 0, nil
	}
	return 0, &ParseError{Snippet: snippet(data), Reason: r, Facts: facts}
}
