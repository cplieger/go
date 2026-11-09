package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Transient is the interface an error implements to report whether it
// represents a transient (retryable) failure. It embeds error because every
// value this interface can ever name IS one: it is read through
// [errors.As]/[errors.AsType] over an error tree, whose nodes are errors by
// construction, so a non-error implementation is unreachable. Declaring the
// error half makes the type say that, keeps Error() available on a recovered
// value for diagnostics, and is what lets a caller write
// errors.AsType[Transient](err) — [errors.AsType] constrains its type
// parameter to error, where the older [errors.As] accepted any target. Same
// shape as [net.Error].
type Transient interface {
	error
	IsTransient() bool
}

// RetryAfterHint is implemented by errors that carry an explicit wait duration
// for the next retry, typically a parsed and capped Retry-After. When fn's
// returned error is transient AND implements this interface with a positive
// duration, Do waits that duration before the next attempt instead of its
// jittered exponential backoff. The exponential base still advances, so a
// later transient error without a hint resumes the normal progression. The
// hint MUST already be capped by the implementer (e.g. via ParseRetryAfter);
// Do sleeps on it verbatim and applies no ceiling of its own, so an uncapped
// value is an unbounded-wait hazard.
//
// It embeds error for the same reason [Transient] does: the interface is read
// through [errors.As]/[errors.AsType] over an error tree, so it can only ever
// name an error.
type RetryAfterHint interface {
	error
	RetryAfterHint() time.Duration
}

// Sentinels a *[StatusError] matches with errors.Is, by its Code.
var (
	// ErrUnauthorized matches a 401.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden matches a 403.
	ErrForbidden = errors.New("forbidden")
	// ErrRateLimited matches a 429.
	ErrRateLimited = errors.New("rate limited")
	// ErrServerError matches a 5xx, and a status of 600 or more, which RFC 9110
	// section 15 says to process as a 5xx. Which of them are retried is
	// [IsRetryableStatus]'s rule.
	ErrServerError = errors.New("server error")
)

// --- HTTP status errors ---

// StatusError is the error [CheckStatus] and [GetBytes] return for a non-2xx
// response. Match a status class with errors.Is against [ErrUnauthorized],
// [ErrForbidden], [ErrRateLimited] or [ErrServerError], or read Code.
type StatusError struct {
	// rawURL is the URL GetBytes was given, unredacted; Error() renders it
	// redacted. CheckStatus leaves it empty. It is unexported so encoding/json
	// cannot carry its credentials into a log.
	rawURL string
	Code   int
	// RetryAfter is the response's Retry-After, raw and uncapped, zero when
	// absent or malformed. The server controls it (CWE-400): bound it before
	// sleeping on it, as Do's rate-limit options do.
	RetryAfter time.Duration
}

var _ Transient = (*StatusError)(nil)

// GoString renders e for %#v with its URL redacted, as Error renders it, so
// the unexported raw URL cannot reach a debug line either.
func (e *StatusError) GoString() string {
	shown := ""
	if e.rawURL != "" {
		shown = redactURL(e.rawURL)
	}
	return fmt.Sprintf("&httpclient.StatusError{URL:%q, Code:%d, RetryAfter:%s}", shown, e.Code, e.RetryAfter)
}

func (e *StatusError) Error() string {
	if e.rawURL == "" {
		return fmt.Sprintf("HTTP %d", e.Code)
	}
	return fmt.Sprintf("HTTP %d from %s", e.Code, redactURL(e.rawURL))
}

// Is matches the sentinel for e's Code.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Code == http.StatusUnauthorized
	case ErrForbidden:
		return e.Code == http.StatusForbidden
	case ErrRateLimited:
		return e.Code == http.StatusTooManyRequests
	case ErrServerError:
		return e.Code >= 500
	}
	return false
}

// IsTransient reports whether [Do] retries e by default: [IsRetryableStatus]
// less 429, which only a rate-limit option retries.
func (e *StatusError) IsTransient() bool {
	return e.Code != http.StatusTooManyRequests && IsRetryableStatus(e.Code)
}

// newStatusError builds the *StatusError for resp, a non-2xx response to a
// request for reqURL.
func newStatusError(resp *http.Response, reqURL string) *StatusError {
	return &StatusError{rawURL: reqURL, Code: resp.StatusCode, RetryAfter: ParseRetryAfterResponse(resp)}
}

// ResponseTooLargeError is returned by GetBytes when the response body exceeds
// the configured maximum (WithMaxBodyBytes, default 10 MB). The
// body is not returned: a truncated payload indistinguishable from a complete
// one is a silent-corruption hazard, so GetBytes fails loud instead. Limit is
// the cap that was exceeded, mirroring the stdlib *http.MaxBytesError shape.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("response body exceeds %d bytes", e.Limit)
}

// --- Permanent marking ---

// permanentError wraps an error to declare it never retryable. It is
// unexported, like the transient and attempt-timeout marks, because the mark
// is read through [IsPermanent] and [IsTransient] rather than by type; errors.Is
// and errors.As still reach the wrapped error through Unwrap.
type permanentError struct{ Err error }

func (e *permanentError) Error() string { return e.Err.Error() }
func (e *permanentError) Unwrap() error { return e.Err }

// Permanent wraps err so that no retry door repeats it, whatever else it
// carries: [IsTransient] reports false for it, and the rate-limit options do
// not retry a marked 429. Returns nil for nil input. It is the mirror of
// [AttemptTimeout], which forces the opposite verdict.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{Err: err}
}

// IsPermanent reports whether err, or an error it wraps, was marked by
// [Permanent].
func IsPermanent(err error) bool {
	_, ok := errors.AsType[*permanentError](err)
	return ok
}

// --- Transient marking ---

// transientError wraps an error to declare it retryable. It is unexported
// because the mark is read through the [Transient] interface (and so through
// [IsTransient]) rather than by type: a consumer asking "is this retryable"
// must get the same answer for a marked error and for its own Transient
// implementation.
type transientError struct{ Err error }

var _ Transient = (*transientError)(nil)

func (e *transientError) Error() string { return e.Err.Error() }
func (e *transientError) Unwrap() error { return e.Err }

// IsTransient implements Transient: the caller marked this error retryable.
func (*transientError) IsTransient() bool { return true }

// MarkTransient wraps err so [IsTransient] reports true, overriding a
// non-transient verdict already on it, for a failure your operation knows
// self-heals where the status rule cannot see it (a server fault inside a 200
// envelope). [Permanent], a *[StatusError] for 401, 403 or 429 and a caller
// context error still win; for a per-attempt deadline use [AttemptTimeout].
// Returns nil for nil input.
func MarkTransient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{Err: err}
}

// --- AttemptTimeout ---

// attemptTimeoutError marks a timeout as the expiry of a bound that governed
// ONE attempt. It WRAPS the error rather than replacing it, so both things a
// caller needs survive the mark: the cause that says which phase stalled (a
// dial, the TLS handshake, the response headers) and the
// errors.Is(err, context.DeadlineExceeded) match the caller's own callers test
// for. That match is exactly why IsTransient has to consult this mark before
// its caller-context rejection — the marked cause typically CARRIES the
// sentinel, which the rejection reads as terminal.
type attemptTimeoutError struct{ Err error }

func (e *attemptTimeoutError) Error() string { return "attempt timeout: " + e.Err.Error() }
func (e *attemptTimeoutError) Unwrap() error { return e.Err }

