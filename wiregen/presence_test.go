package wiregen_test

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/presence"
)

func presenceReg() *testRegistry {
	r := newTestRegistry()
	r.Types = []wiregen.Type{
		wiregen.TypeRef[presence.Wire](), wiregen.TypeRef[presence.Inner](),
		wiregen.TypeRef[presence.Inherited](), wiregen.TypeRef[presence.ProtoReq](),
	}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "presence", Version: "0"}
	return r
}

// What encoding/json writes for nil pointers, collections and embeds, at
// every depth, is what the decoder reads and the response schema admits.
func TestPresence_decoderAndSchemaAcceptEncodingJSON(t *testing.T) {
	one := 1
	onePtr := &one
	var nilInt *int
	var nilSlice []string
	var nilMap map[string]int
	var nilBytes []byte
	s := "s"
	instances := map[string]presence.Wire{
		"zero": {},
		"nil under non-nil pointers": {
			Base: &presence.Base{}, Items: &nilSlice, Map: &nilMap, Bytes: &nilBytes,
			Deep: &nilInt, DeepReq: &nilInt, Elems: []*int{nil, &one},
			Vals: map[string]*string{"k": nil}, Nested: []*presence.Inner{nil, {}},
		},
		"populated": {
			Base: &presence.Base{ID: 2}, Items: &[]string{"a"}, Map: &map[string]int{"k": 1},
			Bytes: &[]byte{1}, Deep: &onePtr, DeepReq: &onePtr, Elems: []*int{&one},
			Vals: map[string]*string{"k": &s}, Nested: []*presence.Inner{{A: 1}},
			Struct: presence.Inner{A: 1}, Quoted: &one, Some: &presence.Some{"a"},
			Octets: []presence.Octet{1, 2}, Name: "n",
		},
	}
	r := presenceReg()
	doc := mustGen(t, r.openapiFile)
	schema := compiled(t, doc, "#/components/schemas/Wire")
	var names, bodies []string
	for name, w := range instances {
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		names, bodies = append(names, name), append(bodies, string(b))
		if err := validates(t, schema, string(b)); err != nil {
			t.Errorf("%s: the response schema refuses encoding/json's %s: %v", name, b, err)
		}
	}
	always := map[string]int{}
	for _, b := range bodies {
		var m map[string]any
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			always[k]++
		}
	}
	var component struct {
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(doc), &component); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, k := range component.Components.Schemas["Wire"].Required {
		required[k] = true
	}
	for k, n := range always {
		if n == len(bodies) != required[k] {
			t.Errorf("key %q is in %d of %d encodings, but the schema's required is %v", k, n, len(bodies), required[k])
		}
	}
	for i, verdict := range decodeVerdicts(t, r, "decodeWire", bodies) {
		if verdict != "ok" {
			t.Errorf("%s: the decoder refuses encoding/json's %s: %s", names[i], bodies[i], verdict)
		}
	}
}

func requiredReg() *testRegistry {
	r := newTestRegistry()
	r.Types = []wiregen.Type{
		wiregen.TypeRef[presence.Required](), wiregen.TypeRef[presence.Inner](), wiregen.TypeRef[presence.Named](),
	}
	r.Mappings = map[string]wiregen.Mapping{
		"github.com/cplieger/go/wiregen/v2/testdata/presence.Mapped": {TS: "unknown"},
	}
	return r
}

// A required field the decoder reads whole is refused absent, naming the
// field, while null stays what the field's kind makes it: data for raw JSON
// and an unchecked mapping, {} for a named map, refused for a struct.
func TestPresence_requiredKeyRefusedAbsent(t *testing.T) {
	got := decodeResults(t, requiredReg(), "decodeRequired", []string{
		`{"raw":1,"mapped":"m","struct":{"a":1},"named":{"k":1}}`,
		`{"raw":null,"mapped":null,"struct":{"a":1},"named":null}`,
		`{"mapped":"m","struct":{"a":1},"named":{}}`,
		`{"raw":1,"struct":{"a":1},"named":{}}`,
		`{"raw":1,"mapped":"m","named":{}}`,
		`{"raw":1,"mapped":"m","struct":{"a":1}}`,
		`{"raw":1,"mapped":"m","struct":null,"named":{}}`,
	})
	want := []string{
		`{"raw":1,"mapped":"m","struct":{"a":1},"named":{"k":1}}`,
		`{"raw":null,"mapped":null,"struct":{"a":1},"named":{}}`,
		`$.required.raw: expected a JSON value, got undefined`,
		`$.required.mapped: expected a JSON value, got undefined`,
		`$.required.struct: expected a JSON value, got undefined`,
		`$.required.named: expected a JSON value, got undefined`,
		`$.inner: expected object, got null`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("body %d decoded to %s, want %s", i, got[i], want[i])
		}
	}
}

// The decoders read only own JSON properties: an inherited constructor or
// toString is absent, and a __proto__ key, in a map or a struct, survives as
// an own property.
func TestPresence_ownPropertiesOnly(t *testing.T) {
	r := presenceReg()
	got := decodeResults(t, r, "decodeInherited", []string{
		`{"meta":{"__proto__":"owned","k":"v"}}`,
		`{"meta":{},"constructor":"c","toString":"t","__proto__":"p"}`,
	})
	want := []string{
		`{"meta":{"__proto__":"owned","k":"v"}}`,
		`{"meta":{},"constructor":"c","toString":"t","__proto__":"p"}`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("decoded %s, want %s", got[i], want[i])
		}
	}
	if got := decodeResults(t, r, "decodeProtoReq", []string{`{"__proto__":"p"}`, `{}`}); got[0] != `{"__proto__":"p"}` || got[1] == `{}` {
		t.Errorf("decodeProtoReq = %v, want the own __proto__ kept and an absent one refused", got)
	}
}
