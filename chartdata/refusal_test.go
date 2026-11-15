package chartdata_test

import (
	"math"
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

type refusalCase struct {
	build func() error
	name  string
	rule  chartdata.Rule
	field string
}

func runRefusals(t *testing.T, cases []refusalCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantRefusal(t, tc.build(), tc.rule, tc.field)
		})
	}
}

func series(key string, values ...float64) []chartdata.Series {
	return []chartdata.Series{{Key: key, Values: values}}
}

var inf = math.Inf(1)

func TestConstructorsRefuseABadDeclaration(t *testing.T) {
	t.Parallel()
	var typedNil *measure.Gauge
	runRefusals(t, []refusalCase{
		{name: "nil", rule: chartdata.RuleDeclaration, field: "declaration", build: func() error {
			_, err := chartdata.NewSeriesFrame(nil, nil, nil)
			return err
		}},
		{name: "typedNil", rule: chartdata.RuleDeclaration, field: "declaration", build: func() error {
			_, err := chartdata.NewCategoriesFrame(typedNil, nil, nil)
			return err
		}},
		{name: "noName", rule: chartdata.RuleDeclaration, field: "declaration", build: func() error {
			_, err := chartdata.NewValueFrame(gauge("", measure.UnitNone), nil)
			return err
		}},
		{name: "pointsX", rule: chartdata.RuleDeclaration, field: "measures.x", build: func() error {
			_, err := chartdata.NewPointsFrame(chartdata.Measures{Y: gauge("y", measure.UnitNone)}, nil)
			return err
		}},
		{name: "pointsY", rule: chartdata.RuleDeclaration, field: "measures.y", build: func() error {
			_, err := chartdata.NewPointsFrame(chartdata.Measures{X: gauge("x", measure.UnitNone)}, nil)
			return err
		}},
		{name: "pointsTypedNilR", rule: chartdata.RuleDeclaration, field: "measures.r", build: func() error {
			_, err := chartdata.NewPointsFrame(chartdata.Measures{
				X: gauge("x", measure.UnitNone), Y: gauge("y", measure.UnitNone), R: typedNil,
			}, nil)
			return err
		}},
	})
}

func TestConstructorsRefuseABadOption(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	x := []float64{1, 2}
	s := series("depth", 1, 2)
	newSeries := func(opts ...chartdata.Option) func() error {
		return func() error {
			_, err := chartdata.NewSeriesFrame(d, x, s, opts...)
			return err
		}
	}
	runRefusals(t, []refusalCase{
		{name: "nilOption", rule: chartdata.RuleOption, field: "options", build: newSeries(nil)},
		{name: "domainOnCategories", rule: chartdata.RuleOption, field: "options", build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, nil, nil, chartdata.WithDomain(chartdata.Range{From: 0, To: 1}))
			return err
		}},
		{name: "openOnTree", rule: chartdata.RuleOption, field: "options", build: func() error {
			_, err := chartdata.NewTreeFrame(d, nil, chartdata.WithOpen(true))
			return err
		}},
		{name: "resetsOnCalendar", rule: chartdata.RuleOption, field: "options", build: func() error {
			_, err := chartdata.NewCalendarFrame(d, "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil,
				chartdata.WithResets(time.Now()))
			return err
		}},
		{
			name: "domainInfinite", rule: chartdata.RuleFinite, field: "domain",
			build: newSeries(chartdata.WithDomain(chartdata.Range{From: 0, To: inf})),
		},
		{
			name: "domainNaN", rule: chartdata.RuleFinite, field: "domain",
			build: newSeries(chartdata.WithDomain(chartdata.Range{From: nan, To: 1})),
		},
		{
			name: "domainBackwards", rule: chartdata.RuleOrder, field: "domain",
			build: newSeries(chartdata.WithDomain(chartdata.Range{From: 2, To: 2})),
		},
		{
			name: "resetsOutOfOrder", rule: chartdata.RuleOrder, field: "resets",
			build: newSeries(chartdata.WithResets(time.UnixMilli(2), time.UnixMilli(2))),
		},
		{
			name: "resetsInOneMillisecond", rule: chartdata.RuleOrder, field: "resets",
			build: newSeries(chartdata.WithResets(time.UnixMilli(2), time.UnixMilli(2).Add(time.Microsecond))),
		},
		{
			name: "refInfinite", rule: chartdata.RuleFinite, field: "refs[1].value",
			build: newSeries(chartdata.WithRefs(chartdata.FrameRef{Key: "a", Value: 1}, chartdata.FrameRef{Key: "b", Value: -inf})),
		},
		{
			name: "refTooLarge", rule: chartdata.RuleMagnitude, field: "refs[0].value",
			build: newSeries(chartdata.WithRefs(chartdata.FrameRef{Key: "a", Value: 1e301})),
		},
		{
			name: "noteNaN", rule: chartdata.RuleFinite, field: "notes[0].at",
			build: newSeries(chartdata.WithNotes(chartdata.FrameNote{Key: "deploy", At: nan})),
		},
	})
}