// IsTransient implements Transient: a bound that governed a single attempt
// expired, which is a retryable failure by construction.
func (*attemptTimeoutError) IsTransient() bool { return true }

// AttemptTimeout wraps err to declare that the timeout it reports bounded ONE
// ATTEMPT rather than the caller's total budget, making it retryable:
// [IsTransient] reports true for it (checked ahead of the context-error
// rejection), and the retry doors retry it. Returns nil for nil input. It is
// the mirror of [Permanent], which forces the opposite verdict.
//
// It exists because a context deadline alone cannot express the difference. A
// caller's expired deadline means "the budget is gone, stop"; the expiry of a
// per-attempt bound means "this attempt failed, try another" — opposite
// instructions from the same [context.DeadlineExceeded]. Only the code that
// installed the bound knows which one it is, so httpclient cannot infer it and
// defaults to the safe reading (terminal). This mark is how the installer says
// otherwise, and the wrapper deliberately keeps the deadline visible to
// errors.Is instead of hiding it, which was the only way to opt in before.
//
// Reach for it when the per-attempt bound is NOT the retry door's own and
// httpclient cannot see whose bound it was:
//
//   - a [context.WithTimeout] a caller derives itself inside a [Do] callback.
//     Its expiry carries the deadline sentinel, indistinguishable from the
//     caller's own. [WithAttemptTimeout] does this for you, mark included;
//     hand-roll it only when the bound must cover something other than one
//     whole attempt.
//   - a net-level bound such as a [net.Dialer] Timeout. The net package maps
//     every expired context onto one shared "i/o timeout" value, so a
//     *net.OpError or *net.DNSError reporting a deadline cannot say whose it
//     was and stays terminal (see IsTransient).
//
// An [http.Client.Timeout] and a [net/http.Transport] ResponseHeaderTimeout do
// NOT need the mark: net/http reports those through its own timeout error,
// which never carries the sentinel, so [IsTransient] already reads them as
// per-attempt and retries them — including under the [RetryRoundTripper]'s
// default policy. Use [Permanent] (or a [TransportConfig].CheckRetry) if you
// want one of them to stop the loop instead.
//
// The caller owns the judgment: mark ONLY a bound you installed over a single
// attempt. Marking a deadline that came from your own caller makes an
// exhausted budget look retryable, which is the failure mode the default
// classification prevents. [WithAttemptTimeout] avoids the judgment entirely by
// deciding from the two contexts it owns.
func AttemptTimeout(err error) error {
	if err == nil {
		return nil
	}
	return &attemptTimeoutError{Err: err}
}

// IsAttemptTimeout reports whether err (or any wrapped error) was marked by
// [AttemptTimeout] as a per-attempt timeout.
func IsAttemptTimeout(err error) bool {
	_, ok := errors.AsType[*attemptTimeoutError](err)
	return ok
}

// --- Constants ---

// RetryAfterCap is the longest wait ParseRetryAfter returns, so the longest
// Retry-After GetBytes and the retrying transport honor.
// ParseRetryAfterResponse and StatusError.RetryAfter keep the raw value; Do's
// rate-limit options cap it at their maxWait, and a non-positive maxWait takes
// RetryAfterCap. Its value is part of the v1 API, so a caller may derive its
// own bounds from it (a server timeout covering the longest Retry-After wait
// of GetBytes or the transport).
const RetryAfterCap = 60 * time.Second

// The retry and body defaults are unexported so tuning one is not an API
// change; the option and field docs state each value.
const (
	// defaultBaseDelay is the production base for exponential-backoff retry.
	defaultBaseDelay = time.Second
	// defaultMaxAttempts caps the retry doors at three total attempts.
	defaultMaxAttempts = 3
	// defaultMaxBodyBytes caps response bodies at 10 MB.
	defaultMaxBodyBytes int64 = 10 << 20
)

// drainLimit caps body drain reads for connection reuse.
const drainLimit = 64 << 10

// redirectCap is the number of redirects a shipped policy follows by default.
const redirectCap = 5

// --- Retry-After parsing ---

// maxRetryAfterDateLen bounds what is handed to [http.ParseTime].
//
// This is a DELIBERATE REFUSAL of pathological input, not a claim of
// equivalence, and the distinction matters because an earlier version of this
// comment got it wrong. It asserted that no value this refuses could have parsed
// as a date. That is false: time.RFC850's zone element accepts GMT followed by a
// sign and an UNBOUNDED run of digits (parseSignedOffset into leadingInt), so
// "Monday, 02-Jan-38 15:04:05 GMT+" plus forty zeros plus "23" is 73 bytes long
// and parses. Under this bound it returns 0 instead. A 6.2M-execution
// differential fuzz did not find that, because the generator never built a valid
// RFC850 date carrying a zero-padded offset; it was found by reading the
// stdlib's parser.
//
// So the bound trades a behaviour change on input no server sends for the
// removal of a real amplification. Every date format ParseTime accepts is short
// - IMF-fixdate 29 characters, RFC850 with the longest weekday 33, asctime 24 -
// and a legitimate numeric zone offset fits inside that. What is refused is a
// date whose zone offset is padded past 64 bytes, which carries the same
// semantic value as its unpadded spelling.
//
// The amplification being removed is the reason to accept that trade. Each of
// ParseTime's three failed layout attempts CLONES the input into the error it
// returns: measured, ParseTime alone accounts for 6.3 MB of allocation on a 1 MB
// header, and the allocation COUNT stays flat whatever the length, so no metric
// the weekly benchmark tracker charts could see it. The header is
// upstream-controlled and an upstream that is already failing is the one sending
// it.
//
// TestParseRetryAfterRefusesAPaddedZoneOffset pins the divergence so it stays a
// decision rather than becoming a surprise.
const maxRetryAfterDateLen = 64

// looksLikeDeltaSeconds reports whether h has the shape strconv.ParseInt
// accepts at base 10: an optional sign followed by at least one ASCII digit.
//
// It is a pre-filter rather than an optimization of the parse. Handing a long
// non-numeric value straight to ParseInt makes IT clone the whole string into a
// *strconv.NumError that is discarded, which is the same amplification the date
// bound above exists to prevent. A scan costs nothing and there is no length
// bound to be had here, because a delta-seconds value may carry unlimited
// leading zeros and still parse.
func looksLikeDeltaSeconds(h string) bool {
	if h == "" {
		return false
	}
	if h[0] == '+' || h[0] == '-' {
		h = h[1:]
	}
	if h == "" {
		return false
	}
	for i := range len(h) {
		if h[i] < '0' || h[i] > '9' {
			return false
		}
	}
	return true
}

// maxInt64Digits is the number of digits in math.MaxInt64 (9223372036854775807).
// A delta-seconds value with more SIGNIFICANT digits than this cannot fit an
// int64, so strconv.ParseInt would return a range error and the value would end
// up as zero. Deciding that from the digit count instead keeps the oversized
// string away from ParseInt, which clones its whole input into the error it
// builds; leading zeros are stripped first because they are unbounded and carry
// no magnitude.
const maxInt64Digits = 19

