package measure

import (
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func (*failWriter) Write([]byte) (int, error) {
	return 0, errors.New("forced write failure")
}

func (*failWriter) WriteHeader(int) {}

// captureDebugLogs points the default slog logger at an in-memory buffer at
// Debug level for the test and returns the buffer; callers stay serial.
// slog.SetDefault also redirects the standard log package and does not undo
// that when the previous logger is reinstalled, so log's writer and flags
// are restored explicitly, after slog, which would otherwise redo it.
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

// The shared machinery for the allocation contracts beside each source file.
// Recording into an existing series allocates nothing and creating one costs
// a bounded constant; the weekly tracker alerts on ratios and cannot see a
// bounded count drift. Every contract builds its fixtures outside the
// measured closure, says whether it measures the loaded path or series
// creation (AllocsPerRun runs the closure runs+1 times), and stays off
// t.Parallel, since AllocsPerRun reads process-wide counters.

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

func (*discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (*discardResponseWriter) WriteString(s string) (int, error) { return len(s), nil }

func (*discardResponseWriter) WriteHeader(int) {}

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
