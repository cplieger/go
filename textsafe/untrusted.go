package textsafe

import (
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"strconv"
)

// Untrusted holds untrusted upstream text. It decodes raw and emits the
// Sanitize form through slog, any encoding.TextMarshaler-aware encoder and
// the fmt verbs it formats: all but %T, %p and a %w on a non-error, with %#v
// printing an escaped constructor call. It is a struct because encoding/json
// writes a string-kinded map key without calling MarshalText
// (https://go.dev/issue/81355). It stores no raw unsafe rune, so a reflection
// path (%p, an unexported field) prints none. The zero value is empty; values
// compare and key maps by raw bytes.
// docs/untrusted.md holds the full contract.
type Untrusted struct {
	// shown is the Sanitize form, which equals the raw bytes when quoted is
	// empty. quoted is strconv.QuoteToASCII of the raw bytes whenever
	// Sanitize changes them (an unsafe rune or invalid UTF-8), else empty.
	// The pair is a function of the raw bytes and quoted is reversible, so
	// == stays raw byte equality.
	shown  string
	quoted string
}

var (
	_ slog.LogValuer           = Untrusted{}
	_ fmt.Stringer             = Untrusted{}
	_ fmt.Formatter            = Untrusted{}
	_ encoding.TextMarshaler   = Untrusted{}
	_ encoding.TextUnmarshaler = (*Untrusted)(nil)
)

// NewUntrusted tags s as untrusted, keeping its bytes exactly. It sanitizes
// once, so clean text costs one scan and no allocation.
func NewUntrusted(s string) Untrusted {
	shown := Sanitize(s)
	if shown == s {
		return Untrusted{shown: s}
	}
	return Untrusted{shown: shown, quoted: strconv.QuoteToASCII(s)}
}

// LogValue implements slog.LogValuer: a tagged attr value resolves to its
// Sanitize form in every handler before encoding.
func (u Untrusted) LogValue() slog.Value {
	return slog.StringValue(u.shown)
}

// String implements fmt.Stringer and returns the Sanitize form. It keeps CR
// and LF, so a hand-built single-line sink that escapes nothing uses
// SingleLine instead.
func (u Untrusted) String() string {
	return u.shown
}

// Format implements fmt.Formatter, which fmt calls for every verb but %T, %p
// and a %w on a non-error. Each verb formats the Sanitize form with the
// directive's flags, width and precision, except %#v: it prints Go syntax that
// rebuilds the raw value, with every non-ASCII rune escaped, and ignores width
// and precision.
func (u Untrusted) Format(f fmt.State, verb rune) {
	_, hasWidth := f.Width()
	_, hasPrec := f.Precision()
	switch {
	case verb == 'v' && f.Flag('#'):
		fmt.Fprintf(f, "textsafe.NewUntrusted(%s)", strconv.QuoteToASCII(u.Raw()))
	case (verb == 'v' || verb == 's') && !hasWidth && !hasPrec:
		// The plain directive, written without building a format string so
		// %v and %s stay allocation-free.
		_, _ = io.WriteString(f, u.shown)
	default:
		fmt.Fprintf(f, fmt.FormatString(f, verb), u.shown)
	}
}

// MarshalText implements encoding.TextMarshaler: encoders emit the Sanitize
// form, as a value or a map key, at any nesting depth.
func (u Untrusted) MarshalText() ([]byte, error) {
	return []byte(u.shown), nil
}

// UnmarshalText implements encoding.TextUnmarshaler by keeping text exactly,
// so a decoded field or map key holds the raw bytes. Because MarshalText
// sanitizes, a tagged field written out and read back returns sanitized:
// state a program reads back stores Raw in a plain string field.
func (u *Untrusted) UnmarshalText(text []byte) error {
	*u = NewUntrusted(string(text))
	return nil
}

// SingleLine returns the SanitizeSingleLine form, for hand-built single-line
// sinks whose encoder does not escape CR/LF.
func (u Untrusted) SingleLine() string {
	return SanitizeSingleLine(u.Raw())
}

// Raw returns the exact bytes as received, for matching, dedupe keys and
// context-aware escapers. Matching on Raw is byte equality, so two values
// differing only in an unsafe rune stay distinct (docs/non-goals.md explains
// why a case fold is not a substitute). Raw allocates only when Sanitize
// changed the input. A cap on the emitted form goes on CapBytes(u.String(), n),
// because sanitizing can grow the raw bytes.
func (u Untrusted) Raw() string {
	if u.quoted == "" {
		return u.shown
	}
	// quoted is always QuoteToASCII output, which Unquote cannot reject.
	raw, _ := strconv.Unquote(u.quoted)
	return raw
}
