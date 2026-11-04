# Unsupported by design

This page lists what httpclient leaves out on purpose, for a developer wondering whether a missing feature is coming. These are decisions, not a backlog. To argue one back into scope, open an issue first.

| Feature | Reason |
| --- | --- |
| Circuit breaker | A separate pattern from retry. Compose one around httpclient, for example sony/gobreaker |
| Retry budget or token bucket | It would add about 150 lines and shared mutable state to a focused library |
| Full or decorrelated jitter | Equal jitter is the default the AWS Builders' Library recommends. Full jitter can produce near-zero delays |
| An error handler for exhaustion | The exhaustion error wraps the last error, which callers unwrap with `errors.Is` and `errors.As` |
| The response body on error | It would make ownership of closing the body part of the API. Use `Do[T]` with your own logic instead |
| Idempotency-key injection | It belongs to the application, not to a retry library |
| Serving a stored answer on age (freshness, `max-age`, heuristic expiry) | Every read reaches the server, and only its `304` serves a held copy. [Revalidation cache](caching.md) explains why |
| A disk or shared cache tier | It would hold response bodies outside the process, and the saving after a restart is one full read per answer |
| A configurable Retry-After cap | `ParseRetryAfter` caps waits at 60 seconds so a server cannot stall a client. The `maxWait` argument of the rate-limit options sets your own limit |

[failsafe-go](https://github.com/failsafe-go/failsafe-go) provides a circuit breaker, a rate limiter, a bulkhead and other policies that can wrap the same calls.
