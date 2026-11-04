package measure

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// FuzzFormatValueRoundTrip pins formatValue's core contract: the single
// canonical float formatter shared by both exposition encoders emits a token
// that parses back to the exact same float64 for every finite input, plus the
// documented spec tokens for the non-finite cases. Both branches must
// round-trip: the bare-integer branch (integral values in the int64-exact
// [-1e15, 1e15] range) and the shortest-'g' fallback. Uses only the standard
// library (testing.F), honoring the zero-dependency contract.
func FuzzFormatValueRoundTrip(f *testing.F) {
	f.Add(0.0)
	f.Add(1.0)
	f.Add(-1.0)
	f.Add(1e15)
	f.Add(-1e15)
	f.Add(1e16)
	f.Add(0.005)
	f.Add(3.14)
	f.Add(1e-7)
	f.Add(math.MaxFloat64)
	f.Add(math.SmallestNonzeroFloat64)
	f.Fuzz(func(t *testing.T, v float64) {
		got := formatValue(v)
		if got == "" {
			t.Fatalf("formatValue(%v) returned empty string", v)
		}
		if strings.ContainsAny(got, " \t\n") {
			t.Fatalf("formatValue(%v) = %q contains whitespace, not a valid value token", v, got)
		}
		switch {
		case math.IsInf(v, 1):
			if got != "+Inf" {
				t.Fatalf("formatValue(+Inf) = %q, want \"+Inf\"", got)
			}
			return
		case math.IsInf(v, -1):
			if got != "-Inf" {
				t.Fatalf("formatValue(-Inf) = %q, want \"-Inf\"", got)
			}
			return
		case math.IsNaN(v):
			if got != "NaN" {
				t.Fatalf("formatValue(NaN) = %q, want \"NaN\"", got)
			}
			return
		}
		back, err := strconv.ParseFloat(got, 64)
		if err != nil {
			t.Fatalf("formatValue(%v) = %q, not parseable as float64: %v", v, got, err)
		}
		if back != v {
			t.Errorf("round-trip: formatValue(%v) = %q parsed back to %v, want %v", v, got, back, v)
		}
	})
}
