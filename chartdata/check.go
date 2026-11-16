package chartdata

import (
	"math"
	"strconv"
)

// count tallies a frame's values against MaxValues, as the chart counts
// them, before any of them is copied.
type count struct{ n int }

func (c *count) add(n int, field string) error {
	c.n += n
	if c.n > MaxValues {
		return refusef(RuleLimit, field, "takes the frame past %d values", MaxValues)
	}
	return nil
}

// seriesCharge is what n series of length values each count: max(1, length)
// each, or length when there is no series, as the chart charges a mark.
func seriesCharge(n, length int) int {
	if n == 0 {
		return length
	}
	return n * max(1, length)
}

func indexed(field string, i int) string {
	return field + "[" + strconv.Itoa(i) + "]"
}

// checkFinite refuses NaN, ±Inf and a value beyond ±MaxMagnitude.
func checkFinite(field string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return refuse(RuleFinite, field, "must be a finite number")
	}
	if math.Abs(v) > MaxMagnitude {
		return refusef(RuleMagnitude, field, "must lie within ±%g", MaxMagnitude)
	}
	return nil
}

// checkFiniteAll is checkFinite over vs.
func checkFiniteAll(field string, vs []float64) error {
	for i, v := range vs {
		if !isWithin(v) {
			return checkFinite(indexed(field, i), v)
		}
	}
	return nil
}

// isWithin reports whether checkFinite takes v, without building the field
// path a refusal names, which a check of every value cannot afford.
func isWithin(v float64) bool {
	return math.Abs(v) <= MaxMagnitude
}

// gapped copies vs as the wire's values: NaN becomes a null gap, and ±Inf
// and a value beyond ±MaxMagnitude are refused. The copies share one
// backing array.
func gapped(field string, vs []float64) ([]*float64, error) {
	buf := make([]float64, len(vs))
	out := make([]*float64, len(vs))
	for i, v := range vs {
		if math.IsNaN(v) {
			continue
		}
		if !isWithin(v) {
			return nil, checkFinite(indexed(field, i), v)
		}
		buf[i] = v
		out[i] = &buf[i]
	}
	return out, nil
}

// gappedLen is gapped, refusing vs unless it is n long.
func gappedLen(field string, vs []float64, n int, against string) ([]*float64, error) {
	if len(vs) != n {
		return nil, refusef(RuleLength, field, "holds %d values, not one per %s (%d)", len(vs), against, n)
	}
	return gapped(field, vs)
}

// checkOrder refuses a non-finite xs and one that does not strictly increase,
// or with ties, that decreases.
func checkOrder(field string, xs []float64, ties bool) error {
	if err := checkFiniteAll(field, xs); err != nil {
		return err
	}
	for i := 1; i < len(xs); i++ {
		if xs[i] < xs[i-1] || (!ties && xs[i] == xs[i-1]) {
			if ties {
				return refusef(RuleOrder, indexed(field, i), "must not decrease")
			}
			return refusef(RuleOrder, indexed(field, i), "must strictly increase")
		}
	}
	return nil
}

// checkNames refuses an empty name and a name given twice.
func checkNames(field string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		if name == "" {
			return refuse(RuleEmpty, indexed(field, i), "must not be empty")
		}
		if _, dup := seen[name]; dup {
			return refusef(RuleDuplicate, indexed(field, i), "repeats %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// checkKeys refuses a key given twice; a key may be empty, as a label value
// may be.
func checkKeys(field string, keys func(int) string, n int) error {
	seen := make(map[string]struct{}, n)
	for i := range n {
		key := keys(i)
		if _, dup := seen[key]; dup {
			return refusef(RuleDuplicate, indexed(field, i)+".key", "repeats %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func checkMost(field string, n, most int, what string) error {
	if n > most {
		return refusef(RuleLimit, field, "holds %d %s, past the most, %d", n, what, most)
	}
	return nil
}
