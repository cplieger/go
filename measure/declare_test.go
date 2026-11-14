package measure

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The refusals and acceptances below follow the declaration rules: a closed
// unit list, a name suffix that may not contradict the kind or unit, and an
// unbounded label only on a series that never reaches the page.

func TestRegisterRefusesAContradictoryDeclaration(t *testing.T) {
	_ = captureDebugLogs(t)
	chart := WithExport(ExportChartdata)
	cases := []struct {
		name string
		m    Metric
		want string
	}{
		{"noUnit", NewGauge("depth", "h", ""), "declares no unit"},
		{"unitOffTheList", NewGauge("temperature", "h", Unit("celsius")), `unit "celsius", which is not`},
		{"secondsInBytes", NewGauge("wait_seconds", "h", UnitBytes), `ends in "_seconds" but declares unit "bytes"`},
		{"secondsCounterInCount", NewCounter("cpu_seconds_total", "h", UnitCount), `ends in "_seconds" but declares unit "count"`},
		{"secondsHistogramInNone", NewHistogram("latency_seconds", "h", UnitNone), `ends in "_seconds"`},
		{"bytesInCount", NewLabeledGauge("heap_bytes", "h", UnitCount, []string{"pool"}), `ends in "_bytes" but declares unit "count"`},
		{"bytesCounterInNone", NewLabeledCounter("rx_bytes_total", "h", UnitNone, []string{"if"}), `ends in "_bytes"`},
		{"ratioInNone", NewGauge("hit_ratio", "h", UnitNone), `ends in "_ratio" but declares unit "none"`},
		{"timestampInCount", NewGauge("last_run_timestamp_seconds", "h", UnitCount), `"_timestamp_seconds" but declares unit "count"; a timestamp is in seconds`},
		{"totalOnGauge", NewGauge("jobs_total", "h", UnitCount), `ends in "_total" but declares kind "gauge"`},
		{"totalOnLabeledGauge", NewLabeledGauge("jobs_total", "h", UnitCount, []string{"q"}), `ends in "_total" but declares kind "gauge"`},
		{"totalOnHistogram", NewHistogram("sizes_total", "h", UnitNone), `ends in "_total" but declares kind "histogram"`},
		{"totalOnStateSet", NewStateSet("mode_total", "h", []string{"mode"}, []string{"a"}), `ends in "_total" but declares kind "stateset"`},
		{"totalOnInfo", NewInfo("build_total", "h", []string{"version"}), `ends in "_total" but declares kind "info"`},
		{"totalOnGaugeDesc", NewCollector(func(context.Context, *Emitter) {}, NewGaugeDesc("rows_total", "h", UnitCount, nil)), `ends in "_total" but declares kind "gauge"`},
		{"secondsOnHistogramDesc", NewCollector(func(context.Context, *Emitter) {},
			NewHistogramDesc("wait_seconds", "h", UnitCount, nil, nil)), `ends in "_seconds"`},
		{
			"unboundedOnMetric", NewLabeledCounter("turns_total", "h", UnitCount, []string{"agent"}, WithUnbounded("agent")),
			`unbounded label "agent" and is served on /metrics (export metric)`,
		},
		{
			"unboundedOnBoth", NewLabeledGauge("depth", "h", UnitCount, []string{"path"}, WithUnbounded("path"), WithExport(ExportBoth)),
			"(export both)",
		},
		{"unboundedOnMetricDesc", NewCollector(func(context.Context, *Emitter) {},
			NewGaugeDesc("depth", "h", UnitCount, []string{"path"}, WithUnbounded("path"))), "is served on /metrics"},
		{
			"unboundedNotALabel", NewLabeledGauge("depth", "h", UnitCount, []string{"queue"}, WithUnbounded("path"), chart),
			`unbounded label "path", which is not one of its labels`,
		},
		{"unboundedOnUnlabeled", NewGauge("depth", "h", UnitCount, WithUnbounded("path"), chart), "which is not one of its labels"},
		{"exportOffTheList", NewGauge("depth", "h", UnitCount, WithExport(Export(7))), "export Export(7), which is not"},
		{"bucketsOnCounter", NewCounter("events_total", "h", UnitCount, WithBuckets([]float64{1})), "WithBuckets on metric \"events_total\""},
		{"bucketsOnGauge", NewLabeledGauge("depth", "h", UnitCount, []string{"q"}, WithBuckets(nil)), "not a histogram instrument"},
		{"bucketsOnHistogramDesc", NewCollector(func(context.Context, *Emitter) {},
			NewHistogramDesc("wait_seconds", "h", UnitSeconds, nil, nil, WithBuckets([]float64{1}))), "not a histogram instrument"},
		{"seedOnCounter", NewCounter("events_total", "h", UnitCount, WithSeed([]string{})), "WithSeed on metric \"events_total\", which takes no seed"},
		{"seedOnGauge", NewGauge("depth", "h", UnitCount, WithSeed()), "takes no seed"},
		{"seedOnHistogram", NewHistogram("wait_seconds", "h", UnitSeconds, WithSeed()), "takes no seed"},
		{"seedOnInfo", NewInfo("build_info", "h", []string{"version"}, WithSeed([]string{"1"})), "takes no seed"},
		{"seedOnReadSeries", NewCollector(func(context.Context, *Emitter) {},
			NewGaugeDesc("problem_active", "h", UnitNone, []string{"component"}, WithSeed([]string{"db"}))), "takes no seed"},
		{
			"seedArity", NewLabeledCounter("errors_total", "h", UnitCount, []string{"op", "code"}, WithSeed([]string{"get"})),
			"gives 1 label values, want 2",
		},
		{
			"stateSeedArity", NewStateSet("health", "h", []string{"provider", "health"}, []string{"up"}, WithSeed([]string{"a", "up"})),
			"gives 2 label values, want 1",
		},
		{"stateSetNoLabel", NewStateSet("mode", "h", nil, []string{"a"}), "has no state label"},
		{"stateSetNoStates", NewStateSet("mode", "h", []string{"mode"}, nil), "declares no states"},
		{"stateSetRepeatedState", NewStateSet("mode", "h", []string{"mode"}, []string{"a", "b", "a"}), `state "a" twice`},
		{"stateSetInvalidState", NewStateSet("mode", "h", []string{"mode"}, []string{"a\xff"}), "not valid UTF-8"},
		{"stateSetDescNoStates", NewCollector(func(context.Context, *Emitter) {},
			NewStateSetDesc("mode", "h", []string{"mode"}, nil)), "declares no states"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustRegisterError(t, NewRegistry("app"), tc.m, tc.want)
		})
	}
}

