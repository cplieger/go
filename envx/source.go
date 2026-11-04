package envx

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Source reads environment variables through an injected getter, so an app
// that threads its environment as a function value — the testable-main shape,
// run(os.Args, os.Getenv) — can use the typed getters without giving that
// seam up. Construct one from whatever getenv the seam carries:
//
//	func run(args []string, getenv func(string) string) int {
//		env := envx.Source{Get: getenv}
//		timeout := env.Duration("DUMP_TIMEOUT", 5*time.Minute)
//		...
//	}
//
// The zero Source reads the process environment: a nil Get means os.Getenv,
// and the package-level getters are exactly the zero Source's methods, so the
// two forms cannot drift apart. Every other rule is shared too — keys are
// validated Keys, empty equals unset, the parsing methods trim, tolerant and
// strict variants share one parser, the tolerant methods Warn through
// slog.Default() on a malformed value, and the strict methods never log.
type Source struct {
	// Get returns the value of the named environment variable, empty when it
	// is unset (os.Getenv's contract, which os.Getenv itself satisfies). nil
	// means os.Getenv.
	Get func(string) string
}

// getenv resolves key through the source's getter, defaulting to os.Getenv.
// It is the single read path for every Source method, so key validation
// cannot be skipped by any of them.
func (s Source) getenv(key Key) string {
	key.validate()
	if s.Get == nil {
		return os.Getenv(string(key))
	}
	return s.Get(string(key))
}

// String returns the value of the environment variable key, empty when the
// variable is unset or empty, with the package-level String's exact
// semantics: no trimming, whitespace-only counts as set, and the default is
// the caller's to compose with cmp.Or.
func (s Source) String(key Key) string {
	return s.getenv(key)
}

// Require returns the value of the environment variable key, or a
// *MissingError when it is unset or empty, with the package-level Require's
// exact semantics. It is on Source so a caller that injects its environment
// for tests (the run(args, getenv) seam) can require its one mandatory
// secret through the same seam it reads everything else through.
func (s Source) Require(key Key) (string, error) {
	v := s.getenv(key)
	if v == "" {
		return "", &MissingError{Key: key}
	}
	return v, nil
}

// Bool returns the boolean value of the environment variable key, or fallback
// when the variable is unset or empty, with the package-level Bool's exact
// semantics: tolerant spellings, one Warn through slog's default logger on a
// malformed value.
func (s Source) Bool(key Key, fallback bool) bool {
	b, raw, ok, err := s.parseEnv(key, parseBool)
	if err != nil {
		warnMalformed(key, raw, "boolean", fallback)
		return fallback
	}
	if !ok {
		return fallback
	}
	return b
}

// Int returns the integer value of the environment variable key, or fallback
// when the variable is unset or empty, with the package-level Int's exact
// semantics: one Warn through slog's default logger on a malformed value.
func (s Source) Int(key Key, fallback int) int {
	n, raw, ok, err := s.parseEnv(key, strconv.Atoi)
	if err != nil {
		warnMalformed(key, raw, "integer", fallback)
		return fallback
	}
	if !ok {
		return fallback
	}
	return n
}

// Duration returns the value of the environment variable key parsed with
// time.ParseDuration, or fallback when the variable is unset or empty, with
// the package-level Duration's exact semantics: a bare unitless number is
// rejected, and a malformed value logs one Warn through slog's default
// logger.
func (s Source) Duration(key Key, fallback time.Duration) time.Duration {
	d, raw, ok, err := s.parseEnv(key, time.ParseDuration)
	if err != nil {
		warnMalformed(key, raw, "duration", fallback)
		return fallback
	}
	if !ok {
		return fallback
	}
	return d
}

// BoolStrict returns the boolean value of the environment variable key with
// the parse result owned by the caller, with the package-level BoolStrict's
// exact semantics: never logs, and the error names the key and the accepted
// spellings but never the value.
//
// (value and ok share their bool type in the signature at the linter's
// insistence; they mean what the family's other strict variants mean.)
func (s Source) BoolStrict(key Key) (value, ok bool, err error) {
	b, _, ok, err := s.parseEnv(key, parseBool)
	if err != nil {
		return false, false, fmt.Errorf("environment variable %s: %w", key, err)
	}
	return b, ok, nil
}

// IntStrict returns the integer value of the environment variable key with
// the parse result owned by the caller, with the package-level IntStrict's
// exact semantics: never logs, and a malformed value returns a *ParseError
// carrying the key and the trimmed value.
func (s Source) IntStrict(key Key) (value int, ok bool, err error) {
	n, raw, ok, err := s.parseEnv(key, strconv.Atoi)
	if err != nil {
		return 0, false, &ParseError{Key: key, Value: raw, Err: err}
	}
	return n, ok, nil
}

// DurationStrict returns the value of the environment variable key parsed
// with time.ParseDuration, with the parse result owned by the caller and the
// package-level DurationStrict's exact semantics: never logs, and a malformed
// value returns a *ParseError carrying the key and the trimmed value.
func (s Source) DurationStrict(key Key) (value time.Duration, ok bool, err error) {
	d, raw, ok, err := s.parseEnv(key, time.ParseDuration)
	if err != nil {
		return 0, false, &ParseError{Key: key, Value: raw, Err: err}
	}
	return d, ok, nil
}
