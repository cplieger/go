package ssrf

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// TestErrorKind_String pins that String is the reason label the redirect and
// address-policy log lines carry, and a fixed fallback outside the constants.
func TestErrorKind_String(t *testing.T) {
	t.Parallel()
	for k := KindInvalidURL; k < kindEnd; k++ {
		if got := k.String(); got != reasonLabel(k) {
			t.Errorf("ErrorKind(%d).String() = %q, want the reason label %q", k, got, reasonLabel(k))
		}
	}
	if got := kindEnd.String(); got != "blocked" {
		t.Errorf("ErrorKind(kindEnd).String() = %q, want %q", got, "blocked")
	}
}

// TestURLPolicy_WithLoggerAttributesRedirectRefusals pins that a policy's
// logger receives both redirect refusal lines, the classify refusal and the
// hop limit, so a per-peer client can attribute them, that each line's reason
// is the returned Kind's String, and that a later WithLogger(nil) keeps the
// logger rather than resetting it.
func TestURLPolicy_WithLoggerAttributesRedirectRefusals(t *testing.T) {
	t.Parallel()
	safe := newTestReq(t, "https://example.com/")
	for _, tc := range []struct {
		name   string
		target string
		via    int
		kind   ErrorKind
	}{
		{"classify refusal", "http://127.0.0.1/", 1, KindBadScheme},
		{"hop limit", "https://example.com/", maxRedirects, KindTooManyRedirects},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			l := slog.New(slog.NewTextHandler(&buf, nil)).With("peer", "p1")
			check := URLPolicy{}.WithLogger(l).WithLogger(nil).RedirectPolicy(nil)
			req := newTestReq(t, tc.target)
			via := make([]*http.Request, tc.via)
			for i := range via {
				via[i] = safe
			}
			var se *Error
			if err := check(req, via); !errors.As(err, &se) || se.Kind != tc.kind {
				t.Fatalf("check(%s, %d hops) = %v, want an *Error of Kind %v", tc.target, tc.via, err, tc.kind)
			}
			out := buf.String()
			// The redirect lines label the Kind they return, so the
			// reason is the Kind's own String.
			for _, want := range []string{"ssrf redirect blocked", "reason=" + tc.kind.String(), "peer=p1"} {
				if !strings.Contains(out, want) {
					t.Errorf("policy logger got %q, want it to contain %q", out, want)
				}
			}
		})
	}
}

// TestNewURLPolicy_EmptySchemeIsNotAllowed pins that "" never becomes an
// allowed scheme, so a protocol-relative URL cannot pass, and that a list of
// only empty schemes keeps the HTTPS-only default.
func TestNewURLPolicy_EmptySchemeIsNotAllowed(t *testing.T) {
	t.Parallel()
	if err := NewURLPolicy("").Validate("//8.8.8.8/"); err == nil {
		t.Error(`NewURLPolicy("") accepted a protocol-relative URL`)
	}
	if err := NewURLPolicy("").Validate("https://example.com/"); err != nil {
		t.Errorf(`NewURLPolicy("") refused https: %v, want the HTTPS-only default`, err)
	}
	if err := NewURLPolicy("kafka", "").Validate("//8.8.8.8/"); err == nil {
		t.Error(`NewURLPolicy("kafka", "") accepted a protocol-relative URL`)
	}
}
