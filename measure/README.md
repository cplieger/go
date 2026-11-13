# measure

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/measure.svg)](https://pkg.go.dev/github.com/cplieger/go/measure) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=measure%2Fgo.mod)](https://github.com/cplieger/go/blob/main/measure/go.mod)

measure gives your Go service a Prometheus `/metrics` endpoint with counters, gauges, histograms and process metrics, using only the standard library.

It uses only the standard library, so it brings no other modules into your build. It is licensed under Apache-2.0.

## Why use it

measure is built for Go services whose set of metrics is fixed when the program starts.

- Counters, gauges and histograms each have a labeled variant, plus a timer and a `RecordHTTP` hook for middleware.
- A bad name, label set or bucket layout never panics at construction. `Register` returns the error, and `MustRegister` panics at startup naming the metric.
- The handler writes valid Prometheus text format 0.0.4, with invalid UTF-8 replaced by U+FFFD.
- Every registry adds the Go and process metrics that client_golang's default collectors serve, under the same names, so stock Go dashboards work. On Linux and macOS, the process ones the OS gives a program without cgo appear when their reads succeed.
- A labeled metric logs one warning when it reaches 1,000 series, so a runaway label shows in your logs.
- A collector serves values your code reads when Prometheus scrapes, from a store, a file or a device, with any number of labels.
- `Gather` reads a registry as structured values, so a test can check a metric without parsing the page.
- Recording and scraping are safe from many goroutines at once.

Consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Prometheus project's Go client, if you need summary instruments, exemplars, native histograms or a third-party `prometheus.Collector`. Consider [VictoriaMetrics/metrics](https://github.com/VictoriaMetrics/metrics) if you push metrics to remote storage.

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
	reqs = measure.NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"method", "route", "code"})
	dur  = measure.NewHistogram("http_request_duration_seconds", "Request latency")
)

func main() {
	reg.MustRegister(reqs, dur) // panics here, at startup, on a bad metric

	reqs.Inc("GET", "/api/widget", "200")
	dur.Observe(0.042)

	http.Handle("/metrics", reg.Handler())
	log.Fatal(http.ListenAndServe(":8080", nil))
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

// methodLabel maps the client's method onto the nine standard ones, or
// "other", since r.Method is whatever the client sent.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return m
	}
	return "other"
}

func instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		measure.RecordHTTP(reqs, dur, time.Since(start), methodLabel(r.Method), route, strconv.Itoa(sw.status))
	})
}
```

Label by the route you registered, never by the raw request path, and name the labels `method`, `route` and `code`, as client_golang's promhttp middleware (`InstrumentHandlerCounter`) names `method` and `code`. `r.Method` is whatever the client sent, so map it onto the standard methods first, as httpserver's `WithRecordRouteMetric` hook does. Each distinct label combination is a series kept until `Delete` or `Reset`. The [HTTP instrumentation](docs/how-it-works.md#http-instrumentation) section covers the adapter for [httpserver](https://github.com/cplieger/go/tree/main/httpserver)'s route hook.

To serve values read when Prometheus scrapes, declare their families and register a collector. Every scrape calls the function with the request's context:

```go
var depth = measure.NewGaugeDesc("queue_depth", "Items waiting in each queue", []string{"queue"})

