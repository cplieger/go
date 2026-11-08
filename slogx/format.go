package slogx

import "strings"

// ParseFormat maps a log-format string to a Format. It is case-insensitive,
// trims surrounding space, and accepts "text" and "json" — the same contract
// ParseLevel gives a log-level string.
//
// An empty string returns def with ok=true — an unset format is not an error.
// A non-empty unrecognized value returns def with ok=false, so the caller can
// warn (a config may hold an expanded secret in the wrong field, so warn
// field-name-only when that is a possibility). Parse the format BEFORE
// installing the handler (via Setup), then warn on ok==false afterward, so the
// warning is emitted through the configured handler.
func ParseFormat(raw string, def Format) (format Format, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return def, true
	case "text":
		return Text, true
	case "json":
		return JSON, true
	default:
		return def, false
	}
}
