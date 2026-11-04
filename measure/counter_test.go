package measure

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestCounterInc(t *testing.T) {
	c := NewCounter("test_counter", "test")
	c.Inc()
	c.Inc()
	if got := c.val.Load(); got != 2 {
		t.Errorf("Counter.Inc() = %d, want 2", got)
	}
}

func TestCounterAdd(t *testing.T) {
	c := NewCounter("test_counter_add", "test")
	c.Add(5)
	c.Add(3)
	if got := c.val.Load(); got != 8 {
		t.Errorf("Counter.Add() = %d, want 8", got)
	}
}

func TestCounterSaturatesAtMaxInt64(t *testing.T) {
	// A counter pushed past MaxInt64 pins to MaxInt64 instead of wrapping to a
	// negative value, preserving the monotonic contract in the exposed series.
	c := NewCounter("test_counter_saturate_total", "test")
	c.Add(math.MaxInt64)
	c.Add(math.MaxInt64) // would wrap negative without saturation
	if got := c.val.Load(); got != math.MaxInt64 {
		t.Errorf("Counter saturation: got %d, want MaxInt64", got)
	}
	c.Inc() // stays pinned
	if got := c.val.Load(); got != math.MaxInt64 {
		t.Errorf("Counter saturation after Inc: got %d, want MaxInt64", got)
	}

	lc := NewLabeledCounter("test_lcounter_saturate_total", "test", []string{"k"})
	lc.Add(math.MaxInt64, "v")
	lc.Add(math.MaxInt64, "v")
	lc.mu.RLock()
	v := lc.vals[labelKey{"v"}]
	lc.mu.RUnlock()
	if got := v.Load(); got != math.MaxInt64 {
		t.Errorf("LabeledCounter saturation: got %d, want MaxInt64", got)
	}
}

func TestCounterAddNegativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative Add")
		}
	}()
	c := NewCounter("test_counter_neg", "test")
	c.Add(-1)
}

// TestCounterAdd_ZeroIsAllowedNoPanic pins the negative guard at its inclusive
// boundary: Add(0) is a legal no-op (the guard is n < 0, not n <= 0).
func TestCounterAdd_ZeroIsAllowedNoPanic(t *testing.T) {
	c := NewCounter("mk_counter_add0", "test")
	c.Add(0) // must not panic
	if got := c.val.Load(); got != 0 {
		t.Errorf("Counter.Add(0) value = %d, want 0", got)
	}
	c.Add(7)
	if got := c.val.Load(); got != 7 {
		t.Errorf("Counter.Add(7) after Add(0) = %d, want 7", got)
	}
}

func TestLabeledCounterInc(t *testing.T) {
	lc := NewLabeledCounter("test_lc", "test", []string{"method", "status"})
	lc.Inc("GET", "200")
	lc.Inc("GET", "200")
	lc.Inc("POST", "201")

	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 2 {
		t.Errorf("LabeledCounter[GET,200] = %d, want 2", got)
	}
	key2 := labelKey{"POST", "201", "", ""}
	if got := lc.vals[key2].Load(); got != 1 {
		t.Errorf("LabeledCounter[POST,201] = %d, want 1", got)
	}
}

func TestLabeledCounterArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("test_lc_arity", "test", []string{"method", "status"})
	lc.Inc("GET") // wrong arity
}

// TestNewLabeledCounter_TooManyLabelsErrorsAtRegister pins the maxLabels cap:
// a ninth label is captured at construction and surfaces at registration.
func TestNewLabeledCounter_TooManyLabelsErrorsAtRegister(t *testing.T) {
	lc := NewLabeledCounter("test_lc_many", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"})
	mustRegisterError(t, NewRegistry(""), lc, `LabeledCounter "test_lc_many" supports at most 8 labels`)
}

// TestNewLabeledCounter_ExactlyMaxLabelsAllowed pins the arity guard at its
// inclusive maximum: eight labels is the legal maximum (the guard is > 8).
func TestNewLabeledCounter_ExactlyMaxLabelsAllowed(t *testing.T) {
	lc := NewLabeledCounter("mk_lc8_total", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	lc.Inc("1", "2", "3", "4", "5", "6", "7", "8") // must not panic with eight labels
	if got := lc.vals[labelKey{"1", "2", "3", "4", "5", "6", "7", "8"}].Load(); got != 1 {
		t.Errorf("LabeledCounter[8 labels] = %d, want 1", got)
	}
}

func TestLabeledCounterAdd(t *testing.T) {
	lc := NewLabeledCounter("test_lc_add", "test", []string{"method", "status"})
	lc.Add(5, "GET", "200") // new key: Store(5)
	lc.Add(3, "GET", "200") // existing key: Add(3) -> 8
	lc.Add(10, "POST", "201")

	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 8 {
		t.Errorf("LabeledCounter.Add[GET,200] = %d, want 8", got)
	}
	key2 := labelKey{"POST", "201", "", ""}
	if got := lc.vals[key2].Load(); got != 10 {
		t.Errorf("LabeledCounter.Add[POST,201] = %d, want 10", got)
	}
}

func TestLabeledCounterAdd_zeroOnNewKey(t *testing.T) {
	lc := NewLabeledCounter("test_lc_add_zero", "test", []string{"k"})
	lc.Add(0, "a")
	key := labelKey{"a", "", "", ""}
	v, ok := lc.vals[key]
	if !ok {
		t.Fatal("Add(0) should create the label entry")
	}
	if got := v.Load(); got != 0 {
		t.Errorf("LabeledCounter.Add(0)[a] = %d, want 0", got)
	}
}

func TestLabeledCounterAdd_negativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative Add")
		}
	}()
	lc := NewLabeledCounter("test_lc_add_neg", "test", []string{"k"})
	lc.Add(-1, "a") // correct arity: hits the negative guard, not the arity guard
}

