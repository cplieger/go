// Package slogx is a standard structured-logging setup for log/slog.
//
// It is a thin helper around log/slog, not a logging framework and not a
// replacement handler:
//
//   - Setup installs slog's default logger the standard way: a leveled text
//     (logfmt) or JSON handler with UTC-normalized timestamps, returning the
//     *slog.LevelVar so the level can be set after config is read or flipped at
//     runtime. NewHandler is the same without the SetDefault, for composition.
//   - ParseLevel maps a LOG_LEVEL string to an slog.Level, adding the long-form
//     "warning" alias slog lacks and reporting whether the value was recognized.
//   - ParseFormat is its format sibling, mapping a log-format string ("text" or
//     "json") to a Format under the same trim/lower/empty/ok contract.
//   - UTCTime is the exported ReplaceAttr that renders timestamps in UTC, for
//     consumers that build their own slog.HandlerOptions.
//
// The capture subpackage is the matching test-support recorder: a slog.Handler
// that records emitted log records so tests can assert on them.
//
// It does not own log-level environment-variable names, secret redaction,
// audit-event schemas or per-app attribute conventions, which stay in the
// consuming app. It carries no dependencies beyond the standard library.
package slogx
