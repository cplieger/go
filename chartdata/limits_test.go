package chartdata_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

func TestTheLimitsAreTheChartsCeilings(t *testing.T) {
	t.Parallel()
	// @cplieger/ui src/data/chart/model.ts MOST_VALUES, MOST_ROWS and
	// MOST_SUB_ROWS (40,000 px over 16 px rows), and LIMIT in its numbers.
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"MaxValues", chartdata.MaxValues, 1_000_000},
		{"MaxRows", chartdata.MaxRows, 1_000},
		{"MaxSubRows", chartdata.MaxSubRows, 2_500},
		{"MaxMagnitude", chartdata.MaxMagnitude, 1e300},
		{"MaxCalendarDays", chartdata.MaxCalendarDays, 6_994},
		{"MinEvery", float64(chartdata.MinEvery), float64(time.Second)},
		{"MaxPoints", chartdata.MaxPoints, 10_000},
		{"MaxLabelSets", chartdata.MaxLabelSets, 1_000},
		{"MaxBaselines", chartdata.MaxBaselines, 4_000},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// ui src/data/chart/calendar.ts lays a calendar out in ceil((lead + days)
	// / 7) weeks, lead the first day's place in the page's week, 0 to 6, and
	// refuses more than MOST_ROWS weeks.
	weeks := func(lead, days int) int { return (lead + days + 6) / 7 }
	for lead := range 7 {
		if w := weeks(lead, chartdata.MaxCalendarDays); w > chartdata.MaxRows {
			t.Errorf("MaxCalendarDays from a first day %d into its week takes %d weeks, past %d", lead, w, chartdata.MaxRows)
		}
	}
	if w := weeks(6, chartdata.MaxCalendarDays+1); w <= chartdata.MaxRows {
		t.Errorf("one day past MaxCalendarDays takes %d weeks at most, so the cap is below what every week start fits", w)
	}
}

func keys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "k" + strconv.Itoa(i)
	}
	return out
}

func nSeries(n int) []chartdata.Series {
	out := make([]chartdata.Series, n)
	for i := range out {
		out[i].Key = "s" + strconv.Itoa(i)
	}
	return out
}

func TestConstructorsRefuseAFramePastTheLimits(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	x := make([]float64, 1_001)
	for i := range x {
		x[i] = float64(i)
	}
	notes := make([]chartdata.FrameNote, 2)
	runRefusals(t, []refusalCase{
		// 1,000 series of 1,001 values is 1,001,000, checked before a value is read.
		{name: "seriesValues", rule: chartdata.RuleLimit, field: "frame", build: func() error {
			_, err := chartdata.NewSeriesFrame(d, x, nSeries(1_000))
			return err
		}},
		// No series charges each x once: 999,999 x and two notes pass the ceiling.
		{name: "refsAndNotesCount", rule: chartdata.RuleLimit, field: "refs", build: func() error {
			_, err := chartdata.NewSeriesFrame(d, make([]float64, 999_999), nil, chartdata.WithNotes(notes...))
			return err
		}},
		{name: "categoryNames", rule: chartdata.RuleLimit, field: "names", build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, keys(1_001), nil)
			return err
		}},
		{name: "slopeSeries", rule: chartdata.RuleLimit, field: "series", build: func() error {
			_, err := chartdata.NewCategoriesFrame(d, []string{"before", "after"}, nSeries(1_001))
			return err
		}},
		{name: "boxNames", rule: chartdata.RuleLimit, field: "names", build: func() error {
			_, err := chartdata.NewBoxesFrame(d, keys(1_001), nil)
			return err
		}},
		{name: "boxSubRows", rule: chartdata.RuleLimit, field: "series", build: func() error {
			_, err := chartdata.NewBoxesFrame(d, keys(501), make([]chartdata.Boxes, 5))
			return err
		}},
		{name: "gridRows", rule: chartdata.RuleLimit, field: "y", build: func() error {
			_, err := chartdata.NewGridFrame(d, &chartdata.Grid{
				X: chartdata.FrameGridAxis{Names: []string{"a"}}, Y: chartdata.FrameGridAxis{Names: keys(1_001)},
			})
			return err
		}},
		{name: "calendarDays", rule: chartdata.RuleLimit, field: "values", build: func() error {
			_, err := chartdata.NewCalendarFrame(d, "UTC", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), make([]float64, 6_995))
			return err
		}},
		// An oversized axis is charged from its length, before its edges are
		// read: these are out of order too.
		{name: "gridEdgesCharged", rule: chartdata.RuleLimit, field: "frame", build: func() error {
			e := make([]float64, 1_000_002)
			_, err := chartdata.NewGridFrame(d, &chartdata.Grid{
				X: chartdata.FrameGridAxis{Edges: e}, Y: chartdata.FrameGridAxis{Names: []string{"r"}},
			})
			return err
		}},
		{name: "eventRows", rule: chartdata.RuleLimit, field: "rows", build: func() error {
			_, err := chartdata.NewEventsFrame(d, make([]chartdata.Times, 1_001))
			return err
		}},
		{name: "treeParts", rule: chartdata.RuleLimit, field: "frame", build: func() error {
			_, err := chartdata.NewTreeFrame(d, make([]chartdata.TreePart, 1_000_001))
			return err
		}},
		{name: "flowLinks", rule: chartdata.RuleLimit, field: "frame", build: func() error {
			_, err := chartdata.NewFlowFrame(d, chartdata.Flow{Nodes: keys(2), Links: make([]chartdata.FlowLink, 999_999)})
			return err
		}},
		{name: "states", rule: chartdata.RuleLimit, field: "frame", build: func() error {
			_, err := chartdata.NewStatesFrame(d, make([]chartdata.State, 1_000_001))
			return err
		}},
	})
}

func TestConstructorsTakeAFrameAtTheLimits(t *testing.T) {
	t.Parallel()
	d := gauge("depth", measure.UnitCount)
	x := make([]float64, 1_000)
	values := make([]float64, 1_000)
	for i := range x {
		x[i] = float64(i)
	}
	s := nSeries(1_000)
	for i := range s {
		s[i].Values = values
	}
	if _, err := chartdata.NewSeriesFrame(d, x, s); err != nil {
		t.Errorf("1,000 series of 1,000 values: %v", err)
	}
	if _, err := chartdata.NewCategoriesFrame(d, keys(1_000), nil); err != nil {
		t.Errorf("1,000 names: %v", err)
	}
	if _, err := chartdata.NewCalendarFrame(d, "UTC", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), make([]float64, 6_994)); err != nil {
		t.Errorf("6,994 days: %v", err)
	}
}
