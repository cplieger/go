package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// TestStatusError_entry_points pins the one error both status doors return:
// its code, its URL, its raw Retry-After, which sentinels it matches and
// whether IsTransient retries it, for every status band.
func TestStatusError_entry_points(t *testing.T) {
	t.Parallel()
	const reqURL = "http://example.com/items?key=k"
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrUnauthorized", httpclient.ErrUnauthorized},
		{"ErrForbidden", httpclient.ErrForbidden},
		{"ErrRateLimited", httpclient.ErrRateLimited},
		{"ErrServerError", httpclient.ErrServerError},
	}
	tests := []struct {
		name           string
		retryAfter     string
		want           error
		code           int
		wantRetryAfter time.Duration
		wantTransient  bool
	}{
		{name: "101 switching protocols", code: 101},
		{name: "301 redirect", code: 301},
		{name: "304 not modified", code: 304},
		{name: "400 bad request", code: 400},
		{name: "401 unauthorized", code: 401, want: httpclient.ErrUnauthorized},
		{name: "403 forbidden", code: 403, want: httpclient.ErrForbidden},
		{name: "404 not found", code: 404},
		{name: "408 request timeout", code: 408, wantTransient: true},
		{name: "429 rate limited", code: 429, want: httpclient.ErrRateLimited},
		{name: "429 with Retry-After", code: 429, retryAfter: "30", want: httpclient.ErrRateLimited, wantRetryAfter: 30 * time.Second},
		{name: "429 with Retry-After past the cap", code: 429, retryAfter: "3600", want: httpclient.ErrRateLimited, wantRetryAfter: time.Hour},
		{name: "499 top of 4xx", code: 499},
		{name: "500 internal error", code: 500, want: httpclient.ErrServerError, wantTransient: true},
		{name: "501 not implemented", code: 501, want: httpclient.ErrServerError},
		{name: "503 with Retry-After", code: 503, retryAfter: "7", want: httpclient.ErrServerError, wantRetryAfter: 7 * time.Second, wantTransient: true},
		{name: "511 network auth", code: 511, want: httpclient.ErrServerError},
		{name: "599 top of 5xx", code: 599, want: httpclient.ErrServerError, wantTransient: true},
		{name: "600 past the status range", code: 600, want: httpclient.ErrServerError},
	}
	newResp := func(code int, retryAfter string) *http.Response {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("body"))}
	}
	doors := []struct {
		call    func(code int, retryAfter string) error
		name    string
		wantURL string
	}{
		{name: "CheckStatus", call: func(code int, retryAfter string) error {
			resp := newResp(code, retryAfter)
			defer resp.Body.Close()
			return httpclient.CheckStatus(resp)
		}},
		{name: "GetBytes", wantURL: reqURL, call: func(code int, retryAfter string) error {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return newResp(code, retryAfter), nil
			})}
			_, err := httpclient.GetBytes(t.Context(), client, reqURL,
				httpclient.WithMaxAttempts(1), httpclient.WithLogger(quiet))
			return err
		}},
	}
	for _, door := range doors {
		for _, tt := range tests {
			t.Run(door.name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				err := door.call(tt.code, tt.retryAfter)
				se, ok := errors.AsType[*httpclient.StatusError](err)
				if !ok {
					t.Fatalf("%s(%d) = %T %v, want a *StatusError in the chain", door.name, tt.code, err, err)
				}
				if se.Code != tt.code {
					t.Errorf("Code = %d, want %d", se.Code, tt.code)
				}
				if se.URL != door.wantURL {
					t.Errorf("URL = %q, want %q", se.URL, door.wantURL)
				}
				if se.RetryAfter != tt.wantRetryAfter {
					t.Errorf("RetryAfter = %v, want %v", se.RetryAfter, tt.wantRetryAfter)
				}
				for _, s := range sentinels {
					if got, want := errors.Is(err, s.err), s.err == tt.want; got != want {
						t.Errorf("errors.Is(err, %s) = %v, want %v", s.name, got, want)
					}
				}
				if got := httpclient.IsTransient(err); got != tt.wantTransient {
					t.Errorf("IsTransient = %v, want %v", got, tt.wantTransient)
				}
			})
		}
	}
}

// TestCheckStatus_success_band pins the 2xx-only success window at both edges.
func TestCheckStatus_success_band(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		code    int
		wantNil bool
	}{
		{"100 Continue", http.StatusContinue, false},
		{"101 Switching Protocols", http.StatusSwitchingProtocols, false},
		{"199 below 2xx", 199, false},
		{"200 OK", 200, true},
		{"201 Created", 201, true},
		{"204 No Content", 204, true},
		{"299 top of 2xx", 299, true},
		{"300 first non-success", 300, false},
		{"307 Temporary Redirect", 307, false},
		{"399 below 400", 399, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := httpclient.CheckStatus(&http.Response{StatusCode: tt.code, Header: http.Header{}})
			if tt.wantNil {
				if err != nil {
					t.Errorf("CheckStatus(%d) = %v, want nil", tt.code, err)
				}
				return
			}
			if se, ok := errors.AsType[*httpclient.StatusError](err); !ok || se.Code != tt.code {
				t.Errorf("CheckStatus(%d) = %v, want *StatusError{Code: %d}", tt.code, err, tt.code)
			}
		})
	}
}

