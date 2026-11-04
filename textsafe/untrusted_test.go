package textsafe_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/go/textsafe"
)

// TestUntrustedSinkForms pins every emission form against the preset
// oracles — String, MarshalText, and LogValue must equal Sanitize;
// SingleLine must equal SanitizeSingleLine — while Raw round-trips the
// exact input bytes, including invalid UTF-8.
func TestUntrustedSinkForms(t *testing.T) {
	inputs := []string{
		"",
		"plain text",
		"葬送のフリーレン",
		"a\x1b[2Jb",
		"a\u009bb",
		"a\u202evil\u202cb",
		"a\u2028b\u2029c",
		"line1\nline2\rline3",
		"a\xffb",
	}
	for _, in := range inputs {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			u := textsafe.NewUntrusted(in)
			want := textsafe.Sanitize(in)
			if got := u.String(); got != want {
				t.Errorf("String() = %q, want Sanitize form %q", got, want)
			}
			text, err := u.MarshalText()
			if err != nil {
				t.Errorf("MarshalText() error: %v", err)
			}
			if got := string(text); got != want {
				t.Errorf("MarshalText() = %q, want Sanitize form %q", got, want)
			}
			v := u.LogValue()
			if v.Kind() != slog.KindString {
				t.Errorf("LogValue().Kind() = %v, want KindString", v.Kind())
			}
			if got := v.String(); got != want {
				t.Errorf("LogValue() = %q, want Sanitize form %q", got, want)
			}
			if got, wantStrict := u.SingleLine(), textsafe.SanitizeSingleLine(in); got != wantStrict {
				t.Errorf("SingleLine() = %q, want SanitizeSingleLine form %q", got, wantStrict)
			}
			if got := u.Raw(); got != in {
				t.Errorf("Raw() = %q, want exact input %q", got, in)
			}
		})
	}
}

// TestUntrustedJSONAsymmetry pins the decode-raw / encode-sanitized
// contract: a JSON document carrying a C1 introducer and a bidi override
// decodes into the tagged field byte-exact, and marshaling the same struct
// emits the sanitized form — including for a nested field, the class no
// sink-side rewriter can reach.
func TestUntrustedJSONAsymmetry(t *testing.T) {
	type inner struct {
		Title textsafe.Untrusted `json:"title"`
	}
	type doc struct {
		Title  textsafe.Untrusted `json:"title"`
		Nested inner              `json:"nested"`
	}
	raw := "Frieren\u009b\u202egpj.exe"
	blob := `{"title":"Frieren\u009b\u202egpj.exe","nested":{"title":"Frieren\u009b\u202egpj.exe"}}`
	var d doc
	if err := json.Unmarshal([]byte(blob), &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Title.Raw() != raw || d.Nested.Title.Raw() != raw {
		t.Fatalf("decode changed bytes: top %q nested %q, want %q", d.Title.Raw(), d.Nested.Title.Raw(), raw)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := textsafe.Sanitize(raw)
	for _, r := range []rune{'\u009b', '\u202e'} {
		if bytes.ContainsRune(out, r) {
			t.Errorf("marshaled document carries raw %U: %s", r, out)
		}
	}
	var back struct {
		Title  string `json:"title"`
		Nested struct {
			Title string `json:"title"`
		} `json:"nested"`
	}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back.Title != want || back.Nested.Title != want {
		t.Errorf("marshaled forms = %q / %q, want Sanitize form %q", back.Title, back.Nested.Title, want)
	}
}

// TestUntrustedSlogResolution proves the LogValuer fires in both built-in
// handlers, as a bare kv attr and inside a group: the encoded record
// carries the sanitized form and never the raw C1/bidi runes.
func TestUntrustedSlogResolution(t *testing.T) {
	u := textsafe.NewUntrusted("Frieren\u009b\u202egpj.exe")
	handlers := map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	}
	for name, mk := range handlers {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(mk(&buf)).Info("emit", "title", u, slog.Group("g", "title", u))
			got := buf.String()
			for _, r := range []rune{'\u009b', '\u202e'} {
				if strings.ContainsRune(got, r) {
					t.Errorf("%s record carries raw %U: %s", name, r, got)
				}
			}
			if !strings.Contains(got, "Frieren") {
				t.Errorf("%s record lost the safe text: %s", name, got)
			}
		})
	}
}

