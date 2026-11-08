package measure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// countFamiliesWithPrefix counts collected families whose name has prefix.
func countFamiliesWithPrefix(fams []metricFamily, prefix string) int {
	n := 0
	for i := range fams {
		if strings.HasPrefix(fams[i].name, prefix) {
			n++
		}
	}
	return n
}

func TestLabeledCounterFamily_skipsConcurrentlyDeletedKey(t *testing.T) {
	lc := NewLabeledCounter("nilguard_total", "help", []string{"k"})
	lc.Add(5, "live")
	// A concurrent Delete/Reset can null a map slot between family()'s key
	// snapshot and its value load; inject that state to exercise the guard
	// deterministically. "ghost" sorts before "live", so it is processed first
	// and a missing guard would panic on v.Load() of the nil *atomic.Int64.
	lc.vals[labelKey{"ghost"}] = nil

	fam, ok := lc.family()
	if !ok {
		t.Fatal("family() ok = false, want true (a live key remains)")
	}
	if len(fam.samples) != 1 {
		t.Fatalf("family() emitted %d samples, want 1 (nil-valued key must be skipped)", len(fam.samples))
	}
	if fam.samples[0].value != "5" {
		t.Errorf("family() sample value = %q, want \"5\"", fam.samples[0].value)
	}
}

func TestLabeledHistogramFamily_skipsConcurrentlyDeletedKey(t *testing.T) {
	lh := NewLabeledHistogram("nilguard_seconds", "help", []string{"k"})
	lh.Observe(0.5, "live")
	// Same concurrent-Delete race as the labeled counter: a nil map slot must be
	// skipped, not dereferenced (h.snapshot() on a nil *Histogram panics). "ghost"
	// sorts before "live" so it is processed first, pinning the guard.
	lh.vals[labelKey{"ghost"}] = nil

	fam, ok := lh.family()
	if !ok {
		t.Fatal("family() ok = false, want true (a live key remains)")
	}
	wantSamples := len(DefaultBuckets()) + 3 // finite buckets + +Inf + _sum + _count
	if len(fam.samples) != wantSamples {
		t.Fatalf("family() emitted %d samples, want %d (nil-valued key must be skipped)", len(fam.samples), wantSamples)
	}
}

// The next three tests are the boundary companions to the
// *Family_skipsConcurrentlyDeletedKey tests: when a racing Delete/Reset nulls
// EVERY observed key's slot (not just some), no sample survives, so family()
// must report ok=false. collect() emits a family only when ok is true, so a
// false-positive ok would leak a bare "# HELP"/"# TYPE" block with no samples —
// malformed exposition that both parsers reject.

func TestLabeledCounterFamily_reportsNotOkWhenAllObservedKeysNilValued(t *testing.T) {
	lc := NewLabeledCounter("allnil_total", "help", []string{"k"})
	// The sole observed key, nulled between family()'s key snapshot and its
	// value load. keys stays non-empty (so the len(keys)==0 early-out does not
	// fire), but the guarded loop appends nothing.
	lc.vals[labelKey{"ghost"}] = nil

	fam, ok := lc.family()
	if ok {
		t.Error("family() ok = true, want false when every observed key is nil-valued")
	}
	if len(fam.samples) != 0 {
		t.Errorf("family() emitted %d samples, want 0", len(fam.samples))
	}
}

func TestLabeledGaugeFamily_reportsNotOkWhenAllObservedKeysNilValued(t *testing.T) {
	lg := NewLabeledGauge("allnil", "help", []string{"k"})
	lg.vals[labelKey{"ghost"}] = nil

	fam, ok := lg.family()
	if ok {
		t.Error("family() ok = true, want false when every observed key is nil-valued")
	}
	if len(fam.samples) != 0 {
		t.Errorf("family() emitted %d samples, want 0", len(fam.samples))
	}
}

