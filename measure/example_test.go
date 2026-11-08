package measure_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/go/measure"
)

func Example() {
	r := measure.NewRegistry("myapp") // prefixes every registered metric name with "myapp_"
	reqs := measure.NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"method", "status"})
	dur := measure.NewHistogram("http_duration_seconds", "Request latency", measure.WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1, 5}))
	// Exposed as myapp_http_requests_total and myapp_http_duration_seconds.
	// MustRegister panics on a bad metric; Register returns the error instead.
	r.MustRegister(reqs, dur)

	reqs.Inc("GET", "200")
	timer := measure.NewTimer(dur)
	timer.ObserveDuration()

	http.Handle("/metrics", r.Handler())
	// Output:
}

// A labeled counter has no series until its first record, so increase() over a
// failure that has never happened reads as no data, and an alert on it cannot
// tell "no failures" from "no series". Seed each label set you know of at 0
// when the program starts, so the first failure shows as an increase.
func ExampleLabeledCounter_Add_seed() {
	failures := measure.NewLabeledCounter("sync_failures_total", "Sync failures by source.", []string{"source"})
	for _, source := range []string{"plex", "sonarr"} {
		failures.Add(0, source)
	}

	r := measure.NewRegistry("")
	r.MustRegister(failures)
	rec := httptest.NewRecorder()
	r.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	page, _, _ := strings.Cut(rec.Body.String(), "# HELP go_") // the runtime and process families follow
	fmt.Print(page)
	// Output:
	// # HELP sync_failures_total Sync failures by source.
	// # TYPE sync_failures_total counter
	// sync_failures_total{source="plex"} 0
	// sync_failures_total{source="sonarr"} 0
}

// Refresh a labeled gauge from each new snapshot by setting what is there and
// deleting what is gone. Never Reset and then set the survivors again: a
// scrape between the two sees an empty family, and every alert on it
// resolves. A gauge cannot list its series, so keep the previous keys.
func ExampleLabeledGauge_Delete() {
	active := measure.NewLabeledGauge("alerts_active", "Open alerts by source.", []string{"source"})
	prev := map[string]bool{}
	refresh := func(open map[string]float64) {
		for source, n := range open {
			active.Set(n, source)
		}
		for source := range prev {
			if _, ok := open[source]; !ok {
				active.Delete(source)
			}
		}
		clear(prev)
		for source := range open {
			prev[source] = true
		}
	}

	refresh(map[string]float64{"plex": 1, "sonarr": 2})
	refresh(map[string]float64{"plex": 1}) // sonarr's alerts are all resolved

	r := measure.NewRegistry("")
	r.MustRegister(active)
	rec := httptest.NewRecorder()
	r.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	page, _, _ := strings.Cut(rec.Body.String(), "# HELP go_") // the runtime and process families follow
	fmt.Print(page)
	// Output:
	// # HELP alerts_active Open alerts by source.
	// # TYPE alerts_active gauge
	// alerts_active{source="plex"} 1
}

// RecordHTTP takes an unlabeled histogram. For a duration per route, observe a
// labeled histogram in the same middleware, beside RecordHTTP, and pass nil for
// RecordHTTP's histogram.
func ExampleRecordHTTP_routeDuration() {
	reqs := measure.NewLabeledCounter("http_requests_total", "HTTP requests by route and status.", []string{"route", "code"})
	byRoute := measure.NewLabeledHistogram("http_request_duration_seconds", "Request latency by route.",
		[]string{"route"}, measure.WithBuckets([]float64{0.1, 1}))
	record := func(route string, code int, elapsed time.Duration) {
		measure.RecordHTTP(reqs, nil, elapsed, route, strconv.Itoa(code))
		byRoute.Observe(elapsed.Seconds(), route)
	}

	record("/api/items", 200, 40*time.Millisecond)
	record("/api/items", 500, 2*time.Second)

	r := measure.NewRegistry("")
	r.MustRegister(reqs, byRoute)
	rec := httptest.NewRecorder()
	r.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	page, _, _ := strings.Cut(rec.Body.String(), "# HELP go_") // the runtime and process families follow
	fmt.Print(page)
	// Output:
	// # HELP http_requests_total HTTP requests by route and status.
	// # TYPE http_requests_total counter
	// http_requests_total{code="200",route="/api/items"} 1
	// http_requests_total{code="500",route="/api/items"} 1
	// # HELP http_request_duration_seconds Request latency by route.
	// # TYPE http_request_duration_seconds histogram
	// http_request_duration_seconds_bucket{route="/api/items",le="0.1"} 1
	// http_request_duration_seconds_bucket{route="/api/items",le="1"} 1
	// http_request_duration_seconds_bucket{route="/api/items",le="+Inf"} 2
	// http_request_duration_seconds_sum{route="/api/items"} 2.04
	// http_request_duration_seconds_count{route="/api/items"} 2
}

// A build-info gauge carries the version as a label and the value 1, so a query
// can join it onto any other series of the app.
func Example_buildInfo() {
	version := "v1.4.0" // in an app, set at build time with -ldflags "-X main.version=..."
	info := measure.NewLabeledGauge("build_info", "Build information, always 1.", []string{"version"})
	info.Set(1, version)

	r := measure.NewRegistry("")
	r.MustRegister(info)
	rec := httptest.NewRecorder()
	r.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	page, _, _ := strings.Cut(rec.Body.String(), "# HELP go_") // the runtime and process families follow
	fmt.Print(page)
	// Output:
	// # HELP build_info Build information, always 1.
	// # TYPE build_info gauge
	// build_info{version="v1.4.0"} 1
}
