package measure

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// staticCollector returns a collector that emits the same samples on every run.
func staticCollector(emit func(e *Emitter), descs ...*Desc) *Collector {
	return NewCollector(func(_ context.Context, e *Emitter) { emit(e) }, descs...)
}

func findFamily(t *testing.T, fams []Family, name string) Family {
	t.Helper()
	i := slices.IndexFunc(fams, func(f Family) bool { return f.Name == name })
	if i < 0 {
		t.Fatalf("Gather has no family %q", name)
	}
	return fams[i]
}

func TestCollectorServesItsFamiliesOnThePage(t *testing.T) {
	r := NewRegistry("app")
	queue := NewGaugeDesc("queue_depth", "Items waiting", []string{"queue"})
	done := NewCounterDesc("jobs_done_total", "Jobs finished", nil)
	r.MustRegister(staticCollector(func(e *Emitter) {
		e.Emit(queue, 3, "b")
		e.Emit(queue, 1.5, "a")
		e.Emit(done, 42)
	}, queue, done))

	out := body(t, r)
	want := "# HELP app_queue_depth Items waiting\n# TYPE app_queue_depth gauge\n" +
		"app_queue_depth{queue=\"a\"} 1.5\napp_queue_depth{queue=\"b\"} 3\n" +
		"# HELP app_jobs_done_total Jobs finished\n# TYPE app_jobs_done_total counter\napp_jobs_done_total 42\n"
	if !strings.Contains(out, want) {
		t.Errorf("page lacks the collector's families in order:\n%s\nwant:\n%s", out, want)
	}
	if strings.Index(out, "app_jobs_done_total") > strings.Index(out, pmGoroutines) {
		t.Error("collector families must come before the process families")
	}
}

func TestCollectorPageSortsLabelsByNameAndEscapesValues(t *testing.T) {
	r := NewRegistry("")
	d := NewGaugeDesc("g", "h", []string{"zone", "app"})
	r.MustRegister(staticCollector(func(e *Emitter) { e.Emit(d, 1, "a\"b\\c\nd", "x") }, d))
	if out := body(t, r); !strings.Contains(out, "g{app=\"x\",zone=\"a\\\"b\\\\c\\nd\"} 1\n") {
		t.Errorf("page = %q, want labels sorted by name and escaped", out)
	}
}

func TestCollectorTakesAnyNumberOfLabels(t *testing.T) {
	names := labelNames(21)
	vals := make([]string, len(names))
	for i := range vals {
		vals[i] = fmt.Sprintf("v%d", i)
	}
	r := NewRegistry("")
	d := NewGaugeDesc("plays", "Plays", names)
	r.MustRegister(staticCollector(func(e *Emitter) { e.Emit(d, 1, vals...) }, d))
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather = %v", err)
	}
	if f := findFamily(t, fams, "plays"); len(f.Samples) != 1 || len(f.Samples[0].Labels) != 21 {
		t.Fatalf("plays = %+v, want one sample with 21 labels", f)
	}
	if !strings.Contains(body(t, r), "plays{") {
		t.Error("the 21-label family is missing from the page")
	}
	mustRegisterError(t, NewRegistry(""), NewLabeledGauge("wide", "h", labelNames(9)), "at most 8 labels")
}

