package measure

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestNewCounter_InvalidNameErrorsAtRegister pins the v4 name-validation
// shape: an invalid metric name no longer panics at construction — it is
// captured into the metric and surfaces as the Register error.
func TestNewCounter_InvalidNameErrorsAtRegister(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"metric-name",
		"metric.name",
		"metric name",
		"metric\x00name",
		"metric\nname",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			c := NewCounter(name, "test") // must not panic
			mustRegisterError(t, NewRegistry(""), c, fmt.Sprintf("invalid metric name %q", name))
		})
	}
}

func TestNewCounter_ValidNameRegisters(t *testing.T) {
	valid := []string{
		"a",
		"_private",
		":colon",
		"abc_123",
		"A_B_C",
		"metric:submetric",
		"__internal",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if err := NewRegistry("").Register(NewCounter(name, "test")); err != nil {
				t.Errorf("Register(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestNewLabeledCounter_InvalidLabelNameErrorsAtRegister(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"label-name",
		"label:name", // colons not allowed in labels
		"label.name",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			lc := NewLabeledCounter("valid_metric", "test", []string{name}) // must not panic
			mustRegisterError(t, NewRegistry(""), lc, fmt.Sprintf(`invalid label name %q for metric "valid_metric"`, name))
		})
	}
}

// TestErroredMetric_RecordPathsAreInert pins the no-op contract for a metric
// carrying a construction error: every record method returns without
// recording and without panicking — including calls that would otherwise trip
// the label-arity or negative-delta panics, since an errored metric's label
// set is not trustworthy enough to judge arity against and there is nothing
// to corrupt. Serial: it captures slog.Default (the inert drops warn once per
// metric; TestErroredMetric_FirstDropWarnsOnce pins that contract).
func TestErroredMetric_RecordPathsAreInert(t *testing.T) {
	_ = captureDebugLogs(t) // absorb the expected one-time inert-record warnings

	c := NewCounter("bad name", "x")
	c.Inc()
	c.Add(-1) // would panic on a valid counter
	if got := c.val.Load(); got != 0 {
		t.Errorf("errored Counter value = %d, want 0", got)
	}

	g := NewGauge("bad name", "x")
	g.Set(3)
	g.Add(1)
	g.Sub(1)
	if got := g.Value(); got != 0 {
		t.Errorf("errored Gauge value = %v, want 0", got)
	}

	lc := NewLabeledCounter("inert_total", "x", []string{"bad-label", "m"})
	lc.Inc("wrong", "arity", "here") // would panic on a valid counter
	lc.Add(-5)                       // negative AND wrong arity: still a no-op
	lc.Delete("wrong")
	lc.Reset()
	if got := len(lc.vals); got != 0 {
		t.Errorf("errored LabeledCounter series = %d, want 0", got)
	}

	lg := NewLabeledGauge("inert_gauge", "x", []string{"__rsv"})
	lg.Set(1, "too", "many")
	lg.Delete("too", "many")
	if got := len(lg.vals); got != 0 {
		t.Errorf("errored LabeledGauge series = %d, want 0", got)
	}

	h := NewHistogram("inert_hist", "x", WithBuckets([]float64{2, 1}))
	h.Observe(0.5)
	if got := h.count.Load(); got != 0 {
		t.Errorf("errored Histogram count = %d, want 0", got)
	}

	lh := NewLabeledHistogram("inert_seconds", "x", []string{"le"})
	lh.Observe(0.5, "too", "many")
	tm := lh.NewTimer("wrong", "arity") // eager arity check is skipped when errored
	tm.ObserveDuration()                // observes into the inert histogram: no-op
	lh.Delete("v")
	if got := len(lh.vals); got != 0 {
		t.Errorf("errored LabeledHistogram series = %d, want 0", got)
	}
}

// TestConstruction_FirstErrorWins pins the capture order when a constructor
// sees several violations: the metric-name error is captured, and it is what
// registration reports (naming the offending metric).
func TestConstruction_FirstErrorWins(t *testing.T) {
	lc := NewLabeledCounter("bad name", "x", []string{"also-bad"})
	mustRegisterError(t, NewRegistry(""), lc, `invalid metric name "bad name"`)
}

