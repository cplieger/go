package masked_test

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
)

const redacted = "[redacted]"

// runtimeVerb builds a format string at run time, so go vet's printf check
// cannot see the verb the call uses.
func runtimeVerb(verb rune) string { return "%" + string(verb) }

// verbs are the fmt verbs a Secret and its holders are printed with; each
// reaches Format. %p is added separately, through runtimeVerb.
var verbs = []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t", "%c", "%U", "%e", "%10.3s"}

// dump prints v the way a reflective printer that follows pointers does
// (go-spew, go-cmp): every field, exported or not, with no method called.
func dump(b *strings.Builder, v reflect.Value, depth int) {
	if depth > 16 {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			dump(b, v.Elem(), depth+1)
		}
	case reflect.Struct:
		for _, f := range v.Fields() {
			dump(b, f, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			dump(b, v.Index(i), depth+1)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			dump(b, it.Key(), depth+1)
			dump(b, it.Value(), depth+1)
		}
	case reflect.String:
		b.WriteString(v.String())
	case reflect.Uint8:
		b.WriteByte(byte(v.Uint()))
	default:
		fmt.Fprintf(b, "%v ", v)
	}
}

func dumped(v any) string {
	var b strings.Builder
	dump(&b, reflect.ValueOf(v), 0)
	return b.String()
}

// renderings returns v printed through every route a Secret must survive,
// keyed by route. The %p route is fmt's bad-verb path for a non-pointer.
func renderings(t *testing.T, v any) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, verb := range verbs {
		out[verb] = fmt.Sprintf(verb, v)
	}
	out["%p"] = fmt.Sprintf(runtimeVerb('p'), v)
	out["Sprint"] = fmt.Sprint(v)
	out["Sprintln"] = fmt.Sprintln(v)
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("msg", "v", v, slog.Group("g", "v", v))
	slog.New(slog.NewJSONHandler(&js, nil)).Info("msg", "v", v, slog.Group("g", "v", v))
	out["slog.TextHandler"] = text.String()
	out["slog.JSONHandler"] = js.String()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", v, err)
	}
	out["json.Marshal"] = string(b)
	out["reflect"] = dumped(v)
	return out
}

// checkNeverPrints requires that no rendering of v holds text in any form
// Leak knows, and that every route that reaches the Secret's own methods
// renders "[redacted]".
func checkNeverPrints(t *testing.T, v any, text string) {
	t.Helper()
	for route, got := range renderings(t, v) {
		if maskedtest.Leak(got, text) {
			t.Errorf("%s printed the text: %s", route, got)
		}
		if route != "%p" && route != "reflect" && !strings.Contains(got, redacted) {
			t.Errorf("%s = %s, want it to contain %q", route, got, redacted)
		}
	}
}

// holder is a plain struct with no printing methods of its own.
type holder struct {
	Label  string
	Value  masked.Secret
	hidden masked.Secret
}

func TestSecretNeverPrints(t *testing.T) {
	t.Parallel()
	const known = "pl41n-v4lue-0451"
	t.Run("checker_sees_plain_text", func(t *testing.T) {
		t.Parallel()
		plain := struct{ s string }{s: known}
		for route, got := range map[string]string{
			"reflect": dumped(plain),
			"%x":      fmt.Sprintf("%x", known),
			"base64":  base64.StdEncoding.EncodeToString([]byte("x" + known)),
		} {
			if !maskedtest.Leak(got, known) {
				t.Errorf("%s of a plain string: Leak = false, want true", route)
			}
		}
	})
	s := masked.NewSecret(known)
	h := holder{Label: "lbl", Value: s, hidden: s}
	values := map[string]any{
		"value":            s,
		"pointer":          &s,
		"array":            [1]masked.Secret{s},
		"slice":            []masked.Secret{s},
		"map":              map[string]masked.Secret{"k": s},
		"field":            h,
		"pointer_to_field": &h,
		"interface_slice":  []any{s},
	}
	for name, v := range values {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkNeverPrints(t, v, known)
		})
	}
	t.Run("masked_bytes", func(t *testing.T) {
		t.Parallel()
		// A 32-byte tag and the encrypted text: everything a printer that
		// follows the pointer can reach.
		b := masked.MaskedBytes(s)
		if got, want := len(b), sha256.Size+len(known); got != want {
			t.Errorf("Secret holds %d masked bytes, want %d", got, want)
		}
		if maskedtest.Leak(string(b), known) {
			t.Errorf("Secret holds the text in its masked bytes: %q", b)
		}
	})
	t.Run("type_graph", func(t *testing.T) {
		t.Parallel()
		allow := maskedtest.Allow{Fields: []maskedtest.Field{{In: reflect.TypeFor[holder](), Name: "Label"}}}
		for _, line := range maskedtest.Unmasked(allow, reflect.TypeFor[*holder]()) {
			t.Error(line)
		}
	})
	t.Run("zero", func(t *testing.T) {
		t.Parallel()
		var zero masked.Secret
		for _, verb := range verbs {
			if got := fmt.Sprintf(verb, zero); got != "" {
				t.Errorf("Sprintf(%s, zero) = %q, want empty", verb, got)
			}
			if got := fmt.Sprintf(verb, &zero); got != "" {
				t.Errorf("Sprintf(%s, &zero) = %q, want empty", verb, got)
			}
		}
		if got := zero.LogValue().String(); got != "" {
			t.Errorf("zero.LogValue() = %q, want empty", got)
		}
		var text bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("msg", "v", zero)
		if got := text.String(); !strings.Contains(got, ` v=""`) {
			t.Errorf("slog.TextHandler = %s, want v=\"\"", got)
		}
		var js bytes.Buffer
		slog.New(slog.NewJSONHandler(&js, nil)).Info("msg", "v", zero)
		if got := js.String(); !strings.Contains(got, `"v":""`) {
			t.Errorf("slog.JSONHandler = %s, want \"v\":\"\"", got)
		}
		b, err := json.Marshal(zero)
		if err != nil {
			t.Fatalf("json.Marshal(zero) error = %v", err)
		}
		if got := string(b); got != `""` {
			t.Errorf("json.Marshal(zero) = %s, want \"\"", got)
		}
	})
}