func TestCollectorConstructionErrorsSurfaceAtRegister(t *testing.T) {
	ok := func() *Desc { return NewGaugeDesc("ok", "h", nil) }
	shared := ok()
	NewCollector(func(context.Context, *Emitter) {}, shared)
	twice := ok()
	for name, tc := range map[string]struct {
		c    *Collector
		want string
	}{
		"bad name":      {NewCollector(func(context.Context, *Emitter) {}, NewGaugeDesc("1bad", "h", nil)), "invalid metric name"},
		"bad label":     {NewCollector(func(context.Context, *Emitter) {}, NewGaugeDesc("g", "h", []string{"__x"})), "reserved"},
		"dup label":     {NewCollector(func(context.Context, *Emitter) {}, NewCounterDesc("c", "h", []string{"a", "a"})), "duplicate label"},
		"no descs":      {NewCollector(func(context.Context, *Emitter) {}), "no descs"},
		"nil desc":      {NewCollector(func(context.Context, *Emitter) {}, nil), "nil desc"},
		"nil collect":   {NewCollector(nil, ok()), "nil collect"},
		"same name":     {NewCollector(func(context.Context, *Emitter) {}, ok(), ok()), "declares \"ok\" twice"},
		"shared desc":   {NewCollector(func(context.Context, *Emitter) {}, shared), "another collector"},
		"desc repeated": {NewCollector(func(context.Context, *Emitter) {}, twice, twice), "twice"},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry("")
			mustRegisterError(t, r, tc.c, tc.want)
			if len(r.collectors) != 0 || len(r.names) != len(processFamilyNames) {
				t.Error("a refused collector changed the registry")
			}
		})
	}
	mustRegisterError(t, NewRegistry("bad-prefix"), NewCollector(func(context.Context, *Emitter) {}, ok()), "invalid registry prefix")
}

func TestCollectorNamesCollideWithEveryOtherFamily(t *testing.T) {
	newC := func(name string) *Collector {
		return NewCollector(func(context.Context, *Emitter) {}, NewGaugeDesc("other", "h", nil), NewGaugeDesc(name, "h", nil))
	}
	t.Run("an instrument holds the name", func(t *testing.T) {
		r := NewRegistry("p")
		r.MustRegister(NewGauge("taken", "h"))
		mustRegisterError(t, r, newC("taken"), "collides")
		if _, held := r.names["p_other"]; held {
			t.Error("a refused collector kept the names it reserved before the collision")
		}
	})
	t.Run("a collector holds the name", func(t *testing.T) {
		r := NewRegistry("")
		r.MustRegister(NewCollector(func(context.Context, *Emitter) {}, NewGaugeDesc("taken", "h", nil)))
		mustRegisterError(t, r, NewCounter("taken", "h"), "collides")
		mustRegisterError(t, r, newC("taken"), "collides")
	})
	t.Run("a process family holds the name", func(t *testing.T) {
		mustRegisterError(t, NewRegistry(""), newC(pmGoroutines), "collides")
	})
	t.Run("registered twice", func(t *testing.T) {
		r := NewRegistry("")
		c := newC("x")
		r.MustRegister(c)
		mustRegisterError(t, r, c, "already registered")
		mustRegisterError(t, NewRegistry(""), c, "already registered")
	})
	t.Run("refused, then registered elsewhere", func(t *testing.T) {
		r := NewRegistry("")
		r.MustRegister(NewGauge("taken", "h"))
		c := newC("taken")
		mustRegisterError(t, r, c, "collides")
		if err := NewRegistry("").Register(c); err != nil {
			t.Fatalf("a collector refused for a collision must stay registrable: %v", err)
		}
	})
}