// deltaSeconds interprets h, already known to have the delta-seconds shape, as a
// Retry-After wait. Zero for anything non-positive or out of range, matching
// what the two parsers between them used to return for the same input.
func deltaSeconds(h string) time.Duration {
	// Every negative value ends as zero (a wait in the past is no wait), so it
	// never needs parsing at all.
	if h[0] == '-' {
		return 0
	}
	h = strings.TrimPrefix(h, "+")
	// TrimLeft returns a substring, so this costs nothing.
	if h = strings.TrimLeft(h, "0"); h == "" {
		return 0 // all zeros, however many
	}
	if len(h) > maxInt64Digits {
		return 0
	}
	n, err := strconv.ParseInt(h, 10, 64)
	if err != nil {
		// Nineteen digits can still exceed MaxInt64. The clone ParseInt makes
		// here is of a 19-byte string, not of the header.
		return 0
	}
	if n <= 0 {
		return 0
	}
	// int64 guard is correct on 32-bit platforms: ParseInt(...,10,64)
	// keeps parsing and the guard in int64 space, so a large delta-seconds
	// value is capped rather than (as strconv.Atoi did) failing with a
	// range error above the platform int max on GOARCH=386 and falling
	// through to HTTP-date parsing.
	const maxSecs = (1<<63 - 1) / int64(time.Second)
	if n > maxSecs {
		return time.Duration(maxSecs) * time.Second
	}
	return time.Duration(n) * time.Second
}

// parseRetryAfterValue parses a Retry-After header value (delta-seconds or
// HTTP-date) into an uncapped, non-negative duration. Returns zero for
// missing, malformed, or past values. It is the shared core for both
// ParseRetryAfter (capped) and ParseRetryAfterResponse (uncapped).
func parseRetryAfterValue(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if looksLikeDeltaSeconds(h) {
		return deltaSeconds(h)
	}
	if len(h) > maxRetryAfterDateLen {
		return 0
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ParseRetryAfter parses a Retry-After header value (delta-seconds or HTTP-date).
// Returns zero for missing/malformed values. Caps at RetryAfterCap for safety
// (prevents unbounded waits in retry loops). For raw uncapped values, use
// ParseRetryAfterResponse.
func ParseRetryAfter(h string) time.Duration {
	return min(parseRetryAfterValue(h), RetryAfterCap)
}

// ParseRetryAfterResponse parses the Retry-After header from an *http.Response.
// Returns zero if absent or unparseable. Does NOT cap — preserves the raw
// duration for [StatusError]'s RetryAfter, so callers make their own decisions.
// For capped values suitable for retry loops, use ParseRetryAfter. A nil resp
// returns zero.
func ParseRetryAfterResponse(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	return parseRetryAfterValue(resp.Header.Get("Retry-After"))
}

// --- Status checking ---

// CheckStatus returns nil for a 2xx and otherwise a *[StatusError] carrying no
// URL, so its text carries no URL into a log. A 3xx reaches it only from
// a client that does not follow redirects (see [RefuseAllRedirects]) and is
// reported as a failure, not a completed request.
func CheckStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return newStatusError(resp, "")
}

// IsRetryableStatus is the one status rule every retry door applies: true for
// 408, 429 and 500 to 599 less the codes whose answer repeats, false for every
// other status. A code of 600 or more is a server error ([ErrServerError]) but
// not a valid status, so it is not retried. [GetBytes] and the
// [RetryRoundTripper]'s default policy call it on each response, and
// [StatusError.IsTransient] reports it less 429.
func IsRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	// Each is settled by its definition: RFC 9110 sections 15.6.2 and 15.6.6,
	// RFC 2295 section 8.1, RFC 5842 section 7.2, RFC 2774 section 7, RFC 6585
	// section 6. An unregistered 5xx reads as 500 (RFC 9110 section 15).
	case http.StatusNotImplemented, http.StatusHTTPVersionNotSupported,
		http.StatusVariantAlsoNegotiates, http.StatusLoopDetected,
		http.StatusNotExtended, http.StatusNetworkAuthenticationRequired:
		return false
	}
	return code >= 500 && code <= 599
}

// --- Backoff helpers ---

// JitteredBackoff returns a duration in [backoff/2, backoff] using the "equal
// jitter" strategy (per AWS Builders' Library). Full jitter and decorrelated
// jitter are intentionally not provided — equal jitter is the recommended
// default for HTTP retry as it avoids thundering herd while maintaining a
// minimum backoff floor.
func JitteredBackoff(backoff time.Duration) time.Duration {
	if backoff <= 0 {
		return backoff
	}
	// rand.N is generic over any integer kind, so the draw happens in
	// time.Duration itself rather than round-tripping through int64. half is at
	// most MaxInt64/2, so half+1 cannot overflow.
	half := backoff / 2
	return half + rand.N(half+1) //nolint:gosec // G404: jitter, not crypto
}

// SafeDouble doubles a duration, guarding against int64 overflow.
func SafeDouble(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	doubled := d * 2
	if doubled < d {
		return time.Duration(1<<63 - 1)
	}
	return doubled
}

// SleepCtx sleeps for d or returns early on context cancellation.
func SleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	select {
	case <-ctx.Done():
		t.Stop()
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ContextWithDefaultTimeout bounds ctx by def ONLY when ctx carries no
// deadline; a deadline the caller already set is the authoritative budget
// and is never undercut (or extended). A def <= 0 means "no default" and
// leaves ctx unbounded. The returned cancel is always non-nil and must be
// called (a no-op on the passthrough paths), matching context.WithTimeout's
// contract.
//
// It is the per-request timeout rule for an API client: per-attempt bounds
// live on the transport (ResponseHeaderTimeout), the total budget is the
// caller's ctx, and this default applies only when the caller brought no
// budget of its own. Extracted from the identical requestContext helpers in
// the plexapi and arrapi client libraries.
func ContextWithDefaultTimeout(ctx context.Context, def time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok || def <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, def)
}

// --- Transient classification ---

// IsTransient reports whether err is worth another attempt: a [Transient]
// that says so, an [AttemptTimeout] mark, or an error whose [CauseOf] is DNS,
// refused, reset or timeout. A [Permanent] mark, a *[StatusError] for 401, 403
// or 429 and the caller's own expired or canceled context are never
// transient. The caller's deadline is the sentinel itself in the chain, so an
// [http.Client.Timeout] or a ResponseHeaderTimeout, which only match it, are
// per-attempt bounds and retried (see isCallerContextError).
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if IsPermanent(err) {
		return false
	}
	// Checked by code rather than through Transient so a MarkTransient wrapper
	// cannot make a credential or rate-limit answer retryable.
	if se, ok := errors.AsType[*StatusError](err); ok {
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			return false
		}
	}
	// Ordered ahead of the context-error rejection on purpose: the mark exists
	// on errors that DO carry context.DeadlineExceeded (the wrapper keeps the
	// deadline visible to a caller's errors.Is), so the rejection below would
	// otherwise decide first and no mark could ever be seen. Permanent still
	// outranks it above — Permanent(AttemptTimeout(err)) is not retried.
	if IsAttemptTimeout(err) {
		return true
	}
	// A context error means the CALLER's budget is gone: terminal. Only code
	// holding both contexts can tell a caller's deadline from a per-attempt
	// one, which is why nothing here tries to infer it — AttemptTimeout above
	// is how the code that installed the bound says so.
	if isCallerContextError(err) {
		return false
	}
	if t, ok := errors.AsType[Transient](err); ok {
		return t.IsTransient()
	}
	// The transient causes are failures of this connection or this attempt; a
	// TLS refusal and an oversized body repeat identically.
	switch CauseOf(err) {
	case CauseDNS, CauseRefused, CauseReset, CauseTimeout:
		return true
	}
	return false
}