// TestUntrustedErrorConstruction pins the error-class coverage: an error
// built with fmt.Errorf("%s", v) carries the sanitized form at
// construction, before any sink sees it.
func TestUntrustedErrorConstruction(t *testing.T) {
	u := textsafe.NewUntrusted("bad request\u009b\nlevel=ERROR forged")
	err := fmt.Errorf("upstream said %s", u)
	msg := err.Error()
	if strings.ContainsRune(msg, '\u009b') {
		t.Errorf("error message carries raw C1: %q", msg)
	}
	if want := "upstream said " + textsafe.Sanitize(u.Raw()); msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
}

// TestUntrustedComparableRaw pins the compute contract: equality and map
// keys operate on the raw bytes (two values differing only in an unsafe
// rune stay distinct), so matching and dedupe keep working on tagged
// fields without unwrapping.
func TestUntrustedComparableRaw(t *testing.T) {
	a, b := textsafe.NewUntrusted("x\u202ey"), textsafe.NewUntrusted("x y")
	if a == b {
		t.Error("raw-distinct values compare equal; equality must be on raw bytes")
	}
	if a.String() != b.String() {
		t.Errorf("sanitized forms differ: %q vs %q", a.String(), b.String())
	}
	m := map[textsafe.Untrusted]int{a: 1, b: 2}
	if len(m) != 2 {
		t.Errorf("map collapsed raw-distinct keys: %v", m)
	}
}

// TestUntrustedKeptCRLFCannotForgeRecord pins the doc-comment claim that
// the keepCRLF=true preset in LogValue is safe for BOTH built-in handlers:
// a kept CR/LF is escaped by the handler's own encoding (JSONHandler string
// escaping; TextHandler strconv quoting), so an upstream newline can never
// forge a second log record. An environmental-conformance pin, like
// TestClassifierUnicodeConformance: it guards the stdlib behavior the
// package's safety claim rests on across Go upgrades.
func TestUntrustedKeptCRLFCannotForgeRecord(t *testing.T) {
	u := textsafe.NewUntrusted("ok\r\nlevel=ERROR msg=forged")
	handlers := map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	}
	for name, mk := range handlers {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(mk(&buf)).Info("emit", "title", u)
			got := buf.String()
			if n := strings.Count(got, "\n"); n != 1 {
				t.Errorf("%s record carries %d newlines, want exactly 1 (the record terminator): %q", name, n, got)
			}
			if strings.Contains(got, "\r") {
				t.Errorf("%s record carries a raw CR: %q", name, got)
			}
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("%s record does not end with the terminator: %q", name, got)
			}
		})
	}
}

