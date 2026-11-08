package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// Backoff/wait behavior of the RetryRoundTripper: the default jittered
// doubling progression, the Retry-After override, and the MaxElapsedTime hard
// ceiling. (v2's pluggable custom-Backoff subsystem was deleted in v3; the
// equal-jitter progression configured by BaseDelay is the one strategy.)

func TestRetryRoundTripper_MaxElapsedTime(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 50 * time.Millisecond, MaxAttempts: 11, MaxElapsedTime: 80 * time.Millisecond})
	client := &http.Client{Transport: rt}

	_, err := client.Get(srv.URL) //nolint:bodyclose // error path returns no body
	if err == nil {
		t.Fatal("expected error from MaxElapsedTime")
	}
	if !errors.Is(err, httpclient.ErrMaxElapsed) || !strings.Contains(err.Error(), "(80ms)") {
		t.Errorf("error = %v, want ErrMaxElapsed naming the 80ms ceiling", err)
	}
	// The always-503 server leaves RoundTrip's elapsed stop with no error to
	// wrap, which must read cleanly (no fmt.Errorf("...: %w", nil) artifact).
	if strings.Contains(err.Error(), "<nil>") || strings.Contains(err.Error(), "%!w") {
		t.Errorf("clean nil-lastErr error expected, got %v", err)
	}
	if got := calls.Load(); got > 4 {
		t.Errorf("calls = %d, want <= 4 with MaxElapsedTime", got)
	}
}

func TestRetryRoundTripper_MaxElapsedTime_wraps_transport_error(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Hour, MaxAttempts: 3, MaxElapsedTime: time.Millisecond})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/elapsed-transport-err", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip = nil, want max-elapsed-time error")
	}
	if !errors.Is(err, httpclient.ErrMaxElapsed) {
		t.Errorf("error = %v, want ErrMaxElapsed", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want errors.Is(err, io.ErrUnexpectedEOF)", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("transport calls = %d, want 1", got)
	}
}

// TestRetryRoundTripper_MaxElapsedTime_stop_releases_discarded_response pins
// that the elapsed stop still hands the discarded 503 to OnRetry and closes
// its body, so the connection is not leaked.
func TestRetryRoundTripper_MaxElapsedTime_stop_releases_discarded_response(t *testing.T) {
	t.Parallel()
	var closed, onRetry atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": {"60"}},
			Body:       &trackingCloser{Reader: strings.NewReader("busy"), onClose: func() { closed.Add(1) }},
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{
		MaxElapsedTime: time.Second,
		OnRetry:        func(int, *http.Request, *http.Response, error) { onRetry.Add(1) },
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/elapsed-drain", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
		t.Fatalf("RoundTrip resp = %d, want nil on the elapsed stop", resp.StatusCode)
	}
	if !errors.Is(err, httpclient.ErrMaxElapsed) {
		t.Errorf("RoundTrip err = %v, want the max-elapsed-time stop", err)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("discarded body closes = %d, want 1", got)
	}
	if got := onRetry.Load(); got != 1 {
		t.Errorf("OnRetry calls = %d, want 1", got)
	}
}

// TestRetryRoundTripper_zero_base_delay_defaults_to_base: a zero base delay must
// default to the 1s base delay; the single retry sleep blows the 100ms
// deadline. A boundary mutant that keeps the delay at 0 runs the retry instantly.
func TestRetryRoundTripper_zero_base_delay_defaults_to_base(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 0, MaxAttempts: 2})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Errorf("RoundTrip(baseDelay=0) err = nil, want context deadline (delay defaulted)")
	}
}

// TestRetryRoundTripper_default_backoff_doubles_between_retries: with doubling,
// the three jittered sleeps total >= 60+120+240 = 420ms (> 400ms deadline);
// without doubling they total <= 120*3 = 360ms. A mutant that skips SafeDouble
// finishes within the deadline and returns 503/nil.
func TestRetryRoundTripper_default_backoff_doubles_between_retries(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 120 * time.Millisecond, MaxAttempts: 4})
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Errorf("RoundTrip(default backoff) err = nil, want context deadline (backoff must double)")
	}
}

// TestRetryRoundTripper_zero_max_elapsed_does_not_cap: maxElapsedTime==0 means
// no cap, so the retry reaches 200. A mutant treating 0 as "cap enabled" aborts.
func TestRetryRoundTripper_zero_max_elapsed_does_not_cap(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL)
	if err != nil {
		t.Fatalf("Get = %v, want nil", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (no elapsed-time cap)", resp.StatusCode)
	}
}

// TestRetryRoundTripper_large_max_elapsed_allows_fast_retry: elapsed (~ms) is far
// below the 10s cap, so the retry reaches 200.
func TestRetryRoundTripper_large_max_elapsed_allows_fast_retry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, MaxElapsedTime: 10 * time.Second})
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL)
	if err != nil {
		t.Fatalf("Get = %v, want nil", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (within elapsed cap)", resp.StatusCode)
	}
}

// TestRetryRoundTripper_429_without_retry_after_uses_backoff: with no Retry-After,
// ParseRetryAfter==0 must NOT override the huge jittered backoff, so the retry
// sleep blows the 100ms deadline.
func TestRetryRoundTripper_429_without_retry_after_uses_backoff(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 10 * time.Second, MaxAttempts: 2})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Errorf("RoundTrip(429 no Retry-After) err = nil, want context deadline (backoff used)")
	}
}

// TestRetryRoundTripper_sleep_error_aborts_with_bare_context_error: a
// deadline-interrupted sleep must abort RoundTrip and surface the bare context
// error, not a transport *url.Error from another attempt against the dead context.
func TestRetryRoundTripper_sleep_error_aborts_with_bare_context_error(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 10 * time.Second, MaxAttempts: 2})
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip err = nil, want context error from interrupted sleep")
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		t.Errorf("RoundTrip err = %T (%v), want bare context error, not *url.Error", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("RoundTrip err = %v, want context.DeadlineExceeded", err)
	}
}

// TestRetryRoundTripper_aborts_when_wait_consumes_remaining_budget pins the
// remaining-budget arithmetic (maxElapsedTime - elapsed). The always-503
// upstream sends a Retry-After equal to the whole budget, so the honored wait
// is never smaller than the remaining budget (the real elapsed time only
// shrinks it) and the hard ceiling trips on the first retry: RoundTrip aborts
// after a single transport call without sleeping. A budget computed as
// maxElapsedTime + elapsed would instead exceed the wait, letting the
// round-tripper sleep out the full second and make more attempts (also caught
// by the elapsed-time assertion).
func TestRetryRoundTripper_aborts_when_wait_consumes_remaining_budget(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		h := http.Header{}
		h.Set("Retry-After", "1") // honored wait: exactly the 1s budget
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     h,
		}, nil
	})
	const budget = time.Second
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{
		BaseDelay:      time.Millisecond,
		MaxAttempts:    3,
		MaxElapsedTime: budget,
	})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/budget", http.NoBody)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	elapsed := time.Since(start)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip = nil, want max-elapsed-time abort (next wait consumes the remaining budget)")
	}
	if !errors.Is(err, httpclient.ErrMaxElapsed) {
		t.Errorf("error = %v, want ErrMaxElapsed", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("transport calls = %d, want 1 (abort before the second attempt)", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want immediate abort (the 1s Retry-After must not be slept)", elapsed)
	}
}