func TestLabeledCounterAdd_arityMismatchPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("test_lc_add_arity", "test", []string{"method", "status"})
	lc.Add(1, "GET") // n>=0 passes negative guard, then arity guard fires
}

func TestLabeledCounter_Reset(t *testing.T) {
	lc := NewLabeledCounter("lc_reset_total", "test", []string{"host"})
	lc.Inc("a")
	lc.Inc("b")
	lc.Reset()

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledCounter_Delete(t *testing.T) {
	lc := NewLabeledCounter("lc_delete_total", "test", []string{"host"})
	lc.Inc("a")
	lc.Inc("b")
	lc.Delete("a")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledCounter_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("lc_del_panic_total", "test", []string{"a", "b"})
	lc.Delete("only_one")
}

// TestLabeledCounter_emptyLabelSet exercises a labeled counter declared with no
// label names: Inc with no values is valid and the family is still emitted.
func TestLabeledCounter_emptyLabelSet(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zero-label counter panicked: %v", r)
		}
	}()
	lc := NewLabeledCounter("rt6_empty_labels", "test", []string{})
	lc.Inc()
	lc.Inc()

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, "rt6_empty_labels") {
		t.Errorf("empty label counter missing: %s", out)
	}
}

func TestWriteCounterFormat(t *testing.T) {
	c := NewCounter("http_requests_total", "Total HTTP requests")
	c.Inc()
	c.Inc()
	c.Inc()

	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()

	if !strings.Contains(out, "# HELP http_requests_total Total HTTP requests") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, "http_requests_total 3") {
		t.Errorf("missing counter value: %s", out)
	}
}

func TestWriteCounter_helpCarriageReturnRaw(t *testing.T) {
	c := NewCounter("cr_counter", "line1\rline2")
	c.Inc()

	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()

	// Carriage return is not a defined escape in the Prometheus text format
	// (only \, ", and \n are), so a raw CR passes through HELP unchanged rather
	// than being emitted as the invalid escape sequence \r.
	if !strings.Contains(out, "# HELP cr_counter line1\rline2") {
		t.Errorf("raw carriage return not preserved in HELP: %q", out)
	}
	if strings.Contains(out, `# HELP cr_counter line1\rline2`) {
		t.Errorf("CR was escaped to the invalid \\r sequence: %q", out)
	}
}

func TestWriteLabeledCounterSorted(t *testing.T) {
	lc := NewLabeledCounter("sorted_lc", "test", []string{"method", "path", "status"})
	lc.Inc("POST", "/b", "201")
	lc.Inc("GET", "/a", "200")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `method="GET",path="/a",status="200"`) {
		t.Errorf("labels not sorted: %s", out)
	}
}

func TestLabelValueEscaping(t *testing.T) {
	lc := NewLabeledCounter("esc_lc", "test", []string{"path"})
	lc.Inc("C:\\DIR\\FILE.TXT")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, `path="C:\\DIR\\FILE.TXT"`) {
		t.Errorf("label value not escaped correctly: %s", out)
	}
}

func TestLabelValueEscapingNewlineAndQuote(t *testing.T) {
	lc := NewLabeledCounter("esc_lc2", "test", []string{"msg"})
	lc.Inc("hello\n\"world\"")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, `msg="hello\n\"world\""`) {
		t.Errorf("label escaping wrong: %s", out)
	}
}