// TestUntrustedNoSinkEmitsRaw pins that no raw C1 or bidi rune reaches encoder,
// slog or fmt output from an Untrusted held directly, behind a pointer or an
// interface, in a slice or map value, or as a map key of those shapes. A
// string-kinded key of these shapes skips MarshalText on go1.27.2's
// encoding/json (https://go.dev/issue/81355); a struct-kinded one cannot. The
// value shapes are regression pins.
func TestUntrustedNoSinkEmitsRaw(t *testing.T) {
	u := textsafe.NewUntrusted("k\u009b\u202e")
	shapes := map[string]any{
		"value":                 u,
		"pointer":               &u,
		"interface":             []encoding.TextMarshaler{u},
		"slice":                 []textsafe.Untrusted{u},
		"map-value":             map[string]textsafe.Untrusted{"k": u},
		"map-key":               map[textsafe.Untrusted]int{u: 1},
		"pointer-map-key":       map[*textsafe.Untrusted]int{&u: 1},
		"interface-map-key":     map[encoding.TextMarshaler]int{u: 1},
		"interface-pointer-key": map[encoding.TextMarshaler]int{&u: 1},
		"struct-field-map-key":  struct{ M map[textsafe.Untrusted]int }{map[textsafe.Untrusted]int{u: 1}},
	}
	encoders := map[string]func(any) ([]byte, error){
		"json":    json.Marshal,
		"json-v2": func(v any) ([]byte, error) { return jsonv2.Marshal(v) },
		"slog-json": func(v any) ([]byte, error) {
			var buf bytes.Buffer
			slog.New(slog.NewJSONHandler(&buf, nil)).Info("emit", "v", v)
			return buf.Bytes(), nil
		},
		"slog-text": func(v any) ([]byte, error) {
			var buf bytes.Buffer
			slog.New(slog.NewTextHandler(&buf, nil)).Info("emit", "v", v)
			return buf.Bytes(), nil
		},
		"fmt": func(v any) ([]byte, error) { return fmt.Appendf(nil, "%v %+v %s", v, v, v), nil },
	}
	want := []byte(u.String())
	for shape, v := range shapes {
		t.Run(shape, func(t *testing.T) {
			for name, encode := range encoders {
				t.Run(name, func(t *testing.T) {
					out, err := encode(v)
					if err != nil {
						t.Fatalf("encode: %v", err)
					}
					for _, r := range []rune{'\u009b', '\u202e'} {
						if bytes.ContainsRune(out, r) {
							t.Errorf("output carries raw %U: %q", r, out)
						}
					}
					if !bytes.Contains(out, want) {
						t.Errorf("output %q does not carry the Sanitize form %q", out, want)
					}
				})
			}
		})
	}
	t.Run("xml", func(t *testing.T) {
		doc := struct {
			XMLName xml.Name           `xml:"d"`
			Attr    textsafe.Untrusted `xml:"a,attr"`
			Elem    textsafe.Untrusted `xml:"e"`
		}{Attr: u, Elem: u}
		out, err := xml.Marshal(doc)
		if err != nil {
			t.Fatalf("xml.Marshal: %v", err)
		}
		if w := `<d a="k  "><e>k  </e></d>`; string(out) != w {
			t.Errorf("xml.Marshal = %q, want %q", out, w)
		}
	})
}

// TestUntrustedReflectionPrintsNoRaw pins that no raw C1 or bidi rune reaches
// fmt or slog's TextHandler on the paths that never call String: a verb
// String does not serve, %p, which fmt formats by reflection before any
// method, and an unexported field, through which fmt calls no method at all
// (https://pkg.go.dev/fmt#hdr-Printing). Only the stored representation
// protects the last two.
func TestUntrustedReflectionPrintsNoRaw(t *testing.T) {
	u := textsafe.NewUntrusted("k\u009b\u202e")
	holders := map[string]any{
		"value":              u,
		"pointer":            &u,
		"exported-field":     struct{ V textsafe.Untrusted }{u},
		"unexported-field":   struct{ v textsafe.Untrusted }{u},
		"unexported-slice":   struct{ s []textsafe.Untrusted }{[]textsafe.Untrusted{u}},
		"unexported-map-key": struct{ m map[textsafe.Untrusted]int }{map[textsafe.Untrusted]int{u: 1}},
	}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%c", "%d", "%U", "%f", "%o", "%t", "%p", "%-6.2s"}
	for name, h := range holders {
		t.Run(name, func(t *testing.T) {
			outputs := map[string]string{}
			for _, verb := range verbs {
				outputs[verb] = fmt.Sprintf(verb, h)
			}
			var buf bytes.Buffer
			slog.New(slog.NewTextHandler(&buf, nil)).Info("emit", "v", h)
			outputs["slog-text"] = buf.String()
			for via, out := range outputs {
				for _, r := range []rune{'\u009b', '\u202e'} {
					if strings.ContainsRune(out, r) {
						t.Errorf("%s output carries raw %U: %q", via, r, out)
					}
				}
			}
		})
	}
}

