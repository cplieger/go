# Sampling

This page is for a Go developer charting the recent history of a series the service keeps nowhere else, such as a queue's depth or a request latency. It covers the `Sampler`, what it reads from each kind of series, its reads and the events it sends. [Frames](frames.md) covers building frames from your own data, and [Events](events.md) the event kinds.

## When to sample

Sample a series only when the app holds its history nowhere else. When the history is in the app's own store, build the frame from a store query with a constructor instead: a sampler's rings start empty at every restart. Prometheus keeps the operator's history of every exported series in any case.

## Setting up a sampler

`NewSampler(cfg, tracks...)` builds a sampler of tracks, and `Run(ctx)` runs it until `ctx` ends.

```go
reg := measure.NewRegistry("myapp")
latency := measure.NewLabeledHistogram("request_duration_seconds", "Request latency", measure.UnitSeconds,
	[]string{"route", "code"})
depth := measure.NewGauge("queue_depth", "Items waiting", measure.UnitCount)
reg.MustRegister(latency, depth)

p95 := chartdata.Sampled(latency, chartdata.AsQuantile(0.95), chartdata.Key("p95"))
queue := chartdata.Sampled(depth)
s, err := chartdata.NewSampler(chartdata.Sampling{
	Registry: reg,
	Every:    time.Minute,
	Keep:     24 * time.Hour,
	Emit:     stream.Publish, // your event stream's send, which must not block
}, p95, queue)
if err != nil {
	return err
}
go func() {
	if err := s.Run(ctx); err != nil {
		logger.Error("sampler stopped", "err", err)
	}
}()
```

`Sampling` holds:

| Field | Meaning |
| --- | --- |
| `Registry` | The registry every tracked declaration is registered in. Required. |
| `Every` | The step. Columns are taken on multiples of `Every` since the Unix epoch, so two processes with the same step agree on the edges. At least `MinEvery`, 1 second. |
| `Keep` | How long a column is held, at most `MaxPoints` (10,000) steps, rounded up to a whole step. At least `Every`. |
| `Emit` | Optional. Receives each step's events, on the sampler's goroutine. A column whose edge passes while `Emit` runs is a gap, and so is a counter's or histogram's next column, so hand the event to your stream without waiting. |
| `Logger` | Optional. Takes the warnings below, written on the sampler's goroutine after each step, so a slow handler never holds up `Series` or `Bins`; nil means `slog.Default()`. |

`NewSampler` returns an error for a configuration outside those bounds and for a track that does not fit its declaration: a nil track or option, a track given twice, `By` naming no label of the declaration, a histogram with neither `AsQuantile` nor `AsBins`, either of them on anything else, a quantile outside 0 to 1, an `AsBins` window outside `Every` to `Keep`, and an `AsBins` track of a histogram with a bucket bound beyond ±`MaxMagnitude`, since no `BinsFrame` carries that edge.

`Run` blocks until `ctx` ends and then returns nil. It returns an error, and stops, when a tracked declaration is not registered in the registry, and when it is called a second time.

## What each step reads

Each step reads the tracked declarations only, through `measure`'s `Registry.GatherOf`. A collector runs once per step for all the declarations it serves, and no other collector runs, and the Go and process families are not read. A collector's function must therefore be safe to call while a scrape runs it too.

`Sampled(d, opts...)` tracks one declaration, read according to its kind:

| Kind | Each column holds |
| --- | --- |
| Gauge, state set, info | its value |
| Counter | its increase since the previous column, as PromQL's `increase()` takes it: a fall, such as a `Delete` and a new record, is a reset, and the new value is the increase. A label set adds nothing to the column it first appears in, since it has no earlier value, so the process's first column is a gap |
| Histogram | the increase of each bucket since the previous column, read as `AsQuantile(q)` or `AsBins(window)` |

A reading that is not finite, or beyond ±`MaxMagnitude`, is no reading, since no frame can carry it. It adds nothing to its column, which is a gap when no other label set of its series gives a value, and it changes nothing the track holds: a counter's next usable reading takes its increase from the last usable one, unless edges were missed in between ([Bounds](#bounds)), and the series stays in place while its ring holds a value. A label set takes a place, and a series comes into being, only with its first point, so one with nothing but unusable readings holds no place and counts toward no cap. `measure` drops a NaN counter sample before the sampler reads it, so that label set is not read at that step, and its next reading is a first appearance.

Two options narrow a track, and one names it:

- `Where(keep)` keeps the label sets `keep` returns true for, such as status codes from 500 to 599. It runs on the sampler's goroutine.
- `By(label)` sums over every other label, as PromQL's `sum by` does, keeping a histogram's buckets apart. Each value of `label` is one series of the frame, keyed by that value. Without `By`, every label set kept is summed into one series keyed by the declaration's registered name.
- `Key(key)` names the track in its events, so a page drawing two tracks of one declaration, such as its p50 and its p95, can tell their events apart. Without `Key`, the events carry the declaration's registered name.

