# Non-goals

This page lists what metrics leaves out on purpose and why, for a developer deciding whether it fits or planning a change. These are design decisions, so open an issue to argue for one before sending a pull request.

| Left out | Reason |
| --- | --- |
| Summary metric type | Use a histogram. Prometheus's guide recommends histograms whenever you aggregate across instances, and a summary needs a windowed-quantile implementation |
| OpenMetrics exposition and content negotiation | `Handler` serves Prometheus text format 0.0.4, which both of Prometheus's default scrape-format lists include |
| Exemplars | They need a tracing integration and OpenMetrics or protobuf exposition |
| Pushing metrics or remote write | metrics serves a pull endpoint for Prometheus to scrape |
| Protobuf exposition | Prometheus asks for protobuf first only when native histograms are on, and protobuf needs the protobuf runtime module as a dependency |
| Native histograms with exponential buckets | They need protobuf exposition and a large specialized implementation |
| Unregistering metrics or a dynamic metric lifecycle | metrics is built for a set of metrics that is fixed when the program starts |
| Third-party collectors | The `Metric` interface is sealed, so registration accepts the six built-in types only. `client_golang` has an open `Collector` interface for this |
| Float counters | Counters count whole events as `int64` |
| Gzip compression of the response | Wrap `Handler` in standard HTTP middleware |
| `Gauge.SetToCurrentTime()` | Call `g.Set(float64(time.Now().Unix()))` |

If you need summaries, custom collectors, exemplars or native histograms, consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Go client of the Prometheus project.