func TestSeriesAndBinsFramesRefuseMalformedData(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	newSeries := func(x []float64, s []chartdata.Series) func() error {
		return func() error {
			_, err := chartdata.NewSeriesFrame(d, x, s)
			return err
		}
	}
	newBins := func(axis chartdata.FrameAxis, bins []chartdata.Bin, s []chartdata.Series) func() error {
		return func() error {
			_, err := chartdata.NewBinsFrame(d, axis, bins, s)
			return err
		}
	}
	two := []chartdata.Bin{{From: 0, To: 1}, {From: 1, To: 2}}
	runRefusals(t, []refusalCase{
		{name: "xInfinite", rule: chartdata.RuleFinite, field: "x[1]", build: newSeries([]float64{1, inf}, nil)},
		{name: "xNaN", rule: chartdata.RuleFinite, field: "x[0]", build: newSeries([]float64{nan}, nil)},
		{name: "xTooLarge", rule: chartdata.RuleMagnitude, field: "x[0]", build: newSeries([]float64{-2e300}, nil)},
		{name: "xUnsorted", rule: chartdata.RuleOrder, field: "x[1]", build: newSeries([]float64{2, 1}, nil)},
		{name: "xDuplicate", rule: chartdata.RuleOrder, field: "x[2]", build: newSeries([]float64{1, 2, 2}, nil)},
		{name: "valuesShort", rule: chartdata.RuleLength, field: "series[0].values", build: newSeries([]float64{1, 2}, series("a", 1))},
		{name: "valueInfinite", rule: chartdata.RuleFinite, field: "series[0].values[1]", build: newSeries([]float64{1, 2}, series("a", 1, inf))},
		{name: "valueTooLarge", rule: chartdata.RuleMagnitude, field: "series[0].values[0]", build: newSeries([]float64{1}, series("a", 1e301))},
		{
			name: "keyTwice", rule: chartdata.RuleDuplicate, field: "series[1].key",
			build: newSeries([]float64{1}, []chartdata.Series{{Key: "a", Values: []float64{1}}, {Key: "a", Values: []float64{2}}}),
		},
		{name: "axis", rule: chartdata.RuleShape, field: "axis", build: newBins("bucket", two, nil)},
		{
			name: "x1Unsorted", rule: chartdata.RuleOrder, field: "x1[1]",
			build: newBins(chartdata.AxisValue, []chartdata.Bin{{From: 1, To: 2}, {From: 1, To: 3}}, nil),
		},
		{
			name: "x2Decreasing", rule: chartdata.RuleOrder, field: "x2[1]",
			build: newBins(chartdata.AxisValue, []chartdata.Bin{{From: 0, To: 5}, {From: 1, To: 3}}, nil),
		},
		{
			name: "x1AfterX2", rule: chartdata.RuleBin, field: "bins[1]",
			build: newBins(chartdata.AxisTime, []chartdata.Bin{{From: 0, To: 3}, {From: 4, To: 3}}, nil),
		},
		{
			name: "emptyBin", rule: chartdata.RuleBin, field: "bins[0]",
			build: newBins(chartdata.AxisTime, []chartdata.Bin{{From: 1, To: 1}}, nil),
		},
		{
			name: "binInfinite", rule: chartdata.RuleFinite, field: "x2[0]",
			build: newBins(chartdata.AxisValue, []chartdata.Bin{{From: 1, To: inf}}, nil),
		},
		{
			name: "binValuesShort", rule: chartdata.RuleLength, field: "series[0].values",
			build: newBins(chartdata.AxisValue, two, series("a", 1)),
		},
	})
}

