# Non-goals

This page lists what measure leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions, so open an issue to argue for one before sending a pull request.

| Left out | Reason |
| --- | --- |
| A store of past values | measure keeps current values only. Prometheus keeps the operator's history, and the app's own store keeps product history. The `chartdata` module's sampled history is bounded and starts empty at each restart |
| Summary instruments | Use a histogram. Prometheus's guide recommends histograms whenever you aggregate across instances, and a summary needs a windowed-quantile implementation |
| OpenMetrics exposition and content negotiation | `Handler` serves Prometheus text format 0.0.4, which both of Prometheus's default scrape-format lists include. The signals that reopen OpenMetrics 2.0 follow this table |
| Exemplars | They need a tracing integration and OpenMetrics or protobuf exposition |
| Pushing metrics or remote write | measure serves a pull endpoint for Prometheus to scrape |
| Protobuf exposition | Prometheus asks for protobuf first only when native histograms are on, and protobuf needs the protobuf runtime module as a dependency |
| Native histograms with exponential buckets | They need protobuf exposition and a large specialized implementation |
| Alert rules and thresholds | measure declares and serves series. Rules and their thresholds live beside the app in its rule files, where they are reviewed and tested with `promtool` |
| Unregistering metrics or a dynamic metric lifecycle | measure is built for a set of declarations that is fixed when the program starts. A label set comes and goes with `Delete` |
| Third-party collectors | The `Metric` and `Declared` interfaces are sealed. A collector is a function given to `NewCollector`, so registration accepts this package's types only, and reading a registry (`Gather`, `GatherOf`, `Declaration`) opens no way to register into it. `client_golang` has an open `Collector` interface for a third-party `prometheus.Collector` |
| Downsampling | measure serves current values. A chart's frame holds the points its window holds, within the `chartdata` module's limits |
| Float counter instruments | The counter types count whole events as `int64`. A collector counter reports a `float64` total it reads elsewhere |
| A unit beyond seconds, bytes, ratio, count and none | A unit is added only when a declared series needs it. A rate is derived from a count and the time axis |
| Gzip compression of the response | Wrap `Handler` in standard HTTP middleware |
| `Gauge.SetToCurrentTime()` | Call `g.Set(float64(time.Now().Unix()))` |

Any of these reopens OpenMetrics 2.0:

- The OpenMetrics spec publishes 2.0.0 without `-rc`.
- Prometheus adds 2.0 to its default `scrape_protocols`.
- Grafana Alloy or the OpenTelemetry Collector lists a 2.0 protocol.
- measure takes on a feature that needs 2.0.

None of them has happened, and measure v2's declarations, state sets and info series need none of them: both kinds render as gauges in 0.0.4.

`Handler`'s page has one summary, the Go runtime's `go_gc_duration_seconds`, whose quantiles the runtime computes, so leaving out summary instruments leaves that family in place.

If you need summary instruments, exemplars, native histograms or a third-party `prometheus.Collector`, consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Go client of the Prometheus project.