A track reads every label set `Where` keeps. A label set takes one of `MaxLabelSets` (1,000) places with its first point, a value, an increase, a bin or a quantile, while fewer than 1,000 are held. Each step settles places in this order: a label set holding a place keeps it with its point, then new label sets of the series already held take the places left, in the order `Gather` returns them, then new series take theirs, in key order, while the track holds fewer than its most series; a label set of a series past the most takes no place. A step whose sum for a series is a gap, such as a quantile or a `By` sum beyond ±`MaxMagnitude`, gives that series' ring no point, so none of its label sets takes or renews a place at that step, and the places they would take go to the other series in the same order. A series whose sum is a gap only for want of a place waits until the series whose sums are final have given theirs up, and then, in key order, the first of the rest gives its places up. A label set gives the place up once its series' ring holds none of its points, however often it is read: a reading, or a baseline, alone holds no place. A track holds at most `MaxLabelSets` series, and a value or quantile track at most `MaxValues / (Keep / Every)`, so its whole ring fits one frame: 100 series for 10,000 steps. A label set or series that is new past either cap is dropped, never one already held, and the first drop logs one warning.

A counter's or histogram's increase is taken from its label set's baseline, its last usable reading. The track keeps a baseline for each label set it read last step, with a place or not, up to `MaxBaselines` (4,000). Past it, the baselines of label sets holding no place are forgotten, the least recently changed first; a label set holding a place never loses its baseline, since places are fewer. A forgotten label set's next reading is a first appearance: its column is a gap, and the reading becomes its baseline. The first time a track forgets a baseline, it logs the same one warning as a drop. An idle histogram therefore keeps its baseline without a place, and its next observation counts in the column it is made in.

### Histograms

`AsQuantile(q)` estimates the q-quantile of each column's bucket increases as Prometheus's [`histogram_quantile`](https://prometheus.io/docs/prometheus/latest/querying/functions/#histogram_quantile) does: linearly within the bucket the rank falls in, the lowest bucket from 0 when its bound is above 0, and the highest finite bound for a rank in the `+Inf` bucket. A column with no observations, or a histogram with no finite bucket, is a gap.

For either form, a step with no observation gives no point. A label set read with no observation yet holds no place, and its baseline of zeros makes its first observations count in the column they are made in. An `AsBins` series with no observation over its window is not in the frame.

`AsBins(window)` reads the observations in each bucket over the last `window` as a `BinsFrame` on a value axis. The first bin starts at 0 when its bound is above 0. A bucket with no finite lower edge, the `+Inf` bucket or the lowest under a bound of 0 or below, is no bin, since a frame refuses a non-finite edge: declare a higher top bound when observations pass yours. Use `DurationEdges` for the bounds of a histogram of durations, so its bins land on the page's duration axis.

## Reading a frame

- `Series(track, last)` reads any track but an `AsBins` one as a `SeriesFrame` of its columns in the last span, from `Every` to `Keep`. Its domain is the span, ending at the newest column, so a page drawing 6 hours from a 24-hour ring reads `Series(track, 6*time.Hour)`. It holds every series the ring holds, sorted by key, a series with no value in the span as gaps, in the order the append events' values take.
- `Bins(track)` reads an `AsBins` track as the `BinsFrame` of its window.

Both are safe to call while the sampler runs. When the process's first column falls inside a `Series` span, the frame carries it in `resets`, so the page can mark the restart.

## Bounds

- Each ring holds `Keep / Every` columns, and drops the oldest when full. An `AsBins` track's rings hold its window's columns only.
- A series whose ring holds no value at all is removed, however often it is read. A label set holds a place only while its series' ring holds one of its points, so per track at most `MaxLabelSets` (1,000) label sets hold places and at most `MaxBaselines` (4,000) keep a baseline.
- A step that runs past the next edges, such as a slow `Emit`, leaves a gap column at each edge it missed, and a counter's or histogram's first column after them is a gap, since its increase would cover several steps. A wait that runs past them, such as in a paused process, does the same: the step reads at the latest edge it passed, so its reading is stamped when it was taken, every edge it missed is a gap, and a counter's or histogram's increase across the pause is a gap too, since when inside the pause it happened is unknown.
- The edges only move forward: when the wall clock steps back, the next column waits for the edge after the newest.
- A restart starts every ring empty. The sampler keeps no store and never downsamples: a frame holds every column of its span.

## The events a sampler sends

When `Emit` is set, each step sends, for each track in the order given to `NewSampler`:

- at the process's first column, a `reset` at that column;
- for a gauge, counter or `AsQuantile` track, an `append` of each new column, the gap columns of missed edges included, with a value per series in key order, or, when the set of series changed, a `set` of the whole ring's `SeriesFrame`;
- for an `AsBins` track, a `set` of its window's `BinsFrame`.

`NewSampler` admits only tracks whose frames the constructors take, so every `set` is sent; a frame refused all the same would be a bug, and its track logs one warning. [Events](events.md) says how the page applies each kind.