func TestCategoriesAndBoxesFramesRefuseMalformedData(t *testing.T) {
	t.Parallel()
	d := gauge("wait_seconds", measure.UnitSeconds)
	newCategories := func(names []string, s []chartdata.Series) func() error {
		return func() error {
			_, err := chartdata.NewCategoriesFrame(d, names, s)
			return err
		}
	}
	box := func(low, q1, median, q3, high float64, outliers ...float64) chartdata.Boxes {
		b := chartdata.Boxes{Key: "a", Low: []float64{low}, Q1: []float64{q1}, Median: []float64{median}, Q3: []float64{q3}, High: []float64{high}}
		if outliers != nil {
			b.Outliers = [][]float64{outliers}
		}
		return b
	}
	newBoxes := func(names []string, s ...chartdata.Boxes) func() error {
		return func() error {
			_, err := chartdata.NewBoxesFrame(d, names, s)
			return err
		}
	}
	one := []string{"x"}
	runRefusals(t, []refusalCase{
		{name: "emptyName", rule: chartdata.RuleEmpty, field: "names[1]", build: newCategories([]string{"a", ""}, nil)},
		{name: "nameTwice", rule: chartdata.RuleDuplicate, field: "names[1]", build: newCategories([]string{"a", "a"}, nil)},
		{name: "valuesLong", rule: chartdata.RuleLength, field: "series[0].values", build: newCategories(one, series("a", 1, 2))},
		{name: "valueInfinite", rule: chartdata.RuleFinite, field: "series[0].values[0]", build: newCategories(one, series("a", -inf))},
		{name: "boxNameTwice", rule: chartdata.RuleDuplicate, field: "names[1]", build: newBoxes([]string{"x", "x"})},
		{name: "boxKeyTwice", rule: chartdata.RuleDuplicate, field: "series[1].key", build: newBoxes(one, box(1, 2, 3, 4, 5), box(1, 2, 3, 4, 5))},
		{name: "boxShort", rule: chartdata.RuleLength, field: "series[0].q1", build: newBoxes(one, chartdata.Boxes{
			Key: "a", Low: []float64{1}, Median: []float64{3}, Q3: []float64{4}, High: []float64{5},
		})},
		{name: "boxInfinite", rule: chartdata.RuleFinite, field: "series[0].high[0]", build: newBoxes(one, box(1, 2, 3, 4, inf))},
		{name: "boxPartial", rule: chartdata.RuleShape, field: "series[0][0]", build: newBoxes(one, box(1, 2, nan, 4, 5))},
		{name: "boxOutOfOrder", rule: chartdata.RuleOrder, field: "series[0][0]", build: newBoxes(one, box(1, 3, 2, 4, 5))},
		{name: "outliersWithoutBox", rule: chartdata.RuleShape, field: "series[0][0]", build: newBoxes(one, box(nan, nan, nan, nan, nan, 9))},
		{name: "outlierInside", rule: chartdata.RuleShape, field: "series[0][0].outliers[1]", build: newBoxes(one, box(1, 2, 3, 4, 5, 0, 5))},
		{name: "outlierInfinite", rule: chartdata.RuleFinite, field: "series[0][0].outliers[0]", build: newBoxes(one, box(1, 2, 3, 4, 5, inf))},
		{name: "outlierListsShort", rule: chartdata.RuleLength, field: "series[0].outliers", build: newBoxes([]string{"x", "y"}, chartdata.Boxes{
			Key: "a", Low: []float64{1, 1}, Q1: []float64{2, 2}, Median: []float64{3, 3}, Q3: []float64{4, 4}, High: []float64{5, 5},
			Outliers: [][]float64{{9}},
		})},
	})
}

