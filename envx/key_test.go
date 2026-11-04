package envx

import (
	"strconv"
	"testing"
	"time"
)

// wantKeyPanic is the exact message validate emits for a malformed key; the
// tests build it the same way so a wording change is a deliberate edit here,
// not an accident there.
func wantKeyPanic(k Key) string {
	return "envx: " + strconv.Quote(string(k)) + " is not an environment variable name"
}

// mustPanicWith asserts fn panics with exactly the wanted message. The exact
// match matters: the message IS the diagnostic contract — it must name the
// offending string.
func mustPanicWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Error("no panic; a malformed key must panic on first use")
			return
		}
		got, ok := r.(string)
		if !ok {
			t.Errorf("panic value = %v (%T), want a string message", r, r)
			return
		}
		if got != want {
			t.Errorf("panic message = %q, want %q", got, want)
		}
	}()
	fn()
}

// TestMalformedKeyPanicMessage pins the diagnostic for a key that is not a
// variable name — a typo, or a dynamically built name that came out wrong.
// Without the panic such a key reads as permanently unset and the caller gets
// its default forever, which is the silent failure the validation exists to
// convert into a deterministic one.
func TestMalformedKeyPanicMessage(t *testing.T) {
	const want = `envx: "127.0.0.1:7681" is not an environment variable name`
	mustPanicWith(t, want, func() { String("127.0.0.1:7681") })
}

// TestMalformedKeyPanicEchoIsBounded pins the two rules the echo budget is made
// of, at the byte where they meet. A key whose quoted form fits the budget is
// named in FULL — that is the exact-naming contract a caller reads to find the
// offending literal, and it must still hold for a key that fills the budget
// exactly. A longer key is named by the longest quoted prefix that still fits
// once the ellipsis is inside the closing quote, so a dynamically built name
// that came out as a whole connection string or a mount path names the mistake
// without carrying the entire value into a log store.
//
// The two keys are sized against the budget deliberately: the first quotes to
// exactly keyEchoMax bytes, the second to more, and both echoes are therefore
// keyEchoMax bytes wide by different rules.
func TestMalformedKeyPanicEchoIsBounded(t *testing.T) {
	for name, tc := range map[string]struct {
		key  Key
		want string
	}{
		"quoted_form_exactly_fills_the_budget": {
			key:  "/run/secrets/production/postgres/appdb/password/current/tokens",
			want: `envx: "/run/secrets/production/postgres/appdb/password/current/tokens" is not an environment variable name`,
		},
		"quoted_form_over_the_budget": {
			key:  "postgres://user:password@db.internal.example.com:5432/appdb?sslmode=require",
			want: `envx: "postgres://user:password@db.internal.example.com:5432/appdb..." is not an environment variable name`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			mustPanicWith(t, tc.want, func() { String(tc.key) })
		})
	}
}

// TestStringTakesNoFallback is a compile-time pin on the signature that closes
// the key/fallback transposition class. A (key, fallback string) String was
// two adjacent strings, so a swapped call read the fallback as a variable name
// and returned the name as the value, silently and forever; the fix was to
// delete the parameter rather than to detect the swap, since cmp.Or composes
// the default and the two-parameter body was cmp.Or all along. Restoring a
// second string parameter breaks these lines.
//
// The assertion is the PARAMETER type: passing each getter to a func(Key)
// string parameter admits exactly that signature and nothing else. Spelling it
// as `var _ func(Key) string = String` reads the same to a human but is a
// declaration whose type an inference pass can drop, at which point it asserts
// nothing at all — the pin has to be a position that cannot be inferred away.
func TestStringTakesNoFallback(t *testing.T) {
	pin := func(func(Key) string) {}
	pin(String)
	pin(Source{}.String)
}

// TestMalformedKeyPanicsInEveryGetter sweeps the panic across the whole
// getter surface — the fallback-taking family, the strict family, the
// require/secret family, and every Source method — for the two swap shapes
// every getter can receive (a non-name value and the empty string). No getter
// may read anything, log anything, or fall back on a malformed key.
func TestMalformedKeyPanicsInEveryGetter(t *testing.T) {
	getters := map[string]func(Key){
		"String":                func(k Key) { String(k) },
		"Bool":                  func(k Key) { Bool(k, false) },
		"Int":                   func(k Key) { Int(k, 0) },
		"Duration":              func(k Key) { Duration(k, time.Second) },
		"BoolStrict":            func(k Key) { _, _, _ = BoolStrict(k) },
		"IntStrict":             func(k Key) { _, _, _ = IntStrict(k) },
		"DurationStrict":        func(k Key) { _, _, _ = DurationStrict(k) },
		"Require":               func(k Key) { _, _ = Require(k) },
		"Secret":                func(k Key) { _, _ = Secret(k) },
		"SecretWithSource":      func(k Key) { _, _, _ = SecretWithSource(k) },
		"IsBlankSecretFilePath": func(k Key) { _ = IsBlankSecretFilePath(k) },
		"Source.String":         func(k Key) { Source{}.String(k) },
		"Source.Bool":           func(k Key) { Source{}.Bool(k, false) },
		"Source.Int":            func(k Key) { Source{}.Int(k, 0) },
		"Source.Duration":       func(k Key) { Source{}.Duration(k, time.Second) },
		"Source.BoolStrict":     func(k Key) { _, _, _ = Source{}.BoolStrict(k) },
		"Source.IntStrict":      func(k Key) { _, _, _ = Source{}.IntStrict(k) },
		"Source.DurationStrict": func(k Key) { _, _, _ = Source{}.DurationStrict(k) },
	}
	for name, get := range getters {
		for _, k := range []Key{"127.0.0.1:7681", ""} {
			t.Run(name+"/"+strconv.Quote(string(k)), func(t *testing.T) {
				rec := captureWarns(t)
				mustPanicWith(t, wantKeyPanic(k), func() { get(k) })
				if len(rec.msgs) != 0 {
					t.Errorf("getter logged before panicking: %v", rec.msgs)
				}
			})
		}
	}
}

// TestKeyNameGrammar pins the boundary of the name rule
// ([A-Za-z_][A-Za-z0-9_]*) on the shapes a mistyped or mis-built key actually
// takes — addresses, paths, durations, flags — plus the grammar's own edges.
// String carries the sweep: it is the widest-used getter and the only one with
// no parse and no Warn, so a non-panic outcome asserts exactly one thing.
func TestKeyNameGrammar(t *testing.T) {
	invalid := []Key{
		" ",
		"  APP_KEY", // padded: a name is never trimmed into validity
		"APP_KEY ",
		":8080",
		"/run/secrets/token",
		"6h",
		"1PORT", // leading digit
		"APP-KEY",
		"APP KEY",
		"APP.KEY",
		"${APP_KEY}",
		"APP=1",
		"🚀",
		"APP\nKEY",
		"pas\x00nul",
	}
	for _, k := range invalid {
		t.Run("invalid/"+strconv.Quote(string(k)), func(t *testing.T) {
			mustPanicWith(t, wantKeyPanic(k), func() { String(k) })
		})
	}

	valid := []Key{
		"A", "a", "_", "_A", "_9", "lower_case", "APP_PORT_2", "ENVX_TEST_GRAMMAR_OK",
		"AZaz_09", // every endpoint of the three character ranges
	}
	for _, k := range valid {
		t.Run("valid/"+strconv.Quote(string(k)), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("String(%q) panicked on a valid name: %v", k, r)
				}
			}()
			_ = String(k)
		})
	}
}
