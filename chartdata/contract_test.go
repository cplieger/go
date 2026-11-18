package chartdata_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

// fixtureDir holds the golden fixtures @cplieger/ui vendors beside frame.ts.
const fixtureDir = "contract/fixtures"

const regenerate = "regenerate with UPDATE_GOLDEN=1 go test ./ -run TestTheFixturesAreCurrent in chartdata, and sync @cplieger/ui's copy"

// fixture is one golden file: its name and the value it holds, or raw JSON
// for a refusal no Go value can encode.
type fixture struct {
	value any
	err   error
	name  string
	raw   string
}

// refusal is a refusal fixture's body: a frame the constructors refuse, a
// count CountOf refuses or a read ReadOf refuses, and the rule and field
// they name.
type refusal struct {
	Frame any            `json:"frame,omitempty"`
	Count any            `json:"count,omitempty"`
	Read  any            `json:"read,omitempty"`
	Rule  chartdata.Rule `json:"rule"`
	Field string         `json:"field"`
}

// named returns a function making the fixture name of a constructor's
// results, so a constructor call is written as named(...)(call).
func named(name string) func(v any, err error) fixture {
	return func(v any, err error) fixture { return fixture{name: name, value: v, err: err} }
}

func frameFixtures(t *testing.T) []fixture {
	t.Helper()
	wait := gauge("wait_seconds", measure.UnitSeconds)
	runs := gauge("runs", measure.UnitCount)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return []fixture{
		named("frame-series")(chartdata.NewSeriesFrame(wait,
			[]float64{1_772_323_200_000, 1_772_323_260_000, 1_772_323_320_000},
			[]chartdata.Series{{Key: "p50", Values: []float64{0.1, nan, 0.2}}, {Key: "p95", Values: []float64{0.4, nan, 0.9}}},
			chartdata.WithDomain(chartdata.Range{From: 1_772_323_140_000, To: 1_772_323_320_000}),
			chartdata.WithOpen(true), chartdata.WithResets(time.UnixMilli(1_772_323_200_000)),
			chartdata.WithRefs(chartdata.FrameRef{Key: "target", Value: 0.5}),
			chartdata.WithNotes(chartdata.FrameNote{Key: "deploy", At: 1_772_323_260_000}))),
		named("frame-bins")(chartdata.NewBinsFrame(wait, chartdata.AxisValue,
			[]chartdata.Bin{{From: 0, To: 1}, {From: 1, To: 2}, {From: 2, To: 5}},
			[]chartdata.Series{{Key: "wait_seconds", Values: []float64{12, 4, 1}}})),
		named("frame-categories")(chartdata.NewCategoriesFrame(runs, []string{"ok", "failed", "skipped"},
			[]chartdata.Series{{Key: "host-a", Values: []float64{30, 2, nan}}, {Key: "host-b", Values: []float64{28, 0, 1}}})),
		named("frame-boxes")(chartdata.NewBoxesFrame(wait, []string{"scan", "fetch"}, []chartdata.Boxes{{
			Key: "wait_seconds", Low: []float64{0.1, nan}, Q1: []float64{0.2, nan}, Median: []float64{0.3, nan},
			Q3: []float64{0.5, nan}, High: []float64{0.9, nan}, Outliers: [][]float64{{2.5}, {}},
		}})),
		named("frame-points")(chartdata.NewPointsFrame(chartdata.Measures{
			X: gauge("size_bytes", measure.UnitBytes), Y: wait, R: runs,
		}, []chartdata.Points{{Key: "hosts", Names: []string{"a", "b"}, X: []float64{1024, 2048}, Y: []float64{0.2, nan}, R: []float64{3, 0}}})),
		named("frame-grid")(chartdata.NewGridFrame(runs, &chartdata.Grid{
			X: chartdata.FrameGridAxis{Edges: []float64{0, 6, 12, 18, 24}}, Y: chartdata.FrameGridAxis{Names: []string{"mon", "tue"}},
			Values: [][]float64{{1, 4, nan, 0}, {2, 3, 5, 1}},
		})),
		named("frame-calendar")(chartdata.NewCalendarFrame(runs, "UTC", day, []float64{3, nan, 5}, chartdata.WithOpen(true))),
		named("frame-tree")(chartdata.NewTreeFrame(gauge("disk_bytes", measure.UnitBytes),
			[]chartdata.TreePart{{Key: "media", Value: 9e11}, {Key: "backups", Value: 2e11}, {Key: "unknown", Value: nan}})),
		named("frame-tree-groups")(chartdata.NewGroupedTreeFrame(gauge("disk_bytes", measure.UnitBytes), []chartdata.TreeGroup{
			{Key: "media", Children: []chartdata.TreePart{{Key: "films", Value: 6e11}, {Key: "series", Value: 3e11}}},
			{Key: "backups", Children: []chartdata.TreePart{{Key: "db", Value: 2e11}}},
		})),
		named("frame-flow")(chartdata.NewFlowFrame(gauge("energy", measure.UnitNone), chartdata.Flow{Links: []chartdata.FlowLink{
			{From: "coal", To: "power", Value: 380}, {From: "gas", To: "power", Value: 120}, {From: "power", To: "homes", Value: 290},
		}})),
		named("frame-flow-stages")(chartdata.NewFlowFrame(gauge("voters", measure.UnitCount), chartdata.Flow{
			Nodes: []string{"blue", "red"}, Stages: []string{"2019", "2024"},
			Links: []chartdata.FlowLink{{From: "blue", To: "blue", Value: 10}, {From: "blue", To: "red", Value: 2}, {From: "red", To: "red", Value: nan}},
		})),
		named("frame-events")(chartdata.NewEventsFrame(gauge("restarts", measure.UnitCount), []chartdata.Times{
			{Key: "web", At: []time.Time{time.UnixMilli(1_772_323_200_000), time.UnixMilli(1_772_326_800_000)}}, {Key: "db"},
		})),
		named("frame-states")(chartdata.NewStatesFrame(
			measure.NewStateSet("provider_health", "h", []string{"provider", "health"}, []string{"up", "degraded", "down"}),
			[]chartdata.State{
				{At: time.UnixMilli(1_772_323_200_000), Key: "up"},
				{At: time.UnixMilli(1_772_323_260_000)},
				{At: time.UnixMilli(1_772_323_320_000), Key: "down"},
			},
		)),
		named("frame-value")(chartdata.NewValueFrame(gauge("coverage_ratio", measure.UnitRatio), []chartdata.FrameMeter{
			{Key: "en", Value: 0.92, Min: 0, Max: 1, Low: new(0.5), High: new(0.9), Optimum: new(1.0)},
			{Key: "fr", Value: 0.4, Min: 0, Max: 1},
		})),
		named("frame-series-read")(chartdata.NewSeriesFrame(wait,
			[]float64{1_772_323_200_000, 1_772_323_260_000},
			[]chartdata.Series{{Key: "p95", Values: []float64{0.4, 0.9}}},
			chartdata.WithRead(measure.Whole(readAt).Cut(measure.ReadPartial, "rate_limited")))),
	}
}