// isCallerContextError reports whether err says the CALLER's context expired
// or was canceled, which is terminal: the budget that authorized the work is
// gone, so another attempt has nothing to spend.
//
// The deadline test is Unwrap-chain IDENTITY, not [errors.Is], because
// errors.Is is unsound for this question. An error can declare
// Is(context.DeadlineExceeded) == true without the sentinel ever appearing in
// its chain, and net/http's own timeout error does exactly that
// (net/http.timeoutError.Is compares the target to context.DeadlineExceeded
// and returns true; the value it reports is built from a message string and
// unwraps to nothing). errors.Is therefore folded the two bounds net/http
// installs ITSELF — an [http.Client.Timeout] and a [net/http.Transport]
// ResponseHeaderTimeout, each of which governs ONE attempt — in with caller
// budget expiry and classified them terminal, so neither was ever retried
// despite being the per-attempt bound this package tells callers to use.
// Identity separates them: a real context expiry carries the sentinel VALUE,
// net/http's timeout only claims to match it.
//
// Two carriers keep the old errors.Is verdict deliberately:
//
//   - Cancellation. [context.Canceled] is tested with errors.Is because the one
//     stdlib type that matches it without being it (net.canceledError, "operation
//     was canceled") is produced ONLY by mapping a genuinely canceled context, so
//     the match is faithful and identity would lose it.
//   - A net-package error reporting a deadline. The net package maps EVERY
//     expired context onto one shared value ("i/o timeout") that likewise
//     matches the deadline without carrying it, so a *net.OpError or
//     *net.DNSError reporting a deadline cannot say whose deadline it was: the
//     caller's, or a net.Dialer.Timeout the transport installed. Unknowable
//     stays terminal — mark it with [AttemptTimeout] to opt a bound you own
//     back in. (Through an *http.Client this case does not arise for a caller
//     deadline: net/http prefers the request context's own error over the dial
//     error whenever that context is done, so the sentinel is present.)
//
// The final line stays on [errors.As] rather than [errors.AsType]: it asks one
// question about two types, and AsType cannot be an expression, so the rewrite
// would split that single boolean fact into two statements and an early return.
// Neither binding is read, so nothing is gained by paying for it.
func isCallerContextError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if unwrapsTo(err, context.DeadlineExceeded) {
		return true
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	return errors.As(err, &opErr) || errors.As(err, &dnsErr)
}

// unwrapsTo reports whether target appears in err's unwrap tree BY IDENTITY,
// consulting no Is method. It is the "is this actually that value" test
// [errors.Is] cannot express once any error in the chain answers Is for a
// value it does not hold (see isCallerContextError).
func unwrapsTo(err, target error) bool {
	for err != nil {
		if err == target { //nolint:errorlint // identity is the whole point: errors.Is would consult Is methods.
			return true
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			return slices.ContainsFunc(x.Unwrap(), func(e error) bool { return unwrapsTo(e, target) })
		default:
			return false
		}
	}
	return false
}

// --- Body helpers ---

// Drain reads and discards up to 64 KB of a response body to enable
// HTTP connection reuse. A failed drain forfeits only that reuse, and is
// reported as a bare Debug line on slog.Default(). The read error itself is
// deliberately NOT logged.
//
// Go 1.27 made HTTP/1 Response.Body drain itself on Close, which raises the
// question of whether this still earns its place. It does, on two measured
// grounds. The stdlib drain is HTTP/1 only — net/http/internal/http2 has no
// equivalent — so on an HTTP/2 response, which every transport this package
// builds negotiates (CloneDefaultTransport keeps ForceAttemptHTTP2), this is the
// only drain there is. And Drain takes an io.ReadCloser, not an *http.Response,
// so it also covers a body the stdlib never wraps. Where both apply they
// compose rather than collide: this drain runs first and, for a body inside the
// 64 KB budget, reaches EOF, which short-circuits the stdlib's drain entirely.
//
// The two limits differ deliberately and do not need reconciling. 64 KB is how
// much effort this function spends; net/http's own budget is 256 KiB with a
// 50 ms deadline, and it applies only when unread content remains AND
// resp.ContentLength <= 256 KiB. That gate is why an over-limit body refused by
// [ReadLimitedBody] reads nothing extra: a response declaring more than 256 KiB
// is excluded outright. Measured on go1.27.0 against go1.26.7, a declared 10 MB
// body read to a 1 MB cap pulls an identical 1,114,112 bytes on both. A CHUNKED
// over-limit body has ContentLength == -1, which passes the gate, so its worst
// case is the cap plus at most 256 KiB more. Either way the caller pays no
// latency for it: the drain runs on net/http's read-loop goroutine, and
// Close() measured 24-44 microseconds against a deliberately stalled chunked
// body that provably passes the gate.
//
// That omission is a CWE-532 fix, not an oversight. A body-read error's text is
// written by the FAR END: net/http renders a malformed chunked trailer as
//
//	malformed MIME header: missing colon: "<remote bytes>"
//
// and for a consumer whose request URL carries its credential in the PATH (a
// webhook token is the canonical shape) an edge that echoes the request URI
// puts that credential in those bytes. Logging it needs only the consumer to be
// running at Debug — the level an operator raises precisely while diagnosing
// failing deliveries. Three properties make the site uncloseable from anywhere
// else, which is why the value is dropped HERE rather than by the caller:
//
//   - Nobody can route it. Drain takes no logger (it is called from defer at
//     consumer call sites and from four sites inside this package, one of them
//     a RoundTripper that has no logger at all), and the retry doors'
//     [WithLogger] governs their own loop lines, not slog.Default().
//   - [LogSafeError] does not reduce it. That boundary is TYPE-based: it strips
//     the *url.Error envelope this package's own machinery adds. A body-read
//     error is not a *url.Error — a malformed trailer surfaces as
//     textproto.ProtocolError, whose entire value IS the message — so the
//     reduction returns it byte-identically.
//   - Redaction needs a secret to redact, and Drain is handed none.
//
// No diagnosis a caller can act on is lost. Drain runs only where the body is
// being thrown away, so the response's own outcome is already reported by the
// path that discarded it (CheckStatus's error, the retry lines,
// ReadLimitedBody's error); a drain error is never the only signal of a
// failure, and no caller can repair a drain. This is the same call the package
// makes in urlErrorCause, which substitutes a contentless stand-in rather than
// log the one field it holds.
func Drain(body io.ReadCloser) {
	if _, err := io.CopyN(io.Discard, body, drainLimit); err != nil && !errors.Is(err, io.EOF) {
		slog.Debug("failed to drain response body")
	}
}

// DrainClose reads remaining bytes (up to drainLimit) from rc before closing it.
// It inherits [Drain]'s logging contract: a failed drain logs a bare Debug line
// carrying no remote-authored text.
func DrainClose(rc io.ReadCloser) {
	Drain(rc)
	rc.Close()
}