func TestCollectorDropsBadSamplesAndReportsThem(t *testing.T) {
	other := NewGaugeDesc("other", "h", nil)
	NewCollector(func(context.Context, *Emitter) {}, other)
	for name, tc := range map[string]struct {
		emit func(e *Emitter, g, c *Desc)
		want string
	}{
		"undeclared desc":  {func(e *Emitter, _, _ *Desc) { e.Emit(other, 1) }, "does not declare"},
		"nil desc":         {func(e *Emitter, _, _ *Desc) { e.Emit(nil, 1) }, "does not declare"},
		"label count":      {func(e *Emitter, g, _ *Desc) { e.Emit(g, 1, "a", "b") }, "wrong number of label values"},
		"duplicate labels": {func(e *Emitter, g, _ *Desc) { e.Emit(g, 1, "a"); e.Emit(g, 2, "a") }, "already emitted"},
		"negative counter": {func(e *Emitter, _, c *Desc) { e.Emit(c, -1) }, "negative or NaN"},
		"NaN counter":      {func(e *Emitter, _, c *Desc) { e.Emit(c, math.NaN()) }, "negative or NaN"},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureDebugLogs(t)
			g := NewGaugeDesc("g", "h", []string{"k"})
			c := NewCounterDesc("c_total", "h", nil)
			r := NewRegistry("")
			r.MustRegister(staticCollector(func(e *Emitter) { tc.emit(e, g, c) }, g, c))
			for range 3 {
				w := httptest.NewRecorder()
				r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if w.Code != http.StatusOK {
					t.Fatalf("scrape answered %d, want 200", w.Code)
				}
			}
			fams, err := r.Gather(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Gather error = %v, want one naming %q", err, tc.want)
			}
			if name == "duplicate labels" {
				if f := findFamily(t, fams, "g"); len(f.Samples) != 1 || f.Samples[0].Value != 1 {
					t.Errorf("g = %+v, want the first sample to stand", f.Samples)
				}
			}
			if n := strings.Count(logs.String(), "measure: collector problem\""); n != 1 {
				t.Errorf("%d warnings over four runs with the same problem, want 1:\n%s", n, logs)
			}
		})
	}
}

func TestCollectorLogsAProblemOnceAndItsClearingOnce(t *testing.T) {
	logs := captureDebugLogs(t)
	g := NewGaugeDesc("g", "h", []string{"k"})
	var mu sync.Mutex
	bad := true
	r := NewRegistry("")
	r.MustRegister(staticCollector(func(e *Emitter) {
		mu.Lock()
		defer mu.Unlock()
		if bad {
			e.Emit(g, 1)
		}
		e.Emit(g, 2, "a")
	}, g))
	run := func(n int) {
		for range n {
			body(t, r)
		}
	}
	run(3)
	mu.Lock()
	bad = false
	mu.Unlock()
	run(3)
	if w, i := strings.Count(logs.String(), "level=WARN msg=\"measure: collector problem\""), strings.Count(logs.String(), `level=INFO msg="measure: collector problem cleared"`); w != 1 || i != 1 {
		t.Errorf("got %d warnings and %d clearings over six scrapes, want 1 and 1:\n%s", w, i, logs)
	}
	if _, err := r.Gather(t.Context()); err != nil {
		t.Errorf("Gather after the problem cleared = %v, want nil", err)
	}
}

func TestCollectorWarnsOnceAtTheSeriesThreshold(t *testing.T) {
	logs := captureDebugLogs(t)
	g := NewGaugeDesc("wide", "h", []string{"k"})
	var mu sync.Mutex
	n := cardinalityWarnThreshold
	r := NewRegistry("")
	r.MustRegister(staticCollector(func(e *Emitter) {
		mu.Lock()
		defer mu.Unlock()
		for i := range n {
			e.Emit(g, 1, fmt.Sprint(i))
		}
	}, g))
	body(t, r)
	body(t, r)
	if _, err := r.Gather(t.Context()); err != nil {
		t.Errorf("Gather over the threshold = %v, want nil: nothing is dropped", err)
	}
	mu.Lock()
	n = 10
	mu.Unlock()
	body(t, r)
	got := logs.String()
	if w, c := strings.Count(got, "or more series"), strings.Count(got, `level=INFO msg="measure: collector problem cleared"`); w != 2 || c != 1 {
		t.Errorf("got %d threshold lines and %d clearings, want one warning, one clearing:\n%s", w, c, got)
	}
}

func TestCollectorSanitizesLabelValues(t *testing.T) {
	logs := captureDebugLogs(t)
	g := NewGaugeDesc("g", "h", []string{"k"})
	r := NewRegistry("")
	r.MustRegister(staticCollector(func(e *Emitter) { e.Emit(g, 1, "a\xffb") }, g))
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather = %v, want nil: a sanitized value is kept", err)
	}
	if v := findFamily(t, fams, "g").Samples[0].Labels[0].Value; v != "a\uFFFDb" {
		t.Errorf("label value = %q, want U+FFFD in place of the invalid byte", v)
	}
	if !strings.Contains(logs.String(), "invalid UTF-8") {
		t.Errorf("no warning for the sanitized value:\n%s", logs)
	}
}

