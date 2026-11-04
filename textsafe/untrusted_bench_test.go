package textsafe_test

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/cplieger/go/textsafe"
)

// The tag's cost claim: NewUntrusted sanitizes once, so a tagged field costs one
// Sanitize scan per decode and nothing per log line. The sinks are typed, because
// consuming LogValue's result through an `any` measures the interface boxing of the
// returned Value, which no real caller pays.
var (
	sinkValue     slog.Value
	sinkUntrusted textsafe.Untrusted
)

// discardState is a fmt.State that, like fmt's own printer, implements
// io.StringWriter. The fmt cases call Format on it directly, because a real
// fmt call's allocations depend on fmt's printer pool, which the race
// detector drains at random.
type discardState struct{}

func (discardState) Write(p []byte) (int, error)       { return len(p), nil }
func (discardState) WriteString(s string) (int, error) { return len(s), nil }
func (discardState) Width() (int, bool)                { return 0, false }
func (discardState) Precision() (int, bool)            { return 0, false }
func (discardState) Flag(int) bool                     { return false }

var (
	formatState fmt.State       = discardState{}
	_           io.StringWriter = discardState{}
)

// TestUntrustedSinksAreAllocationFreeForCleanText gates the cost claim under the type's
// adoption advice: tagging a field is free for the values an honest upstream sends, at
// construction and at every sink the tag fires through.
func TestUntrustedSinksAreAllocationFreeForCleanText(t *testing.T) {
	const runs = 50
	clean := cleanASCII(typicalBytes)
	u := textsafe.NewUntrusted(clean)
	for _, tc := range []struct {
		name string
		desc string
		call func()
	}{
		{"new", "NewUntrusted, the construction path", func() { sinkUntrusted = textsafe.NewUntrusted(clean) }},
		{"log_value", "LogValue, the slog.LogValuer path", func() { sinkValue = u.LogValue() }},
		{"string", "String, the fmt.Stringer and fmt.Errorf path", func() { sinkString = u.String() }},
		{"fmt", "Format under %v and %s", func() { u.Format(formatState, 'v'); u.Format(formatState, 's') }},
		{"single_line", "SingleLine, the strict hand-built-sink path", func() { sinkString = u.SingleLine() }},
		{"raw", "Raw, the compute path that must not transform", func() { sinkString = u.Raw() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, tc.call); got != 0 {
				t.Errorf("Untrusted(clean ASCII, %d bytes).%s allocated %v times per run, "+
					"want 0: tagging an honest value must cost nothing", typicalBytes, tc.desc, got)
			}
		})
	}
}

// TestUntrustedLogLinesAreAllocationFree pins that the per-log-line paths cost nothing
// even for text with unsafe runes, because the Sanitize form is computed once, at
// construction, and a tagged field resolves on every log line that carries it.
func TestUntrustedLogLinesAreAllocationFree(t *testing.T) {
	const runs = 50
	u := textsafe.NewUntrusted(unsafeDense(typicalBytes))
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"log_value", func() { sinkValue = u.LogValue() }},
		{"string", func() { sinkString = u.String() }},
		{"fmt", func() { u.Format(formatState, 'v'); u.Format(formatState, 's') }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(runs, tc.call); got != 0 {
				t.Errorf("Untrusted(unsafe dense, %d bytes) %s allocated %v times per run, want 0",
					typicalBytes, tc.name, got)
			}
		})
	}
}

// TestUntrustedMarshalTextCopiesOnce pins the one sink the allocation-free property does
// not reach. encoding.TextMarshaler returns []byte, and converting the sanitized string
// to one is a copy no fast path can remove. Asserting the number keeps the copy single.
func TestUntrustedMarshalTextCopiesOnce(t *testing.T) {
	const runs = 50
	u := textsafe.NewUntrusted(cleanASCII(typicalBytes))
	var buf []byte
	if got := testing.AllocsPerRun(runs, func() {
		buf, _ = u.MarshalText()
	}); got != 1 {
		t.Errorf("Untrusted(clean ASCII, %d bytes).MarshalText allocated %v times per run, "+
			"want 1: the []byte the TextMarshaler contract requires is one unavoidable "+
			"copy of the sanitized string, and only one", typicalBytes, got)
	}
	sinkString = string(buf)
}

// BenchmarkNewUntrusted is the per-decode series for the tag, the only place it does
// work. On the clean fixture it should sit on BenchmarkSanitize/clean_ascii at the same
// size, at zero allocations: any daylight between them is wrapper overhead.
func BenchmarkNewUntrusted(b *testing.B) {
	clean := cleanASCII(typicalBytes)
	b.ReportAllocs()
	for b.Loop() {
		sinkUntrusted = textsafe.NewUntrusted(clean)
	}
}
