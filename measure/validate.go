package measure

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"
)

// labelEscaper escapes only the three characters mandated by the Prometheus text format
// for label values: backslash, double-quote, and newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// maxLogValueLen caps how much of a caller-forwarded (potentially
// attacker-influenced) value is embedded in a sanitization warning's log
// attributes: the prefix identifies the offending value while bounding the
// size of the log record a hostile multi-megabyte value would otherwise
// produce.
const maxLogValueLen = 256

// truncateForLog bounds an attacker-influenceable value for embedding in a log
// attribute, appending the shared "..." truncation marker OUTSIDE the cap
// (output is at most maxLogValueLen+3 bytes) when the value was cut; a
// within-cap value is returned untouched. The rune-boundary backoff matches
// textsafe.CapBytes byte for byte; compare against it when changing this. Kept
// local and NARROWER than textsafe's SanitizeSingleLineBounded: measure has no
// dependencies, every value logged here already passed sanitizeUTF8, and slog's
// handlers escape control bytes in quoted text output.
func truncateForLog(s string) string {
	if len(s) <= maxLogValueLen {
		return s
	}
	// Back off to the previous rune boundary so the truncated prefix of a
	// just-sanitized (valid UTF-8) value cannot itself carry a split rune
	// (textsafe.CapBytes semantics).
	cut := maxLogValueLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// sanitizeUTF8 is the shared UTF-8 sanitization engine for record-time label
// values and construction-time help text. It returns s unchanged when s is
// valid UTF-8; otherwise it returns s with every run of invalid bytes replaced
// by the Unicode replacement character U+FFFD and reports the change. Both
// exposition formats require valid UTF-8, but the library never panics on
// invalid UTF-8: degraded input is repaired and logged instead (unlike the
// label-arity guard, which stays a fail-fast panic, and the name/label/bucket
// checks, whose errors are captured into the metric and surface at
// registration).
func sanitizeUTF8(s string) (string, bool) {
	if utf8.ValidString(s) {
		return s, false
	}
	return strings.ToValidUTF8(s, "\uFFFD"), true
}

// sanitizeHelp returns help sanitized to valid UTF-8 via sanitizeUTF8. Invalid
// help would make a strict parser reject the entire scrape, so it is repaired
// with U+FFFD and a warning names the metric. Constructors hold no locks, so
// warning directly here is safe.
func sanitizeHelp(name, help string) string {
	san, changed := sanitizeUTF8(help)
	if changed {
		slog.Warn("measure: help text contained invalid UTF-8; sanitized with U+FFFD",
			"metric", name, "help", truncateForLog(san))
	}
	return san
}

// isValidMetricName checks if a metric name matches [a-zA-Z_:][a-zA-Z0-9_:]*.
func isValidMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, b := range name {
		if isLetter(b) || b == '_' || b == ':' {
			continue
		}
		if i > 0 && isDigit(b) {
			continue
		}
		return false
	}
	return true
}

// isValidLabelName checks if a label name matches [a-zA-Z_][a-zA-Z0-9_]*.
func isValidLabelName(name string) bool {
	if name == "" {
		return false
	}
	for i, b := range name {
		if isLetter(b) || b == '_' {
			continue
		}
		if i > 0 && isDigit(b) {
			continue
		}
		return false
	}
	return true
}

func isLetter(b rune) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b rune) bool  { return b >= '0' && b <= '9' }

// checkMetricName reports an invalid metric name as an error. Constructors
// capture it into the metric value (client_golang's Desc.err shape) rather
// than panicking; registration is where it surfaces.
func checkMetricName(name string) error {
	if !isValidMetricName(name) {
		return fmt.Errorf("measure: invalid metric name %q", name)
	}
	return nil
}

// checkLabelNames returns an owned copy of labels and the first violation as
// an error: an invalid label name, a reserved "__"-prefixed name, or a
// duplicate. metric names the owning metric in each error, so a registration
// failure out of a multi-metric MustRegister block identifies which
// declaration carried the bad label. The clone is returned even on error, so
// later mutation of the caller's original slice cannot alter the metric or
// bypass set-level invariants (uniqueness, the reserved-name and arity
// guards).
func checkLabelNames(metric string, labels []string) ([]string, error) {
	owned := slices.Clone(labels)
	seen := make(map[string]struct{}, len(owned))
	for _, l := range owned {
		if !isValidLabelName(l) {
			return owned, fmt.Errorf("measure: invalid label name %q for metric %q", l, metric)
		}
		if strings.HasPrefix(l, "__") {
			return owned, fmt.Errorf(`measure: label name %q for metric %q uses the reserved "__" prefix`, l, metric)
		}
		if _, ok := seen[l]; ok {
			return owned, fmt.Errorf("measure: duplicate label name %q for metric %q", l, metric)
		}
		seen[l] = struct{}{}
	}
	return owned, nil
}

// checkNameAndLabels runs the construction-time validation shared by the
// three labeled metric types: metric name, label names, and the maxLabels
// cap. kind names the concrete type in the label-cap error. The first
// violation wins; the owned label clone is returned either way.
func checkNameAndLabels(kind, name string, labels []string) ([]string, error) {
	err := checkMetricName(name)
	owned, lerr := checkLabelNames(name, labels)
	if err == nil {
		err = lerr
	}
	if err == nil && len(owned) > maxLabels {
		// The cap comes from the constant, not a literal: labelKey is a
		// [maxLabels]string array, so raising the cap edits one const and this
		// message follows it.
		err = fmt.Errorf("measure: %s %q supports at most %d labels", kind, name, maxLabels)
	}
	return owned, err
}
