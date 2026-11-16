package chartdata_test

import (
	"math/rand/v2"
	"testing"

	"github.com/cplieger/go/chartdata"
)

// cdf is histogram_quantile's model of where observations lie, the reference
// Quantile inverts: the count at or below x, spread evenly through each
// finite bucket, the lowest from 0, or all at its edge when that edge is 0
// or below.
func cdf(edges, counts []float64, x float64) float64 {
	sum := 0.0
	for i, hi := range edges {
		lo := 0.0
		if i > 0 {
			lo = edges[i-1]
		}
		if i == 0 && hi <= 0 {
			if x >= hi {
				sum += counts[0]
			}
			continue
		}
		sum += counts[i] * min(1, max(0, (x-lo)/(hi-lo)))
	}
	return sum
}

// Over random integer edges and counts and a dyadic q, so the rank is exact,
// Quantile returns the least x whose cdf reaches the rank, or the highest
// edge when the finite buckets hold fewer.
func FuzzQuantile_invertsTheCDF(f *testing.F) {
	for seed := range uint64(200) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		rng := rand.New(rand.NewPCG(seed, seed>>32|1))
		edges := make([]float64, 1+rng.IntN(8))
		counts := make([]float64, len(edges)+1)
		next := float64(rng.IntN(21) - 10)
		for i := range edges {
			edges[i] = next
			next += float64(1 + rng.IntN(10))
		}
		for i := range counts {
			if rng.IntN(3) > 0 {
				counts[i] = float64(rng.IntN(21))
			}
		}
		scale := 1 << rng.IntN(7)
		q := float64(1+rng.IntN(scale)) / float64(scale)

		got, ok := chartdata.Quantile(edges, counts, q)
		total, finite := 0.0, cdf(edges, counts, edges[len(edges)-1])
		for _, c := range counts {
			total += c
		}
		rank := q * total
		switch {
		case total == 0:
			if ok {
				t.Fatalf("Quantile(%v, %v, %v) = %v, want no answer for no observations", edges, counts, q, got)
			}
		case !ok:
			t.Fatalf("Quantile(%v, %v, %v) gave no answer", edges, counts, q)
		case rank > finite:
			if got != edges[len(edges)-1] {
				t.Fatalf("Quantile(%v, %v, %v) = %v, want the highest edge %v", edges, counts, q, got, edges[len(edges)-1])
			}
		default:
			// The lowest bucket's count all lies at an edge of 0 or below, so the
			// cdf there may pass the rank.
			mass := got == edges[0] && edges[0] <= 0
			if at := cdf(edges, counts, got); at < rank-1e-9*total || (!mass && at > rank+1e-9*total) {
				t.Fatalf("Quantile(%v, %v, %v) = %v, whose cdf %v is not the rank %v", edges, counts, q, got, at, rank)
			}
			if below := cdf(edges, counts, got-1e-3); below > rank-1e-6 {
				t.Fatalf("Quantile(%v, %v, %v) = %v, but %v below it the cdf %v already reaches the rank %v", edges, counts, q, got, 1e-3, below, rank)
			}
		}
	})
}
