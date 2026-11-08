package ssrf

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// TestErrorKind_String pins that String is the reason label the package's own
// log lines carry, and a fixed fallback outside the constants.
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
// logger receives its redirect refusals, so a per-peer client can attribute
// them.
func TestURLPolicy_WithLoggerAttributesRedirectRefusals(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil)).With("peer", "p1")
	check := URLPolicy{}.WithLogger(l).RedirectPolicy(nil)
	req := newTestReq(t, "http://127.0.0.1/")
	var se *Error
	if err := check(req, []*http.Request{req}); !errors.As(err, &se) {
		t.Fatalf("redirect to a cleartext loopback URL = %v, want an *Error", err)
	}
	if out := buf.String(); !strings.Contains(out, "ssrf redirect blocked") || !strings.Contains(out, "peer=p1") {
		t.Errorf("policy logger got %q, want the attributed refusal line", out)
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