// TestErroredMetric_FirstDropWarnsOnce pins the inert-record diagnostic for
// every metric type: the first record dropped on a metric carrying a
// construction error emits exactly one slog warning naming the metric and the
// error (mirroring the one-time cardinality warning), and later records stay
// silent no-ops. Registration is the reporting door, but a metric constructed
// and never registered would otherwise die with no diagnostic at all. Serial:
// it captures slog.Default.
func TestErroredMetric_FirstDropWarnsOnce(t *testing.T) {
	const warnMsg = "record dropped, metric carries a construction error"
	cases := []struct {
		kind   string
		metric string
		record func()
	}{
		{"Counter", "bad name", func() {
			c := NewCounter("bad name", "x")
			c.Inc()
			c.Add(3)
		}},
		{"Gauge Set then Add", "bad gauge", func() {
			g := NewGauge("bad gauge", "x")
			g.Set(1)
			g.Add(2)
		}},
		{"LabeledCounter", "inert_total", func() {
			lc := NewLabeledCounter("inert_total", "x", []string{"bad-label"})
			lc.Inc("v")
			lc.Add(2, "v")
		}},
		{"LabeledGauge", "inert_gauge", func() {
			lg := NewLabeledGauge("inert_gauge", "x", []string{"__rsv"})
			lg.Set(1, "v")
			lg.Set(2, "v")
		}},
		{"Histogram", "inert_hist", func() {
			h := NewHistogram("inert_hist", "x", WithBuckets([]float64{2, 1}))
			h.Observe(0.5)
			h.Observe(0.7)
		}},
		{"LabeledHistogram via Timer", "inert_seconds", func() {
			lh := NewLabeledHistogram("inert_seconds", "x", []string{"le"})
			lh.NewTimer("v").ObserveDuration()
			lh.Observe(0.5, "v")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			buf := captureDebugLogs(t)
			tc.record()
			logs := buf.String()
			if got := strings.Count(logs, warnMsg); got != 1 {
				t.Fatalf("inert-record warnings after two records = %d, want exactly 1\nlogs: %s", got, logs)
			}
			if !strings.Contains(logs, "metric="+strconv.Quote(tc.metric)) && !strings.Contains(logs, "metric="+tc.metric) {
				t.Errorf("logs = %q, want warning naming metric %q", logs, tc.metric)
			}
			if !strings.Contains(logs, "error=") {
				t.Errorf("logs = %q, want warning carrying the construction error", logs)
			}
		})
	}
}

// TestValidMetric_RecordPathNeverWarns guards the fast path: a valid metric's
// records emit no inert-record warning (the warn branch is reachable only
// when a construction error was captured). Serial: it captures slog.Default.
func TestValidMetric_RecordPathNeverWarns(t *testing.T) {
	buf := captureDebugLogs(t)
	c := NewCounter("valid_total", "x")
	c.Inc()
	c.Add(2)
	if logs := buf.String(); strings.Contains(logs, "record dropped") {
		t.Errorf("valid metric records logged an inert-record warning: %q", logs)
	}
}

// TestValidate_CharacterRangeBoundaries pins the inclusive ends of isLetter's
// and isDigit's ranges: 'Z' is a letter, '0' and '9' are digits in a
// non-initial position. Dropping a boundary character would reject these
// otherwise-valid names.
func TestValidate_CharacterRangeBoundaries(t *testing.T) {
	if !isValidMetricName("Z") {
		t.Error(`isValidMetricName("Z") = false, want true (isLetter 'Z' boundary)`)
	}
	if !isValidMetricName("a0") {
		t.Error(`isValidMetricName("a0") = false, want true (isDigit '0' boundary)`)
	}
	if !isValidMetricName("a9") {
		t.Error(`isValidMetricName("a9") = false, want true (isDigit '9' boundary)`)
	}
}

