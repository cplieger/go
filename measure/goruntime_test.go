package measure

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strings"
	"testing"
	"time"
)

// clientGolangFamilies is client_golang v1.25.0's default Go and process page
// as "name type" lines. It was generated on Linux, outside this module so that
// client_golang never enters go.mod, by a main that registers
// collectors.NewGoCollector() and
// collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}) on
// prometheus.NewPedanticRegistry() and prints each gathered family's name and
// strings.ToLower of its type, sorted by name. Regenerate it the same way for a
// newer client_golang.
const clientGolangFamilies = "client_golang_v1.25.0_families.txt"

// measureOnlyFamilies are the two process families measure serves that
// client_golang does not.
var measureOnlyFamilies = []string{pmGCPauseTotal, pmUptime}

func readClientGolangFamilies(t *testing.T) map[string]Type {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", clientGolangFamilies))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[string]Type{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, typ, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			t.Fatalf("malformed line %q in %s", sc.Text(), clientGolangFamilies)
		}
		want[name] = Type(typ)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return want
}

// TestProcessFamiliesMatchClientGolang is the golden check: on Linux, where
// every /proc read succeeds, the Go and process families are client_golang's
// default set by name and type, plus measure's own two, and processFamilyNames
// reserves exactly that set.
func TestProcessFamiliesMatchClientGolang(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("the /proc families are Linux-only")
	}
	want := readClientGolangFamilies(t)
	for _, n := range measureOnlyFamilies {
		want[n] = map[string]Type{pmGCPauseTotal: TypeCounter, pmUptime: TypeGauge}[n]
	}
	got := map[string]Type{}
	var names []string
	for _, f := range processGather() {
		got[f.Name] = f.Type
		names = append(names, f.Name)
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("%s: type %q, want %q", name, got[name], typ)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s is served but is neither client_golang's nor measure's own", name)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("process families are not sorted by name: %v", names)
	}
	if !slices.Equal(names, processFamilyNames) {
		t.Errorf("processFamilyNames = %v, want the served set %v", processFamilyNames, names)
	}
}

// TestRuntimeMetricNamesAreSupported fails on a Go release that drops or
// retypes a runtime/metrics value the Go families read, which would otherwise
// serve 0.
func TestRuntimeMetricNamesAreSupported(t *testing.T) {
	kinds := map[string]metrics.ValueKind{}
	for _, d := range metrics.All() {
		kinds[d.Name] = d.Kind
	}
	seen := map[string]bool{}
	for i, name := range runtimeMetricNames {
		if name == "" {
			t.Errorf("runtime metric index %d has no name", i)
			continue
		}
		if seen[name] {
			t.Errorf("%s is listed twice", name)
		}
		seen[name] = true
		if kind, ok := kinds[name]; !ok || kind != metrics.KindUint64 {
			t.Errorf("%s: supported %v, kind %v; want a supported KindUint64 metric", name, ok, kind)
		}
	}
}