func TestPointsAndGridFramesRefuseMalformedData(t *testing.T) {
	t.Parallel()
	xy := chartdata.Measures{X: gauge("x", measure.UnitNone), Y: gauge("y", measure.UnitNone)}
	xyr := chartdata.Measures{X: xy.X, Y: xy.Y, R: gauge("r", measure.UnitCount)}
	newPoints := func(m chartdata.Measures, s ...chartdata.Points) func() error {
		return func() error {
			_, err := chartdata.NewPointsFrame(m, s)
			return err
		}
	}
	d := gauge("runs", measure.UnitCount)
	newGrid := func(g *chartdata.Grid) func() error {
		return func() error {
			_, err := chartdata.NewGridFrame(d, g)
			return err
		}
	}
	names := func(n ...string) chartdata.FrameGridAxis { return chartdata.FrameGridAxis{Names: n} }
	edges := func(e ...float64) chartdata.FrameGridAxis { return chartdata.FrameGridAxis{Edges: e} }
	runRefusals(t, []refusalCase{
		{name: "pointsKeyTwice", rule: chartdata.RuleDuplicate, field: "series[1].key", build: newPoints(xy,
			chartdata.Points{Key: "g"}, chartdata.Points{Key: "g"})},
		{name: "pointNameAcrossGroups", rule: chartdata.RuleDuplicate, field: "names[1]", build: newPoints(xy,
			chartdata.Points{Key: "g", Names: []string{"a"}, X: []float64{1}, Y: []float64{1}},
			chartdata.Points{Key: "h", Names: []string{"a"}, X: []float64{1}, Y: []float64{1}})},
		{name: "pointNameEmpty", rule: chartdata.RuleEmpty, field: "names[0]", build: newPoints(xy,
			chartdata.Points{Key: "g", Names: []string{""}, X: []float64{1}, Y: []float64{1}})},
		{name: "pointsXShort", rule: chartdata.RuleLength, field: "series[0].x", build: newPoints(xy,
			chartdata.Points{Key: "g", Names: []string{"a"}, Y: []float64{1}})},
		{name: "pointsYInfinite", rule: chartdata.RuleFinite, field: "series[0].y[0]", build: newPoints(xy,
			chartdata.Points{Key: "g", Names: []string{"a"}, X: []float64{1}, Y: []float64{inf}})},
		{name: "sizeWithoutMeasure", rule: chartdata.RuleShape, field: "series[0].r", build: newPoints(xy,
			chartdata.Points{Key: "g", Names: []string{"a"}, X: []float64{1}, Y: []float64{1}, R: []float64{1}})},
		{name: "sizeMissing", rule: chartdata.RuleLength, field: "series[0].r", build: newPoints(xyr,
			chartdata.Points{Key: "g", Names: []string{"a"}, X: []float64{1}, Y: []float64{1}})},
		{name: "sizeNegative", rule: chartdata.RuleNegative, field: "series[0].r[0]", build: newPoints(xyr,
			chartdata.Points{Key: "g", Names: []string{"a"}, X: []float64{1}, Y: []float64{1}, R: []float64{-1}})},
		{name: "gridNil", rule: chartdata.RuleEmpty, field: "grid", build: newGrid(nil)},
		{name: "gridBoth", rule: chartdata.RuleShape, field: "x", build: newGrid(&chartdata.Grid{
			X: chartdata.FrameGridAxis{Names: []string{"a"}, Edges: []float64{0, 1}}, Y: names("r"),
		})},
		{name: "gridNeither", rule: chartdata.RuleEmpty, field: "y", build: newGrid(&chartdata.Grid{X: names("a")})},
		{name: "gridOneEdge", rule: chartdata.RuleShape, field: "x.edges", build: newGrid(&chartdata.Grid{X: edges(1), Y: names("r")})},
		{name: "gridEdgesUnsorted", rule: chartdata.RuleOrder, field: "y.edges[1]", build: newGrid(&chartdata.Grid{X: names("a"), Y: edges(1, 1)})},
		{name: "gridEdgeInfinite", rule: chartdata.RuleFinite, field: "x.edges[1]", build: newGrid(&chartdata.Grid{X: edges(0, inf), Y: names("r")})},
		{name: "gridNameTwice", rule: chartdata.RuleDuplicate, field: "x.names[1]", build: newGrid(&chartdata.Grid{X: names("a", "a"), Y: names("r")})},
		{name: "gridRowsShort", rule: chartdata.RuleLength, field: "values", build: newGrid(&chartdata.Grid{
			X: names("a"), Y: names("r", "s"),
			Values: [][]float64{{1}},
		})},
		{name: "gridRowShort", rule: chartdata.RuleLength, field: "values[0]", build: newGrid(&chartdata.Grid{
			X: edges(0, 1, 2), Y: names("r"),
			Values: [][]float64{{1}},
		})},
		{name: "gridCellInfinite", rule: chartdata.RuleFinite, field: "values[0][0]", build: newGrid(&chartdata.Grid{
			X: names("a"), Y: names("r"),
			Values: [][]float64{{inf}},
		})},
	})
}

