package measure

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGaugeFloat64(t *testing.T) {
	g := NewGauge("test_gauge_f64", "test")
	g.Set(3.14)
	if got := g.Get(); math.Abs(got-3.14) > 0.001 {
		t.Errorf("Gauge.Set(3.14) = %f", got)
	}
	g.Inc()
	if got := g.Get(); math.Abs(got-4.14) > 0.001 {
		t.Errorf("Gauge after Inc = %f", got)
	}
	g.Dec()
	if got := g.Get(); math.Abs(got-3.14) > 0.001 {
		t.Errorf("Gauge after Dec = %f", got)
	}
	g.Add(1.5)
	if got := g.Get(); math.Abs(got-4.64) > 0.001 {
		t.Errorf("Gauge after Add = %f", got)
	}
	g.Sub(0.64)
	if got := g.Get(); math.Abs(got-4.0) > 0.001 {
		t.Errorf("Gauge after Sub = %f", got)
	}
}

func TestGaugeIncDec(t *testing.T) {
	g := NewGauge("test_gauge", "test")
	g.Inc()
	g.Inc()
	g.Dec()
	if got := g.Get(); got != 1 {
		t.Errorf("Gauge = %f, want 1", got)
	}
}

func TestLabeledGauge(t *testing.T) {
	lg := NewLabeledGauge("lg_test", "test", []string{"host"})
	lg.Set(42.5, "server1")
	lg.Set(10, "server2")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()

	if !strings.Contains(out, "# TYPE lg_test gauge") {
		t.Error("missing TYPE")
	}
	if !strings.Contains(out, `lg_test{host="server1"} 42.5`) {
		t.Errorf("missing server1: %s", out)
	}
	if !strings.Contains(out, `lg_test{host="server2"} 10`) {
		t.Errorf("missing server2: %s", out)
	}
}

func TestLabeledGauge_Reset(t *testing.T) {
	lg := NewLabeledGauge("lg_reset", "test", []string{"host"})
	lg.Set(1, "a")
	lg.Set(2, "b")
	lg.Reset()

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledGauge_Delete(t *testing.T) {
	lg := NewLabeledGauge("lg_delete", "test", []string{"host"})
	lg.Set(1, "a")
	lg.Set(2, "b")
	lg.Delete("a")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledGauge_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lg := NewLabeledGauge("lg_del_panic", "test", []string{"a", "b"})
	lg.Delete("only_one")
}

func TestLabeledGauge_SetArityPanic(t *testing.T) {
	lg := NewLabeledGauge("arity_g", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lg.Set(1.0, "only_one")
}

// TestNewLabeledGauge_TooManyLabelsErrorsAtRegister pins the maxLabels cap:
// a ninth label is captured at construction and surfaces at registration.
func TestNewLabeledGauge_TooManyLabelsErrorsAtRegister(t *testing.T) {
	lg := NewLabeledGauge("lg_many", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"})
	mustRegisterError(t, NewRegistry(""), lg, `LabeledGauge "lg_many" supports at most 8 labels`)
}

// TestNewLabeledGauge_ExactlyMaxLabelsAllowed pins the arity guard at its
// inclusive maximum: eight labels is the legal maximum (the guard is > 8).
func TestNewLabeledGauge_ExactlyMaxLabelsAllowed(t *testing.T) {
	lg := NewLabeledGauge("mk_lg8", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	lg.Set(9, "1", "2", "3", "4", "5", "6", "7", "8") // must not panic with eight labels

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	if out := b.String(); !strings.Contains(out, `a="1",b="2",c="3",d="4",e="5",f="6",g="7",h="8"`) {
		t.Errorf("eight-label gauge not exposed correctly:\n%s", out)
	}
}

func TestWriteGaugeFormat(t *testing.T) {
	g := NewGauge("active_connections", "Active connection count")
	g.Inc()
	g.Inc()
	g.Dec()

	var b strings.Builder
	WriteGauge(&b, g)
	out := b.String()

	if !strings.Contains(out, "# HELP active_connections Active connection count") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE active_connections gauge") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, "active_connections 1") {
		t.Errorf("missing gauge value: %s", out)
	}
}

// TestGauge_specialValues checks the spec tokens for non-finite and signed-zero
// gauge values.
func TestGauge_specialValues(t *testing.T) {
	posInf := "+" + "Inf"
	negInf := "-" + "Inf"
	nanStr := "Na" + "N"
	tests := []struct {
		name string
		prom string
		val  float64
	}{
		{name: "pos_inf", val: math.Inf(1), prom: posInf},
		{name: "neg_inf", val: math.Inf(-1), prom: negInf},
		{name: "nan", val: math.NaN(), prom: nanStr},
		{name: "zero", val: 0, prom: "0"},
		{name: "neg_zero", val: math.Copysign(0, -1), prom: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGauge("rt6_special_"+tt.name, "test")
			g.Set(tt.val)

			var b strings.Builder
			WriteGauge(&b, g)
			out := b.String()
			if !strings.Contains(out, "rt6_special_"+tt.name+" "+tt.prom) {
				t.Errorf("Prometheus: expected %q in:\n%s", tt.prom, out)
			}
		})
	}
}

func TestLabeledGauge_specialValues(t *testing.T) {
	lg := NewLabeledGauge("rt6_lg_special", "test", []string{"v"})
	lg.Set(math.Inf(1), "inf")
	lg.Set(math.NaN(), "nan")
	lg.Set(math.Inf(-1), "neginf")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	posInf := "+" + "Inf"
	negInf := "-" + "Inf"
	nanStr := "Na" + "N"
	if !strings.Contains(out, posInf) {
		t.Errorf("missing +Inf in labeled gauge: %s", out)
	}
	if !strings.Contains(out, nanStr) {
		t.Errorf("missing NaN in labeled gauge: %s", out)
	}
	if !strings.Contains(out, negInf) {
		t.Errorf("missing -Inf in labeled gauge: %s", out)
	}
}

// TestLabeledGauge_emptyLabelSet exercises a labeled gauge declared with no
// label names: Set with no values is valid and the family is still emitted.
func TestLabeledGauge_emptyLabelSet(t *testing.T) {
	lg := NewLabeledGauge("rt6_empty_lg", "test", []string{})
	lg.Set(42)

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	if !strings.Contains(out, "rt6_empty_lg") {
		t.Errorf("empty label gauge missing: %s", out)
	}
}

// TestGauge_AddSub_Concurrent exercises the float64 CAS loop under contention:
// balanced concurrent Add/Sub must leave the gauge near zero (no lost update).
func TestGauge_AddSub_Concurrent(t *testing.T) {
	g := NewGauge("race_gauge_cas", "test")
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			for range 1000 {
				g.Add(0.1)
				g.Sub(0.1)
			}
		})
	}
	wg.Wait()
	if v := g.Get(); math.Abs(v) > 1.0 {
		t.Errorf("gauge drift too large: %f", v)
	}
}