// TestRuntimeFamiliesFollowClientGolang restates client_golang v1.25.0's
// mapping (prometheus/go_collector_latest.go, memStatsFromRM, and the
// default runtime metric rule in internal/go_collector_options.go) by
// runtime/metrics name, so a wrong index in runtimeFamilies fails here.
func TestRuntimeFamiliesFollowClientGolang(t *testing.T) {
	want := map[string][]string{
		"go_gc_gogc_percent":              {"/gc/gogc:percent"},
		"go_gc_gomemlimit_bytes":          {"/gc/gomemlimit:bytes"},
		"go_memstats_alloc_bytes":         {"/memory/classes/heap/objects:bytes"},
		"go_memstats_alloc_bytes_total":   {"/gc/heap/allocs:bytes"},
		"go_memstats_buck_hash_sys_bytes": {"/memory/classes/profiling/buckets:bytes"},
		"go_memstats_frees_total":         {"/gc/heap/frees:objects", "/gc/heap/tiny/allocs:objects"},
		"go_memstats_gc_sys_bytes":        {"/memory/classes/metadata/other:bytes"},
		"go_memstats_heap_alloc_bytes":    {"/memory/classes/heap/objects:bytes"},
		"go_memstats_heap_idle_bytes":     {"/memory/classes/heap/released:bytes", "/memory/classes/heap/free:bytes"},
		"go_memstats_heap_inuse_bytes":    {"/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes"},
		"go_memstats_heap_objects":        {"/gc/heap/objects:objects"},
		"go_memstats_heap_released_bytes": {"/memory/classes/heap/released:bytes"},
		"go_memstats_heap_sys_bytes": {
			"/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes",
			"/memory/classes/heap/released:bytes", "/memory/classes/heap/free:bytes",
		},
		"go_memstats_mallocs_total":      {"/gc/heap/allocs:objects", "/gc/heap/tiny/allocs:objects"},
		"go_memstats_mcache_inuse_bytes": {"/memory/classes/metadata/mcache/inuse:bytes"},
		"go_memstats_mcache_sys_bytes":   {"/memory/classes/metadata/mcache/inuse:bytes", "/memory/classes/metadata/mcache/free:bytes"},
		"go_memstats_mspan_inuse_bytes":  {"/memory/classes/metadata/mspan/inuse:bytes"},
		"go_memstats_mspan_sys_bytes":    {"/memory/classes/metadata/mspan/inuse:bytes", "/memory/classes/metadata/mspan/free:bytes"},
		"go_memstats_next_gc_bytes":      {"/gc/heap/goal:bytes"},
		"go_memstats_other_sys_bytes":    {"/memory/classes/other:bytes"},
		"go_memstats_stack_inuse_bytes":  {"/memory/classes/heap/stacks:bytes"},
		"go_memstats_stack_sys_bytes":    {"/memory/classes/heap/stacks:bytes", "/memory/classes/os-stacks:bytes"},
		"go_memstats_sys_bytes":          {"/memory/classes/total:bytes"},
		"go_sched_gomaxprocs_threads":    {"/sched/gomaxprocs:threads"},
	}
	if len(runtimeFamilies) != len(want) {
		t.Errorf("runtimeFamilies has %d entries, want %d", len(runtimeFamilies), len(want))
	}
	for _, f := range runtimeFamilies {
		var got []string
		for _, idx := range f.sum {
			got = append(got, runtimeMetricNames[idx])
		}
		slices.Sort(got)
		w := slices.Sorted(slices.Values(want[f.name]))
		if !slices.Equal(got, w) {
			t.Errorf("%s sums %v, want %v", f.name, got, w)
		}
	}
}