func TestIsValidLabelName_digitAfterFirstChar(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"letter then digit", "label1", true},
		{"underscore then digit", "_0", true},
		{"letters and digits mixed", "http2xx", true},
		{"leading digit rejected", "1label", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidLabelName(tc.in); got != tc.want {
				t.Errorf("isValidLabelName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewLabeled_DuplicateLabelErrorsAtRegister pins the set-level uniqueness
// invariant: constructing any labeled metric with a repeated label name
// captures an error naming both the label and the metric, which surfaces at
// registration, since duplicate names would otherwise emit invalid series
// like {a="x",a="y"}.
func TestNewLabeled_DuplicateLabelErrorsAtRegister(t *testing.T) {
	dup := []string{"method", "method"}
	cases := []struct {
		name   string
		metric string
		make   func() Metric
	}{
		{"LabeledCounter", "dup_counter_total", func() Metric { return NewLabeledCounter("dup_counter_total", "test", dup) }},
		{"LabeledGauge", "dup_gauge", func() Metric { return NewLabeledGauge("dup_gauge", "test", dup) }},
		{"LabeledHistogram", "dup_hist_seconds", func() Metric { return NewLabeledHistogram("dup_hist_seconds", "test", dup) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustRegisterError(t, NewRegistry(""), tc.make(),
				fmt.Sprintf(`duplicate label name "method" for metric %q`, tc.metric))
		})
	}
}

// TestNewLabeledCounter_MutatingCallerSliceIsolated verifies the constructor owns
// its label-name slice: mutating the caller's original slice after construction
// does not alter the emitted label names.
func TestNewLabeledCounter_MutatingCallerSliceIsolated(t *testing.T) {
	labels := []string{"region"}
	lc := NewLabeledCounter("iso_counter_total", "test", labels)
	labels[0] = "zone" // mutate caller slice after construction
	lc.Inc("us-east")

	var b strings.Builder
	writeLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `region="us-east"`) {
		t.Errorf("expected owned label name region, got: %s", out)
	}
	if strings.Contains(out, `zone=`) {
		t.Errorf("caller-slice mutation leaked into emitted labels: %s", out)
	}
}

// TestNewLabeledHistogram_MutationCannotBypassLeGuard verifies that owning the
// label-name slice prevents a post-construction mutation of the caller's slice
// from injecting the reserved "le" name into the emitted series.
func TestNewLabeledHistogram_MutationCannotBypassLeGuard(t *testing.T) {
	labels := []string{"path"}
	lh := NewLabeledHistogram("iso_hist_seconds", "test", labels)
	labels[0] = "le" // would collide with the reserved bucket-bound label if not owned
	lh.Observe(0.5, "/api")

	var b strings.Builder
	writeLabeledHistogram(&b, lh)
	out := b.String()
	if !strings.Contains(out, `path="/api"`) {
		t.Errorf("expected owned label name path, got: %s", out)
	}
	// The only le="..." occurrences must be the bucket-bound label, never a user
	// label carrying the observed value "/api".
	if strings.Contains(out, `le="/api"`) {
		t.Errorf("caller-slice mutation bypassed the reserved le guard: %s", out)
	}
}

// TestNewLabeled_ReservedPrefixErrorsAtRegister pins the Prometheus data-model
// guard: a label name beginning with the reserved "__" prefix (used for
// internal names like __name__) is captured at construction of any labeled
// metric and surfaces at registration naming both the label and the metric,
// matching client_golang's checkLabelName rule.
func TestNewLabeled_ReservedPrefixErrorsAtRegister(t *testing.T) {
	reserved := []string{"__foo", "__name__", "__"}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			cases := []struct {
				kind   string
				metric string
				make   func() Metric
			}{
				{"LabeledCounter", "rsv_counter_total", func() Metric { return NewLabeledCounter("rsv_counter_total", "test", []string{name}) }},
				{"LabeledGauge", "rsv_gauge", func() Metric { return NewLabeledGauge("rsv_gauge", "test", []string{name}) }},
				{"LabeledHistogram", "rsv_hist_seconds", func() Metric { return NewLabeledHistogram("rsv_hist_seconds", "test", []string{name}) }},
			}
			for _, tc := range cases {
				t.Run(tc.kind, func(t *testing.T) {
					mustRegisterError(t, NewRegistry(""), tc.make(),
						fmt.Sprintf(`label name %q for metric %q uses the reserved "__" prefix`, name, tc.metric))
				})
			}
		})
	}
}

