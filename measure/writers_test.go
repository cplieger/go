package measure

import "strings"

// The text writers below write one valid metric as Handler would. Handler
// reaches each metric through the registry's gather path, which refuses a
// metric carrying a construction error at Register, so these exist only for
// the tests that check one metric's exposition on its own.

// writeCounter writes one metric as Handler would, for tests.
func writeCounter(b *strings.Builder, c *Counter) {
	appendPrometheus(b, []metricFamily{c.family()})
}

// writeLabeledCounter writes one metric as Handler would, for tests.
func writeLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	if f, ok := lc.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}

// writeGauge writes one metric as Handler would, for tests.
func writeGauge(b *strings.Builder, g *Gauge) {
	appendPrometheus(b, []metricFamily{g.family()})
}

// writeLabeledGauge writes one metric as Handler would, for tests.
func writeLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	if f, ok := lg.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}

// writeHistogram writes one metric as Handler would, for tests.
func writeHistogram(b *strings.Builder, h *Histogram) {
	appendPrometheus(b, []metricFamily{h.family()})
}

// writeLabeledHistogram writes one metric as Handler would, for tests.
func writeLabeledHistogram(b *strings.Builder, lh *LabeledHistogram) {
	if f, ok := lh.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}

// writeProcess writes one metric as Handler would, for tests.
func writeProcess(b *strings.Builder) {
	appendPrometheus(b, processFamilies())
}
