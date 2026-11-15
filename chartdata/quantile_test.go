package chartdata_test

import (
	"math"
	"strings"
	"testing"

	"github.com/cplieger/go/chartdata"
)

// The expected values follow Prometheus's histogram_quantile
// (promql/quantile.go): linear inside the bucket the rank falls in, the
// first bucket whose cumulative count reaches the rank; the lowest bucket
// from 0 when its edge is above 0 and its edge otherwise; the highest edge
// for a rank in the +Inf bucket; no answer for no observations or fewer than
// two buckets.
func TestQuantileIsHistogramQuantile(t *testing.T) {
	t.Parallel()
	edges := []float64{1, 2, 4}
	for _, tc := range []struct {
		name   string
		edges  []float64
		counts []float64
		q      float64
		want   float64
		ok     bool
	}{
		{"median", edges, []float64{1, 2, 1, 0}, 0.5, 1.5, true},
		{"top", edges, []float64{1, 2, 1, 0}, 1, 4, true},
		{"p99", edges, []float64{1, 2, 1, 0}, 0.99, 3.92, true},
		{"lowestFromZero", edges, []float64{4, 0, 0, 0}, 0.25, 0.25, true},
		{"zeroQuantile", edges, []float64{1, 1, 0, 0}, 0, 0, true},
		{"rankOnAnEdgeTakesTheLowerBucket", edges, []float64{2, 0, 2, 0}, 0.5, 1, true},
		{"emptyBucketsBelowTheRank", edges, []float64{0, 0, 2, 0}, 0.5, 3, true},
		{"inTheInfBucket", edges, []float64{1, 0, 0, 3}, 0.9, 4, true},
		{"oneEdge", []float64{5}, []float64{2, 2}, 0.25, 2.5, true},
		{"oneEdgeInTheInfBucket", []float64{5}, []float64{2, 2}, 0.75, 5, true},
		{"lowestEdgeAtZero", []float64{0, 1}, []float64{2, 2, 0}, 0.25, 0, true},
		{"lowestEdgeBelowZero", []float64{-1, 1}, []float64{2, 2, 0}, 0.25, -1, true},
		{"belowZero", []float64{-4, -2, 0}, []float64{0, 2, 0, 0}, 0.5, -3, true},
		{"zeroQuantileOfAnEmptyLowestBucket", edges, []float64{0, 1, 0, 0}, 0, 0, false},
		{"zeroQuantileOfAnEmptyLowestBucketAtZero", []float64{0, 1}, []float64{0, 2, 0}, 0, 0, true},
		{"noObservations", edges, []float64{0, 0, 0, 0}, 0.5, 0, false},
		{"onlyTheInfBucket", nil, []float64{5}, 0.5, 0, false},
		{"negativeCount", edges, []float64{2, -1, 2, 0}, 0.5, 0, false},
		{"nanCount", edges, []float64{2, math.NaN(), 2, 0}, 0.5, 0, false},
		{"infiniteCount", edges, []float64{2, math.Inf(1), 2, 0}, 0.5, 0, false},
		{"totalPastFloat64", edges, []float64{math.MaxFloat64, math.MaxFloat64, 0, 0}, 0.5, 0, false},
		{"estimatePastFloat64", []float64{-math.MaxFloat64, math.MaxFloat64}, []float64{0, 2, 0}, 0.5, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := chartdata.Quantile(tc.edges, tc.counts, tc.q)
			if ok != tc.ok || (ok && math.Abs(got-tc.want) > 1e-12) {
				t.Errorf("Quantile(%v, %v, %v) = %v, %v, want %v, %v", tc.edges, tc.counts, tc.q, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestQuantilePanicsOnACallerError(t *testing.T) {
	t.Parallel()
	edges, counts := []float64{1, 2, 4}, []float64{1, 2, 1, 0}
	for _, tc := range []struct {
		name   string
		edges  []float64
		counts []float64
		q      float64
		want   string
	}{
		{"qBelowZero", edges, counts, -0.1, "is not from 0 to 1"},
		{"qAboveOne", edges, counts, 1.1, "is not from 0 to 1"},
		{"qNaN", edges, counts, math.NaN(), "is not from 0 to 1"},
		{"countsShort", edges, counts[:3], 0.5, "3 counts for 3 edges"},
		{"countsLong", edges, append(counts, 0), 0.5, "5 counts for 3 edges"},
		{"edgeNaN", []float64{1, math.NaN(), 4}, counts, 0.5, "edges[1] NaN is not finite"},
		{"edgeInfinite", []float64{1, 2, math.Inf(1)}, counts, 0.5, "edges[2] +Inf is not finite"},
		{"edgeNegativeInfinite", []float64{math.Inf(-1), 2, 4}, counts, 0.5, "edges[0] -Inf is not finite"},
		{"edgeRepeated", []float64{1, 2, 2}, counts, 0.5, "edges[2] 2 does not strictly increase"},
		{"edgeDecreasing", []float64{1, 0, 4}, counts, 0.5, "edges[1] 0 does not strictly increase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "chartdata: Quantile ") || !strings.Contains(msg, tc.want) {
					t.Errorf("Quantile(%v, %v, %v) panicked with %q, want a chartdata panic naming %q", tc.edges, tc.counts, tc.q, msg, tc.want)
				}
			}()
			chartdata.Quantile(tc.edges, tc.counts, tc.q)
		})
	}
}