func TestLabelValueTabNotOverEscaped(t *testing.T) {
	lc := NewLabeledCounter("esc_lc3", "test", []string{"msg"})
	lc.Inc("a\tb") // tab should NOT be escaped

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, "msg=\"a\tb\"") {
		t.Errorf("tab should pass through unescaped: %s", out)
	}
}

func TestLabelValueCarriageReturnPassthrough(t *testing.T) {
	lc := NewLabeledCounter("esc_cr_lc", "test", []string{"msg"})
	lc.Inc("a\rb")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	// CR is not a Prometheus escape, so a raw CR passes through the label value
	// unchanged rather than being emitted as the invalid escape sequence \r.
	if !strings.Contains(out, "msg=\"a\rb\"") {
		t.Errorf("raw carriage return not preserved in label value: %q", out)
	}
	if strings.Contains(out, `msg="a\rb"`) {
		t.Errorf("CR was escaped to the invalid \\r sequence in label value: %q", out)
	}
}

func TestSortedLabelKeys_returnsLexicographicOrder(t *testing.T) {
	var s series
	vals := map[labelKey]int{
		{"b", "2", "", ""}: 1,
		{"a", "9", "", ""}: 1,
		{"a", "1", "", ""}: 1,
		{"c", "0", "", ""}: 1,
	}
	got := s.sortedLabelKeys(vals)
	want := []labelKey{
		{"a", "1", "", ""},
		{"a", "9", "", ""},
		{"b", "2", "", ""},
		{"c", "0", "", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedLabelKeys() = %v, want %v", got, want)
	}
}

func TestSortedLabelKeys_empty(t *testing.T) {
	var s series
	if got := s.sortedLabelKeys(map[labelKey]int{}); len(got) != 0 {
		t.Errorf("sortedLabelKeys(empty) = %v, want empty", got)
	}
}

func TestLabeledCounterConcurrent(t *testing.T) {
	lc := NewLabeledCounter("conc_lc", "test", []string{"method", "status"})
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { lc.Inc("GET", "200") })
	}
	wg.Wait()
	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 100 {
		t.Errorf("concurrent LabeledCounter = %d, want 100", got)
	}
}

// TestCounter_Inc_Concurrent asserts the unlabeled atomic counter totals every
// concurrent increment without loss.
func TestCounter_Inc_Concurrent(t *testing.T) {
	c := NewCounter("rt6_counter_hot", "hot path")
	const n = 100
	const iters = 1000
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for range iters {
				c.Inc()
			}
		})
	}
	wg.Wait()
	if got := c.val.Load(); got != int64(n*iters) {
		t.Errorf("counter = %d, want %d", got, n*iters)
	}
}

// TestLabeledMetricCardinalityWarning pins the one-time cardinality-threshold
// warning in loadOrStore: it fires exactly once when the series count reaches
// cardinalityWarnThreshold and names the metric and series count. Serial: it
// captures slog.Default().
func TestLabeledMetricCardinalityWarning(t *testing.T) {
	buf := captureDebugLogs(t)
	lc := NewLabeledCounter("cardinality_warn_total", "test", []string{"path"})

	for i := range cardinalityWarnThreshold {
		lc.Inc(string(rune('A' + i)))
	}

	logs := buf.String()
	if !strings.Contains(logs, "possible label-cardinality explosion") {
		t.Fatalf("cardinality warning log = %q, want threshold warning", logs)
	}
	if !strings.Contains(logs, "metric=cardinality_warn_total") {
		t.Errorf("cardinality warning log = %q, want metric name", logs)
	}
	if !strings.Contains(logs, "series=1000") {
		t.Errorf("cardinality warning log = %q, want series=1000", logs)
	}

	lc.Inc("beyond-threshold")
	if got := strings.Count(buf.String(), "possible label-cardinality explosion"); got != 1 {
		t.Errorf("cardinality warnings after crossing threshold = %d, want 1", got)
	}

	// A Delete that dips the map below the threshold followed by a new series
	// re-crosses it; the warning is one-time per metric, not per crossing.
	lc.Delete("A")
	lc.Delete("B")
	lc.Inc("re-cross-1")
	lc.Inc("re-cross-2")
	if got := strings.Count(buf.String(), "possible label-cardinality explosion"); got != 1 {
		t.Errorf("cardinality warnings after a dip and re-cross = %d, want 1", got)
	}
}

func BenchmarkLabeledCounterInc(b *testing.B) {
	lc := NewLabeledCounter("bench_lc", "bench", []string{"method", "path", "status"})
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	for b.Loop() {
		lc.Inc("GET", "/api", "200")
	}
}

