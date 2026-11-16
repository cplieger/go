package measure

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Seeding: the label sets known at start appear at 0 the moment the
// instrument exists; a value that appears later is recorded by the code that
// adds it; a read series emits only what its function reads.

func TestWithSeedPutsEachKnownLabelSetOnThePageAt0(t *testing.T) {
	r := NewRegistry("app")
	failures := NewLabeledCounter("sync_failures_total", "h", UnitCount, []string{"source", "reason"},
		WithSeed([]string{"plex", "timeout"}, []string{"sonarr", "timeout"}))
	up := NewLabeledGauge("host_up", "h", UnitNone, []string{"host"}, WithSeed([]string{"a"}, []string{"b"}))
	wait := NewLabeledHistogram("wait_seconds", "h", UnitSeconds, []string{"queue"},
		WithBuckets([]float64{1}), WithSeed([]string{"ingest"}))
	health := NewStateSet("health", "h", []string{"provider", "health"}, []string{"ok", "down"}, WithSeed([]string{"a"}))
	r.MustRegister(failures, up, wait, health)

	page := body(t, r)
	for _, line := range []string{
		`app_sync_failures_total{reason="timeout",source="plex"} 0`,
		`app_sync_failures_total{reason="timeout",source="sonarr"} 0`,
		`app_host_up{host="a"} 0`,
		`app_host_up{host="b"} 0`,
		`app_wait_seconds_bucket{queue="ingest",le="1"} 0`,
		`app_wait_seconds_bucket{queue="ingest",le="+Inf"} 0`,
		`app_wait_seconds_sum{queue="ingest"} 0`,
		`app_wait_seconds_count{queue="ingest"} 0`,
		`app_health{health="down",provider="a"} 0`,
		`app_health{health="ok",provider="a"} 0`,
	} {
		if !strings.Contains(page, line+"\n") {
			t.Errorf("page has no seeded line %q:\n%s", line, page)
		}
	}
}

func TestASeededSeriesRecordsLikeAnyOther(t *testing.T) {
	r := NewRegistry("")
	failures := NewLabeledCounter("failures_total", "h", UnitCount, []string{"source"}, WithSeed([]string{"plex"}))
	r.MustRegister(failures)
	failures.Inc("plex")
	failures.Add(0, "radarr") // a source added at run time, by the code that adds it
	fams, err := r.GatherOf(t.Context(), failures)
	if err != nil {
		t.Fatalf("GatherOf: %v", err)
	}
	for vals, want := range map[string]float64{"plex": 1, "radarr": 0} {
		if s, ok := fams[0].Sample(vals); !ok || s.Value != want {
			t.Errorf("failures_total{source=%q} = %v, %v, want %v", vals, s.Value, ok, want)
		}
	}
}

func TestAnUnseededLabeledInstrumentHasNoSeriesUntilItsFirstRecord(t *testing.T) {
	r := NewRegistry("")
	reqs := NewLabeledCounter("http_requests_total", "h", UnitCount, []string{"method", "route", "code"})
	r.MustRegister(reqs)
	if page := body(t, r); strings.Contains(page, "http_requests_total") {
		t.Fatalf("the HTTP request counter is on the page before its first request:\n%s", page)
	}
	RecordHTTP(reqs, nil, time.Millisecond, "GET", "/api", "200")
	if page := body(t, r); !strings.Contains(page, `http_requests_total{code="200",method="GET",route="/api"} 1`+"\n") {
		t.Errorf("RecordHTTP's series is not on the page:\n%s", page)
	}
}

func TestAReadSeriesServesOnlyWhatItsFunctionReads(t *testing.T) {
	r := NewRegistry("")
	reachable := NewGaugeDesc("host_reachable", "h", UnitNone, []string{"host"})
	hosts := map[string]bool{"a": true}
	r.MustRegister(NewCollector(func(_ context.Context, e *Emitter) {
		for h, up := range hosts {
			if up {
				e.Emit(reachable, 1, h)
			}
		}
	}, reachable))

	if page := body(t, r); !strings.Contains(page, `host_reachable{host="a"} 1`+"\n") {
		t.Fatalf("page has no host a:\n%s", page)
	}
	hosts["a"] = false
	if page := body(t, r); strings.Contains(page, "host_reachable") {
		t.Errorf("a series its function no longer reads stayed on the page:\n%s", page)
	}
}

func TestDeleteOfTheEmptyLabelSetTakesTheGaugeOffThePage(t *testing.T) {
	r := NewRegistry("app")
	resident := NewLabeledGauge("engine_resident_memory_bytes", "h", UnitBytes, nil)
	r.MustRegister(resident)
	resident.Set(1024)
	if page := body(t, r); !strings.Contains(page, "app_engine_resident_memory_bytes 1024\n") {
		t.Fatalf("page has no engine memory:\n%s", page)
	}
	resident.Delete()
	if page := body(t, r); strings.Contains(page, "app_engine_resident_memory_bytes") {
		t.Errorf("Delete() left the gauge on the page:\n%s", page)
	}
	resident.Set(2048)
	if page := body(t, r); !strings.Contains(page, "app_engine_resident_memory_bytes 2048\n") {
		t.Errorf("Set after Delete() did not bring the gauge back:\n%s", page)
	}
}

