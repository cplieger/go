package measure

import (
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// mustPanicContaining runs fn and fails unless it panics with a string or
// error value containing want. Used to assert the package's fail-fast guards
// (invalid registry prefix, label arity, negative Counter.Add) and the
// MustRegister door, which panics with the registration error.
func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		var msg string
		switch v := r.(type) {
		case string:
			msg = v
		case error:
			msg = v.Error()
		default:
			t.Fatalf("panic = %v, want a string or error containing %q", r, want)
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want it to contain %q", r, want)
		}
	}()
	fn()
}

// mustRegisterError registers m and fails unless Register returns an error
// containing want — the v4 shape of the old panic-at-construction and
// panic-at-registration assertions.
func mustRegisterError(t *testing.T, r *Registry, m Metric, want string) {
	t.Helper()
	err := r.Register(m)
	if err == nil {
		t.Fatalf("Register = nil, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Register error = %q, want it to contain %q", err, want)
	}
}

// body serves r's Prometheus handler against a throwaway request and returns
// the response body, the common shape for asserting on a registry's exposition.
func body(t *testing.T, r *Registry) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w.Body.String()
}

// failWriter is an http.ResponseWriter whose Write always fails, used to drive
// the write-error logging branch of the Prometheus handler.
type failWriter struct {
	hdr http.Header
}

func (w *failWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *failWriter) Write([]byte) (int, error) {
	return 0, errors.New("forced write failure")
}

func (w *failWriter) WriteHeader(int) {}

// captureDebugLogs redirects the default slog logger to an in-memory buffer at
// Debug level for the duration of the test and returns that buffer. Callers
// must be serial (no t.Parallel): the default logger is a process global.
//
// slog.SetDefault also points the standard log package at the installed
// handler, and it skips that redirect when the logger being installed carries
// slog's own default handler. Reinstalling the previous logger therefore does
// not undo the redirect, so the writer and flags are saved and restored
// explicitly. slog goes back first: reinstalling a previous handler that is not
// slog's default re-runs the redirect and would overwrite a log restore done
// before it.
func captureDebugLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// TestCaptureDebugLogsRestoresLogGlobals pins the restore in captureDebugLogs:
// the swap redirects the standard log package's writer and zeroes its flags, and
// the cleanup must put both back. Without it, one test silences slog for the
// rest of the package, because slog's own default handler writes through
// log.Output.
func TestCaptureDebugLogsRestoresLogGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("swap", func(t *testing.T) {
		captureDebugLogs(t)
		if log.Writer() == wantWriter {
			t.Fatal("captureDebugLogs did not redirect log.Writer(); the restore under test would guard nothing")
		}
	})

	if got := log.Writer(); got != wantWriter {
		t.Errorf("log.Writer() after captureDebugLogs cleanup = %#v, want the original %#v", got, wantWriter)
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() after captureDebugLogs cleanup = %d, want %d", got, wantFlags)
	}
}

// assertExpositionLabelsBalanced verifies every labeled exposition line in out
// has balanced braces and a quote-balanced (properly escaped) label section. It
// is shared by the label-value fuzz targets so they assert a structural
// invariant rather than merely catching panics.
func assertExpositionLabelsBalanced(t *testing.T, out string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		// The label section runs from the FIRST '{' to the LAST '}': a label
		// value may legitimately contain either brace. Cutting around both
		// separators carries the "found" answer with the slice, so no index
		// arithmetic can go one out.
		_, afterOpen, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		inner, _, ok := strings.CutLast(afterOpen, "}")
		if !ok {
			t.Fatalf("unbalanced braces: %q", line)
		}
		checkLabelQuoting(t, inner)
	}
}

func checkLabelQuoting(t *testing.T, inner string) {
	t.Helper()
	inQuote := false
	for i := 0; i < len(inner); i++ {
		switch {
		case inner[i] == '\\' && inQuote:
			i++ // skip escaped char
		case inner[i] == '"':
			inQuote = !inQuote
		}
	}
	if inQuote {
		t.Fatalf("unbalanced quotes in label section: %q", inner)
	}
}

// The shared machinery for this package's allocation contracts, which live in
// counter_test.go, gauge_test.go, histogram_test.go, measure_test.go and
// exposition_test.go — each beside the source file whose cost it pins.
//
// The library's cost model has two halves and both are now measured: recording
// into an EXISTING series allocates nothing, and creating a new series costs a
// bounded constant. The weekly benchmark tracker cannot stand in for either.
// It alerts on the ratio between consecutive runs, so it catches a 0 -> n
// regression (an infinite ratio, at any threshold) but is structurally blind
// to a bounded count drifting: 260 -> 380 allocations per scrape is a ratio of
// 1.46, and 2 -> 3 is exactly 1.5. The bounded counts are therefore where
// these contracts earn their keep, and the exposition ones bound a per-metric
// RATE rather than a total, because a scrape's total cost is legitimately
// proportional to how many metrics the registry holds.
//
// Three rules every contract here follows, each of which is a way to measure
// the wrong thing:
//
//   - Fixtures are built OUTSIDE the measured closure. Registering a metric,
//     constructing a registry and building a label-value slice all allocate,
//     and inside the closure that cost is reported as the library's.
//   - The closure is deliberate about which path it takes. A registry is
//     stateful and [testing.AllocsPerRun] runs the closure runs+1 times, so a
//     closure touching ONE label set measures the loaded fast path on every
//     run after the first, while one touching a FRESH label set per run
//     measures series creation. Each contract's comment says which it is.
//   - No t.Parallel. AllocsPerRun reads process-wide malloc counters and
//     panics outright when called from a parallel test.

