package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// CheckRetry is the signature for a retry policy function.
// It receives the context, the response (may be nil on transport error), and
// the error (nil on successful response). It returns whether to retry and an
// optional error to short-circuit with.
// Mirrors hashicorp/go-retryablehttp CheckRetry.
type CheckRetry func(ctx context.Context, resp *http.Response, err error) (bool, error)

// OnRetry is called before each retry attempt (not the initial request).
// attempt is 1-indexed (first retry = 1). resp may be nil on transport errors.
type OnRetry func(attempt int, req *http.Request, resp *http.Response, err error)

// PrepareRetry is called before each retry to allow request mutation
// (e.g., re-signing with a fresh token). Mirrors go-retryablehttp PrepareRetry.
type PrepareRetry func(req *http.Request) error

// TransportConfig configures a RetryRoundTripper (and, through NewRetryClient,
// the retrying client). The zero value is ready to use: three total attempts,
// one-second base delay, the default retry policy, no hooks, no elapsed ceiling, no
// non-idempotent replay.
type TransportConfig struct {
	// CheckRetry overrides the retry policy. nil means the default policy:
	// an error [IsTransient] accepts, or a status [IsRetryableStatus] accepts.
	CheckRetry CheckRetry
	// OnRetry is a hook called before each retry attempt, the transport's only
	// observability seam (it logs nothing itself).
	OnRetry OnRetry
	// PrepareRetry is called before each retry to mutate the cloned request
	// (e.g. refresh an auth token).
	PrepareRetry PrepareRetry
	// MaxAttempts is the TOTAL attempt count including the initial request.
	// Zero means unset and takes DefaultMaxAttempts (3); a NEGATIVE value
	// means exactly one attempt (a zero struct field cannot distinguish absent
	// from zero, so try-once is spelled as a negative).
	MaxAttempts int
	// BaseDelay is the initial backoff delay; non-positive means
	// DefaultBaseDelay (1s). Waits are equal-jitter with overflow-safe
	// doubling, and a retryable response's Retry-After header (capped at
	// RetryAfterCap) overrides the computed wait.
	BaseDelay time.Duration
	// MaxElapsedTime is a hard ceiling on total time across retries,
	// including any honored Retry-After: when now+wait would meet or pass it,
	// the round-tripper aborts with an error instead of sleeping. It is
	// checked BETWEEN attempts and cannot interrupt a stalled in-flight
	// attempt; bound single attempts on the base transport (e.g.
	// ResponseHeaderTimeout) and the whole call with the request context.
	// Zero means no ceiling.
	MaxElapsedTime time.Duration
	// RetryNonIdempotent extends retries from the safe methods to every
	// method (POST, PUT, PATCH, DELETE and the rest).
	RetryNonIdempotent bool
}

// RetryRoundTripper implements http.RoundTripper with automatic retry.
//
// By default only the safe methods (GET, HEAD, OPTIONS, TRACE) are retried;
// TransportConfig.RetryNonIdempotent extends that to every method. Either way
// a request whose body has no GetBody to rewind it is sent once.
//
// Unlike hashicorp/go-retryablehttp, it takes a plain *http.Request and counts
// total attempts (TransportConfig.MaxAttempts), not retries.
type RetryRoundTripper struct {
	next               http.RoundTripper
	checkRetry         CheckRetry
	onRetry            OnRetry
	prepareRetry       PrepareRetry
	baseDelay          time.Duration
	maxAttempts        int
	maxElapsedTime     time.Duration
	retryNonIdempotent bool
}

// NewRetryRoundTripper creates a RetryRoundTripper wrapping next with the
// given configuration. If next is nil, http.DefaultTransport is used.
// TransportConfig{} gives the defaults (see its field docs).
func NewRetryRoundTripper(next http.RoundTripper, cfg TransportConfig) *RetryRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &RetryRoundTripper{
		next:               next,
		checkRetry:         cfg.CheckRetry,
		onRetry:            cfg.OnRetry,
		prepareRetry:       cfg.PrepareRetry,
		baseDelay:          cfg.BaseDelay,
		maxAttempts:        cfg.MaxAttempts,
		maxElapsedTime:     cfg.MaxElapsedTime,
		retryNonIdempotent: cfg.RetryNonIdempotent,
	}
}