// countFixtures is one Count per read state, as an app's API row carries
// it, the three states that follow a whole read keeping its value and time,
// then one Read per state, as a row carries it beside a list's total.
func countFixtures() []fixture {
	last := 412.0
	whole := measure.Whole(readAt)
	reads := []struct {
		last  *float64
		state string
		read  measure.Read
	}{
		{state: "complete", last: &last, read: whole},
		{state: "partial", last: &last, read: whole.Cut(measure.ReadPartial, "rate_limited")},
		{state: "blind", last: &last, read: whole.Cut(measure.ReadBlind, "root_unavailable")},
		{state: "unread"},
		{state: "unsupported", read: measure.Read{State: measure.ReadUnsupported, Reason: "token_scope"}},
		{state: "excluded", read: measure.Read{State: measure.ReadExcluded}},
	}
	out := make([]fixture, 0, 2*len(reads))
	for _, r := range reads {
		out = append(out, named("count-"+r.state)(chartdata.CountOf(r.last, r.read)))
	}
	for _, r := range reads {
		out = append(out, named("read-"+r.state)(chartdata.ReadOf(r.read)))
	}
	return out
}

func eventFixtures(t *testing.T) []fixture {
	t.Helper()
	categories, err := chartdata.NewCategoriesFrame(gauge("runs", measure.UnitCount), []string{"ok", "failed"},
		[]chartdata.Series{{Key: "runs", Values: []float64{30, 2}}})
	if err != nil {
		t.Fatalf("NewCategoriesFrame: %v", err)
	}
	return []fixture{
		named("event-append-column")(chartdata.AppendColumn("wait_p95", 1_772_323_380_000, []float64{0.7, nan})),
		named("event-append-bin")(chartdata.AppendBin("runs_daily", chartdata.Bin{From: 1_772_323_200_000, To: 1_772_409_600_000}, []float64{4})),
		named("event-replace-column")(chartdata.ReplaceColumn("wait_p95", []float64{0.8, 0.1}, true)),
		named("event-replace-bin")(chartdata.ReplaceBin("runs_daily", chartdata.Bin{From: 1_772_323_200_000, To: 1_772_380_000_000}, []float64{5}, true)),
		{name: "event-reset", value: chartdata.ResetAt("wait_p95", time.UnixMilli(1_772_323_200_000))},
		named("event-set")(chartdata.SetFrame("runs", &categories)),
	}
}

