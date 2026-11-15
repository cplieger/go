# Declarations

This page is for a Go developer declaring the series a service exports. It covers what every declaration states, the units and kinds, where a series is served, which labels may be unbounded, seeding, taking a series off the page, what registration refuses, and how other code reads a declaration.

Declare each series once, at startup, in the package that records it, and register it into the service's one registry. Series with no chart and charts with no `/metrics` series are both normal.

## What a declaration states

| Part | Where it comes from |
| --- | --- |
| Name and HELP text | The constructor's first two arguments. The registry prefixes the name. |
| Unit | The constructor's third argument, from the closed list below. A state set and an info series are always `UnitNone`. |
| Kind | The constructor: a counter, gauge, histogram, state set or info series. |
| Labels | The label names, in order. A collector family takes any number; an instrument takes at most 8. |
| Source | An instrument is recorded by your code. A collector's `Desc` is read by the collector's function at each scrape and each `GatherOf`. |
| Export | `WithExport`, which defaults to `ExportMetric`. |
| Unbounded labels | `WithUnbounded`, for a series only charts read. |
| Seeds | `WithSeed`, for a labeled instrument. |

`Declaration()` returns all of it as a `Declaration` value, whose slices are copies. Its `Name` is the registered name, prefix included, once the declaration is registered.

## Units

A unit is one of `UnitSeconds`, `UnitBytes`, `UnitRatio` (a share from 0 to 1), `UnitCount` and `UnitNone`. Values are in the base unit: seconds, never milliseconds.

- A counter of events is `UnitCount`, and so is a gauge of a count, such as `jobs_queued`, whose name carries no suffix.
- A rate has no unit of its own. A chart derives it from a `UnitCount` counter and its time axis.
- A 0/1 condition gauge, a state set and an info series are `UnitNone`.
- A value in a unit the list does not hold is `UnitNone`, such as a name ending in `_milliseconds` or `_kbps` that was released before this rule. A chart draws it as a plain number.

## Kinds

| Kind | Instrument | Collector family | On the page |
| --- | --- | --- | --- |
| Counter | `NewCounter`, `NewLabeledCounter` | `NewCounterDesc` | `counter` |
| Gauge | `NewGauge`, `NewLabeledGauge` | `NewGaugeDesc` | `gauge` |
| Histogram | `NewHistogram`, `NewLabeledHistogram` | `NewHistogramDesc` | `histogram` |
| State set | `NewStateSet` | `NewStateSetDesc` | `gauge` |
| Info | `NewInfo` | `NewInfoDesc` | `gauge` |

Text format 0.0.4 has no type for a state set or an info series, so both render as gauges, with the same lines a gauge set by hand writes.

A state set's last label is its state label, whose values are the states you declare. Each combination of the labels before it is one state set, such as one per provider for a `provider_health{provider,health}` family. `Set(state, labels...)` makes one state current: a scrape sees the old state or the new one at 1, never both and never neither. A state set with only the state label starts with every state at 0. A collector emits a state set with `Emitter.EmitState`.

An info series holds one label set at 1. `Set(labels...)` replaces it and `Delete()` takes it off the page. A collector emits an info family with `Emit(d, 1, labels...)`.

## Where a series is served

| Export | `/metrics` | `Gather` and `GatherOf` |
| --- | --- | --- |
| `ExportMetric`, the default | yes | yes |
| `ExportChartdata` | no | yes |
| `ExportBoth` | yes | yes |

`ExportChartdata` is for a series only charts read, through the `chartdata` module. Its name is still reserved in the registry, so it cannot collide with a series on the page.

A series that only charts built from your own data draw, such as runs counted in your store, needs no instrument and no collector. Declare it as a `Desc` with `ExportChartdata` and register the `Desc` on its own. It reserves its name and serves nothing: `Gather` and `GatherOf` list it with no samples, and you pass it to the `chartdata` constructors that build its frames. Registration refuses such a `Desc` with any other export, and one a collector holds.

```go
var runs = measure.NewCounterDesc("runs_total", "Runs by result and host.", measure.UnitCount,
	[]string{"result", "host"}, measure.WithExport(measure.ExportChartdata))

reg.MustRegister(runs)
```

## Labels

