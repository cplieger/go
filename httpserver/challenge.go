package httpserver

import (
	"net/http"
	"strings"
)

// AuthParam is one auth-param of a challenge, rendered as name="value".
type AuthParam struct {
	Name  string
	Value string
}

// Challenge is one RFC 9110 section 11.6.1 challenge for a WWW-Authenticate
// field: an auth-scheme and its auth-params, such as Bearer with
// realm="knell". It renders the header only; the response's status and body
// stay the caller's.
type Challenge struct {
	Scheme string
	Params []AuthParam
}

// String renders the challenge: the scheme, then each param as name="value"
// with the value a quoted-string, separated by ", ". It panics when the
// scheme or a param name is not an RFC 9110 token, or a value cannot be a
// quoted-string (CheckFieldValue refuses it for a control byte, invalid UTF-8
// or an invisible rune), since those come from the program, not a request.
// A value taken from outside the program is checked with CheckFieldValue
// first.
func (c Challenge) String() string {
	if !isToken(c.Scheme) {
		panic("httpserver: challenge scheme " + quoteForPanic(c.Scheme) + " is not an HTTP token")
	}
	var b strings.Builder
	b.WriteString(c.Scheme)
	for i, p := range c.Params {
		if !isToken(p.Name) {
			panic("httpserver: challenge param name " + quoteForPanic(p.Name) + " is not an HTTP token")
		}
		if err := checkQuotable(p.Value); err != nil {
			panic("httpserver: challenge param " + p.Name + ": " + err.Error())
		}
		if i == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		b.WriteString(`="`)
		for j := range len(p.Value) {
			if c := p.Value[j]; c == '"' || c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(p.Value[j])
		}
		b.WriteByte('"')
	}
	return b.String()
}

// checkQuotable refuses a value no quoted-string can carry. Unlike a whole
// field value, a quoted-string keeps edge spaces, so only the other
// CheckFieldValue reasons apply.
func checkQuotable(v string) error {
	if v == "" {
		return nil
	}
	// Wrapping the value in visible bytes takes edge spaces out of the check,
	// and the offsets are shifted back by one.
	err := CheckFieldValue("x" + v + "x")
	if fe, ok := err.(*FieldValueError); ok {
		return &FieldValueError{Err: fe.Err, Offset: fe.Offset - 1}
	}
	return err
}

// quoteForPanic shows a programmer's bad scheme or name in a panic, escaped
// so a control byte cannot reach the log raw.
func quoteForPanic(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r < ' ' || r == 0x7f || r == '"' || r == '\\' {
			b.WriteString(`\x`)
			b.WriteByte("0123456789abcdef"[r>>4&0xf])
			b.WriteByte("0123456789abcdef"[r&0xf])
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// SetChallenge replaces h's WWW-Authenticate field with one line per
// challenge, in order, so a 401 names how to authenticate (RFC 9110 section
// 11.6.1 requires at least one challenge on a 401). It sets the header only.
func SetChallenge(h http.Header, c Challenge, more ...Challenge) {
	lines := make([]string, 0, 1+len(more))
	lines = append(lines, c.String())
	for _, m := range more {
		lines = append(lines, m.String())
	}
	h["Www-Authenticate"] = lines
}
