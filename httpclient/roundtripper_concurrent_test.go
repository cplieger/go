package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// TestRetryRoundTripper_does_not_mutate_caller_request verifies the
// http.RoundTripper contract: PrepareRetry mutates only the per-attempt clone,
// never the caller's request.
func TestRetryRoundTripper_does_not_mutate_caller_request(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("err")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, PrepareRetry: func(req *http.Request) error {
		req.Header.Set("X-Mutated", "yes")
		return nil
	}})

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/test", http.NoBody)
	req.Header.Set("X-Original", "keep")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if req.Header.Get("X-Mutated") != "" {
		t.Errorf("caller's request was mutated: X-Mutated = %q", req.Header.Get("X-Mutated"))
	}
	if req.Header.Get("X-Original") != "keep" {
		t.Errorf("caller's original header lost: X-Original = %q", req.Header.Get("X-Original"))
	}
}

// TestRetryRoundTripper_clone_isolates_caller_body verifies the caller's
// req.Body pointer is not replaced and every replayed attempt sees the full body.
func TestRetryRoundTripper_clone_isolates_caller_body(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, RetryNonIdempotent: true})

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", strings.NewReader("payload"))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("payload")), nil
	}
	callerBody := req.Body

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if req.Body != callerBody {
		t.Error("caller's req.Body was replaced (pointer changed)")
	}
	for i, b := range bodies {
		if b != "payload" {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, "payload")
		}
	}
}

// TestRetryRoundTripper_prepareRetry_header_isolation_per_attempt verifies each
// retry clone derives from the ORIGINAL request: PrepareRetry tokens do not
// accumulate, so attempt N sees only its own freshly-set token.
func TestRetryRoundTripper_prepareRetry_header_isolation_per_attempt(t *testing.T) {
	var attempt atomic.Int32
	var headersSeen []string
	var mu sync.Mutex

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := attempt.Add(1)
		mu.Lock()
		headersSeen = append(headersSeen, req.Header.Get("X-Token"))
		mu.Unlock()
		if n < 4 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	var prepareAttempt atomic.Int32
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 5, PrepareRetry: func(req *http.Request) error {
		n := prepareAttempt.Add(1)
		// Distinct token per retry ('A','B','C',...); built via []byte to
		// avoid an int-to-string conversion.
		req.Header.Set("X-Token", string([]byte{byte('A' + n - 1)}))
		// X-Accum would accumulate across attempts if clones leaked state.
		req.Header.Add("X-Accum", "x")
		return nil
	}})

	origReq, _ := http.NewRequest(http.MethodGet, "http://example.com/prepareisolation", http.NoBody)
	origReq.Header.Set("X-Token", "original")

	resp, err := rt.RoundTrip(origReq)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp.Body.Close()

	if origReq.Header.Get("X-Token") != "original" {
		t.Fatalf("PrepareRetry mutated original request X-Token = %q", origReq.Header.Get("X-Token"))
	}
	if origReq.Header.Get("X-Accum") != "" {
		t.Fatal("PrepareRetry X-Accum leaked to original request")
	}
	if headersSeen[0] != "original" {
		t.Fatalf("attempt 1 X-Token = %q, want 'original'", headersSeen[0])
	}
	for i, want := range []string{"A", "B", "C"} {
		if headersSeen[i+1] != want {
			t.Errorf("attempt %d X-Token = %q, want %q", i+2, headersSeen[i+1], want)
		}
	}
}

// TestRetryRoundTripper_clone_preserves_trailers verifies request Trailers
// survive the per-attempt Clone.
func TestRetryRoundTripper_clone_preserves_trailers(t *testing.T) {
	var attempt atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := attempt.Add(1)
		if req.Trailer == nil || req.Trailer.Get("X-Checksum") != "abc123" {
			t.Errorf("attempt %d: Trailer missing or wrong, got %v", n, req.Trailer)
		}
		if n < 2 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/trailers", http.NoBody)
	req.Trailer = http.Header{"X-Checksum": {"abc123"}}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp.Body.Close()
}

// TestRetryRoundTripper_clone_preserves_host verifies a custom req.Host survives
// the per-attempt Clone.
func TestRetryRoundTripper_clone_preserves_host(t *testing.T) {
	var attempt atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := attempt.Add(1)
		if req.Host != "custom.host.example" {
			t.Errorf("attempt %d: Host = %q, want 'custom.host.example'", n, req.Host)
		}
		if n < 2 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/hostclone", http.NoBody)
	req.Host = "custom.host.example"

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	resp.Body.Close()
}

// TestRetryRoundTripper_prepareRetry_error_does_not_leak_to_caller verifies that
// when PrepareRetry returns an error, the partial mutation it made stays on the
// clone and never reaches the caller's request.
func TestRetryRoundTripper_prepareRetry_error_does_not_leak_to_caller(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, PrepareRetry: func(req *http.Request) error {
		req.Header.Set("X-Mutated", "yes")
		return io.ErrUnexpectedEOF
	}})

	origReq, _ := http.NewRequest(http.MethodGet, "http://example.com/prepareerr", http.NoBody)
	_, err := rt.RoundTrip(origReq) //nolint:bodyclose // error path, no response body
	if err == nil {
		t.Fatal("expected PrepareRetry error")
	}
	if origReq.Header.Get("X-Mutated") != "" {
		t.Fatal("PrepareRetry error path leaked mutation to caller's request")
	}
}

// TestRetryRoundTripper_cancel_during_backoff_no_goroutine_leak drives many
// cancelled-mid-backoff RoundTrips and asserts no goroutines are leaked.
func TestRetryRoundTripper_cancel_during_backoff_no_goroutine_leak(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	})

	before := runtime.NumGoroutine()
	for range 20 {
		ctx, cancel := context.WithCancel(t.Context())
		rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Hour, MaxAttempts: 11})
		go func() {
			time.Sleep(time.Millisecond)
			cancel()
		}()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/leak", http.NoBody)
		resp, _ := rt.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after-before > 5 {
		t.Errorf("goroutine leak after cancellation: before=%d, after=%d", before, after)
	}
}
