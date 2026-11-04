package jsonx

import (
	"bytes"
	"strconv"
)

// Reason identifies which policy gate rejected a value. Consumers that
// wrap a *ParseError with their own message can switch on it.
type Reason string

// The rejection reasons a ParseError can carry, one per Policy gate.
const (
	// ReasonEmptyInput rejects zero-length input.
	ReasonEmptyInput Reason = "empty input"
	// ReasonNull rejects the JSON null literal.
	ReasonNull Reason = "json null"
	// ReasonEmptyString rejects `""`.
	ReasonEmptyString Reason = "empty string"
	// ReasonMalformedString rejects an invalid JSON string token.
	ReasonMalformedString Reason = "malformed string"
	// ReasonNonNumericString rejects string content that is not a
	// decimal number.
	ReasonNonNumericString Reason = "non-numeric string"
	// ReasonOtherShape rejects objects, arrays, booleans, and invalid
	// tokens.
	ReasonOtherShape Reason = "non-number JSON shape"
	// ReasonPaddedString rejects whitespace-padded numeric string
	// content.
	ReasonPaddedString Reason = "whitespace-padded string"
	// ReasonFloatForm rejects float-syntax literals.
	ReasonFloatForm Reason = "float form"
	// ReasonFractional rejects non-integral values.
	ReasonFractional Reason = "fractional value"
	// ReasonOutOfRange rejects values outside the policy's range,
	// including int64 overflows.
	ReasonOutOfRange Reason = "out of range"
)

// snippetCap bounds the raw-input excerpt embedded in a ParseError so an
// oversized upstream token cannot balloon error strings or logs.
const snippetCap = 40

// snippet returns a bounded excerpt of the offending value bytes.
func snippet(data []byte) string {
	data = bytes.Trim(data, jsonWhitespace)
	if len(data) <= snippetCap {
		return string(data)
	}
	return string(data[:snippetCap]) + "..."
}

// ParseError reports a value rejected by a Policy: which gate fired
// (Reason), the full syntactic Facts, and a bounded excerpt of the raw
// input for logs. Match it with errors.As.
type ParseError struct {
	// Snippet is a bounded excerpt of the offending value bytes.
	Snippet string
	// Reason names the policy gate that rejected the value.
	Reason Reason
	// Facts carries the full classification of the rejected value.
	Facts Facts
}

var _ error = (*ParseError)(nil)

// Error implements error with a stable, greppable shape:
// `jsonx: <reason>: "<snippet>"`.
func (e *ParseError) Error() string {
	return "jsonx: " + string(e.Reason) + ": " + strconv.Quote(e.Snippet)
}