// BenchmarkLabeledCounterInc_DynamicLabel measures Inc where the caller BUILDS
// a label value per call, which is what a real request handler does. The value
// cycles through eight paths, so after the eighth iteration every series
// already exists and this charts the loaded path plus the caller's string
// building, not series creation — that is TestLabeledCounterNewSeriesCostIsConstant,
// which can control the fixture where a benchmark loop cannot.
func BenchmarkLabeledCounterInc_DynamicLabel(b *testing.B) {
	lc := NewLabeledCounter("bench_lc_dynamic", "bench", []string{"method", "path", "status"})
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		lc.Inc("GET", "/api/"+strings.Repeat("x", i%8), "200")
	}
}

func BenchmarkLabeledCounterInc_Parallel(b *testing.B) {
	lc := NewLabeledCounter("bench_lc_par", "bench", []string{"method", "path", "status"})
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lc.Inc("GET", "/api", "200")
		}
	})
}

// TestStoreNewSeries_doubleCheckLoadsExistingWithoutWarn pins the write-lock
// double-check in storeNewSeries: when the key was inserted between the
// caller's RLock miss and the write lock (simulated by pre-populating the
// map), the existing entry is returned unchanged, makeV is never invoked, no
// duplicate insert happens, and no warning is signalled (zero seriesWarnings —
// including the sanitize warning, which must not fire on the
// double-check-found path).
func TestStoreNewSeries_doubleCheckLoadsExistingWithoutWarn(t *testing.T) {
	key := labelKey{"a"}
	existing := new(int)
	*existing = 7
	m := map[labelKey]*int{key: existing}
	s := series{name: "double_check_total"}

	made := false
	v, loaded, w := s.storeNewSeries(m, &key, func() *int {
		made = true
		return new(int)
	})

	if !loaded {
		t.Fatal("storeNewSeries loaded = false, want true for a key inserted before the write lock")
	}
	if v != existing {
		t.Error("storeNewSeries returned a different entry; want the pre-existing one")
	}
	if made {
		t.Error("storeNewSeries invoked makeV although the key already existed")
	}
	if w != (seriesWarnings{}) {
		t.Errorf("storeNewSeries warnings = %+v, want zero value on the double-check path", w)
	}
	if len(m) != 1 {
		t.Errorf("map len = %d, want 1 (no duplicate insert)", len(m))
	}
}

// TestLabeledCounterDelete_SanitizesLabelValues pins create/delete symmetry
// for invalid-UTF-8 label values: recording stores the series under the
// sanitized key, so Delete called with the SAME raw invalid values must
// sanitize its lookup key the same way and remove that series. Asserted via
// exposition, not map internals. One labeled type suffices: all three Delete
// methods share sanitizeLabelKey. Serial: it captures slog.Default.
func TestLabeledCounterDelete_SanitizesLabelValues(t *testing.T) {
	captureDebugLogs(t)
	bad := "\xff\xfe"
	lc := NewLabeledCounter("del_sanval_counter_total", "test", []string{"m"})
	lc.Inc(bad)

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	if out := b.String(); !strings.Contains(out, "\uFFFD") {
		t.Fatalf("exposition missing sanitized series before Delete: %q", out)
	}

	lc.Delete(bad)

	b.Reset()
	WriteLabeledCounter(&b, lc)
	if out := b.String(); strings.Contains(out, "\uFFFD") {
		t.Errorf("exposition still contains sanitized series after Delete with raw values: %q", out)
	}
}

// TestCounterRecordPathIsAllocationFree pins the free half of the counter cost
// model: incrementing a counter, or a labeled counter's EXISTING series, costs
// nothing. Every consuming app calls this per request, so an allocation here is
// charged to every request every consumer serves. It fails at merge time and
// names the method, where the weekly benchmark chart notices a week later.
//
// The label set is created in setup, so the measured closure takes loadOrStore's
// RLock fast path. TestLabeledCounterNewSeriesCostIsConstant measures CREATION.
func TestCounterRecordPathIsAllocationFree(t *testing.T) {
	c := NewCounter("alloc_counter_total", "allocation contract")
	lc := NewLabeledCounter("alloc_labeled_counter_total", "allocation contract",
		[]string{"method", "path", "status"})
	lc.Inc("GET", "/api", "200")

	cases := []struct {
		name string // subtest name: an identifier, no slashes
		call string // how the call reads, for the failure message
		fn   func()
	}{
		{"counter_inc", "Counter.Inc()", func() { c.Inc() }},
		{"counter_add", "Counter.Add(7)", func() { c.Add(7) }},
		{"labeled_counter_inc", `LabeledCounter.Inc("GET", "/api", "200")`, func() { lc.Inc("GET", "/api", "200") }},
		{"labeled_counter_add", `LabeledCounter.Add(7, "GET", "/api", "200")`, func() { lc.Add(7, "GET", "/api", "200") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(allocRuns, tc.fn); got != 0 {
				t.Errorf("%s allocated %v times per run, want 0: this is the per-request record path, so an allocation here is paid by every request every consuming app serves", tc.call, got)
			}
		})
	}
}