// ReadLimitedBody reads body up to limit bytes, always closes body, and returns
// the bytes read. It reads one byte past limit to detect an over-limit body and
// returns *ResponseTooLargeError (with nil bytes) rather than a silently
// truncated payload — a truncated body indistinguishable from a complete one is
// a corruption hazard. A limit of math.MaxInt64 means "effectively unlimited"
// and is guarded against probe-size overflow.
//
// It is the same cap+1 read GetBytes applies internally, exposed for callers
// that issue their own request and decode outside GetBytes but still want the
// fail-loud size bound. On any error the body is already closed.
func ReadLimitedBody(body io.ReadCloser, limit int64) ([]byte, error) {
	defer body.Close()
	probe := limit
	if probe < math.MaxInt64 {
		probe++
	}
	data, err := io.ReadAll(io.LimitReader(body, probe))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &ResponseTooLargeError{Limit: limit}
	}
	return data, nil
}

// --- Redirect allowlist (functional options) ---

// redirectCfg holds internal configuration for the redirect policy.
type redirectCfg struct {
	allowedHosts    []string
	allowedSuffixes []string
	maxHops         int
	sameHost        bool
	sameOrigin      bool
	preserveMethod  bool
}

// RedirectOption configures a redirect policy built by [RedirectPolicy]. It
// is sealed like [DoOption]: only this package's With* redirect options
// implement it.
type RedirectOption interface{ applyRedirect(*redirectCfg) }

// redirectOptionFunc adapts a func to RedirectOption for the shipped options.
type redirectOptionFunc func(*redirectCfg)

func (f redirectOptionFunc) applyRedirect(c *redirectCfg) { f(c) }

// WithAllowedHosts sets the exact hostnames allowed as redirect targets. A
// name matches without regard to ASCII case; an IP address entry, written
// without brackets ("2001:db8::a", "fe80::1%eth0"), matches by address, its
// IPv6 zone exactly. An empty entry matches nothing.
func WithAllowedHosts(hosts ...string) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.allowedHosts = hosts })
}

// WithAllowedSuffixes sets the domain suffixes allowed (e.g. ".docker.com").
// A suffix matches a host name only, never an IP literal.
func WithAllowedSuffixes(suffixes ...string) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.allowedSuffixes = suffixes })
}

// WithMaxHops makes the policy follow at most n redirects in one request and
// refuse the next with [RedirectTooMany]. n <= 0 means the default, 5.
func WithMaxHops(n int) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.maxHops = n })
}

// WithSameHost permits a redirect whose target host equals the original
// request's host, as [WithSameOrigin] compares hosts, beside any allowlist
// entries. It follows that host on any port and an http->https upgrade there,
// so a custom auth header can reach another service on the host;
// [WithSameOrigin] keeps it on one origin. An https->http hop is still
// refused. Later options win, so a later WithSameHost(false) turns the
// permission back off.
func WithSameHost(same bool) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.sameHost = same })
}

// WithSameOrigin permits a redirect to the original request's origin: its
// scheme, its port (absent reads 80 for http, 443 for https) and its host, a
// name compared ASCII case-insensitively and an IP literal by address with its
// IPv6 zone, a local implementation-defined identifier (RFC 4007 section 11.2),
// compared byte for byte. Unlike [WithSameHost] it refuses another port and an
// http->https upgrade. When set, the origin alone decides a hop to the original
// host, whatever [WithSameHost] or [WithAllowedHosts] say; the allowlist and
// the downgrade rule judge other hosts, and a refusal is [RedirectOtherOrigin].
func WithSameOrigin(same bool) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.sameOrigin = same })
}

// WithPreserveMethod refuses, when true, a hop that would change the request
// method, as net/http does to a POST, PUT, PATCH or DELETE across a 301, 302 or
// 303 (RFC 9110 §15.4, https://go.dev/issue/18570). It returns
// [http.ErrUseLastResponse], so the caller gets the 3xx with a nil error, as
// with [RefuseAllRedirects]. The method is compared with the original request
// (via[0]) and an empty via fails closed. The hop cap, target and downgrade
// refusals take precedence, and it grants nothing: with no allowlist,
// [WithSameHost] or [WithSameOrigin] every redirect is still refused.
func WithPreserveMethod(preserve bool) RedirectOption {
	return redirectOptionFunc(func(c *redirectCfg) { c.preserveMethod = preserve })
}

// asciiLower lowercases only ASCII letters A-Z, leaving every other byte
// unchanged. URI schemes and reg-names fold ASCII case (RFC 3986 §6.2.2.1);
// strings.ToLower must NOT be used here because it folds each invalid UTF-8
// byte to U+FFFD, collapsing distinct hosts (e.g. "\xfe" and "\xae") into one
// allowlist-matching equivalence class — a redirect-allowlist bypass. It also
// allocates only when an uppercase letter is present (hostnames are normally
// already lowercase), so the common path is zero-allocation.
func asciiLower(s string) string {
	var b []byte
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

// RedirectReason names the rule of a redirect policy that refused a hop.
type RedirectReason int

const (
	// RedirectNotAllowed means the policy follows no redirect at all (no
	// allowlist, no WithSameHost(true), no WithSameOrigin(true)).
	RedirectNotAllowed RedirectReason = iota + 1
	// RedirectOtherOrigin means the target is neither an allowed host nor, under
	// WithSameHost or WithSameOrigin, the original request's host or origin.
	RedirectOtherOrigin
	// RedirectDowngrade means an https->http hop to an otherwise allowed target.
	RedirectDowngrade
	// RedirectTooMany means the hop would exceed the policy's hop cap.
	RedirectTooMany
)

var redirectReasonNames = [...]string{
	RedirectNotAllowed:  "not_allowed",
	RedirectOtherOrigin: "other_origin",
	RedirectDowngrade:   "downgrade",
	RedirectTooMany:     "too_many",
}

// String returns the reason's snake_case name, as [Cause.String] does, or
// RedirectReason(n) for a value outside the constants.
func (r RedirectReason) String() string {
	if r >= RedirectNotAllowed && int(r) < len(redirectReasonNames) {
		return redirectReasonNames[r]
	}
	return "RedirectReason(" + strconv.Itoa(int(r)) + ")"
}

// RedirectRefusedError is a shipped redirect policy's hard refusal of one hop:
// the hop cap, target permission and downgrade checks of a policy
// [RedirectPolicy] builds and of [DefaultRedirectPolicy] answer one, so a
// caller tells them apart with [errors.As] and Reason alone.
// [RefuseAllRedirects] and a [WithPreserveMethod] refusal answer
// [http.ErrUseLastResponse] instead. An [http.Client] returns it inside a
// [*url.Error], and it is never transient.
type RedirectRefusedError struct {
	// Origin is the refused target's origin, "scheme://host" plus ":port"
	// when the port is not the scheme's default, escaped so [url.Parse]
	// reads it back. It never holds the target's userinfo, path or query, so
	// Error is safe to log for a target that carried a credential there.
	Origin string
	// Reason is the rule that refused the hop.
	Reason RedirectReason
}

// Error renders the refusal from Reason and Origin only.
func (e *RedirectRefusedError) Error() string {
	switch e.Reason {
	case RedirectNotAllowed:
		return "redirects not allowed"
	case RedirectTooMany:
		return "too many redirects"
	case RedirectDowngrade:
		return "refusing scheme downgrade to " + e.Origin
	default:
		return "refusing redirect to " + e.Origin
	}
}

func refuseHop(target *url.URL, reason RedirectReason) *RedirectRefusedError {
	return &RedirectRefusedError{Origin: originOf(target), Reason: reason}
}

// originOf serializes u's origin as a URL [url.Parse] reads back: the
// lowercased scheme, the host as the target wrote it (not case-folded, so a
// caller redacting a credential from Origin finds its bytes), and the port
// only when it is not the default. [url.URL.String] escapes the host as a URL
// does: an IPv6 zone's % reads %25, a non-ASCII byte %XX.
func originOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	scheme := asciiLower(u.Scheme)
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != defaultPort(scheme) {
		host += ":" + port
	}
	return (&url.URL{Scheme: scheme, Host: host}).String()
}

