# Frames

This page is for a Go developer serving chart data from a service. It covers the twelve frame types, their constructors and options, the wire format, every refusal and the limits table. [Sampling](sampling.md) covers frames read from a `Sampler`, and [Events](events.md) covers changing a frame already drawn.

## What a frame is

A frame is a plain struct holding the data one chart element draws, with no presentation: no mark, colour, words or height. The page chooses those. Every frame carries:

- `type`, which names its frame type, so the chart can check that its mark fits the frame and a `set` event can carry any frame;
- `name` and, on every frame of quantities, `unit`, taken from the `measure` declaration the constructor is given, so they match what the registry holds for the same series once the declaration is registered (a `PointsFrame` has `units`, one per axis; an `EventsFrame` and a `StatesFrame` hold no quantities and no unit);
- optionally `refs`, reference values the server computed, such as a median, a p95 or a threshold, drawn as rules;
- optionally `notes`, events at a place on the x axis, such as a deploy, drawn as text marks.

A `key` is always data: a label value, a reference name or the declaration's name. The words the page shows for a key are the page's.

## The wire format

Frames encode with `encoding/json` as they are.

- An instant is a Unix time in milliseconds, what JavaScript's `Date` takes.
- A quantity is in the declaration's base unit: seconds, bytes, a ratio from 0 to 1, a count, or a plain number for `none`.
- A gap is `null`. The constructors take `NaN` for a gap in a `[]float64` and write it as a `nil` element of the frame's `[]*float64`.
- An optional field is left out when it is unset.

## The frame types

Each constructor takes the declaration the frame draws first: any `measure` instrument or `*measure.Desc`. Register it first, so the frame carries the registered name, the registry's prefix included; a declaration not yet registered gives its declared name. For a series that only charts draw, declare a `Desc` with `measure.WithExport(measure.ExportChartdata)`; [measure's declarations page](../../measure/docs/declarations.md) shows how to register one on its own.

| Frame | Constructor | Holds | Drawn as |
| --- | --- | --- | --- |
| `SeriesFrame` | `NewSeriesFrame(d, x, series, opts...)` | `x`, instants or plain numbers, and `series`, each a value per x | a line, an area or a sparkline |
| `BinsFrame` | `NewBinsFrame(d, axis, bins, series, opts...)` | `x1` and `x2`, each bin's edges along a time or value `axis`, and `series`, each a value per bin | rects or a histogram |
| `CategoriesFrame` | `NewCategoriesFrame(d, names, series, opts...)` | `names` and `series`, each a value per name | bars, points, a slope (two names), a pie or symbols (one series), a share bar or ranked bars |
| `BoxesFrame` | `NewBoxesFrame(d, names, series, opts...)` | `names` and `series`, each with `low`, `q1`, `median`, `q3` and `high` per name, and optional `outliers` | box plots |
| `PointsFrame` | `NewPointsFrame(m, series, opts...)` | `series`, each a group of named points with `x`, `y` and an optional size `r`, and `units`, one per measure | a scatter or bubble chart |
| `GridFrame` | `NewGridFrame(d, grid, opts...)` | an `x` and a `y` axis, each of `names` or bin `edges`, and `values`, a row per y entry | a cell grid or a 2D histogram |
| `CalendarFrame` | `NewCalendarFrame(d, zone, start, values, opts...)` | `zone`, an IANA zone name, `start`, the first day's midnight in it, and `values`, one per calendar day | a calendar heatmap |
| `TreeFrame` | `NewTreeFrame(d, parts, opts...)` or `NewGroupedTreeFrame(d, groups, opts...)` | one level of `parts`, or two levels of `groups` and their `children` | a treemap |
| `FlowFrame` | `NewFlowFrame(d, flow, opts...)` | `links` from node to node, with optional `nodes` for their order and `stages` for an alluvial diagram | a Sankey or alluvial diagram |
| `EventsFrame` | `NewEventsFrame(d, rows, opts...)` | `rows`, each a key and the instants of its events | a dot timeline strip |
| `StatesFrame` | `NewStatesFrame(d, states, opts...)` | `keys` and `at`, a state per observation, a `null` key for a gap | a state history |
| `ValueFrame` | `NewValueFrame(d, meters, opts...)` | `meters`, each a value from `min` to `max` with optional `low`, `high` and `optimum` | a meter, or several sharing one bar |

A few shapes need more than the table says:

- `NewPointsFrame` takes a `Measures{X, Y, R}`, one declaration per axis and `R` for the point's size, or nil for none. The frame's name is `Y`'s.
- A `TreeFrame` holds `parts` or `groups`, never both. A part with no data sends `value: null`; a group sends no value, since its value is its children's sum.
- A `FlowFrame` without stages is a Sankey diagram, and its links may form no loop, since a node in a loop has no column. With stages it is an alluvial diagram, and each link names the stage it leaves; with exactly two stages a link may leave it empty, meaning the first.
- A `StatesFrame` of a `measure` state set takes only the states the state set declares.
- A `CalendarFrame`'s day is the calendar day in its zone, 23 or 25 hours long across a daylight-saving change. `zone` must be a name `time.LoadLocation` resolves, other than `""` and `"Local"`, which name no zone, and `start` must be a midnight in that zone, whatever `start`'s own location.