// refusalFixtures pairs each fixture's frame, as the wire would carry it,
// with the constructor call that refuses the same data, so the rule a
// fixture names is the rule the constructors apply.
func refusalFixtures(t *testing.T) []fixture {
	t.Helper()
	d := gauge("depth", measure.UnitCount)
	names := keys(chartdata.MaxRows + 1)
	type check struct {
		frame any
		count any
		read  any
		build func() error
		name  string
		raw   string
		// rule is the rule a fixture named for a case of it breaks.
		rule chartdata.Rule
		// field is set on a body no constructor writes, whose fixture
		// names the field a reader refuses.
		field string
	}
	whole := measure.Whole(readAt)
	readFrame := func(read map[string]any) map[string]any {
		return map[string]any{
			"type": "series", "name": "depth", "unit": "count", "x": []float64{1},
			"series": []map[string]any{{"key": "depth", "values": []float64{1}}}, "read": read,
		}
	}
	readSeries := func(r measure.Read) func() error {
		return func() error {
			_, err := chartdata.NewSeriesFrame(d, []float64{1}, series("depth", 1), chartdata.WithRead(r))
			return err
		}
	}
	long := strings.Repeat("a", 65)
	// year10000 is an as_of only a wire written by hand carries:
	// time.Time's JSON refuses to write a year past 9999.
	const year10000 = "10000-01-01T00:00:00Z"
	checks := []check{
		{name: "length", frame: map[string]any{
			"type": "series", "name": "depth", "unit": "count", "x": []float64{1, 2},
			"series": []map[string]any{{"key": "depth", "values": []float64{1}}},
		}, build: func() error {
			_, err := chartdata.NewSeriesFrame(d, []float64{1, 2}, series("depth", 1))
			return err
		}},
		{name: "order", frame: map[string]any{
			"type": "series", "name": "depth", "unit": "count", "x": []float64{2, 1}, "series": []any{},
		}, build: func() error {
			_, err := chartdata.NewSeriesFrame(d, []float64{2, 1}, nil)
			return err
		}},
		{name: "bin", frame: map[string]any{
			"type": "bins", "name": "depth", "unit": "count", "axis": "value", "x1": []float64{0, 4}, "x2": []float64{3, 3}, "series": []any{},
		}, build: func() error {
			_, err := chartdata.NewBinsFrame(d, chartdata.AxisValue, []chartdata.Bin{{From: 0, To: 3}, {From: 4, To: 3}}, nil)
			return err
		}},
		{
			name: "finite", raw: `{"type":"series","name":"depth","unit":"count","x":[1],"series":[{"key":"depth","values":[1e999]}]}`,
			build: func() error {
				_, err := chartdata.NewSeriesFrame(d, []float64{1}, series("depth", inf))
				return err
			},
		},
		{name: "magnitude", frame: map[string]any{
			"type": "series", "name": "depth", "unit": "count", "x": []float64{1},
			"series": []map[string]any{{"key": "depth", "values": []float64{1e301}}},
		}, build: func() error {
			_, err := chartdata.NewSeriesFrame(d, []float64{1}, series("depth", 1e301))
			return err
		}},
		{name: "negative", frame: map[string]any{
			"type": "tree", "name": "depth", "unit": "count", "parts": []map[string]any{{"key": "a", "value": -1}},
		}, build: func() error {
			_, err := chartdata.NewTreeFrame(d, []chartdata.TreePart{{Key: "a", Value: -1}})
			return err
		}},
		{name: "duplicate", frame: map[string]any{
			"type": "categories", "name": "depth", "unit": "count", "names": []string{"a", "a"}, "series": []any{},
		}, build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, []string{"a", "a"}, nil)
			return err
		}},
		{name: "empty", frame: map[string]any{
			"type": "categories", "name": "depth", "unit": "count", "names": []string{""}, "series": []any{},
		}, build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, []string{""}, nil)
			return err
		}},
		{name: "shape", frame: map[string]any{
			"type": "boxes", "name": "depth", "unit": "count", "names": []string{"a"},
			"series": []map[string]any{{"key": "depth", "low": []any{1}, "q1": []any{2}, "median": []any{nil}, "q3": []any{4}, "high": []any{5}}},
		}, build: func() error {
			_, err := chartdata.NewBoxesFrame(d, []string{"a"}, []chartdata.Boxes{{
				Key: "depth", Low: []float64{1}, Q1: []float64{2}, Median: []float64{nan}, Q3: []float64{4}, High: []float64{5},
			}})
			return err
		}},
		{name: "limit", frame: map[string]any{
			"type": "categories", "name": "depth", "unit": "count", "names": names, "series": []any{},
		}, build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, names, nil)
			return err
		}},
		{
			name: "read-complete", rule: chartdata.RuleRead, field: "read.state",
			frame: readFrame(map[string]any{"state": "complete", "as_of": readAt}),
		},
		{
			name: "read-state", rule: chartdata.RuleRead, frame: readFrame(map[string]any{"state": "stale", "as_of": readAt}),
			build: readSeries(whole.Cut("stale", "")),
		},
		{
			name: "read-reason", rule: chartdata.RuleRead,
			frame: readFrame(map[string]any{"state": "partial", "as_of": readAt, "reason": "Rate limited"}),
			build: readSeries(whole.Cut(measure.ReadPartial, "Rate limited")),
		},
		{
			name: "read-reason-length", rule: chartdata.RuleRead,
			frame: readFrame(map[string]any{"state": "partial", "as_of": readAt, "reason": long}),
			build: readSeries(whole.Cut(measure.ReadPartial, long)),
		},
		{
			name: "read-count-reason", rule: chartdata.RuleRead,
			count: map[string]any{"value": 5, "state": "complete", "as_of": readAt, "reason": "rate_limited"},
			build: func() error {
				_, err := chartdata.CountOf(new(5.0), whole.Cut(measure.ReadComplete, "rate_limited"))
				return err
			},
		},
		{
			name: "read-as-of-year", rule: chartdata.RuleRead, field: "read.as_of",
			frame: readFrame(map[string]any{"state": "partial", "as_of": year10000}),
		},
		{
			name: "read-count-value-without-as-of", rule: chartdata.RuleRead,
			count: map[string]any{"value": 5, "state": "partial", "as_of": nil},
			build: func() error {
				_, err := chartdata.CountOf(new(5.0), measure.Read{State: measure.ReadPartial})
				return err
			},
		},
		{
			name: "read-count-as-of-without-value", rule: chartdata.RuleRead,
			count: map[string]any{"value": nil, "state": "partial", "as_of": readAt},
			build: func() error {
				_, err := chartdata.CountOf(nil, whole.Cut(measure.ReadPartial, ""))
				return err
			},
		},
		{
			name: "read-count-complete-without-as-of", rule: chartdata.RuleRead,
			count: map[string]any{"value": 5, "state": "complete", "as_of": nil},
			build: func() error {
				_, err := chartdata.CountOf(new(5.0), measure.Read{State: measure.ReadComplete})
				return err
			},
		},
		{
			name: "read-complete-without-as-of", rule: chartdata.RuleRead,
			read: map[string]any{"state": "complete", "as_of": nil},
			build: func() error {
				_, err := chartdata.ReadOf(measure.Read{State: measure.ReadComplete})
				return err
			},
		},
		{
			name: "read-count-as-of-year", rule: chartdata.RuleRead, field: "as_of",
			count: map[string]any{"value": 5, "state": "complete", "as_of": year10000},
		},
	}
	out := make([]fixture, len(checks))
	for i, c := range checks {
		if c.rule == "" {
			c.rule = chartdata.Rule(c.name)
		}
		rule, field := c.rule, c.field
		if c.build != nil {
			rule, field = refusalOf(t, c.name, c.rule, c.build())
		}
		f := fixture{name: "refusal-" + c.name, value: refusal{Frame: c.frame, Count: c.count, Read: c.read, Rule: rule, Field: field}}
		if c.raw != "" {
			f = fixture{name: f.name, raw: `{"frame":` + c.raw + `,"rule":"` + string(rule) + `","field":"` + field + `"}`}
		}
		out[i] = f
	}
	return out
}

