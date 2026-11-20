package httpserver_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver/v2"
)

// FuzzCheckFieldValue_verdictMatchesBytes: an admitted value has no control
// byte, no edge space and is valid UTF-8; a refusal points at a byte inside
// the value whose kind is the one reported.
func FuzzCheckFieldValue_verdictMatchesBytes(f *testing.F) {
	for _, s := range []string{"", "token", " a", "a ", "a\x00b", "a\x7f", "\xff", "a\u200bb", "café", "a\tb", "\ufeff", "a\u2028"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		err := httpserver.CheckFieldValue(v)
		if err == nil {
			if !utf8.ValidString(v) {
				t.Fatalf("admitted invalid UTF-8 %q", v)
			}
			if strings.IndexFunc(v, func(r rune) bool { return (r < ' ' && r != '\t') || r == 0x7f }) >= 0 {
				t.Fatalf("admitted a control byte in %q", v)
			}
			if v != "" && (strings.ContainsAny(v[:1], " \t") || strings.ContainsAny(v[len(v)-1:], " \t")) {
				t.Fatalf("admitted an edge space in %q", v)
			}
			return
		}
		var fe *httpserver.FieldValueError
		if !errors.As(err, &fe) {
			t.Fatalf("refusal %v is not a *FieldValueError", err)
		}
		if fe.Offset < 0 || fe.Offset >= len(v) {
			t.Fatalf("Offset %d outside %q", fe.Offset, v)
		}
		b := v[fe.Offset]
		switch {
		case errors.Is(err, httpserver.ErrFieldControl):
			if (b >= ' ' || b == '\t') && b != 0x7f {
				t.Fatalf("control reported at %d, byte %#x in %q", fe.Offset, b, v)
			}
		case errors.Is(err, httpserver.ErrFieldEdgeSpace):
			if (b != ' ' && b != '\t') || (fe.Offset != 0 && fe.Offset != len(v)-1) {
				t.Fatalf("edge space reported at %d in %q", fe.Offset, v)
			}
		case errors.Is(err, httpserver.ErrFieldInvalidUTF8):
			if r, size := utf8.DecodeRuneInString(v[fe.Offset:]); r != utf8.RuneError || size != 1 {
				t.Fatalf("invalid UTF-8 reported at %d in %q", fe.Offset, v)
			}
		case errors.Is(err, httpserver.ErrFieldInvisible):
			if b < utf8.RuneSelf {
				t.Fatalf("invisible rune reported at ASCII byte %d in %q", fe.Offset, v)
			}
		default:
			t.Fatalf("unknown reason %v", err)
		}
	})
}