func gatheredFamily(t *testing.T, name string) Family {
	t.Helper()
	for _, f := range processGather() {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %s family", name)
	return Family{}
}

// TestRuntimeSettingFamiliesReadTheLiveSettings changes GOGC and reads it back,
// so the setting families read the runtime, not a constant. Serial: it changes
// the process's GC percent.
func TestRuntimeSettingFamiliesReadTheLiveSettings(t *testing.T) {
	prev := debug.SetGCPercent(173)
	t.Cleanup(func() { debug.SetGCPercent(prev) })
	for name, want := range map[string]float64{
		pmGOGC:       173,
		pmGOMAXPROCS: float64(runtime.GOMAXPROCS(0)),
		pmGOMEMLIMIT: float64(debug.SetMemoryLimit(-1)),
	} {
		if got := gatheredFamily(t, name).Samples[0].Value; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	info := gatheredFamily(t, pmGoInfo).Samples[0]
	if !slices.Equal(info.Labels, []Label{{Name: "version", Value: runtime.Version()}}) || info.Value != 1 {
		t.Errorf("go_info = %+v, want version %q and 1", info, runtime.Version())
	}
	// A Go program has at least the main thread and sysmon's, and this test's
	// goroutine.
	if threads := gatheredFamily(t, pmThreads).Samples[0].Value; threads < 2 {
		t.Errorf("go_threads = %v, want at least 2", threads)
	}
	if goroutines := gatheredFamily(t, pmGoroutines).Samples[0].Value; goroutines < 1 {
		t.Errorf("go_goroutines = %v, want at least 1", goroutines)
	}
}

// TestGCDurationSummary checks the one runtime summary after two collections:
// five ascending quantiles from 0 to 1, a count of at least two, and a positive
// pause total that process_gc_pause_seconds_total shares.
func TestGCDurationSummary(t *testing.T) {
	runtime.GC()
	runtime.GC()
	f := gatheredFamily(t, pmGCDuration)
	if f.Type != TypeSummary || len(f.Samples) != 1 || f.Samples[0].Summary == nil || len(f.Samples[0].Labels) != 0 {
		t.Fatalf("go_gc_duration_seconds = %+v, want one unlabeled summary series", f)
	}
	s := f.Samples[0].Summary
	var qs []float64
	for i, q := range s.Quantiles {
		qs = append(qs, q.Quantile)
		if i > 0 && q.Value < s.Quantiles[i-1].Value {
			t.Errorf("quantile %v = %v is below quantile %v = %v", q.Quantile, q.Value, s.Quantiles[i-1].Quantile, s.Quantiles[i-1].Value)
		}
	}
	if !slices.Equal(qs, []float64{0, 0.25, 0.5, 0.75, 1}) {
		t.Errorf("quantiles %v, want client_golang's 0, 0.25, 0.5, 0.75 and 1", qs)
	}
	if s.Count < 2 || s.Sum <= 0 {
		t.Errorf("count %d, sum %v; want at least two collections and a positive total", s.Count, s.Sum)
	}
	if pause := gatheredFamily(t, pmGCPauseTotal).Samples[0].Value; pause < s.Sum {
		t.Errorf("process_gc_pause_seconds_total = %v, below the summary's earlier sum %v", pause, s.Sum)
	}
	last := gatheredFamily(t, pmLastGC).Samples[0].Value
	if now := float64(time.Now().UnixNano()) / 1e9; last > now || last < now-60 {
		t.Errorf("go_memstats_last_gc_time_seconds = %v, want the last collection's Unix time, within a minute of %v", last, now)
	}
}

// TestSummaryOnThePage pins the summary's exposition lines: the quantile label
// after the series' own labels, then _sum and _count.
func TestSummaryOnThePage(t *testing.T) {
	f := Family{Name: "s", Help: "h", Type: TypeSummary, Samples: []Sample{{
		Labels:  []Label{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}},
		Summary: &SummaryValue{Quantiles: []Quantile{{0, 0.001}, {0.5, 0.25}, {1, 3}}, Count: 7, Sum: 4.5},
	}}}
	mf, ok := pageFamily(&f)
	if !ok {
		t.Fatal("a summary with a sample was dropped")
	}
	var b strings.Builder
	appendPrometheus(&b, []metricFamily{mf})
	want := `# HELP s h
# TYPE s summary
s{a="1",b="2",quantile="0"} 0.001
s{a="1",b="2",quantile="0.5"} 0.25
s{a="1",b="2",quantile="1"} 3
s_sum{a="1",b="2"} 4.5
s_count{a="1",b="2"} 7
`
	if b.String() != want {
		t.Errorf("summary page:\n%s\nwant:\n%s", b.String(), want)
	}
}

// TestPageHasNoRepeatedFamily is the exposition twin of the registry's name
// check: a repeated "# TYPE" line makes the scrape invalid.
func TestPageHasNoRepeatedFamily(t *testing.T) {
	r := goldenFixtureRegistry()
	d := NewGaugeDesc("collected", "h", []string{"k"})
	r.MustRegister(staticCollector(func(e *Emitter) { e.Emit(d, 1, "v") }, d))
	seen := map[string]bool{}
	for line := range strings.SplitSeq(serve(t, r.Handler()), "\n") {
		name, ok := strings.CutPrefix(line, "# TYPE ")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, " ")
		if seen[name] {
			t.Errorf("family %s appears twice on the page", name)
		}
		seen[name] = true
	}
	if !seen[pmGCDuration] || !seen["collected"] {
		t.Errorf("page families %v miss the runtime summary or the collector", seen)
	}
}

// TestRuntimeNamesAreReserved: a user metric cannot take a Go family name or
// a series name the summary writes.
func TestRuntimeNamesAreReserved(t *testing.T) {
	for _, name := range []string{pmThreads, "go_memstats_sys_bytes", pmGCDuration + "_sum", pmGCDuration + "_count", pmNetReceive} {
		mustRegisterError(t, NewRegistry(""), NewGauge(name, "h"), "collides")
	}
}