func TestCheckStatus_429_parses_http_date(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", future)
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}
	se, ok := errors.AsType[*httpclient.StatusError](httpclient.CheckStatus(resp))
	if !ok {
		t.Fatal("CheckStatus(429) returned no *StatusError")
	}
	if se.RetryAfter < 30*time.Second || se.RetryAfter > 60*time.Second {
		t.Errorf("RetryAfter = %v, want ~45s", se.RetryAfter)
	}
}

// TestCheckStatus_3xx_classification pins that a surfaced redirect is not
// retried, matches no sentinel and carries no URL for LogSafeError to strip.
func TestCheckStatus_3xx_classification(t *testing.T) {
	t.Parallel()
	for _, code := range []int{300, 301, 302, 303, 304, 307, 308, 399} {
		err := httpclient.CheckStatus(&http.Response{StatusCode: code, Header: http.Header{}})
		if httpclient.IsTransient(err) {
			t.Errorf("IsTransient(HTTP %d) = true, want false (a redirect is not a retryable server failure)", code)
		}
		if errors.Is(err, httpclient.ErrServerError) {
			t.Errorf("errors.Is(HTTP %d, ErrServerError) = true, want false", code)
		}
		if got := httpclient.LogSafeError(err); got != err {
			t.Errorf("LogSafeError(HTTP %d) = %v, want the error unchanged", code, got)
		}
		if got, want := err.Error(), fmt.Sprintf("HTTP %d", code); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	}
}

// TestIsRetryableStatus pins IsRetryableStatus's verdict code by code, invalid codes outside 100-599 included.
func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want bool
	}{
		{"408 Request Timeout", http.StatusRequestTimeout, true},
		{"429 Too Many Requests", http.StatusTooManyRequests, true},
		{"500 Internal Server Error", http.StatusInternalServerError, true},
		{"502 Bad Gateway", http.StatusBadGateway, true},
		{"503 Service Unavailable", http.StatusServiceUnavailable, true},
		{"504 Gateway Timeout", http.StatusGatewayTimeout, true},
		{"507 Insufficient Storage", http.StatusInsufficientStorage, true},
		{"520 unregistered", 520, true},
		{"599 top of 5xx", 599, true},
		{"501 Not Implemented", http.StatusNotImplemented, false},
		{"505 HTTP Version Not Supported", http.StatusHTTPVersionNotSupported, false},
		{"506 Variant Also Negotiates", http.StatusVariantAlsoNegotiates, false},
		{"508 Loop Detected", http.StatusLoopDetected, false},
		{"510 Not Extended", http.StatusNotExtended, false},
		{"511 Network Authentication Required", http.StatusNetworkAuthenticationRequired, false},
		{"600 invalid", 600, false},
		{"999 invalid", 999, false},
		{"000 invalid", 0, false},
		{"099 invalid", 99, false},
		{"200 OK", http.StatusOK, false},
		{"204 No Content", http.StatusNoContent, false},
		{"301 Moved Permanently", http.StatusMovedPermanently, false},
		{"304 Not Modified", http.StatusNotModified, false},
		{"400 Bad Request", http.StatusBadRequest, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"403 Forbidden", http.StatusForbidden, false},
		{"404 Not Found", http.StatusNotFound, false},
		{"409 Conflict", http.StatusConflict, false},
		{"499 top of 4xx", 499, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := httpclient.IsRetryableStatus(tt.code); got != tt.want {
				t.Errorf("IsRetryableStatus(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestIsRetryableStatus_agrees_with_the_GetBytes_loop is the delegation pin:
// the exported predicate is the SAME decision the built-in retry makes, so for
// every status code on the wire the door must retry exactly when the predicate
// says retryable. It sweeps every three-digit code net/http parses from a
// status line (000-999) against the door's real behavior — a second attempt is
// made or it is not — rather than against a second copy of the table, so
// re-inlining a divergent guard in getAttempt fails here even though the
// predicate alone would still look right.
func TestIsRetryableStatus_agrees_with_the_GetBytes_loop(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for code := range 1000 {
		var calls atomic.Int32
		client := &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: code,
					Body:       io.NopCloser(strings.NewReader("body")),
					Header:     http.Header{},
				}, nil
			}),
		}
		// Two attempts: a retryable status spends both, a settled one returns
		// after the first, so the observed call count IS the door's verdict.
		_, _ = httpclient.GetBytes(t.Context(), client, "http://example.com/sweep",
			httpclient.WithMaxAttempts(2), httpclient.WithBaseDelay(time.Microsecond), httpclient.WithLogger(quiet))
		retried := calls.Load() == 2
		if want := httpclient.IsRetryableStatus(code); retried != want {
			t.Errorf("HTTP %d: GetBytes retried = %v, IsRetryableStatus = %v (the door and the predicate must agree)",
				code, retried, want)
		}
	}
}

func TestStatusError_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"with URL, query redacted", "http://example.com/x?key=k", "HTTP 503 from http://example.com/x?key=REDACTED"},
		{"without URL", "", "HTTP 503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (&httpclient.StatusError{Code: 503, URL: tt.url}).Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusError_Is_unrelated_target_returns_false(t *testing.T) {
	se := &httpclient.StatusError{Code: http.StatusTooManyRequests, URL: "http://example.com"}
	if errors.Is(se, io.EOF) {
		t.Error("errors.Is(StatusError{429}, io.EOF) = true, want false")
	}
	if errors.Is(se, context.Canceled) {
		t.Error("errors.Is(StatusError{429}, context.Canceled) = true, want false")
	}
}
