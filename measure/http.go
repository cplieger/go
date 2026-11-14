package measure

import "time"

// RecordHTTP records one request: it increments c with labelVals, which
// must match c's label names, and observes d in seconds on h. Either metric
// may be nil to skip it. Name c's labels method, route and code, with values
// from a bounded set: a registered route, never a raw path. h is unlabeled to
// keep its cardinality fixed; for a latency per route, observe a
// LabeledHistogram beside it and pass nil. Middleware calls RecordHTTP once
// the response is complete; httpserver's WithRecordRouteMetric hook supplies
// bounded method, route and status values.
func RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string) {
	if c != nil {
		c.Inc(labelVals...)
	}
	if h != nil {
		h.Observe(d.Seconds())
	}
}