// refusalOf is err's rule and field, failing t unless err is a refusal by
// the rule its fixture names.
func refusalOf(t *testing.T, name string, want chartdata.Rule, err error) (rule chartdata.Rule, field string) {
	t.Helper()
	var r *chartdata.RefusalError
	if !errors.As(err, &r) || r.Rule != want {
		t.Fatalf("refusal-%s: the constructor returned %v, want a %q refusal", name, err, want)
	}
	return r.Rule, r.Field
}

func limitsFixtures() []fixture {
	return []fixture{
		{name: "limits", value: map[string]any{
			"maxValues": chartdata.MaxValues, "maxRows": chartdata.MaxRows, "maxSubRows": chartdata.MaxSubRows,
			"maxMagnitude": chartdata.MaxMagnitude, "maxCalendarDays": chartdata.MaxCalendarDays,
			"minEveryMs": chartdata.MinEvery.Milliseconds(), "maxPoints": chartdata.MaxPoints, "maxLabelSets": chartdata.MaxLabelSets,
			"maxBaselines": chartdata.MaxBaselines,
		}},
		{name: "duration-edges", value: map[string]any{"unit": "seconds", "edges": chartdata.DurationEdges()}},
	}
}

// encodeFixture is a fixture's bytes: two-space JSON and a final newline.
func encodeFixture(t *testing.T, f fixture) []byte {
	t.Helper()
	if f.err != nil {
		t.Fatalf("%s: building it: %v", f.name, f.err)
	}
	var buf bytes.Buffer
	if f.raw != "" {
		buf.WriteString(f.raw)
	} else {
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(f.value); err != nil {
			t.Fatalf("%s: encode: %v", f.name, err)
		}
	}
	var out bytes.Buffer
	if err := json.Indent(&out, bytes.TrimSpace(buf.Bytes()), "", "  "); err != nil {
		t.Fatalf("%s: indent: %v", f.name, err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// TestTheFixturesAreCurrent is the fixtures' drift check: each file in
// contract/fixtures is what the constructors produce today, and no other
// file is there.
func TestTheFixturesAreCurrent(t *testing.T) {
	all := slices.Concat(frameFixtures(t), countFixtures(), eventFixtures(t), refusalFixtures(t), limitsFixtures())
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	if update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{}
	for _, f := range all {
		file := filepath.Join(fixtureDir, f.name+".json")
		want[filepath.Base(file)] = true
		got := encodeFixture(t, f)
		if update {
			if err := os.WriteFile(file, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: %v; %s", file, err, regenerate)
			continue
		}
		if !bytes.Equal(got, committed) {
			t.Errorf("%s is stale; %s", file, regenerate)
		}
	}
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("%s/%s is no fixture the constructors produce; delete it", fixtureDir, e.Name())
		}
	}
}

// TestFrameFixturesDecodeAsTheirTypes reads each frame, count, read and
// event fixture back into its Go type, as an app's decoder would, and finds
// the same bytes, so no field is lost on the wire.
func TestFrameFixturesDecodeAsTheirTypes(t *testing.T) {
	t.Parallel()
	for _, f := range slices.Concat(frameFixtures(t), countFixtures(), eventFixtures(t)) {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			b := encodeFixture(t, f)
			back := reflect.New(reflect.TypeOf(f.value)).Interface()
			if err := json.Unmarshal(b, back); err != nil {
				t.Fatalf("decode: %v", err)
			}
			again := encodeFixture(t, fixture{name: f.name, value: back})
			if !bytes.Equal(b, again) {
				t.Errorf("round trip =\n%s\nwant\n%s", again, b)
			}
		})
	}
}

