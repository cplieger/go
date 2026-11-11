# Non-goals

This page lists what health leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions rather than missing features, and a pull request that adds one is declined. To argue for a change, open an issue first.

| Left out | Reason |
| --- | --- |
| Running dependency checks on each request | The Status is a verdict the service records, not a registry of checks. `CheckHandler` reads the signals it is given and never calls a database or an upstream; a registry that does so per request is a different abstraction |
| Separate liveness and readiness signals | Docker has one `HEALTHCHECK`. On Kubernetes, serve two `CheckHandler`s over two Status values |
| A shutdown hook or `context.Context` | The library starts no goroutine, so nothing needs cancelling. `BeginDrain` is the shutdown action |
| Status-change callbacks | Each change of verdict is logged through `slog`. Wrap `Set` to add your own callback |
| A staleness check by default | A lease is opt-in for each service, through `Fresh` and `WithMaxAge`, because only a service with its own work cycle can name one |
| Prometheus metrics | A consumer adds one with `prometheus.NewGaugeFunc` over `Healthy` |
| Content inside the marker file | The probe's check is one stat, with no format to parse or version |
| An exported degraded-mode flag | `/healthz` reads memory, so no reader needs to know whether the marker can be written. The warning `NewStatus` logs names the cause and the fix |
| Health written by a second process | Only the long-running process writes its verdict. A second writer would race the verdict `/healthz` reports, and a one-shot command reports through its exit code |
| A `Signal` that takes a context | A signal reads state the service already keeps, so it has nothing to wait on or cancel |

If you need registered checks against your dependencies behind an HTTP endpoint, consider [alexliesenfeld/health](https://github.com/alexliesenfeld/health). It runs them on each request or periodically, caches the results and reports a status for each component.
