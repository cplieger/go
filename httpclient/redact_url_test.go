package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "query value redacted, keys and path kept",
			in:      "https://tautulli.example/api/v2?apikey=supersecret&cmd=get_history",
			absent:  []string{"supersecret", "get_history"},
			present: []string{"apikey=REDACTED", "cmd=REDACTED", "tautulli.example", "/api/v2"},
		},
		{
			name:    "userinfo dropped entirely, not just the password",
			in:      "https://user:hunter2@example.com/path",
			absent:  []string{"hunter2", "user:"},
			present: []string{"REDACTED@", "example.com", "/path"},
		},
		{
			// url.Redacted() masks only the password, so a username-only token
			// (a real credential shape for API endpoints) used to survive
			// verbatim in every log line this helper is meant to make safe.
			name:    "username-only token dropped",
			in:      "https://sk_live_abc123@prowlarr.example/1/api",
			absent:  []string{"sk_live_abc123"},
			present: []string{"REDACTED@", "prowlarr.example", "/1/api"},
		},
		{
			name:    "fragment dropped",
			in:      "https://example.com/p?token=abc#secretfrag",
			absent:  []string{"abc", "secretfrag"},
			present: []string{"token=REDACTED"},
		},
		{
			name:    "no query left unchanged",
			in:      "https://example.com/healthz",
			present: []string{"https://example.com/healthz"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.in)
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("redactURL(%q) = %q, must not contain %q", tc.in, got, s)
				}
			}
			for _, s := range tc.present {
				if !strings.Contains(got, s) {
					t.Errorf("redactURL(%q) = %q, want substring %q", tc.in, got, s)
				}
			}
		})
	}
}

func TestRedactURL_unparseableYieldsPlaceholder(t *testing.T) {
	got := redactURL("://\x7f-not-a-url")
	if got != "[unparseable url]" {
		t.Errorf("redactURL(bad) = %q, want %q", got, "[unparseable url]")
	}
}

func TestLogSafeError(t *testing.T) {
	if LogSafeError(nil) != nil {
		t.Fatal("LogSafeError(nil) should be nil")
	}

	// A transport *url.Error embeds the full URL; LogSafeError drops it.
	ue := &url.Error{
		Op:  "Get",
		URL: "https://example.com/p?apikey=supersecret",
		Err: errors.New("dial tcp: connection refused"),
	}
	got := LogSafeError(ue)
	if strings.Contains(got.Error(), "supersecret") || strings.Contains(got.Error(), "example.com") {
		t.Errorf("LogSafeError leaked URL: %q", got.Error())
	}
	if !strings.Contains(got.Error(), "connection refused") {
		t.Errorf("LogSafeError dropped the cause: %q", got.Error())
	}

	// A *StatusError passes through (its Error() already redacts) and the
	// unwrap chain is preserved.
	se := newStatusError(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, "https://example.com/p?apikey=supersecret")
	got2 := LogSafeError(se)
	if _, ok := errors.AsType[*StatusError](got2); !ok {
		t.Error("LogSafeError(*StatusError) broke the unwrap chain")
	}
	if strings.Contains(got2.Error(), "supersecret") {
		t.Errorf("StatusError still leaked the secret: %q", got2.Error())
	}
}

