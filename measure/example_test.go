package measure_test

import (
	"net/http"

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
