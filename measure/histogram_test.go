package measure

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestNewHistogram_defaultBuckets(t *testing.T) {
	h := NewHistogram("default_hist", "test")
	if !slices.Equal(h.bounds, DefaultBuckets()) {
		t.Errorf("NewHistogram no-opts bounds = %v, want DefaultBuckets %v", h.bounds, DefaultBuckets())
	}
}

func TestNewLabeledHistogram_defaultBuckets(t *testing.T) {
	lh := NewLabeledHistogram("default_lh", "test", []string{"k"})
	if !slices.Equal(lh.bounds, DefaultBuckets()) {
		t.Errorf("NewLabeledHistogram no-opts bounds = %v, want DefaultBuckets %v", lh.bounds, DefaultBuckets())
	}
}

func TestNewHistogram_withBucketsOverrides(t *testing.T) {
	h := NewHistogram("custom_hist", "test", WithBuckets([]float64{1, 2, 3}))
	if !slices.Equal(h.bounds, []float64{1, 2, 3}) {
		t.Errorf("bounds = %v, want [1 2 3]", h.bounds)
	}
}

// TestNewHistogram_invalidBuckets_errorsAtRegister verifies the bucket
// contract is validated at construction — bounds must be strictly increasing
// finite values — with the violation captured into the histogram and surfaced
// at registration. Each case pins a violation that would otherwise emit
// duplicate or non-monotonic le series that parsers reject.
func TestNewHistogram_invalidBuckets_errorsAtRegister(t *testing.T) {
	tests := []struct {
		name   string
		bounds []float64
		want   string
	}{
		{"duplicates", []float64{1, 1, 5, 5}, `bounds for metric "inv_duplicates" must be strictly increasing`},
		{"unsorted", []float64{2, 1}, `bounds for metric "inv_unsorted" must be strictly increasing`},
		{"nan", []float64{math.NaN(), 0.5, 1}, `bound for metric "inv_nan" must be finite`},
		{"pos_inf", []float64{0.5, math.Inf(1)}, `bound for metric "inv_pos_inf" must be finite`},
		{"neg_inf", []float64{math.Inf(-1), 0, 1}, `bound for metric "inv_neg_inf" must be finite`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHistogram("inv_"+tc.name, "test", WithBuckets(tc.bounds)) // must not panic
			mustRegisterError(t, NewRegistry(""), h, tc.want)
		})
	}
}

// TestNewHistogram_nilOptions verifies a nil Option is skipped safely: nils
// alone leave DefaultBuckets in place, and a nil between real options does not
// disturb last-wins ordering.
func TestNewHistogram_nilOptions(t *testing.T) {
	t.Run("single nil uses defaults", func(t *testing.T) {
		h := NewHistogram("nil_h", "test", nil)
		h.Observe(0.5)
		if h.count.Load() != 1 {
			t.Errorf("count = %d, want 1", h.count.Load())
		}
		if len(h.bounds) != len(DefaultBuckets()) {
			t.Errorf("bounds len = %d, want %d", len(h.bounds), len(DefaultBuckets()))
		}
	})
	t.Run("multiple nils use defaults", func(t *testing.T) {
		h := NewHistogram("multi_nil_h", "test", nil, nil, nil)
		if len(h.bounds) != len(DefaultBuckets()) {
			t.Errorf("bounds len = %d, want %d", len(h.bounds), len(DefaultBuckets()))
		}
	})
	t.Run("nil between real options, last wins", func(t *testing.T) {
		h := NewHistogram("nil_between_h", "test",
			WithBuckets([]float64{1, 2}),
			nil,
			WithBuckets([]float64{10, 20, 30}),
		)
		if !slices.Equal(h.bounds, []float64{10, 20, 30}) {
			t.Errorf("bounds = %v, want [10 20 30]", h.bounds)
		}
	})
}

func TestNewLabeledHistogram_nilOption(t *testing.T) {
	lh := NewLabeledHistogram("nil_lh", "test", []string{"k"}, nil)
	lh.Observe(0.1, "v")
	if lh.vals[labelKey{"v"}].count.Load() != 1 {
		t.Error("observe failed after nil option")
	}
}

// TestWithBuckets_doesNotMutateInput verifies NewHistogram clones the bound
// slice, so the caller's slice is never modified.
func TestWithBuckets_doesNotMutateInput(t *testing.T) {
	input := []float64{1, 3, 5}
	original := slices.Clone(input)
	NewHistogram("nomutate_hist", "test", WithBuckets(input))
	if !slices.Equal(input, original) {
		t.Errorf("WithBuckets mutated input: got %v, want %v", input, original)
	}
}

