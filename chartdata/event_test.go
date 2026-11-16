package chartdata_test

import (
	"testing"
	"time"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/measure/v2"
)

func TestEventsOnTheWire(t *testing.T) {
	t.Parallel()
	appendColumn, err := chartdata.AppendColumn("depth", 1_700_000_060_000, []float64{3, nan})
	if err != nil {
		t.Fatalf("AppendColumn: %v", err)
	}
	appendBin, err := chartdata.AppendBin("runs", chartdata.Bin{From: 10, To: 20}, []float64{1})
	if err != nil {
		t.Fatalf("AppendBin: %v", err)
	}
	replaceColumn, err := chartdata.ReplaceColumn("depth", []float64{4}, true)
	if err != nil {
		t.Fatalf("ReplaceColumn: %v", err)
	}
	replaceBin, err := chartdata.ReplaceBin("runs", chartdata.Bin{From: 10, To: 25}, []float64{2}, false)
	if err != nil {
		t.Fatalf("ReplaceBin: %v", err)
	}
	categories, err := chartdata.NewCategoriesFrame(gauge("runs", measure.UnitCount), []string{"ok"}, series("runs", 1))
	if err != nil {
		t.Fatalf("NewCategoriesFrame: %v", err)
	}
	set, err := chartdata.SetFrame("runs", &categories)
	if err != nil {
		t.Fatalf("SetFrame: %v", err)
	}
	for _, tc := range []struct {
		name  string
		event chartdata.FrameEvent
		want  string
	}{
		{"appendColumn", appendColumn, `{"x":1700000060000,"key":"depth","kind":"append","values":[3,null]}`},
		{"appendBin", appendBin, `{"x1":10,"x2":20,"key":"runs","kind":"append","values":[1]}`},
		{"replaceColumn", replaceColumn, `{"open":true,"key":"depth","kind":"replace","values":[4]}`},
		{"replaceBin", replaceBin, `{"x1":10,"x2":25,"open":false,"key":"runs","kind":"replace","values":[2]}`},
		{"reset", chartdata.ResetAt("depth", time.UnixMilli(1_700_000_000_000)), `{"at":1700000000000,"key":"depth","kind":"reset"}`},
		{"set", set, `{"categories":{"type":"categories","name":"runs","unit":"count","names":["ok"],` +
			`"series":[{"key":"runs","values":[1]}]},"key":"runs","kind":"set"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := encode(t, tc.event); got != tc.want {
				t.Errorf("JSON =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestSetFrameCarriesEachFrameTypeInItsField(t *testing.T) {
	t.Parallel()
	frames := []chartdata.Frame{
		&chartdata.SeriesFrame{}, &chartdata.BinsFrame{}, &chartdata.CategoriesFrame{}, &chartdata.BoxesFrame{},
		&chartdata.PointsFrame{}, &chartdata.GridFrame{}, &chartdata.CalendarFrame{}, &chartdata.TreeFrame{},
		&chartdata.FlowFrame{}, &chartdata.EventsFrame{}, &chartdata.StatesFrame{}, &chartdata.ValueFrame{},
	}
	for i, f := range frames {
		e, err := chartdata.SetFrame("k", f)
		if err != nil {
			t.Fatalf("SetFrame(%T): %v", f, err)
		}
		set := []bool{
			e.Series != nil, e.Bins != nil, e.Categories != nil, e.Boxes != nil, e.Points != nil, e.Grid != nil,
			e.Calendar != nil, e.Tree != nil, e.Flow != nil, e.Events != nil, e.States != nil, e.Value != nil,
		}
		for k, on := range set {
			if on != (k == i) {
				t.Errorf("SetFrame(%T) sets field %d = %v, want only field %d", f, k, on, i)
			}
		}
	}
}

func TestSetFrameCopiesTheFrame(t *testing.T) {
	t.Parallel()
	f, err := chartdata.NewValueFrame(gauge("coverage_ratio", measure.UnitRatio), []chartdata.FrameMeter{{Key: "en", Max: 1}})
	if err != nil {
		t.Fatalf("NewValueFrame: %v", err)
	}
	e, err := chartdata.SetFrame("coverage", &f)
	if err != nil {
		t.Fatalf("SetFrame: %v", err)
	}
	f.Name = "changed"
	if e.Value.Name != "coverage_ratio" {
		t.Errorf("the set event follows the frame it was built from: name %q", e.Value.Name)
	}
}

func TestEventConstructorsRefuseMalformedEvents(t *testing.T) {
	t.Parallel()
	var nilFrame *chartdata.SeriesFrame
	runRefusals(t, []refusalCase{
		{name: "appendXInfinite", rule: chartdata.RuleFinite, field: "x", build: func() error {
			_, err := chartdata.AppendColumn("k", inf, nil)
			return err
		}},
		{name: "appendValueInfinite", rule: chartdata.RuleFinite, field: "values[0]", build: func() error {
			_, err := chartdata.AppendColumn("k", 1, []float64{inf})
			return err
		}},
		{name: "appendBinBackwards", rule: chartdata.RuleBin, field: "bin", build: func() error {
			_, err := chartdata.AppendBin("k", chartdata.Bin{From: 2, To: 1}, nil)
			return err
		}},
		{name: "appendBinNaN", rule: chartdata.RuleFinite, field: "bin.from", build: func() error {
			_, err := chartdata.AppendBin("k", chartdata.Bin{From: nan, To: 1}, nil)
			return err
		}},
		{name: "replaceBinInfinite", rule: chartdata.RuleFinite, field: "bin.to", build: func() error {
			_, err := chartdata.ReplaceBin("k", chartdata.Bin{From: 1, To: inf}, nil, true)
			return err
		}},
		{name: "replaceValueTooLarge", rule: chartdata.RuleMagnitude, field: "values[1]", build: func() error {
			_, err := chartdata.ReplaceColumn("k", []float64{1, 1e301}, false)
			return err
		}},
		{name: "setNil", rule: chartdata.RuleEmpty, field: "frame", build: func() error {
			_, err := chartdata.SetFrame("k", nil)
			return err
		}},
		{name: "setTypedNil", rule: chartdata.RuleEmpty, field: "frame", build: func() error {
			_, err := chartdata.SetFrame("k", nilFrame)
			return err
		}},
	})
}