func TestCalendarFrameRefusesABadZoneOrStart(t *testing.T) {
	t.Parallel()
	d := gauge("checks", measure.UnitCount)
	midnight := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newCalendar := func(zone string, start time.Time, values ...float64) func() error {
		return func() error {
			_, err := chartdata.NewCalendarFrame(d, zone, start, values)
			return err
		}
	}
	// A FixedZone may borrow an IANA name at another offset: its midnight is
	// not the named zone's.
	borrowed := func(zone string) time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone(zone, 0)) }
	runRefusals(t, []refusalCase{
		{name: "noZone", rule: chartdata.RuleShape, field: "zone", build: newCalendar("", midnight)},
		{name: "localZone", rule: chartdata.RuleShape, field: "zone", build: newCalendar("Local", midnight)},
		{name: "unknownZone", rule: chartdata.RuleShape, field: "zone", build: newCalendar("Mars/Olympus", midnight)},
		{name: "notMidnight", rule: chartdata.RuleShape, field: "start", build: newCalendar("UTC", midnight.Add(time.Hour))},
		{name: "borrowedParis", rule: chartdata.RuleShape, field: "start", build: newCalendar("Europe/Paris", borrowed("Europe/Paris"))},
		{name: "borrowedNewYork", rule: chartdata.RuleShape, field: "start", build: newCalendar("America/New_York", borrowed("America/New_York"))},
		{name: "valueInfinite", rule: chartdata.RuleFinite, field: "values[1]", build: newCalendar("UTC", midnight, 1, inf)},
	})
}