func TestLabeledGauge_ResetConcurrent(t *testing.T) {
	lg := NewLabeledGauge("lg_conc_reset", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			for j := range 20 {
				lg.Set(float64(j), strconv.Itoa(i))
			}
		})
	}
	for range 10 {
		wg.Go(func() {
			lg.Reset()
		})
	}
	wg.Wait()
}

func TestLabeledGauge_DeleteConcurrent(t *testing.T) {
	lg := NewLabeledGauge("lg_conc_del", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			lg.Set(float64(i), key)
			lg.Delete(key)
		})
	}
	wg.Wait()
}

// TestLabeledGauge_SetResetDelete_ConcurrentScrape races Set/Reset/Delete
// against a concurrent scrape (WriteLabeledGauge) to pin the nil-map-slot skip
// in the family materialiser: a key nulled mid-scrape must not be dereferenced.
func TestLabeledGauge_SetResetDelete_ConcurrentScrape(t *testing.T) {
	lg := NewLabeledGauge("rt6_race_gauge", "race test", []string{"id"})
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := range 20 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			for {
				select {
				case <-stop:
					return
				default:
					lg.Set(float64(i), key)
				}
			}
		})
	}
	for i := range 10 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			for {
				select {
				case <-stop:
					return
				default:
					lg.Delete(key)
				}
			}
		})
	}
	for range 5 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					lg.Reset()
				}
			}
		})
	}
	for range 5 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					var b strings.Builder
					WriteLabeledGauge(&b, lg)
				}
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let producers/scrapers actually overlap
	close(stop)
	wg.Wait()
}

