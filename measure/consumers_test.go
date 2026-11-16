package measure

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

const consumerSeriesFile = "testdata/consumer-series.tsv"

// consumerSeries is one row of consumerSeriesFile: a series an app exports
// through measure v1 today.
type consumerSeries struct {
	app, path, prefix, name, kind, unit, source string
	labels                                      []string
}

func (s *consumerSeries) exported() string {
	if s.prefix == "" {
		return s.name
	}
	return s.prefix + "_" + s.name
}

// readConsumerSeries reads the rows and, from the header, the number of rows
// each app should have.
func readConsumerSeries(t *testing.T) (rows []consumerSeries, want map[string]int) {
	t.Helper()
	f, err := os.Open(consumerSeriesFile)
	if err != nil {
		t.Fatalf("open %s: %v", consumerSeriesFile, err)
	}
	defer f.Close()
	want = map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if head, ok := strings.CutPrefix(line, "# "); ok {
			if cols := strings.Split(head, "\t"); len(cols) == 3 {
				if n, err := strconv.Atoi(cols[2]); err == nil {
					want[cols[0]] = n
				}
			}
			continue
		}
		if line == "" || line == "#" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 8 {
			t.Fatalf("%s: row %q has %d columns, want 8", consumerSeriesFile, line, len(cols))
		}
		s := consumerSeries{app: cols[0], path: cols[1], prefix: cols[2], name: cols[3], kind: cols[4], unit: cols[5], source: cols[6]}
		if cols[7] != "" {
			s.labels = strings.Split(cols[7], ",")
		}
		rows = append(rows, s)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", consumerSeriesFile, err)
	}
	return rows, want
}

// declare builds s as the v2 declaration an app moving from v1 writes: the
// same name, kind, labels and source, with the unit its row names.
func (s *consumerSeries) declare(t *testing.T) Declared {
	t.Helper()
	unit := Unit(s.unit)
	switch {
	case s.source == "collector" && s.kind == "counter":
		return NewCounterDesc(s.name, "h", unit, s.labels)
	case s.source == "collector" && s.kind == "gauge":
		return NewGaugeDesc(s.name, "h", unit, s.labels)
	case s.source == "collector" && s.kind == "histogram":
		return NewHistogramDesc(s.name, "h", unit, s.labels, nil)
	case s.kind == "counter" && s.labels == nil:
		return NewCounter(s.name, "h", unit)
	case s.kind == "counter":
		return NewLabeledCounter(s.name, "h", unit, s.labels)
	case s.kind == "gauge" && s.labels == nil:
		return NewGauge(s.name, "h", unit)
	case s.kind == "gauge":
		return NewLabeledGauge(s.name, "h", unit, s.labels)
	case s.kind == "histogram" && s.labels == nil:
		return NewHistogram(s.name, "h", unit)
	case s.kind == "histogram":
		return NewLabeledHistogram(s.name, "h", unit, s.labels)
	}
	t.Fatalf("%s %s: no declaration for kind %q from source %q", s.app, s.name, s.kind, s.source)
	return nil
}

// TestEveryConsumerSeriesRegistersUnchanged is a release gate on every v2
// tag: each series an app exports through v1 today registers under v2 with
// its name, kind and labels, and is served under the name it has today.
func TestEveryConsumerSeriesRegistersUnchanged(t *testing.T) {
	rows, want := readConsumerSeries(t)
	byApp := map[string][]consumerSeries{}
	for _, s := range rows {
		byApp[s.app] = append(byApp[s.app], s)
	}
	if len(want) == 0 {
		t.Fatalf("%s names no app in its header", consumerSeriesFile)
	}
	for app, n := range want {
		if len(byApp[app]) != n {
			t.Errorf("%s has %d rows for %s, its header says %d", consumerSeriesFile, len(byApp[app]), app, n)
		}
	}
	for app, series := range byApp {
		t.Run(app, func(t *testing.T) {
			if _, ok := want[app]; !ok {
				t.Fatalf("%s has rows for %s but its header does not name the app", consumerSeriesFile, app)
			}
			r := NewRegistry(series[0].prefix)
			decls := make([]Declared, len(series))
			var descs []*Desc
			for i := range series {
				s := &series[i]
				decls[i] = s.declare(t)
				if d, ok := decls[i].(*Desc); ok {
					descs = append(descs, d)
					continue
				}
				if err := r.Register(decls[i].(Metric)); err != nil {
					t.Errorf("%s (%s): Register = %v", s.exported(), s.path, err)
				}
			}
			if len(descs) > 0 {
				if err := r.Register(NewCollector(func(context.Context, *Emitter) {}, descs...)); err != nil {
					t.Errorf("the collector's families: Register = %v", err)
				}
			}
			fams, err := r.Gather(t.Context())
			if err != nil {
				t.Fatalf("Gather: %v", err)
			}
			for i := range series {
				s := &series[i]
				if got := decls[i].Declaration().Name; got != s.exported() {
					t.Errorf("%s (%s) registered as %q", s.exported(), s.path, got)
				}
				if _, ok := Find(fams, s.exported()); !ok {
					t.Errorf("%s (%s) is not among the registry's families", s.exported(), s.path)
				}
			}
		})
	}
}