// TestNewHistogram_callerMutationAfterConstructionIsolated verifies the
// constructor owns its bucket slice: mutating the caller's slice after
// construction must not alter the histogram's bounds.
func TestNewHistogram_callerMutationAfterConstructionIsolated(t *testing.T) {
	input := []float64{1, 2, 3}
	h := NewHistogram("mutafter_hist", "test", WithBuckets(input))
	input[0] = 99
	if !slices.Equal(h.bounds, []float64{1, 2, 3}) {
		t.Errorf("h.bounds = %v, want [1 2 3] (caller mutation leaked into owned bounds)", h.bounds)
	}
}

func TestWithBuckets_lastWins(t *testing.T) {
	h := NewHistogram("multi_buckets_hist", "test",
		WithBuckets([]float64{1, 2, 3}),
		WithBuckets([]float64{10, 20}),
	)
	if !slices.Equal(h.bounds, []float64{10, 20}) {
		t.Errorf("expected last WithBuckets to win, got %v", h.bounds)
	}
}

// TestNewHistogram_emptyAndNilBuckets covers the degenerate bound sets: an empty
// or nil slice yields a histogram with only the implicit +Inf bucket.
func TestNewHistogram_emptyAndNilBuckets(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		h := NewHistogram("empty_buckets", "test", WithBuckets([]float64{}))
		h.Observe(1.0)
		h.Observe(5.0)

		var b strings.Builder
		writeHistogram(&b, h)
		if out := b.String(); !strings.Contains(out, `empty_buckets_bucket{le="+Inf"} 2`) {
			t.Errorf("empty buckets wrong: %s", out)
		}
		if h.count.Load() != 2 {
			t.Errorf("count = %d, want 2", h.count.Load())
		}
	})
	t.Run("nil slice", func(t *testing.T) {
		h := NewHistogram("nil_slice_buckets", "test", WithBuckets(nil))
		if len(h.bounds) != 0 {
			t.Errorf("nil slice bounds = %d, want 0", len(h.bounds))
		}
		h.Observe(1.0)
		if h.count.Load() != 1 {
			t.Errorf("count = %d, want 1", h.count.Load())
		}
	})
}