// typeCheck type-checks the package's own source with one more function,
// probe(a, b Secret) bool, whose body returns expr.
func typeCheck(t *testing.T, expr string) error {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	probe := "package masked\n\nfunc probe(a, b Secret) bool { return " + expr + " }\n"
	f, err := parser.ParseFile(fset, "probe.go", probe, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	_, err = conf.Check("github.com/cplieger/go/masked", fset, append(files, f), nil)
	return err
}

func TestSecretEqualityDoesNotCompile(t *testing.T) {
	t.Parallel()
	if err := typeCheck(t, "a.Equal(b)"); err != nil {
		t.Fatalf("the probe does not type-check a.Equal(b): %v", err)
	}
	err := typeCheck(t, "a == b")
	if err == nil {
		t.Fatal("a == b on two Secrets type-checks, want a compile error")
	}
	if !strings.Contains(err.Error(), "cannot be compared") {
		t.Errorf("a == b error = %v, want one saying a Secret cannot be compared", err)
	}
}

func TestSecretKey(t *testing.T) {
	t.Parallel()
	// At most 32 bytes, so a Key that copied the text's leading bytes would
	// hold the whole text, which the leak subtest looks for.
	const text = "kn0wn-s3cret-v4lue-0451"
	other := "X" + text[1:]
	shared := strings.Repeat("s", 32)
	t.Run("same_text", func(t *testing.T) {
		t.Parallel()
		a, b := masked.NewSecret(text), masked.NewSecret(text)
		if a.Key() != b.Key() {
			t.Error("two NewSecret of one text give different keys")
		}
	})
	t.Run("different_text", func(t *testing.T) {
		t.Parallel()
		if masked.NewSecret(text).Key() == masked.NewSecret(other).Key() {
			t.Error("two NewSecret of different texts give one key")
		}
	})
	t.Run("zero", func(t *testing.T) {
		t.Parallel()
		for name, z := range map[string]masked.Secret{"literal": {}, "empty": masked.NewSecret("")} {
			if z.Key() != ([32]byte{}) {
				t.Errorf("%s: Key() = %x, want 32 zero bytes", name, z.Key())
			}
		}
		if masked.NewSecret(text).Key() == ([32]byte{}) {
			t.Error("a non-zero Secret gives 32 zero bytes")
		}
	})
	t.Run("agrees_with_equal", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			a, b masked.Secret
			name string
			want bool
		}{
			{name: "same_text", a: masked.NewSecret(text), b: masked.NewSecret(text), want: true},
			{name: "first_byte_differs", a: masked.NewSecret(text), b: masked.NewSecret(other), want: false},
			{name: "last_byte_differs", a: masked.NewSecret(text), b: masked.NewSecret(text[:len(text)-1] + "X"), want: false},
			{name: "unequal_length", a: masked.NewSecret("a"), b: masked.NewSecret("aa"), want: false},
			{name: "shared_32_byte_prefix", a: masked.NewSecret(shared + strings.Repeat("a", 32)), b: masked.NewSecret(shared + strings.Repeat("b", 32)), want: false},
			{name: "two_zero", a: masked.Secret{}, b: masked.NewSecret(""), want: true},
			{name: "zero_and_non_zero", a: masked.Secret{}, b: masked.NewSecret(text), want: false},
			{name: "non_zero_and_zero", a: masked.NewSecret(text), b: masked.Secret{}, want: false},
		}
		for _, tc := range cases {
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Errorf("%s: Equal() = %t, want %t", tc.name, got, tc.want)
			}
			if got := tc.a.Key() == tc.b.Key(); got != tc.want {
				t.Errorf("%s: Key() == Key() is %t, want %t", tc.name, got, tc.want)
			}
		}
	})
	t.Run("map_key", func(t *testing.T) {
		t.Parallel()
		m := map[[32]byte]int{masked.NewSecret(text).Key(): 7}
		if got, ok := m[masked.NewSecret(text).Key()]; !ok || got != 7 {
			t.Errorf("map lookup by a fresh NewSecret = (%d, %t), want (7, true)", got, ok)
		}
		if _, ok := m[masked.NewSecret(other).Key()]; ok {
			t.Error("map lookup by another text's key found a value")
		}
	})
	t.Run("leak", func(t *testing.T) {
		t.Parallel()
		k := masked.NewSecret(text).Key()
		for route, got := range map[string]string{
			"clear":      string(k[:]),
			"hex":        hex.EncodeToString(k[:]),
			"base64":     base64.StdEncoding.EncodeToString(k[:]),
			"url_base64": base64.RawURLEncoding.EncodeToString(k[:]),
			"%x":         fmt.Sprintf("%x", k),
			"%v":         fmt.Sprintf("%v", k),
		} {
			if maskedtest.Leak(got, text) {
				t.Errorf("the key's %s form holds the text: %s", route, got)
			}
		}
	})
}

