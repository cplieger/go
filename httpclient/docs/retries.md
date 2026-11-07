# Retries

This page is for a developer choosing between the three ways httpclient retries a call and tuning one of them. It covers the options, which failures each one retries, how waits are computed, what happens when attempts run out, and what gets logged.

## Three ways to retry

Each has a different owner for the request, the body and the response. All three run one retry loop, with the same attempt budget, backoff and `Retry-After` handling, and differ only in how they judge an attempt.

| Call | You give it | It returns | Use it when |
| --- | --- | --- | --- |
| `Do[T]` | a function returning `(T, error)` | the typed result | you build the request and decode the response yourself |
| `GetBytes` | a client and a URL | the body bytes, capped | you want a GET whose body comes back as `[]byte` |
| `NewRetryRoundTripper` | a base transport and a `TransportConfig` | an `http.RoundTripper` | code you do not control already takes an `*http.Client` |

`NewRetry(base, policy, cfg)` builds an `*http.Client` from a `RetryRoundTripper` and the redirect policy you pass. The policy is required and a nil one panics, because a nil `CheckRedirect` means net/http follows redirects to any host. The client sets no `Client.Timeout`, for the reason in [Timeouts and deadlines](timeouts.md).

`New(timeout)` is a plain client with that timeout and `DefaultRedirectPolicy` installed. It does not retry by itself, so it is the client to hand to `GetBytes`.

`ContextWithDefaultTimeout(ctx, def)` bounds `ctx` by `def` only when the caller set no deadline. A caller's deadline is never shortened or extended, a `def` of zero or less means no default, and the returned cancel function is never nil.

## Options for `Do` and `GetBytes`

`Do` and `GetBytes` take functional options. They are typed, so passing a `Do` option to `GetBytes`, or the reverse, does not compile.

