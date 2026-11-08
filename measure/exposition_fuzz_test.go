package measure

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzPrometheusHelpExposition asserts the Prometheus HELP line for an arbitrary
// help string is well-formed: no raw newline survives inside the line and every
// backslash is part of a valid \\ or \n escape (carriage return is left raw, as
// it is not a defined Prometheus escape).
func FuzzPrometheusHelpExposition(f *testing.F) {
	f.Add("simple help")
	f.Add("line\nbreak")
	f.Add(`back\slash`)
	f.Add("quote\"here")
	f.Add("\x00\xff\n\\\n")
	f.Add("cr\rreturn")
	f.Fuzz(func(t *testing.T, help string) {
		c := &Counter{name: "test_counter", help: help}
		var b strings.Builder
		writeCounter(&b, c)
		out := b.String()
		for line := range strings.SplitSeq(out, "\n") {
			if !strings.HasPrefix(line, "# HELP ") {
				continue
			}
			helpContent := strings.TrimPrefix(line, "# HELP test_counter ")
			if strings.ContainsRune(helpContent, '\n') {
				t.Fatal("raw newline in HELP line")
			}
			for i := 0; i < len(helpContent); i++ {
				if helpContent[i] == '\\' {
					if i+1 >= len(helpContent) {
						t.Fatal("trailing backslash in HELP line")
					}
					next := helpContent[i+1]
					if next != '\\' && next != 'n' {
						t.Fatalf("invalid escape \\%c in HELP line", next)
					}
					i++ // skip next
				}
			}
		}
	})
}

// FuzzRegistryFullExposition asserts a full Prometheus scrape over arbitrary
// label values is structurally sound: every non-empty line is a comment or a
// known sample series, and every TYPE line carries a valid metric type.
func FuzzRegistryFullExposition(f *testing.F) {
	f.Add("val1", "val2")
	f.Add("quote\"x", "nl\ny")
	f.Add("back\\s", "\x00")
	f.Fuzz(func(t *testing.T, lv1, lv2 string) {
		// Invalid-UTF-8 label values are rewritten to U+FFFD at record time
		// (sanitizeUTF8 via sanitizeLabelKey), so they store as valid-UTF-8
		// series the fuzzer already reaches directly; skipping them loses no
		// structural coverage and keeps the input the value actually exposed.
		if !utf8.ValidString(lv1) || !utf8.ValidString(lv2) {
			t.Skip()
		}
		reg := NewRegistry("")
		c := NewCounter("fuzz_counter", "counter help")
		c.Inc()
		reg.MustRegister(c)
		g := NewGauge("fuzz_gauge", "gauge help")
		g.Set(42)
		reg.MustRegister(g)
		h := NewHistogram("fuzz_histogram", "hist help")
		h.Observe(0.1)
		reg.MustRegister(h)
		lc := NewLabeledCounter("fuzz_labeled", "labeled help", []string{"a", "b"})
		lc.Inc(lv1, lv2)
		reg.MustRegister(lc)

		rec := httptest.NewRecorder()
		reg.Handler()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		out := rec.Body.String()
		for line := range strings.SplitSeq(out, "\n") {
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "fuzz_") &&
				!strings.HasPrefix(line, "process_") && !strings.HasPrefix(line, "go_") {
				t.Fatalf("unexpected line: %q", line)
			}
			if strings.HasPrefix(line, "# TYPE ") {
				parts := strings.Fields(line)
				if len(parts) < 4 {
					t.Fatalf("malformed TYPE line: %q", line)
				}
				typ := parts[len(parts)-1]
				switch typ {
				case "counter", "gauge", "histogram":
				case "summary":
					if parts[2] != pmGCDuration {
						t.Fatalf("summary type on %q, which only %s carries", parts[2], pmGCDuration)
					}
				default:
					t.Fatalf("unknown type %q in line: %q", typ, line)
				}
			}
		}
	})
}

// FuzzLabeledExposition_balanced asserts that for any label value, the labeled
// counter and gauge exposition keep braces and quotes balanced — i.e. the
// label-value escaping can never break the line structure.
func FuzzLabeledExposition_balanced(f *testing.F) {
	f.Add("simple", 1.0)
	f.Add("with\"quote", 0.0)
	f.Add("with\\backslash", -1.0)
	f.Add("with\nnewline", math.Inf(1))
	f.Add("null\x00byte", math.NaN())
	f.Add("", 42.0)
	f.Add(strings.Repeat("x", 500), 99.9)
	f.Add("emoji🎉", 7.0)
	// Braces are NOT escaped by the Prometheus text format (only backslash,
	// double-quote and newline are), so a brace inside a label value appears
	// raw within the brace group. These two seeds are the only inputs that
	// exercise the balance checker's "first { to last }" span; without them a
	// checker that cut around the FIRST '}' passed the whole suite.
	f.Add("brace}close", 1.0)
	f.Add("{both}", 2.0)

	f.Fuzz(func(t *testing.T, val string, gaugeVal float64) {
		// Invalid-UTF-8 label values are rewritten to U+FFFD at record time
		// (sanitizeUTF8 via sanitizeLabelKey); the sanitized result is a
		// valid-UTF-8 value the fuzzer already reaches directly, so skipping
		// keeps the escaping under test aligned with the value as stored.
		if !utf8.ValidString(val) {
			t.Skip()
		}
		lc := NewLabeledCounter("fuzz_counter", "fuzz help", []string{"v"})
		lc.Inc(val)
		lg := NewLabeledGauge("fuzz_gauge", "fuzz help", []string{"v"})
		lg.Set(gaugeVal, val)

		var b strings.Builder
		writeLabeledCounter(&b, lc)
		assertExpositionLabelsBalanced(t, b.String())

		b.Reset()
		writeLabeledGauge(&b, lg)
		assertExpositionLabelsBalanced(t, b.String())
	})
}
