# metrics

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/measure.svg)](https://pkg.go.dev/github.com/cplieger/go/measure) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=measure%2Fgo.mod)](https://github.com/cplieger/go/blob/main/measure/go.mod) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/metrics/badges/mutation.json)](https://github.com/cplieger/go/issues?q=label%3Agremlins-tracker)

measure gives your Go service a Prometheus `/metrics` endpoint with counters, gauges, histograms and process metrics, using only the standard library.

It uses only the standard library, so it brings no other modules into your build. It needs Go 1.27.1 or later and is licensed under Apache-2.0. It is a v4 module that follows semantic versioning.

## Why use it

metrics is built for Go services whose set of metrics is fixed when the program starts.

- Counters, gauges and histograms each have a labeled variant, plus a timer and a `RecordHTTP` hook for middleware.
- A bad name, label set or bucket layout never panics at construction. `Register` returns the error, and `MustRegister` panics at startup naming the metric.
- The handler writes valid Prometheus text format 0.0.4, with invalid UTF-8 replaced by U+FFFD.
- Every registry adds Go and process metrics. On Linux, it also adds CPU time, resident memory and file descriptors when their `/proc` reads succeed.
- A labeled metric logs one warning when it reaches 1,000 series, so a runaway label shows in your logs.
- Recording and scraping are safe from many goroutines at once.

Consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Prometheus project's Go client, if you need summaries, custom collectors, exemplars or native histograms. Consider [VictoriaMetrics/metrics](https://github.com/VictoriaMetrics/metrics) if you push metrics to remote storage.

## Install

```sh
go get github.com/cplieger/go/measure@latest
```

## Usage

```go
package main

import (
	"log"
	"net/http"

	"github.com/cplieger/go/measure"
)

var (
	reg  = measure.NewRegistry("myapp")
	reqs = measure.NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"route", "status"})
	dur  = measure.NewHistogram("http_request_duration_seconds", "Request latency")
)

func main() {
	reg.MustRegister(reqs, dur) // panics here, at startup, on a bad metric

	reqs.Inc("/api/widget", "200")
	dur.Observe(0.042)

	http.Handle("/metrics", reg.Handler())
	log.Fatal(http.ListenAndServe(":9090", nil))
}
```

The registry prefixes every name it registers, so Prometheus sees `myapp_http_requests_total` and `myapp_http_request_duration_seconds`. Pass `""` for no prefix. `Register` returns the error instead of panicking.

To time a code path for one label set, start a timer from a labeled histogram. `APIBuckets` suits slow calls that the default buckets, which stop at 1 second, would put in `+Inf`:

```go
var scan = measure.NewLabeledHistogram("scan_seconds", "Scan duration", []string{"kind"},
	measure.WithBuckets(measure.APIBuckets()))

func fullScan() {
	t := scan.NewTimer("full")
	defer t.ObserveDuration()
	// ... the work being timed ...
}
```

To record HTTP requests, call `RecordHTTP` from middleware once the status is known. `RecordHTTP` takes the elapsed time and the label values only, so your middleware measures the time and captures the status code to pass as a label:

```go
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		measure.RecordHTTP(reqs, dur, time.Since(start), route, strconv.Itoa(sw.status))
	})
}
```

Label by the route you registered, never by the raw request path. Each distinct label combination is a series kept until `Delete` or `Reset`. The [HTTP instrumentation](docs/how-it-works.md#http-instrumentation) section covers the adapter for [webhttp](https://github.com/cplieger/go/tree/main/httpserver)'s route hook.

## API

- Metrics: `NewCounter`, `NewGauge`, `NewHistogram` and their labeled variants `NewLabeledCounter`, `NewLabeledGauge` and `NewLabeledHistogram`.
- Buckets: `DefaultBuckets`, `APIBuckets` and the `WithBuckets` option.
- Timing and HTTP: `NewTimer`, `(*LabeledHistogram).NewTimer` and `RecordHTTP`.
- Registry: `NewRegistry`, `Register`, `MustRegister`, `Handler` and the `Metric` interface, which only this package can implement.
- Custom handlers: `WriteCounter`, `WriteGauge`, `WriteHistogram`, their labeled variants and `WriteProcess`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/measure). Its `Example` function is runnable, and `go test` keeps it true. [How metrics works](docs/how-it-works.md) gives the bucket values, the record and delete methods and the limits of each type.

## Errors surface at registration

A constructor never panics on an invalid metric name, an invalid, reserved or duplicate label name, more than 8 labels, or bad histogram buckets. It captures the error into the metric, and the error surfaces when you register it. `Register` returns it, and `MustRegister` panics on the first one. Use `MustRegister` for metrics declared at package level, where no caller can take an error.

`Register` also rejects a metric that is already registered, a nil metric and a name that collides with another metric or with a process metric. When `Register` refuses a metric because its name collides, the metric stays unattached, and you can still register it with a different registry. A metric with a construction error stays broken, so build a new one with valid arguments. Every `Register` call reports an invalid registry prefix.

A metric that carries a construction error records nothing and never reaches the output. Its first dropped record logs one warning. Two mistakes panic at the call that makes them. A record on a labeled metric with the wrong number of label values panics, and so does a negative `Add` on a counter.

## Output follows the Prometheus text format

`Handler` serves Prometheus text exposition format 0.0.4, the plain-text format every Prometheus server can scrape.

- Label values escape only `\`, `"` and newline. HELP text escapes only `\` and newline.
- Invalid UTF-8 in a label value or HELP text is replaced with U+FFFD and never panics. A label value logs one warning per new series, and HELP text one warning per metric.
- Every histogram has a `+Inf` bucket equal to its `_count`.
- Whole numbers render as bare integers such as `42`, other values in the shortest form that reads back exactly, and non-finite values as `+Inf`, `-Inf` and `NaN`.

The process metrics are `go_goroutines`, `go_memstats_heap_alloc_bytes`, `process_gc_pause_seconds_total`, `process_uptime_seconds` and `process_start_time_seconds`. On Linux, `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds` and `process_max_fds` are added when their `/proc` reads succeed. The goroutine and heap names match `client_golang`'s, so dashboards built on those two names keep working.

## Unsupported by design

metrics leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- The summary metric type. Use a histogram.
- OpenMetrics and protobuf exposition, and the content negotiation between formats.
- Exemplars and native histograms.
- Pushing metrics or remote write.
- Removing a metric from a registry after it is registered.
- Third-party collectors. Only this package's six metric types can be registered.
- Float counters, gzip compression of the response and `Gauge.SetToCurrentTime`.

## Documentation

- [How metrics works](docs/how-it-works.md) covers each metric type, the registry, label values, HTTP instrumentation, process metrics and the low-level writers.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Credits

Two designs follow [prometheus/client_golang](https://github.com/prometheus/client_golang). A construction error is captured into the metric value, and `MustRegister` panics on the first registration error. No code is taken from it.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
