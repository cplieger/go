package envx

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Source reads environment variables through an injected getter, so a
// run(os.Args, os.Getenv) test seam keeps the typed getters:
//
//	env := envx.Source{Get: getenv}
//
// The zero Source reads the process environment, and the package-level getters,
// strict getters and Require are its methods, so both forms share every rule.
// The secret calls are package-level only, because they also read files.
type Source struct {
	// Get returns the named variable's value, empty when unset, as os.Getenv
	// does. nil means os.Getenv.
	Get func(string) string
}

// getenv is every Source method's one read path, so none skips key validation.
func (s Source) getenv(key Key) string {
	key.validate()
	if s.Get == nil {
		return os.Getenv(string(key))
	}
	return s.Get(string(key))
}

// String is the package-level [String] over s.
func (s Source) String(key Key) string {
	return s.getenv(key)
}

// Require is the package-level [Require] over s.
func (s Source) Require(key Key) (string, error) {
	v := s.getenv(key)
	if v == "" {
		return "", &MissingError{Key: key}
	}
	return v, nil
}

// Bool is the package-level [Bool] over s.
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

// Int is the package-level [Int] over s.
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

// Duration is the package-level [Duration] over s.
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

// BoolStrict is the package-level [BoolStrict] over s.
func (s Source) BoolStrict(key Key) (value, ok bool, err error) {
	b, _, ok, err := s.parseEnv(key, parseBool)
	if err != nil {
		return false, false, fmt.Errorf("environment variable %s: %w", key, err)
	}
	return b, ok, nil
}

// IntStrict is the package-level [IntStrict] over s.
func (s Source) IntStrict(key Key) (value int, ok bool, err error) {
	n, raw, ok, err := s.parseEnv(key, strconv.Atoi)
	if err != nil {
		return 0, false, &ParseError{Key: key, Value: raw, Err: err}
	}
	return n, ok, nil
}

// DurationStrict is the package-level [DurationStrict] over s.
func (s Source) DurationStrict(key Key) (value time.Duration, ok bool, err error) {
	d, raw, ok, err := s.parseEnv(key, time.ParseDuration)
	if err != nil {
		return 0, false, &ParseError{Key: key, Value: raw, Err: err}
	}
	return d, ok, nil
}