## Options

Options set the optional parts of a frame. An option the frame's type cannot take is refused with the `option` rule, and the last of two same options wins.

| Option | Frames | Sets |
| --- | --- | --- |
| `WithRefs(refs...)` | every frame | the reference values, each finite |
| `WithNotes(notes...)` | every frame | the notes, each at a finite place |
| `WithDomain(Range{From, To})` | `SeriesFrame`, `BinsFrame` | the span the axis covers when it is wider than the data, such as a window whose first hours hold no samples |
| `WithOpen(open)` | `SeriesFrame`, `BinsFrame`, `CalendarFrame` | that the last column, bin or day is still filling |
| `WithResets(at...)` | `SeriesFrame`, `BinsFrame` | the instants a restart broke the series at, strictly increasing; a line breaks there instead of joining across them |

## Refusals

A constructor returns a `*RefusalError` for a frame it refuses, and never a partial frame. Its `Rule` says which rule the frame breaks, `Field` the path of the field at fault, such as `series[2].values[7]`, and `Reason` what is wrong. Match it with `errors.As`. A frame past `MaxValues` is refused before any of its values is read.

| Rule | Refuses |
| --- | --- |
| `declaration` | a nil declaration, a typed nil one, and one with no name |
| `option` | a nil option, and an option the frame's type cannot take |
| `length` | two fields that must be as long as each other and are not, such as a series with a value count other than the x count |
| `order` | an x, an instant or an edge out of order, or repeated where it must strictly increase; a domain that does not run from a low to a high; a box whose five numbers are out of order; a meter whose min is not below its max, or whose low is above its high |
| `bin` | a bin whose start is not below its end |
| `finite` | an infinite value, which JSON cannot carry, and `NaN` where no gap is allowed, such as an x or a ref |
| `magnitude` | a value beyond ±`MaxMagnitude` |
| `negative` | a negative tree part, flow link or point size, each a part of a whole or an area |
| `duplicate` | a name, series key, row key, meter key or tree key given twice, or two flow links joining the same nodes at the same stage |
| `empty` | an empty name, node or required list, such as a tree group with no parts, or a nil `Grid` |
| `shape` | parts that do not fit together, such as a box with some of its five numbers, a grid axis with both names and edges, a flow link naming a node `nodes` does not list, a flow loop, a state its state set does not declare, a meter value outside its range, or a calendar start that is not a midnight or not in an IANA zone |
| `limit` | a frame past one of the limits below |

The rules are stable. The refusal fixtures carry them, so the chart elements refuse the same frames by the same rule. `declaration` and `option` have no fixture, since no frame on the wire can break them.

## Limits

`chartdata` owns the ceilings the chart elements draw within, and every constructor enforces them. A refusal that depends on how the page presents a frame, such as a log axis's thinnest bin or a plot's size in pixels, is left to the element, the only side that knows the scale.

| Limit | Value | Bounds |
| --- | --- | --- |
| `MaxValues` | 1,000,000 | the values in one frame: samples, bars, dots, cells, tree parts and groups, flow links and nodes, refs and notes together, each series counted as one value at least |
| `MaxRows` | 1,000 | a `CategoriesFrame`'s names and its series, a `BoxesFrame`'s names, a `GridFrame`'s y entries and an `EventsFrame`'s rows |
| `MaxSubRows` | 2,500 | a `BoxesFrame`'s names times its series, one box each |
| `MaxCalendarDays` | 6,994 | a `CalendarFrame`'s days: the most that fit in `MaxRows` weeks at any week start, since the page picks it and a first day late in its week starts a partial week |
| `MaxMagnitude` | 1e300 | the magnitude of any value, so the chart's sums and scales stay finite |
| `MinEvery` | 1 s | a `Sampler`'s shortest step |
| `MaxPoints` | 10,000 | the points one sampled ring holds, `Keep / Every` |
| `MaxLabelSets` | 1,000 | the label sets a `Sampler` reads per track, after `Where`, and the series it holds per track; a value or quantile track holds at most `MaxValues / (Keep / Every)` series, so its whole ring fits one frame |

## The duration ladder

`DurationEdges` returns the bin edges, in seconds, that a duration axis and a histogram of waits share: 0, 1, 2, 5, 10 and 30 seconds, 1, 2, 5, 10 and 30 minutes, 1, 2, 6 and 12 hours, then 1, 3, 7, 14, 30 and 90 days. Pass a prefix of it to `measure.WithBuckets` and bin stored durations on the same edges, so sampled and built `BinsFrame`s land on the edges the page's duration axis uses:

```go
edges := chartdata.DurationEdges()
wait := measure.NewHistogram("job_wait_seconds", "Time a job waits to start", measure.UnitSeconds,
	measure.WithBuckets(edges[1:12])) // 1 s to 1 h; measure's buckets are the upper bounds
```
