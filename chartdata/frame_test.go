package chartdata_test

import (
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

func TestFramesTakeTheirNameAndUnitFromTheDeclaration(t *testing.T) {
	t.Parallel()
	r := measure.NewRegistry("app")
	depth := gauge("queue_depth", measure.UnitCount)
	r.MustRegister(depth)
	f, err := chartdata.NewSeriesFrame(depth, []float64{1000}, []chartdata.Series{{Key: "queue_depth", Values: []float64{3}}})
	if err != nil {
		t.Fatalf("NewSeriesFrame: %v", err)
	}
	if f.Name != "app_queue_depth" || f.Unit != "count" || f.Type != chartdata.TypeSeries {
		t.Errorf("frame = %q %q %q, want series app_queue_depth in count, the registered name", f.Type, f.Name, f.Unit)
	}
}

func TestSeriesFrameOnTheWire(t *testing.T) {
	t.Parallel()
	start := time.UnixMilli(1_700_000_000_000)
	f, err := chartdata.NewSeriesFrame(gauge("wait_seconds", measure.UnitSeconds),
		[]float64{1_700_000_000_000, 1_700_000_060_000},
		[]chartdata.Series{{Key: "p95", Values: []float64{0.25, nan}}},
		chartdata.WithDomain(chartdata.Range{From: 1_699_999_940_000, To: 1_700_000_060_000}),
		chartdata.WithOpen(true),
		chartdata.WithResets(start),
		chartdata.WithRefs(chartdata.FrameRef{Key: "median", Value: 0.1}),
		chartdata.WithNotes(chartdata.FrameNote{Key: "deploy", At: 1_700_000_000_000}))
	if err != nil {
		t.Fatalf("NewSeriesFrame: %v", err)
	}
	want := `{"type":"series","name":"wait_seconds","unit":"seconds","x":[1700000000000,1700000060000],` +
		`"series":[{"key":"p95","values":[0.25,null]}],"domain":[1699999940000,1700000060000],` +
		`"resets":[1700000000000],"refs":[{"key":"median","value":0.1}],` +
		`"notes":[{"key":"deploy","at":1700000000000}],"open":true}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestEmptyFramesEncodeEmptyLists(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewSeriesFrame(gauge("depth", measure.UnitCount), nil, nil)
	if err != nil {
		t.Fatalf("NewSeriesFrame: %v", err)
	}
	if got, want := encode(t, f), `{"type":"series","name":"depth","unit":"count","x":[],"series":[]}`; got != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestBinsFrameOnTheWire(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewBinsFrame(gauge("wait_seconds", measure.UnitSeconds), chartdata.AxisValue,
		[]chartdata.Bin{{From: 0, To: 1}, {From: 1, To: 5}},
		[]chartdata.Series{{Key: "wait_seconds", Values: []float64{4, 2}}})
	if err != nil {
		t.Fatalf("NewBinsFrame: %v", err)
	}
	want := `{"type":"bins","name":"wait_seconds","unit":"seconds","axis":"value","x1":[0,1],"x2":[1,5],` +
		`"series":[{"key":"wait_seconds","values":[4,2]}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestCategoriesFrameOnTheWire(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewCategoriesFrame(gauge("coverage_ratio", measure.UnitRatio), []string{"en", "fr"},
		[]chartdata.Series{{Key: "subtitles", Values: []float64{0.9, nan}}})
	if err != nil {
		t.Fatalf("NewCategoriesFrame: %v", err)
	}
	want := `{"type":"categories","name":"coverage_ratio","unit":"ratio","names":["en","fr"],` +
		`"series":[{"key":"subtitles","values":[0.9,null]}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestBoxesFrameOnTheWire(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewBoxesFrame(gauge("wait_seconds", measure.UnitSeconds), []string{"a", "b"},
		[]chartdata.Boxes{{
			Key: "wait", Low: []float64{1, nan}, Q1: []float64{2, nan}, Median: []float64{3, nan},
			Q3: []float64{4, nan}, High: []float64{5, nan}, Outliers: [][]float64{{9}, nil},
		}})
	if err != nil {
		t.Fatalf("NewBoxesFrame: %v", err)
	}
	want := `{"type":"boxes","name":"wait_seconds","unit":"seconds","names":["a","b"],"series":[{"key":"wait",` +
		`"low":[1,null],"q1":[2,null],"median":[3,null],"q3":[4,null],"high":[5,null],"outliers":[[9],[]]}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestPointsFrameOnTheWire(t *testing.T) {
	t.Parallel()
	m := chartdata.Measures{
		X: gauge("size_bytes", measure.UnitBytes),
		Y: gauge("wait_seconds", measure.UnitSeconds),
		R: gauge("jobs", measure.UnitCount),
	}
	f, err := chartdata.NewPointsFrame(m, []chartdata.Points{{
		Key: "hosts", Names: []string{"a", "b"}, X: []float64{1, 2}, Y: []float64{3, nan}, R: []float64{0, 5},
	}})
	if err != nil {
		t.Fatalf("NewPointsFrame: %v", err)
	}
	want := `{"type":"points","name":"wait_seconds","units":{"x":"bytes","y":"seconds","r":"count"},` +
		`"series":[{"key":"hosts","names":["a","b"],"x":[1,2],"y":[3,null],"r":[0,5]}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestGridFrameOnTheWire(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewGridFrame(gauge("runs", measure.UnitCount), &chartdata.Grid{
		X:      chartdata.FrameGridAxis{Edges: []float64{0, 10, 20}},
		Y:      chartdata.FrameGridAxis{Names: []string{"mon"}},
		Values: [][]float64{{1, nan}},
	})
	if err != nil {
		t.Fatalf("NewGridFrame: %v", err)
	}
	want := `{"type":"grid","name":"runs","unit":"count","x":{"edges":[0,10,20]},"y":{"names":["mon"]},"values":[[1,null]]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestCalendarFrameOnTheWire(t *testing.T) {
	t.Parallel()
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	want := `{"type":"calendar","name":"checks","unit":"count","zone":"Europe/Paris","values":[1,null],` +
		`"start":1774738800000,"open":true}`
	midnight := time.Date(2026, 3, 29, 0, 0, 0, 0, paris)
	for name, start := range map[string]time.Time{"inTheZone": midnight, "inUTC": midnight.UTC()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, err := chartdata.NewCalendarFrame(gauge("checks", measure.UnitCount), "Europe/Paris", start, []float64{1, nan}, chartdata.WithOpen(true))
			if err != nil {
				t.Fatalf("NewCalendarFrame: %v", err)
			}
			if got := encode(t, f); got != want {
				t.Errorf("JSON =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// A day's first instant is its start even where it reads no single 00:00:
// the first of Havana's two midnights on 2024-11-03, and Santiago's 01:00
// on 2024-09-08, a day with no 00:00.
func TestCalendarFrameStartsAtTheFirstInstantOfItsDay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, zone string
		start      time.Time
	}{
		{"havanaFirstMidnight", "America/Havana", time.Date(2024, 11, 3, 4, 0, 0, 0, time.UTC)},
		{"santiagoWithNoMidnight", "America/Santiago", time.Date(2024, 9, 8, 4, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := chartdata.NewCalendarFrame(gauge("checks", measure.UnitCount), tc.zone, tc.start, []float64{1})
			if err != nil {
				t.Fatalf("NewCalendarFrame: %v", err)
			}
			if f.Start != tc.start.UnixMilli() {
				t.Errorf("start = %d, want %d", f.Start, tc.start.UnixMilli())
			}
		})
	}
}

func TestTreeFramesOnTheWire(t *testing.T) {
	t.Parallel()
	d := gauge("disk_bytes", measure.UnitBytes)
	flat, err := chartdata.NewTreeFrame(d, []chartdata.TreePart{{Key: "a", Value: 3}, {Key: "b", Value: nan}})
	if err != nil {
		t.Fatalf("NewTreeFrame: %v", err)
	}
	want := `{"type":"tree","name":"disk_bytes","unit":"bytes","parts":[{"value":3,"key":"a"},{"value":null,"key":"b"}]}`
	if got := encode(t, flat); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
	grouped, err := chartdata.NewGroupedTreeFrame(d, []chartdata.TreeGroup{{Key: "g", Children: []chartdata.TreePart{{Key: "a", Value: 1}}}})
	if err != nil {
		t.Fatalf("NewGroupedTreeFrame: %v", err)
	}
	want = `{"type":"tree","name":"disk_bytes","unit":"bytes","groups":[{"key":"g","children":[{"value":1,"key":"a"}]}]}`
	if got := encode(t, grouped); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestFlowFramesOnTheWire(t *testing.T) {
	t.Parallel()
	d := gauge("energy", measure.UnitNone)
	sankey, err := chartdata.NewFlowFrame(d, chartdata.Flow{Links: []chartdata.FlowLink{
		{From: "coal", To: "power", Value: 380}, {From: "power", To: "homes", Value: nan},
	}})
	if err != nil {
		t.Fatalf("NewFlowFrame: %v", err)
	}
	want := `{"type":"flow","name":"energy","unit":"none","links":[{"value":380,"from":"coal","to":"power"},` +
		`{"value":null,"from":"power","to":"homes"}]}`
	if got := encode(t, sankey); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
	alluvial, err := chartdata.NewFlowFrame(d, chartdata.Flow{
		Nodes:  []string{"a", "b"},
		Stages: []string{"2019", "2024"},
		Links:  []chartdata.FlowLink{{From: "a", To: "a", Value: 2}, {From: "a", To: "b", Value: 1, Stage: "2019"}},
	})
	if err != nil {
		t.Fatalf("NewFlowFrame with stages: %v", err)
	}
	want = `{"type":"flow","name":"energy","unit":"none","nodes":[{"key":"a"},{"key":"b"}],"stages":["2019","2024"],` +
		`"links":[{"value":2,"from":"a","to":"a"},{"value":1,"from":"a","to":"b","stage":"2019"}]}`
	if got := encode(t, alluvial); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestEventsFrameOnTheWire(t *testing.T) {
	t.Parallel()
	at := time.UnixMilli(1_700_000_000_000)
	f, err := chartdata.NewEventsFrame(gauge("restarts", measure.UnitCount),
		[]chartdata.Times{{Key: "web", At: []time.Time{at, at}}, {Key: "db"}})
	if err != nil {
		t.Fatalf("NewEventsFrame: %v", err)
	}
	want := `{"type":"events","name":"restarts","rows":[{"key":"web","at":[1700000000000,1700000000000]},{"key":"db","at":[]}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestStatesFrameOnTheWire(t *testing.T) {
	t.Parallel()
	health := measure.NewStateSet("provider_health", "h", []string{"provider", "health"}, []string{"up", "down"})
	f, err := chartdata.NewStatesFrame(health, []chartdata.State{
		{At: time.UnixMilli(1000), Key: "up"}, {At: time.UnixMilli(2000)}, {At: time.UnixMilli(3000), Key: "down"},
	})
	if err != nil {
		t.Fatalf("NewStatesFrame: %v", err)
	}
	want := `{"type":"states","name":"provider_health","keys":["up",null,"down"],"at":[1000,2000,3000]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestValueFrameOnTheWire(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewValueFrame(gauge("coverage_ratio", measure.UnitRatio), []chartdata.FrameMeter{
		{Key: "en", Value: 0.5, Min: 0, Max: 1, Low: new(0.2), High: new(0.8), Optimum: new(1.0)},
		{Key: "fr", Value: 1, Min: 0, Max: 1},
	})
	if err != nil {
		t.Fatalf("NewValueFrame: %v", err)
	}
	want := `{"type":"value","name":"coverage_ratio","unit":"ratio","meters":[` +
		`{"low":0.2,"high":0.8,"optimum":1,"key":"en","value":0.5,"min":0,"max":1},` +
		`{"key":"fr","value":1,"min":0,"max":1}]}`
	if got := encode(t, f); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestFramesOwnTheirData(t *testing.T) {
	t.Parallel()
	x := []float64{1, 2}
	values := []float64{3, 4}
	refs := []chartdata.FrameRef{{Key: "p95", Value: 1}}
	f, err := chartdata.NewSeriesFrame(gauge("depth", measure.UnitCount), x,
		[]chartdata.Series{{Key: "depth", Values: values}}, chartdata.WithRefs(refs...))
	if err != nil {
		t.Fatalf("NewSeriesFrame: %v", err)
	}
	x[0], values[0], refs[0].Value = 99, 99, 99
	if f.X[0] != 1 || *f.Series[0].Values[0] != 3 || f.Refs[0].Value != 1 {
		t.Errorf("frame follows the caller's slices: x %v, value %v, ref %v", f.X[0], *f.Series[0].Values[0], f.Refs[0].Value)
	}
}

func TestDurationEdgesAreTheUILadder(t *testing.T) {
	t.Parallel()
	// @cplieger/ui src/data/scale/ladder.ts EDGES, in seconds.
	want := []float64{
		0, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600, 43200,
		86400, 259200, 604800, 1209600, 2592000, 7776000,
	}
	got := chartdata.DurationEdges()
	if len(got) != len(want) {
		t.Fatalf("DurationEdges = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DurationEdges[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	got[0] = 99
	if chartdata.DurationEdges()[0] != 0 {
		t.Error("DurationEdges shares its slice between calls")
	}
	if err := measure.NewRegistry("").Register(measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds,
		measure.WithBuckets(chartdata.DurationEdges()))); err != nil {
		t.Errorf("the ladder as histogram buckets: Register = %v", err)
	}
}