func TestRegisterAcceptsADeclarationItsNameDoesNotContradict(t *testing.T) {
	chart := WithExport(ExportChartdata)
	collect := func(context.Context, *Emitter) {}
	cases := []struct {
		name string
		m    Metric
	}{
		{"everyUnitOnAPlainName", NewCollector(collect,
			NewGaugeDesc("a", "h", UnitSeconds, nil), NewGaugeDesc("b", "h", UnitBytes, nil),
			NewGaugeDesc("c", "h", UnitRatio, nil), NewGaugeDesc("d", "h", UnitCount, nil), NewGaugeDesc("e", "h", UnitNone, nil))},
		{"secondsHistogram", NewHistogram("request_duration_seconds", "h", UnitSeconds)},
		{"secondsCounter", NewCounter("process_cpu_seconds_total", "h", UnitSeconds)},
		{"bytesCounter", NewLabeledCounter("rx_bytes_total", "h", UnitBytes, []string{"if"})},
		{"ratioGauge", NewGauge("cache_hit_ratio", "h", UnitRatio)},
		{"timestampGauge", NewLabeledGauge("last_success_timestamp_seconds", "h", UnitSeconds, []string{"job"})},
		{"countGaugeWithNoSuffix", NewGauge("jobs_queued", "h", UnitCount)},
		{"millisecondsAsNone", NewCollector(collect, NewGaugeDesc("plex_library_duration_milliseconds", "h", UnitNone, []string{"library"}))},
		{"counterWithoutTotal", NewCounter("events", "h", UnitCount)},
		{"gaugeEndingInCount", NewGauge("pool_count", "h", UnitCount)},
		{"gaugeEndingInInfo", NewLabeledGauge("tls_info", "h", UnitNone, []string{"version"})},
		{"secondsCounterDesc", NewCollector(collect, NewCounterDesc("busy_seconds_total", "h", UnitSeconds, []string{"cpu"}))},
		{"unboundedOnChartdataCounter", NewLabeledCounter("turns_total", "h", UnitCount, []string{"agent"}, WithUnbounded("agent"), chart)},
		{"unboundedOnChartdataDesc", NewCollector(collect,
			NewGaugeDesc("thread_posts", "h", UnitCount, []string{"thread", "author"}, WithUnbounded("thread", "author"), chart))},
		{"chartdataWithNoUnbounded", NewGauge("queue_depth", "h", UnitCount, chart)},
		{"both", NewHistogram("wait_seconds", "h", UnitSeconds, WithExport(ExportBoth))},
		{"seededCounter", NewLabeledCounter("errors_total", "h", UnitCount, []string{"op"}, WithSeed([]string{"get"}, []string{"put"}))},
		{"seededLabelFreeGauge", NewLabeledGauge("engine_up", "h", UnitNone, nil, WithSeed([]string{}))},
		{"seededHistogram", NewLabeledHistogram("op_seconds", "h", UnitSeconds, []string{"op"}, WithSeed([]string{"get"}))},
		{"stateSet", NewStateSet("config_mode", "h", []string{"mode"}, []string{"live", "dry_run"})},
		{"seededStateSet", NewStateSet("provider_health", "h", []string{"provider", "health"}, []string{"ok", "down"},
			WithSeed([]string{"a"}))},
		{"info", NewInfo("build_info", "h", []string{"version"})},
		{"stateSetAndInfoDescs", NewCollector(collect,
			NewStateSetDesc("health", "h", []string{"component", "health"}, []string{"ok", "down"}),
			NewInfoDesc("store_info", "h", []string{"engine"}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := NewRegistry("app").Register(tc.m); err != nil {
				t.Fatalf("Register = %v, want nil", err)
			}
		})
	}
}

func TestDeclarationCarriesWhatWasDeclared(t *testing.T) {
	lh := NewLabeledHistogram("op_seconds", "Op latency", UnitSeconds, []string{"op", "path"},
		WithBuckets([]float64{0.1, 1}), WithUnbounded("path"), WithExport(ExportChartdata))
	want := Declaration{
		Name: "op_seconds", Help: "Op latency", Kind: KindHistogram, Unit: UnitSeconds,
		Labels: []string{"op", "path"}, Unbounded: []string{"path"}, Buckets: []float64{0.1, 1},
		Export: ExportChartdata, Source: SourceRecorded,
	}
	checkDeclaration := func(t *testing.T, got, want Declaration) {
		t.Helper()
		if got.Name != want.Name || got.Help != want.Help || got.Kind != want.Kind || got.Unit != want.Unit ||
			got.Export != want.Export || got.Source != want.Source {
			t.Errorf("Declaration() = %+v, want %+v", got, want)
		}
		if !slices.Equal(got.Labels, want.Labels) || !slices.Equal(got.Unbounded, want.Unbounded) ||
			!slices.Equal(got.Buckets, want.Buckets) || !slices.Equal(got.States, want.States) {
			t.Errorf("Declaration() lists = %+v, want %+v", got, want)
		}
	}

	t.Run("beforeRegistration", func(t *testing.T) {
		checkDeclaration(t, lh.Declaration(), want)
	})
	t.Run("afterRegistrationNamesThePrefix", func(t *testing.T) {
		NewRegistry("app").MustRegister(lh)
		w := want
		w.Name = "app_op_seconds"
		checkDeclaration(t, lh.Declaration(), w)
	})
	t.Run("returnsFreshCopies", func(t *testing.T) {
		d := lh.Declaration()
		d.Labels[0], d.Unbounded[0], d.Buckets[0] = "x", "x", 99
		w := want
		w.Name = "app_op_seconds"
		checkDeclaration(t, lh.Declaration(), w)
	})
	t.Run("aReadSeries", func(t *testing.T) {
		d := NewStateSetDesc("health", "Health", []string{"component", "health"}, []string{"ok", "down"})
		NewRegistry("").MustRegister(NewCollector(func(context.Context, *Emitter) {}, d))
		checkDeclaration(t, d.Declaration(), Declaration{
			Name: "health", Help: "Health", Kind: KindStateSet, Unit: UnitNone,
			Labels: []string{"component", "health"}, States: []string{"ok", "down"}, Source: SourceRead,
		})
	})
	t.Run("eachKind", func(t *testing.T) {
		for _, tc := range []struct {
			d    Declared
			kind Kind
			unit Unit
		}{
			{NewCounter("a_total", "h", UnitCount), KindCounter, UnitCount},
			{NewLabeledCounter("b_total", "h", UnitBytes, []string{"x"}), KindCounter, UnitBytes},
			{NewGauge("c", "h", UnitRatio), KindGauge, UnitRatio},
			{NewLabeledGauge("d", "h", UnitCount, nil), KindGauge, UnitCount},
			{NewHistogram("e", "h", UnitBytes), KindHistogram, UnitBytes},
			{NewStateSet("f", "h", []string{"s"}, []string{"a"}), KindStateSet, UnitNone},
			{NewInfo("g_info", "h", []string{"v"}), KindInfo, UnitNone},
			{NewCounterDesc("h_total", "h", UnitSeconds, nil), KindCounter, UnitSeconds},
			{NewHistogramDesc("i_seconds", "h", UnitSeconds, nil, []float64{1}), KindHistogram, UnitSeconds},
			{NewInfoDesc("j_info", "h", []string{"v"}), KindInfo, UnitNone},
		} {
			got := tc.d.Declaration()
			if got.Kind != tc.kind || got.Unit != tc.unit {
				t.Errorf("%s: Kind, Unit = %q, %q, want %q, %q", got.Name, got.Kind, got.Unit, tc.kind, tc.unit)
			}
		}
	})
}

func TestExportDecidesWhatThePageServes(t *testing.T) {
	r := NewRegistry("app")
	metric := NewGauge("metric_only", "h", UnitCount)
	chart := NewLabeledCounter("chart_only_total", "h", UnitCount, []string{"agent"},
		WithExport(ExportChartdata), WithUnbounded("agent"))
	both := NewGauge("both", "h", UnitCount, WithExport(ExportBoth))
	read := NewGaugeDesc("read_chart_only", "h", UnitCount, nil, WithExport(ExportChartdata))
	readBoth := NewGaugeDesc("read_both", "h", UnitCount, nil, WithExport(ExportBoth))
	r.MustRegister(metric, chart, both, NewCollector(func(_ context.Context, e *Emitter) {
		e.Emit(read, 1)
		e.Emit(readBoth, 2)
	}, read, readBoth))
	chart.Inc("someone")

	page := body(t, r)
	for _, name := range []string{"app_metric_only", "app_both", "app_read_both"} {
		if !strings.Contains(page, "# TYPE "+name+" ") {
			t.Errorf("page has no %s family:\n%s", name, page)
		}
	}
	for _, name := range []string{"app_chart_only_total", "app_read_chart_only"} {
		if strings.Contains(page, name) {
			t.Errorf("page serves %s, an ExportChartdata family:\n%s", name, page)
		}
	}

	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, name := range []string{"app_metric_only", "app_chart_only_total", "app_both", "app_read_chart_only", "app_read_both"} {
		if _, ok := Find(fams, name); !ok {
			t.Errorf("Gather has no %s family", name)
		}
	}
	f, _ := Find(fams, "app_chart_only_total")
	if s, ok := f.Sample("someone"); !ok || s.Value != 1 {
		t.Errorf("Gather's app_chart_only_total{agent=someone} = %v, %v, want 1, true", s.Value, ok)
	}
}

func TestGatherOfReadsOnlyTheNamedDeclarations(t *testing.T) {
	r := NewRegistry("app")
	reqs := NewLabeledCounter("requests_total", "h", UnitCount, []string{"code"})
	depth := NewGauge("depth", "h", UnitCount)
	rows := NewGaugeDesc("rows", "h", UnitCount, []string{"table"})
	size := NewGaugeDesc("size_bytes", "h", UnitBytes, nil, WithExport(ExportChartdata))
	other := NewGaugeDesc("other", "h", UnitCount, nil)
	var storeReads, otherReads int
	r.MustRegister(reqs, depth,
		NewCollector(func(_ context.Context, e *Emitter) {
			storeReads++
			e.Emit(rows, 3, "posts")
			e.Emit(size, 4096)
		}, rows, size),
		NewCollector(func(_ context.Context, e *Emitter) {
			otherReads++
			e.Emit(other, 1)
		}, other))
	reqs.Add(2, "200")
	depth.Set(5)

	fams, err := r.GatherOf(t.Context(), size, reqs, rows, depth)
	if err != nil {
		t.Fatalf("GatherOf: %v", err)
	}
	names := make([]string, len(fams))
	for i, f := range fams {
		names[i] = f.Name
	}
	if want := []string{"app_size_bytes", "app_requests_total", "app_rows", "app_depth"}; !slices.Equal(names, want) {
		t.Fatalf("GatherOf families = %v, want %v, in the order asked", names, want)
	}
	if storeReads != 1 {
		t.Errorf("the store collector ran %d times, want once for its two declarations", storeReads)
	}
	if otherReads != 0 {
		t.Errorf("a collector serving no named declaration ran %d times, want 0", otherReads)
	}
	for _, f := range fams {
		if strings.HasPrefix(f.Name, "go_") || strings.HasPrefix(f.Name, "process_") {
			t.Errorf("GatherOf read %s, a Go or process family", f.Name)
		}
	}
	checks := []struct {
		fam  Family
		vals []string
		want float64
	}{
		{fams[0], nil, 4096},
		{fams[1], []string{"200"}, 2},
		{fams[2], []string{"posts"}, 3},
		{fams[3], nil, 5},
	}
	for _, c := range checks {
		if s, ok := c.fam.Sample(c.vals...); !ok || s.Value != c.want {
			t.Errorf("%s%v = %v, %v, want %v", c.fam.Name, c.vals, s.Value, ok, c.want)
		}
	}
}

func TestGatherOfRefusesWhatTheRegistryDoesNotHold(t *testing.T) {
	r := NewRegistry("")
	held := NewGauge("held", "h", UnitCount)
	r.MustRegister(held)
	elsewhere := NewGauge("elsewhere", "h", UnitCount)
	NewRegistry("").MustRegister(elsewhere)
	desc := NewGaugeDesc("unregistered", "h", UnitCount, nil)
	_ = NewCollector(func(context.Context, *Emitter) {}, desc)

	for _, tc := range []struct {
		name string
		d    Declared
		want string
	}{
		{"nil", nil, "nil declaration"},
		{"typedNil", (*Gauge)(nil), "nil declaration"},
		{"typedNilDesc", (*Desc)(nil), "nil declaration"},
		{"anotherRegistrys", elsewhere, `"elsewhere" is not registered in this registry`},
		{"anUnregisteredDesc", desc, `"unregistered" is not registered`},
		{"neverRegistered", NewCounter("loose_total", "h", UnitCount), `"loose_total" is not registered`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fams, err := r.GatherOf(t.Context(), held, tc.d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("GatherOf error = %v, want it to contain %q", err, tc.want)
			}
			if fams != nil {
				t.Errorf("GatherOf families = %v, want none on an error", fams)
			}
		})
	}
}

