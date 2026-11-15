package chartdata

import (
	"math"
	"testing"
	"time"
)

// The cases follow Prometheus's histogram_quantile (promql/quantile.go):
// linear interpolation inside the bucket the rank falls in, the lowest
// bucket from 0 when its bound is above 0, the highest finite bound for a
// rank in the +Inf bucket, NaN for no observations or fewer than two buckets.
func TestQuantileIsHistogramQuantile(t *testing.T) {
	t.Parallel()
	bounds := []float64{1, 2, 4}
	for _, tc := range []struct {
		name   string
		bounds []float64
		cum    []float64
		q      float64
		want   float64
	}{
		{"median", bounds, []float64{1, 3, 4, 4}, 0.5, 1.5},
		{"top", bounds, []float64{1, 3, 4, 4}, 1, 4},
		{"p99", bounds, []float64{1, 3, 4, 4}, 0.99, 3.92},
		{"lowestFromZero", bounds, []float64{4, 4, 4, 4}, 0.25, 0.25},
		{"inTheInfBucket", bounds, []float64{1, 1, 1, 4}, 0.9, 4},
		{"lowestBoundAtZero", []float64{0, 1}, []float64{2, 4, 4}, 0.25, 0},
		{"lowestBoundBelowZero", []float64{-1, 1}, []float64{2, 4, 4}, 0.25, -1},
		{"unsortedCountsMadeMonotonic", bounds, []float64{2, 1, 4, 4}, 0.5, 1},
		{"noObservations", bounds, []float64{0, 0, 0, 0}, 0.5, math.NaN()},
		{"onlyTheInfBucket", nil, []float64{5}, 0.5, math.NaN()},
		{"wrongLength", bounds, []float64{1, 2}, 0.5, math.NaN()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quantile(tc.q, tc.bounds, tc.cum)
			if !(got == tc.want || math.IsNaN(got) && math.IsNaN(tc.want)) && math.Abs(got-tc.want) > 1e-12 {
				t.Errorf("quantile(%v, %v, %v) = %v, want %v", tc.q, tc.bounds, tc.cum, got, tc.want)
			}
		})
	}
}

func TestNextEdgeIsTheNextMultipleSinceTheEpoch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		now   time.Time
		every time.Duration
		want  time.Time
	}{
		{time.Unix(90, 0), time.Minute, time.Unix(120, 0)},
		{time.Unix(120, 0), time.Minute, time.Unix(180, 0)},
		{time.Unix(7, 1), 7 * time.Second, time.Unix(14, 0)},
	} {
		if got := nextEdge(tc.now, tc.every); !got.Equal(tc.want) {
			t.Errorf("nextEdge(%v, %v) = %v, want %v", tc.now.Unix(), tc.every, got.Unix(), tc.want.Unix())
		}
	}
}
