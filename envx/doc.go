// Package envx reads typed configuration from environment variables.
//
// Every parsing getter takes a Key and a fallback and never fails on the
// environment's content: an unset or empty variable returns the fallback, and
// a malformed value returns it with one Warn through slog's default logger.
// A Key that is not a valid variable name panics on first use (see [Key]).
// [String] takes no fallback; compose one with cmp.Or.
//
// Require errors on a missing mandatory variable; Secret also reads a
// Docker-style KEY_FILE secret file, with each failure class a sentinel.
// BoolStrict, IntStrict and DurationStrict return (value, ok, error) and
// never log. A Source carries the same getters over an injected environment.
//
// envx holds no state and starts no goroutines.
package envx