// allocRuns is the AllocsPerRun run count for the record-path contracts.
// AllocsPerRun divides as integers, so the average of a path that allocates
// nothing is exactly 0 and one stray allocation per hundred runs still floors
// to 0; the count is high enough that a per-call allocation cannot hide and
// low enough that the contracts stay instant under -race.
const allocRuns = 100

// scrapeAllocRuns is the run count for the exposition sweeps. It is lower
// because one run of the largest fixture performs thousands of allocations, so
// the average is already stable at this count, and because a sweep pays it once
// per registry size at every shape it measures.
const scrapeAllocRuns = 15

// raceDetectorEnabled reports whether this test binary was built with -race.
// The build settings carry it: `go test -race` records `-race=true`, and a
// plain build records no such setting at all.
func raceDetectorEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// allocCeiling returns the allocation bound for the mode this binary was built
// in. The two numbers are not a fudge factor: the race detector's
// instrumentation defeats some of the compiler's escape analysis, so the
// exposition path measures about a third more allocations per metric under
// -race (7.0 plain against 9.4 per counter family, measured) while the record
// paths stay at exactly 0 in both modes. CI runs the suite with -race, so a
// contract carrying only the plain number would have to be loosened until it
// passed there, which is the same as not gating the plain number; and skipping
// it under -race would leave the contract gating nothing where CI runs it.
func allocCeiling(plain, withRace float64) float64 {
	if raceDetectorEnabled() {
		return withRace
	}
	return plain
}

// discardResponseWriter is an http.ResponseWriter that keeps nothing, so a
// scrape measurement is charged for the exposition and not for a recorder's
// growing body buffer (httptest.ResponseRecorder's buffer doubles as the
// output grows, which adds allocations that track the very thing being
// measured). It implements io.StringWriter because net/http's own response
// writer does: io.WriteString in Registry.Handler then takes the same branch
// it takes in production, rather than the fallback that converts the whole
// exposition to a []byte.
type discardResponseWriter struct{ hdr http.Header }

func (w *discardResponseWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *discardResponseWriter) WriteString(s string) (int, error) { return len(s), nil }

func (w *discardResponseWriter) WriteHeader(int) {}

// labelNames returns n label names ("l0", "l1", ...), for a contract that
// varies a metric's label ARITY.
func labelNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = "l" + strconv.Itoa(i)
	}
	return names
}

// newSeriesLabelValues returns runs+2 distinct label-value slices, each
// carrying nLabels values of at least valueLen bytes, for the contracts that
// measure the SERIES-CREATION path. AllocsPerRun calls its closure runs+1
// times and a label set is created once, so a closure reusing one slice would
// measure the loaded fast path instead; one slice per run is the only way to
// make every run take the cold path. They are built here, outside the measured
// closure, because building one allocates and that cost belongs to the fixture.
func newSeriesLabelValues(runs, nLabels, valueLen int) [][]string {
	pad := strings.Repeat("x", valueLen)
	out := make([][]string, runs+2)
	for i := range out {
		vals := make([]string, nLabels)
		for j := range vals {
			vals[j] = strconv.Itoa(i) + "-" + strconv.Itoa(j) + pad
		}
		out[i] = vals
	}
	return out
}

// loggedValueAttr returns the raw "value" attribute of the captured log line
// mentioning metric, unquoting it when the TextHandler quoted it (it quotes and
// \x-escapes anything that is not a plain printable string, which includes any
// value carrying U+FFFD). Used by the log-truncation tests, whose subject is the
// exact bytes of that attribute. value is the last attribute the sanitization
// warnings carry, so everything after " value=" belongs to it.
func loggedValueAttr(t *testing.T, logs, metric string) string {
	t.Helper()
	var line string
	for l := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(l, metric) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("logs = %q, want a line mentioning metric %s", logs, metric)
	}
	_, attr, ok := strings.Cut(line, " value=")
	if !ok {
		t.Fatalf("log line %q, want a value attribute", line)
	}
	if !strings.HasPrefix(attr, `"`) {
		return attr
	}
	unquoted, err := strconv.Unquote(attr)
	if err != nil {
		t.Fatalf("unquote value attribute %q: %v", attr, err)
	}
	return unquoted
}
