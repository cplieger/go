# How measure works

This page is for a Go developer using measure in a service. It covers each metric type and its methods, the registry, collectors, read state, reading a registry with `Gather` and `GatherOf`, construction errors, label values, HTTP instrumentation, process metrics and the output format. [Declarations](declarations.md) covers units, kinds, export, unbounded labels, seeding and what registration refuses.

## Metric types

Every constructor takes a metric name, its HELP text and its unit, then options. The labeled constructors also take the label names, and a labeled metric accepts at most 8 of them. A collector family takes any number, as [Collectors](#collectors) describes.

| Type | Constructor | Methods |
| --- | --- | --- |
| Counter | `NewCounter(name, help, unit, opts...)` | `Inc()`, `Add(n int64)` |
| Labeled counter | `NewLabeledCounter(name, help, unit, labels, opts...)` | `Inc(vals...)`, `Add(n, vals...)`, `Delete(vals...)`, `Reset()` |
| Gauge | `NewGauge(name, help, unit, opts...)` | `Set`, `Add`, `Sub`, `Inc`, `Dec` and `Value`, all on `float64` |
| Labeled gauge | `NewLabeledGauge(name, help, unit, labels, opts...)` | `Set(v, vals...)`, `Delete(vals...)`, `Reset()` |
| Histogram | `NewHistogram(name, help, unit, opts...)` | `Observe(v)` |
| Labeled histogram | `NewLabeledHistogram(name, help, unit, labels, opts...)` | `Observe(v, vals...)`, `Seed(vals...)`, `Delete(vals...)`, `Reset()`, `NewTimer(vals...)` |
| State set | `NewStateSet(name, help, labels, states, opts...)` | `Set(state, vals...)`, `Delete(vals...)` |
| Info | `NewInfo(name, help, labels, opts...)` | `Set(vals...)`, `Delete()` |

Counters hold `int64` values. A counter saturates at `math.MaxInt64` instead of wrapping to a negative value, so the series stays monotonic. A negative `Add` panics.

On a labeled metric, every record, `Seed`, `Delete` and `NewTimer` call takes one value per label name, in the order the names were given. A call with a different number of values panics. A state set's `Set` and `Delete` take the values of the labels before its state label, and `Set` panics on a state it does not declare. `Delete` removes one label combination, and `Reset` removes all of them.

A histogram observes values in its unit, which for a latency is seconds.

## Buckets

A histogram instrument uses `DefaultBuckets` unless you pass the `WithBuckets` option. Both presets return a fresh slice on every call, so a caller that edits one changes nothing for other callers.

| Preset | Bounds in seconds | Suits |
| --- | --- | --- |
| `DefaultBuckets()` | 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0 | HTTP request latency |
| `APIBuckets()` | 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30 | outbound API calls and slow collect or scan cycles, which the default set would put in `+Inf` |

`WithBuckets` sets custom bounds. They must be a strictly increasing sequence of finite values. The `le="+Inf"` bucket is added for you, so do not include `+Inf`. A non-finite, duplicate or out-of-order bound is a construction error. An empty slice gives a histogram with only the `+Inf` bucket.

## Timers

`NewTimer(h)` starts a timer for an unlabeled histogram. `(*LabeledHistogram).NewTimer(vals...)` starts one for a label set, so a per-label latency can use `defer t.ObserveDuration()`. `ObserveDuration` records the elapsed time in seconds and returns it as a `time.Duration`.

## The registry

`NewRegistry(prefix)` creates a registry that prefixes every metric it registers with `<prefix>_`. Process metrics keep their own names. Pass `""` for no prefix. Build a registry only with `NewRegistry`, because the zero `Registry` panics on its first registration. Histograms and the three labeled types also need their constructors, because their zero values panic on the first record.

`Register(m)` accepts any of the eight metric types and a collector. It returns an error in each of these cases:

- The metric captured a construction error, described in [Construction errors](#construction-errors), including a declaration [Declarations](declarations.md#what-registration-refuses) refuses.
- The metric is already registered.
- The metric's name collides with a registered metric or with a process metric name. A histogram also claims its `_bucket`, `_sum` and `_count` names.
- The metric is nil.

On an error the metric is not attached and the registry is unchanged. A metric refused for a name collision can still be registered with a different registry. `MustRegister(ms...)` registers each metric in order and panics on the first error. Use it for metrics declared at package level and registered in `init`, where no caller can take an error.

`Handler()` returns the `http.HandlerFunc` that serves every registered metric declared `ExportMetric` or `ExportBoth`, plus the process metrics.

## Collectors

A collector serves values your code reads when Prometheus scrapes, such as rows in a store, the age of a file or the state of a device, instead of values recorded as they happen.

- Declare each family with `NewGaugeDesc(name, help, unit, labels, opts...)`, `NewCounterDesc(name, help, unit, labels, opts...)`, `NewHistogramDesc(name, help, unit, labels, bounds, opts...)`, `NewStateSetDesc(name, help, labels, states, opts...)` or `NewInfoDesc(name, help, labels, opts...)`. A collector family takes any number of labels, and is never seeded. A collector counter holds a `float64`, because it reports a total read elsewhere. A collector histogram takes the bucket bounds its samples were counted against, under the same rules as an instrument's: finite and strictly increasing, with `le="+Inf"` implicit and `le` reserved as a label name.
- `NewCollector(collect, descs...)` builds the collector, and `Register` adds it. Registration prefixes each family name and reserves it like any other metric's, so a collector family cannot take a name another metric or a process metric holds. No descs, a nil desc, a nil `collect`, two descs with one name and a desc another collector holds are construction errors that surface at `Register`, and so are a zero `Collector` or `Desc` not built by their constructors.
- Every scrape, every `Gather` and every `GatherOf` that names one of its families calls `collect` with that request's context, once however many of its families are read. `collect` calls `Emitter.Emit(desc, value, labelValues...)` for each gauge, counter or info sample (an info value is 1), `Emitter.EmitHistogram(desc, buckets, count, sum, labelValues...)` for each histogram series, with `buckets` the cumulative count at each declared bound, and `Emitter.EmitState(desc, state, labelValues...)` for each state set. Two concurrent reads call `collect` concurrently, each with its own `Emitter`, so guard anything `collect` shares. An `Emit` after `collect` returned records nothing. The scrape waits for `collect`, so return promptly once `ctx` is done; a panic in `collect` fails that scrape.
- A sample for a desc the collector did not declare, with the wrong number of label values, with a label set already emitted in that scrape, or with a negative or NaN counter value is dropped. So is a sample that does not fit its family: a value for a histogram or state set, a histogram for any other kind, an info value other than 1, a state the state set does not declare, or a histogram whose buckets are the wrong length, fall, or exceed its count, or whose sum is NaN. The first one stands for a repeated label set. Label values are repaired before that check, so two raw values that repair to the same string are one label set, and the second is dropped. The scrape still answers with the samples that survived, and `Gather` returns the drops as an error.
- A drop logs one warning through `log/slog` when a scrape first sees it, naming the family and the problem, and one info line when a later scrape no longer does. A problem present on every scrape logs once. A family emitting 1,000 or more series, and invalid UTF-8 in a label value, which is repaired with U+FFFD, follow the same rule without dropping anything.
- The page lists collector families after the instruments and before the process metrics, in registration order, with samples sorted by label values. A family with no samples is left off the page.
- A `collect` whose own read failed, such as its store read or its snapshot, calls `Emitter.Keep(err)` instead of emitting. That scrape serves a copy of the samples of the collector's latest-started successful run again, with that run's drops, and discards whatever the failed run emitted. `Gather` returns no error for the failure, which logs one warning naming `err` when a run first keeps and one info line when a run succeeds again. A collector with no successful run yet serves nothing, since it has nothing true to repeat. A run that drops samples but never calls `Keep` counts as successful.

## Read state

A count an app reads from somewhere else, an upstream API, a directory, another process's queue, says how that read went, so a page shows the last whole value marked partial instead of a 0, and an alert keys on the read instead of on the count.

- `ReadState` has six values, in the order `ReadStates` returns them: `complete` (read whole), `partial` (part of it read: a cut list, some reads failed, a stop mid-scope), `blind` (nothing read vouches for it: every read failed, or what it is read through failed), `unread` (no read reached it yet), `unsupported` (a read the credential or the upstream can never make, a lasting limit rather than a failure) and `excluded` (the configuration leaves it out). The zero `ReadState` reads as `unread`, so a zero `Read` never reads complete. A value outside the six reads as `blind` in `FoldParts`, `FoldNeeds` and `Under`, and `EmitRead` drops its state set's samples, which `Gather` reports.
- A `Read` holds `State`, `AsOf`, the time of the last whole read whatever the state says (zero before the first), and `Reason`, the app's code for why the read is not whole, such as `rate_limited` (empty when complete). Keep reasons to a closed list of short codes: the page holds their words.
- `Whole(at)` is a complete read at `at`. `prev.Cut(state, reason)` is a later read that was not whole: it keeps `prev.AsOf`, so the time a page and a series show is always the last whole read's.
- Two folds combine several reads into one, for the two ways a value can be whole. Both start the same way. No inputs is `unread`. Every input `excluded` is `excluded`. Otherwise the excluded inputs are left out, and if every input left is `unsupported` the fold is `unsupported`. Otherwise those are left out too. `AsOf` is the oldest among the inputs counted, and zero when any of them has none, so a fold is current only when every input is. `Reason` is the worst counted input's, ranking `blind`, `unread`, `partial` and `complete` in that order, the first in argument order among equals, and empty when the fold is complete. The state, time and reason do not depend on the order of the inputs, except which of several equally bad inputs gives the reason.
  - `FoldParts(parts...)` is the read of a scope made of separate parts, each read on its own, such as a connection's repositories. Every part `complete` is `complete`. No part `complete` or `partial` is `blind` when one part is `blind`, and `unread` otherwise. Anything else is `partial`, so one failed repository out of a hundred reads `partial`.
  - `FoldNeeds(signals...)` is the read of a value computed from several signals it needs all of, such as a count of pull requests whose checks fail, which needs both the pull requests and their checks. It takes the worst of `blind`, `unread`, `partial` and `complete`, because a value missing one of its inputs is no better than that input.
- `r.Under(parent)` is a read made through another, such as a file's coverage through its media root: a read is never more complete than what it is read through. A read that is neither `complete` nor `partial` is kept, and so is any read under a `complete` or `excluded` parent; under a `blind` parent it is `blind`, and under any other parent `partial`. A read that is neither `complete` nor `partial` keeps its own `AsOf`, and so does any read under an `excluded` parent; otherwise the time is the older of the two, zero when either is. A state the parent changes takes the parent's reason.
- `NewReadDescs(stem, help, labels, opts...)` declares the pair a read scope exports: `<stem>_state`, a state set over `labels` and a last `state` label with the six states, and `<stem>_complete_timestamp_seconds`, a gauge in seconds. Register both with the collector that emits them. `Emitter.EmitRead(descs, read, labelValues...)` emits the scope's state and its `AsOf` in Unix seconds, 0 before the first whole read.
- Export each count read elsewhere at the value of its last whole read, written when the read that completes it is stored, so a later cut read changes neither the value nor its time; before the first whole read, emit no count. A rule that reads a count joins it with its scope's state and time, and `unsupported` and `excluded` never fire a degraded rule. Remove a scope's series by no longer emitting it.

## Reading a registry with `Gather` and `GatherOf`

`Gather(ctx)` returns every family as a `Family`: its name, HELP text, `Type` and samples, with numbers rather than rendered text. A test can check a metric without parsing the page.

- Families come in the page's order, with each `Desc` registered on its own after the collectors' families. A declared family with no series yet, a labeled metric, a collector family or a `Desc` registered on its own, is listed with no samples, which the page omits. An `ExportChartdata` family, which the page never serves, is listed too.
- A state set and an info family have `Type` gauge, as on the page.
- Each `Sample` has its labels in declared order. A counter or gauge sets `Value`, which for a counter is its `int64` count converted to `float64`, exact up to 2^53. A histogram sets `Histogram`, with cumulative buckets in ascending order; the `+Inf` bucket's count is `Count`. A summary sets `Summary`, with its quantiles in ascending order; only the Go runtime's `go_gc_duration_seconds` is one.
- `Gather` runs every collector with `ctx`, and returns every sample a collector dropped as one error naming each family. The families it returns hold what survived.

`GatherOf(ctx, decls...)` reads only the declarations named, one `Family` each in the order given, whatever their export. It runs the collectors those declarations need, each once, and no other, and reads no Go or process family, so a reader on its own schedule costs only what it reads. It returns an error and no families for a nil declaration or one the registry does not hold, and otherwise the drops of the collectors it ran.

`Find(fams, name)` returns the family with that name, and `Family.Sample(vals...)` the series with those label values, in declared order.

## Construction errors

A constructor never panics on an invalid metric name, an invalid, reserved or duplicate label name, more than 8 label names on a labeled metric, bad buckets, or a declaration [Declarations](declarations.md#what-registration-refuses) refuses. It captures the error into the metric, and each error names the metric. The error surfaces when you register the metric. `Register` returns it, and `MustRegister` panics on it. A label name starting with `__` is reserved, and so is `le` on a labeled histogram.

A construction error cannot be cleared. Rebuild the metric with a valid declaration.

A metric that carries a construction error records nothing. Its `Inc`, `Add`, `Set` and `Observe` calls do nothing, and the first one logs a warning through `log/slog` that names the metric and the error. If you record through an invalid metric before registering it, the first dropped record still leaves a trace in your logs. The metric never reaches the output.

An invalid registry prefix is captured the same way. Every `Register` and `MustRegister` call on that registry reports it, because no metric under that prefix could have a valid name.

## Label values

Label values are yours to choose. Invalid UTF-8 never panics. The value is repaired with the Unicode replacement character U+FFFD, and a warning naming the metric is logged when the repaired series is first created. Later records onto that series do not warn again.

Repair has two costs. Distinct raw values that repair to the same string share one series. Every record carrying invalid UTF-8 takes the slower series-lookup path, even after the repaired series exists. Validate values that come from untrusted input before you record them. `Delete` repairs its values the same way, so calling it with the original raw values removes the series that recording created.

Each distinct label combination is a series that stays until `Delete` or `Reset`. A label whose values nothing in the program bounds is declared with `WithUnbounded`, which only a series charts read may carry. A label filled from a raw request path or a header grows memory and scrape size without bound, so map paths to a fixed set of routes first. When a labeled metric reaches 1,000 series, it logs one warning naming the metric. The threshold is fixed and cannot be configured. The warning does not cap or reject anything, and it fires once per metric even when the count drops and rises again.

## HTTP instrumentation

`RecordHTTP(c, h, d, labelVals...)` records one request. It increments the labeled counter `c` with `labelVals` and observes `d` in seconds on the histogram `h`, which should be declared `UnitSeconds`. Either metric may be nil to skip it. The values must match `c`'s label names in count and order. Name the counter's labels `method`, `route` and `code`, as client_golang's promhttp middleware (`InstrumentHandlerCounter`) names `method` and `code`, with `route` the template you registered. Leave the counter unseeded, with no `Add(0, ...)` at startup, because its status codes are not known in advance, unlike a counter of known failure sources. The README's [Patterns](../README.md#patterns) says how a rule on it copes.

`RecordHTTP` takes no request and no status, so your middleware captures both and calls it once the response is complete. Its parameters are this package's own types. Middleware that does not import measure cannot take `RecordHTTP` as its hook, so you write a small adapter that turns the middleware's per-request values into the label list.

With [httpserver](https://github.com/cplieger/go/tree/main/httpserver), adapt the access-log hook that `WithRecordRouteMetric` registers. Its `Method` is the request method collapsed to the nine standard methods or `other`, not the method written on the matched pattern. Its `Path` is that pattern's template or `unmatched`. The number of series stays bounded however much traffic arrives. The access logger records the status for you. Pass `m.Method`, `m.Path` and `strconv.Itoa(m.Status)` as the `method`, `route` and `code` values. measure has no dependencies, so it cannot ship a compiling example of that pairing. The hook's signature is in [httpserver's reference for `WithRecordRouteMetric`](https://pkg.go.dev/github.com/cplieger/go/httpserver/v2#WithRecordRouteMetric).

## Process metrics

Every registry serves these families without any registration. They are the families client_golang's default Go and process collectors serve, with the same names and types, so dashboards built for client_golang read them. Two are measure's own.

| Families | Source | Platforms |
| --- | --- | --- |
| The 21 `go_memstats_*` families other than `go_memstats_last_gc_time_seconds`, such as `go_memstats_heap_alloc_bytes` and `go_memstats_alloc_bytes_total` | `runtime/metrics`, mapped as client_golang maps them | all |
| `go_gc_gogc_percent`, `go_gc_gomemlimit_bytes` and `go_sched_gomaxprocs_threads`, the GOGC, GOMEMLIMIT and GOMAXPROCS settings | `runtime/metrics` | all |
| `go_gc_duration_seconds`, a summary with the 0, 0.25, 0.5, 0.75 and 1 quantiles of recent GC pauses, and `go_memstats_last_gc_time_seconds` | `runtime/debug.ReadGCStats` | all |
| `go_goroutines`, `go_threads` and `go_info`, whose `version` label is the Go release that built the program | `runtime` | all |
| `process_start_time_seconds` | `/proc/self/stat` and `/proc/stat` on Linux; elsewhere, and when that read fails, the time the package loaded | all |
| `process_gc_pause_seconds_total` and `process_uptime_seconds`, measure's own | `runtime/debug.ReadGCStats` and the start time | all |
| `process_cpu_seconds_total` | `/proc/self/stat` on Linux, `getrusage` on macOS | Linux, macOS |
| `process_virtual_memory_bytes` | `/proc/self/stat` | Linux |
| `process_resident_memory_bytes` | `/proc/self/status` | Linux |
| `process_open_fds` | `/proc/self/fd` on Linux, `/dev/fd` on macOS | Linux, macOS |
| `process_max_fds` and `process_virtual_memory_max_bytes`, the soft limits | `/proc/self/limits` on Linux, `getrlimit` on macOS | Linux, macOS |
| `process_network_receive_bytes_total` and `process_network_transmit_bytes_total`, the traffic of the process's network namespace, as client_golang counts it | `/proc/self/net/netstat` | Linux |

On macOS, resident and virtual memory need `task_info` through cgo, so measure serves neither, as client_golang does in a build without cgo. An unlimited soft limit reads `9.223372036854776e+18`, macOS's `RLIM_INFINITY`, where Linux reads `1.8446744073709552e+19`; client_golang serves the same values. Two values differ from client_golang's on macOS. The start time is the time the package loaded, moments after the program started, because the `kern.proc.pid` sysctl client_golang reads has no wrapper in the standard library. `process_open_fds` leaves out both descriptors that listing `/dev/fd` holds, the directory's own and the duplicate Go hands to `fdopendir`, so it reads one below client_golang's count, which leaves out only the first.

No read stops the world: measure never calls `runtime.ReadMemStats`. A `runtime/metrics` name that a Go release drops would read as 0, and a test fails on the Go update that drops one. Each Linux and macOS family appears only when its read succeeds, and the registry logs a warning when the set of failing reads changes and one line when they all recover. The families are sorted by name. Their names, and the summary's `_sum` and `_count` series, are reserved in every registry, so a metric of yours cannot take one.

## Output format

`Handler` answers with `Content-Type: text/plain; version=0.0.4; charset=utf-8` and `X-Content-Type-Options: nosniff`. The body is Prometheus text exposition format 0.0.4.

- Label values escape only backslash, double quote and newline, as `\\`, `\"` and `\n`. HELP text escapes only backslash and newline.
- Metric names, label names and bucket bounds are checked when the metric is built. A violation becomes a construction error, so an invalid metric never reaches the output.
- Label values and HELP text are always valid UTF-8. Invalid input is repaired with U+FFFD and logged once, for a label value when its series is created and for HELP text when the metric is built.
- Every histogram has a `+Inf` bucket equal to its `_count`.

One formatter renders every number. Whole values render as bare integers such as `42`. Other values use the shortest form that reads back to the same `float64`. Non-finite values render as `+Inf`, `-Inf` and `NaN`.

Recording, scraping and `GatherOf` are safe to run from many goroutines at once, and all are safe while metrics are being registered.
