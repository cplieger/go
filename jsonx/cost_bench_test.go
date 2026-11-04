package jsonx

import (
	"fmt"
	"strings"
	"testing"
)

// jsonx sells a bound on what untrusted input can cost: Classify is a total
// inspection over raw bytes and ParseInt64 must not be made expensive by a
// hostile number. These tests pin the cost half of that contract.
//
// Allocation counts are not uniformly zero (a float form costs 1, a
// rejected oversized value costs a few more for the *ParseError), and
// that is fine: the property that matters for a defensive decoder is
// SIZE INDEPENDENCE — growing the payload must not grow the cost. So each
// test grows the input 128x and asserts the allocation count does not
// move, logging the measured constant rather than hardcoding it.
//
// testing.AllocsPerRun returns a mean, and on Go 1.27 that mean is not
// stable for any path through encoding/json: v1 is backed by
// encoding/json/v2, whose pooled coder state varies its hit/miss pattern
// between processes under -race. A pool miss can only add allocations,
// never remove them, so the minimum over a few trials is the steady-state
// cost these tests assert on (exactly, not with a tolerance).

// minAllocsPerRun is testing.AllocsPerRun's steady-state cost: the minimum
// over several trials, discarding pooled-state misses (see the file comment).
func minAllocsPerRun(runs, trials int, f func()) float64 {
	best := testing.AllocsPerRun(runs, f)
	for range trials - 1 {
		if got := testing.AllocsPerRun(runs, f); got < best {
			best = got
		}
	}
	return best
}

// TestClassifyCostIsSizeIndependent pins the classifier as O(1) in the payload.
func TestClassifyCostIsSizeIndependent(t *testing.T) {
	shapes := []struct {
		name  string
		build func(n int) []byte
	}{
		{"digit run", func(n int) []byte { return []byte(strings.Repeat("9", n)) }},
		{"quoted digit run", func(n int) []byte { return []byte(`"` + strings.Repeat("9", n) + `"`) }},
		{"exponent", func(n int) []byte { return []byte("1e" + strings.Repeat("9", n)) }},
		{"leading zeros", func(n int) []byte { return append([]byte(strings.Repeat("0", n)), '1') }},
		{"float", func(n int) []byte { return []byte(strings.Repeat("9", n) + ".5") }},
		{"garbage", func(n int) []byte { return []byte("{[" + strings.Repeat("x", n)) }},
	}
	// Both sizes sit well past any short-input fast path, so a difference here
	// is growth with input rather than a change of code path.
	//
	// The fixture is built OUTSIDE the measured closure: building it inside
	// would measure the test's own string construction alongside the
	// subject, which under -race costs one allocation more for the larger
	// fixture for a reason unrelated to Classify.
	const small, large = 512, 65536
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			smallInput, largeInput := s.build(small), s.build(large)
			lo := minAllocsPerRun(20, 3, func() { _ = Classify(smallInput) })
			hi := minAllocsPerRun(20, 3, func() { _ = Classify(largeInput) })
			if lo != hi {
				t.Errorf("Classify allocated %v at %d bytes but %v at %d bytes: a "+
					"128x larger payload must not cost more", lo, small, hi, large)
			}
			t.Logf("constant %v allocations from %d to %d bytes", lo, small, large)
		})
	}
}

// TestParseCostIsSizeIndependent is the amplification guard on the decode path.
func TestParseCostIsSizeIndependent(t *testing.T) {
	policies := map[string]Policy{
		"tolerant_zero":     TolerantZero(),
		"strict":            Strict(),
		"strict_absentzero": StrictAbsentZero(),
	}
	shapes := []struct {
		name  string
		build func(n int) []byte
	}{
		{"digit run", func(n int) []byte { return []byte(strings.Repeat("9", n)) }},
		{"exponent", func(n int) []byte { return []byte("1e" + strings.Repeat("9", n)) }},
		{"leading zeros", func(n int) []byte { return append([]byte(strings.Repeat("0", n)), '1') }},
	}
	const small, large = 512, 65536
	for pName, p := range policies {
		for _, s := range shapes {
			t.Run(fmt.Sprintf("%s/%s", pName, s.name), func(t *testing.T) {
				// Hoisted for the same reason as in the Classify test above.
				smallInput, largeInput := s.build(small), s.build(large)
				lo := minAllocsPerRun(20, 3, func() { _, _ = ParseInt64(smallInput, p) })
				hi := minAllocsPerRun(20, 3, func() { _, _ = ParseInt64(largeInput, p) })
				if lo != hi {
					t.Errorf("ParseInt64 allocated %v at %d bytes but %v at %d bytes: "+
						"parse cost must not grow with the attacker's payload",
						lo, small, hi, large)
				}
				t.Logf("constant %v allocations from %d to %d bytes", lo, small, large)
			})
		}
	}
}

// TestErrorSnippetStaysBounded pins the documented 40-byte snippet cap. An error
// string that embedded the whole rejected token would reintroduce, through the
// library's own diagnostics, the amplification the library exists to stop.
func TestErrorSnippetStaysBounded(t *testing.T) {
	p := Strict()
	for _, n := range []int{64, 4096, 65536} {
		_, err := ParseInt64([]byte(strings.Repeat("9", n)), p)
		if err == nil {
			t.Fatalf("n=%d: an overflowing digit run should be rejected", n)
		}
		if got := len(err.Error()); got > 256 {
			t.Errorf("n=%d: error string is %d bytes; a rejection must not carry the "+
				"payload into logs", n, got)
		}
	}
}

// BenchmarkClassify covers the inspection every decode runs first.
func BenchmarkClassify(b *testing.B) {
	cases := map[string][]byte{
		"plain":       []byte(`12345`),
		"quoted":      []byte(`"12345"`),
		"null":        []byte(`null`),
		"float":       []byte(`1.5`),
		"adversarial": []byte(`1e999999999`),
		"long_digits": []byte(strings.Repeat("9", 4096)),
	}
	for name, data := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				_ = Classify(data)
			}
		})
	}
}

// BenchmarkParseInt64 measures the three shipped policies against the same
// inputs, so a policy that quietly became the expensive one is visible.
func BenchmarkParseInt64(b *testing.B) {
	policies := map[string]Policy{
		"tolerant_zero":     TolerantZero(),
		"strict":            Strict(),
		"strict_absentzero": StrictAbsentZero(),
	}
	inputs := map[string][]byte{
		"plain":  []byte(`12345`),
		"quoted": []byte(`"12345"`),
		"null":   []byte(`null`),
	}
	for pName, p := range policies {
		for iName, data := range inputs {
			b.Run(fmt.Sprintf("%s/%s", pName, iName), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_, _ = ParseInt64(data, p)
				}
			})
		}
	}
}

// BenchmarkParseInt64Adversarial is the number to watch. These inputs are cheap
// to send and are the ones a naive implementation makes expensive to reject.
func BenchmarkParseInt64Adversarial(b *testing.B) {
	p := Strict()
	cases := map[string][]byte{
		"huge_exponent":     []byte(`1e999999999`),
		"long_digit_run":    []byte(strings.Repeat("9", 65536)),
		"long_leading_zero": append([]byte(strings.Repeat("0", 65536)), '1'),
	}
	for name, data := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				_, _ = ParseInt64(data, p)
			}
		})
	}
}
