package envx

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBoolStrict(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		value   string
		want    bool
		wantOK  bool
		wantErr bool
	}{
		{name: "unset", want: false, wantOK: false},
		{name: "empty", set: true, value: "", want: false, wantOK: false},
		{name: "whitespace-only", set: true, value: "   ", want: false, wantOK: false},
		{name: "tab and newline only", set: true, value: "\t\n", want: false, wantOK: false},
		// Every accepted true spelling.
		{name: "true", set: true, value: "true", want: true, wantOK: true},
		{name: "1", set: true, value: "1", want: true, wantOK: true},
		{name: "yes", set: true, value: "yes", want: true, wantOK: true},
		{name: "on", set: true, value: "on", want: true, wantOK: true},
		// Every accepted false spelling.
		{name: "false", set: true, value: "false", want: false, wantOK: true},
		{name: "0", set: true, value: "0", want: false, wantOK: true},
		{name: "no", set: true, value: "no", want: false, wantOK: true},
		{name: "off", set: true, value: "off", want: false, wantOK: true},
		// Case variations.
		{name: "TRUE", set: true, value: "TRUE", want: true, wantOK: true},
		{name: "True", set: true, value: "True", want: true, wantOK: true},
		{name: "tRuE", set: true, value: "tRuE", want: true, wantOK: true},
		{name: "YES", set: true, value: "YES", want: true, wantOK: true},
		{name: "On", set: true, value: "On", want: true, wantOK: true},
		{name: "FALSE", set: true, value: "FALSE", want: false, wantOK: true},
		{name: "No", set: true, value: "No", want: false, wantOK: true},
		{name: "OFF", set: true, value: "OFF", want: false, wantOK: true},
		// Surrounding whitespace ignored.
		{name: "padded true", set: true, value: " true ", want: true, wantOK: true},
		{name: "tab-padded yes", set: true, value: "\tyes\t", want: true, wantOK: true},
		{name: "newline-padded off", set: true, value: " off\n", want: false, wantOK: true},
		{name: "padded case-mixed", set: true, value: "  True  ", want: true, wantOK: true},
		// Malformed: the error is returned, never logged (asserted below).
		{name: "typo", set: true, value: "ture", wantErr: true},
		{name: "out-of-range numeric", set: true, value: "2", wantErr: true},
		{name: "negative numeric", set: true, value: "-1", wantErr: true},
		{name: "strconv-only spelling t", set: true, value: "t", wantErr: true},
		{name: "strconv-only spelling f", set: true, value: "f", wantErr: true},
		{name: "word", set: true, value: "enabled", wantErr: true},
		{name: "inner whitespace", set: true, value: "t rue", wantErr: true},
		{name: "non-ascii", set: true, value: "🚀", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := captureWarns(t)
			if tt.set {
				t.Setenv("ENVX_TEST_BOOLSTRICT", tt.value)
			}
			got, ok, err := BoolStrict("ENVX_TEST_BOOLSTRICT")
			if got != tt.want || ok != tt.wantOK || (err != nil) != tt.wantErr {
				t.Errorf("BoolStrict() = (%v, %v, %v), want (%v, %v, err=%v)",
					got, ok, err, tt.want, tt.wantOK, tt.wantErr)
			}
			if ok && err != nil {
				t.Errorf("ok and err are not mutually exclusive: (%v, %v)", ok, err)
			}
			if len(rec.msgs) != 0 {
				t.Errorf("strict getter logged: %v", rec.msgs)
			}
		})
	}
}