// TestGauge_reservedSuffixNamesRenderVerbatim verifies suffix conventions
// never mangle other metric types: a gauge whose name ends in _total or
// _bucket is exposed under its exact name (no stripping, no derived series).
func TestGauge_reservedSuffixNamesRenderVerbatim(t *testing.T) {
	gt := NewGauge("weird_total", "a gauge whose name ends in _total")
	gt.Set(7)
	var b strings.Builder
	WriteGauge(&b, gt)
	if out := b.String(); !strings.Contains(out, "# TYPE weird_total gauge") || !strings.Contains(out, "weird_total 7") {
		t.Errorf("gauge named _total mangled:\n%s", out)
	}

	gb := NewGauge("my_bucket", "a gauge whose name ends in _bucket")
	gb.Set(3)
	b.Reset()
	WriteGauge(&b, gb)
	if out := b.String(); !strings.Contains(out, "my_bucket 3") {
		t.Errorf("gauge named _bucket mangled: %s", out)
	}
}

// TestGaugeRecordPathIsAllocationFree pins the gauge's record path at zero.
// Set stores a bit pattern and the arithmetic forms run a CAS loop, so none of
// them has any reason to allocate; Get is included because a gauge read is on
// the same call sites (a caller that reads-modify-writes calls both). The
// labeled series is created in setup, so every run of that closure takes
// loadOrStore's fast path.
func TestGaugeRecordPathIsAllocationFree(t *testing.T) {
	g := NewGauge("alloc_gauge", "allocation contract")
	lg := NewLabeledGauge("alloc_labeled_gauge", "allocation contract", []string{"kind"})
	lg.Set(1, "scan")

	cases := []struct {
		name string
		call string
		fn   func()
	}{
		{"set", "Gauge.Set(1.5)", func() { g.Set(1.5) }},
		{"add", "Gauge.Add(1.5)", func() { g.Add(1.5) }},
		{"sub", "Gauge.Sub(1.5)", func() { g.Sub(1.5) }},
		{"inc", "Gauge.Inc()", func() { g.Inc() }},
		{"dec", "Gauge.Dec()", func() { g.Dec() }},
		{"get", "Gauge.Get()", func() { _ = g.Get() }},
		{"labeled_set", `LabeledGauge.Set(1.5, "scan")`, func() { lg.Set(1.5, "scan") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(allocRuns, tc.fn); got != 0 {
				t.Errorf("%s allocated %v times per run, want 0: gauges are set from request handlers and collect loops, so an allocation here recurs at that rate", tc.call, got)
			}
		})
	}
}

// TestLabeledGaugeNewSeriesCostIsConstant is the gauge half of the
// series-creation contract. LabeledGauge stores an *atomic.Uint64 where the
// counter stores an *atomic.Int64, so the constant is its own measurement
// rather than one inherited from the counter, and the two axes asserted here
// are the ones a gauge is exposed to: caller-owned label values whose length
// it does not choose, and a series map that only grows in a long-running
// process. Arity independence is pinned once, on the counter.
//
// Each run creates a NEW series; the label values are pre-built.
func TestLabeledGaugeNewSeriesCostIsConstant(t *testing.T) {
	// The 20k-series pre-fill crosses cardinalityWarnThreshold; keep its
	// warning out of the test output. The crossing is in setup.
	captureDebugLogs(t)

	t.Run("independent_of_value_length", func(t *testing.T) {
		lengths := []int{8, 1024, 65536}
		counts := make([]float64, len(lengths))
		for i, n := range lengths {
			keys := newSeriesLabelValues(allocRuns, 1, n)
			lg := NewLabeledGauge("alloc_gauge_vallen", "allocation contract", []string{"k"})
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lg.Set(1, keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledGauge.Set(a new series with %d-byte label values) allocated %v times per run, want %v (its count at %d bytes): label values are caller-owned, so a count that tracks their SIZE lets whoever supplies the text choose the cost",
					lengths[i], got, counts[0], lengths[0])
			}
		}
		t.Logf("a constant %v allocations per new series from %d- to %d-byte label values", counts[0], lengths[0], lengths[len(lengths)-1])
	})

	t.Run("independent_of_existing_series", func(t *testing.T) {
		existing := []int{0, 1_000, 20_000}
		counts := make([]float64, len(existing))
		for i, n := range existing {
			keys := newSeriesLabelValues(allocRuns, 1, 8)
			lg := NewLabeledGauge("alloc_gauge_existing", "allocation contract", []string{"k"})
			for k := range n {
				lg.Set(1, "pre"+strconv.Itoa(k))
			}
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lg.Set(1, keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledGauge.Set(a new series into a metric already holding %d) allocated %v times per run, want %v (its count at %d existing): a per-existing-series cost makes series creation quadratic over a process's lifetime",
					existing[i], got, counts[0], existing[0])
			}
		}
		t.Logf("a constant %v allocations per new series with %d to %d series already present", counts[0], existing[0], existing[len(existing)-1])
	})
}
