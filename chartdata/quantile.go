package chartdata

import (
	"fmt"
	"math"
)

// Quantile estimates the q-quantile of a histogram as PromQL's
// histogram_quantile does. edges are the finite bucket bounds, strictly
// increasing, and counts each bucket's observations, the last above every
// edge. It answers 0, false for no observations, fewer than two buckets, a
// negative or non-finite count, a total past float64, a rank inside a bucket
// whose edges are further apart than float64 reaches, or q of 0 over an
// empty lowest bucket above 0, and panics for q outside [0, 1], a non-finite
// or out-of-order edge, or a counts length other than len(edges)+1.
func Quantile(edges, counts []float64, q float64) (float64, bool) {
	checkQuantile(edges, counts, q)
	total := 0.0
	for _, c := range counts {
		if !(c >= 0) {
			return 0, false
		}
		total += c
	}
	if len(edges) == 0 || !(total > 0) || math.IsInf(total, 1) {
		return 0, false
	}
	rank := q * total
	b, before := 0, 0.0
	for b < len(edges) && before+counts[b] < rank {
		before += counts[b]
		b++
	}
	switch {
	case b == len(edges):
		return edges[b-1], true
	case b == 0 && edges[0] <= 0:
		return edges[0], true
	}
	lo := 0.0
	if b > 0 {
		lo = edges[b-1]
	}
	// 0/0 when q is 0 and the lowest bucket is empty, a NaN in PromQL too.
	v := lo + (edges[b]-lo)*((rank-before)/counts[b])
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func checkQuantile(edges, counts []float64, q float64) {
	if !(q >= 0 && q <= 1) {
		panic(fmt.Sprintf("chartdata: Quantile q %v is not from 0 to 1", q))
	}
	if len(counts) != len(edges)+1 {
		panic(fmt.Sprintf("chartdata: Quantile takes one more count than edges, not %d counts for %d edges", len(counts), len(edges)))
	}
	for i, e := range edges {
		if math.IsNaN(e) || math.IsInf(e, 0) {
			panic(fmt.Sprintf("chartdata: Quantile edges[%d] %v is not finite", i, e))
		}
		if i > 0 && e <= edges[i-1] {
			panic(fmt.Sprintf("chartdata: Quantile edges[%d] %v does not strictly increase", i, e))
		}
	}
}
