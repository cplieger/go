package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// Nil-option guards (a nil functional option must be skipped, not panic) and the
// negative-attempt clamp edge case.

func TestGetBytes_nil_option_is_skipped(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})
	client := &http.Client{Transport: transport}
	var nilOpt httpclient.GetOption
	if _, err := httpclient.GetBytes(t.Context(), client, "http://example.com/nilopt", nilOpt); err != nil {
		t.Fatalf("nil GetOption caused error: %v", err)
	}
	// A shared Option (interface superset) is accepted by both doors; nil is
	// skipped there too.
	var nilShared httpclient.Option
	if _, err := httpclient.GetBytes(t.Context(), client, "http://example.com/nilopt2", nilShared); err != nil {
		t.Fatalf("nil shared Option caused error: %v", err)
	}
}

func TestDo_nil_option_is_skipped(t *testing.T) {
	var nilOpt httpclient.DoOption
	got, err := httpclient.Do(t.Context(), func(_ context.Context) (string, error) {
		return "ok", nil
	}, nilOpt)
	if err != nil {
		t.Fatalf("nil DoOption caused error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("Do = %q, want ok", got)
	}
}

func TestRedirectPolicyFunc_nil_option_still_refuses(t *testing.T) {
	var nilOpt httpclient.RedirectOption
	policy := httpclient.RedirectPolicyFunc(nilOpt)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/nilredir", http.NoBody)
	if err := policy(req, nil); err == nil {
		t.Fatal("nil RedirectOption should still refuse (no hosts configured)")
	}
}

// TestRetryRoundTripper_negative_max_attempts_clamps_to_one covers the negative
// edge (the exact-count table lives in contract_test.go): a negative
// MaxAttempts means exactly one attempt (the v3 "try once" configuration; zero
// means unset and takes the default).
func TestRetryRoundTripper_negative_max_attempts_clamps_to_one(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{
		MaxAttempts: -1,
		BaseDelay:   time.Millisecond,
	})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/neg", http.NoBody)
	resp, _ := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TransportConfig{MaxAttempts: -1}: calls=%d, want 1 (negative means a single attempt)", got)
	}
}