// defaultPort is the port an absent one means for scheme (lowercased), or ""
// for a scheme net/http does not dial.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// hostKey is a host's identity for every redirect decision, comparable with
// ==. An IP literal sets addr only, so it compares by address with its IPv6
// zone exact (RFC 4007 section 11.2 leaves the zone format to the
// implementation, and net resolves a zone by its exact interface name) and no
// name rule, such as a suffix, ever matches it. A reg-name sets name only,
// ASCII-lowercased (RFC 3986 section 6.2.2.1).
type hostKey struct {
	addr netip.Addr
	name string
}

// hostKeyOf returns the identity of hostname as [url.URL.Hostname] gives it,
// its IPv6 zone already percent-decoded and never decoded again, or false
// when it is empty.
func hostKeyOf(hostname string) (hostKey, bool) {
	if hostname == "" {
		return hostKey{}, false
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		return hostKey{addr: addr}, true
	}
	return hostKey{name: asciiLower(hostname)}, true
}

// matchesSuffix reports whether k is a name equal to suffix without its
// leading "." or ending in suffix, which must be dot-prefixed.
func (k hostKey) matchesSuffix(suffix string) bool {
	return k.name != "" && (k.name == suffix[1:] || strings.HasSuffix(k.name, suffix))
}

// originKey is a URL's origin, the RFC 6454 section 4 origin tuple with its
// host compared as hostKey compares it, comparable with ==. port is the
// effective port, 0 when the URL has none and its scheme no default.
type originKey struct {
	host   hostKey
	scheme string
	port   uint16
}

// originKeyOf returns u's origin, or false for a nil or host-less URL or a
// port outside 0-65535, which no dialer reaches.
func originKeyOf(u *url.URL) (originKey, bool) {
	if u == nil {
		return originKey{}, false
	}
	host, ok := hostKeyOf(u.Hostname())
	if !ok {
		return originKey{}, false
	}
	o := originKey{host: host, scheme: asciiLower(u.Scheme)}
	port := u.Port()
	if port == "" {
		port = defaultPort(o.scheme)
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return originKey{}, false
		}
		o.port = uint16(n)
	}
	return o, true
}

// RedirectPolicy returns a [CheckRedirect] configured with the given options;
// a nil option is skipped. A redirect is followed only to an allowed target (a WithAllowedHosts
// entry, a WithAllowedSuffixes match, the original host under WithSameHost, the
// original origin under WithSameOrigin) and never https->http, so a custom
// auth header, which Go forwards across a redirect, never rides a cleartext
// hop. With none of those permissions every redirect is refused. The hop cap
// is WithMaxHops. Every hard refusal is a [*RedirectRefusedError];
// WithPreserveMethod(true) instead answers http.ErrUseLastResponse for a hop
// that would change the method.
func RedirectPolicy(opts ...RedirectOption) CheckRedirect {
	cfg := redirectCfg{}
	for _, o := range opts {
		if o != nil {
			o.applyRedirect(&cfg)
		}
	}
	if len(cfg.allowedHosts) == 0 && len(cfg.allowedSuffixes) == 0 && !cfg.sameHost && !cfg.sameOrigin {
		return func(req *http.Request, _ []*http.Request) error {
			return refuseHop(req.URL, RedirectNotAllowed)
		}
	}
	maxHops := cfg.maxHops
	if maxHops <= 0 {
		maxHops = redirectCap
	}
	rp := &resolvedRedirect{
		allowedHosts:   hostKeysOf(cfg.allowedHosts),
		suffixes:       normalizeSuffixes(cfg.allowedSuffixes),
		maxHops:        maxHops,
		sameHost:       cfg.sameHost,
		sameOrigin:     cfg.sameOrigin,
		preserveMethod: cfg.preserveMethod,
	}
	return rp.check
}

// resolvedRedirect is a compiled redirect policy: RedirectPolicy resolves
// its options into one of these once and returns its check method as the
// http.Client CheckRedirect.
type resolvedRedirect struct {
	allowedHosts   []hostKey
	suffixes       []string
	maxHops        int
	sameHost       bool
	sameOrigin     bool
	preserveMethod bool
}

// check implements the CheckRedirect contract for a resolved policy: it caps
// hops, refuses a target that is neither allowlisted nor (with sameHost or
// sameOrigin) the original request's host or origin, refuses a scheme
// downgrade, and (with preserveMethod) refuses a
// method-changing hop.
func (rp *resolvedRedirect) check(req *http.Request, via []*http.Request) error {
	// net/http calls CheckRedirect with every request sent so far in via, so
	// len(via) is the number of the hop being judged.
	if len(via) > rp.maxHops {
		return refuseHop(req.URL, RedirectTooMany)
	}
	// via[0] is the original request; net/http always populates its URL, but
	// guard against a nil URL so the policy degrades gracefully rather than
	// panicking if invoked with a hand-built via chain.
	var origURL *url.URL
	if len(via) > 0 {
		origURL = via[0].URL
	}
	if !rp.targetAllowed(req.URL, origURL) {
		return refuseHop(req.URL, RedirectOtherOrigin)
	}
	if origURL != nil && isSchemeDowngrade(origURL.Scheme, req.URL.Scheme) {
		return refuseHop(req.URL, RedirectDowngrade)
	}
	// Ordered last on purpose: the refusals above are the stronger security
	// decisions and must keep precedence, because they fail the request with a
	// hard error while this one returns ErrUseLastResponse (a nil-error 3xx the
	// caller must classify). A hop that both leaves the allowlist and changes
	// the method is refused as the allowlist violation it is.
	if rp.preserveMethod && methodChanged(req, via) {
		return http.ErrUseLastResponse
	}
	return nil
}

// methodChanged reports whether req's method differs from the ORIGINAL
// request's (via[0]) — the comparison that also catches a chain whose method
// survives an early 307 and is downgraded by a later 302. An empty Method is
// normalized to GET, which is how net/http itself reads it (redirectBehavior
// rewrites a "" method to "GET" on a 301/302/303 hop), so a hand-built
// zero-value origin request is not a spurious mismatch. An empty via chain has
// no original method to compare against and reports true: the caller of this
// helper fails closed rather than vouching for a hop it cannot verify.
func methodChanged(req *http.Request, via []*http.Request) bool {
	if len(via) == 0 {
		return true
	}
	return effectiveMethod(req.Method) != effectiveMethod(via[0].Method)
}

// effectiveMethod returns the method net/http would use for a request carrying
// method: an empty Request.Method means GET.
func effectiveMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

// targetAllowed reports whether target is an allowed redirect target: under
// sameOrigin the original request's origin decides a hop to its host;
// otherwise an exact or suffix allowlist match, or (with sameHost) the
// original request's own host.
func (rp *resolvedRedirect) targetAllowed(target, origURL *url.URL) bool {
	to, ok := originKeyOf(target)
	if !ok {
		return false
	}
	from, fromOK := originKeyOf(origURL)
	onOrigHost := fromOK && to.host == from.host
	if rp.sameOrigin && onOrigHost {
		return to == from
	}
	if redirectAllowed(to.host, rp.allowedHosts, rp.suffixes) {
		return true
	}
	return rp.sameHost && onOrigHost
}