func TestTreeAndFlowFramesRefuseMalformedData(t *testing.T) {
	t.Parallel()
	d := gauge("energy", measure.UnitNone)
	newTree := func(parts ...chartdata.TreePart) func() error {
		return func() error {
			_, err := chartdata.NewTreeFrame(d, parts)
			return err
		}
	}
	newGroups := func(groups ...chartdata.TreeGroup) func() error {
		return func() error {
			_, err := chartdata.NewGroupedTreeFrame(d, groups)
			return err
		}
	}
	newFlow := func(f chartdata.Flow) func() error {
		return func() error {
			_, err := chartdata.NewFlowFrame(d, f)
			return err
		}
	}
	link := func(from, to string, v float64) chartdata.FlowLink {
		return chartdata.FlowLink{From: from, To: to, Value: v}
	}
	staged := func(from, to, stage string) chartdata.FlowLink {
		return chartdata.FlowLink{From: from, To: to, Stage: stage, Value: 1}
	}
	three := []string{"a", "b", "c"}
	part := chartdata.TreePart{Key: "p", Value: 1}
	runRefusals(t, []refusalCase{
		{name: "partNegative", rule: chartdata.RuleNegative, field: "parts[0].value", build: newTree(chartdata.TreePart{Key: "a", Value: -1})},
		{name: "partInfinite", rule: chartdata.RuleFinite, field: "parts[0].value", build: newTree(chartdata.TreePart{Key: "a", Value: inf})},
		{name: "partEmptyKey", rule: chartdata.RuleEmpty, field: "parts[1]", build: newTree(part, chartdata.TreePart{Value: 1})},
		{name: "partKeyTwice", rule: chartdata.RuleDuplicate, field: "parts[1]", build: newTree(part, part)},
		{name: "groupEmpty", rule: chartdata.RuleEmpty, field: "groups[0].children", build: newGroups(chartdata.TreeGroup{Key: "g"})},
		{name: "groupKeyTwice", rule: chartdata.RuleDuplicate, field: "groups[1]", build: newGroups(
			chartdata.TreeGroup{Key: "g", Children: []chartdata.TreePart{part}}, chartdata.TreeGroup{Key: "g", Children: []chartdata.TreePart{part}},
		)},
		{name: "childNegative", rule: chartdata.RuleNegative, field: "groups[0].children[0].value", build: newGroups(
			chartdata.TreeGroup{Key: "g", Children: []chartdata.TreePart{{Key: "p", Value: -2}}},
		)},
		{name: "oneStage", rule: chartdata.RuleShape, field: "stages", build: newFlow(chartdata.Flow{Stages: []string{"a"}})},
		{name: "stageTwice", rule: chartdata.RuleDuplicate, field: "stages[1]", build: newFlow(chartdata.Flow{Stages: []string{"a", "a"}})},
		{name: "linkEmptyEnd", rule: chartdata.RuleEmpty, field: "links[0]", build: newFlow(chartdata.Flow{Links: []chartdata.FlowLink{link("a", "", 1)}})},
		{name: "nodeTwice", rule: chartdata.RuleDuplicate, field: "nodes[1]", build: newFlow(chartdata.Flow{Nodes: []string{"a", "a"}})},
		{name: "linkToUnlistedNode", rule: chartdata.RuleShape, field: "links[0]", build: newFlow(chartdata.Flow{
			Nodes: []string{"a"}, Links: []chartdata.FlowLink{link("a", "b", 1)},
		})},
		{name: "listedNodeUnlinked", rule: chartdata.RuleShape, field: "nodes[2]", build: newFlow(chartdata.Flow{
			Nodes: []string{"a", "b", "c"}, Links: []chartdata.FlowLink{link("a", "b", 1)},
		})},
		{name: "stageWithoutStages", rule: chartdata.RuleShape, field: "links[0].stage", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{staged("a", "b", "x")},
		})},
		{name: "selfLink", rule: chartdata.RuleShape, field: "links[1]", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{link("a", "b", 1), link("b", "b", 1)},
		})},
		{name: "loop", rule: chartdata.RuleShape, field: "links", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{link("a", "b", 1), link("b", "c", 1), link("c", "a", 1), link("d", "a", 1)},
		})},
		{name: "stageMissing", rule: chartdata.RuleEmpty, field: "links[0].stage", build: newFlow(chartdata.Flow{
			Stages: three, Links: []chartdata.FlowLink{link("x", "y", 1)},
		})},
		{name: "stageUnlisted", rule: chartdata.RuleShape, field: "links[0].stage", build: newFlow(chartdata.Flow{
			Stages: three, Links: []chartdata.FlowLink{staged("x", "y", "d")},
		})},
		{name: "lastStage", rule: chartdata.RuleShape, field: "links[0].stage", build: newFlow(chartdata.Flow{
			Stages: three, Links: []chartdata.FlowLink{staged("x", "y", "c")},
		})},
		{name: "linkTwice", rule: chartdata.RuleDuplicate, field: "links[1]", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{link("a", "b", 1), link("a", "b", 2)},
		})},
		{name: "linkTwiceAtTheFirstOfTwoStages", rule: chartdata.RuleDuplicate, field: "links[1]", build: newFlow(chartdata.Flow{
			Stages: []string{"a", "b"}, Links: []chartdata.FlowLink{link("x", "y", 1), staged("x", "y", "a")},
		})},
		{name: "linkNegative", rule: chartdata.RuleNegative, field: "links[0].value", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{link("a", "b", -1)},
		})},
		{name: "linkInfinite", rule: chartdata.RuleFinite, field: "links[0].value", build: newFlow(chartdata.Flow{
			Links: []chartdata.FlowLink{link("a", "b", inf)},
		})},
	})
}

