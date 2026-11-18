package chartdata_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

// A series only charts draw is a Desc exported to chartdata alone, which
// /metrics never serves. Registering it reserves its name, which the frame
// then carries. This is the README's example.
func ExampleNewCategoriesFrame() {
	reg := measure.NewRegistry("myapp")
	runs := measure.NewCounterDesc("runs_total", "Runs by result and host.", measure.UnitCount,
		[]string{"result", "host"}, measure.WithExport(measure.ExportChartdata))
	reg.MustRegister(runs)

	f, err := chartdata.NewCategoriesFrame(runs, []string{"ok", "failed"}, []chartdata.Series{
		{Key: "host-a", Values: []float64{30, 2}},
		{Key: "host-b", Values: []float64{28, math.NaN()}},
	})
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(f)
	fmt.Println(string(b))
	// Output:
	// {"type":"categories","name":"myapp_runs_total","unit":"count","names":["ok","failed"],"series":[{"key":"host-a","values":[30,2]},{"key":"host-b","values":[28,null]}]}
}

func ExampleNewSeriesFrame() {
	wait := measure.NewGauge("queue_wait_seconds", "Oldest item's wait", measure.UnitSeconds)
	minute := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	f, err := chartdata.NewSeriesFrame(wait,
		[]float64{float64(minute.UnixMilli()), float64(minute.Add(time.Minute).UnixMilli())},
		[]chartdata.Series{{Key: "queue_wait_seconds", Values: []float64{0.4, 1.5}}},
		chartdata.WithOpen(true), chartdata.WithRefs(chartdata.FrameRef{Key: "target", Value: 1}))
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(f)
	fmt.Println(string(b))
	// Output:
	// {"type":"series","name":"queue_wait_seconds","unit":"seconds","x":[1772366400000,1772366460000],"series":[{"key":"queue_wait_seconds","values":[0.4,1.5]}],"refs":[{"key":"target","value":1}],"open":true}
}

func ExampleRefusalError() {
	depth := measure.NewGauge("queue_depth", "Items waiting", measure.UnitCount)

	_, err := chartdata.NewSeriesFrame(depth, []float64{2, 1}, nil)
	if refusal, ok := errors.AsType[*chartdata.RefusalError](err); ok {
		fmt.Println(refusal.Rule, refusal.Field)
	}
	// Output:
	// order x[1]
}

func ExampleSetFrame() {
	coverage := measure.NewGauge("coverage_ratio", "Share of items covered", measure.UnitRatio)

	f, err := chartdata.NewValueFrame(coverage, []chartdata.FrameMeter{{Key: "en", Value: 0.92, Min: 0, Max: 1}})
	if err != nil {
		log.Fatal(err)
	}
	e, err := chartdata.SetFrame("coverage", &f)
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(e)
	fmt.Println(string(b))
	// Output:
	// {"value":{"type":"value","name":"coverage_ratio","unit":"ratio","meters":[{"key":"en","value":0.92,"min":0,"max":1}]},"key":"coverage","kind":"set"}
}

// A built frame's p95 from the counts the app stores for each bucket, the
// last above every edge, agrees with an AsQuantile track's.
func ExampleQuantile() {
	edges := []float64{0.5, 1, 2, 5}
	counts := []float64{40, 30, 20, 8, 2}

	p95, ok := chartdata.Quantile(edges, counts, 0.95)
	fmt.Println(p95, ok)
	// Output:
	// 3.875 true
}

// A Sampler holds the last 6 hours of a latency's p95, one column a
// minute, and sends each step's event to the page.
func ExampleNewSampler() {
	reg := measure.NewRegistry("myapp")
	latency := measure.NewLabeledHistogram("request_duration_seconds", "Request latency", measure.UnitSeconds,
		[]string{"route"})
	reg.MustRegister(latency)

	p95 := chartdata.Sampled(latency, chartdata.AsQuantile(0.95), chartdata.Key("p95"))
	s, err := chartdata.NewSampler(chartdata.Sampling{
		Registry: reg,
		Every:    time.Minute,
		Keep:     6 * time.Hour,
		Emit:     func(e chartdata.FrameEvent) { /* publish e on the app's event stream */ },
	}, p95)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := s.Run(ctx); err != nil {
			log.Print(err)
		}
	}()

	// In the page's handler, read the frame the page draws first.
	f, err := s.Series(p95, time.Hour)
	if err != nil {
		log.Print(err)
	}
	_ = f
}

// A read cut short after a whole one keeps the whole read's value and time,
// so the page shows 412, marked partial as of 14:05, and never 0.
func ExampleCountOf() {
	whole := measure.Whole(time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC))
	open := 412.0
	c, err := chartdata.CountOf(&open, whole.Cut(measure.ReadPartial, "rate_limited"))
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(c)
	fmt.Println(string(b))
	// Output:
	// {"value":412,"state":"partial","as_of":"2026-11-18T14:05:00Z","reason":"rate_limited"}
}

func ExampleReadOf() {
	whole := measure.Whole(time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC))
	for _, r := range []measure.Read{whole, whole.Cut(measure.ReadBlind, "root_unavailable")} {
		state, err := chartdata.ReadOf(r)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := json.Marshal(state)
		fmt.Println(string(b))
	}
	// Output:
	// {"state":"complete","as_of":"2026-11-18T14:05:00Z"}
	// {"state":"blind","as_of":"2026-11-18T14:05:00Z","reason":"root_unavailable"}
}
