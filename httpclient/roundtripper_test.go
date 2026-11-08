package httpclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpclient"
)

func TestRetryRoundTripper_success_no_retry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRetryRoundTripper_retries_on_503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

// TestRetryRoundTripper_retries_on_429_with_retry_after asserts the honored
// Retry-After wait EXACTLY, in synthetic time.
//
// The bubble is what makes the assertion exact. On a real clock this test slept
// a full second and could only assert a fuzzy lower bound (>= 900ms) to survive
// scheduler noise, which let a wait of 900ms — or of an hour — pass.
// synctest.Test virtualizes the clock, and httptest.NewTestServer (Go 1.27)
// supplies the in-memory network the bubble needs: a real loopback listener
// parks a goroutine on an external socket, which is never "durably blocked", so
// the fake clock could never advance and the test would hang until the go test
// timeout. Server.Listener is nil on that path; Server.URL is "http://example.com"
// and Server.Client() routes every host to the handler, so nothing else changes.
func TestRetryRoundTripper_retries_on_429_with_retry_after(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))

		rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
		client := &http.Client{Transport: rt}

		start := time.Now()
		resp, err := client.Get(srv.URL)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if elapsed != time.Second {
			t.Errorf("elapsed = %v, want exactly 1s (the Retry-After, not the 1ms BaseDelay)", elapsed)
		}
	})
}

// TestRetryRoundTripper_honors_retry_after_on_503 asserts the RoundTripper
// honors Retry-After on a 5xx, not just on 429. Runs in synthetic time; see
// the sibling 429 test for why the bubble needs httptest.NewTestServer.
func TestRetryRoundTripper_honors_retry_after_on_503(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"502", http.StatusBadGateway},
		{"503", http.StatusServiceUnavailable},
		{"504", http.StatusGatewayTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(tt.status)
						return
					}
					w.WriteHeader(http.StatusOK)
				}))

				rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
				client := &http.Client{Transport: rt}

				start := time.Now()
				resp, err := client.Get(srv.URL)
				elapsed := time.Since(start)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("status = %d, want 200", resp.StatusCode)
				}
				// Retry-After: 1 must be honored on the 5xx, not just on 429.
				if elapsed != time.Second {
					t.Errorf("elapsed = %v, want exactly 1s (Retry-After honored on %s)", elapsed, tt.name)
				}
			})
		})
	}
}

func TestRetryRoundTripper_no_retry_on_POST_without_opt_in(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry for POST without opt-in)", got)
	}
}

func TestRetryRoundTripper_POST_with_GetBody_and_opt_in(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bodyContent := "hello-body"
	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, RetryNonIdempotent: true})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(bodyContent))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(bodyContent)), nil
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
	for i, b := range bodies {
		if b != bodyContent {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, bodyContent)
		}
	}
}

func TestRetryRoundTripper_POST_without_GetBody_no_retry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, RetryNonIdempotent: true})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("data"))
	req.GetBody = nil
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry without GetBody)", got)
	}
}

// TestRetryRoundTripper_keyed_request pins the Idempotency-Key rule without
// RetryNonIdempotent: a non-empty key makes any method retryable, a body still
// needs GetBody, and every attempt resends the same key and body.
func TestRetryRoundTripper_keyed_request(t *testing.T) {
	const body = "post-payload"
	rewindable := func() io.ReadCloser { return io.NopCloser(strings.NewReader(body)) }
	for _, tc := range []struct {
		name      string
		method    string
		key       *string
		withBody  bool
		getBody   bool
		wantCalls int32
	}{
		{"keyed POST with GetBody", http.MethodPost, new("k-1"), true, true, 3},
		{"keyed PATCH with no body", http.MethodPatch, new("k-2"), false, false, 3},
		{"keyed DELETE with no body", http.MethodDelete, new("k-3"), false, false, 3},
		{"keyed POST without GetBody", http.MethodPost, new("k-4"), true, false, 1},
		{"empty key is no key", http.MethodPost, new(""), true, true, 1},
		{"no key", http.MethodPost, nil, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var keys, bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				bodies = append(bodies, string(b))
				mu.Unlock()
				if calls.Add(1) <= 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4})

			var reqBody io.Reader = http.NoBody
			if tc.withBody {
				reqBody = strings.NewReader(body)
			}
			req, _ := http.NewRequest(tc.method, srv.URL, reqBody)
			req.GetBody = nil
			if tc.getBody {
				req.GetBody = func() (io.ReadCloser, error) { return rewindable(), nil }
			}
			if tc.key != nil {
				req.Header.Set("Idempotency-Key", *tc.key)
			}
			resp, err := (&http.Client{Transport: rt}).Do(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resp.Body.Close()
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", got, tc.wantCalls)
			}
			mu.Lock()
			defer mu.Unlock()
			for i := range keys {
				if tc.key != nil && keys[i] != *tc.key {
					t.Errorf("attempt %d Idempotency-Key = %q, want %q", i+1, keys[i], *tc.key)
				}
				if tc.withBody && bodies[i] != body {
					t.Errorf("attempt %d body = %q, want %q", i+1, bodies[i], body)
				}
			}
		})
	}
}

