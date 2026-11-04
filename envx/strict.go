package envx

import (
	"time"
)

// ParseError reports a set-but-malformed environment variable from IntStrict or
// DurationStrict. It carries the key and the TRIMMED value the parser actually
// saw, so a caller can quote what was rejected without reaching back to the
// environment.
//
// That second read is what this type exists to remove, and it was never
// equivalent: os.Getenv returns the value UNTRIMMED, so a diagnostic built that
// way can quote " 5x " while the parse error beside it is about "5x". Value is
// the string the parse failed on, by construction.
//
// BoolStrict deliberately does NOT return it. That variant exists for a key
// whose value must never be echoed (see BoolStrict), and a typed error carrying
// the value would hand it to every caller that logs the error. For Int and
// Duration the value is already repeated inside the wrapped parse error
// (*strconv.NumError.Num, time.ParseDuration's message), so Value discloses
// nothing the error did not already carry.
//
// Err is the underlying strconv or time parse error; errors.As still reaches
// *strconv.NumError through it.
//
// Key carries the Key type, not a plain string: the field IS an environment
// variable name, so a caller that aggregates failures and re-reads a variable
// with a default passes it straight back into a getter. Value stays a plain
// string because a rejected value is not a key.
//
// Field order is govet fieldalignment's, not editorial: Err leads because an
// interface is two pointer words, which shortens the GC scan range.
type ParseError struct {
	// Err is the underlying strconv or time parse error.
	Err error
	// Key is the environment variable name whose value did not parse.
	Key Key
	// Value is the trimmed value the parser rejected.
	Value string
}

// Error implements the error interface. The text is the key followed by the
// underlying parse error, which is the form every strict variant has always
// returned; only the type is new.
func (e *ParseError) Error() string {
	return "environment variable " + string(e.Key) + ": " + e.Err.Error()
}

// Unwrap exposes the underlying parse error so errors.As reaches
// *strconv.NumError, as callers of IntStrict already rely on.
func (e *ParseError) Unwrap() error { return e.Err }

// BoolStrict returns the boolean value of the environment variable key with
// the parse result owned by the caller: a set-but-malformed value is returned
// as an error instead of the tolerant getters' warn-and-fallback.
//
// The accepted spellings are exactly Bool's (true/1/yes/on and false/0/no/off,
// case-insensitive, surrounding whitespace ignored) because both route through
// the same parser and cannot drift apart. The three states match IntStrict:
// unset or empty (false, false, nil), malformed (false, false, err), valid
// (b, true, nil). value is false in two of those three, so ok — never value
// alone — distinguishes "set to false" from "not set".
//
// Unlike IntStrict and DurationStrict, the error carries no fragment of the
// value: there is no parse error to wrap, and the reason to reach for
// BoolStrict is usually that the value must not be repeated anywhere. It names
// the key and the accepted vocabulary. Strict variants never log.
//
// Use Bool when a malformed value should fall back with a Warn naming the raw
// value; use BoolStrict for a key whose value may be sensitive (one an
// operator could wire to a secret by mistake, so no diagnostic may echo it) or
// when the caller owns its own diagnostics.
//
// (value and ok share their bool type in the signature at the linter's
// insistence; they mean what the family's other strict variants mean.)
func BoolStrict(key Key) (value, ok bool, err error) {
	return Source{}.BoolStrict(key)
}

// IntStrict returns the integer value of the environment variable key with
// the parse result owned by the caller: a set-but-malformed value is returned
// as an error instead of the tolerant getters' warn-and-fallback.
//
// ok reports a successfully parsed value; it is false when the variable is
// unset or empty (empty equals unset, as with every getter) and false when
// the value did not parse. err is non-nil only for a set-but-malformed value;
// it names the key and wraps the underlying strconv error. Exactly one of
// the three states holds: unset (0, false, nil), malformed (0, false, err),
// or valid (n, true, nil). Strict variants never log.
//
// Use Int when a malformed value should fall back with a Warn; use IntStrict
// when the caller must decide what a malformed value means (reject startup,
// apply bounds, keep an existing value).
//
// A malformed value returns a *ParseError carrying the key and the trimmed
// value, so a caller quoting the rejected input needs no second environment
// read.
func IntStrict(key Key) (value int, ok bool, err error) {
	return Source{}.IntStrict(key)
}

// DurationStrict returns the value of the environment variable key parsed
// with time.ParseDuration ("30s", "6h", "1h30m"), with the parse result owned
// by the caller: a set-but-malformed value is returned as an error instead of
// the tolerant getters' warn-and-fallback.
//
// The three states match IntStrict: unset or empty (0, false, nil), malformed
// (0, false, err), valid (d, true, nil). As with Duration, a bare number
// without a unit is rejected ("30" is ambiguous between seconds and minutes
// across tools). Strict variants never log.
//
// A malformed value returns a *ParseError carrying the key and the trimmed
// value, so a caller quoting the rejected input needs no second environment
// read.
func DurationStrict(key Key) (value time.Duration, ok bool, err error) {
	return Source{}.DurationStrict(key)
}