// TestBoolStrictNeverLogs is the reason BoolStrict exists: the caller owns the
// diagnostic because the value may be sensitive, so the malformed path — the
// one path where the tolerant Bool logs, and logs the raw value — must emit
// nothing at all through the default logger, at any level.
func TestBoolStrictNeverLogs(t *testing.T) {
	const secretish = "hunter2-not-a-boolean"
	for _, value := range []string{secretish, "", "   ", "true", "off"} {
		t.Run("value="+value, func(t *testing.T) {
			rec := captureWarns(t)
			t.Setenv("ENVX_TEST_BOOLSTRICT_QUIET", value)

			_, _, err := BoolStrict("ENVX_TEST_BOOLSTRICT_QUIET")

			if len(rec.msgs) != 0 {
				t.Errorf("BoolStrict logged %d record(s): %v", len(rec.msgs), rec.msgs)
			}
			if len(rec.keys) != 0 {
				t.Errorf("BoolStrict logged key attrs: %v", rec.keys)
			}
			// The malformed value must not survive into the error either: the
			// caller is expected to log that error.
			if err != nil && strings.Contains(err.Error(), value) {
				t.Errorf("BoolStrict error echoes the value: %v", err)
			}
		})
	}
}

// TestBoolStrictErrorContract pins what the caller can rely on: the error
// names the key (so the caller's own diagnostic can too) and states the
// accepted vocabulary, and the tolerant Bool would have warned about the very
// same value.
func TestBoolStrictErrorContract(t *testing.T) {
	rec := captureWarns(t)
	t.Setenv("ENVX_TEST_BOOLSTRICT_ERR", "ture")

	_, ok, err := BoolStrict("ENVX_TEST_BOOLSTRICT_ERR")
	if ok || err == nil {
		t.Fatalf("BoolStrict() = (_, %v, %v), want (_, false, error)", ok, err)
	}
	if !strings.Contains(err.Error(), "ENVX_TEST_BOOLSTRICT_ERR") {
		t.Errorf("BoolStrict error does not name the key: %v", err)
	}
	for _, spelling := range []string{"true", "1", "yes", "on", "false", "0", "no", "off"} {
		if !strings.Contains(err.Error(), spelling) {
			t.Errorf("BoolStrict error does not name accepted spelling %q: %v", spelling, err)
		}
	}
	if len(rec.msgs) != 0 {
		t.Errorf("strict getter logged: %v", rec.msgs)
	}
}

// TestBoolAndBoolStrictAgree pins the shared-parser guarantee: for every
// well-formed input the two getters decide identically, so the grammar cannot
// drift between the tolerant and strict layers. Bool is called with both
// fallbacks: agreement must come from the value, not from a fallback that
// happens to match.
func TestBoolAndBoolStrictAgree(t *testing.T) {
	wellFormed := []string{
		"true", "1", "yes", "on", "false", "0", "no", "off",
		"TRUE", "True", "tRuE", "YES", "On", "FALSE", "No", "OFF",
		" true ", "\tyes\t", " off\n", "  False  ", " 1 ", " 0 ",
	}
	for _, value := range wellFormed {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			captureWarns(t) // Bool must not warn on a well-formed value
			t.Setenv("ENVX_TEST_BOOL_AGREE", value)

			strict, ok, err := BoolStrict("ENVX_TEST_BOOL_AGREE")
			if !ok || err != nil {
				t.Fatalf("BoolStrict(%q) = (_, %v, %v), want ok", value, ok, err)
			}
			if got := Bool("ENVX_TEST_BOOL_AGREE", true); got != strict {
				t.Errorf("Bool(fallback=true) = %v, BoolStrict = %v for %q", got, strict, value)
			}
			if got := Bool("ENVX_TEST_BOOL_AGREE", false); got != strict {
				t.Errorf("Bool(fallback=false) = %v, BoolStrict = %v for %q", got, strict, value)
			}
		})
	}
}