// TestValidateLabelNames_SingleUnderscoreValid verifies a single leading
// underscore stays a valid label name; only the double-underscore "__" prefix
// is reserved.
func TestValidateLabelNames_SingleUnderscoreValid(t *testing.T) {
	cases := []struct {
		kind string
		make func() Metric
	}{
		{"LabeledCounter", func() Metric { return NewLabeledCounter("us_counter_total", "test", []string{"_foo"}) }},
		{"LabeledGauge", func() Metric { return NewLabeledGauge("us_gauge", "test", []string{"_foo"}) }},
		{"LabeledHistogram", func() Metric { return NewLabeledHistogram("us_hist_seconds", "test", []string{"_foo"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if err := NewRegistry("").Register(tc.make()); err != nil {
				t.Errorf("Register = %v, want nil for a single-underscore label", err)
			}
		})
	}
}

// TestSanitizeLabelValues_InvalidUTF8NoPanic pins the record-time UTF-8
// policy: recording an invalid-UTF-8 label value never panics — the value is
// sanitized with the Unicode replacement character U+FFFD, the sanitized
// series appears in the exposition, a warning is logged exactly once, and a
// second record of the same bad value increments the SAME sanitized series
// without re-warning. Serial: it captures slog.Default.
func TestSanitizeLabelValues_InvalidUTF8NoPanic(t *testing.T) {
	buf := captureDebugLogs(t)
	bad := "\xff\xfe"
	lc := NewLabeledCounter("sanval_counter_total", "test", []string{"m"})
	lc.Inc(bad) // must not panic

	var b strings.Builder
	writeLabeledCounter(&b, lc)
	if out := b.String(); !strings.Contains(out, "\uFFFD") {
		t.Errorf("exposition missing U+FFFD replacement: %q", out)
	}
	if got := strings.Count(buf.String(), "label value contained invalid UTF-8"); got != 1 {
		t.Fatalf("sanitize warnings after first record = %d, want 1", got)
	}

	lc.Inc(bad) // same bad value: same sanitized series, no second warn
	if got := len(lc.vals); got != 1 {
		t.Fatalf("series count after repeated bad value = %d, want 1", got)
	}
	for _, v := range lc.vals {
		if got := v.Load(); got != 2 {
			t.Errorf("sanitized series value = %d, want 2", got)
		}
	}
	if got := strings.Count(buf.String(), "label value contained invalid UTF-8"); got != 1 {
		t.Errorf("sanitize warnings after second record = %d, want 1 (no re-warn)", got)
	}
}

// TestSanitizeLabelValues_NoPanicAllTypes verifies the no-panic UTF-8 policy
// holds across every labeled metric type. Serial: it captures slog.Default.
func TestSanitizeLabelValues_NoPanicAllTypes(t *testing.T) {
	bad := "\xff\xfe"
	cases := []struct {
		kind string
		fn   func()
	}{
		{"LabeledCounter", func() { NewLabeledCounter("sanval2_counter_total", "test", []string{"m"}).Inc(bad) }},
		{"LabeledGauge", func() { NewLabeledGauge("sanval_gauge", "test", []string{"m"}).Set(1, bad) }},
		{"LabeledHistogram", func() { NewLabeledHistogram("sanval_hist_seconds", "test", []string{"m"}).Observe(0.5, bad) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			buf := captureDebugLogs(t)
			tc.fn() // must not panic
			if !strings.Contains(buf.String(), "label value contained invalid UTF-8") {
				t.Errorf("logs = %q, want sanitize warning", buf.String())
			}
		})
	}
}

// TestSanitizeLabelValues_LongInvalidUTF8LogTruncated pins the maxLogValueLen
// truncation: a hostile multi-hundred-byte invalid label value sanitizes
// without panicking, and the warning's value attribute retains a bounded
// prefix with the shared "..." truncation marker (the textsafe convention)
// while dropping the attacker-controlled tail. Serial: it captures slog.Default.
func TestSanitizeLabelValues_LongInvalidUTF8LogTruncated(t *testing.T) {
	buf := captureDebugLogs(t)
	longInvalid := strings.Repeat("a", maxLogValueLen) + "\xff" + strings.Repeat("b", 64)

	NewLabeledCounter("long_sanval_total", "test", []string{"m"}).Inc(longInvalid) // must not panic

	logs := buf.String()
	if !strings.Contains(logs, "label value contained invalid UTF-8") {
		t.Fatalf("logs = %q, want sanitize warning", logs)
	}
	if !strings.Contains(logs, "a...") {
		t.Errorf("logs = %q, want the \"...\" marker at the cut joint", logs)
	}
	if !strings.Contains(logs, strings.Repeat("a", 32)) {
		t.Errorf("logs = %q, want retained prefix", logs)
	}
	if strings.Contains(logs, strings.Repeat("b", 16)) {
		t.Errorf("logs = %q, want attacker-controlled tail truncated", logs)
	}

	// Multi-byte rune straddling byte 256: after sanitization the value is
	// 255 'a's, then U+00E9 occupying bytes 255-256, so a naive byte-256 cut
	// would split the rune. The logged value attribute must stay valid UTF-8
	// (truncateForLog backs the cut off to the previous rune boundary).
	buf.Reset()
	straddle := strings.Repeat("a", maxLogValueLen-1) + "\u00e9\xff" + strings.Repeat("b", 64)
	NewLabeledCounter("long_sanval_straddle_total", "test", []string{"m"}).Inc(straddle) // must not panic

	attr := loggedValueAttr(t, buf.String(), "long_sanval_straddle_total")
	if !utf8.ValidString(attr) {
		t.Errorf("logged value attribute %q is not valid UTF-8; truncation split a rune", attr)
	}
	// The straddling rune is DROPPED, not carried: the cut moves back to the
	// boundary below the cap, so the marker follows the last whole 'a' and the
	// U+00E9 spanning the cap never reaches the log.
	want := strings.Repeat("a", maxLogValueLen-1) + "..."
	if attr != want {
		t.Errorf("logged value attribute = %q, want %q (cut backs off to the rune boundary below the cap)", attr, want)
	}
}

// TestSanitizeLabelValues_ValueExactlyAtLogCapLoggedWhole pins the log-cap
// boundary itself: a sanitized value of exactly maxLogValueLen bytes is within
// the cap, so the warning carries it whole with no truncation marker. The input
// is maxLogValueLen-3 ASCII bytes plus one invalid byte, which sanitizes to
// maxLogValueLen-3 bytes plus the 3-byte U+FFFD — exactly the cap. Serial: it
// captures slog.Default.
func TestSanitizeLabelValues_ValueExactlyAtLogCapLoggedWhole(t *testing.T) {
	buf := captureDebugLogs(t)
	atCap := strings.Repeat("a", maxLogValueLen-3) + "\xff"

	NewLabeledCounter("logcap_exact_total", "test", []string{"m"}).Inc(atCap) // must not panic

	attr := loggedValueAttr(t, buf.String(), "logcap_exact_total")
	want := strings.Repeat("a", maxLogValueLen-3) + "\uFFFD"
	if attr != want {
		t.Errorf("logged value attribute = %q, want the whole %d-byte value %q", attr, len(want), want)
	}
	if strings.Contains(attr, "...") {
		t.Errorf("logged value attribute = %q, want no truncation marker for a value exactly at the cap", attr)
	}
}

// TestSanitizeHelp_InvalidUTF8NoPanic pins the construction-time help-text
// UTF-8 policy: constructing any metric with invalid-UTF-8 help never panics —
// the help is sanitized with U+FFFD and a warning is logged naming the metric.
// Serial: it captures slog.Default.
func TestSanitizeHelp_InvalidUTF8NoPanic(t *testing.T) {
	badHelp := "\xff\xfe"
	cases := []struct {
		kind   string
		metric string
		fn     func()
	}{
		{"Counter", "sanhelp_counter_total", func() { NewCounter("sanhelp_counter_total", badHelp) }},
		{"LabeledCounter", "sanhelp_lcounter_total", func() { NewLabeledCounter("sanhelp_lcounter_total", badHelp, []string{"m"}) }},
		{"Gauge", "sanhelp_gauge", func() { NewGauge("sanhelp_gauge", badHelp) }},
		{"LabeledGauge", "sanhelp_lgauge", func() { NewLabeledGauge("sanhelp_lgauge", badHelp, []string{"m"}) }},
		{"Histogram", "sanhelp_hist_seconds", func() { NewHistogram("sanhelp_hist_seconds", badHelp) }},
		{"LabeledHistogram", "sanhelp_lhist_seconds", func() { NewLabeledHistogram("sanhelp_lhist_seconds", badHelp, []string{"m"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			buf := captureDebugLogs(t)
			tc.fn() // must not panic
			logs := buf.String()
			if !strings.Contains(logs, "help text contained invalid UTF-8") {
				t.Fatalf("logs = %q, want help sanitize warning", logs)
			}
			if !strings.Contains(logs, "metric="+tc.metric) {
				t.Errorf("logs = %q, want warning naming metric %s", logs, tc.metric)
			}
		})
	}
}

// TestSanitizeHelp_ExpositionCarriesReplacement verifies the sanitized help
// text reaches the exposition HELP line carrying the U+FFFD replacement, for
// both an unlabeled counter (counter.go wiring) and a labeled histogram
// (histogram.go wiring). Serial: it captures slog.Default.
func TestSanitizeHelp_ExpositionCarriesReplacement(t *testing.T) {
	_ = captureDebugLogs(t) // absorb the expected sanitize warnings

	c := NewCounter("sanhelp_expo_counter_total", "bad\xff\xfehelp")
	c.Inc()
	var cb strings.Builder
	writeCounter(&cb, c)
	if out := cb.String(); !strings.Contains(out, "# HELP sanhelp_expo_counter_total bad\uFFFDhelp") {
		t.Errorf("counter HELP = %q, want U+FFFD replacement", out)
	}

	lh := NewLabeledHistogram("sanhelp_expo_hist_seconds", "bad\xff\xfehelp", []string{"m"})
	lh.Observe(0.5, "x")
	var hb strings.Builder
	writeLabeledHistogram(&hb, lh)
	if out := hb.String(); !strings.Contains(out, "# HELP sanhelp_expo_hist_seconds bad\uFFFDhelp") {
		t.Errorf("labeled histogram HELP = %q, want U+FFFD replacement", out)
	}
}

// TestValidateLabelValues_ValidSucceeds verifies that well-formed UTF-8 label
// values (including multi-byte runes) create series without panicking and are
// emitted in the exposition.
func TestValidateLabelValues_ValidSucceeds(t *testing.T) {
	lc := NewLabeledCounter("okval_counter_total", "test", []string{"lang"})
	lc.Inc("héllo") // multi-byte UTF-8
	lc.Inc("plain")

	var b strings.Builder
	writeLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `lang="héllo"`) {
		t.Errorf("expected multi-byte UTF-8 label value in output, got: %s", out)
	}
	if !strings.Contains(out, `lang="plain"`) {
		t.Errorf("expected plain label value in output, got: %s", out)
	}
}

// TestSanitizeHelp_LongInvalidUTF8LogTruncated pins the maxLogValueLen
// truncation on the help-text warning path: constructing a metric with
// hostile multi-hundred-byte invalid help sanitizes without panicking, and
// the warning's help attribute retains a bounded prefix with the shared
// "..." truncation marker while dropping the tail -- the same bound the
// label-value warning already enforces. Serial: it captures slog.Default.
func TestSanitizeHelp_LongInvalidUTF8LogTruncated(t *testing.T) {
	buf := captureDebugLogs(t)
	longInvalid := strings.Repeat("a", maxLogValueLen) + "\xff" + strings.Repeat("b", 64)

	NewCounter("long_sanhelp_total", longInvalid) // must not panic

	logs := buf.String()
	if !strings.Contains(logs, "help text contained invalid UTF-8") {
		t.Fatalf("logs = %q, want help sanitize warning", logs)
	}
	if !strings.Contains(logs, "a...") {
		t.Errorf("logs = %q, want the \"...\" marker at the help attribute's cut joint", logs)
	}
	if strings.Contains(logs, strings.Repeat("b", 16)) {
		t.Errorf("logs = %q, want hostile help tail truncated", logs)
	}
}