func TestRetryRoundTripper_DELETE_no_body_with_opt_in(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, RetryNonIdempotent: true})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestRetryRoundTripper_PUT_with_bytes_buffer_GetBody(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "payload" {
			t.Errorf("body = %q, want %q", b, "payload")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := []byte("payload")
	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, RetryNonIdempotent: true})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodPut, srv.URL, bytes.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRetryRoundTripper_no_retry_on_4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry for 404)", got)
	}
}

func TestRetryRoundTripper_OnRetry_hook(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var hookCalls int
	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, OnRetry: func(attempt int, _ *http.Request, resp *http.Response, _ error) {
		hookCalls++
		if resp == nil {
			t.Error("OnRetry: resp should not be nil for HTTP error")
		}
		if attempt != hookCalls {
			t.Errorf("OnRetry: attempt = %d, want %d", attempt, hookCalls)
		}
	}})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if hookCalls != 2 {
		t.Errorf("OnRetry called %d times, want 2", hookCalls)
	}
}

func TestRetryRoundTripper_CheckRetry_custom(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, CheckRetry: func(_ context.Context, _ *http.Response, _ error) (bool, error) {
		return false, nil
	}})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (custom policy says no retry)", got)
	}
}

func TestRetryRoundTripper_CheckRetry_error_shortcircuit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	wantErr := errors.New("policy abort")
	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, CheckRetry: func(_ context.Context, _ *http.Response, _ error) (bool, error) {
		return false, wantErr
	}})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL) //nolint:bodyclose // error path, no body
	_ = resp
	if err == nil {
		t.Fatal("expected error from CheckRetry")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapping %v", err, wantErr)
	}
}

func TestRetryRoundTripper_PrepareRetry_hook(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.Header.Get("X-Refreshed") != "true" {
			t.Error("PrepareRetry did not set header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, PrepareRetry: func(req *http.Request) error {
		req.Header.Set("X-Refreshed", "true")
		return nil
	}})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRetryRoundTripper_PrepareRetry_error_aborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	wantErr := errors.New("token refresh failed")
	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, PrepareRetry: func(_ *http.Request) error {
		return wantErr
	}})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL) //nolint:bodyclose // error path, no body
	_ = resp
	if err == nil {
		t.Fatal("expected error from PrepareRetry")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapping %v", err, wantErr)
	}
}

func TestRetryRoundTripper_context_cancellation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: 100 * time.Millisecond, MaxAttempts: 11})
	client := &http.Client{Transport: rt}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	resp, err := client.Do(req) //nolint:bodyclose // error path, no body
	_ = resp
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if got := calls.Load(); got > 3 {
		t.Errorf("calls = %d, want <= 3 with context cancellation", got)
	}
}

// Each attempt carries the caller's context, so cancelling it aborts an
// attempt stalled inside the base transport rather than waiting it out.
func TestRetryClient_context_cancel_aborts_stalled_attempt(t *testing.T) {
	var calls atomic.Int32
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		arrived <- struct{}{}
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	client := httpclient.NewRetry(srv.Client().Transport, httpclient.RefuseAllRedirects, httpclient.TransportConfig{
		BaseDelay:   time.Millisecond,
		MaxAttempts: 3,
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		resp, doErr := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- doErr
	}()

	<-arrived
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("client.Do after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client.Do still blocked 5s after the request context was cancelled mid-attempt")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server calls = %d, want 1 (a cancelled context starts no further attempt)", got)
	}
}

