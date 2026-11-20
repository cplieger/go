package httpserver_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

func TestChallenge_String(t *testing.T) {
	cases := []struct {
		name string
		in   httpserver.Challenge
		want string
	}{
		{"a bare scheme", httpserver.Challenge{Scheme: "Bearer"}, "Bearer"},
		{"one param", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: "knell"}}}, `Bearer realm="knell"`},
		{
			"several params",
			httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: "subflux"}, {Name: "error", Value: "invalid_token"}}},
			`Bearer realm="subflux", error="invalid_token"`,
		},
		{"a quote and a backslash escaped", httpserver.Challenge{Scheme: "Basic", Params: []httpserver.AuthParam{{Name: "realm", Value: `a "b" \c`}}}, `Basic realm="a \"b\" \\c"`},
		{"an edge space kept", httpserver.Challenge{Scheme: "Basic", Params: []httpserver.AuthParam{{Name: "realm", Value: " my app "}}}, `Basic realm=" my app "`},
		{"an empty value", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: ""}}}, `Bearer realm=""`},
		{"a non-ASCII value", httpserver.Challenge{Scheme: "Basic", Params: []httpserver.AuthParam{{Name: "realm", Value: "café"}}}, `Basic realm="café"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestChallenge_StringPanicsOnProgramErrors(t *testing.T) {
	cases := []struct {
		name string
		in   httpserver.Challenge
		want string
	}{
		{"an empty scheme", httpserver.Challenge{}, "scheme"},
		{"a scheme with a space", httpserver.Challenge{Scheme: "Bear er"}, "scheme"},
		{"a scheme with an LF", httpserver.Challenge{Scheme: "Bear\ner"}, "scheme"},
		{"a param name with a CR", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "re\ralm", Value: "x"}}}, "param name"},
		{"a param name with a quote", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: `re"alm`, Value: "x"}}}, "param name"},
		{"a value with a CR", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: "a\rb"}}}, "control"},
		{"a value with an invisible rune", httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: "a\u200bb"}}}, "invisible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic %v, want one naming %q", r, tc.want)
				}
				if strings.ContainsAny(msg, "\r\n") {
					t.Errorf("panic %q carries a raw control byte", msg)
				}
			}()
			_ = tc.in.String()
		})
	}
}

func TestSetChallenge(t *testing.T) {
	t.Run("one challenge replaces what was set", func(t *testing.T) {
		h := http.Header{}
		h.Set("WWW-Authenticate", "Stale")
		httpserver.SetChallenge(h, httpserver.Challenge{Scheme: "Bearer"})
		if got := h.Values("WWW-Authenticate"); !slices.Equal(got, []string{"Bearer"}) {
			t.Errorf("WWW-Authenticate = %q, want [Bearer]", got)
		}
	})
	t.Run("several challenges, one line each, in order", func(t *testing.T) {
		h := http.Header{}
		httpserver.SetChallenge(h,
			httpserver.Challenge{Scheme: "Bearer", Params: []httpserver.AuthParam{{Name: "realm", Value: "a"}}},
			httpserver.Challenge{Scheme: "Basic", Params: []httpserver.AuthParam{{Name: "realm", Value: "a"}}},
		)
		want := []string{`Bearer realm="a"`, `Basic realm="a"`}
		if got := h.Values("WWW-Authenticate"); !slices.Equal(got, want) {
			t.Errorf("WWW-Authenticate = %q, want %q", got, want)
		}
	})
}