// TestUntrustedFormat pins fmt's output for the verbs Format controls: a
// string verb formats the Sanitize form with the directive's flags, a
// non-string verb reports it, and %#v prints Go syntax that rebuilds the raw
// value, ignoring width and precision. %T never reaches Format and prints the
// type name; TestUntrustedReflectionPrintsNoRaw covers %p.
func TestUntrustedFormat(t *testing.T) {
	u := textsafe.NewUntrusted("k\u009b\u202e")
	for _, tc := range []struct {
		format, want string
	}{
		{"%v", "k  "},
		{"%+v", "k  "},
		{"%s", "k  "},
		{"%q", `"k  "`},
		{"%x", "6b2020"},
		{"%-5s|", "k    |"},
		{"%.1s", "k"},
		{"%d", "%!d(string=k  )"},
		{"%#v", `textsafe.NewUntrusted("k\u009b\u202e")`},
		{"%#40v", `textsafe.NewUntrusted("k\u009b\u202e")`},
		{"%#.3v", `textsafe.NewUntrusted("k\u009b\u202e")`},
		{"%T", "textsafe.Untrusted"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			if got := fmt.Sprintf(tc.format, u); got != tc.want {
				t.Errorf("Sprintf(%q) = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
	if got, want := fmt.Sprintf("%#v", textsafe.Untrusted{}), `textsafe.NewUntrusted("")`; got != want {
		t.Errorf("Sprintf(%%#v) of the zero value = %q, want %q", got, want)
	}
}

// TestUntrustedDecodesLikeString pins that a tagged JSON field, or XML
// attribute, element or character-data field, decodes exactly as a plain
// string field does, so tagging a decode struct changes no decoded byte and no
// decode outcome: raw runes and escapes survive, a JSON null is handled as for
// a string (left as it was by encoding/json, zeroed by encoding/json/v2), and
// a non-string value or malformed document is rejected.
func TestUntrustedDecodesLikeString(t *testing.T) {
	inputs := []string{
		`{"t":"Frieren\u009b\u202egpj.exe"}`,
		`{"t":"a\u2028b\nc\ud800"}`,
		"{\"t\":\"raw\u202e\xffbyte\"}",
		`{"t":""}`,
		`{"t":null}`,
		`{}`,
		`{"t":5}`,
		`{"t":true}`,
		`{"t":{}}`,
	}
	type unmarshal func([]byte, any) error
	decoders := map[string]unmarshal{
		"json":    json.Unmarshal,
		"json-v2": func(b []byte, v any) error { return jsonv2.Unmarshal(b, v) },
	}
	for name, decode := range decoders {
		t.Run(name, func(t *testing.T) {
			for _, in := range inputs {
				plain := struct {
					T string `json:"t"`
				}{T: "before"}
				tagged := struct {
					T textsafe.Untrusted `json:"t"`
				}{T: textsafe.NewUntrusted("before")}
				plainErr := decode([]byte(in), &plain)
				taggedErr := decode([]byte(in), &tagged)
				if (plainErr == nil) != (taggedErr == nil) {
					t.Errorf("%s: plain err %v, tagged err %v, want the same outcome", in, plainErr, taggedErr)
				}
				if tagged.T.Raw() != plain.T {
					t.Errorf("%s: tagged decoded %q, plain decoded %q", in, tagged.T.Raw(), plain.T)
				}
			}
		})
	}
	t.Run("xml", func(t *testing.T) {
		type plainDoc struct {
			XMLName xml.Name `xml:"d"`
			A       string   `xml:"a,attr"`
			E       string   `xml:"e"`
			N       string   `xml:"n>c"`
			CD      string   `xml:",chardata"`
		}
		type taggedDoc struct {
			XMLName xml.Name           `xml:"d"`
			A       textsafe.Untrusted `xml:"a,attr"`
			E       textsafe.Untrusted `xml:"e"`
			N       textsafe.Untrusted `xml:"n>c"`
			CD      textsafe.Untrusted `xml:",chardata"`
		}
		for _, in := range []string{
			"<d a=\"x\u202e\">t\u009b<e>k\u009b\u202e</e><n><c>c\u202e</c></n></d>",
			`<d a="&amp;&#x202E;">&#x9B;<e>&#x9B;&lt;</e><n><c>&#x2028;</c></n></d>`,
			`<d a=""><e/><n><c></c></n></d>`,
			`<d><e>a<b>z</b>c</e></d>`,
			`<d/>`,
			`<d><e>x</d>`,
		} {
			before := textsafe.NewUntrusted("before")
			plain := plainDoc{A: "before", E: "before", N: "before", CD: "before"}
			tagged := taggedDoc{A: before, E: before, N: before, CD: before}
			plainErr := xml.Unmarshal([]byte(in), &plain)
			taggedErr := xml.Unmarshal([]byte(in), &tagged)
			if (plainErr == nil) != (taggedErr == nil) {
				t.Errorf("%s: plain err %v, tagged err %v, want the same outcome", in, plainErr, taggedErr)
			}
			got := [4]string{tagged.A.Raw(), tagged.E.Raw(), tagged.N.Raw(), tagged.CD.Raw()}
			if want := [4]string{plain.A, plain.E, plain.N, plain.CD}; got != want {
				t.Errorf("%s: tagged decoded %q, plain decoded %q", in, got, want)
			}
		}
	})
}

// TestUntrustedMapKeyRawIdentity pins the raw half of the key contract:
// decoding keeps each key's bytes exactly, and raw-distinct keys that sanitize
// to one name stay distinct in the map, so encoding/json writes the name twice
// while encoding/json/v2 refuses the duplicate.
func TestUntrustedMapKeyRawIdentity(t *testing.T) {
	var decoded map[textsafe.Untrusted]int
	if err := json.Unmarshal([]byte(`{"k\u009b\u202e":1}`), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded[textsafe.NewUntrusted("k\u009b\u202e")]; !ok || len(decoded) != 1 {
		t.Errorf("decoded keys = %v, want exactly the raw key", decoded)
	}

	pair := map[textsafe.Untrusted]int{textsafe.NewUntrusted("k\u202e"): 1, textsafe.NewUntrusted("k "): 2}
	if len(pair) != 2 {
		t.Fatalf("map collapsed raw-distinct keys: %v", pair)
	}
	out, err := json.Marshal(pair)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if n := bytes.Count(out, []byte(`"k "`)); n != 2 {
		t.Errorf("Marshal = %s, want the sanitized name written twice", out)
	}
	if _, err := jsonv2.Marshal(pair); err == nil {
		t.Error("json/v2 accepted a duplicate object name from two raw-distinct keys")
	}
}

// TestUntrustedSurvivesJSONv2Strictness pins what the tag buys under
// encoding/json/v2, whose defaults reject invalid UTF-8 in a JSON string where
// v1 substitutes U+FFFD. A plain string field carrying one invalid byte aborts
// the marshal with `jsontext: invalid UTF-8 within "/v"` mid-object, while the
// tagged field emits the Sanitize form, always valid UTF-8, byte-identical to v1.
func TestUntrustedSurvivesJSONv2Strictness(t *testing.T) {
	const bad = "a\xffb\u009b\u202e"

	tagged := struct {
		V textsafe.Untrusted `json:"v"`
	}{V: textsafe.NewUntrusted(bad)}
	out, err := jsonv2.Marshal(tagged)
	if err != nil {
		t.Fatalf("json/v2 Marshal of a tagged field: %v", err)
	}
	if want := `{"v":"` + textsafe.Sanitize(bad) + `"}`; string(out) != want {
		t.Errorf("json/v2 Marshal = %s, want the Sanitize form %s", out, want)
	}
	if v1, err := json.Marshal(tagged); err != nil || string(v1) != string(out) {
		t.Errorf("v1 Marshal = %s (err %v), want v2's bytes %s", v1, err, out)
	}

	plain := struct {
		V string `json:"v"`
	}{V: bad}
	if _, err := jsonv2.Marshal(plain); err == nil {
		t.Error("json/v2 accepted invalid UTF-8 in an untagged string field; the tag's value here rests on it refusing")
	}
}