// TestLabeledCounterNewSeriesCostIsConstant pins the bounded half of the cost
// model, and this is the half the weekly tracker cannot police: a count that
// drifts from 1 to 2 is a ratio of 2 and would alert, but one that starts
// tracking a per-call quantity is a design change the chart reads as ordinary
// growth. Creating a label set measures ONE allocation (the series' own
// atomic.Int64) and the property that matters is what that number is
// independent OF.
//
// Nor does anything else in this package measure the path:
// BenchmarkLabeledCounterInc_DynamicLabel cycles through eight label values, so
// from its ninth iteration on every series already exists and what it charts is
// the fixture's own string building against the loaded fast path (17 B/op, 1
// alloc/op measured — the concatenation, not a series).
//
// Three axes, each a different regression class:
//
//   - label arity, because a per-label allocation would multiply by a metric's
//     label count for every new series;
//   - label value LENGTH, because label values are caller-owned and can carry
//     upstream text, so a count that tracks bytes is an amplification vector
//     in exactly the place the README already warns about cardinality;
//   - how many series the metric ALREADY holds, which is the expensive one. A
//     per-existing-key cost (a scan to check for a match, a re-sort, a
//     rebuild) makes series creation quadratic over the life of a
//     long-running process, and every consuming app is long-running.
//
// Each run of a measured closure creates a NEW series, which is the cold path
// on purpose; the label-value slices come pre-built so the fixture's own
// allocations are not counted as the library's.
func TestLabeledCounterNewSeriesCostIsConstant(t *testing.T) {
	// The 20k-series case crosses cardinalityWarnThreshold during its
	// pre-fill; capture the warning so it stays out of the test output. The
	// crossing is in setup, never inside a measured closure.
	captureDebugLogs(t)

	t.Run("independent_of_label_count", func(t *testing.T) {
		arities := []int{1, 3, 8}
		counts := make([]float64, len(arities))
		for i, n := range arities {
			keys := newSeriesLabelValues(allocRuns, n, 8)
			lc := NewLabeledCounter("alloc_arity_total", "allocation contract", labelNames(n))
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lc.Inc(keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledCounter.Inc(a new series on %d labels) allocated %v times per run, want %v (its count at %d label): series creation must not charge per label name, or a metric's label count multiplies the cost of every series it grows",
					arities[i], got, counts[0], arities[0])
			}
		}
		t.Logf("a constant %v allocations per new series from %d to %d labels", counts[0], arities[0], arities[len(arities)-1])
	})

	t.Run("independent_of_value_length", func(t *testing.T) {
		lengths := []int{8, 1024, 65536}
		counts := make([]float64, len(lengths))
		for i, n := range lengths {
			keys := newSeriesLabelValues(allocRuns, 3, n)
			lc := NewLabeledCounter("alloc_vallen_total", "allocation contract", []string{"a", "b", "c"})
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lc.Inc(keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledCounter.Inc(a new series with %d-byte label values) allocated %v times per run, want %v (its count at %d bytes): label values are caller-owned and may carry upstream text, so an allocation count that tracks their SIZE lets whoever supplies that text choose the cost",
					lengths[i], got, counts[0], lengths[0])
			}
		}
		t.Logf("a constant %v allocations per new series from %d- to %d-byte label values", counts[0], lengths[0], lengths[len(lengths)-1])
	})

	t.Run("independent_of_existing_series", func(t *testing.T) {
		existing := []int{0, 1_000, 20_000}
		counts := make([]float64, len(existing))
		for i, n := range existing {
			keys := newSeriesLabelValues(allocRuns, 3, 8)
			lc := NewLabeledCounter("alloc_existing_total", "allocation contract", []string{"a", "b", "c"})
			for k := range n {
				lc.Inc("pre", strconv.Itoa(k), "x")
			}
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lc.Inc(keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledCounter.Inc(a new series into a metric already holding %d) allocated %v times per run, want %v (its count at %d existing): a per-existing-series cost makes series creation quadratic over the life of a process, and every consuming app runs for weeks",
					existing[i], got, counts[0], existing[0])
			}
		}
		t.Logf("a constant %v allocations per new series with %d to %d series already present", counts[0], existing[0], existing[len(existing)-1])
	})
}