// isSchemeDowngrade reports whether redirecting from scheme `from` to scheme
// `to` drops transport security (https -> http). Comparison is ASCII
// case-insensitive. A same-scheme redirect and an http->https upgrade are not
// downgrades.
func isSchemeDowngrade(from, to string) bool {
	return strings.EqualFold(from, "https") && strings.EqualFold(to, "http")
}

// normalizeSuffixes dot-anchors and lowercases each allowed redirect suffix so a
// bare "docker.com" cannot be bypassed by a substring match like "evildocker.com".
func normalizeSuffixes(suffixes []string) []string {
	out := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		// A label-less suffix would dot-anchor to a bare ".", which matches
		// every trailing-dot FQDN ("evil.com."): an allowlist bypass.
		if len(s) <= 1 {
			continue
		}
		out = append(out, asciiLower(s))
	}
	return out
}

func hostKeysOf(hosts []string) []hostKey {
	out := make([]hostKey, 0, len(hosts))
	for _, h := range hosts {
		if k, ok := hostKeyOf(h); ok {
			out = append(out, k)
		}
	}
	return out
}

func redirectAllowed(host hostKey, allowedHosts []hostKey, normalizedSuffixes []string) bool {
	return slices.Contains(allowedHosts, host) ||
		slices.ContainsFunc(normalizedSuffixes, host.matchesSuffix)
}

// defaultRedirectPolicy is the compiled same-host policy DefaultRedirectPolicy
// delegates to, so the same-host + downgrade logic lives in exactly one place
// (resolvedRedirect.check) and cannot drift from
// RedirectPolicy(WithSameHost(true)).
var defaultRedirectPolicy = RedirectPolicy(WithSameHost(true))

// DefaultRedirectPolicy is the default redirect policy: it allows a redirect
// only to the same host as the original request, and refuses a same-host
// https->http scheme downgrade (which would forward a custom auth header onto a
// cleartext hop). A cross-host redirect is refused (Go forwards a custom header
// across a redirect, so it would leak) and an http->https upgrade is allowed.
// It delegates to RedirectPolicy(WithSameHost(true)), with one addition: a
// call with an empty via chain (which net/http never produces — via always
// carries at least the original request) is allowed rather than refused. Use
// RedirectPolicy for a custom allowlist or a higher hop cap (WithMaxHops).
func DefaultRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	return defaultRedirectPolicy(req, via)
}

// RefuseAllRedirects is a CheckRedirect policy that follows no redirect: the
// client returns the 3xx response itself with a nil error. Use it for a
// token-bearing client of an API that never redirects, because net/http
// forwards custom headers (X-Plex-Token, X-Api-Key) across a redirect and a
// hostile 302 would leak them. [CheckStatus] reports the surfaced 3xx as
// an error.
func RefuseAllRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// --- Client helpers ---

// CheckRedirect is the http.Client.CheckRedirect function shape. It is a type
// alias, so values are assignable in both directions; every shipped policy
// (DefaultRedirectPolicy, RefuseAllRedirects and anything built with
// RedirectPolicy) is a CheckRedirect.
type CheckRedirect = func(req *http.Request, via []*http.Request) error

// New returns an *http.Client with the given timeout and the
// DefaultRedirectPolicy (same-host only). For a custom redirect allowlist,
// configure CheckRedirect with RedirectPolicy.
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: DefaultRedirectPolicy,
	}
}

// --- Secret redaction ---

// Secret is a credential value being redacted: the needle the redaction
// helpers scan FOR, never the text they scan. It is a distinct type so the
// value-to-hide and the text-to-scan cannot be transposed at a call site — a
// reversed call turns the redactor into a leak, scanning the credential for
// occurrences of the log text and returning the credential untouched. With
// the positions typed apart, a plain string variable in the secret position
// is a compile error; the caller converts exactly the value that is the
// credential (httpclient.Secret(token)) and nothing else fits there.
//
// Convert at the call boundary, never store one: Secret carries no logging
// protection of its own — formatting a Secret with %v/%s or logging it via
// slog prints the credential verbatim. The zero value ("") disables
// redaction: every helper returns its input unredacted when the secret is
// empty, because there is nothing to scan for. Callers redacting a
// possibly-absent credential must treat empty as "no redaction happened",
// not as "redaction succeeded".
type Secret string

// RedactTransportError reduces err as [LogSafeError] does, prefixes it, and
// redacts the secret from its message. Returns nil for nil input, and never
// nil for a non-nil one. When the message holds the secret, the result is a
// new error carrying only the redacted text, because every error in the chain
// still renders the secret: errors.Is, errors.As, [IsTransient] and [CauseOf]
// stop there, so classify err before you redact it. When the message does not
// hold the secret, the reduced and prefixed chain is returned whole.
// The replacement runs through RedactSecretString, whose doc comment carries the
// ordering and representation rules for composing redaction with a normalizing
// transform or a byte cap. An empty secret disables redaction: the (possibly
// prefixed) error is returned with its message untouched, since there is
// nothing to scan for.
func RedactTransportError(err error, prefix string, secret Secret) error {
	if err == nil {
		return nil
	}
	err = reduceURLErrors(err)
	var wrapped error
	if prefix == "" {
		wrapped = err
	} else {
		wrapped = fmt.Errorf("%s: %w", prefix, err)
	}
	if secret == "" {
		return wrapped
	}
	msg := wrapped.Error()
	if !strings.Contains(msg, string(secret)) {
		return wrapped
	}
	return errors.New(RedactSecretString(msg, secret))
}

// RedactSecret replaces occurrences of secret in err's message with "REDACTED",
// as RedactTransportError(err, "", Secret(secret)) does: a message that held
// the secret comes back as a new error with no chain, so classify err first.
// It funnels into RedactSecretString, whose doc comment carries the ordering and
// representation rules the caller owns.
func RedactSecret(err error, secret string) error {
	return RedactTransportError(err, "", Secret(secret))
}