| Option | Accepted by | Default | Effect |
| --- | --- | --- | --- |
| `WithMaxAttempts(n)` | both | 3 | Total attempts, the first included. A value below 1 means exactly one attempt |
| `WithBaseDelay(d)` | both | 1s | First backoff delay. A value of zero or less takes the default |
| `WithLogger(l)` | both | `slog.Default()` | Logger for the retry lines. A nil logger takes the default |
| `WithExhaustedLevel(level)` | both | see [Logging](#logging) | Level of the final "retries exhausted" line |
| `WithLabel(s)` | `Do` | `operation` | Name used in `Do`'s log lines |
| `WithAttemptTimeout(d)` | `Do` | none | Bounds each attempt and makes its expiry retryable |
| `WithRateLimitRetry(maxWait)` | `Do` | off | Retries a 429 `*StatusError` as well as transient errors |
| `WithRateLimitOnly(maxWait)` | `Do` | off | Retries a 429 `*StatusError` and nothing else |
| `WithHeaders(fn)` | `GetBytes` | none | Called to set headers on each request |
| `WithMaxBodyBytes(n)` | `GetBytes` | 10 MB | Body cap. A value of zero or less takes the default |

`WithRateLimitRetry` and `WithRateLimitOnly` set one mode, so when you pass both the later one wins, with its `maxWait`.

## Options for the transport

`TransportConfig{}` is ready to use. It gives three attempts, a one-second base delay and the default retry policy.

| Field | Effect |
| --- | --- |
| `MaxAttempts` | Total attempts. Zero means 3, and a negative value means exactly one attempt |
| `BaseDelay` | First backoff delay. Zero or less means 1s |
| `CheckRetry` | Replaces the retry policy, `func(ctx, resp, err) (bool, error)`. A returned error stops the loop |
| `OnRetry` | Called before each retry, with a 1-based attempt number. The transport logs nothing, so this is where to observe it |
| `PrepareRetry` | Changes the cloned request before each retry, for example to set a fresh token |
| `MaxElapsedTime` | Ceiling on total time across retries, honored `Retry-After` waits included. Checked between attempts; a stop returns `ErrMaxElapsed`. Zero means none |
| `RetryNonIdempotent` | Also retries every other request, POST, PUT, PATCH and DELETE among them, with or without an `Idempotency-Key` |

The transport never changes your request. It clones it for each attempt. By default it retries GET, HEAD, OPTIONS and TRACE, the safe methods of RFC 9110 section 9.2.1.

It also retries a request of any method that carries a non-empty `Idempotency-Key` header, as the [Idempotency-Key draft](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/) describes. Your server must store the key with the write and answer a repeat from that record, so a retried POST changes state once. Every attempt sends the same key, because each attempt clones your request. An empty header value counts as no key.

net/http copies the key onto a 307 or 308 hop, so the redirect target is retried under it too. `DefaultRedirectPolicy` follows a hop to another port on the same host, which can be another service. Unless every redirect target dedups the key, give a keyed client `RefuseAllRedirects`, or `RedirectPolicy(WithSameOrigin(true))` to stay on the original origin.

A server that follows the draft answers 409 while the first attempt is still running. The default policy returns that 409 as final, because many servers send 409 for a real conflict. To wait it out, set `CheckRetry` to also retry a 409 whose `resp.Request` carries the key.

Whatever the method, a request with a body is retried only when it has a `GetBody` function to replay the body.

Each `RoundTrip` keeps its own backoff, so one `RetryRoundTripper` is safe to share between goroutines.

## What each one retries

One status rule, `IsRetryableStatus`, decides for all three: 408, 429 and every status from 500 to 599 are worth another attempt, except the six whose answer repeats. Every other status is a settled answer.

- 501 and 505 say the server does not support the method or the HTTP version (RFC 9110 sections 15.6.2 and 15.6.6).
- 506 and 508 report a server configuration error and a loop (RFC 2295, RFC 5842).
- 510 and 511 wait on the client to add an extension or sign in to the network (RFC 2774, RFC 6585).

A 5xx code no RFC registers is retried like a 500, as RFC 9110 section 15 asks. A status of 600 or more is not a valid status, so it is not retried, though `ErrServerError` still matches it as a server error.

| Failure | `Do` over `CheckStatus` | `GetBytes` | Transport |
| --- | --- | --- | --- |
| Transient network error | yes | yes | yes |
| 408 | yes | yes | yes |
| 429 | only with a rate-limit option | yes | yes |
| 500 to 599, except 501, 505, 506, 508, 510, 511 | yes | yes | yes |
| 501, 505, 506, 508, 510, 511, and 600 or more | no | no | no |
| Other 4xx | no | no | no |

`Do` sees only the error your function returns, and `IsTransient` decides whether it is worth another attempt. When your function returns the error from `CheckStatus`, its `*StatusError` reports itself transient exactly when `IsRetryableStatus` accepts its code, except a 429, which only a rate-limit option retries. A transient error is a network timeout, a connection reset, refused or broken pipe, a server that closed the connection before it answered, an unexpected end of body, a DNS failure, or an error whose `IsTransient()` method says so. `IsTransient` never accepts a `*PermanentError`, a `*StatusError` for 401, 403 or 429, or an expired or canceled caller context.

The transport's default policy retries an error `IsTransient` accepts and a status `IsRetryableStatus` accepts. `GetBytes` judges each attempt the same way. Set `TransportConfig.CheckRetry` to retry a different set.

`Do` never sees the request, so whether repeating it is safe is your decision. Around a POST or another non-idempotent request, `Do` repeats what `IsTransient` accepts, and a server that failed after reading the request may already have applied it (RFC 9110 section 9.2.2). Return `Permanent(err)` for a failure you must not repeat, or send the request through the transport, which checks the method and the `Idempotency-Key` header.

## Waits and Retry-After

The wait before the next attempt is a random time between half and all of the current delay, which starts at `BaseDelay` and doubles after each retry. This is equal jitter, the only strategy httpclient provides.

`GetBytes` and the transport replace that wait with the response's `Retry-After` header when it has one, capped at 60 seconds. The transport honors it on any response it retries.

In `Do`, an error that implements `RetryAfterHint` with a positive duration replaces the wait instead. httpclient does not cap that value, so the type that returns it must. The doubling delay keeps advancing underneath either override.

`ParseRetryAfter` reads a header value in seconds or as an HTTP date and caps it at `RetryAfterCap`, 60 seconds. `ParseRetryAfterResponse` reads it from a response and does not cap it. A missing, malformed or past value is zero.

## Rate limits

`Do` does not retry a 429 `*StatusError` by default, so a generic operation never re-fires a rate-limited call on its own.

- `WithRateLimitRetry(maxWait)` adds rate limits to the retryable set. Before a rate-limit retry, `Do` waits the error's `Retry-After` when it has one, but never longer than `maxWait`. Without a hint it waits `maxWait`.
- `WithRateLimitOnly(maxWait)` retries rate limits with the same waits and returns every other error at once, transient ones included. Its final log line reads "rate limit retries exhausted". The error of the last attempt is returned even when the context was already canceled.

A `maxWait` of zero or less becomes 60 seconds, so the wait between attempts is never zero.

## Marking errors

- `Permanent(err)` makes `IsTransient` reject `err`, so `Do` and the default transport policy never retry it. It also stops a 429 under either rate-limit option. It is the same idea as cenkalti/backoff. `IsPermanent` tests for it, and `errors.Is` and `errors.As` reach the error it wraps.
- `MarkTransient(err)` tells `Do` to retry an error the shared rules would not, such as a server fault inside a 200 envelope. It overrides a non-transient verdict already on the error. It cannot override `Permanent`, a `*StatusError` for 401, 403 or 429, or a caller context error. For a rate limit, name a wait budget with `WithRateLimitRetry` instead.
- `AttemptTimeout(err)` marks a timeout as the end of one attempt, so it is retried. `IsAttemptTimeout` tests for it. [Timeouts and deadlines](timeouts.md) explains when to use it.
- Your own error type can implement `Transient`, an `error` with `IsTransient() bool`, or `RetryAfterHint`, an `error` with `RetryAfterHint() time.Duration`. Both embed `error`, so `errors.AsType[httpclient.Transient](err)` compiles.

## When attempts run out

`Do` returns the last error, or `ctx.Err()` when the context ended after a failed attempt.

`GetBytes` returns a nil body and an error reading `retries exhausted after <elapsed>: <last error>`, which `errors.Is` and `errors.As` can unwrap. A body over the cap is a `*ResponseTooLargeError` and returns no body. When the last attempt carried a `Retry-After`, the error also implements `RetryAfterHint` with that capped wait. It adds no `Transient` verdict of its own, so `IsTransient` reads the wrapped failure: an exhausted 408 or retryable 5xx is transient to an enclosing `Do`, and a 401, 403 or 429 is not.

The transport returns the last response with a nil error, even when that response is a retryable 503. A caller that checks only `err != nil` treats an exhausted 503 as success, so check `resp.StatusCode` and close the body. Only a `MaxElapsedTime` stop returns an error, which matches `ErrMaxElapsed` and wraps the last attempt's error when there was one.

## Running GetBytes inside your own retry loop

A 3-attempt `GetBytes` inside a 3-attempt `Do` makes up to 9 requests. Run it with `WithMaxAttempts(1)` and return its error unchanged:

```go
body, err := httpclient.Do(ctx, func(ctx context.Context) ([]byte, error) {
    return httpclient.GetBytes(ctx, client, url, httpclient.WithMaxAttempts(1))
}, httpclient.WithRateLimitRetry(30*time.Second))
```

`Do` retries a 408, a retryable 5xx or a transient network error by itself, because `IsTransient` reads the `*StatusError` or the network error inside the exhaustion error. A 429 is retried only because of `WithRateLimitRetry`, which waits the response's `Retry-After` but never longer than `maxWait`. Without that option a 429 ends the loop, like a 401, a 403 or any settled status. For the other retried failures, the exhaustion error keeps the `Retry-After` hint, so `Do` waits what the server asked for instead of its own backoff.

## Logging

`Do` and `GetBytes` log through `log/slog`, to `WithLogger` or `slog.Default()`.

| Line | Level |
| --- | --- |
| Each failed attempt that will be retried | Debug |
| A `Do` success after a retry | Debug |
| Attempts ran out, with more than one attempt allowed | Warn |
| Attempts ran out under `WithMaxAttempts(1)` | Debug |
| A successful `GetBytes` attempt that took more than 10 seconds | Warn |

With a single attempt nothing was retried, so the loop around the call owns the warning. `WithExhaustedLevel(level)` sets the level of the final line for any attempt count, and it is the only way to raise it above Warn. Use it to demote the line when your own failure log carries more context, without losing the Debug lines a discard logger would also drop.

The slow-response timer runs per attempt, so backoff sleeps never count as upstream latency. The transport logs nothing, so observe its retries through `OnRetry`, where redacting the URL is up to you. Every URL these lines carry is redacted as [Redirects, TLS and redaction](security-model.md) describes.

## Backoff building blocks

- `JitteredBackoff(d)` returns a random duration in `[d/2, d]`.
- `SafeDouble(d)` doubles `d` and stops at the largest duration instead of overflowing.
- `SleepCtx(ctx, d)` sleeps for `d` and returns early with `ctx.Err()` when the context ends.