// TestBoolAndBoolStrictAgreeOnMalformed is the other half of the shared
// parser: what one layer rejects, the other rejects too — Bool falls back
// (with its Warn), BoolStrict errors.
func TestBoolAndBoolStrictAgreeOnMalformed(t *testing.T) {
	for _, value := range []string{"ture", "2", "-1", "t", "f", "enabled", "🚀"} {
		t.Run(value, func(t *testing.T) {
			rec := captureWarns(t)
			t.Setenv("ENVX_TEST_BOOL_AGREE_BAD", value)

			if _, ok, err := BoolStrict("ENVX_TEST_BOOL_AGREE_BAD"); ok || err == nil {
				t.Fatalf("BoolStrict(%q) = (_, %v, %v), want an error", value, ok, err)
			}
			if got := Bool("ENVX_TEST_BOOL_AGREE_BAD", true); !got {
				t.Errorf("Bool did not fall back to true for %q", value)
			}
			if got := Bool("ENVX_TEST_BOOL_AGREE_BAD", false); got {
				t.Errorf("Bool did not fall back to false for %q", value)
			}
			if rec.count("malformed") != 2 {
				t.Errorf("Bool warns = %d, want 2 (one per call); BoolStrict must add none: %v",
					rec.count("malformed"), rec.msgs)
			}
		})
	}
}

func TestIntStrict(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		value   string
		want    int
		wantOK  bool
		wantErr bool
	}{
		{name: "unset", want: 0, wantOK: false},
		{name: "empty", set: true, value: "", want: 0, wantOK: false},
		{name: "whitespace-only", set: true, value: "   ", want: 0, wantOK: false},
		{name: "valid", set: true, value: "7", want: 7, wantOK: true},
		{name: "padded valid", set: true, value: " 9 ", want: 9, wantOK: true},
		{name: "negative", set: true, value: "-3", want: -3, wantOK: true},
		{name: "zero", set: true, value: "0", want: 0, wantOK: true},
		{name: "malformed", set: true, value: "seven", wantErr: true},
		{name: "float rejected", set: true, value: "1.5", wantErr: true},
		{name: "overflow rejected", set: true, value: "9999999999999999999999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := captureWarns(t)
			if tt.set {
				t.Setenv("ENVX_TEST_INTSTRICT", tt.value)
			}
			got, ok, err := IntStrict("ENVX_TEST_INTSTRICT")
			if got != tt.want || ok != tt.wantOK || (err != nil) != tt.wantErr {
				t.Errorf("IntStrict() = (%d, %v, %v), want (%d, %v, err=%v)",
					got, ok, err, tt.want, tt.wantOK, tt.wantErr)
			}
			if ok && err != nil {
				t.Errorf("ok and err are not mutually exclusive: (%v, %v)", ok, err)
			}
			if len(rec.msgs) != 0 {
				t.Errorf("strict getter logged: %v", rec.msgs)
			}
		})
	}
}

func TestDurationStrict(t *testing.T) {
	tests := []struct {
		name    string
		set     bool
		value   string
		want    time.Duration
		wantOK  bool
		wantErr bool
	}{
		{name: "unset", want: 0, wantOK: false},
		{name: "empty", set: true, value: "", want: 0, wantOK: false},
		{name: "whitespace-only", set: true, value: "   ", want: 0, wantOK: false},
		{name: "seconds", set: true, value: "30s", want: 30 * time.Second, wantOK: true},
		{name: "compound", set: true, value: "1h30m", want: 90 * time.Minute, wantOK: true},
		{name: "padded", set: true, value: " 6h ", want: 6 * time.Hour, wantOK: true},
		{name: "zero", set: true, value: "0s", want: 0, wantOK: true},
		{name: "negative", set: true, value: "-1h", want: -time.Hour, wantOK: true},
		{name: "bare number rejected", set: true, value: "30", wantErr: true},
		{name: "junk rejected", set: true, value: "soon", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := captureWarns(t)
			if tt.set {
				t.Setenv("ENVX_TEST_DURSTRICT", tt.value)
			}
			got, ok, err := DurationStrict("ENVX_TEST_DURSTRICT")
			if got != tt.want || ok != tt.wantOK || (err != nil) != tt.wantErr {
				t.Errorf("DurationStrict() = (%v, %v, %v), want (%v, %v, err=%v)",
					got, ok, err, tt.want, tt.wantOK, tt.wantErr)
			}
			if ok && err != nil {
				t.Errorf("ok and err are not mutually exclusive: (%v, %v)", ok, err)
			}
			if len(rec.msgs) != 0 {
				t.Errorf("strict getter logged: %v", rec.msgs)
			}
		})
	}
}