// RedactSecretString replaces every occurrence of secret in s with "REDACTED"
// and returns the result. It is the string-level building block behind
// RedactSecret and RedactTransportError, exposed for callers that must redact a
// secret from a plain string — a captured HTTP response body destined for an
// error field or a log line — rather than from an error value. An empty secret
// is a no-op (s is returned unchanged), matching the error-shaped variants.
//
// The replacement is literal and byte-exact, which puts three obligations on the
// caller:
//
//  1. Hold the needle in the SAME representation the haystack carries. A decoded
//     token does not match a JSON-escaped or percent-encoded haystack, and the
//     mismatch is silent: nothing is replaced and no error is reported.
//  2. Call this on BOTH sides of any normalizing transform (textsafe
//     Sanitize/SanitizeSingleLine, unicode/norm NFC/NFKC, strconv.Unquote,
//     JSON/URL/HTML unescape, case folding, whitespace collapse), passing the
//     needle in the representation each side carries. A call placed only before
//     the transform misses a secret the transform CONSTRUCTS out of text the
//     needle never matched; a call placed only after it misses a secret the
//     transform REWRITES, leaving a near-complete fragment of the value.
//  3. Put any byte cap (ReadLimitedBody, a bounded sanitizer preset, a plain
//     truncation) LAST. A cut that splits the value leaves a surviving prefix
//     this function can no longer match.
//
// Composed, that order is: redact, normalize, redact, cap.
//
// # This package performs one of those transforms
//
// Every URL httpclient renders — *[StatusError].Error(), and every "url" attribute
// in a [GetBytes] log line — goes through a parse-and-re-serialize step that
// blanks query values and drops userinfo. That step is a URL-encoding transform
// in rule 2's sense, so redacting a known secret out of one of those strings is
// the "only after the transform" mistake. Measured on go1.27.0 (identical on
// go1.26.7): for a secret in the request PATH, which is the one
// credential-bearing position the rendering keeps verbatim, a secret containing
// a space renders as tok%20en, a non-ASCII byte as tok%C3%A9n, and a '#'
// truncates the value at the fragment boundary — so a byte-exact needle held in
// the caller's own representation matches none of them and the value reaches the
// log percent-encoded. A stray '%' is the safe case: the URL fails to parse and
// the whole rendering collapses to a fixed placeholder.
//
// So a caller holding a credential that rides in a URL PATH must redact it
// before the URL reaches this package (or pass the needle in its
// percent-encoded form as well). A credential in the query string or the
// userinfo needs nothing: the rendering removes those wholesale, without
// matching anything.
func RedactSecretString(s string, secret Secret) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, string(secret), "REDACTED")
}

// redactURL returns a log-safe rendering of rawURL. It masks the userinfo
// password (like url.URL.Redacted, mirroring the go-retryablehttp CVE-2024-6104
// fix) and replaces every query value with "REDACTED" (query values commonly
// carry api keys, tokens, and signatures — the same default .NET 9's
// IHttpClientFactory adopted). Query keys, scheme, host, and path are kept for
// debugging; the fragment is dropped. Unparseable input yields a fixed
// placeholder rather than risk logging a raw secret-bearing string.
//
// It re-serializes the URL, so it is a normalizing transform in the sense
// [RedactSecretString]'s ordering contract means: a secret in the PATH — the one
// credential-bearing position this function keeps verbatim — can come out
// percent-encoded, and a caller redacting it out of the result afterwards then
// matches nothing. The contract's "this package performs one of those
// transforms" section carries the measurements and the caller's obligation.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable url]"
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			q[k] = []string{"REDACTED"}
		}
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	u.RawFragment = ""
	// Drop userinfo entirely rather than leaning on url.Redacted(), which masks
	// only the PASSWORD and preserves the username verbatim. A username-only
	// token is a real credential shape for API endpoints
	// (https://token@prowlarr/1/api), so Redacted() alone leaks it into every
	// log line this helper is supposed to make safe (CWE-532) - and callers
	// cannot tell, because the function's whole promise is log-safety.
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	return u.Redacted()
}

// LogSafeError returns an error whose message is safe to log. Every *url.Error
// in err's chain, which renders the raw request URL, is replaced by its cause.
// The marks this package renders on the fly ([Permanent], [MarkTransient],
// [AttemptTimeout]), the RetryRoundTripper's "rewind request body" and
// "prepare retry" steps, and each branch of an errors.Join are kept around the
// reduced cause, so [IsPermanent], [IsTransient], the step and every sibling
// survive.
// Any other wrapper above a *url.Error is dropped, because its message was
// rendered with the URL in it: reduce a transport error before you wrap it with
// your own context. An error with no *url.Error is returned unchanged, a chain
// nested more than eight *url.Error deep reduces to a contentless "request
// failed", nil returns nil, and a non-nil error never reduces to nil. It
// equals RedactTransportError(err, "", ""), the empty-secret case, which is the
// reduction httpclient applies to every transport error it logs or wraps.
func LogSafeError(err error) error {
	if err == nil {
		return nil
	}
	return reduceURLErrors(err)
}

// maxURLErrorDepth bounds reduceURLErrors: net/http builds one *url.Error per
// call, and more come only from a RoundTripper or CheckRedirect that returns
// its own; a *url.Error whose Err is itself never reaches a fixpoint.
const maxURLErrorDepth = 8

// reduceURLErrors is LogSafeError's reduction. A chain still holding a
// *url.Error after maxURLErrorDepth replacements reduces to errRequestFailed
// rather than render a URL.
func reduceURLErrors(err error) error {
	reduced := reduceURL(err, maxURLErrorDepth)
	if reduced == errURLDepth {
		return errRequestFailed
	}
	return reduced
}

// errURLDepth is reduceURL's internal verdict that a *url.Error remained when
// the budget ran out; reduceURLErrors turns it into errRequestFailed, so it
// never leaves the package.
var errURLDepth = errors.New("url error depth exceeded")

// reduceURL rebuilds err with each *url.Error replaced by its cause, keeping
// this package's marks and every errors.Join branch, and dropping any other
// wrapper above a *url.Error. budget counts the replacements left; the result
// is errURLDepth once a *url.Error remains when it runs out.
func reduceURL(err error, budget int) error {
	if _, has := errors.AsType[*url.Error](err); !has {
		return err
	}
	switch e := err.(type) {
	case *url.Error:
		if budget == 0 {
			return errURLDepth
		}
		return reduceURL(urlErrorCause(e), budget-1)
	case *permanentError:
		return rewrap(reduceURL(e.Err, budget), func(inner error) error { return &permanentError{Err: inner} })
	case *transientError:
		return rewrap(reduceURL(e.Err, budget), func(inner error) error { return &transientError{Err: inner} })
	case *attemptTimeoutError:
		return rewrap(reduceURL(e.Err, budget), func(inner error) error { return &attemptTimeoutError{Err: inner} })
	case *stageError:
		return rewrap(reduceURL(e.err, budget), func(inner error) error { return &stageError{stage: e.stage, err: inner} })
	case interface{ Unwrap() []error }:
		branches := e.Unwrap()
		kept := make([]error, 0, len(branches))
		for _, b := range branches {
			r := reduceURL(b, budget)
			if r == errURLDepth {
				return errURLDepth
			}
			kept = append(kept, r)
		}
		return errors.Join(kept...)
	default:
		inner := errors.Unwrap(err)
		if inner == nil {
			// An As method surfaced the *url.Error with no chain to descend.
			return errRequestFailed
		}
		return reduceURL(inner, budget)
	}
}

// rewrap puts a reduced inner error back under its mark, passing errURLDepth
// through unmarked.
func rewrap(inner error, mark func(error) error) error {
	if inner == errURLDepth {
		return errURLDepth
	}
	return mark(inner)
}

// errRequestFailed stands in for the cause of a *url.Error that has none. It
// is deliberately contentless: an error with no cause has nothing else to
// report, and the one field such a *url.Error does carry is the URL this
// package exists to keep out of logs.
var errRequestFailed = errors.New("request failed")

// urlErrorCause returns the cause a *url.Error reduces to: urlErr.Err through
// nameHangUp. A nil *url.Error or a nil Err reduces to errRequestFailed,
// because nil would report the failure as a success and the *url.Error itself
// renders the raw URL (CWE-532).
func urlErrorCause(urlErr *url.Error) error {
	if urlErr == nil || urlErr.Err == nil {
		return errRequestFailed
	}
	return nameHangUp(urlErr.Err)
}
