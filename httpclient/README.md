# httpclient

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/httpclient.svg)](https://pkg.go.dev/github.com/cplieger/go/httpclient) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=httpclient%2Fgo.mod)](https://github.com/cplieger/go/blob/main/httpclient/go.mod)

httpclient makes your Go HTTP calls survive flaky servers, with retries, backoff and Retry-After built in.

It replaces the retry loop, status check and redirect policy you would otherwise write around `net/http`, and it hands back a plain `*http.Client`. It uses only the standard library at run time, needs Go 1.27 or later and is licensed under Apache-2.0.

## Why use it

httpclient is built for Go code that must keep working when a remote API fails for a moment.

- `Do` retries a typed operation, `GetBytes` fetches a capped body, and a retrying `http.RoundTripper` fits an existing `*http.Client`.
- Each decides which network errors and status codes to retry. `GetBytes` and the transport honor `Retry-After` up to 60 seconds.
- `GetBytes` removes a URL's username, password and query values from its logs and error messages.
- The default redirect policy keeps custom auth headers on the host you called. No shipped policy follows an `https` to `http` hop.
- `GetBytes` fails on a body over its 10 MB default cap instead of cutting it.

To serve repeated GETs from memory when the server answers `304 Not Modified`, add the [httpcache](https://github.com/cplieger/go/tree/main/httpcache) module's transport to the same client.

Consider [cenkalti/backoff](https://github.com/cenkalti/backoff) if you want to choose your own backoff policy, through its `WithBackOff` option. Consider [failsafe-go](https://github.com/failsafe-go/failsafe-go) if you need a circuit breaker, a rate limiter or a bulkhead, policies it wraps around any function.

## Install

```sh
go get github.com/cplieger/go/httpclient@latest
```

## Usage

```go
// A client with a timeout that follows same-host redirects only.
client := httpclient.New(30 * time.Second)

// Three attempts with backoff, a body capped at 10 MB, the URL redacted in logs.
body, err := httpclient.GetBytes(ctx, client, "https://api.example.com/items")
```

For code that already works with `*http.Client`, put the retry in the transport:

```go
client := httpclient.NewRetry(nil, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{MaxAttempts: 4})
resp, err := client.Do(req)
```

`NewRetry` panics on a nil redirect policy, because a nil `CheckRedirect` means net/http follows redirects to any host. Pass `DefaultRedirectPolicy`, `RefuseAllRedirects` or a `RedirectPolicy` allowlist.

Retry any operation and keep its typed result:

```go
items, err := httpclient.Do(ctx, func(ctx context.Context) ([]Item, error) {
    return fetchItems(ctx)
}, httpclient.WithMaxAttempts(4), httpclient.WithLabel("fetch items"))
```

Turn a response status into an error. Only a 2xx returns nil:

```go
if err := httpclient.CheckStatus(resp); err != nil {
    if errors.Is(err, httpclient.ErrUnauthorized) {
        // 401: the credential is wrong or expired.
    }
    // Every non-2xx is a *StatusError with the code in Code.
}
```

`GetBytes` returns an error holding the same `*StatusError`, whose text names the URL you passed with its userinfo and query values redacted. Find it with `errors.As` or match it with `errors.Is`: after retries run out it sits inside the exhaustion error.

`ExampleGetBytes` and `ExampleDo` on pkg.go.dev are runnable, and `go test` keeps them true.

## API

- Retry: `Do[T]`, `GetBytes`, `NewRetryRoundTripper` (the `RetryRoundTripper`), `NewRetry` and `New`, with their options (`Option`, `DoOption`, `GetOption`), `TransportConfig` with its `CheckRetry`, `OnRetry` and `PrepareRetry` hooks, `ErrMaxElapsed` and `ContextWithDefaultTimeout`.
- Observing calls: `Observer` with its `Attempt` and `Outcome` records, set through `WithObserver` or `TransportConfig.Observer`.
- Backoff and Retry-After: `JitteredBackoff`, `SafeDouble`, `SleepCtx`, `ParseRetryAfter` with its 60-second `RetryAfterCap`, and `ParseRetryAfterResponse`, which keeps the raw value.
- Classification: `IsTransient`, `Permanent` and `IsPermanent`, `MarkTransient`, `AttemptTimeout` and `IsAttemptTimeout`, `IsRetryableStatus`, `CauseOf` with its `Cause` names, the `Transient` and `RetryAfterHint` interfaces.
- Status and errors: `CheckStatus`, the `StatusError` type with its `ErrUnauthorized`, `ErrForbidden`, `ErrRateLimited` and `ErrServerError` sentinels, and the `ResponseTooLargeError` type.
- Redirects: `DefaultRedirectPolicy`, `RefuseAllRedirects` and `RedirectPolicy` with its `RedirectOption` options, the `CheckRedirect` shape they share, and the `RedirectRefusedError` every hard refusal returns, whose `RedirectReason` names the rule.
- TLS: `CATransport` pins a private CA and returns `ErrNoCertsInPEM` for PEM data holding no certificate, `CloneDefaultTransport`, and the `certtest` package for tests.
- Redaction: `LogSafeError`, `RedactSecret`, `RedactSecretString`, `RedactTransportError`.
- Bodies: `ReadLimitedBody`, `Drain`, `DrainClose`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/httpclient).

## Retries and timeouts

`Do`, `GetBytes` and the retrying transport each count total attempts, three by default. Between attempts they wait a random time between half and all of a delay that starts at one second and doubles. In `GetBytes` and the transport, a `Retry-After` header replaces that wait, capped at 60 seconds.

All three entry points retry transient network errors and apply one status rule, `IsRetryableStatus`: 408, 429 and 500 to 599, except the 501, 505, 506, 508, 510 and 511 answers that repeat. `Do` treats a 429 `*StatusError`, from `CheckStatus` or `GetBytes`, as final unless you pass `WithRateLimitRetry`. The transport retries GET, HEAD, OPTIONS and TRACE, and a request of any method that carries a non-empty `Idempotency-Key` header. `RetryNonIdempotent` extends that to every request. Whatever the method, it retries a request with a body only when it has a `GetBody` function to replay the body. `Do` never sees the request, so around a non-idempotent one, return `Permanent(err)` for a failure you must not repeat.

When the transport runs out of attempts, it hands back the last response as a normal response, with a nil error, even when that response is a 503. So check `resp.StatusCode` and close the body.

A context deadline is the total budget and is never retried. A per-attempt timeout is retried, whether it is `WithAttemptTimeout` under `Do` or `ResponseHeaderTimeout` on the transport. Do not set `Client.Timeout` on a retrying client, because it caps the whole retry sequence.

[Retries](docs/retries.md) and [Timeouts and deadlines](docs/timeouts.md) have the full contract.

## Credentials in logs and redirects

Go forwards custom headers such as `X-Api-Key` across a redirect. `DefaultRedirectPolicy` follows same-host hops only, and every shipped policy refuses an `https` to `http` downgrade. Same host is not same origin: another port on that host can be another service. For a client whose key must reach only the service you configured, use `RedirectPolicy(WithSameOrigin(true))`, which follows only the original scheme, host and port. Host names match without regard to ASCII case; an IP address matches by value, and its IPv6 zone must match exactly. A `RedirectPolicy` allowlist follows the other hosts you name, never with a downgrade. `RefuseAllRedirects` follows none and hands the 3xx back to you, which `CheckStatus` reports as an error.

In its log lines and error messages, `GetBytes` replaces a URL's userinfo and each query value with `REDACTED`. It reduces transport errors to their cause, so their text carries no raw URL. URL paths are kept, so do not put a credential in a URL path you pass to `GetBytes`. `RedactSecretString` takes the secret as the `Secret` type, so swapping the two arguments does not compile.

[Redirects, TLS and redaction](docs/security-model.md) covers each policy option, the pinned-CA transport and the order to redact in.

## Unsupported by design

httpclient has no circuit breaker, retry budget, alternative jitter strategies, exhaustion error handler, response body on error, idempotency-key injection or configurable Retry-After cap. [Unsupported by design](docs/non-goals.md) gives the reason for each and what to use instead.

## Documentation

- [Retries](docs/retries.md) covers the three ways to retry, their options, backoff, rate limits, exhaustion and logging.
- [Timeouts and deadlines](docs/timeouts.md) says which timeouts are retried and where to put each bound.
- [Status codes, errors and bodies](docs/responses.md) lists the status mapping, error types and body caps.
- [Redirects, TLS and redaction](docs/security-model.md) covers redirect policies, the pinned-CA transport and keeping secrets out of logs.
- [Unsupported by design](docs/non-goals.md) lists the features left out on purpose, with the reasons.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