func init() {
	reg.MustRegister(measure.NewCollector(func(ctx context.Context, e *measure.Emitter) {
		for name, n := range store.QueueDepths(ctx) {
			e.Emit(depth, float64(n), name)
		}
	}, depth))
}
```

## Patterns

The [package reference](https://pkg.go.dev/github.com/cplieger/go/measure) has a runnable example of each pattern except serving `Handler` alone, which a test checks, so `go test` keeps them true.

- Seed a labeled counter at 0. A labeled counter has no series until its first record, so an alert on `increase()` cannot tell no failures from no series. Call `Add(0, labels...)` for each label set you know of at startup. The HTTP request counter is the exception. Its status codes are not known at startup, so it stays unseeded. `increase()` misses the request that creates each of its series, so a rule that must see a single request adds the series that appeared within the window, as in `increase(c[15m]) > 0 or (c unless c offset 15m) > 0`. A rule on sustained volume can ignore the miss.
- Remove a series with `Delete`. Never `Reset` and then set the survivors again: a scrape between the two sees an empty family, and every alert on it resolves.
- Serve `Handler` alone. Its page already includes the Go and process metrics.
- Time each route beside `RecordHTTP`. `RecordHTTP` takes an unlabeled histogram, so observe a `LabeledHistogram` keyed by route in the same middleware, and pass nil as `RecordHTTP`'s histogram.
- Publish build information as `build_info`, a `LabeledGauge` with a `version` label, set to 1. The registry prefix makes it `<prefix>_build_info{version="..."}`, which a query joins onto any other series of the app.

## API

- Metrics: `NewCounter`, `NewGauge`, `NewHistogram` and their labeled variants `NewLabeledCounter`, `NewLabeledGauge` and `NewLabeledHistogram`.
- Buckets: `DefaultBuckets`, `APIBuckets` and the `WithBuckets` option.
- Timing and HTTP: `NewTimer`, `(*LabeledHistogram).NewTimer` and `RecordHTTP`.
- Registry: `NewRegistry` with its `WithLogger` option (`RegistryOption`), `Register`, `MustRegister`, `Handler` and the `Metric` interface, which only this package can implement.
- Collectors: `NewGaugeDesc`, `NewCounterDesc`, `NewCollector` and `(*Emitter).Emit`.
- Reading: `Gather`, which returns `Family` values with their `Type`, `Sample`, `Label`, `HistogramValue`, `Bucket`, `SummaryValue` and `Quantile`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/measure). Its examples are runnable, and `go test` keeps them true. [How measure works](docs/how-it-works.md) gives the bucket values, the record and delete methods and the limits of each type.

## Errors surface at registration

A constructor never panics on an invalid metric name, an invalid, reserved or duplicate label name, more than 8 labels on an instrument, or bad histogram buckets. It captures the error into the metric, and the error surfaces when you register it. `Register` returns it, and `MustRegister` panics on the first one. Use `MustRegister` for metrics declared at package level, where no caller can take an error.

`Register` also rejects a metric that is already registered, a nil metric and a name that collides with another metric or with a process metric. When `Register` refuses a metric because its name collides, the metric stays unattached, and you can still register it with a different registry. A metric with a construction error stays broken, so build a new one with valid arguments. Every `Register` call reports an invalid registry prefix.

A metric that carries a construction error records nothing and never reaches the output. Its first dropped record logs one warning. Two mistakes panic at the call that makes them. A record on a labeled metric with the wrong number of label values panics, and so does a negative `Add` on a counter.

## Output follows the Prometheus text format

`Handler` serves Prometheus text exposition format 0.0.4, the plain-text format every Prometheus server can scrape.

- Label values escape only `\`, `"` and newline. HELP text escapes only `\` and newline.
- Invalid UTF-8 in a label value or HELP text is replaced with U+FFFD and never panics. On an instrument, a label value logs one warning per new series, and HELP text one warning per metric. A collector logs it once when it appears and once when it clears.
- Every histogram has a `+Inf` bucket equal to its `_count`.
- Whole numbers render as bare integers such as `42`, other values in the shortest form that reads back exactly, and non-finite values as `+Inf`, `-Inf` and `NaN`.

Every registry serves the families client_golang's default Go and process collectors serve, by the same names and types: the `go_memstats_*` families, `go_gc_duration_seconds` (a summary of recent GC pauses), the GOGC, GOMEMLIMIT and GOMAXPROCS settings, `go_goroutines`, `go_threads` and `go_info`. On Linux it adds CPU time, resident and virtual memory, file descriptors and network traffic from `/proc` when their reads succeed. On macOS it adds CPU time, open file descriptors and the file descriptor and address space limits, the set client_golang serves there without cgo. Two families are measure's own: `process_gc_pause_seconds_total` and `process_uptime_seconds`. [Process metrics](docs/how-it-works.md#process-metrics) lists them all.

## Unsupported by design

measure leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- Summary instruments. Use a histogram. The one summary on the page is the Go runtime's `go_gc_duration_seconds`.
- OpenMetrics and protobuf exposition, and the content negotiation between formats.
- Exemplars and native histograms.
- Pushing metrics or remote write.
- Removing a metric from a registry after it is registered.
- Third-party collectors. A collector is a function you give `NewCollector`, and only this package's types can be registered.
- Float counter instruments, gzip compression of the response and `Gauge.SetToCurrentTime`.

## Documentation

- [How measure works](docs/how-it-works.md) covers each metric type, the registry, collectors, reading a registry with `Gather`, label values, HTTP instrumentation, process metrics and the output format.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Credits

Three designs follow [prometheus/client_golang](https://github.com/prometheus/client_golang). A construction error is captured into the metric value, `MustRegister` panics on the first registration error, and the Go and process families take its default collectors' names and value mappings. No code is taken from it.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
