package measure_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/go/measure"
)

// A labeled counter has no series until its first record, so increase() over a
// failure that has never happened reads as no data, and an alert on it cannot
// tell "no failures" from "no series". Seed each label set you know of at 0
// when the program starts, so the first failure shows as an increase.
func ExampleLabeledCounter_Add_seed() {
	failures := measure.NewLabeledCounter("sync_failures_total", "Sync failures by source.", []string{"source"})
	for _, source := range []string{"plex", "sonarr"} {
		failures.Add(0, source)
	}

	var b strings.Builder
	measure.WriteLabeledCounter(&b, failures)
	fmt.Print(b.String())
	// Output:
	// # HELP sync_failures_total Sync failures by source.
	// # TYPE sync_failures_total counter
	// sync_failures_total{source="plex"} 0
	// sync_failures_total{source="sonarr"} 0
}

// Remove one series with Delete when the thing it describes goes away. Never
// Reset and then set the survivors again: a scrape between the two sees an
// empty family, and every alert on it resolves.
func ExampleLabeledGauge_Delete() {
	active := measure.NewLabeledGauge("alerts_active", "Open alerts by source.", []string{"source"})
	active.Set(1, "plex")
	active.Set(1, "sonarr")
	active.Delete("sonarr") // the sonarr source left the configuration

	var b strings.Builder
	measure.WriteLabeledGauge(&b, active)
	fmt.Print(b.String())
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

	var b strings.Builder
	measure.WriteLabeledCounter(&b, reqs)
	measure.WriteLabeledHistogram(&b, byRoute)
	fmt.Print(b.String())
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

	var b strings.Builder
	measure.WriteLabeledGauge(&b, info)
	fmt.Print(b.String())
	// Output:
	// # HELP build_info Build information, always 1.
	// # TYPE build_info gauge
	// build_info{version="v1.4.0"} 1
}