// isSafeMethod reports a safe method (RFC 9110 section 9.2.1); an empty
// method is GET.
func isSafeMethod(method string) bool {
	switch method {
	case "", http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// canRetry reports whether this request may be sent more than once: its
// method allows it, and a body it carries can be rewound through GetBody.
func (rt *RetryRoundTripper) canRetry(req *http.Request) bool {
	if !isSafeMethod(req.Method) && !rt.retryNonIdempotent {
		return false
	}
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func defaultCheckRetry(_ context.Context, resp *http.Response, err error) (bool, error) {
	return retryableRoundTrip(resp, err), nil
}

// retryableRoundTrip is the default verdict on one round trip, GetBytes's and
// the transport's: a transport error [IsTransient] accepts, or a status
// [IsRetryableStatus] accepts.
func retryableRoundTrip(resp *http.Response, err error) bool {
	if err != nil {
		return IsTransient(err)
	}
	return resp != nil && IsRetryableStatus(resp.StatusCode)
}

// retryAfterOf returns resp's Retry-After, capped at RetryAfterCap by
// ParseRetryAfter so a hostile header cannot force an unbounded wait, or zero.
func retryAfterOf(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	return ParseRetryAfter(resp.Header.Get("Retry-After"))
}

// schedule returns the transport's budget: a zero MaxAttempts takes
// DefaultMaxAttempts and a negative one means one attempt.
func (rt *RetryRoundTripper) schedule() schedule {
	s := schedule{attempts: rt.maxAttempts, baseDelay: rt.baseDelay, maxElapsed: rt.maxElapsedTime}
	switch {
	case s.attempts == 0:
		s.attempts = DefaultMaxAttempts
	case s.attempts < 0:
		s.attempts = 1
	}
	if s.baseDelay <= 0 {
		s.baseDelay = DefaultBaseDelay
	}
	return s
}

func (rt *RetryRoundTripper) transport() http.RoundTripper {
	if rt.next != nil {
		return rt.next
	}
	return http.DefaultTransport
}

func (rt *RetryRoundTripper) getCheckRetry() CheckRetry {
	if rt.checkRetry != nil {
		return rt.checkRetry
	}
	return defaultCheckRetry
}

// RoundTrip implements http.RoundTripper: it retries an eligible request on a
// failure its policy accepts, honoring Retry-After on every retried status
// (RFC 9110 defines it for 503 and 3xx, RFC 6585 for 429), and never mutates req.
//
// When retries run out the last response comes back with a nil error, even a
// retryable 503: the caller must close its body and check resp.StatusCode.
func (rt *RetryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !rt.canRetry(req) {
		return rt.roundTripOnce(req)
	}
	ctx := req.Context()
	check := rt.getCheckRetry()
	var resp *http.Response
	var err error
	_, stopErr := rt.schedule().run(ctx, func(n int) (bool, time.Duration) {
		tryReq, buildErr := rt.buildAttemptRequest(ctx, req, n)
		if buildErr != nil {
			resp, err = nil, buildErr
			return true, 0
		}
		resp, err = rt.roundTripOnce(tryReq) //nolint:bodyclose // the body goes back to the caller, or drainResp closes it in pause or on a stop
		retry, checkErr := check(ctx, resp, err)
		switch {
		case checkErr != nil:
			drainResp(resp)
			resp, err = nil, checkErr
			return true, 0
		case !retry:
			return true, 0
		case ctx.Err() != nil:
			drainResp(resp)
			resp, err = nil, ctx.Err()
			return true, 0
		}
		return false, retryAfterOf(resp)
	}, func(retry int, _ time.Duration) {
		if rt.onRetry != nil {
			rt.onRetry(retry, req, resp, err)
		}
		drainResp(resp)
	})
	switch {
	case errors.Is(stopErr, errElapsed) && err != nil:
		return nil, fmt.Errorf("max elapsed time %s exceeded: %w", rt.maxElapsedTime, err)
	case errors.Is(stopErr, errElapsed):
		return nil, fmt.Errorf("max elapsed time %s exceeded", rt.maxElapsedTime)
	case stopErr != nil:
		return nil, stopErr
	}
	return resp, err
}

// roundTripOnce sends one attempt through the base transport, naming its bare
// io.EOF hang-up so CheckRetry, OnRetry and the caller all see a reset.
func (rt *RetryRoundTripper) roundTripOnce(req *http.Request) (*http.Response, error) {
	resp, err := rt.transport().RoundTrip(req)
	return resp, nameHangUp(err)
}

// buildAttemptRequest clones req for a single attempt. The caller's request is
// never mutated (RoundTripper contract). On retries (attempt > 0) it rewinds the
// body via GetBody and applies the PrepareRetry hook.
func (rt *RetryRoundTripper) buildAttemptRequest(ctx context.Context, req *http.Request, attempt int) (*http.Request, error) {
	tryReq := req.Clone(ctx)
	if attempt == 0 {
		return tryReq, nil
	}
	if req.GetBody != nil {
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return nil, fmt.Errorf("rewind request body: %w", bodyErr)
		}
		tryReq.Body = body
	}
	if rt.prepareRetry != nil {
		if prepErr := rt.prepareRetry(tryReq); prepErr != nil {
			return nil, fmt.Errorf("prepare retry: %w", prepErr)
		}
	}
	return tryReq, nil
}

// drainResp drains and closes a response body if present.
func drainResp(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		DrainClose(resp.Body)
	}
}

// NewRetryClient returns an *http.Client whose Transport is a
// RetryRoundTripper over base (nil means http.DefaultTransport) and whose
// CheckRedirect is policy. It panics on a nil policy, because nil means
// net/http's follow-anywhere default; pass DefaultRedirectPolicy,
// RefuseAllRedirects or a RedirectPolicyFunc. It sets no Client.Timeout, which
// would cap the whole retry sequence: bound one attempt on the base transport
// and the total with a context deadline or TransportConfig.MaxElapsedTime.
func NewRetryClient(base http.RoundTripper, policy CheckRedirect, cfg TransportConfig) *http.Client {
	if policy == nil {
		panic("httpclient.NewRetryClient: nil redirect policy (pass DefaultRedirectPolicy, RefuseAllRedirects, or a RedirectPolicyFunc allowlist)")
	}
	return &http.Client{
		Transport:     NewRetryRoundTripper(base, cfg),
		CheckRedirect: policy,
	}
}