func TestCollectorReceivesTheRequestContext(t *testing.T) {
	type key struct{}
	g := NewGaugeDesc("g", "h", nil)
	var mu sync.Mutex
	var seen []error
	var tagged []any
	r := NewRegistry("")
	r.MustRegister(NewCollector(func(ctx context.Context, e *Emitter) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, ctx.Err())
		tagged = append(tagged, ctx.Value(key{}))
		e.Emit(g, 1)
	}, g))

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "scrape"))
	cancel()
	r.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
	if _, err := r.Gather(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []error{context.Canceled, context.Canceled}) || !slices.Equal(tagged, []any{"scrape", "scrape"}) {
		t.Errorf("collect saw errors %v and values %v, want the caller's cancelled context twice", seen, tagged)
	}
}

func TestEmitAfterCollectReturnedRecordsNothing(t *testing.T) {
	g := NewGaugeDesc("g", "h", nil)
	var kept *Emitter
	r := NewRegistry("")
	r.MustRegister(NewCollector(func(_ context.Context, e *Emitter) { kept = e }, g))
	if _, err := r.Gather(t.Context()); err != nil {
		t.Fatal(err)
	}
	kept.Emit(g, 7)
	if len(kept.samples) != 0 || len(kept.problems) != 0 {
		t.Errorf("a late Emit recorded %v / %v, want nothing", kept.samples, kept.problems)
	}
}

func TestGatherListsDeclaredFamiliesWithNoSamples(t *testing.T) {
	r := NewRegistry("")
	d := NewGaugeDesc("alerts_active", "h", []string{"kind", "id"})
	r.MustRegister(
		NewCollector(func(context.Context, *Emitter) {}, d),
		NewLabeledCounter("errors_total", "h", []string{"kind"}),
		NewLabeledGauge("lg", "h", []string{"k"}),
		NewLabeledHistogram("lh", "h", []string{"k"}),
	)
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alerts_active", "errors_total", "lg", "lh"} {
		if f := findFamily(t, fams, name); len(f.Samples) != 0 {
			t.Errorf("%s = %+v, want a declared family with no samples", name, f)
		}
	}
	out := body(t, r)
	for _, name := range []string{"alerts_active", "errors_total", "lg", "lh"} {
		if strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("the page lists the empty family %s", name)
		}
	}
}

func TestGatherReadsEveryInstrumentAsStructuredValues(t *testing.T) {
	r := NewRegistry("p")
	c := NewCounter("c_total", "C")
	g := NewGauge("g", "G")
	lc := NewLabeledCounter("lc_total", "LC", []string{"b", "a"})
	lg := NewLabeledGauge("lg", "LG", []string{"k"})
	h := NewHistogram("h_seconds", "H", WithBuckets([]float64{0.5, 1}))
	lh := NewLabeledHistogram("lh_seconds", "LH", []string{"k"}, WithBuckets([]float64{2}))
	r.MustRegister(c, g, lc, lg, h, lh)
	c.Add(5)
	g.Set(-2.5)
	lc.Add(3, "y", "x")
	lc.Add(1, "x", "z")
	lg.Set(9, "k1")
	h.Observe(0.2)
	h.Observe(0.7)
	h.Observe(3)
	lh.Observe(1, "q")

	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Family{
		"p_c_total": {Name: "p_c_total", Help: "C", Type: TypeCounter, Samples: []Sample{{Labels: []Label{}, Value: 5}}},
		"p_g":       {Name: "p_g", Help: "G", Type: TypeGauge, Samples: []Sample{{Labels: []Label{}, Value: -2.5}}},
		"p_lc_total": {Name: "p_lc_total", Help: "LC", Type: TypeCounter, Samples: []Sample{
			{Labels: []Label{{"b", "x"}, {"a", "z"}}, Value: 1},
			{Labels: []Label{{"b", "y"}, {"a", "x"}}, Value: 3},
		}},
		"p_lg": {Name: "p_lg", Help: "LG", Type: TypeGauge, Samples: []Sample{{Labels: []Label{{"k", "k1"}}, Value: 9}}},
		"p_h_seconds": {Name: "p_h_seconds", Help: "H", Type: TypeHistogram, Samples: []Sample{{Labels: []Label{}, Histogram: &HistogramValue{
			Buckets: []Bucket{{0.5, 1}, {1, 2}}, Count: 3, Sum: 3.9,
		}}}},
		"p_lh_seconds": {Name: "p_lh_seconds", Help: "LH", Type: TypeHistogram, Samples: []Sample{{Labels: []Label{{"k", "q"}}, Histogram: &HistogramValue{
			Buckets: []Bucket{{2, 1}}, Count: 1, Sum: 1,
		}}}},
	}
	for name, w := range want {
		if got := findFamily(t, fams, name); !familyEqual(got, w) {
			t.Errorf("%s = %s, want %s", name, describeFamily(got), describeFamily(w))
		}
	}
}