func TestSecretReveal(t *testing.T) {
	t.Parallel()
	const known = "pl41n-v4lue-0451"
	s := masked.NewSecret(known)
	if got := s.Reveal(); got != known {
		t.Errorf("Reveal() = %q, want %q", got, known)
	}
	if s.IsZero() {
		t.Error("IsZero() = true for a non-empty plaintext, want false")
	}
	for name, z := range map[string]masked.Secret{"literal": {}, "empty": masked.NewSecret("")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !z.IsZero() {
				t.Error("IsZero() = false, want true")
			}
			if got := z.Reveal(); got != "" {
				t.Errorf("Reveal() = %q, want empty", got)
			}
		})
	}
}

func TestSecretDeepEqual(t *testing.T) {
	t.Parallel()
	const text = "pl41n-v4lue-0451"
	if !reflect.DeepEqual(masked.NewSecret(text), masked.NewSecret(text)) {
		t.Error("reflect.DeepEqual of two NewSecret of one text = false, want true")
	}
	if reflect.DeepEqual(masked.NewSecret(text), masked.NewSecret(text+"X")) {
		t.Error("reflect.DeepEqual of two NewSecret of different texts = true, want false")
	}
}

func TestSecretHasNoDecoder(t *testing.T) {
	t.Parallel()
	const known = "pl41n-v4lue-0451"
	t.Run("interfaces", func(t *testing.T) {
		t.Parallel()
		codecs := []reflect.Type{
			reflect.TypeFor[json.Unmarshaler](),
			reflect.TypeFor[encoding.TextMarshaler](),
			reflect.TypeFor[encoding.TextUnmarshaler](),
		}
		for _, typ := range []reflect.Type{reflect.TypeFor[masked.Secret](), reflect.TypeFor[*masked.Secret]()} {
			for _, codec := range codecs {
				if typ.Implements(codec) {
					t.Errorf("%s implements %s", typ, codec)
				}
			}
		}
	})
	t.Run("string", func(t *testing.T) {
		t.Parallel()
		var v masked.Secret
		err := decode(`"`+known+`"`, &v)
		switch {
		case err == nil:
			t.Error("json.Unmarshal(string) error = nil, want an error")
		case strings.Contains(err.Error(), known):
			t.Errorf("json.Unmarshal(string) error %q holds the input", err)
		case !strings.Contains(err.Error(), "masked.Secret"):
			t.Errorf("json.Unmarshal(string) error %q does not name the type", err)
		}
		if !v.IsZero() {
			t.Error("json.Unmarshal(string) set the Secret")
		}
	})
	for name, in := range map[string]string{"null": "null", "object": `{"p":"` + known + `"}`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var z masked.Secret
			if err := decode(in, &z); err != nil {
				t.Errorf("json.Unmarshal(%s) error = %v", in, err)
			}
			if !z.IsZero() {
				t.Errorf("json.Unmarshal(%s) set the Secret, want it left zero", in)
			}
		})
	}
}

// decode is json.Unmarshal behind an any, so staticcheck's check for a
// struct with nothing to decode does not refuse the call these tests make
// on purpose.
func decode(in string, dst any) error { return json.Unmarshal([]byte(in), dst) }