// TestLabeledHistogram_emptyBuckets covers a labeled histogram constructed with
// an empty bound set: only the implicit +Inf bucket, _sum, and _count are
// emitted per label combination (the labeled companion to
// TestNewHistogram_emptyAndNilBuckets).
func TestLabeledHistogram_emptyBuckets(t *testing.T) {
	lh := NewLabeledHistogram("lhx_empty_buckets", "test", []string{"k"}, WithBuckets(nil))
	lh.Observe(1.5, "v")
	lh.Observe(0.5, "v")

	fam, ok := lh.family()
	if !ok {
		t.Fatal("family() ok = false, want true")
	}
	if len(fam.samples) != 3 {
		t.Fatalf("family() emitted %d samples, want 3 (+Inf bucket, _sum, _count)", len(fam.samples))
	}

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	out := b.String()
	for _, want := range []string{
		`lhx_empty_buckets_bucket{k="v",le="+Inf"} 2`,
		`lhx_empty_buckets_sum{k="v"} 2`,
		`lhx_empty_buckets_count{k="v"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-bucket labeled histogram missing %q:\n%s", want, out)
		}
	}
}

func TestHistogramObserve(t *testing.T) {
	h := NewHistogram("test_hist", "test")
	h.Observe(0.003) // <= 0.005
	h.Observe(0.05)  // <= 0.05
	h.Observe(2.0)   // > 1.0, only +Inf

	if got := h.count.Load(); got != 3 {
		t.Errorf("Histogram.count = %d, want 3", got)
	}
	sum := math.Float64frombits(h.sumBits.Load())
	if math.Abs(sum-2.053) > 0.0001 {
		t.Errorf("Histogram.sum = %f, want ~2.053", sum)
	}
	if got := h.buckets[0].Load(); got != 1 {
		t.Errorf("bucket[0] = %d, want 1", got)
	}
	if got := h.buckets[len(h.bounds)].Load(); got != 3 {
		t.Errorf("bucket[+Inf] = %d, want 3", got)
	}
}

func TestHistogramCustomBuckets(t *testing.T) {
	h := NewHistogram("test_custom_hist", "test", WithBuckets([]float64{1, 5, 10}))
	h.Observe(0.5)
	h.Observe(3)
	h.Observe(7)
	h.Observe(20)

	if got := h.buckets[0].Load(); got != 1 {
		t.Errorf("bucket[<=1] = %d, want 1", got)
	}
	if got := h.buckets[1].Load(); got != 2 {
		t.Errorf("bucket[<=5] = %d, want 2", got)
	}
	if got := h.buckets[2].Load(); got != 3 {
		t.Errorf("bucket[<=10] = %d, want 3", got)
	}
	if got := h.buckets[3].Load(); got != 4 {
		t.Errorf("bucket[+Inf] = %d, want 4", got)
	}
}

func TestHistogram_Observe_valueEqualToBound_countsInThatBucket(t *testing.T) {
	// le is "less than or equal": an observation exactly equal to a bound
	// must be counted in that bound's cumulative bucket.
	h := NewHistogram("boundary_hist", "test", WithBuckets([]float64{0.1, 0.5, 1}))

	h.Observe(0.1) // exactly the first bound
	h.Observe(0.5) // exactly the second bound
	h.Observe(1)   // exactly the third bound

	_, count, bucketVals := h.snapshot()

	if count != 3 {
		t.Errorf("Observe boundary values: count = %d, want 3", count)
	}
	// Cumulative: le=0.1 -> {0.1}=1; le=0.5 -> {0.1,0.5}=2; le=1 -> all=3; +Inf=3.
	want := []int64{1, 2, 3, 3}
	for i, w := range want {
		if got := bucketVals[i]; got != w {
			t.Errorf("bucket[%d] = %d, want %d (boundary inclusive)", i, got, w)
		}
	}
}

// TestHistogram_zeroBucketBoundary pins le="0" cumulative counting: an
// observation exactly at the zero bound (and a negative one) count as <= 0.
func TestHistogram_zeroBucketBoundary(t *testing.T) {
	h := NewHistogram("zero_bucket_hist", "test", WithBuckets([]float64{0}))
	h.Observe(0)    // exactly at boundary
	h.Observe(-0.1) // below zero
	h.Observe(0.1)  // above
	if h.count.Load() != 3 {
		t.Errorf("count = %d, want 3", h.count.Load())
	}
	// le="0" contains obs <= 0: that's 0 and -0.1 = 2.
	if h.buckets[0].Load() != 2 {
		t.Errorf("le=0 bucket = %d, want 2", h.buckets[0].Load())
	}
}

func TestHistogram_NegativeBuckets(t *testing.T) {
	h := NewHistogram("neg_buckets", "test", WithBuckets([]float64{-10, -1, 0, 1, 10}))
	h.Observe(-5)
	h.Observe(0)
	h.Observe(5)

	var b strings.Builder
	writeHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, `neg_buckets_bucket{le="-10"} 0`) {
		t.Errorf("negative bucket wrong: %s", out)
	}
	if !strings.Contains(out, `neg_buckets_bucket{le="-1"} 1`) {
		t.Errorf("-1 bucket wrong: %s", out)
	}
	if h.count.Load() != 3 {
		t.Errorf("count wrong: %d", h.count.Load())
	}
}

func TestHistogram_NaNObserve(t *testing.T) {
	h := NewHistogram("nan_obs", "test", WithBuckets([]float64{0.1, 1}))
	h.Observe(math.NaN())
	// NaN should not panic, count should increment.
	if h.count.Load() != 1 {
		t.Errorf("NaN observe count wrong: %d", h.count.Load())
	}
}

func TestHistogram_InfObserve(t *testing.T) {
	h := NewHistogram("inf_obs", "test", WithBuckets([]float64{0.1, 1}))
	h.Observe(math.Inf(1))
	h.Observe(math.Inf(-1))
	if h.count.Load() != 2 {
		t.Errorf("Inf observe count wrong: %d", h.count.Load())
	}
	// +Inf observation lands only in the +Inf bucket; -Inf is <= every bound so
	// it lands everywhere including +Inf. The +Inf bucket therefore counts both.
	infBucket := h.buckets[len(h.bounds)].Load()
	if infBucket != 2 {
		t.Errorf("+Inf bucket = %d, want 2", infBucket)
	}
}

func TestHistogram_SingleBucket(t *testing.T) {
	h := NewHistogram("single_bucket", "test", WithBuckets([]float64{1}))
	h.Observe(0.5)
	h.Observe(1.5)

	var b strings.Builder
	writeHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, `single_bucket_bucket{le="1"} 1`) {
		t.Errorf("single bucket wrong: %s", out)
	}
	if !strings.Contains(out, `single_bucket_bucket{le="+Inf"} 2`) {
		t.Errorf("single bucket +Inf wrong: %s", out)
	}
}

func TestHistogramSnapshot(t *testing.T) {
	h := NewHistogram("snap_test", "snapshot",
		WithBuckets([]float64{0.1, 0.5, 1}))
	h.Observe(0.05) // lands in <=0.1, <=0.5, <=1, +Inf
	h.Observe(0.3)  // lands in <=0.5, <=1, +Inf
	h.Observe(2.0)  // lands in +Inf only
	sum, count, buckets := h.snapshot()
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	wantSum := 0.05 + 0.3 + 2.0
	if math.Abs(sum-wantSum) > 1e-9 {
		t.Errorf("sum = %v, want %v", sum, wantSum)
	}
	want := []int64{1, 2, 2, 3}
	for i := range want {
		if buckets[i] != want[i] {
			t.Errorf("buckets[%d] = %d, want %d", i, buckets[i], want[i])
		}
	}
}

func TestWriteHistogramFormat(t *testing.T) {
	h := NewHistogram("fmt_hist", "test help")
	h.Observe(0.01)

	var b strings.Builder
	writeHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, "# HELP fmt_hist test help") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE fmt_hist histogram") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, `fmt_hist_bucket{le="+Inf"} 1`) {
		t.Errorf("missing +Inf bucket: %s", out)
	}
	if !strings.Contains(out, "fmt_hist_sum") {
		t.Error("missing _sum line")
	}
	if !strings.Contains(out, "fmt_hist_count 1") {
		t.Error("missing _count line")
	}
}

func TestWriteHistogram_SumRendering(t *testing.T) {
	t.Run("tiny sum keeps precision", func(t *testing.T) {
		h := NewHistogram("hist_sum_tiny", "test")
		h.Observe(1e-7) // a fixed-precision %.6f would floor this sum to zero
		var b strings.Builder
		writeHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_tiny_sum 1e-07\n") {
			t.Errorf("writeHistogram sum = %q, want line %q", got, "hist_sum_tiny_sum 1e-07")
		}
	})
	t.Run("whole sum renders as bare integer", func(t *testing.T) {
		h := NewHistogram("hist_sum_whole", "test")
		h.Observe(2)
		h.Observe(2) // sum = 4
		var b strings.Builder
		writeHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_whole_sum 4\n") {
			t.Errorf("writeHistogram sum = %q, want line %q", got, "hist_sum_whole_sum 4")
		}
	})
}

func TestHistogramConcurrent(t *testing.T) {
	h := NewHistogram("conc_hist", "test")
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { h.Observe(0.01) })
	}
	wg.Wait()
	if got := h.count.Load(); got != 100 {
		t.Errorf("concurrent Histogram.count = %d, want 100", got)
	}
}

func TestLabeledHistogram(t *testing.T) {
	lh := NewLabeledHistogram("lh_test", "test", []string{"method"}, WithBuckets([]float64{0.1, 0.5, 1}))
	lh.Observe(0.05, "GET")
	lh.Observe(0.3, "GET")
	lh.Observe(2.0, "POST")

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	out := b.String()

	if !strings.Contains(out, "# TYPE lh_test histogram") {
		t.Error("missing TYPE")
	}
	if !strings.Contains(out, `lh_test_bucket{method="GET",le="0.1"} 1`) {
		t.Errorf("missing GET le=0.1 bucket: %s", out)
	}
	if !strings.Contains(out, `lh_test_bucket{method="GET",le="+Inf"} 2`) {
		t.Errorf("missing GET +Inf bucket: %s", out)
	}
	if !strings.Contains(out, `lh_test_bucket{method="POST",le="+Inf"} 1`) {
		t.Errorf("missing POST +Inf bucket: %s", out)
	}
}

func TestLabeledHistogram_Reset(t *testing.T) {
	lh := NewLabeledHistogram("lh_reset", "test", []string{"host"}, WithBuckets([]float64{0.1, 1}))
	lh.Observe(0.05, "a")
	lh.Observe(0.05, "b")
	lh.Reset()

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledHistogram_Delete(t *testing.T) {
	lh := NewLabeledHistogram("lh_delete", "test", []string{"host"}, WithBuckets([]float64{0.1, 1}))
	lh.Observe(0.05, "a")
	lh.Observe(0.05, "b")
	lh.Delete("a")

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledHistogram_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lh := NewLabeledHistogram("lh_del_panic", "test", []string{"a", "b"})
	lh.Delete("only_one")
}

func TestLabeledHistogram_ObserveArityPanic(t *testing.T) {
	lh := NewLabeledHistogram("arity_h", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lh.Observe(1.0, "only_one")
}

// TestNewLabeledHistogram_TooManyLabelsErrorsAtRegister pins the maxLabels
// cap: a ninth label is captured at construction and surfaces at registration.
func TestNewLabeledHistogram_TooManyLabelsErrorsAtRegister(t *testing.T) {
	lh := NewLabeledHistogram("lh_many", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"})
	mustRegisterError(t, NewRegistry(""), lh, `LabeledHistogram "lh_many" supports at most 8 labels`)
}

// TestNewLabeledHistogram_ExactlyMaxLabelsAllowed pins the arity guard at its
// inclusive maximum: eight labels is the legal maximum (the guard is > 8).
func TestNewLabeledHistogram_ExactlyMaxLabelsAllowed(t *testing.T) {
	lh := NewLabeledHistogram("mk_lh8", "test", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	lh.Observe(0.5, "1", "2", "3", "4", "5", "6", "7", "8") // must not panic with eight labels

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	if out := b.String(); !strings.Contains(out, `a="1",b="2",c="3",d="4",e="5",f="6",g="7",h="8"`) {
		t.Errorf("eight-label histogram not exposed correctly:\n%s", out)
	}
}

// TestNewLabeledHistogram_LabelNameLeErrorsAtRegister pins the guard that
// reserves the label name "le" for the implicit bucket bound: constructing a
// labeled histogram with an "le" label captures the reserved-label error,
// which surfaces at registration. It fails if the guard is removed or relaxed.
func TestNewLabeledHistogram_LabelNameLeErrorsAtRegister(t *testing.T) {
	lh := NewLabeledHistogram("lh_reserved_le", "test", []string{"method", "le"}) // must not panic
	mustRegisterError(t, NewRegistry(""), lh, `label name "le" for metric "lh_reserved_le" is reserved`)
}

// TestLabeledHistogram_Concurrent asserts every concurrent Observe across
// several label combinations is counted, exercising the per-key lazy histogram
// creation under contention.
func TestLabeledHistogram_Concurrent(t *testing.T) {
	lh := NewLabeledHistogram("conc_lh", "test", []string{"method", "status"},
		WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1.0}))

	var wg sync.WaitGroup
	labels := [][2]string{
		{"GET", "200"}, {"POST", "201"}, {"GET", "404"}, {"DELETE", "500"},
	}
	const N = 50
	const ops = 200
	for _, l := range labels {
		for range N {
			wg.Go(func() {
				for range ops {
					lh.Observe(0.03, l[0], l[1])
				}
			})
		}
	}
	wg.Wait()

	totalCount := int64(0)
	lh.mu.RLock()
	for _, h := range lh.vals {
		totalCount += h.count.Load()
	}
	lh.mu.RUnlock()
	expected := int64(len(labels) * N * ops)
	if totalCount != expected {
		t.Errorf("total count = %d, want %d", totalCount, expected)
	}
}

// TestTimer runs inside a synctest bubble so the elapsed duration is exact
// rather than a lower bound: on the bubble's synthetic clock a 10ms
// synctest.Sleep advances time.Since by exactly 10ms, which pins both the
// duration Timer returns and the seconds it observes. On the real clock the
// same test could only assert `d >= 10ms` and cost 10ms of wall time.
func TestTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHistogram("timer_test", "test")
		timer := NewTimer(h)
		synctest.Sleep(10 * time.Millisecond)
		if d := timer.ObserveDuration(); d != 10*time.Millisecond {
			t.Errorf("ObserveDuration() = %v, want exactly 10ms", d)
		}
		if got := h.count.Load(); got != 1 {
			t.Errorf("histogram count = %d, want 1 (timer did not observe)", got)
		}
		if sum, _, _ := h.snapshot(); sum != 0.010 {
			t.Errorf("observed sum = %v, want 0.010 (the elapsed duration in seconds)", sum)
		}
	})
}

func TestLabeledHistogramTimer(t *testing.T) {
	lh := NewLabeledHistogram("op_seconds", "op", []string{"kind"})
	r := NewRegistry("")
	r.MustRegister(lh)
	tm := lh.NewTimer("scan")
	if d := tm.ObserveDuration(); d < 0 {
		t.Fatalf("negative duration %v", d)
	}
	if out := body(t, r); !strings.Contains(out, `op_seconds_count{kind="scan"} 1`) {
		t.Errorf("labeled timer should record one observation:\n%s", out)
	}
}

// TestLabeledHistogramTimer_CopiesLabelValuesAtConstruction verifies NewTimer
// snapshots the caller's variadic label values at construction: mutating the
// caller-owned slice before ObserveDuration must not change the recorded
// label set.
func TestLabeledHistogramTimer_CopiesLabelValuesAtConstruction(t *testing.T) {
	lh := NewLabeledHistogram("op_seconds_copy", "op", []string{"kind"})
	r := NewRegistry("")
	r.MustRegister(lh)
	vals := []string{"scan"}
	tm := lh.NewTimer(vals...)
	vals[0] = "mutated"
	tm.ObserveDuration()
	out := body(t, r)
	if !strings.Contains(out, `op_seconds_copy_count{kind="scan"} 1`) {
		t.Fatalf("timer did not preserve construction-time label value:\n%s", out)
	}
	if strings.Contains(out, `kind="mutated"`) {
		t.Fatalf("caller slice mutation leaked into timer labels:\n%s", out)
	}
}

// TestLabeledHistogramNewTimer_ArityMismatchPanicsAtConstruction pins the
// eager arity guard: a wrong-arity NewTimer panics when the timer is created,
// not later in ObserveDuration.
func TestLabeledHistogramNewTimer_ArityMismatchPanicsAtConstruction(t *testing.T) {
	lh := NewLabeledHistogram("timer_arity_seconds", "test", []string{"a", "b"})
	mustPanicContaining(t, "label arity mismatch", func() { lh.NewTimer("only_one") })
}

func TestAPIBucketsWide(t *testing.T) {
	buckets := APIBuckets()
	if buckets[len(buckets)-1] < 10 {
		t.Errorf("APIBuckets should extend well past 1s for slow calls, got %v", buckets)
	}
}

// TestBucketAccessorsReturnFreshSlices verifies a caller mutating a returned
// bucket slice cannot alter the boundaries later callers receive.
func TestBucketAccessorsReturnFreshSlices(t *testing.T) {
	d := DefaultBuckets()
	d[0] = 99
	if got := DefaultBuckets(); got[0] == 99 {
		t.Error("DefaultBuckets: mutation of a returned slice leaked into later calls")
	}
	a := APIBuckets()
	a[0] = 99
	if got := APIBuckets(); got[0] == 99 {
		t.Error("APIBuckets: mutation of a returned slice leaked into later calls")
	}
}

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewHistogram("bench_hist", "bench")
	b.ReportAllocs()
	for b.Loop() {
		h.Observe(0.042)
	}
}

func BenchmarkHistogramObserve_Parallel(b *testing.B) {
	h := NewHistogram("bench_hist_par", "bench")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.Observe(0.042)
		}
	})
}

// TestHistogram_nameEndingInCount verifies a histogram whose base name already
// ends in _count still emits its derived _bucket/_sum/_count series correctly
// (the derived suffixes append to the base name without collapsing).
func TestHistogram_nameEndingInCount(t *testing.T) {
	h := NewHistogram("ops_count", "operations", WithBuckets([]float64{1, 5, 10}))
	h.Observe(3)
	var b strings.Builder
	writeHistogram(&b, h)
	out := b.String()
	for _, want := range []string{
		"# TYPE ops_count histogram",
		"ops_count_bucket",
		"ops_count_sum",
		"ops_count_count 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("histogram named _count missing %q:\n%s", want, out)
		}
	}
}

// TestHistogramObserveIsAllocationFreeAtEveryBucketPosition pins Observe at
// zero allocations wherever the observed value falls. Observe walks the bounds
// to find the first bucket the value fits, then increments every bucket from
// there up, so the branch it takes is chosen by the value: a value below the
// first bound runs the longest bucket loop, one above the last bound skips the
// loop entirely and touches only +Inf, and a value exactly ON a bound must
// land in that bucket (the comparison is `seconds <= bound`) rather than
// falling through to the next. Each of those is a different path and any of
// them could start allocating on its own, which is why the positions are
// listed rather than sampled.
//
// The two shape cases are there for a different reason: a histogram with only
// the implicit +Inf bucket and one with 64 bounds bracket the loop, so a
// regression that allocated per bucket visited would show up in the wide case
// even if it hid in the default eight.
func TestHistogramObserveIsAllocationFreeAtEveryBucketPosition(t *testing.T) {
	// DefaultBuckets is 0.005 .. 1.0, so these positions are stated against
	// it. Spelled out rather than derived, so a change to DefaultBuckets is a
	// visible edit here instead of a silently relabelled case.
	h := NewHistogram("alloc_histogram_seconds", "allocation contract")
	wide := make([]float64, 64)
	for i := range wide {
		wide[i] = float64(i + 1)
	}

	cases := []struct {
		name     string
		position string
		h        *Histogram
		v        float64
	}{
		{"below_first_bound", "below the first bound (every bucket incremented)", h, 0.001},
		{"on_first_bound", "exactly on the first bound", h, 0.005},
		{"between_bounds", "between two bounds", h, 0.0075},
		{"on_last_bound", "exactly on the last bound", h, 1.0},
		{"above_last_bound", "above the last bound (+Inf only)", h, 2.0},
		{"zero", "zero seconds", h, 0},
		{"inf_only_histogram", "into a histogram with only the +Inf bucket", NewHistogram("alloc_histogram_inf", "allocation contract", WithBuckets(nil)), 1},
		{"64_bounds", "above the last of 64 bounds", NewHistogram("alloc_histogram_wide", "allocation contract", WithBuckets(wide)), 100},
		{"64_bounds_below_first", "below the first of 64 bounds (64 buckets incremented)", NewHistogram("alloc_histogram_wide2", "allocation contract", WithBuckets(wide)), 0.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(allocRuns, func() { tc.h.Observe(tc.v) }); got != 0 {
				t.Errorf("Histogram.Observe(%v), %s, allocated %v times per run, want 0: every instrumented request and every timer lands here, so an allocation on this path is charged per observation",
					tc.v, tc.position, got)
			}
		})
	}
}

// TestLabeledHistogramObserveIsAllocationFreeOnAnExistingSeries pins the
// labeled Observe on the path a steady-state app takes: the label set exists,
// so the call is a map read plus the child histogram's own free Observe. The
// series is created in setup for exactly that reason — a closure observing a
// fresh label set every run would measure series creation, which is a bounded
// constant rather than zero (TestLabeledHistogramNewSeriesCostIsConstant).
func TestLabeledHistogramObserveIsAllocationFreeOnAnExistingSeries(t *testing.T) {
	lh := NewLabeledHistogram("alloc_labeled_histogram_seconds", "allocation contract", []string{"kind"})
	lh.Observe(0.1, "scan")

	if got := testing.AllocsPerRun(allocRuns, func() { lh.Observe(0.1, "scan") }); got != 0 {
		t.Errorf(`LabeledHistogram.Observe(0.1, "scan") on an existing series allocated %v times per run, want 0: this is the per-request latency path in every consuming app`, got)
	}
}

// TestLabeledHistogramNewSeriesCostIsConstant pins series creation for the
// labeled histogram, whose new series is a whole child Histogram rather than
// one atomic: it measures two allocations (the Histogram and its bucket slice)
// where the counter and gauge measure one.
//
// The bucket-count axis is the one specific to this type. `make([]atomic.Int64,
// len(bounds)+1)` is a single allocation whatever its length, so the constant
// must not move between an 8-bound and a 32-bound histogram; if it ever does,
// something started building per-bucket state and a metric's bucket layout
// begins multiplying the cost of every label set a caller adds. The
// existing-series axis is the quadratic guard, as on the counter.
func TestLabeledHistogramNewSeriesCostIsConstant(t *testing.T) {
	// The 20k-series pre-fill crosses cardinalityWarnThreshold; keep its
	// warning out of the test output. The crossing is in setup.
	captureDebugLogs(t)

	t.Run("independent_of_bucket_count", func(t *testing.T) {
		bounds := [][]float64{nil, DefaultBuckets(), make([]float64, 32)}
		for i := range bounds[2] {
			bounds[2][i] = float64(i + 1)
		}
		counts := make([]float64, len(bounds))
		for i, bs := range bounds {
			keys := newSeriesLabelValues(allocRuns, 1, 8)
			lh := NewLabeledHistogram("alloc_hist_buckets", "allocation contract", []string{"k"}, WithBuckets(bs))
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lh.Observe(0.1, keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledHistogram.Observe(a new series on a histogram with %d bounds) allocated %v times per run, want %v (its count at %d bounds): the bucket slice is one allocation at any length, so a count that tracks the bucket layout means every label set a caller adds now pays per bucket",
					len(bounds[i]), got, counts[0], len(bounds[0]))
			}
		}
		t.Logf("a constant %v allocations per new series from %d to %d bounds", counts[0], len(bounds[0]), len(bounds[len(bounds)-1]))
	})

	t.Run("independent_of_existing_series", func(t *testing.T) {
		existing := []int{0, 1_000, 20_000}
		counts := make([]float64, len(existing))
		for i, n := range existing {
			keys := newSeriesLabelValues(allocRuns, 1, 8)
			lh := NewLabeledHistogram("alloc_hist_existing", "allocation contract", []string{"k"})
			for k := range n {
				lh.Observe(0.1, "pre"+strconv.Itoa(k))
			}
			next := 0
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lh.Observe(0.1, keys[next]...)
				next++
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("LabeledHistogram.Observe(a new series into a metric already holding %d) allocated %v times per run, want %v (its count at %d existing): a per-existing-series cost makes series creation quadratic over a process's lifetime",
					existing[i], got, counts[0], existing[0])
			}
		}
		t.Logf("a constant %v allocations per new series with %d to %d series already present", counts[0], existing[0], existing[len(existing)-1])
	})
}

// TestTimerAllocationCost pins the timer, which the README puts on the
// `defer t.ObserveDuration()` path of a request handler, and it is the one
// place in this file where the answer is not a uniform zero.
//
// Recording is free: ObserveDuration is time.Since plus a call through the
// stored observe func, for both timer flavors. Construction is not, and the
// two flavors differ for a reason worth stating. NewTimer stores the
// histogram's Observe METHOD VALUE, which costs nothing while the timer stays
// on the stack — the whole create-and-observe round measures 0 — but two
// allocations (the Timer and the bound method) as soon as it escapes.
// (*LabeledHistogram).NewTimer always costs three: it clones the caller's
// label values and closes over them, and a closure the Timer holds escapes by
// construction. That is the price of the label set, not of the timer, and what
// matters is that it is a constant: it must not grow with the number of label
// values, or a per-label-per-timer cost lands on every instrumented request.
func TestTimerAllocationCost(t *testing.T) {
	h := NewHistogram("alloc_timer_seconds", "allocation contract")

	t.Run("observe_duration_is_free", func(t *testing.T) {
		lh := NewLabeledHistogram("alloc_timer_labeled_seconds", "allocation contract", []string{"kind"})
		lh.Observe(0.1, "scan")
		plain := NewTimer(h)
		labeled := lh.NewTimer("scan")

		if got := testing.AllocsPerRun(allocRuns, func() { _ = plain.ObserveDuration() }); got != 0 {
			t.Errorf("Timer.ObserveDuration() allocated %v times per run, want 0: it runs once per instrumented operation", got)
		}
		if got := testing.AllocsPerRun(allocRuns, func() { _ = labeled.ObserveDuration() }); got != 0 {
			t.Errorf("Timer.ObserveDuration() on a LabeledHistogram timer allocated %v times per run, want 0: it runs once per instrumented operation", got)
		}
	})

	t.Run("unlabeled_timer_end_to_end", func(t *testing.T) {
		// The measured shape is the documented one: create the timer, defer
		// the observation, return. It is 0 only because the Timer and its
		// bound method stay on the stack, so this case is a statement about
		// the shape and the compiler together — a non-zero result means the
		// Timer started escaping, which turns two allocations per instrumented
		// call back on.
		if got := testing.AllocsPerRun(allocRuns, func() {
			func() {
				timer := NewTimer(h)
				defer timer.ObserveDuration()
			}()
		}); got != 0 {
			t.Errorf("NewTimer(h) with a deferred ObserveDuration allocated %v times per run, want 0: the timer is meant to stay on the caller's stack, and the README puts this shape in a request handler", got)
		}
	})

	t.Run("labeled_timer_arity", func(t *testing.T) {
		arities := []int{1, 4, 8}
		counts := make([]float64, len(arities))
		for i, n := range arities {
			vals := make([]string, n)
			for j := range vals {
				vals[j] = "v"
			}
			lh := NewLabeledHistogram("alloc_timer_arity_seconds", "allocation contract", labelNames(n))
			lh.Observe(0.1, vals...)
			counts[i] = testing.AllocsPerRun(allocRuns, func() {
				lh.NewTimer(vals...).ObserveDuration()
			})
		}
		for i, got := range counts {
			if got != counts[0] {
				t.Errorf("(*LabeledHistogram).NewTimer(%d label values) plus ObserveDuration allocated %v times per run, want %v (its count at %d value): the clone and the closure are one allocation each at any arity, so a count that tracks the label count charges per label on every instrumented request",
					arities[i], got, counts[0], arities[0])
			}
		}
		t.Logf("a constant %v allocations per labeled timer from %d to %d label values", counts[0], arities[0], arities[len(arities)-1])
	})
}