func TestLabeledHistogramFamily_reportsNotOkWhenAllObservedKeysNilValued(t *testing.T) {
	lh := NewLabeledHistogram("allnil_seconds", "help", []string{"k"})
	lh.vals[labelKey{"ghost"}] = nil

	fam, ok := lh.family()
	if ok {
		t.Error("family() ok = true, want false when every observed key is nil-valued")
	}
	if len(fam.samples) != 0 {
		t.Errorf("family() emitted %d samples, want 0", len(fam.samples))
	}
}

// The five Collect_capacity tests register more metrics of a single type than
// the built-in process-family count, then read them all back. This guards each
// term of collect()'s preallocation sum: a wrong sign on any term would make
// the make() capacity negative and panic before any family is returned.

func TestCollect_capacityCounters(t *testing.T) {
	const prefix = "capctr_"
	reg := NewRegistry("")
	for i := range 20 {
		reg.MustRegister(NewCounter(prefix+strconv.Itoa(i)+"_total", "h"))
	}

	fams := reg.collect(context.Background())

	if got := countFamiliesWithPrefix(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

func TestCollect_capacityLabeledGauges(t *testing.T) {
	const prefix = "caplg_"
	reg := NewRegistry("")
	for i := range 20 {
		lg := NewLabeledGauge(prefix+strconv.Itoa(i), "h", []string{"k"})
		reg.MustRegister(lg)
		lg.Set(1, "v") // a labeled metric only emits a family once a combo is set
	}

	fams := reg.collect(context.Background())

	if got := countFamiliesWithPrefix(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

func TestCollect_capacityHistograms(t *testing.T) {
	const prefix = "caphist_"
	reg := NewRegistry("")
	for i := range 20 {
		reg.MustRegister(NewHistogram(prefix+strconv.Itoa(i), "h"))
	}

	fams := reg.collect(context.Background())

	if got := countFamiliesWithPrefix(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

func TestCollect_capacityLabeledHistograms(t *testing.T) {
	const prefix = "caplh_"
	reg := NewRegistry("")
	for i := range 20 {
		lh := NewLabeledHistogram(prefix+strconv.Itoa(i), "h", []string{"k"})
		reg.MustRegister(lh)
		lh.Observe(0.1, "v")
	}

	fams := reg.collect(context.Background())

	if got := countFamiliesWithPrefix(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

func TestCollect_capacityGauges(t *testing.T) {
	const prefix = "capg_"
	reg := NewRegistry("")
	for i := range 20 {
		reg.MustRegister(NewGauge(prefix+strconv.Itoa(i), "h"))
	}

	fams := reg.collect(context.Background())

	if got := countFamiliesWithPrefix(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

// TestWrite_ErroredMetricWritesNothing pins the v4 invariant on the direct
// write path: a metric value carrying a construction error emits NOTHING
// through its low-level Write* function — never a # HELP/# TYPE block for a
// family that could corrupt the scrape. One case per Write* shim, each with a
// different captured-error class. Serial: it captures slog.Default (the
// record calls staging each metric emit the one-time inert-record warning).
func TestWrite_ErroredMetricWritesNothing(t *testing.T) {
	_ = captureDebugLogs(t) // absorb the expected one-time inert-record warnings
	cases := []struct {
		name  string
		write func(b *strings.Builder)
	}{
		{"counter, invalid name", func(b *strings.Builder) {
			c := NewCounter("bad-name", "x")
			c.Inc()
			writeCounter(b, c)
		}},
		{"gauge, invalid name", func(b *strings.Builder) {
			g := NewGauge("bad-name", "x")
			g.Set(4)
			writeGauge(b, g)
		}},
		{"labeled counter, invalid label name", func(b *strings.Builder) {
			lc := NewLabeledCounter("wn_ok_total", "x", []string{"bad-label"})
			lc.Inc("v")
			writeLabeledCounter(b, lc)
		}},
		{"labeled gauge, reserved label prefix", func(b *strings.Builder) {
			lg := NewLabeledGauge("wn_ok_gauge", "x", []string{"__rsv"})
			lg.Set(1, "v")
			writeLabeledGauge(b, lg)
		}},
		{"histogram, unordered buckets", func(b *strings.Builder) {
			h := NewHistogram("wn_ok_hist", "x", WithBuckets([]float64{2, 1}))
			h.Observe(0.5)
			writeHistogram(b, h)
		}},
		{"labeled histogram, reserved le label", func(b *strings.Builder) {
			lh := NewLabeledHistogram("wn_ok_seconds", "x", []string{"le"})
			lh.Observe(0.5, "v")
			writeLabeledHistogram(b, lh)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			tc.write(&b)
			if b.Len() != 0 {
				t.Errorf("Write* of a metric carrying a construction error emitted %q, want nothing", b.String())
			}
		})
	}
}

// TestEncoders_bothFormats_allTypes verifies every metric type appears in the
// exposition and that no OpenMetrics-style "# EOF" trailer is emitted.
func TestEncoders_bothFormats_allTypes(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("parity_counter", "A counter")
	g := NewGauge("parity_gauge", "A gauge")
	h := NewHistogram("parity_hist", "A histogram", WithBuckets([]float64{0.1, 1}))
	lc := NewLabeledCounter("parity_lc", "Labeled counter", []string{"k"})
	lg := NewLabeledGauge("parity_lg", "Labeled gauge", []string{"k"})
	lh := NewLabeledHistogram("parity_lh", "Labeled histogram", []string{"k"}, WithBuckets([]float64{0.5}))

	r.MustRegister(c, g, h, lc, lg, lh)

	c.Add(10)
	g.Set(3.14)
	h.Observe(0.05)
	h.Observe(5.0)
	lc.Inc("v1")
	lg.Set(99, "v1")
	lh.Observe(0.3, "v1")

	rec1 := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	prom := rec1.Body.String()

	for _, name := range []string{"parity_counter", "parity_gauge", "parity_hist", "parity_lc", "parity_lg", "parity_lh"} {
		if !strings.Contains(prom, name) {
			t.Errorf("Prometheus missing %s", name)
		}
	}

	if strings.Contains(prom, "# EOF") {
		t.Error("Prometheus should not have EOF")
	}
}

// TestHelpEscaping_Prometheus locks the HELP-escaping contract: Prometheus
// escapes backslash and newline but leaves the double-quote raw.
func TestHelpEscaping_Prometheus(t *testing.T) {
	help := "line1\\line2\nline3\"quoted\""
	c := NewCounter("help_esc", help)
	c.Inc()

	var b strings.Builder
	writeCounter(&b, c)
	if out := b.String(); !strings.Contains(out, `# HELP help_esc line1\\line2\nline3"quoted"`) {
		t.Errorf("Prometheus HELP escaping wrong:\n%s", out)
	}
}

// TestExpositionCostDependsOnLabelValueContent measures what escaping costs at
// scrape time. It is the only contract here whose answer turns on what the
// input CONTAINS rather than on how much of it there is, and the answer is yes:
// a label value carrying a character the text format must escape (`\`, `"` or a
// newline) costs measurably MORE to expose than one that does not, and the
// surcharge grows with the value's length.
//
// Both halves are worth pinning rather than only the flattering one. Label
// values are caller-owned and the README already warns that they may carry
// upstream text, so whoever supplies that text also chooses which of these two
// paths a scrape takes. What the library owes is that the choice buys a bounded
// surcharge: it must stay charged per VALUE and per output doubling, never per
// byte. A per-byte escape cost would let a caller feeding a quoted 64 KiB
// string into a label turn one series into thousands of allocations on every
// scrape — an amplification vector reachable from ordinary input, since none of
// this is on the record path where a cardinality warning would fire.
//
// Measured at 100 series: the escaped path costs about 3 allocations more per
// series at 32 bytes and 11 at 2048 (3.3 and 11.1 under -race — the surcharge
// is a difference between two counts, so the detector's overhead very nearly
// cancels). Sixty-four times the bytes for under four times the surcharge is
// the escaper's output buffer doubling, which is the shape a correct
// implementation has; a per-byte one would have cost 64 times as much.
func TestExpositionCostDependsOnLabelValueContent(t *testing.T) {
	// maxSurchargePerSeries bounds the escaped path's extra allocations per
	// series at the smallest value size, and maxSurchargeGrowth bounds how much
	// that surcharge may grow when the value grows 64x. The growth bound is the
	// load-bearing one: it is what separates "charged per doubling" (measured
	// 3.6x) from "charged per byte" (which would be 64x).
	const (
		series                = 100
		maxSurchargePerSeries = 4.0
		maxSurchargeGrowth    = 6.0
	)

	// buildRegistry returns a registry holding one labeled counter with
	// `series` series whose label value is valueLen bytes of fill. The fill is
	// a quote on the escaped path, so EVERY byte needs escaping: that is the
	// worst case the grammar allows and the one a caller cannot make worse.
	//
	// The measurement below goes through collect+encode rather than the
	// handler: it is a difference between two counts, so the handler's fixed
	// cost would cancel out of it anyway, and the encoder is where the escaping
	// happens.
	buildRegistry := func(fill string, valueLen int) *Registry {
		r := NewRegistry("escapecost")
		lc := NewLabeledCounter("content_total", "escaping cost contract", []string{"v"})
		r.MustRegister(lc)
		for i := range series {
			lc.Inc(strings.Repeat(fill, valueLen) + strconv.Itoa(i))
		}
		return r
	}

	lengths := []int{32, 2048}
	surcharge := make([]float64, len(lengths))
	for i, n := range lengths {
		plainRegistry := buildRegistry("a", n)
		escapedRegistry := buildRegistry(`"`, n)

		// The fixture asserts its own regime: if the escaped output ever stops
		// being longer than the plain one, the escaper is no longer on this
		// path and the surcharge below would be measuring nothing.
		plainOut := encodePrometheus(plainRegistry.collect(context.Background()))
		escapedOut := encodePrometheus(escapedRegistry.collect(context.Background()))
		if len(escapedOut) <= len(plainOut) {
			t.Fatalf("with %d-byte all-quote label values the exposition is %d bytes against %d for plain values, want longer: the fixture is meant to exercise the escaper",
				n, len(escapedOut), len(plainOut))
		}

		plainAllocs := testing.AllocsPerRun(scrapeAllocRuns, func() { _ = encodePrometheus(plainRegistry.collect(context.Background())) })
		escapedAllocs := testing.AllocsPerRun(scrapeAllocRuns, func() { _ = encodePrometheus(escapedRegistry.collect(context.Background())) })
		surcharge[i] = (escapedAllocs - plainAllocs) / series
		t.Logf("%d series of %d-byte label values: %v allocations per scrape plain, %v all-escaped, a surcharge of %.3f per series",
			series, n, plainAllocs, escapedAllocs, surcharge[i])
	}

	if surcharge[0] > maxSurchargePerSeries {
		t.Errorf("exposing %d series of %d-byte all-escaped label values cost %.3f extra allocations per series, want at most %v: escaping is charged per value, so this number multiplies by the series count on every scrape",
			series, lengths[0], surcharge[0], maxSurchargePerSeries)
	}
	growth := surcharge[len(surcharge)-1] / surcharge[0]
	factor := lengths[len(lengths)-1] / lengths[0]
	if growth > maxSurchargeGrowth {
		t.Errorf("growing an all-escaped label value %dx (from %d to %d bytes) grew the escape surcharge %.2fx (%.3f to %.3f allocations per series), want at most %vx: the escaper's cost must follow its output buffer's doublings, not the byte count, or a caller who supplies a large quoted value multiplies every scrape's cost",
			factor, lengths[0], lengths[len(lengths)-1], growth, surcharge[0], surcharge[len(surcharge)-1], maxSurchargeGrowth)
	}
	t.Logf("a %dx longer all-escaped label value costs %.2fx the surcharge, not %dx", factor, growth, factor)
}
