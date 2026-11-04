package envx

import (
	"errors"
	"log/slog"
	"strings"
	"time"
)

// String returns the value of the environment variable key, empty when the
// variable is unset or set to the empty string. Those two cases are
// deliberately indistinguishable here, because compose files and CI matrices
// routinely materialize `KEY=` for a knob the operator left blank; use
// os.LookupEnv directly for the rare caller that must tell them apart.
//
// String takes no fallback, and that is the whole reason it is safe. A
// (key, fallback string) pair is two adjacent strings, so a transposed call
// read the fallback as a variable name and returned the name as the value —
// silently, forever. There is nothing to transpose against one parameter.
// Compose the default with [cmp.Or], which is what the two-parameter form did
// internally anyway:
//
//	addr := cmp.Or(envx.String("APP_LISTEN"), ":8080")
//
// The parsing getters (Bool, Int, Duration) keep their fallback: it is a
// different type from the key, so no transposition compiles, and a malformed
// value has to resolve to something. String has nothing to parse, so it needs
// no such rule.
//
// Unlike the parsing getters, String does not trim the value: whitespace can
// be meaningful in a free-form string, and the caller knows whether its value
// is a path, a token, or a list. A whitespace-only value therefore counts as
// set, and cmp.Or keeps it.
func String(key Key) string {
	return Source{}.String(key)
}

// Bool returns the boolean value of the environment variable key, or fallback
// when the variable is unset or empty.
//
// Parsing is tolerant of the spellings deployment files actually contain:
// true/1/yes/on and false/0/no/off, case-insensitive, surrounding whitespace
// ignored. Any other value logs one Warn through slog's default logger and
// returns fallback, so a typo ("ture") is visible in the logs instead of
// silently flipping a flag.
//
// The Warn line carries the raw value. Use BoolStrict for a key whose value
// may be sensitive, or when the caller owns its own diagnostics.
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
// time.ParseDuration ("30s", "6h", "1h30m"), or fallback when the variable is
// unset or empty. A set-but-unparseable value logs one Warn through slog's
// default logger and returns fallback.
//
// A bare number without a unit is deliberately not accepted: "30" is ambiguous
// between seconds and minutes across tools, and time.ParseDuration rejecting
// it (with the Warn line naming the key) is clearer than guessing.
func Duration(key Key, fallback time.Duration) time.Duration {
	return Source{}.Duration(key, fallback)
}

// parseEnv is the single trim-and-parse core shared by the tolerant parsing
// getters (Bool, Int, Duration) and their strict variants (BoolStrict,
// IntStrict, DurationStrict), on both the package level and the Source
// methods, so the layers cannot drift apart mechanically: resolve the key
// through the source, trim surrounding whitespace, treat empty as unset
// (ok=false, no error), then parse. It returns the trimmed raw value for the
// tolerant layer's Warn diagnostic. Policy stays with the callers:
// warn-and-fallback in the getters, error-as-data in the strict variants.
//
// It is a method with its own type parameter, which Go 1.27 permits. Before
// that a method could not be generic, so the receiver had to be smuggled in as
// a leading parameter — parseEnv(s, key, parse) — spelling a Source operation
// as a free function over a Source. Nothing reaches this through an interface
// (it is unexported, and the package declares no interface at all), so the rule
// that an interface method may not have type parameters never applies here.
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

// errInvalidBool is the value-free parse failure for a set-but-unrecognized
// boolean spelling. It names the accepted vocabulary but never the offending
// value: BoolStrict hands this to a caller that may log it, and the value may
// be a secret an operator wired to the key by mistake. The tolerant Bool adds
// the raw value itself, in the Warn line it owns.
var errInvalidBool = errors.New("invalid boolean (want true/1/yes/on or false/0/no/off)")

// parseBool is the single home of the package's boolean vocabulary, shared by
// the tolerant Bool and the strict BoolStrict through parseEnv so the two
// cannot drift apart. It receives the already-trimmed value (parseEnv trims
// and treats empty as unset), so it only decides the spelling: true/1/yes/on
// and false/0/no/off, case-insensitive.
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

// warnMalformed emits the single shared diagnostic for a set-but-unparseable
// variable. The raw value is included: config values are not secrets (Secret
// never routes here), and the operator fixing the deployment needs to see
// what was actually set.
func warnMalformed(key Key, value, kind string, fallback any) {
	slog.Warn("envx: malformed value, using fallback",
		"key", string(key), "value", value, "kind", kind, "fallback", fallback)
}
