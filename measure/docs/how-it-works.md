# How metrics works

This page is for a Go developer using metrics in a service. It covers each metric type and its methods, the registry, construction errors, label values, HTTP instrumentation, process metrics, the output format and the low-level writers.

## Metric types

Every constructor takes a metric name and its HELP text. The labeled constructors also take the label names, and a labeled metric accepts at most 8 of them.

| Type | Constructor | Methods |
| --- | --- | --- |
| Counter | `NewCounter(name, help)` | `Inc()`, `Add(n int64)` |
| Labeled counter | `NewLabeledCounter(name, help, labels)` | `Inc(vals...)`, `Add(n, vals...)`, `Delete(vals...)`, `Reset()` |
| Gauge | `NewGauge(name, help)` | `Set`, `Add`, `Sub`, `Inc`, `Dec`, `Get`, all on `float64` |
| Labeled gauge | `NewLabeledGauge(name, help, labels)` | `Set(v, vals...)`, `Delete(vals...)`, `Reset()` |
| Histogram | `NewHistogram(name, help, opts...)` | `Observe(seconds)` |
| Labeled histogram | `NewLabeledHistogram(name, help, labels, opts...)` | `Observe(seconds, vals...)`, `Delete(vals...)`, `Reset()`, `NewTimer(vals...)` |

Counters hold `int64` values. A counter saturates at `math.MaxInt64` instead of wrapping to a negative value, so the series stays monotonic. A negative `Add` panics.

On a labeled metric, every record, `Delete` and `NewTimer` call takes one value per label name, in the order the names were given. A call with a different number of values panics. `Delete` removes one label combination, and `Reset` removes all of them.

## Buckets

A histogram uses `DefaultBuckets` unless you pass `WithBuckets`. Both presets return a fresh slice on every call, so a caller that edits one changes nothing for other callers.

| Preset | Bounds in seconds | Suits |
| --- | --- | --- |
| `DefaultBuckets()` | 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0 | HTTP request latency |
| `APIBuckets()` | 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30 | outbound API calls and slow collect or scan cycles, which the default set would put in `+Inf` |

`WithBuckets` sets custom bounds. They must be a strictly increasing sequence of finite values. The `le="+Inf"` bucket is added for you, so do not include `+Inf`. A non-finite, duplicate or out-of-order bound is a construction error. An empty slice gives a histogram with only the `+Inf` bucket.

## Timers

`NewTimer(h)` starts a timer for an unlabeled histogram. `(*LabeledHistogram).NewTimer(vals...)` starts one for a label set, so a per-label latency can use `defer t.ObserveDuration()`. `ObserveDuration` records the elapsed time in seconds and returns it as a `time.Duration`.

## The registry

`NewRegistry(prefix)` creates a registry that prefixes every metric it registers with `<prefix>_`. Process metrics keep their own names. Pass `""` for no prefix. Build a registry only with `NewRegistry`, because the zero `Registry` panics on its first registration. Histograms and the three labeled types also need their constructors, because their zero values panic on the first record.

`Register(m)` accepts any of the six metric types. It returns an error in each of these cases:

- The metric captured a construction error, described in the next section.
- The metric is already registered.
- The metric's name collides with a registered metric or with a process metric name. A histogram also claims its `_bucket`, `_sum` and `_count` names.
- The metric is nil.

On an error the metric is not attached and the registry is unchanged. A metric refused for a name collision can still be registered with a different registry. `MustRegister(ms...)` registers each metric in order and panics on the first error. Use it for metrics declared at package level and registered in `init`, where no caller can take an error.

`Handler()` returns the `http.HandlerFunc` that serves every registered metric plus the process metrics.

## Construction errors

A constructor never panics on an invalid metric name, an invalid, reserved or duplicate label name, more than 8 label names, or bad buckets. It captures the error into the metric, and each error names the metric. The error surfaces when you register the metric. `Register` returns it, and `MustRegister` panics on it. A label name starting with `__` is reserved, and so is `le` on a labeled histogram.

A construction error cannot be cleared. Rebuild the metric with a valid name, label set or buckets.

A metric that carries a construction error records nothing. Its `Inc`, `Add`, `Set` and `Observe` calls do nothing, and the first one logs a warning through `log/slog` that names the metric and the error. If you record through an invalid metric before registering it, the first dropped record still leaves a trace in your logs. The metric never reaches the output, and the low-level writers write nothing for it.

