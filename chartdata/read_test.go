package chartdata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

// readAt is a whole read's instant with a nine-digit fraction, which the
// wire carries as time.Time's own JSON writes it.
var readAt = time.Date(2026, 11, 18, 14, 5, 0, 123456789, time.UTC)

const readAtJSON = `"2026-11-18T14:05:00.123456789Z"`

func TestCountOf(t *testing.T) {
	t.Parallel()
	five := 5.0
	whole := measure.Whole(readAt)
	tests := []struct {
		last *float64
		name string
		want string
		read measure.Read
	}{
		{name: "complete", last: &five, read: whole, want: `{"value":5,"state":"complete","as_of":` + readAtJSON + `}`},
		{
			name: "partialKeepsTheLastWholeValue", last: &five, read: whole.Cut(measure.ReadPartial, "rate_limited"),
			want: `{"value":5,"state":"partial","as_of":` + readAtJSON + `,"reason":"rate_limited"}`,
		},
		{
			name: "blindKeepsTheLastWholeValue", last: &five, read: whole.Cut(measure.ReadBlind, "root_unavailable"),
			want: `{"value":5,"state":"blind","as_of":` + readAtJSON + `,"reason":"root_unavailable"}`,
		},
		{name: "zeroReadIsUnread", want: `{"value":null,"state":"unread","as_of":null}`},
		{
			name: "partialBeforeTheFirstWholeRead", read: measure.Read{State: measure.ReadPartial, Reason: "page_cap"},
			want: `{"value":null,"state":"partial","as_of":null,"reason":"page_cap"}`,
		},
		{
			name: "unsupported", read: measure.Read{State: measure.ReadUnsupported, Reason: "token_scope"},
			want: `{"value":null,"state":"unsupported","as_of":null,"reason":"token_scope"}`,
		},
		{name: "excluded", read: measure.Read{State: measure.ReadExcluded}, want: `{"value":null,"state":"excluded","as_of":null}`},
		{name: "zeroValue", last: new(0.0), read: whole, want: `{"value":0,"state":"complete","as_of":` + readAtJSON + `}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := chartdata.CountOf(tc.last, tc.read)
			if err != nil {
				t.Fatalf("CountOf: %v", err)
			}
			if got := encode(t, c); got != tc.want {
				t.Errorf("CountOf = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCountOfCopiesItsInputs(t *testing.T) {
	t.Parallel()
	last, at := 5.0, readAt
	c, err := chartdata.CountOf(&last, measure.Whole(at))
	if err != nil {
		t.Fatalf("CountOf: %v", err)
	}
	last = 6
	if c.Value == nil || c.AsOf == nil {
		t.Fatalf("CountOf = %s, want a value and a time", encode(t, c))
	}
	if c.Value == &last || *c.Value != 5 {
		t.Errorf("Value = %v, want its own 5, unmoved by the caller's later write", *c.Value)
	}
	if *c.AsOf != readAt {
		t.Errorf("AsOf = %v, want %v", *c.AsOf, readAt)
	}
}

// TestReadOf holds a read beside a figure that is not a Count, such as a
// list's total: every state is written, complete included with its as_of.
func TestReadOf(t *testing.T) {
	t.Parallel()
	whole := measure.Whole(readAt)
	tests := []struct {
		name string
		want string
		read measure.Read
	}{
		{name: "complete", read: whole, want: `{"state":"complete","as_of":` + readAtJSON + `}`},
		{
			name: "partial", read: whole.Cut(measure.ReadPartial, "rate_limited"),
			want: `{"state":"partial","as_of":` + readAtJSON + `,"reason":"rate_limited"}`,
		},
		{
			name: "blind", read: whole.Cut(measure.ReadBlind, "root_unavailable"),
			want: `{"state":"blind","as_of":` + readAtJSON + `,"reason":"root_unavailable"}`,
		},
		{name: "zeroReadIsUnread", want: `{"state":"unread","as_of":null}`},
		{
			name: "partialBeforeTheFirstWholeRead", read: measure.Read{State: measure.ReadPartial, Reason: "page_cap"},
			want: `{"state":"partial","as_of":null,"reason":"page_cap"}`,
		},
		{
			name: "unsupported", read: measure.Read{State: measure.ReadUnsupported, Reason: "token_scope"},
			want: `{"state":"unsupported","as_of":null,"reason":"token_scope"}`,
		},
		{name: "excluded", read: measure.Read{State: measure.ReadExcluded}, want: `{"state":"excluded","as_of":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := chartdata.ReadOf(tc.read)
			if err != nil {
				t.Fatalf("ReadOf: %v", err)
			}
			if got := encode(t, r); got != tc.want {
				t.Errorf("ReadOf = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestFrameReadRefusals holds what rule read refuses, on a frame through
// WithRead, on a Count through CountOf and on a read through ReadOf.
func TestFrameReadRefusals(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	frame := func(r measure.Read) func() error {
		return func() error {
			_, err := chartdata.NewSeriesFrame(d, []float64{1}, series("depth", 1), chartdata.WithRead(r))
			return err
		}
	}
	five := 5.0
	count := func(last *float64, r measure.Read) func() error {
		return func() error {
			_, err := chartdata.CountOf(last, r)
			return err
		}
	}
	alone := func(r measure.Read) func() error {
		return func() error {
			_, err := chartdata.ReadOf(r)
			return err
		}
	}
	whole := measure.Whole(readAt)
	longest := strings.Repeat("a", 64)
	past := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	beforeYear0 := time.Date(-1, 12, 31, 0, 0, 0, 0, time.UTC)
	farZone := readAt.In(time.FixedZone("", 24*3600))
	runRefusals(t, []refusalCase{
		{name: "frameUnknownState", rule: chartdata.RuleRead, field: "read.state", build: frame(whole.Cut("stale", ""))},
		{name: "frameCompleteWithReason", rule: chartdata.RuleRead, field: "read.reason", build: frame(whole.Cut(measure.ReadComplete, "rate_limited"))},
		{name: "frameReasonUppercase", rule: chartdata.RuleRead, field: "read.reason", build: frame(whole.Cut(measure.ReadPartial, "Rate_limited"))},
		{name: "frameReasonDash", rule: chartdata.RuleRead, field: "read.reason", build: frame(whole.Cut(measure.ReadPartial, "rate-limited"))},
		{name: "frameReasonText", rule: chartdata.RuleRead, field: "read.reason", build: frame(whole.Cut(measure.ReadPartial, "rate limited"))},
		{name: "frameReasonPast64Bytes", rule: chartdata.RuleRead, field: "read.reason", build: frame(whole.Cut(measure.ReadPartial, longest+"a"))},
		{name: "frameAsOfPastYear9999", rule: chartdata.RuleRead, field: "read.as_of", build: frame(measure.Whole(past).Cut(measure.ReadPartial, ""))},
		{name: "countUnknownState", rule: chartdata.RuleRead, field: "state", build: count(&five, whole.Cut("stale", ""))},
		{name: "countCompleteWithReason", rule: chartdata.RuleRead, field: "reason", build: count(&five, whole.Cut(measure.ReadComplete, "rate_limited"))},
		{name: "countReasonUppercase", rule: chartdata.RuleRead, field: "reason", build: count(&five, whole.Cut(measure.ReadPartial, "Rate_limited"))},
		{name: "countReasonPast64Bytes", rule: chartdata.RuleRead, field: "reason", build: count(&five, whole.Cut(measure.ReadPartial, longest+"a"))},
		{name: "countAsOfPastYear9999", rule: chartdata.RuleRead, field: "as_of", build: count(&five, measure.Whole(past))},
		{name: "countAsOfBeforeYear0", rule: chartdata.RuleRead, field: "as_of", build: count(&five, measure.Whole(beforeYear0))},
		{name: "frameAsOfZonePast23Hours", rule: chartdata.RuleRead, field: "read.as_of", build: frame(measure.Whole(farZone).Cut(measure.ReadBlind, ""))},
		{name: "countCompleteWithNoTime", rule: chartdata.RuleRead, field: "as_of", build: count(&five, measure.Read{State: measure.ReadComplete})},
		{name: "countValueWithNoWholeRead", rule: chartdata.RuleRead, field: "value", build: count(&five, measure.Read{State: measure.ReadPartial})},
		{name: "countWholeReadWithNoValue", rule: chartdata.RuleRead, field: "value", build: count(nil, whole.Cut(measure.ReadPartial, ""))},
		{name: "readUnknownState", rule: chartdata.RuleRead, field: "state", build: alone(whole.Cut("stale", ""))},
		{name: "readCompleteWithReason", rule: chartdata.RuleRead, field: "reason", build: alone(whole.Cut(measure.ReadComplete, "rate_limited"))},
		{name: "readReasonDash", rule: chartdata.RuleRead, field: "reason", build: alone(whole.Cut(measure.ReadPartial, "rate-limited"))},
		{name: "readReasonPast64Bytes", rule: chartdata.RuleRead, field: "reason", build: alone(whole.Cut(measure.ReadPartial, longest+"a"))},
		{name: "readAsOfPastYear9999", rule: chartdata.RuleRead, field: "as_of", build: alone(measure.Whole(past))},
		{name: "readCompleteWithNoTime", rule: chartdata.RuleRead, field: "as_of", build: alone(measure.Read{State: measure.ReadComplete})},
		{name: "countNaN", rule: chartdata.RuleFinite, field: "value", build: count(new(nan), whole)},
		{name: "countInfinite", rule: chartdata.RuleFinite, field: "value", build: count(new(-inf), whole)},
		{name: "countPastMaxMagnitude", rule: chartdata.RuleMagnitude, field: "value", build: count(new(1e301), whole)},
	})
	t.Run("longestReason", func(t *testing.T) {
		t.Parallel()
		if err := frame(whole.Cut(measure.ReadPartial, longest))(); err != nil {
			t.Errorf("a frame read with a 64-byte reason: %v, want it taken", err)
		}
		if err := count(&five, whole.Cut(measure.ReadPartial, longest))(); err != nil {
			t.Errorf("a count with a 64-byte reason: %v, want it taken", err)
		}
		if err := alone(whole.Cut(measure.ReadPartial, longest))(); err != nil {
			t.Errorf("a read with a 64-byte reason: %v, want it taken", err)
		}
	})
}

// readOf is the read member f's JSON carries, and whether it carries one.
func readOf(t *testing.T, f any) (string, bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encode(t, f)), &fields); err != nil {
		t.Fatal(err)
	}
	r, ok := fields["read"]
	return string(r), ok
}

// TestWithReadMarksEveryFrame builds each frame type through its constructor
// with WithRead: a read that is not whole is carried as read, and a whole
// one adds nothing, so the frame encodes as it does with no option.
func TestWithReadMarksEveryFrame(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	states := measure.NewStateSet("health", "h", []string{"health"}, []string{"up"})
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	builders := map[string]func(...chartdata.Option) (any, error){
		"series": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewSeriesFrame(d, []float64{1}, series("depth", 1), o...)
		},
		"bins": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewBinsFrame(d, chartdata.AxisValue, []chartdata.Bin{{From: 0, To: 1}}, series("depth", 1), o...)
		},
		"categories": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewCategoriesFrame(d, []string{"a"}, series("depth", 1), o...)
		},
		"boxes": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewBoxesFrame(d, []string{"a"}, []chartdata.Boxes{{
				Key: "depth", Low: []float64{1}, Q1: []float64{2}, Median: []float64{3}, Q3: []float64{4}, High: []float64{5},
			}}, o...)
		},
		"points": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewPointsFrame(chartdata.Measures{X: d, Y: d},
				[]chartdata.Points{{Key: "a", Names: []string{"p"}, X: []float64{1}, Y: []float64{2}}}, o...)
		},
		"grid": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewGridFrame(d, &chartdata.Grid{
				X: chartdata.FrameGridAxis{Names: []string{"a"}}, Y: chartdata.FrameGridAxis{Names: []string{"b"}}, Values: [][]float64{{1}},
			}, o...)
		},
		"calendar": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewCalendarFrame(d, "UTC", day, []float64{1}, o...)
		},
		"tree": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewTreeFrame(d, []chartdata.TreePart{{Key: "a", Value: 1}}, o...)
		},
		"treeGroups": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewGroupedTreeFrame(d, []chartdata.TreeGroup{{Key: "g", Children: []chartdata.TreePart{{Key: "a", Value: 1}}}}, o...)
		},
		"flow": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewFlowFrame(d, chartdata.Flow{Links: []chartdata.FlowLink{{From: "a", To: "b", Value: 1}}}, o...)
		},
		"events": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewEventsFrame(d, []chartdata.Times{{Key: "a", At: []time.Time{day}}}, o...)
		},
		"states": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewStatesFrame(states, []chartdata.State{{At: day, Key: "up"}}, o...)
		},
		"value": func(o ...chartdata.Option) (any, error) {
			return chartdata.NewValueFrame(d, []chartdata.FrameMeter{{Key: "a", Value: 1, Max: 2}}, o...)
		},
	}
	whole := measure.Whole(readAt)
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bare, err := build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			cut, err := build(chartdata.WithRead(whole.Cut(measure.ReadPartial, "rate_limited")))
			if err != nil {
				t.Fatalf("build WithRead(partial): %v", err)
			}
			want := `{"state":"partial","as_of":` + readAtJSON + `,"reason":"rate_limited"}`
			if got, _ := readOf(t, cut); got != want {
				t.Errorf("a partial read = %s, want %s", got, want)
			}
			unread, err := build(chartdata.WithRead(measure.Read{}))
			if err != nil {
				t.Fatalf("build WithRead(unread): %v", err)
			}
			if got, _ := readOf(t, unread); got != `{"state":"unread","as_of":null}` {
				t.Errorf("the zero read = %s, want unread with a null as_of", got)
			}
			for label, opts := range map[string][]chartdata.Option{
				"whole":         {chartdata.WithRead(whole)},
				"wholeLastWins": {chartdata.WithRead(whole.Cut(measure.ReadBlind, "")), chartdata.WithRead(whole)},
			} {
				f, err := build(opts...)
				if err != nil {
					t.Fatalf("build %s: %v", label, err)
				}
				if got := encode(t, f); got != encode(t, bare) {
					t.Errorf("%s: %s, want the frame built with no option, %s", label, got, encode(t, bare))
				}
			}
		})
	}
}

