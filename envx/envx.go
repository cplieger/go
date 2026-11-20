package envx

import (
	"errors"
	"log/slog"
	"strings"
	"time"
)

// String returns the value of the environment variable key, empty when it is
// unset or empty; use os.LookupEnv to tell those apart. It does not trim, so a
// whitespace-only value counts as set.
//
// String takes no fallback because a (key, fallback) pair of strings can be
// transposed silently. Compose the default with [cmp.Or]:
//
//	addr := cmp.Or(envx.String("APP_LISTEN"), ":8080")
func String(key Key) string {
	return Source{}.String(key)
}

// Bool returns the boolean value of the environment variable key, or fallback
// when it is unset or empty. It accepts true/1/yes/on and false/0/no/off,
// case-insensitive, with surrounding whitespace ignored. Any other value logs
// one Warn through slog's default logger, carrying the trimmed value, and
// returns fallback. Use BoolStrict for a key whose value may be sensitive.
func Bool(key Key, fallback bool) bool {
	return Source{}.Bool(key, fallback)
}

// Int returns the integer value of the environment variable key, or fallback
// when the variable is unset or empty. A set-but-unparseable value logs one
// Warn through slog's default logger and returns fallback.
func Int(key Key, fallback int) int {
	return Source{}.Int(key, fallback)
}

// Duration returns the value of the environment variable key parsed with
// time.ParseDuration ("30s", "6h", "1h30m"), or fallback when it is unset or
// empty. A malformed value, including a bare number with no unit, logs one Warn
// through slog's default logger and returns fallback.
func Duration(key Key, fallback time.Duration) time.Duration {
	return Source{}.Duration(key, fallback)
}

// parseEnv is the one trim-and-parse core of every parsing getter, tolerant and
// strict, package-level and on Source, so their rules cannot drift. Empty after
// trimming is unset (ok false, no error). raw is the trimmed value, for the
// tolerant getters' Warn.
func (s Source) parseEnv[T any](key Key, parse func(string) (T, error)) (value T, raw string, ok bool, err error) {
	raw = strings.TrimSpace(s.getenv(key))
	if raw == "" {
		return value, "", false, nil
	}
	v, err := parse(raw)
	if err != nil {
		return value, raw, false, err
	}
	return v, raw, true, nil
}

// errInvalidBool names the accepted spellings but never the value, because
// BoolStrict hands it to callers that may log it.
var errInvalidBool = errors.New("invalid boolean (want true/1/yes/on or false/0/no/off)")

// parseBool decides the boolean spelling of an already-trimmed value.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, errInvalidBool
	}
}

// warnMalformed logs a malformed value with the value itself, which the
// operator needs to fix it. Secret never routes here.
func warnMalformed(key Key, value, kind string, fallback any) {
	slog.Warn("envx: malformed value, using fallback",
		"key", string(key), "value", value, "kind", kind, "fallback", fallback)
}