// TestStrictErrorContract pins what a caller can rely on in the error: it
// names the offending variable (operators grep logs for the key) and wraps
// the underlying parse error for errors.As.
func TestStrictErrorContract(t *testing.T) {
	t.Setenv("ENVX_TEST_STRICT_ERR", "junk")

	_, _, err := IntStrict("ENVX_TEST_STRICT_ERR")
	if err == nil || !strings.Contains(err.Error(), "ENVX_TEST_STRICT_ERR") {
		t.Errorf("IntStrict error does not name the key: %v", err)
	}
	if _, ok := errors.AsType[*strconv.NumError](err); !ok {
		t.Errorf("IntStrict error does not wrap *strconv.NumError: %v", err)
	}

	_, _, err = DurationStrict("ENVX_TEST_STRICT_ERR")
	if err == nil || !strings.Contains(err.Error(), "ENVX_TEST_STRICT_ERR") {
		t.Errorf("DurationStrict error does not name the key: %v", err)
	}
}

// TestParseErrorCarriesTheTrimmedValue pins the contract that removed the
// consumers' second environment read: the error carries the value the parser
// actually saw, TRIMMED. os.Getenv would return " 5x " where the parse failed
// on "5x", so a caller quoting the raw variable could name a string the parser
// never parsed. The trimming is the load-bearing half — an untrimmed Value
// would reintroduce exactly the mismatch this replaced.
func TestParseErrorCarriesTheTrimmedValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   string
		want  string
		parse func(Key) error
	}{
		{"int padded", " 5x ", "5x", func(k Key) error { _, _, err := IntStrict(k); return err }},
		{"int plain", "seven", "seven", func(k Key) error { _, _, err := IntStrict(k); return err }},
		{"duration padded", "\t9zz\n", "9zz", func(k Key) error { _, _, err := DurationStrict(k); return err }},
		{"duration unitless", "30", "30", func(k Key) error { _, _, err := DurationStrict(k); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const key = "ENVX_TEST_PARSE_ERR"
			t.Setenv(key, tc.set)

			err := tc.parse(key)
			perr, ok := errors.AsType[*ParseError](err)
			if !ok {
				t.Fatalf("error is not a *ParseError: %v", err)
			}
			if perr.Key != key {
				t.Errorf("Key = %q, want %q", perr.Key, key)
			}
			if perr.Value != tc.want {
				t.Errorf("Value = %q, want %q (the trimmed value the parser saw)", perr.Value, tc.want)
			}
			if perr.Err == nil {
				t.Error("Err is nil; the underlying parse error must survive for errors.As")
			}
			// The message is what every strict variant has always returned;
			// only the type is new, so no consumer's assertion may shift.
			if want := "environment variable " + key + ": " + perr.Err.Error(); err.Error() != want {
				t.Errorf("Error() = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestBoolStrictReturnsNoParseError pins the deliberate asymmetry. BoolStrict
// exists for a key whose value must never be echoed, so it must NOT hand that
// value back in a typed error every caller can log. If someone "unifies" the
// three variants onto ParseError, this fails.
func TestBoolStrictReturnsNoParseError(t *testing.T) {
	const key = "ENVX_TEST_BOOLSTRICT_NOVALUE"
	t.Setenv(key, "s3cret-ish")

	_, _, err := BoolStrict(key)
	if err == nil {
		t.Fatal("BoolStrict accepted a malformed value")
	}
	if perr, ok := errors.AsType[*ParseError](err); ok {
		t.Errorf("BoolStrict returned a *ParseError carrying Value %q; it must never echo the value", perr.Value)
	}
	if strings.Contains(err.Error(), "s3cret-ish") {
		t.Errorf("BoolStrict error repeats the value: %v", err)
	}
}
