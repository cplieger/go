package envx

import (
	"cmp"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSourceAgreesWithPackageGetters pins the documented identity: the zero
// Source, an explicit Source over os.Getenv, and the package-level getters
// are the same getters. Every family is compared on every input class —
// unset, empty, whitespace-only, valid, malformed — so the delegation cannot
// silently fork policy (trim rules, empty-equals-unset, error text) between
// the package level and the injected form.
func TestSourceAgreesWithPackageGetters(t *testing.T) {
	// Distinct keys per family so one value exercises each parser's own
	// valid/malformed classes; sharing one key would make "valid int" a
	// malformed bool and drown the table in warns.
	inputs := []struct {
		name string
		set  bool
		val  string
	}{
		{name: "unset"},
		{name: "empty", set: true, val: ""},
		{name: "whitespace only", set: true, val: "   "},
		{name: "bool spelling", set: true, val: " On "},
		{name: "int", set: true, val: " 7 "},
		{name: "duration", set: true, val: "90m"},
		{name: "malformed everywhere", set: true, val: "junk"},
	}
	sources := map[string]Source{
		"zero value":         {},
		"explicit os.Getenv": {Get: os.Getenv},
	}
	const key = "ENVX_TEST_SOURCE_PARITY"
	for _, tc := range inputs {
		for srcName, src := range sources {
			t.Run(tc.name+"/"+srcName, func(t *testing.T) {
				captureWarns(t) // silence the tolerant family's malformed warns
				if tc.set {
					t.Setenv(key, tc.val)
				}
				if got, want := src.String(key), String(key); got != want {
					t.Errorf("Source.String = %q, package String = %q", got, want)
				}
				if got, want := src.Bool(key, true), Bool(key, true); got != want {
					t.Errorf("Source.Bool = %v, package Bool = %v", got, want)
				}
				if got, want := src.Int(key, 42), Int(key, 42); got != want {
					t.Errorf("Source.Int = %d, package Int = %d", got, want)
				}
				if got, want := src.Duration(key, time.Minute), Duration(key, time.Minute); got != want {
					t.Errorf("Source.Duration = %v, package Duration = %v", got, want)
				}
				assertTripleAgrees(t, "BoolStrict", tripleOf(src.BoolStrict(key)), tripleOf(BoolStrict(key)))
				assertTripleAgrees(t, "IntStrict", tripleOf(src.IntStrict(key)), tripleOf(IntStrict(key)))
				assertTripleAgrees(t, "DurationStrict", tripleOf(src.DurationStrict(key)), tripleOf(DurationStrict(key)))
			})
		}
	}
}

// triple is a strict variant's full return, normalized for comparison across
// the Source and package forms.
type triple[T comparable] struct {
	value T
	ok    bool
	err   string // "" for nil; strict errors compare by text
}

// tripleOf normalizes one strict-variant return.
func tripleOf[T comparable](value T, ok bool, err error) triple[T] {
	tr := triple[T]{value: value, ok: ok}
	if err != nil {
		tr.err = err.Error()
	}
	return tr
}

// assertTripleAgrees reports any divergence between the Source and package
// forms of one strict variant.
func assertTripleAgrees[T comparable](t *testing.T, fn string, got, want triple[T]) {
	t.Helper()
	if got != want {
		t.Errorf("Source.%s = %+v, package %s = %+v", fn, got, fn, want)
	}
}

// TestSourceReadsThroughTheInjectedGetter pins the seam Source exists for
// (the getter-parameterised main, run(os.Args, os.Getenv)): every family
// resolves through the injected function and never through the process
// environment, with the package semantics intact — empty equals unset, the
// parsers trim, the tolerant family warns through slog's default logger
// naming the key, and the strict family stays silent and value-safe.
func TestSourceReadsThroughTheInjectedGetter(t *testing.T) {
	env := map[string]string{
		"FAKE_STRING":   "value",
		"FAKE_BOOL":     " On ",
		"FAKE_INT":      " 7 ",
		"FAKE_DUR":      "90m",
		"FAKE_EMPTY":    "",
		"FAKE_BAD_INT":  " 5x ",
		"FAKE_BAD_BOOL": "ture",
	}
	src := Source{Get: func(name string) string { return env[name] }}

	// The process environment holds a conflicting value: reading it instead
	// of the fake would be a visible wrong answer, not a silent coincidence.
	t.Setenv("FAKE_STRING", "from-process-env")

	rec := captureWarns(t)
	if got := src.String("FAKE_STRING"); got != "value" {
		t.Errorf("String = %q, want the injected source's %q", got, "value")
	}
	if got := cmp.Or(src.String("FAKE_UNSET"), "fb"); got != "fb" {
		t.Errorf("cmp.Or(String(unset), \"fb\") = %q, want the fallback", got)
	}
	if got := cmp.Or(src.String("FAKE_EMPTY"), "fb"); got != "fb" {
		t.Errorf("cmp.Or(String(empty), \"fb\") = %q, want the fallback: empty equals unset through an injected source too", got)
	}
	if got := src.Bool("FAKE_BOOL", false); !got {
		t.Error("Bool = false, want the injected ' On ' parsed true (trim + tolerant spellings apply)")
	}
	if got := src.Int("FAKE_INT", 42); got != 7 {
		t.Errorf("Int = %d, want the injected 7", got)
	}
	if got := src.Duration("FAKE_DUR", time.Second); got != 90*time.Minute {
		t.Errorf("Duration = %v, want the injected 90m", got)
	}
	if len(rec.msgs) != 0 {
		t.Errorf("well-formed reads logged: %v", rec.msgs)
	}

	// Tolerant family: one Warn per malformed read, naming the key.
	if got := src.Int("FAKE_BAD_INT", 42); got != 42 {
		t.Errorf("Int(malformed) = %d, want the fallback", got)
	}
	if rec.count("malformed") != 1 {
		t.Errorf("warns = %d, want exactly 1: %v", rec.count("malformed"), rec.msgs)
	}
	if len(rec.keys) != 1 || rec.keys[0] != "FAKE_BAD_INT" {
		t.Errorf("warn key attrs = %v, want [FAKE_BAD_INT]", rec.keys)
	}

	// Strict family: silent, and *ParseError carries the TRIMMED injected
	// value — the same contract as over the process environment.
	_, ok, err := src.IntStrict("FAKE_BAD_INT")
	if ok || err == nil {
		t.Fatalf("IntStrict = (_, %v, %v), want a strict error", ok, err)
	}
	perr, ok := errors.AsType[*ParseError](err)
	if !ok {
		t.Fatalf("IntStrict error = %v, want *ParseError", err)
	}
	if perr.Key != "FAKE_BAD_INT" || perr.Value != "5x" {
		t.Errorf("ParseError = {Key: %q, Value: %q}, want {FAKE_BAD_INT, 5x}", perr.Key, perr.Value)
	}
	_, _, berr := src.BoolStrict("FAKE_BAD_BOOL")
	if berr == nil {
		t.Fatal("BoolStrict = nil error for a malformed value")
	}
	if strings.Contains(berr.Error(), "ture") {
		t.Errorf("BoolStrict error echoes the value through an injected source: %v", berr)
	}
	if rec.count("malformed") != 1 {
		t.Errorf("strict variants logged: %v", rec.msgs)
	}
}

// Source.Require must behave exactly as the package-level Require, through an
// injected getter: pg-autodump's run(args, getenv) seam requires its one
// mandatory secret through the same Source it reads everything else from.
func TestSourceRequire(t *testing.T) {
	env := map[string]string{"AUTH_TOKEN": "tok"}
	src := Source{Get: func(k string) string { return env[k] }}
	got, err := src.Require("AUTH_TOKEN")
	if err != nil || got != "tok" {
		t.Errorf(`Source.Require("AUTH_TOKEN") = (%q, %v), want ("tok", nil)`, got, err)
	}
	_, err = src.Require("ABSENT")
	miss, ok := errors.AsType[*MissingError](err)
	if !ok || miss.Key != "ABSENT" {
		t.Errorf(`Source.Require("ABSENT") error = %v, want *MissingError{Key: "ABSENT"}`, err)
	}
}