// TestEventsSendTheFieldsTheirKindsUnionAllows checks WireUnions against
// what the constructors build: every event, a set of each frame included,
// sends only its kind's fields, and each field a kind lists is sent by some
// event of that kind, so the generated union neither refuses a real event
// nor admits a field no event sends.
func TestEventsSendTheFieldsTheirKindsUnionAllows(t *testing.T) {
	t.Parallel()
	unions := chartdata.WireUnions()
	if len(unions) != 1 || unions[0].Type != reflect.TypeFor[chartdata.FrameEvent]() || unions[0].Tag != "kind" {
		t.Fatalf("WireUnions = %+v, want FrameEvent tagged by kind", unions)
	}
	cases := unions[0].Cases
	events := eventFixtures(t)
	for _, f := range frameFixtures(t) {
		if f.err != nil {
			t.Fatalf("%s: %v", f.name, f.err)
		}
		ptr := reflect.New(reflect.TypeOf(f.value))
		ptr.Elem().Set(reflect.ValueOf(f.value))
		events = append(events, named("set-"+f.name)(chartdata.SetFrame("k", ptr.Interface().(chartdata.Frame))))
	}
	sent := map[string]map[string]bool{}
	for _, e := range events {
		if e.err != nil {
			t.Fatalf("%s: %v", e.name, e.err)
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(encode(t, e.value)), &fields); err != nil {
			t.Fatalf("%s: %v", e.name, err)
		}
		kind, _ := fields["kind"].(string)
		if sent[kind] == nil {
			sent[kind] = map[string]bool{}
		}
		for name := range fields {
			if name == "kind" {
				continue
			}
			sent[kind][name] = true
			if !slices.Contains(cases[kind], name) {
				t.Errorf("%s sends %q, which a %s event's union case does not allow", e.name, name, kind)
			}
		}
	}
	for kind, fields := range cases {
		for _, name := range fields {
			if !sent[kind][name] {
				t.Errorf("the %s case allows %q, which no %s event sends", kind, name, kind)
			}
		}
	}
	unions[0].Cases["set"] = nil
	if chartdata.WireUnions()[0].Cases["set"] == nil {
		t.Error("WireUnions shares its cases between calls")
	}
}

