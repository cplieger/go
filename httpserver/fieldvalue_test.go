package httpserver_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
)

func TestCheckFieldValue_refuses(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   error
		offset int
	}{
		{"a NUL byte", "ab\x00cd", httpserver.ErrFieldControl, 2},
		{"a CR", "ab\rcd", httpserver.ErrFieldControl, 2},
		{"an LF", "ab\ncd", httpserver.ErrFieldControl, 2},
		{"a unit separator", "ab\x1fcd", httpserver.ErrFieldControl, 2},
		{"DEL", "ab\x7fcd", httpserver.ErrFieldControl, 2},
		{"a leading space", " abcd", httpserver.ErrFieldEdgeSpace, 0},
		{"a leading tab", "\tabcd", httpserver.ErrFieldEdgeSpace, 0},
		{"a trailing space", "abcd ", httpserver.ErrFieldEdgeSpace, 4},
		{"a trailing tab", "abcd\t", httpserver.ErrFieldEdgeSpace, 4},
		{"a lone continuation byte", "ab\x80cd", httpserver.ErrFieldInvalidUTF8, 2},
		{"a truncated rune", "ab\xe2\x82", httpserver.ErrFieldInvalidUTF8, 2},
		{"a zero-width space", "ab\u200bcd", httpserver.ErrFieldInvisible, 2},
		{"a right-to-left override", "ab\u202ecd", httpserver.ErrFieldInvisible, 2},
		{"a byte order mark", "\ufeffabcd", httpserver.ErrFieldInvisible, 0},
		{"a soft hyphen", "ab\u00adcd", httpserver.ErrFieldInvisible, 2},
		{"a C1 control", "ab\u0085cd", httpserver.ErrFieldInvisible, 2},
		{"a line separator", "ab\u2028cd", httpserver.ErrFieldInvisible, 2},
		{"a paragraph separator", "ab\u2029cd", httpserver.ErrFieldInvisible, 2},
		{"the first of two faults", "a\x00b\u200b", httpserver.ErrFieldControl, 1},
		{"an invisible rune before a trailing space", "a\u200b ", httpserver.ErrFieldInvisible, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := httpserver.CheckFieldValue(tc.in)
			var fe *httpserver.FieldValueError
			if !errors.As(err, &fe) || !errors.Is(err, tc.want) {
				t.Fatalf("CheckFieldValue = %v, want a *FieldValueError matching %v", err, tc.want)
			}
			if fe.Offset != tc.offset {
				t.Errorf("Offset = %d, want %d", fe.Offset, tc.offset)
			}
		})
	}
}

func TestCheckFieldValue_admits(t *testing.T) {
	for name, v := range map[string]string{
		"empty":                 "",
		"a token":               "s3cr3t-token_value.v2",
		"base64 with padding":   "dXNlcjpwYXNz==",
		"an inner space":        "two words",
		"an inner tab":          "a\tb",
		"visible punctuation":   `!"#$%&'()*+,-./:;<=>?@[\]^_{|}~` + "`",
		"accented letters":      "café-crème",
		"a CJK value":           "字幕",
		"an emoji":              "key-🔑",
		"a single visible byte": "x",
	} {
		t.Run(name, func(t *testing.T) {
			if err := httpserver.CheckFieldValue(v); err != nil {
				t.Errorf("CheckFieldValue(%q) = %v, want nil", v, err)
			}
		})
	}
}

func TestCheckFieldValue_errorNamesNoValue(t *testing.T) {
	secret := "s3cr3t-token-value"
	err := httpserver.CheckFieldValue(secret + "\n")
	if err == nil {
		t.Fatal("a value with an LF passed")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error %q quotes the value", err)
	}
	if want := "at byte 18"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name %q", err, want)
	}
}

// Every value CheckFieldValue admits survives a real request's header round
// trip unchanged; net/http's own refusal of control bytes stays the oracle.
func TestCheckFieldValue_admittedValueRoundTrips(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Token")
	}))
	t.Cleanup(srv.Close)
	for _, v := range []string{"two words", "a\tb", "café-crème", "key-🔑"} {
		if err := httpserver.CheckFieldValue(v); err != nil {
			t.Fatalf("CheckFieldValue(%q) = %v", v, err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Token", v)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("sending %q: %v", v, err)
		}
		_ = resp.Body.Close()
		if got != v {
			t.Errorf("sent %q, the server read %q", v, got)
		}
	}
}
