package jsonx_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/cplieger/go/jsonx"
	"pgregory.net/rapid"
)

// TestPropCrossPolicyInvariants runs the fuzz targets' cross-policy
// invariants as an every-PR property: inputs mix the curated wire-shape
// corpus with raw byte noise. (The weekly fuzz corpus does not persist;
// rapid is the durable invariant net.)
func TestPropCrossPolicyInvariants(t *testing.T) {
	t.Parallel()
	corpus := make([][]byte, len(seedCorpus))
	for i, s := range seedCorpus {
		corpus[i] = []byte(s)
	}
	gen := rapid.OneOf(
		rapid.SampledFrom(corpus),
		rapid.SliceOfN(rapid.Byte(), 0, 24),
	)
	rapid.Check(t, func(rt *rapid.T) {
		checkCrossPolicyInvariants(rt, gen.Draw(rt, "data"))
	})
}

// TestPropDualFormEquivalence runs the number-vs-string metamorphic
// property over generated int64s under every shipped policy.
func TestPropDualFormEquivalence(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		checkFormsEquivalence(rt, rapid.Int64().Draw(rt, "n"))
	})
}

// TestPropTolerantWideBound pins the configurable-bound contract: with the
// bound widened to MaxInt64, TolerantZero accepts every non-negative
// integer exactly and still zeroes every negative one.
func TestPropTolerantWideBound(t *testing.T) {
	t.Parallel()
	wide := jsonx.TolerantZero()
	wide.MaxValue = math.MaxInt64
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.Int64().Draw(rt, "n")
		v, err := jsonx.ParseInt64([]byte(strconv.FormatInt(n, 10)), wide)
		if err != nil {
			rt.Fatalf("wide TolerantZero(%d) error: %v", n, err)
		}
		switch {
		case n >= 0 && v != n:
			rt.Errorf("wide TolerantZero(%d) = %d, want %d", n, v, n)
		case n < 0 && v != 0:
			rt.Errorf("wide TolerantZero(%d) = %d, want 0 (negatives zeroed)", n, v)
		}
	})
}
