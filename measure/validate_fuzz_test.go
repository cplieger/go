package measure

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

var (
	metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRe  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// FuzzMetricNameValidation cross-checks isValidMetricName against an
// independent regexp oracle for the Prometheus metric-name grammar.
func FuzzMetricNameValidation(f *testing.F) {
	f.Add("")
	f.Add("valid_name")
	f.Add("0startsdigit")
	f.Add("has-dash")
	f.Add("colon:ok")
	f.Add("\x00null")
	f.Fuzz(func(t *testing.T, s string) {
		got := isValidMetricName(s)
		want := metricNameRe.MatchString(s)
		if got != want {
			t.Errorf("isValidMetricName(%q) = %v, want %v", s, got, want)
		}
	})
}

// FuzzLabelNameValidation cross-checks isValidLabelName against an independent
// regexp oracle for the Prometheus label-name grammar (no colon allowed).
func FuzzLabelNameValidation(f *testing.F) {
	f.Add("")
	f.Add("valid_label")
	f.Add("0digit")
	f.Add("has:colon")
	f.Add("__reserved")
	f.Fuzz(func(t *testing.T, s string) {
		got := isValidLabelName(s)
		want := labelNameRe.MatchString(s)
		if got != want {
			t.Errorf("isValidLabelName(%q) = %v, want %v", s, got, want)
		}
	})
}

// FuzzLabelValueValidation asserts the record-time label-value UTF-8 policy:
// recording never panics (for correct arity), the stored label value is always
// valid UTF-8 (invalid input is sanitized with U+FFFD), and a valid value
// round-trips into the stored series key unchanged.
func FuzzLabelValueValidation(f *testing.F) {
	f.Add("plain")
	f.Add("héllo")
	f.Add("")
	f.Add("\xff\xfe")
	f.Add("\xc3\x28")
	f.Fuzz(func(t *testing.T, s string) {
		lc := NewLabeledCounter("fuzz_val_total", "test", []string{"m"})
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unexpected panic recording value %q: %v", s, r)
				}
			}()
			lc.Inc(s)
		}()
		if got := len(lc.vals); got != 1 {
			t.Fatalf("series count = %d, want 1 for value %q", got, s)
		}
		for key := range lc.vals {
			stored := key[0]
			if !utf8.ValidString(stored) {
				t.Errorf("stored label value %q is not valid UTF-8 (input %q)", stored, s)
			}
			if utf8.ValidString(s) && stored != s {
				t.Errorf("valid input %q stored as %q, want unchanged round-trip", s, stored)
			}
		}
	})
}
