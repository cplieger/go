package envx

import (
	"time"
)

// ParseError reports a malformed value from IntStrict or DurationStrict. Value
// is the trimmed string the parser rejected, so a caller can quote it without a
// second, untrimmed read. BoolStrict never returns one, because its value must
// not be echoed; for Int and Duration the wrapped parse error already carries
// the value. errors.As reaches *strconv.NumError through Err.
type ParseError struct {
	// Err is the underlying strconv or time parse error.
	Err error
	// Key is the environment variable name whose value did not parse.
	Key Key
	// Value is the trimmed value the parser rejected.
	Value string
}

// Error implements the error interface: the key, then the parse error, or
// "malformed value" when Err is nil.
func (e *ParseError) Error() string {
	if e.Err == nil {
		return "environment variable " + string(e.Key) + ": malformed value"
	}
	return "environment variable " + string(e.Key) + ": " + e.Err.Error()
}

// Unwrap answers the underlying parse error.
func (e *ParseError) Unwrap() error { return e.Err }

// BoolStrict returns the boolean value of the environment variable key with
// Bool's spellings, leaving a malformed value to the caller instead of logging:
// unset or empty is (false, false, nil), malformed (false, false, err), valid
// (b, true, nil), so only ok tells "set to false" from "not set". The error
// names the key and the accepted spellings, never the value, so use it for a
// key an operator could wire to a secret by mistake.
func BoolStrict(key Key) (value, ok bool, err error) {
	return Source{}.BoolStrict(key)
}

// IntStrict returns the integer value of the environment variable key, leaving
// a malformed value to the caller instead of logging: unset or empty is
// (0, false, nil), malformed (0, false, *ParseError), valid (n, true, nil).
func IntStrict(key Key) (value int, ok bool, err error) {
	return Source{}.IntStrict(key)
}

// DurationStrict returns the value of the environment variable key parsed with
// time.ParseDuration, leaving a malformed value, a bare number included, to the
// caller instead of logging: unset or empty is (0, false, nil), malformed
// (0, false, *ParseError), valid (d, true, nil).
func DurationStrict(key Key) (value time.Duration, ok bool, err error) {
	return Source{}.DurationStrict(key)
}