func TestRedactTransportError_unwraps_url_error_and_drops_url(t *testing.T) {
	ue := &url.Error{
		Op:  "Get",
		URL: "https://user:pw@host.example/api?apikey=supersecret",
		Err: errors.New("dial tcp: connection refused"),
	}
	got := RedactTransportError(ue, "fetch", "supersecret")
	if got == nil {
		t.Fatal("RedactTransportError(*url.Error) = nil, want error")
	}
	msg := got.Error()
	for _, leak := range []string{"supersecret", "host.example", "apikey", "pw"} {
		if strings.Contains(msg, leak) {
			t.Errorf("RedactTransportError leaked %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("RedactTransportError = %q, want cause preserved", msg)
	}
}

// TestReductionNeverNilsAFailure pins the floor under both *url.Error
// reductions: a NON-nil error never comes back nil. A *url.Error whose Err is
// nil (net/http never builds one, but errors.As matches whatever a caller
// hands in) used to reduce to that nil field — so Do logged a failed attempt
// with an empty "error" value, and a consumer returning the reduced error as
// its own reported the failure as a success. A typed-nil *url.Error panicked
// on the same path, and RedactTransportError panicked calling Error() on the
// nil it had just produced. The stand-in must also carry no part of the URL.
func TestReductionNeverNilsAFailure(t *testing.T) {
	t.Parallel()
	const rawURL = "https://t0ken@webhook.example/api/webhooks/1/s3cr3t?apikey=k3y"
	leaks := []string{"t0ken", "s3cr3t", "k3y", "webhook.example"}

	var typedNil *url.Error
	inputs := map[string]error{
		"nil cause":            &url.Error{Op: "Post", URL: rawURL},
		"nil cause wrapped":    fmt.Errorf("delivering notification: %w", &url.Error{Op: "Post", URL: rawURL}),
		"typed-nil *url.Error": typedNil,
	}
	// prefix/secret combinations matter: an empty prefix with a non-empty
	// secret is the shape that panicked (Error() on the nil cause), and an
	// empty prefix with an empty secret is the LogSafeError-equivalent path.
	reductions := map[string]func(error) error{
		"LogSafeError":                    LogSafeError,
		"RedactTransportError(no prefix)": func(e error) error { return RedactTransportError(e, "", "") },
		"RedactTransportError(prefix)":    func(e error) error { return RedactTransportError(e, "fetch", "") },
		"RedactTransportError(secret)":    func(e error) error { return RedactTransportError(e, "", "s3cr3t") },
		"RedactTransportError(both)":      func(e error) error { return RedactTransportError(e, "fetch", "s3cr3t") },
		"RedactSecret":                    func(e error) error { return RedactSecret(e, "s3cr3t") },
	}
	for inName, in := range inputs {
		for redName, reduce := range reductions {
			t.Run(inName+"/"+redName, func(t *testing.T) {
				t.Parallel()
				got := reduce(in)
				if got == nil {
					t.Fatalf("%s(%s) = nil, want a non-nil error (a nil erases the diagnostic and reads as success)", redName, inName)
				}
				for _, leak := range leaks {
					if strings.Contains(got.Error(), leak) {
						t.Errorf("%s(%s) leaked %q: %q", redName, inName, leak, got.Error())
					}
				}
			})
		}
	}
}

func TestRedactSecret_replaces_secret_occurrences(t *testing.T) {
	got := RedactSecret(errors.New("auth failed: apikey=supersecret123 rejected"), "supersecret123")
	if got == nil {
		t.Fatal("RedactSecret = nil, want error")
	}
	msg := got.Error()
	if strings.Contains(msg, "supersecret123") {
		t.Errorf("RedactSecret = %q, must not contain the secret", msg)
	}
	if !strings.Contains(msg, "REDACTED") {
		t.Errorf("RedactSecret = %q, want REDACTED in place of secret", msg)
	}
}

// TestRedactSecretString pins the string-level redaction primitive: every
// occurrence of a non-empty secret becomes "REDACTED", an empty secret is a
// no-op, and an absent secret leaves the string unchanged.
func TestRedactSecretString(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		secret Secret
		want   string
	}{
		{"single occurrence", "body key=abc123 end", "abc123", "body key=REDACTED end"},
		{"multiple occurrences", "abc123 and abc123", "abc123", "REDACTED and REDACTED"},
		{"secret absent", "nothing to see here", "abc123", "nothing to see here"},
		{"empty secret is a no-op", "keep abc123 intact", "", "keep abc123 intact"},
		{"empty input", "", "abc123", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactSecretString(tc.s, tc.secret); got != tc.want {
				t.Errorf("RedactSecretString(%q, %q) = %q, want %q", tc.s, tc.secret, got, tc.want)
			}
		})
	}
}

// TestRedactTransportError_prefix_controls_wrapping pins the prefix branch: an
// empty prefix returns the cause verbatim, while a non-empty prefix renders
// "prefix: cause". No *url.Error and no secret are involved, isolating the
// prefix decision from the unwrap and redact steps.
func TestRedactTransportError_prefix_controls_wrapping(t *testing.T) {
	base := errors.New("connection refused")
	if got := RedactTransportError(base, "", ""); got.Error() != "connection refused" {
		t.Errorf(`RedactTransportError(err, "", "") = %q, want %q`, got.Error(), "connection refused")
	}
	if got := RedactTransportError(base, "fetch", ""); got.Error() != "fetch: connection refused" {
		t.Errorf(`RedactTransportError(err, "fetch", "") = %q, want %q`, got.Error(), "fetch: connection refused")
	}
}
