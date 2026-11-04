# Non-goals

This page lists what health leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions rather than missing features, and a pull request that adds one is declined. To argue for a change, open an issue first.

| Left out | Reason |
| --- | --- |
| Registered dependency checks | `Set(bool)` is where your code combines its checks, and the service owns that logic. A check registry is a different abstraction |
| Separate liveness and readiness signals | Docker Compose has one `HEALTHCHECK`. On Kubernetes, create two `Marker` values with different paths |
| Graceful shutdown hooks or `context.Context` | `Cleanup` is the shutdown action. The library starts no goroutine, so nothing needs cancelling |
| Status-change callbacks | Changes of state are logged through `slog`. Wrap `Set` to add your own callback |
| A staleness check by default | The default checks existence, and Docker's `--interval` decides how often. A deadline is opt-in for each service through `WithMaxAge` |
| Prometheus metrics | A consumer adds one with `prometheus.NewGaugeFunc` over `CheckHealthy` |
| Content inside the marker file | The check is one `os.Stat`, with no format to parse or version |

If you need registered checks against your dependencies behind an HTTP endpoint, consider [alexliesenfeld/health](https://github.com/alexliesenfeld/health). It runs them on each request or periodically, caches the results and reports a status for each component.
