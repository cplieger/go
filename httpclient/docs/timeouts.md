# Timeouts and deadlines

This page is for a developer deciding where to put a timeout on a call that httpclient retries. httpclient retries the failure of one attempt and never the end of the caller's budget. So where a timeout lives decides whether its expiry is retried.

## The rule

`IsTransient` treats a caller's expired or canceled context as final. It treats a connection reset, a DNS error and a `net.Error` timeout as worth another attempt. Which status codes each call retries is in [Retries](retries.md#what-each-one-retries). A caller's deadline means the budget is spent. The expiry of a bound over one attempt means that attempt failed and the next one may work.

## Where each bound goes

| Bound | Where to set it | Retried |
| --- | --- | --- |
| Total budget | a context deadline, such as `context.WithTimeout` | no |
| One attempt under `Do` | `WithAttemptTimeout(d)` | yes |
| One attempt on the client or transport | `http.Client.Timeout` or `Transport.ResponseHeaderTimeout` | yes |
| A bound you derive inside your own function | mark its error with `AttemptTimeout(err)` | only once marked |
| A `net.Dialer` timeout | mark its error with `AttemptTimeout(err)` | only once marked |

### Total budget

A context deadline spans every attempt and every backoff sleep, because `SleepCtx` stops when it expires. When it expires, the call ends and is not retried.

### One attempt under `Do`

`WithAttemptTimeout(d)` runs each attempt under a context bounded by `d` and retries that bound's expiry, the per-try timeout model gRPC uses. A caller deadline nearer than `d` still wins, because a context keeps the earlier deadline. So `d` caps one attempt and never extends the total. The expiry counts as the attempt's own only while the caller's context is still live, so a caller out of budget stays final. The attempt context is canceled when your function returns, so read or drain the body inside the function.

### One attempt on the client or transport

net/http reports `Client.Timeout` and `ResponseHeaderTimeout` through its own timeout error. That error claims to match `context.DeadlineExceeded` without carrying it, so both count as per-attempt and are retried, including by the transport's default policy. Return `Permanent(err)` from a `TransportConfig.CheckRetry` if you want one of them to stop the loop instead.

### Bounds httpclient cannot recognize

A `context.WithTimeout` you derive inside your function carries the real deadline value, so it looks like the caller's own. A `net.Dialer` timeout looks the same as a caller deadline after the `net` package reports it. Both stay final until you wrap the error in `AttemptTimeout`, which is what `WithAttemptTimeout` does for you.

### GetBytes

`GetBytes` builds its own request, so it takes no per-attempt option. Bound it with the `Client.Timeout` of the client you pass. Or run it as one attempt inside `Do`, with `WithMaxAttempts(1)` on the GET and `WithAttemptTimeout(d)` on the `Do`. The same composition avoids multiplying two attempt counts.

## How httpclient tells them apart

httpclient checks whether the `context.DeadlineExceeded` value itself is in the error's unwrap chain. It does not use `errors.Is`, because net/http's timeout error answers `errors.Is(err, context.DeadlineExceeded)` with true without carrying that value. `errors.Is` would therefore mix up the bounds net/http installs with the caller's own.

Some cases cannot be told apart. The `net` package maps every expired context onto one shared `i/o timeout` value, so a `*net.OpError` or `*net.DNSError` reporting a deadline could be the caller's or a dialer's. Those stay final, and `AttemptTimeout(err)` is how the code that set the bound opts back in. `AttemptTimeout` keeps the deadline visible, so `errors.Is(err, context.DeadlineExceeded)` still reports true for your own callers.

## Timeouts under the retrying transport

Under `NewRetryRoundTripper` or `NewRetryClient`, the retry loop runs inside `client.Do`. There, `http.Client.Timeout` is no longer per-attempt. It caps the whole retry sequence, and a slow attempt that trips it cancels the remaining retries, which is why `NewRetryClient` sets none.

Put the per-attempt bound on the base transport, as `ResponseHeaderTimeout` on a `CloneDefaultTransport()`, which is retried. Put the total on the request context or `TransportConfig.MaxElapsedTime`.

`MaxElapsedTime` is checked between attempts, so it cannot interrupt an attempt already stalled inside the base transport. A transport-level timeout can.
