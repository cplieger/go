// Package httpclient is a resilient outbound-HTTP toolkit: transient-error
// classification, retries over one jittered exponential backoff loop,
// Retry-After parsing, status mapping, secret redaction, body helpers,
// custom-CA TLS transports and redirect allowlists. They live together because
// they compose into one [net/http.Client]. The revalidation cache that
// composes with them is the httpcache module (github.com/cplieger/go/httpcache).
//
// # Retry doors
//
// Three entry points share one option vocabulary and one backoff progression,
// and passing an option to the wrong door does not compile:
//
//   - [Do] retries a typed operation. You build the requests and keep the
//     typed result.
//   - [GetBytes] is a bounded-bytes GET with redacted diagnostics. It owns the
//     request, the body cap and the close.
//   - [NewRetryRoundTripper] retries transparently beneath any client, under a
//     [TransportConfig].
//
// [New] and [NewRetry] build clients with a redirect policy in place. An
// [Observer], set with [WithObserver] or TransportConfig.Observer, receives
// each door's attempts and outcome.
//
// # Errors
//
// [IsTransient] decides retryability, extended through the [Transient] and
// [RetryAfterHint] interfaces or [MarkTransient]. [Permanent] marks an error
// non-retryable. A context deadline is the total budget and is never retried,
// while [AttemptTimeout] marks the end of one attempt, which is.
// For every non-2xx status, [CheckStatus] and [GetBytes] return an error whose
// chain holds a *[StatusError], matched with errors.As or with errors.Is
// against [ErrUnauthorized], [ErrForbidden], [ErrRateLimited] and
// [ErrServerError].
//
// # Secrets
//
// [RedactSecret], [RedactSecretString], [RedactTransportError] and
// [LogSafeError] keep credentials out of logs and errors, and [GetBytes] never
// logs or returns a raw URL. A caller adding a normalizing transform or a byte
// cap follows the order on [RedactSecretString]: redact, normalize, redact,
// cap.
//
// The README groups the API by task and pkg.go.dev lists every identifier.
// docs/retries.md, docs/timeouts.md, docs/responses.md and
// docs/security-model.md hold the full contracts.
package httpclient