An invalid registry prefix is captured the same way. Every `Register` and `MustRegister` call on that registry reports it, because no metric under that prefix could have a valid name.

## Label values

Label values are yours to choose. Invalid UTF-8 never panics. The value is repaired with the Unicode replacement character U+FFFD, and a warning naming the metric is logged when the repaired series is first created. Later records onto that series do not warn again.

Repair has two costs. Distinct raw values that repair to the same string share one series. Every record carrying invalid UTF-8 takes the slower series-lookup path, even after the repaired series exists. Validate values that come from untrusted input before you record them. `Delete` repairs its values the same way, so calling it with the original raw values removes the series that recording created.

Each distinct label combination is a series that stays until `Delete` or `Reset`. A label filled from a raw request path or a header grows memory and scrape size without bound, so map paths to a fixed set of routes first. When a labeled metric reaches 1,000 series, it logs one warning naming the metric. The threshold is fixed and cannot be configured. The warning does not cap or reject anything, and it fires once per metric even when the count drops and rises again.

## HTTP instrumentation

`RecordHTTP(c, h, d, labelVals...)` records one request. It increments the labeled counter `c` with `labelVals` and observes `d` in seconds on the histogram `h`. Either metric may be nil to skip it. The values must match `c`'s label names in count and order.

`RecordHTTP` takes no request and no status, so your middleware captures both and calls it once the response is complete. Its parameters are this package's own types. Middleware that does not import metrics cannot take `RecordHTTP` as its hook, so you write a small adapter that turns the middleware's per-request values into the label list.

With [webhttp](https://github.com/cplieger/go/tree/main/httpserver), adapt the access-log hook that `WithRecordRouteMetric` registers. Its method and path pair comes from the route table, so the number of series stays bounded however much traffic arrives. The access logger records the status for you. metrics has no dependencies, so it cannot ship a compiling example of that pairing. The hook's signature is in [webhttp's reference for `WithRecordRouteMetric`](https://pkg.go.dev/github.com/cplieger/go/httpserver#WithRecordRouteMetric).

## Process metrics

Every registry serves these metrics without any registration:

| Metric | Platforms |
| --- | --- |
| `go_goroutines` | all |
| `go_memstats_heap_alloc_bytes` | all |
| `process_gc_pause_seconds_total` | all |
| `process_uptime_seconds` | all |
| `process_start_time_seconds` | all |
| `process_cpu_seconds_total` | Linux |
| `process_resident_memory_bytes` | Linux |
| `process_open_fds` | Linux |
| `process_max_fds` | Linux |

The Linux metrics come from `/proc/self`, and each appears only when its read succeeds. `go_goroutines` and `go_memstats_heap_alloc_bytes` have the names `client_golang` uses. All nine names are reserved in every registry, so a metric of yours cannot take one.

## Output format

`Handler` answers with `Content-Type: text/plain; version=0.0.4; charset=utf-8` and `X-Content-Type-Options: nosniff`. The body is Prometheus text exposition format 0.0.4.

- Label values escape only backslash, double quote and newline, as `\\`, `\"` and `\n`. HELP text escapes only backslash and newline.
- Metric names, label names and bucket bounds are checked when the metric is built. A violation becomes a construction error, so an invalid metric never reaches the output.
- Label values and HELP text are always valid UTF-8. Invalid input is repaired with U+FFFD and logged once, for a label value when its series is created and for HELP text when the metric is built.
- Every histogram has a `+Inf` bucket equal to its `_count`.

One formatter renders every number. Whole values render as bare integers such as `42`. Other values use the shortest form that reads back to the same `float64`. Non-finite values render as `+Inf`, `-Inf` and `NaN`.

Recording and scraping are safe to run from many goroutines at once, and both are safe while metrics are being registered.

## Low-level writers

`WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteLabeledGauge`, `WriteHistogram`, `WriteLabeledHistogram` and `WriteProcess` append one metric, or the process metrics, to a `*strings.Builder` in the same format `Handler` serves. Use them to build a custom handler. A metric carrying a construction error writes nothing.

Finish every `Register` and `MustRegister` call before a custom handler built on these writers starts serving. The writers read a metric's name without the registry's lock, and registration sets the prefixed name.
