# Non-goals

This page lists what measure leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions, so open an issue to argue for one before sending a pull request.

| Left out | Reason |
| --- | --- |
| Summary instruments | Use a histogram. Prometheus's guide recommends histograms whenever you aggregate across instances, and a summary needs a windowed-quantile implementation. The one summary on the page is the Go runtime's `go_gc_duration_seconds`, whose quantiles the runtime computes |
| OpenMetrics exposition and content negotiation | `Handler` serves Prometheus text format 0.0.4, which both of Prometheus's default scrape-format lists include. Any of these reopens OpenMetrics 2.0: its spec publishes 2.0.0 without `-rc`, Prometheus adds 2.0 to its default `scrape_protocols`, Grafana Alloy or the OpenTelemetry Collector lists a 2.0 protocol, or measure takes on a feature that needs 2.0 |
| Exemplars | They need a tracing integration and OpenMetrics or protobuf exposition |
| Pushing metrics or remote write | measure serves a pull endpoint for Prometheus to scrape |
| Protobuf exposition | Prometheus asks for protobuf first only when native histograms are on, and protobuf needs the protobuf runtime module as a dependency |
| Native histograms with exponential buckets | They need protobuf exposition and a large specialized implementation |
| Unregistering metrics or a dynamic metric lifecycle | measure is built for a set of metrics that is fixed when the program starts |
| Third-party collectors | The `Metric` interface is sealed. A collector is a function given to `NewCollector`, so registration accepts this package's types only. `client_golang` has an open `Collector` interface for a third-party `prometheus.Collector` |
| Float counter instruments | The counter types count whole events as `int64`. A collector counter reports a `float64` total it reads elsewhere |
| Gzip compression of the response | Wrap `Handler` in standard HTTP middleware |
| `Gauge.SetToCurrentTime()` | Call `g.Set(float64(time.Now().Unix()))` |

If you need summary instruments, exemplars, native histograms or a third-party `prometheus.Collector`, consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Go client of the Prometheus project.