func TestEventsStatesAndValueFramesRefuseMalformedData(t *testing.T) {
	t.Parallel()
	d := gauge("restarts", measure.UnitCount)
	health := measure.NewStateSet("health", "h", []string{"health"}, []string{"up", "down"})
	at := func(ms ...int64) []time.Time {
		out := make([]time.Time, len(ms))
		for i, m := range ms {
			out[i] = time.UnixMilli(m)
		}
		return out
	}
	newMeters := func(meters ...chartdata.FrameMeter) func() error {
		return func() error {
			_, err := chartdata.NewValueFrame(d, meters)
			return err
		}
	}
	meter := chartdata.FrameMeter{Key: "en", Value: 0.5, Min: 0, Max: 1}
	runRefusals(t, []refusalCase{
		{name: "rowKeyTwice", rule: chartdata.RuleDuplicate, field: "rows[1].key", build: func() error {
			_, err := chartdata.NewEventsFrame(d, []chartdata.Times{{Key: "a"}, {Key: "a"}})
			return err
		}},
		{name: "eventsBackwards", rule: chartdata.RuleOrder, field: "rows[0].at[1]", build: func() error {
			_, err := chartdata.NewEventsFrame(d, []chartdata.Times{{Key: "a", At: at(2, 1)}})
			return err
		}},
		{name: "statesNotIncreasing", rule: chartdata.RuleOrder, field: "states[1].at", build: func() error {
			_, err := chartdata.NewStatesFrame(d, []chartdata.State{{At: time.UnixMilli(5), Key: "a"}, {At: time.UnixMilli(5), Key: "b"}})
			return err
		}},
		{name: "statesInOneMillisecond", rule: chartdata.RuleOrder, field: "states[1].at", build: func() error {
			_, err := chartdata.NewStatesFrame(d, []chartdata.State{{At: time.UnixMilli(5), Key: "a"}, {At: time.UnixMilli(5).Add(time.Microsecond), Key: "b"}})
			return err
		}},
		{name: "stateUndeclared", rule: chartdata.RuleShape, field: "states[1].key", build: func() error {
			_, err := chartdata.NewStatesFrame(health, []chartdata.State{{At: time.UnixMilli(1), Key: "up"}, {At: time.UnixMilli(2), Key: "sideways"}})
			return err
		}},
		{name: "meterKeyTwice", rule: chartdata.RuleDuplicate, field: "meters[1].key", build: newMeters(meter, meter)},
		{name: "meterMinInfinite", rule: chartdata.RuleFinite, field: "meters[0].min", build: newMeters(chartdata.FrameMeter{Key: "a", Min: -inf, Max: 1})},
		{name: "meterMaxNaN", rule: chartdata.RuleFinite, field: "meters[0].max", build: newMeters(chartdata.FrameMeter{Key: "a", Max: nan})},
		{name: "meterRangeEmpty", rule: chartdata.RuleOrder, field: "meters[0]", build: newMeters(chartdata.FrameMeter{Key: "a", Min: 1, Max: 1, Value: 1})},
		{name: "meterValueNaN", rule: chartdata.RuleFinite, field: "meters[0].value", build: newMeters(chartdata.FrameMeter{Key: "a", Max: 1, Value: nan})},
		{name: "meterValueOutside", rule: chartdata.RuleShape, field: "meters[0].value", build: newMeters(chartdata.FrameMeter{Key: "a", Max: 1, Value: 2})},
		{name: "meterHighOutside", rule: chartdata.RuleShape, field: "meters[0].high", build: newMeters(chartdata.FrameMeter{Key: "a", Max: 1, High: new(-1.0)})},
		{name: "meterOptimumNaN", rule: chartdata.RuleFinite, field: "meters[0].optimum", build: newMeters(chartdata.FrameMeter{Key: "a", Max: 1, Optimum: new(nan)})},
		{name: "meterLowAboveHigh", rule: chartdata.RuleOrder, field: "meters[0]", build: newMeters(chartdata.FrameMeter{Key: "a", Max: 1, Low: new(0.8), High: new(0.2)})},
	})
}