// TestWireEnumsTypeTheReadState holds the six states as one enum on the
// wire, the type of every read's state, so a generated decoder refuses any
// other.
func TestWireEnumsTypeTheReadState(t *testing.T) {
	t.Parallel()
	if !slices.Contains(chartdata.WireEnums(), reflect.TypeFor[measure.ReadState]()) {
		t.Errorf("WireEnums = %v, want measure.ReadState among them", chartdata.WireEnums())
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[chartdata.Count](), reflect.TypeFor[chartdata.Read]()} {
		if f, ok := typ.FieldByName("State"); !ok || f.Type != reflect.TypeFor[measure.ReadState]() {
			t.Errorf("%s.State is %v, want a measure.ReadState", typ.Name(), f.Type)
		}
	}
}

// TestCountAsOfIsRFC3339 reads the committed count and read fixtures: as_of
// is a string time.Time's JSON writes, with its fraction, and null before
// the first whole read; never a number.
func TestCountAsOfIsRFC3339(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"complete": readAtJSON, "partial": readAtJSON, "blind": readAtJSON,
		"unread": "null", "unsupported": "null", "excluded": "null",
	} {
		for _, kind := range []string{"count", "read"} {
			t.Run(kind+"-"+name, func(t *testing.T) {
				t.Parallel()
				checkAsOfFixture(t, kind+"-"+name, name, want)
			})
		}
	}
}

// checkAsOfFixture holds the fixture named fixture to the state and the
// as_of JSON want.
func checkAsOfFixture(t *testing.T, fixture, state, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, fixture+".json"))
	if err != nil {
		t.Fatalf("%v; %s", err, regenerate)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if got := string(fields["as_of"]); got != want {
		t.Errorf("as_of = %s, want %s", got, want)
	}
	if string(fields["state"]) != `"`+state+`"` {
		t.Errorf("state = %s, want %q", fields["state"], state)
	}
	if want == "null" {
		return
	}
	var at string
	if err := json.Unmarshal(fields["as_of"], &at); err != nil {
		t.Fatalf("as_of is no string: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || !parsed.Equal(readAt) {
		t.Errorf("as_of %q parses as %v (%v), want %v", at, parsed, err, readAt)
	}
}