func TestRetryRoundTripper_exhausts_retries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	rt := httpclient.NewRetryRoundTripper(srv.Client().Transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (last response)", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (1 initial + 2 retries)", got)
	}
}

func TestRetryRoundTripper_HEAD_retried(t *testing.T) {
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
	client := &http.Client{Transport: rt}

	resp, err := client.Head(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

// TestRetryRoundTripper_safe_methods_retried pins the default method set at
// the safe methods of RFC 9110 section 9.2.1.
func TestRetryRoundTripper_safe_methods_retried(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: http.NoBody}, nil
			}), httpclient.TransportConfig{BaseDelay: time.Nanosecond, MaxAttempts: 2})
			req, err := http.NewRequestWithContext(t.Context(), method, "http://example.com/safe", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip(%s) err = %v", method, err)
			}
			resp.Body.Close()
			if got := calls.Load(); got != 2 {
				t.Errorf("RoundTrip(%s) calls = %d, want 2", method, got)
			}
		})
	}
}

// TestRetryRoundTripper_base_breaking_the_RoundTripper_contract pins that a
// base transport answering a nil response, or a response with a nil Body,
// gets that answer back without a panic.
func TestRetryRoundTripper_base_breaking_the_RoundTripper_contract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		resp      *http.Response
		wantCalls int32
	}{
		{"nil_response", nil, 1},
		{"nil_body", &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return tc.resp, nil
			}), httpclient.TransportConfig{BaseDelay: time.Nanosecond, MaxAttempts: 2})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/contract", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(req)
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			if err != nil || resp != tc.resp {
				t.Errorf("RoundTrip = %v, %v, want the base's answer and nil", resp, err)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("base calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestRetryRoundTripper_CheckRetry_error_closes_response pins that a policy
// error discards the response it judged and closes its body.
func TestRetryRoundTripper_CheckRetry_error_closes_response(t *testing.T) {
	t.Parallel()
	var closed atomic.Int32
	policyErr := errors.New("policy refused")
	rt := httpclient.NewRetryRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       &trackingCloser{Reader: strings.NewReader("judged"), onClose: func() { closed.Add(1) }},
		}, nil
	}), httpclient.TransportConfig{CheckRetry: func(context.Context, *http.Response, error) (bool, error) {
		return false, policyErr
	}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/policy", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
		t.Fatalf("RoundTrip resp = %d, want nil with the policy error", resp.StatusCode)
	}
	if !errors.Is(err, policyErr) {
		t.Errorf("RoundTrip err = %v, want %v", err, policyErr)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("body closes = %d, want 1", got)
	}
}

func TestRetryRoundTripper_Permanent_stops_retry(t *testing.T) {
	var calls atomic.Int32
	permErr := httpclient.Permanent(errors.New("do not retry"))
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, permErr
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 6})
	client := &http.Client{Transport: rt}

	_, err := client.Get("http://example.com/test") //nolint:bodyclose // error path returns no body
	if err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (a Permanent mark should not retry)", got)
	}
}

func TestRetryRoundTripper_GetBody_error_aborts_retry(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{},
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4, RetryNonIdempotent: true})
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/x", strings.NewReader("payload"))
	wantErr := errors.New("rewind boom")
	req.GetBody = func() (io.ReadCloser, error) { return nil, wantErr }
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip with failing GetBody = nil, want error")
	}
	if !strings.Contains(err.Error(), "rewind request body") {
		t.Errorf("error = %v, want containing rewind request body", err)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapping the GetBody error", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("transport calls = %d, want 1", got)
	}
}

func TestRetryRoundTripper_cancel_after_retryable_response_skips_onRetry(t *testing.T) {
	var calls, closed atomic.Int32
	var onRetryCalls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       &trackingCloser{Reader: strings.NewReader(""), onClose: func() { closed.Add(1) }},
			Header:     http.Header{},
		}, nil
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Hour, MaxAttempts: 3, OnRetry: func(int, *http.Request, *http.Response, error) {
		onRetryCalls.Add(1)
	}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/cancel", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("transport calls = %d, want 1", got)
	}
	if got := onRetryCalls.Load(); got != 0 {
		t.Errorf("OnRetry calls = %d, want 0 (no retry once context is cancelled)", got)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("discarded body closes = %d, want 1", got)
	}
}

