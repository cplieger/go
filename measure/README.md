# measure

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/measure/v2.svg)](https://pkg.go.dev/github.com/cplieger/go/measure/v2) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=measure%2Fgo.mod)](https://github.com/cplieger/go/blob/main/measure/go.mod)

measure gives your Go service a Prometheus `/metrics` endpoint with counters, gauges, histograms, state sets and process metrics, and declares each series once, with its unit and where it is served, so the same declaration feeds the page and your charts.

It uses only the standard library, so it brings no other modules into your build. It is licensed under Apache-2.0.

## Why use it

measure is built for Go services whose set of metrics is fixed when the program starts.

- Every declaration states its unit, kind and labels, and whether `/metrics`, your charts or both serve it. Registration refuses a declaration that contradicts itself, such as `_total` on a gauge or a `_seconds` name in bytes.
- Counters, gauges and histograms each have a labeled variant, plus a timer and a `RecordHTTP` hook for middleware. A state set moves its one current state atomically, and an info series holds one label set at 1.
- Seeds put the label sets known at start on the page at 0, so an `increase()` rule sees the first failure, and a labeled histogram seeds a label set added at run time.
- A bad declaration never panics at construction. `Register` returns the error, and `MustRegister` panics at startup naming the metric.
- The handler writes valid Prometheus text format 0.0.4, with invalid UTF-8 replaced by U+FFFD.
- Every registry adds the Go and process metrics that client_golang's default collectors serve, under the same names, so stock Go dashboards work. On Linux and macOS, the process ones the OS gives a program without cgo appear when their reads succeed.
- A labeled metric logs one warning when it reaches 1,000 series, so a runaway label shows in your logs.
- A collector serves values your code reads when Prometheus scrapes, from a store, a file or a device, as gauges, counters, histograms, state sets or info series with any number of labels. A collector whose own read fails serves its last successful samples again, so no alert resolves on a scrape that told it nothing.
- A count read from somewhere else says how that read went: six read states, one rule for folding the reads of a scope's parts, and one series pair per scope, its state and the time of its last whole read.
- `Gather` reads a registry as structured values, so a test can check a metric without parsing the page, and `GatherOf` reads only the declarations you name.
- Recording and scraping are safe from many goroutines at once.

Consider [prometheus/client_golang](https://github.com/prometheus/client_golang), the Prometheus project's Go client, if you need summary instruments, exemplars, native histograms or a third-party `prometheus.Collector`. Consider [VictoriaMetrics/metrics](https://github.com/VictoriaMetrics/metrics) if you push metrics to remote storage.

## Install

```sh
go get github.com/cplieger/go/measure/v2@latest
```

## Usage

```go
package main

import (
	"log"
	"net/http"

	"github.com/cplieger/go/measure/v2"
)

var (
	reg  = measure.NewRegistry("myapp")
	reqs = measure.NewLabeledCounter("http_requests_total", "Total HTTP requests", measure.UnitCount,
		[]string{"method", "route", "code"})
	dur = measure.NewHistogram("http_request_duration_seconds", "Request latency", measure.UnitSeconds)
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
var scan = measure.NewLabeledHistogram("scan_seconds", "Scan duration", measure.UnitSeconds, []string{"kind"},
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
var depth = measure.NewGaugeDesc("queue_depth", "Items waiting in each queue", measure.UnitCount, []string{"queue"})

func init() {
	reg.MustRegister(measure.NewCollector(func(ctx context.Context, e *measure.Emitter) {
		for name, n := range store.QueueDepths(ctx) {
			e.Emit(depth, float64(n), name)
		}
	}, depth))
}
```

To say how the read behind a count went, keep a `Read` per scope and export it beside the count. A read cut short keeps the time of the last whole read, a connection's read folds its repositories', and a count folds the signals it needs:

```go
var signal = measure.NewReadDescs("signal", "Each signal's read", []string{"connection", "signal"})

read := measure.Whole(time.Now())                      // a whole read
read = read.Cut(measure.ReadPartial, "rate_limited")   // a later read stopped early: same time
conn := measure.FoldParts(repoReads...)                // the connection's read from its repositories'
ready := measure.FoldNeeds(pullsRead, checksRead)      // a count that needs both signals
e.EmitRead(signal, conn, "github", "checks")           // in a collector: <prefix>_signal_state and _complete_timestamp_seconds
```

To declare a series only your charts read, export it to `chartdata` alone. The page never serves it, and only such a series may carry a label with no bound, such as a user's name:

```go
var turns = measure.NewLabeledCounter("turns_total", "Turns taken, by agent", measure.UnitCount, []string{"agent"},
	measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
```

## Patterns

The [package reference](https://pkg.go.dev/github.com/cplieger/go/measure/v2) has a runnable example of each pattern except serving `Handler` alone and taking a series with no labels off the page, which tests check, so `go test` keeps them true.

- Seed a labeled counter at 0. A labeled counter has no series until its first record, so an alert on `increase()` cannot tell no failures from no series. Pass the label sets known at startup, from your configuration or an enum, with `WithSeed`, and record a value added later with `Add(0, labels...)` where your code adds it. A labeled histogram's label set added later, such as a profile created in the UI, takes `Seed(labels...)`, which serves every bucket, `_sum` and `_count` at 0 until its first observation. The HTTP request counter is the exception. Its status codes are not known at startup, so it stays unseeded. `increase()` misses the request that creates each of its series, so a rule that must see a single request adds the series that appeared within the window, as in `increase(c[15m]) > 0 or (c unless c offset 15m) > 0`. A rule on sustained volume can ignore the miss.
- Remove a series with `Delete`. Never `Reset` and then set the survivors again: a scrape between the two sees an empty family, and every alert on it resolves.
- Serve `Handler` alone. Its page already includes the Go and process metrics.
- Time each route beside `RecordHTTP`. `RecordHTTP` takes an unlabeled histogram, so observe a `LabeledHistogram` keyed by route in the same middleware, and pass nil as `RecordHTTP`'s histogram.
- Publish build information as `build_info`, an `Info` series with a `version` label. The registry prefix makes it `<prefix>_build_info{version="..."} 1`, which a query joins onto any other series of the app.
- Declare a 1-of-n state, such as a mode or a health, as a `StateSet`, so a scrape never sees two states at 1 or none.
- Export a count read elsewhere at its last whole read's value, beside its scope's `NewReadDescs` pair, and key alert rules on the state and the time, never on the count alone. Before the first whole read, emit no count: its state reads `unread` and its time 0.
- Call `Keep(err)` in a collector whose own read failed, instead of emitting nothing: the scrape serves the last successful run's samples, so a firing alert never resolves on a read that told it nothing.
- Take a value that is unknown for a while off the page with `Delete` on its label set. For a series with no labels, declare a `LabeledGauge` with no labels, whose `Delete()` removes it until the next `Set`.

## API

- Metrics: `NewCounter`, `NewGauge`, `NewHistogram`, their labeled variants `NewLabeledCounter`, `NewLabeledGauge` and `NewLabeledHistogram`, `NewStateSet` and `NewInfo`.
- Run-time seeding: `(*LabeledHistogram).Seed`, and a labeled counter seeds with `Add(0, labels...)`.
- Declarations: `Unit`, `Kind`, `Export` and `Source` with their constants, the `Option` functions `WithExport`, `WithUnbounded`, `WithSeed` and `WithBuckets`, and `Declaration`, which every instrument and `Desc` returns through the sealed `Declared` interface.
- Buckets: `DefaultBuckets` and `APIBuckets`.
- Timing and HTTP: `NewTimer`, `(*LabeledHistogram).NewTimer` and `RecordHTTP`.
- Registry: `NewRegistry` with its `WithLogger` option (`RegistryOption`), `Register`, `MustRegister`, `Handler` and the `Metric` interface, which only this package can implement. An `ExportChartdata` `Desc` registers on its own too, reserving its name for a series only charts draw.
- Collectors: `NewGaugeDesc`, `NewCounterDesc`, `NewHistogramDesc`, `NewStateSetDesc`, `NewInfoDesc`, `NewCollector`, `(*Emitter).Emit`, `(*Emitter).EmitHistogram`, `(*Emitter).EmitState` and `(*Emitter).Keep`.
- Read state: `ReadState` with `ReadComplete`, `ReadPartial`, `ReadBlind`, `ReadUnread`, `ReadUnsupported` and `ReadExcluded`, `ReadStates`, `Read`, `Whole`, `(Read).Cut`, `(Read).Under`, `FoldParts`, `FoldNeeds`, `ReadDescs`, `NewReadDescs` and `(*Emitter).EmitRead`.
- Reading: `Gather` and `GatherOf`, which return `Family` values with their `Type`, `Sample`, `Label`, `HistogramValue`, `Bucket`, `SummaryValue` and `Quantile`; `Find` and `(*Family).Sample` pick a family and a series.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/measure/v2). Its examples are runnable, and `go test` keeps them true. [How measure works](docs/how-it-works.md) gives the bucket values, the record and delete methods and the limits of each type.

## Errors surface at registration

A constructor never panics on an invalid metric name, an invalid, reserved or duplicate label name, more than 8 labels on an instrument, bad histogram buckets, or a declaration that contradicts itself: a unit off the list, a unit suffix the unit disagrees with, `_total` on anything but a counter, or a label declared unbounded on a series `/metrics` serves. [Declarations](docs/declarations.md#what-registration-refuses) lists every refusal. It captures the error into the metric, and the error surfaces when you register it. `Register` returns it, and `MustRegister` panics on the first one. Use `MustRegister` for metrics declared at package level, where no caller can take an error.

`Register` also rejects a metric that is already registered, a nil metric and a name that collides with another metric or with a process metric. When `Register` refuses a metric because its name collides, the metric stays unattached, and you can still register it with a different registry. A metric with a construction error stays broken, so build a new one with valid arguments. Every `Register` call reports an invalid registry prefix.

A metric that carries a construction error records nothing and never reaches the output. Its first dropped record logs one warning. Three mistakes panic at the call that makes them. A record on a labeled metric with the wrong number of label values panics, and so do a negative `Add` on a counter and a state the state set does not declare.

## Output follows the Prometheus text format

`Handler` serves Prometheus text exposition format 0.0.4, the plain-text format every Prometheus server can scrape.

- Label values escape only `\`, `"` and newline. HELP text escapes only `\` and newline.
- Invalid UTF-8 in a label value or HELP text is replaced with U+FFFD and never panics. On an instrument, a label value logs one warning per new series, and HELP text one warning per metric. A collector logs it once when it appears and once when it clears.
- Every histogram has a `+Inf` bucket equal to its `_count`.
- A state set and an info series render as gauges, since the format has no type for either.
- Whole numbers render as bare integers such as `42`, other values in the shortest form that reads back exactly, and non-finite values as `+Inf`, `-Inf` and `NaN`.

Every registry serves the families client_golang's default Go and process collectors serve, by the same names and types: the `go_memstats_*` families, `go_gc_duration_seconds` (a summary of recent GC pauses), the GOGC, GOMEMLIMIT and GOMAXPROCS settings, `go_goroutines`, `go_threads` and `go_info`. On Linux it adds CPU time, resident and virtual memory, file descriptors and network traffic from `/proc` when their reads succeed. On macOS it adds CPU time, open file descriptors and the file descriptor and address space limits, the set client_golang serves there without cgo. Two families are measure's own: `process_gc_pause_seconds_total` and `process_uptime_seconds`. [Process metrics](docs/how-it-works.md#process-metrics) lists them all.

## Unsupported by design

measure leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- A store of past values. measure keeps current values only; Prometheus keeps their history.
- Alert rules and thresholds, which live in the app's own rule files.
- Downsampling.
- Summary instruments. Use a histogram. The one summary on the page is the Go runtime's `go_gc_duration_seconds`.
- OpenMetrics and protobuf exposition, and the content negotiation between formats.
- Exemplars and native histograms.
- Pushing metrics or remote write.
- Removing a metric from a registry after it is registered.
- Third-party collectors. A collector is a function you give `NewCollector`, and only this package's types can be registered; reading a registry opens no way into it.
- Float counter instruments, gzip compression of the response and `Gauge.SetToCurrentTime`.

## Moving from v1

The page an unchanged declaration serves is byte for byte v1's, which a test checks against v1's own page on every release.

- Change every import of `github.com/cplieger/go/measure` to `github.com/cplieger/go/measure/v2`.
- Pass a unit after the HELP text of every constructor, including the collector's `NewXDesc` ones: the one the name's suffix states, `measure.UnitCount` for a count, `measure.UnitNone` otherwise.
- Replace `measure.HistogramOption` with `measure.Option`.
- A gauge whose name ends in `_total` is refused, since Prometheus naming keeps `_total` for counters: declare it a counter if it only rises, or give it a name without the suffix.
- Optionally, replace hand-written seeding loops with `WithSeed`, a `build_info` gauge with `NewInfo`, and a hand-written 1-of-n gauge with `NewStateSet`; each serves the same lines.

## Documentation

- [How measure works](docs/how-it-works.md) covers each metric type, the registry, collectors, read state, reading a registry with `Gather` and `GatherOf`, label values, HTTP instrumentation, process metrics and the output format.
- [Declarations](docs/declarations.md) covers units, kinds, export, unbounded labels, seeding, taking a series off the page and every refusal.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Credits

Three designs follow [prometheus/client_golang](https://github.com/prometheus/client_golang). A construction error is captured into the metric value, `MustRegister` panics on the first registration error, and the Go and process families take its default collectors' names and value mappings. No code is taken from it.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
