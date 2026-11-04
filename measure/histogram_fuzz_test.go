package measure

import (
	"math"
	"testing"
)

// FuzzHistogram_BucketPlacementInvariant checks the cumulative-bucket invariant
// for arbitrary strictly-increasing finite bounds and an arbitrary observation:
// bucket[i] is 1 iff obs <= bounds[i], and the +Inf bucket always counts it.
func FuzzHistogram_BucketPlacementInvariant(f *testing.F) {
	f.Add(0.1, 0.5, 1.0, 0.3)
	f.Add(1.0, 5.0, 10.0, 7.0)
	f.Add(-1.0, 0.0, 1.0, 0.0)

	f.Fuzz(func(t *testing.T, b1, b2, b3, obs float64) {
		for _, v := range []float64{b1, b2, b3, obs} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return
			}
		}
		// Contract: bounds must be strictly increasing finite values.
		if !(b1 < b2 && b2 < b3) {
			return
		}
		bounds := []float64{b1, b2, b3}
		h := NewHistogram("fuzz_placement", "fuzz", WithBuckets(bounds))
		h.Observe(obs)

		_, count, bucketVals := h.snapshot()
		if count != 1 {
			t.Fatalf("count = %d, want 1", count)
		}
		// bucket[i] == 1 iff obs <= bounds[i], else 0 (cumulative).
		for i, bound := range bounds {
			want := int64(0)
			if obs <= bound {
				want = 1
			}
			if got := bucketVals[i]; got != want {
				t.Errorf("obs=%v bound[%d]=%v: bucket=%d, want %d", obs, i, bound, got, want)
			}
		}
		// +Inf bucket always counts every observation.
		if got := bucketVals[len(bounds)]; got != 1 {
			t.Errorf("+Inf bucket = %d, want 1", got)
		}
	})
}

// FuzzHistogramObserve constructs a fresh histogram per fuzz input so inputs are
// independent (no cross-iteration state that would let one iteration's NaN
// poison a later finite input), then pins the single-observation contract: count
// is exactly one, the sum reflects that lone observation (NaN/±Inf preserved,
// finite echoed exactly), and each cumulative bucket counts the value iff it is
// <= the bound, with the implicit +Inf bucket always counting it.
func FuzzHistogramObserve(f *testing.F) {
	f.Add(0.001)
	f.Add(0.5)
	f.Add(1.0)
	f.Add(10.0)
	f.Add(math.MaxFloat64)
	f.Add(math.Inf(1))
	f.Add(math.Inf(-1))
	f.Add(math.NaN())
	f.Add(0.0)
	f.Add(-1.0)

	f.Fuzz(func(t *testing.T, val float64) {
		h := NewHistogram("fuzz_test", "fuzz")
		h.Observe(val)

		sum, count, bucketVals := h.snapshot()
		if count != 1 {
			t.Fatalf("count = %d, want 1", count)
		}

		switch {
		case math.IsNaN(val):
			if !math.IsNaN(sum) {
				t.Fatalf("sum after NaN observe = %v, want NaN", sum)
			}
		case math.IsInf(val, 1):
			if !math.IsInf(sum, 1) {
				t.Fatalf("sum after +Inf observe = %v, want +Inf", sum)
			}
		case math.IsInf(val, -1):
			if !math.IsInf(sum, -1) {
				t.Fatalf("sum after -Inf observe = %v, want -Inf", sum)
			}
		default:
			if sum != val {
				t.Fatalf("sum after finite observe = %v, want %v", sum, val)
			}
		}

		for i, bound := range h.bounds {
			want := int64(0)
			if val <= bound {
				want = 1
			}
			if got := bucketVals[i]; got != want {
				t.Errorf("val=%v bound[%d]=%v: bucket=%d, want %d", val, i, bound, got, want)
			}
		}
		if got := bucketVals[len(h.bounds)]; got != 1 {
			t.Errorf("+Inf bucket = %d, want 1", got)
		}
	})
}