// TestRetryRoundTripper_defaultCheckRetry_status_table pins the built-in
// policy at IsRetryableStatus: 408, 429 and 5xx retry; 2xx, other 4xx, the
// repeating 5xx answers and an invalid 6xx do not.
func TestRetryRoundTripper_defaultCheckRetry_status_table(t *testing.T) {
	t.Parallel()
	statusCalls := func(t *testing.T, code, wantCalls int) {
		t.Helper()
		var calls atomic.Int32
		transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: code,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
			}, nil
		})
		rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3})
		req, _ := http.NewRequest(http.MethodGet, "http://example.com", http.NoBody)
		resp, _ := rt.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		if got := int(calls.Load()); got != wantCalls {
			t.Errorf("code %d: calls = %d, want %d", code, got, wantCalls)
		}
	}
	for _, code := range []int{200, 400, 401, 403, 404, 501, 505, 511, 600} {
		statusCalls(t, code, 1) // not retried
	}
	for _, code := range []int{408, 429, 500, 502, 503, 504} {
		statusCalls(t, code, 3) // retried to exhaustion
	}
}

// TestRetryRoundTripper_nil_callbacks_no_panic verifies that explicitly passing
// nil hooks (WithPrepareRetry(nil)/WithOnRetry(nil)/WithCheckRetry(nil)) is safe:
// nil onRetry/prepareRetry are skipped and a nil checkRetry falls back to the
// default policy.
func TestRetryRoundTripper_nil_callbacks_no_panic(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if calls.Add(1) < 2 {
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

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 3, PrepareRetry: nil, OnRetry: nil, CheckRetry: nil})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/nilcb", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("nil callbacks caused error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestRetryRoundTripper_nil_transport_does_not_panic verifies the constructor
// falls back to http.DefaultTransport when next is nil.
func TestRetryRoundTripper_nil_transport_does_not_panic(t *testing.T) {
	t.Parallel()
	rt := httpclient.NewRetryRoundTripper(nil, httpclient.TransportConfig{MaxAttempts: 1})
	if rt == nil {
		t.Fatal("NewRetryRoundTripper(nil, httpclient.TransportConfig{}) returned nil")
	}
}

// TestRetryRoundTripper_maxAttempts_zero_with_custom_checkRetry verifies the
// attempt clamp dominates: even an always-retry custom policy cannot force a
// second attempt when maxAttempts clamps to 1.
func TestRetryRoundTripper_maxAttempts_zero_with_custom_checkRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})
	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{MaxAttempts: -1, CheckRetry: func(_ context.Context, _ *http.Response, _ error) (bool, error) {
		return true, nil // always retry — but a negative MaxAttempts means one attempt
	}})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/zero-custom", http.NoBody)
	_, err := rt.RoundTrip(req) //nolint:bodyclose // error path, no response body
	if err == nil {
		t.Fatal("expected error from the single clamped attempt")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TransportConfig{MaxAttempts: -1}+custom CheckRetry: calls=%d, want 1 (a single attempt)", got)
	}
}

// TestRetryRoundTripper_drains_every_discarded_response_body verifies each
// discarded retry response is drained-and-closed: 3 intermediate + 1 final.
func TestRetryRoundTripper_drains_every_discarded_response_body(t *testing.T) {
	t.Parallel()
	var closed atomic.Int32
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body: &trackingCloser{
				Reader:  strings.NewReader("response-body-data"),
				onClose: func() { closed.Add(1) },
			},
			Header: http.Header{},
		}, nil
	})

	rt := httpclient.NewRetryRoundTripper(transport, httpclient.TransportConfig{BaseDelay: time.Millisecond, MaxAttempts: 4})
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if got := closed.Load(); got != 4 {
		t.Errorf("closed count = %d, want 4 (3 intermediate + 1 final)", got)
	}
}

// idleCounter is a round tripper that counts the calls to close its idle
// connections.
type idleCounter struct {
	http.RoundTripper
	closed atomic.Int32
}

func (c *idleCounter) CloseIdleConnections() { c.closed.Add(1) }

func TestClosingARetryClientsIdleConnectionsReachesItsBase(t *testing.T) {
	base := &idleCounter{RoundTripper: http.DefaultTransport}
	httpclient.NewRetry(base, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{}).CloseIdleConnections()
	if got := base.closed.Load(); got != 1 {
		t.Errorf("the base transport's idle connections were closed %d time(s), want 1", got)
	}
}
