package httpserver

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// The reasons CheckFieldValue refuses a value, matched with errors.Is.
var (
	ErrFieldControl     = errors.New("field value holds an ASCII control byte or DEL")
	ErrFieldEdgeSpace   = errors.New("field value starts or ends with a space or tab")
	ErrFieldInvalidUTF8 = errors.New("field value is not valid UTF-8")
	ErrFieldInvisible   = errors.New("field value holds an invisible character")
)

// FieldValueError is CheckFieldValue's refusal. It names the reason and the
// byte offset of the first refused byte, never the value, so a caller can
// word a configuration error without quoting a secret.
type FieldValueError struct {
	// Err is one of ErrFieldControl, ErrFieldEdgeSpace, ErrFieldInvalidUTF8
	// and ErrFieldInvisible.
	Err error
	// Offset is the byte offset of the refused byte or rune.
	Offset int
}

func (e *FieldValueError) Error() string {
	return fmt.Sprintf("httpserver: %v at byte %d", e.Err, e.Offset)
}

func (e *FieldValueError) Unwrap() error { return e.Err }

// CheckFieldValue reports whether v can be sent verbatim as an HTTP field
// value (RFC 9110 section 5.5) and read back unchanged: no ASCII control byte
// but HTAB, no DEL, no space or tab at an edge (a recipient strips it), valid
// UTF-8, and no invisible rune (a C1 control, a format character such as a
// bidi control or zero-width space, a line or paragraph separator). A refusal
// is a *FieldValueError naming the first refused byte. The empty string
// passes; length and blank-value policy stay the caller's.
func CheckFieldValue(v string) error {
	if v == "" {
		return nil
	}
	if isFieldSpace(v[0]) {
		return &FieldValueError{Err: ErrFieldEdgeSpace, Offset: 0}
	}
	if err := checkFieldBytes(v); err != nil {
		return err
	}
	if last := len(v) - 1; isFieldSpace(v[last]) {
		return &FieldValueError{Err: ErrFieldEdgeSpace, Offset: last}
	}
	return nil
}

// checkFieldBytes refuses v's first control byte, invalid UTF-8 sequence or
// invisible rune, in byte order.
func checkFieldBytes(v string) error {
	for i := 0; i < len(v); {
		b := v[i]
		if b < utf8.RuneSelf {
			if (b < ' ' && b != '\t') || b == 0x7f {
				return &FieldValueError{Err: ErrFieldControl, Offset: i}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(v[i:])
		if r == utf8.RuneError && size == 1 {
			return &FieldValueError{Err: ErrFieldInvalidUTF8, Offset: i}
		}
		if invisibleRune(r) {
			return &FieldValueError{Err: ErrFieldInvisible, Offset: i}
		}
		i += size
	}
	return nil
}

func isFieldSpace(b byte) bool { return b == ' ' || b == '\t' }

// invisibleRune reports a non-ASCII rune that draws nothing where a value is
// read or pasted: a C1 control, a format character, or a line or paragraph
// separator.
func invisibleRune(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
}