func TestLabeledHistogramSeedPutsARunTimeLabelSetOnThePageAt0(t *testing.T) {
	r := NewRegistry("app")
	wait := NewLabeledHistogram("profile_wait_seconds", "h", UnitSeconds, []string{"profile"}, WithBuckets([]float64{0.5, 2}))
	r.MustRegister(wait)
	if page := body(t, r); strings.Contains(page, "app_profile_wait_seconds_") {
		t.Fatalf("an unseeded histogram has series before its first record:\n%s", page)
	}
	wait.Seed("created-in-ui")
	page := body(t, r)
	for _, line := range []string{
		`app_profile_wait_seconds_bucket{profile="created-in-ui",le="0.5"} 0`,
		`app_profile_wait_seconds_bucket{profile="created-in-ui",le="2"} 0`,
		`app_profile_wait_seconds_bucket{profile="created-in-ui",le="+Inf"} 0`,
		`app_profile_wait_seconds_sum{profile="created-in-ui"} 0`,
		`app_profile_wait_seconds_count{profile="created-in-ui"} 0`,
	} {
		if !strings.Contains(page, line+"\n") {
			t.Errorf("page has no seeded line %q:\n%s", line, page)
		}
	}
}

func TestLabeledHistogramSeedOfAnExistingLabelSetChangesNothing(t *testing.T) {
	r := NewRegistry("")
	wait := NewLabeledHistogram("wait_seconds", "h", UnitSeconds, []string{"queue"},
		WithBuckets([]float64{1}), WithSeed([]string{"ingest"}))
	r.MustRegister(wait)
	wait.Observe(0.25, "ingest")
	before := linesOf(body(t, r), "wait_seconds")
	wait.Seed("ingest")
	wait.Seed("ingest")
	if after := linesOf(body(t, r), "wait_seconds"); after != before {
		t.Errorf("Seed of an observed label set changed the page:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(before, `wait_seconds_count{queue="ingest"} 1`+"\n") {
		t.Errorf("the observation is not on the page:\n%s", before)
	}
}

// linesOf returns page's lines that start with metric, so a comparison of two
// scrapes skips the process series that move between them.
func linesOf(page, metric string) string {
	var b strings.Builder
	for line := range strings.Lines(page) {
		if strings.HasPrefix(line, metric) {
			b.WriteString(line)
		}
	}
	return b.String()
}

func TestLabeledHistogramDeleteRemovesASeededLabelSet(t *testing.T) {
	r := NewRegistry("")
	wait := NewLabeledHistogram("wait_seconds", "h", UnitSeconds, []string{"queue"}, WithBuckets([]float64{1}))
	r.MustRegister(wait)
	wait.Seed("ingest")
	wait.Seed("export")
	wait.Delete("ingest")
	page := body(t, r)
	if strings.Contains(page, `queue="ingest"`) {
		t.Errorf("Delete left the seeded label set on the page:\n%s", page)
	}
	if !strings.Contains(page, `wait_seconds_count{queue="export"} 0`+"\n") {
		t.Errorf("Delete of one label set removed another:\n%s", page)
	}
}

func TestLabeledHistogramSeedPanicsOnALabelArityMismatch(t *testing.T) {
	wait := NewLabeledHistogram("wait_seconds", "h", UnitSeconds, []string{"queue", "tier"})
	mustPanicContaining(t, "label arity mismatch", func() { wait.Seed("ingest") })
}

func TestLabeledHistogramSeedOnAHistogramOverTheLabelCapRecordsNothing(t *testing.T) {
	_ = captureDebugLogs(t) // absorbs the one-time inert-record warning
	names := labelNames(maxLabels + 1)
	wait := NewLabeledHistogram("wait_seconds", "h", UnitSeconds, names)
	wait.Seed(names...)
	if got := len(wait.vals); got != 0 {
		t.Errorf("a histogram over the label cap holds %d series after Seed, want 0", got)
	}
	mustRegisterError(t, NewRegistry(""), wait, "supports at most 8 labels")
}

// Serial: it captures slog.Default().
func TestLabeledHistogramSeedCountsTowardTheSeriesWarning(t *testing.T) {
	buf := captureDebugLogs(t)
	wait := NewLabeledHistogram("profile_wait_seconds", "h", UnitSeconds, []string{"profile"})
	for i := range cardinalityWarnThreshold - 1 {
		wait.Observe(1, strconv.Itoa(i))
	}
	if strings.Contains(buf.String(), "possible label-cardinality explosion") {
		t.Fatalf("the series warning fired below the threshold:\n%s", buf.String())
	}
	wait.Seed("one-more")
	if got := strings.Count(buf.String(), "possible label-cardinality explosion"); got != 1 {
		t.Errorf("series warnings after Seed reached the threshold = %d, want 1\nlogs: %s", got, buf.String())
	}
}