func TestGatherOfReportsTheDropsOfTheCollectorsItRan(t *testing.T) {
	_ = captureDebugLogs(t)
	r := NewRegistry("")
	d := NewCounterDesc("reads_total", "h", UnitCount, nil)
	r.MustRegister(NewCollector(func(_ context.Context, e *Emitter) { e.Emit(d, -1) }, d))
	fams, err := r.GatherOf(t.Context(), d)
	if err == nil || !strings.Contains(err.Error(), `"reads_total": 1 sample(s) dropped`) {
		t.Errorf("GatherOf error = %v, want the dropped counter sample", err)
	}
	if len(fams) != 1 || len(fams[0].Samples) != 0 {
		t.Errorf("GatherOf families = %+v, want reads_total with no samples", fams)
	}
}

func TestFindAndSampleReadAFamilyByName(t *testing.T) {
	fams := []Family{
		{Name: "a", Samples: []Sample{{Labels: []Label{{"k", "x"}, {"j", "y"}}, Value: 1}}},
		{Name: "b", Samples: []Sample{{Labels: []Label{}, Value: 2}}},
	}
	t.Run("found", func(t *testing.T) {
		f, ok := Find(fams, "a")
		if !ok || f.Name != "a" {
			t.Fatalf("Find(a) = %v, %v", f.Name, ok)
		}
		if s, ok := f.Sample("x", "y"); !ok || s.Value != 1 {
			t.Errorf("Sample(x, y) = %v, %v, want 1, true", s.Value, ok)
		}
		b, _ := Find(fams, "b")
		if s, ok := b.Sample(); !ok || s.Value != 2 {
			t.Errorf("Sample() on an unlabeled family = %v, %v, want 2, true", s.Value, ok)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if _, ok := Find(fams, "c"); ok {
			t.Error("Find(c) found a family that is not there")
		}
		f, _ := Find(fams, "a")
		for _, vals := range [][]string{{"x"}, {"y", "x"}, {"x", "y", "z"}} {
			if _, ok := f.Sample(vals...); ok {
				t.Errorf("Sample(%v) found a series that is not there", vals)
			}
		}
	})
}

func TestExportAndSourceNameTheirValues(t *testing.T) {
	for _, tc := range []struct {
		got  fmt.Stringer
		want string
	}{
		{ExportMetric, "metric"},
		{ExportChartdata, "chartdata"},
		{ExportBoth, "both"},
		{Export(7), "Export(7)"},
		{SourceRecorded, "recorded"},
		{SourceRead, "read"},
		{Source(5), "Source(5)"},
	} {
		if got := tc.got.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