func TestGatherListsTheSameProcessFamiliesAsThePage(t *testing.T) {
	r := NewRegistry("")
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var gathered []string
	for _, f := range fams {
		gathered = append(gathered, string(f.Type)+" "+f.Name)
	}
	var paged []string
	for line := range strings.SplitSeq(body(t, r), "\n") {
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, typ, _ := strings.Cut(rest, " ")
			paged = append(paged, typ+" "+name)
		}
	}
	if !slices.Equal(gathered, paged) {
		t.Errorf("Gather lists %v, the page %v", gathered, paged)
	}
}

func TestGatherJoinsEveryCollectorsDrops(t *testing.T) {
	a := NewGaugeDesc("a", "h", []string{"k"})
	b := NewGaugeDesc("b", "h", nil)
	r := NewRegistry("")
	r.MustRegister(
		staticCollector(func(e *Emitter) { e.Emit(a, 1) }, a),
		staticCollector(func(e *Emitter) { e.Emit(b, 1, "extra") }, b),
	)
	_, err := r.Gather(t.Context())
	if err == nil || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("Gather = %v, want an error naming both families", err)
	}
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) || len(joined.Unwrap()) != 2 {
		t.Errorf("Gather error %v, want one error per collector", err)
	}
}

func TestCollectorRaceWithScrapesAndRecording(t *testing.T) {
	g := NewGaugeDesc("g", "h", []string{"k"})
	lc := NewLabeledCounter("lc_total", "h", []string{"k"})
	r := NewRegistry("")
	r.MustRegister(lc, NewCollector(func(_ context.Context, e *Emitter) {
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Go(func() { e.Emit(g, float64(i), fmt.Sprint(i)) })
		}
		wg.Wait()
	}, g))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				lc.Inc(fmt.Sprint(j % 5))
				if i%2 == 0 {
					body(t, r)
				} else if _, err := r.Gather(t.Context()); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

func familyEqual(a, b Family) bool {
	return a.Name == b.Name && a.Help == b.Help && a.Type == b.Type &&
		slices.EqualFunc(a.Samples, b.Samples, func(x, y Sample) bool {
			return slices.Equal(x.Labels, y.Labels) && x.Value == y.Value &&
				histogramEqual(x.Histogram, y.Histogram) && x.Summary == nil && y.Summary == nil
		})
}

func histogramEqual(a, b *HistogramValue) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Buckets, b.Buckets) && a.Count == b.Count && math.Abs(a.Sum-b.Sum) < 1e-9
}

func describeFamily(f Family) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "{%s %q %s", f.Name, f.Help, f.Type)
	for _, s := range f.Samples {
		fmt.Fprintf(&sb, " %v=%v", s.Labels, s.Value)
		if s.Histogram != nil {
			fmt.Fprintf(&sb, "%+v", *s.Histogram)
		}
	}
	return sb.String() + "}"
}
