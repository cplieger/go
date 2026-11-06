package measure

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenFixtureRegistry builds a deterministic registry exercising every metric
// type (labeled + unlabeled) plus the Prometheus 0.0.4 escaping edge cases.
// Process metrics are added by the handlers automatically. The fixture is fully
// deterministic so the user-metric portion of the exposition can be
// byte-compared against committed golden files; process-metric VALUES are
// non-deterministic and are masked before comparison (see maskProcessValues).
func goldenFixtureRegistry() *Registry {
	r := NewRegistry("")

	// Labeled counter, including a _total-suffixed name (the common convention)
	// and a label value containing backslash, quote, and newline to lock label
	// escaping.
	lc := NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	r.MustRegister(lc)
	lc.Add(3, "GET", "/api", "200")
	lc.Add(1, "POST", `/a"b\c`, "500")
	lc.Add(7, "GET", "/health", "200")

	// Unlabeled counter WITH _total suffix.
	tasks := NewCounter("tasks_total", "Total tasks processed")
	r.MustRegister(tasks)
	tasks.Add(42)

	// Unlabeled counter WITHOUT _total suffix — locks the raw-name rendering
	// (no suffix is appended or stripped).
	events := NewCounter("events", "Total events")
	r.MustRegister(events)
	events.Add(5)

	// Labeled gauge.
	lg := NewLabeledGauge("queue_depth", "Items queued", []string{"queue"})
	r.MustRegister(lg)
	lg.Set(12, "ingest")
	lg.Set(0.5, "egress")

	// Unlabeled gauge, fractional value (shortest round-trip rendering).
	temp := NewGauge("temperature_celsius", "Ambient temperature")
	r.MustRegister(temp)
	temp.Set(23.5)

	// Unlabeled gauge, whole value (bare-integer rendering).
	conns := NewGauge("active_connections", "Active connections")
	r.MustRegister(conns)
	conns.Set(8)

	// Unlabeled histogram with HELP text containing backslash, newline, and a
	// double-quote — locks the HELP escaping rule (backslash and newline
	// escaped, the double-quote left raw).
	hist := NewHistogram("request_duration_seconds", "Request latency in \"seconds\"\nline2\\end", WithBuckets([]float64{0.1, 0.5, 1}))
	r.MustRegister(hist)
	hist.Observe(0.05)
	hist.Observe(0.1) // exactly on a bound
	hist.Observe(0.3)
	hist.Observe(2.0)

	// Labeled histogram.
	lh := NewLabeledHistogram("api_latency_seconds", "API latency", []string{"endpoint"}, WithBuckets([]float64{0.25, 1}))
	r.MustRegister(lh)
	lh.Observe(0.1, "list")
	lh.Observe(5.0, "create")
	lh.Observe(0.5, "list")

	return r
}

// maskProcessValues normalises non-deterministic process-metric sample VALUES
// so the exposition format (family ordering, HELP/TYPE lines and their order,
// sample series names) can be locked in a golden file while the volatile
// numeric values are ignored. Sample lines whose series name begins with
// "process_" or "go_" (the built-in Go runtime / process families, e.g.
// go_goroutines and go_memstats_heap_alloc_bytes) are masked; comment lines
// (# HELP / # TYPE) are deterministic and pass through untouched. The fixture
// registers no user metric under those prefixes, so the mask targets only the
// built-ins.
func maskProcessValues(exposition string) string {
	lines := strings.Split(exposition, "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(series, "{")
		if name == pmGoInfo {
			series = pmGoInfo + `{version="<version>"}` // the toolchain building the test
		}
		if strings.HasPrefix(name, "process_") || strings.HasPrefix(name, "go_") {
			lines[i] = series + " <value>"
		}
	}
	return strings.Join(lines, "\n")
}

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// assertGolden compares got against the committed golden file. Set
// UPDATE_GOLDEN=1 to (re)generate the fixtures.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		t.Logf("updated golden %s (%d bytes)", name, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with UPDATE_GOLDEN=1 to create): %v", name, err)
	}
	if string(want) != got {
		t.Errorf("exposition does not match golden %s.\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// TestGolden_PrometheusExposition locks the Prometheus 0.0.4 exposition bytes.
// The page holds every process family, as Linux serves it;
// TestProcessFamiliesMatchClientGolang checks the macOS set.
func TestGolden_PrometheusExposition(t *testing.T) {
	if procExpected != procOKAll {
		t.Skip("the golden page holds every process family, which this platform does not serve")
	}
	r := goldenFixtureRegistry()
	got := maskProcessValues(serve(t, r.Handler()))
	assertGolden(t, "prometheus.golden", got)
}

// TestGolden_ContentType locks the exposition content type, which is part of
// the contract the encoder must preserve.
func TestGolden_ContentType(t *testing.T) {
	r := goldenFixtureRegistry()

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("prometheus Content-Type = %q", ct)
	}
}
