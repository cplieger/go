package measure

import "time"

// RecordHTTP records one HTTP request into the caller-supplied counter and
// histogram. The caller owns the label set (arity, ordering, and any path
// templating); labelVals must match c's label names. h receives d.Seconds().
// Either metric may be nil to skip it. The latency histogram is unlabeled on
// purpose, to keep its cardinality fixed: for a latency per route, observe a
// LabeledHistogram beside RecordHTTP and pass nil here.
//
// Name c's labels method, route and code. Label values must come from a
// bounded set: each distinct combination allocates a series retained until
// Delete/Reset, so raw paths, raw methods or header values allow unbounded
// memory growth.
//
// This is the metrics-side hook only: it takes no request and no status, so
// middleware captures both and calls RecordHTTP once the response is complete.
// Its parameters are this package's own types, so middleware that does not
// import measure cannot take RecordHTTP as its hook; the wiring is a small
// adapter that spreads the middleware's per-request values onto labelVals.
//
// With httpserver, adapt the access-log hook WithRecordRouteMetric registers:
// its method is collapsed to the nine standard methods or "other" and its path
// is the matched pattern's template, both bounded as the rule above asks, and
// the access logger records the status itself. A zero-dependency module cannot
// ship a compiling example of that pairing, so the hook's signature and label
// derivation live in httpserver's own godoc for WithRecordRouteMetric and
// RequestMetric.
func RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string) {
	if c != nil {
		c.Inc(labelVals...)
	}
	if h != nil {
		h.Observe(d.Seconds())
	}
}
