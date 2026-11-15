# Events

This page is for a Go developer keeping a drawn chart current. It covers the four event kinds, the functions that build them and how a page applies them. [Sampling](sampling.md) covers the events a `Sampler` sends on its own.

## What an event is

A `FrameEvent` changes a frame the page already drew, without re-sending the frame. The app sends it on its own event stream, such as a server-sent events endpoint behind the same session as its API. `Key` names the frame the page drew, and `Kind` says how the event changes it. Each kind sets only its own fields.

| Kind | Built with | Fields | Changes |
| --- | --- | --- | --- |
| `append` | `AppendColumn(key, x, values)` | `x`, `values` | adds a column to a `SeriesFrame` |
| `append` | `AppendBin(key, bin, values)` | `x1`, `x2`, `values` | adds a bin to a `BinsFrame` |
| `replace` | `ReplaceColumn(key, values, open)` | `values`, `open` | changes a `SeriesFrame`'s last column in place |
| `replace` | `ReplaceBin(key, bin, values, open)` | `x1`, `x2`, `values`, `open` | changes a `BinsFrame`'s last bin in place, whose end may have moved |
| `reset` | `ResetAt(key, at)` | `at` | marks a restart at an instant |
| `set` | `SetFrame(key, &frame)` | the field named for the frame's type | replaces the whole frame |

- The generated `FrameEvent` type is a union by `kind`: each member holds only the fields its kind's rows list, and a decoder refuses another kind's field.
- `values` hold one value per series, in the frame's series order, `NaN` for a gap, which the event carries as `null`.
- An `x` and a bin's edges must be finite, and a bin's start must be below its end. A refused event returns a `*RefusalError`, as a refused frame does.
- The page checks that an appended `x` or bin comes after the frame's last; the server does not hold the drawn frame.

## How a page applies each kind

- `append`: the frame keeps its span. Its domain, or its x extent when it has none, slides to end at the new column, and the columns before the new start drop.
- `replace`: the last column or bin takes the new values, and `open` says whether it is still filling.
- `reset`: a line, area or sparkline breaks at `at` instead of joining across it, and a state history shows a gap there.
- `set`: the page draws the new frame. A frame drawn with a domain keeps its span, ending where the new frame's domain ends, since a `Sampler`'s `set` carries its whole ring.

## Which events a frame takes

- A frame the app builds takes `set` whenever its data changes, and `append` or `replace` when the app knows the new column, such as a counter of today's runs.
- A `ValueFrame`, a `CategoriesFrame` and every frame with no x axis take only `set`.
- A sampled frame takes what its `Sampler` sends.

```go
f, err := chartdata.NewCategoriesFrame(runs, results, series)
if err != nil {
	return err
}
e, err := chartdata.SetFrame("runs", &f)
if err != nil {
	return err
}
stream.Publish(e)
```

A `set` event shares the frame's slices, so leave the frame unchanged until the event is sent.
