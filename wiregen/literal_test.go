package wiregen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/literal"
)

func literalReg() *testRegistry {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[literal.A](), wiregen.TypeRef[literal.B]()}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[literal.Kind]()}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "literal", Version: "0"}
	return r
}

// A field the wiregen const tag pins is typed as its one value, so a union
// of such types narrows on it.
func TestConst_typeIsTheLiteral(t *testing.T) {
	out := mustGen(t, literalReg().typesFile)
	for _, want := range []string{"  kind: \"a\";\n", "  tag: \"x\";\n", "  n: number;\n", "  kind: \"b\";\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("types lack %q:\n%s", want, out)
		}
	}
}

// The decoder accepts the pinned value alone, though the enum has others.
func TestConst_decoderAcceptsOnlyTheLiteral(t *testing.T) {
	sent, err := json.Marshal(literal.A{Kind: literal.KindA, Tag: "x", N: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeResults(t, literalReg(), "decodeA", []string{
		string(sent),
		`{"kind":"b","tag":"x","n":1}`,
		`{"kind":"a","tag":"y","n":1}`,
		`{"tag":"x","n":1}`,
	})
	want := []string{
		`{"kind":"a","tag":"x","n":1}`,
		`$.a.kind: expected one of a, got "b"`,
		`$.a.tag: expected one of x, got "y"`,
		`$.a.kind: expected one of a, got undefined`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("body %d decoded to %s, want %s", i, got[i], want[i])
		}
	}
}

// The schema admits the pinned value alone, as JSON Schema's const does.
func TestConst_schemaAdmitsOnlyTheLiteral(t *testing.T) {
	doc := mustGen(t, literalReg().openapiFile)
	s := compiled(t, doc, "#/components/schemas/A")
	if err := validates(t, s, `{"kind":"a","tag":"x","n":1}`); err != nil {
		t.Errorf("the schema refuses the pinned values: %v", err)
	}
	for _, body := range []string{`{"kind":"b","tag":"x","n":1}`, `{"kind":"a","tag":"y","n":1}`} {
		if validates(t, s, body) == nil {
			t.Errorf("the schema admits %s", body)
		}
	}
}

// The arbitrary draws the pinned value alone.
func TestConst_arbitraryDrawsTheLiteral(t *testing.T) {
	out := mustGen(t, literalReg().arbitrariesFile)
	for _, want := range []string{`kind: fc.constant("a" as const),`, `tag: fc.constant("x" as const),`} {
		if !strings.Contains(out, want) {
			t.Errorf("arbitraries lack %q:\n%s", want, out)
		}
	}
}

// Generate refuses a const tag it cannot hold the field to.
func TestConst_refusals(t *testing.T) {
	cases := []struct {
		typ  wiregen.Type
		want string
	}{
		{wiregen.TypeRef[literal.NotString](), "literal.NotString.N: the wiregen const tag pins a string or string enum field"},
		{wiregen.TypeRef[literal.NotMember](), `literal.NotMember.Kind: the wiregen const "c" is no member of enum Kind`},
		{wiregen.TypeRef[literal.Omitted](), "literal.Omitted.Tag: the wiregen const tag pins a field encoding/json always writes, but this one can be left out"},
		{wiregen.TypeRef[literal.Pointer](), "literal.Pointer.Tag: the wiregen const tag pins a field encoding/json always writes as its value, but a pointer can be null"},
		{wiregen.TypeRef[literal.Unknown](), `literal.Unknown.Tag: wiregen tag "x" is not const=<value>`},
		{wiregen.TypeRef[literal.Empty](), "literal.Empty.Tag: the wiregen const tag names no value"},
		{wiregen.TypeRef[literal.Flat](), "literal.Flat.Inline.V: the wiregen tag on embedded Inline pins no field; tag the field itself"},
	}
	for _, tc := range cases {
		t.Run(tc.typ.Name, func(t *testing.T) {
			r := literalReg()
			r.Types = append(r.Types, tc.typ)
			requireGenerateError(t, r, tc.want)
		})
	}
	t.Run("union tag", func(t *testing.T) {
		r := literalReg()
		r.Unions = []wiregen.Union{{Type: wiregen.TypeRef[literal.Tagged](), Tag: "kind", Cases: map[string][]string{"a": {"n"}, "b": {"n"}}}}
		requireGenerateError(t, r, `tag "kind" is pinned by a wiregen const, but a union's tag varies by case`)
	})
}
