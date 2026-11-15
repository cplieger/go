package measure

import "strings"

// The text writers below write one valid metric as Handler would. Handler
// reaches each metric through the registry's gather path, which refuses a
// metric carrying a construction error at Register, so these exist only for
// the tests that check one metric's exposition on its own.

// writeFamily writes one instrument as Handler would, for tests.
func writeFamily(b *strings.Builder, m instrument) {
	if f, ok := m.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}

// writeProcess writes one metric as Handler would, for tests.
func writeProcess(b *strings.Builder) {
	appendPrometheus(b, processFamilies())
}
