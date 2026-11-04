package slogx

import "log/slog"

// UTCTime is a slog ReplaceAttr that renders a record's built-in time key in
// UTC, so log-line timestamps are zone-stable regardless of the container's TZ.
// It rewrites only the top-level time attribute; a user attribute that happens
// to share the "time" key inside a group is left untouched. Setup and NewHandler
// apply it automatically; it is exported for consumers that build their own
// slog.HandlerOptions.
func UTCTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}