// TestEveryFrameSendsTheTypeItsTagPins checks the Go side of each frame's
// literal type: every frame struct's Type field carries a wiregen const tag,
// no two frames pin the same type, every FrameType constant is pinned, and
// each frame's constructor sends exactly the pinned value.
func TestEveryFrameSendsTheTypeItsTagPins(t *testing.T) {
	t.Parallel()
	all := frameTypeConstants(t)
	pinned := map[reflect.Type]string{}
	owner := map[string]reflect.Type{}
	for _, typ := range chartdata.WireTypes() {
		if !strings.HasSuffix(typ.Name(), "Frame") {
			continue
		}
		f, ok := typ.FieldByName("Type")
		lit, tagged := strings.CutPrefix(f.Tag.Get("wiregen"), "const=")
		if !ok || !tagged {
			t.Errorf("%s.Type carries no wiregen const tag", typ.Name())
			continue
		}
		if prev, dup := owner[lit]; dup {
			t.Errorf("%s and %s both pin type %q", prev.Name(), typ.Name(), lit)
		}
		pinned[typ], owner[lit] = lit, typ
	}
	for _, ft := range all {
		if owner[ft] == nil {
			t.Errorf("no frame pins type %q", ft)
		}
	}
	built := map[reflect.Type]bool{}
	for _, f := range frameFixtures(t) {
		if f.err != nil {
			t.Fatalf("%s: %v", f.name, f.err)
		}
		built[reflect.TypeOf(f.value)] = true
		var sent struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(encode(t, f.value)), &sent); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if want := pinned[reflect.TypeOf(f.value)]; sent.Type != want {
			t.Errorf("%s sends type %q, but %T pins %q", f.name, sent.Type, f.value, want)
		}
	}
	for typ := range pinned {
		if !built[typ] {
			t.Errorf("no fixture builds a %s", typ.Name())
		}
	}
}

// frameTypeConstants is every FrameType value the package declares, read
// from its source because reflect cannot list a type's constants.
func frameTypeConstants(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "FrameType" {
					continue
				}
				for i, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: FrameType %s is not a string literal", fset.Position(v.Pos()), vs.Names[i].Name)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					values = append(values, s)
				}
			}
		}
	}
	if len(values) == 0 {
		t.Fatal("found no FrameType constants")
	}
	return values
}