Every label is bounded unless you declare it unbounded. A bounded label's values come from a fixed list in the code (`op`, `reason`), from the service's configuration (hosts, profiles), or from the process (`version`). The library needs no list of the values.

`WithUnbounded(labels...)` declares labels whose values no part of the program bounds, such as an agent's or a thread's name. Only an `ExportChartdata` declaration may carry one: Prometheus keeps every label set as its own series, so an unbounded label on `/metrics` grows its storage without end.

On every labeled family, reaching 1,000 series logs one warning naming it, whatever its labels.

## Seeding

A labeled counter has no series until its first record, so an `increase()` rule cannot tell "no failures" from "no series". Seed the label sets known at start at 0:

```go
failures := measure.NewLabeledCounter("sync_failures_total", "Sync failures by source.", measure.UnitCount,
	[]string{"source"}, measure.WithSeed([]string{"plex"}, []string{"sonarr"}))
```

- Seed values from the configuration or from an enum, when the instrument is built.
- A value that appears while the program runs, such as a source the operator adds, is recorded by the code that adds it, with `Add(0, ...)` or `Set`, or with its first real value. The code that removes it calls `Delete`.
- Leave the HTTP request counter unseeded: its route and status pairs are not known in advance. A rule that must see a single request also counts the series new in its window.
- A state set's seeds leave out the state label and start every state at 0.
- A collector's families are never seeded. They serve what the function reads at each scrape, so a series absent by design stays absent for `absent()` and `unless`.

`WithSeed` works on `NewLabeledCounter`, `NewLabeledGauge`, `NewLabeledHistogram` and `NewStateSet`.

## Taking a series off the page

`Delete(labels...)` removes one label set. A `LabeledGauge` with no labels holds at most one series, so `Delete()` takes it off the page until the next `Set`, for a value that is unknown for a while.

Never `Reset` a family and then set its survivors again: a scrape between the two sees an empty family, and every alert on it resolves.

## What registration refuses

A constructor never panics on a bad declaration. It captures the error, and `Register` returns it. Registration refuses only a declaration that contradicts itself or that cannot be served safely:

| Declaration | Error |
| --- | --- |
| No unit, or a unit off the list | `declares no unit`, `which is not seconds, bytes, ratio, count or none` |
| A name ending in `_seconds`, `_bytes` or `_ratio`, before any `_total`, with another unit | `ends in "_seconds" but declares unit "count"` |
| A name ending in `_timestamp_seconds` with a unit other than seconds | `a timestamp is in seconds` |
| `_total` on anything but a counter | `ends in "_total" but declares kind "gauge"` |
| An unbounded label on a series served on `/metrics` | `only an ExportChartdata series may carry an unbounded label` |
| An unbounded label the declaration does not have | `which is not one of its labels` |
| An export off the list | `which is not metric, chartdata or both` |
| `WithBuckets` on anything but a histogram instrument | `not a histogram instrument` |
| `WithSeed` on an unlabeled instrument, an info series or a collector family, or with the wrong number of values | `which takes no seed`, `gives 1 label values, want 2` |
| A state set with no labels, no states, an empty state, a repeated state or a state that is not valid UTF-8 | `has no state label`, `declares no states`, `declares an empty state`, `twice`, `not valid UTF-8` |

A name with no suffix contradicts nothing: `jobs_queued` may be `UnitCount`, and a counter may leave out `_total`. A gauge whose name ends in `_count` or `_info` is a valid Prometheus name and is accepted. Every series the cplieger apps export under measure v1 passes these rules, which a test checks on every release.

The checks of v1 stay: an invalid metric or label name, a reserved or repeated label name, more than 8 labels on an instrument, bad histogram buckets and a name another family holds.

## Reading declarations

- `Declaration()` on any instrument or `Desc` returns its metadata. Each implements the sealed `Declared` interface.
- `Registry.Gather(ctx)` reads every family, `ExportChartdata` ones included, and runs every collector.
- `Registry.GatherOf(ctx, decls...)` reads only the named declarations, one `Family` each in the order given. It runs each collector they need once, and no other, and reads no Go or process family. It refuses a declaration the registry does not hold.
- `Find(fams, name)` picks a family by name, and `Family.Sample(labels...)` picks a series by its label values in declared order, so a test reads a value without parsing the page.

A collector's function must be safe to call concurrently: a scrape and a `GatherOf` can overlap.
