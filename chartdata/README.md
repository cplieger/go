# chartdata

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/chartdata.svg)](https://pkg.go.dev/github.com/cplieger/go/chartdata) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=chartdata%2Fgo.mod)](https://github.com/cplieger/go/blob/main/chartdata/go.mod)

chartdata turns your Go service's series into the data its charts draw: typed frames that the `@cplieger/ui` chart elements take as they are, and live events that change a frame already drawn.

It builds on [`measure`](../measure) and otherwise uses only the standard library. It is licensed under Apache-2.0.

## Why use it

chartdata is built for a service that declares its series once with `measure` and draws them on its own pages.

- Each constructor takes the `measure` declaration it draws, so a frame's name and unit match what the registry holds for the same series once the declaration is registered.
- Twelve frame types cover every chart element: series, bins, categories, boxes, points, grids, calendars, trees, flows, event strips, state histories and meters.
- A constructor refuses a malformed frame, such as unequal lengths, an unsorted x or an infinite value, and one past the limits the chart draws within, with an error naming the rule and the field.
- A `Sampler` holds the recent history of the series you keep nowhere else, in bounded rings, and estimates a histogram's quantiles as Prometheus's `histogram_quantile` does.
- Events append a column, replace the last one, mark a restart or replace a whole frame, so a page stays current without reloading.
- A count or a frame built from a read that was not whole carries the read's state, the time of its last whole read and the app's reason, so the page shows the last whole value marked "Partial, as of 14:05" instead of 0.
- The TypeScript types and a golden fixture of every frame, event and refusal are generated from the Go types and committed, so a chart library tests against the exact wire.

Use `measure` alone if the service draws no charts: chartdata adds nothing to `/metrics`.

## Install

```sh
go get github.com/cplieger/go/chartdata@latest
```

## Usage

Build a frame in your API's handler from whatever your store returns, and serve it as JSON:

```go
var (
	reg  = measure.NewRegistry("myapp")
	runs = measure.NewCounterDesc("runs_total", "Runs by result and host.", measure.UnitCount,
		[]string{"result", "host"}, measure.WithExport(measure.ExportChartdata))
)

func main() {
	// Reserves myapp_runs_total, the name the frame carries; /metrics serves nothing for it.
	reg.MustRegister(runs)
	// ...
}

func runsHandler(w http.ResponseWriter, r *http.Request) {
	// Your store's query, as a value per result for each host; NaN is no data.
	f, err := chartdata.NewCategoriesFrame(runs, []string{"ok", "failed"}, []chartdata.Series{
		{Key: "host-a", Values: []float64{30, 2}},
		{Key: "host-b", Values: []float64{28, math.NaN()}},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f)
}
```

The frame encodes as:

```json
{"type":"categories","name":"myapp_runs_total","unit":"count","names":["ok","failed"],"series":[{"key":"host-a","values":[30,2]},{"key":"host-b","values":[28,null]}]}
```

A `NaN` value is a gap, sent as `null`. Instants are Unix milliseconds and quantities are in the declaration's base unit.

To chart a series you keep nowhere else, sample it from the registry:

```go
p95 := chartdata.Sampled(latency, chartdata.AsQuantile(0.95), chartdata.Key("p95"))
s, err := chartdata.NewSampler(chartdata.Sampling{
	Registry: reg, Every: time.Minute, Keep: 24 * time.Hour, Emit: stream.Publish,
}, p95)
if err != nil {
	log.Fatal(err)
}
go func() {
	if err := s.Run(ctx); err != nil {
		log.Print(err)
	}
}()

// In the page's handler: the last 6 hours, kept current by the events Emit gets.
f, err := s.Series(p95, 6*time.Hour)
```

The package examples on pkg.go.dev show more cases, and `go test` keeps them true.

## API

- Frames: `SeriesFrame`, `BinsFrame`, `CategoriesFrame`, `BoxesFrame`, `PointsFrame`, `GridFrame`, `CalendarFrame`, `TreeFrame`, `FlowFrame`, `EventsFrame`, `StatesFrame` and `ValueFrame`, their parts (`FrameSeries`, `FrameRef`, `FrameNote` and the rest), and `FrameType` and `FrameAxis` with their constants.
- Constructors: `NewSeriesFrame`, `NewBinsFrame`, `NewCategoriesFrame`, `NewBoxesFrame`, `NewPointsFrame`, `NewGridFrame`, `NewCalendarFrame`, `NewTreeFrame`, `NewGroupedTreeFrame`, `NewFlowFrame`, `NewEventsFrame`, `NewStatesFrame` and `NewValueFrame`, with their inputs (`Series`, `Bin`, `Boxes`, `Measures`, `Points`, `Grid`, `TreePart`, `TreeGroup`, `Flow`, `FlowLink`, `Times`, `State`).
- Options: `WithRefs`, `WithNotes`, `WithDomain` with `Range`, `WithOpen`, `WithResets` and `WithRead`.
- Read state: `Count` and `CountOf`, an API row's figure for a count read elsewhere, `Read` and `ReadOf`, a read beside any other figure and a frame's `read`, and `MaxReasonBytes`.
- Refusals: `RefusalError` and `Rule` with its constants.
- Limits: `MaxValues`, `MaxRows`, `MaxSubRows`, `MaxMagnitude`, `MaxCalendarDays`, `MinEvery`, `MaxPoints`, `MaxLabelSets` and `MaxBaselines`, and the duration ladder `DurationEdges`.
- Quantiles: `Quantile`, the estimate an `AsQuantile` track takes, from a histogram's edges and per-bucket counts.
- Sampling: `NewSampler` with `Sampling`, `(*Sampler).Run`, `Series` and `Bins`, and `Sampled` with its options `Where`, `By`, `Key`, `AsQuantile` and `AsBins`.
- Events: `FrameEvent` and `FrameEventKind` with its constants, `AppendColumn`, `AppendBin`, `ReplaceColumn`, `ReplaceBin`, `ResetAt` and `SetFrame`.
- Code generation: `WireTypes`, every wire type, `WireEnums`, every string enum on the wire, and `WireUnions` with `WireUnion`, the tagged wire structs, for an app's own wiregen run.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/chartdata).

## Unsupported by design

chartdata leaves these out on purpose. The [non-goals](docs/non-goals.md) page gives the reason for each.

- A store of past values. A sampler's rings are bounded and start empty at each restart.
- Downsampling. A frame holds every point its window holds, within the limits.
- Presentation: marks, colours, words and sizes are the page's.
- Alert rules and thresholds.
- Any way into the `measure` registry beyond its reading API.

## Documentation

- [Frames](docs/frames.md) covers the twelve frame types, their constructors and options, the wire format, every refusal, the limits table, the duration ladder and `Quantile`.
- [Sampling](docs/sampling.md) covers the `Sampler`: what it reads from each kind of series, its bounds, its reads and the events it sends.
- [Read state](docs/read-state.md) covers `Count`, a read beside a list's total, a frame's `read` and what rule `read` refuses.
- [Events](docs/events.md) covers the four event kinds and how a page applies them.
- [The generated contract](docs/contract.md) covers the TypeScript types, the golden fixtures and their paths, the drift check and generating an app's own decoders.
- [Non-goals](docs/non-goals.md) lists what the library leaves out and why.

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