// TestProcessFamiliesOfWireEachValue builds the families from readings with a
// distinct value per source, so each family must carry its own source's value,
// and a failed /proc read must leave its families out.
func TestProcessFamiliesOfWireEachValue(t *testing.T) {
	g := &goRuntime{threads: 9}
	for i := range g.rm {
		g.rm[i] = 1 << i
	}
	g.gc.PauseQuantiles = []time.Duration{1, 2, 3, 4, 5}
	g.gc.PauseTotal = 2 * time.Second
	g.gc.NumGC = 6
	g.gc.LastGC = time.Unix(1700000000, 500000000)
	d := processMetricsData{
		startTime: 1000, uptime: 20, cpuSeconds: 3.5, vsize: 8192, netRx: 11, netTx: 12,
		rss: 4096, maxFDs: 1024, maxVsize: float64(math.MaxUint64), openFDs: 7,
	}
	want := map[string]float64{
		pmThreads: 9, pmLastGC: 1700000000.5, pmGoInfo: 1, pmGCPauseTotal: 2,
		pmStartTime: 1000, pmUptime: 20, pmCPUTotal: 3.5, pmVirtualBytes: 8192,
		pmNetReceive: 11, pmNetTransmit: 12, pmResidentBytes: 4096, pmMaxFDs: 1024,
		pmVirtualMaxBytes: float64(math.MaxUint64), pmOpenFDs: 7,
	}
	for _, f := range runtimeFamilies {
		var v float64
		for _, idx := range f.sum {
			v += float64(uint64(1) << idx)
		}
		want[f.name] = v
	}
	got := map[string]Sample{}
	for _, f := range processFamiliesOf(g, &d) {
		got[f.Name] = f.Samples[0]
	}
	for name, v := range want {
		if s, ok := got[name]; !ok || s.Value != v {
			t.Errorf("%s = %+v (served %v), want %v", name, s, ok, v)
		}
	}
	sum := got[pmGCDuration].Summary
	wantQ := []Quantile{{0, 1e-9}, {0.25, 2e-9}, {0.5, 3e-9}, {0.75, 4e-9}, {1, 5e-9}}
	if sum == nil || sum.Count != 6 || sum.Sum != 2 || !slices.Equal(sum.Quantiles, wantQ) {
		t.Errorf("go_gc_duration_seconds = %+v, want count 6, sum 2 and quantiles %v", sum, wantQ)
	}

	// Fail one /proc read at a time, so each family is gated by its own read.
	procNames := []string{pmCPUTotal, pmResidentBytes, pmOpenFDs, pmMaxFDs, pmVirtualBytes, pmVirtualMaxBytes, pmNetReceive, pmNetTransmit}
	for _, tc := range []struct {
		fail func(*processMetricsData)
		gone []string
	}{
		{func(d *processMetricsData) { d.cpuSeconds = -1 }, []string{pmCPUTotal}},
		{func(d *processMetricsData) { d.rss = 0 }, []string{pmResidentBytes}},
		{func(d *processMetricsData) { d.openFDs = -1 }, []string{pmOpenFDs}},
		{func(d *processMetricsData) { d.maxFDs = -1 }, []string{pmMaxFDs}},
		{func(d *processMetricsData) { d.vsize = -1 }, []string{pmVirtualBytes}},
		{func(d *processMetricsData) { d.maxVsize = -1 }, []string{pmVirtualMaxBytes}},
		{func(d *processMetricsData) { d.netRx, d.netTx = -1, -1 }, []string{pmNetReceive, pmNetTransmit}},
	} {
		one := d
		tc.fail(&one)
		served := map[string]bool{}
		for _, f := range processFamiliesOf(g, &one) {
			served[f.Name] = true
		}
		for _, name := range procNames {
			if want := !slices.Contains(tc.gone, name); served[name] != want {
				t.Errorf("with %v failed, %s served = %v, want %v", tc.gone, name, served[name], want)
			}
		}
		if !served[pmStartTime] || !served[pmUptime] || !served[pmGCDuration] {
			t.Errorf("with %v failed, a family that does not read /proc was dropped", tc.gone)
		}
	}
}
