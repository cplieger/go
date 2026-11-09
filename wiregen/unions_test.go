package wiregen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/unions"
)

// timingUnion is the enum-tagged Timing fixture's Union.
func timingUnion() wiregen.Union {
	return wiregen.Union{
		Type: wiregen.TypeRef[unions.Timing](),
		Tag:  "kind",
		Cases: map[string][]string{
			"identity": {},
			"shift":    {"ms"},
			"linear":   {"scale", "offset_ms"},
		},
	}
}

// shapeUnion is the string-tagged Shape fixture's Union.
func shapeUnion() wiregen.Union {
	return wiregen.Union{
		Type: wiregen.TypeRef[unions.Shape](),
		Tag:  "type",
		Cases: map[string][]string{
			"circle": {"radius", "tags"},
			"square": {"side", "tags"},
		},
	}
}

// unionReg registers Holder, whose fields reach both unions.
func unionReg() *testRegistry {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[unions.Holder]()}
	r.Enums = []wiregen.EnumRef{wiregen.TextEnum[unions.Kind]()}
	r.Unions = []wiregen.Union{timingUnion(), shapeUnion()}
	return r
}

func TestUnion_TypeAlias(t *testing.T) {
	out := mustGen(t, unionReg().typesFile)
	for _, want := range []string{
		"export type Timing =\n" +
			"  | { kind: \"identity\" }\n" +
			"  | { kind: \"shift\"; ms?: number }\n" +
			"  | { kind: \"linear\"; scale?: number; offset_ms?: number };",
		"export type Shape =\n" +
			"  | { type: \"circle\"; radius?: number; tags: string[] }\n" +
			"  | { type: \"square\"; side?: number; tags: string[] };",
		"export interface Holder {\n  timing: Timing;\n  shapes: Shape[];\n}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("types missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "export interface Timing") {
		t.Errorf("a union type also emitted an interface:\n%s", out)
	}
}

func TestUnion_Decoder(t *testing.T) {
	out := mustGen(t, unionReg().decodersFile)
	for _, want := range []string{
		"export const decodeTiming: Decoder<Timing> = (v) => {",
		`const tag = reqOneOf(o, "kind", ["identity", "shift", "linear"] as const, "$.timing");`,
		`    case "identity": {`,
		`      refuseKeys(o, ["ms", "scale", "offset_ms"], "$.timing");`,
		`      const out: globalThis.Extract<Timing, { kind: "identity" }> = {`,
		`        kind: tag,`,
		`      refuseKeys(o, ["scale", "offset_ms"], "$.timing");`,
		`      const out: globalThis.Extract<Timing, { kind: "shift" }> = {`,
		`      refuseKeys(o, ["side"], "$.shape");`,
		"import { own, asObject, reqNum, reqOneOf, decodeArray, refuseKeys, type Decoder }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("decoders missing %q, got:\n%s", want, out)
		}
	}
}

// TestUnion_CaseOrder: an enum tag orders the cases as the enum declares
// them, a string tag by value.
func TestUnion_CaseOrder(t *testing.T) {
	out := mustGen(t, unionReg().typesFile)
	if i, j := strings.Index(out, `"identity"`), strings.Index(out, `"linear"`); i < 0 || j < i {
		t.Errorf("Timing cases out of enum order:\n%s", out)
	}
	if i, j := strings.Index(out, `"circle"`), strings.Index(out, `"square"`); i < 0 || j < i {
		t.Errorf("Shape cases out of value order:\n%s", out)
	}
}

func TestUnion_Refusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testRegistry)
		want string
	}{
		{"no such tag", func(r *testRegistry) { r.Unions[1].Tag = "kind" }, `has no field "kind"`},
		{"optional tag", func(r *testRegistry) {
			r.Types = append(r.Types, wiregen.TypeRef[unions.Untagged]())
			r.Unions = append(r.Unions, wiregen.Union{Type: wiregen.TypeRef[unions.Untagged](), Tag: "kind", Cases: map[string][]string{"a": {"name"}}})
		}, `tag "kind" must be a required string or enum field`},
		{"no case", func(r *testRegistry) { r.Unions[1].Cases = nil }, "declares no case"},
		{"enum cases differ", func(r *testRegistry) { delete(r.Unions[0].Cases, "linear") }, "must be exactly the members of tag enum Kind"},
		{"unknown field", func(r *testRegistry) { r.Unions[1].Cases["circle"] = []string{"diameter", "tags"} }, `lists "diameter", which is no field`},
		{"tag listed", func(r *testRegistry) { r.Unions[1].Cases["circle"] = []string{"type", "radius", "tags"} }, `lists the tag "type"`},
		{"field twice", func(r *testRegistry) { r.Unions[1].Cases["circle"] = []string{"radius", "radius", "tags"} }, `lists "radius" twice`},
		{"field in no case", func(r *testRegistry) { r.Unions[1].Cases["square"] = []string{"tags"} }, `field "side" is in no case`},
		{"always-written field in some cases", func(r *testRegistry) {
			r.Unions[1].Cases["square"] = []string{"side"}
		}, `field "tags" is not in every case, but encoding/json writes it in every case`},
		{"bad case name", func(r *testRegistry) {
			r.Unions[1].Cases["a b"] = r.Unions[1].Cases["circle"]
		}, `case "a b" must be letters`},
		{"declared twice", func(r *testRegistry) { r.Unions = append(r.Unions, shapeUnion()) }, "is declared twice"},
		{"no type", func(r *testRegistry) { r.Unions = append(r.Unions, wiregen.Union{Tag: "x"}) }, `union with tag "x" names no Type`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := unionReg()
			tc.edit(r)
			_, err := r.typesFile(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestUnion_TypeJoinsTypes: a union's type is generated without being listed
// in Types.
func TestUnion_TypeJoinsTypes(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{shapeUnion()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "export type Shape =") {
		t.Errorf("union type not generated:\n%s", out)
	}
}

// TestUnion_RecursiveArbitraryCapped: a union reaching itself draws through a
// depth-capped oneof whose first branches are the leaf cases.
func TestUnion_RecursiveArbitraryCapped(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{{
		Type: wiregen.TypeRef[unions.Tree](), Tag: "type",
		Cases: map[string][]string{"leaf": {"value"}, "node": {"children"}},
	}}
	out := mustGen(t, r.arbitrariesFile)
	entry := letrecEntry(t, out, "Tree")
	if !strings.HasPrefix(entry, "fc.oneof(") || !strings.Contains(out, "maxDepth: 5") {
		t.Fatalf("Tree arbitrary is not depth-capped:\n%s", out)
	}
	if !strings.Contains(out, `type: fc.constant("node" as const),`) {
		t.Errorf("tag not fixed per case:\n%s", out)
	}
}

// TestOverrideKeys_UnknownKeyIsRefused pins that a misspelt override key is a
// generation error naming every bad key, so a rename cannot be silently off.
func TestOverrideKeys_UnknownKeyIsRefused(t *testing.T) {
	r := unionReg()
	r.TSNameOverride = map[string]string{"Holdr": "Box"}
	r.PathNameOverride = map[string]string{"Shap": "shape"}
	r.EnumTSNameOverride = map[string]string{"Knd": "K"}
	_, err := r.typesFile(t.Context())
	if err == nil {
		t.Fatal("Generate accepted override keys that name nothing")
	}
	for _, want := range []string{`TSNameOverride["Holdr"]`, `PathNameOverride["Shap"]`, `EnumTSNameOverride["Knd"]`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// TestOverrideKeys_KnownKeysAreAccepted pins the other side: keys naming a
// registered type, a referenced union type and a registered enum pass.
func TestOverrideKeys_KnownKeysAreAccepted(t *testing.T) {
	r := unionReg()
	r.TSNameOverride = map[string]string{"Holder": "Box"}
	r.PathNameOverride = map[string]string{"Shape": "shape_v2"}
	r.EnumTSNameOverride = map[string]string{"Kind": "ShapeKind"}
	if _, err := r.typesFile(t.Context()); err != nil {
		t.Fatalf("Generate refused override keys that name registered types: %v", err)
	}
}

// TestMappings_UnreachedKeyIsRefused pins that a Mappings key no registered
// type's field reaches is a generation error naming it.
func TestMappings_UnreachedKeyIsRefused(t *testing.T) {
	r := unionReg()
	r.Mappings = map[string]wiregen.Mapping{"example.com/nowhere.Thing": {TS: "string"}}
	_, err := r.typesFile(t.Context())
	if err == nil || !strings.Contains(err.Error(), "no registered type's field reaches") || !strings.Contains(err.Error(), `"example.com/nowhere.Thing"`) {
		t.Fatalf("Generate = %v, want an error naming the unreached Mappings key", err)
	}
}

// TestGenerate_NilRegistryOrContext pins the documented error in place of a
// panic.
func TestGenerate_NilRegistryOrContext(t *testing.T) {
	var nilReg *wiregen.Registry
	if err := nilReg.Generate(t.Context(), wiregen.WithTSDir(t.TempDir())); err == nil {
		t.Error("Generate on a nil Registry returned nil")
	}
	//lint:ignore SA1012 the nil context is the case under test
	if err := (&wiregen.Registry{}).Generate(nil, wiregen.WithTSDir(t.TempDir())); err == nil { //nolint:staticcheck // the nil context is the case under test
		t.Error("Generate with a nil context returned nil")
	}
}

// commandUnion is the Command fixture's payload-typed Union: create and
// rename carry a payload, close carries none.
func commandUnion() wiregen.Union {
	return wiregen.Union{
		Type: wiregen.TypeRef[unions.Command](),
		Tag:  "type",
		Cases: map[string][]string{
			"create": {},
			"rename": {"chat_id"},
			"close":  {"chat_id"},
		},
		Payload: "payload",
		PayloadTypes: map[string]wiregen.Type{
			"create": wiregen.TypeRef[unions.CreateChat](),
			"rename": wiregen.TypeRef[unions.Rename](),
			"close":  {},
		},
	}
}

// commandReg registers Command alone: its payload types join the registry
// through the Union.
func commandReg() *testRegistry {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{commandUnion()}
	return r
}

func TestUnion_PayloadTypes(t *testing.T) {
	types := mustGen(t, commandReg().typesFile)
	for _, want := range []string{
		"export type Command =\n" +
			"  | { type: \"close\"; chat_id?: string }\n" +
			"  | { type: \"create\"; payload?: CreateChat }\n" +
			"  | { type: \"rename\"; chat_id?: string; payload?: Rename };",
		"export interface CreateChat {",
		"export interface Rename {",
	} {
		if !strings.Contains(types, want) {
			t.Errorf("types missing %q, got:\n%s", want, types)
		}
	}
	decoders := mustGen(t, commandReg().decodersFile)
	for _, want := range []string{
		`    case "close": {`,
		`      refuseKeys(o, ["payload"], "$.command");`,
		`      refuseKeys(o, ["chat_id"], "$.command");`,
		"decodeCreateChat(",
		"decodeRename(",
	} {
		if !strings.Contains(decoders, want) {
			t.Errorf("decoders missing %q, got:\n%s", want, decoders)
		}
	}
}

func TestUnion_PayloadOpenAPI(t *testing.T) {
	r := commandReg()
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	doc := mustGen(t, r.openapiFile)
	// omitempty drops a nil payload, but a payload holding the bytes null is
	// still written as null, so even the response component is nullable.
	if got := prop(t, component(t, doc, "Command.create"), "payload"); got != `{"anyOf":[{"$ref":"#/components/schemas/CreateChat"},{"type":"null"}]}` {
		t.Errorf("create's payload = %s, want a nullable CreateChat $ref", got)
	}
	if got := prop(t, component(t, doc, "Command.rename"), "payload"); got != `{"anyOf":[{"$ref":"#/components/schemas/Rename"},{"type":"null"}]}` {
		t.Errorf("rename's payload = %s, want a nullable Rename $ref", got)
	}
	if strings.Contains(mustJSON(t, component(t, doc, "Command.close")), `"payload"`) {
		t.Errorf("close admits a payload: %s", mustJSON(t, component(t, doc, "Command.close")))
	}
	s := compiled(t, doc, "#/components/schemas/Command")
	for body, ok := range map[string]bool{
		`{"type":"create","payload":{"title":"a"}}`:              true,
		`{"type":"rename","chat_id":"c","payload":{"name":"n"}}`: true,
		`{"type":"close","chat_id":"c"}`:                         true,
		`{"type":"create","payload":{"name":"n"}}`:               false,
		`{"type":"close","payload":{"name":"n"}}`:                false,
	} {
		if err := validates(t, s, body); (err == nil) != ok {
			t.Errorf("%s: validates = %v, want ok %v", body, err, ok)
		}
	}
}

func TestUnion_PayloadTypeReachedOnlyThroughPayload(t *testing.T) {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	u := commandUnion()
	u.PayloadTypes["rename"] = wiregen.TypeRef[unions.Nested]()
	r.Types = []wiregen.Type{wiregen.TypeRef[unions.Rename]()}
	r.Unions = []wiregen.Union{u}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	r.Endpoints = []wiregen.Endpoint{{
		Name: "send", Method: "POST", Path: "/api/command", Kind: wiregen.KindJSON,
		Request: wiregen.TypeRef[unions.Command](),
	}}
	doc := mustGen(t, r.openapiFile)
	// Nested is reached only through the rename payload of a request body, so
	// its request form exists and the request case references it.
	component(t, doc, "NestedInput")
	if got := prop(t, component(t, doc, "CommandInput.rename"), "payload"); got != `{"anyOf":[{"$ref":"#/components/schemas/NestedInput"},{"type":"null"}]}` {
		t.Errorf("the request rename payload = %s, want a nullable NestedInput $ref", got)
	}
	if _, err := r.arbitrariesFile(t.Context()); err != nil {
		t.Fatalf("arbitraries: %v", err)
	}
}

func TestUnion_PayloadRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*wiregen.Union)
		want string
	}{
		{"a payload field that is not a field", func(u *wiregen.Union) { u.Payload = "body" }, `payload "body" is no field of the type`},
		{"a payload field that is not raw JSON", func(u *wiregen.Union) {
			u.Type = wiregen.TypeRef[unions.NotRaw]()
			delete(u.Cases, "rename")
			delete(u.Cases, "close")
			u.Cases["create"] = []string{}
			u.PayloadTypes = map[string]wiregen.Type{"create": wiregen.TypeRef[unions.CreateChat]()}
		}, `payload "payload" must be a json.RawMessage field`},
		{"payload types keyed differently from the cases", func(u *wiregen.Union) { delete(u.PayloadTypes, "close") }, `PayloadTypes keys [create rename] must be exactly the Cases keys [close create rename]`},
		{"a case that lists the payload field", func(u *wiregen.Union) { u.Cases["create"] = []string{"payload"} }, `case "create" lists the payload "payload"`},
		{"payload types without a payload field", func(u *wiregen.Union) { u.Payload = "" }, "sets PayloadTypes without Payload"},
		{"a payload no case types", func(u *wiregen.Union) {
			u.PayloadTypes = map[string]wiregen.Type{"create": {}, "rename": {}, "close": {}}
		}, `no case has a payload type, so payload "payload" can never be sent`},
		{"a payload encoding/json writes in every case", func(u *wiregen.Union) {
			u.Type = wiregen.TypeRef[unions.RequiredPayload]()
			u.Cases = map[string][]string{"create": {}, "close": {}}
			u.PayloadTypes = map[string]wiregen.Type{"create": wiregen.TypeRef[unions.CreateChat](), "close": {}}
		}, `field "payload" is not in every case`},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			r := newTestRegistry()
			u := commandUnion()
			tc.edit(&u)
			r.Unions = []wiregen.Union{u}
			requireGenerateError(t, r, tc.want)
		})
	}
}

// A payload type that is no struct is refused under its role, with no hint
// to register it as an enum, whether or not it is registered as one or
// listed in Types too.
func TestUnion_PayloadTypeNotAStruct(t *testing.T) {
	const role = `union case "create" payload type github.com/cplieger/go/wiregen/v2/testdata/unions.Kind`
	for _, tc := range []struct {
		name  string
		enums []wiregen.EnumRef
		types []wiregen.Type
		want  string
	}{
		{"an unregistered non-struct", nil, nil, role + " is not a struct"},
		{"a non-struct also in Types", nil, []wiregen.Type{wiregen.TypeRef[unions.Kind]()}, role + " is not a struct"},
		{"a registered enum", []wiregen.EnumRef{wiregen.TextEnum[unions.Kind]()}, nil, role + " is an enum; it must be a struct"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			r := newTestRegistry()
			r.Enums, r.Types = tc.enums, tc.types
			u := commandUnion()
			u.PayloadTypes["create"] = wiregen.TypeRef[unions.Kind]()
			r.Unions = []wiregen.Union{u}
			err := r.generate(t.Context(), t.TempDir())
			if err == nil || err.Error() != "wiregen: "+tc.want {
				t.Errorf("Generate() error = %v, want %q", err, "wiregen: "+tc.want)
			}
		})
	}
}

// A payload type with its own MarshalJSON is refused under its role, with
// the one fix a role can take: Mappings cannot stand in for a payload type.
func TestUnion_PayloadTypeWithAMarshaler(t *testing.T) {
	r := newTestRegistry()
	u := commandUnion()
	u.PayloadTypes["create"] = wiregen.TypeRef[unions.Custom]()
	r.Unions = []wiregen.Union{u}
	err := r.generate(t.Context(), t.TempDir())
	want := `github.com/cplieger/go/wiregen/v2/testdata/unions.Custom: is the union case "create" payload type, but implements json.Marshaler`
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "Mappings") {
		t.Errorf("Generate() error = %v, want %q and no Mappings hint", err, want)
	}
}

// A union that reaches itself only through a payload type is depth-capped
// like any recursive type, or its arbitrary would draw without end.
func TestUnion_PayloadRecursionCapped(t *testing.T) {
	r := newTestRegistry()
	u := commandUnion()
	u.PayloadTypes["create"] = wiregen.TypeRef[unions.Batch]()
	r.Types = []wiregen.Type{wiregen.TypeRef[unions.CreateChat]()}
	r.Unions = []wiregen.Union{u}
	out := mustGen(t, r.arbitrariesFile)
	if entry := letrecEntry(t, out, "Command"); !strings.HasPrefix(entry, "fc.oneof(\n    { maxDepth: 5") {
		t.Fatalf("Command arbitrary is not depth-capped:\n%s", entry)
	}
}

// A payload with no omit option, typed in every case, is written as null when
// the json.RawMessage is nil: its response schema is nullable and validates
// what encoding/json writes for the zero value.
func TestUnion_PayloadNilWritesNull(t *testing.T) {
	null := json.RawMessage("null")
	nilRaw := json.RawMessage(nil)
	for _, tc := range []struct {
		name   string
		typ    wiregen.Type
		fields []string
		value  any
	}{
		{"a nil payload with no omit option", wiregen.TypeRef[unions.RequiredPayload](), nil, unions.RequiredPayload{Type: "create"}},
		{"omitempty over the bytes null", wiregen.TypeRef[unions.Command](), []string{"chat_id"}, unions.Command{Type: "create", Payload: null}},
		{"omitzero over the bytes null", wiregen.TypeRef[unions.OmitZeroPayload](), nil, unions.OmitZeroPayload{Type: "create", Payload: null}},
		{"a pointer to a nil payload", wiregen.TypeRef[unions.PtrPayload](), nil, unions.PtrPayload{Type: "create", Payload: &nilRaw}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			r.Unions = []wiregen.Union{{
				Type: tc.typ, Tag: "type",
				Cases:        map[string][]string{"create": tc.fields},
				Payload:      "payload",
				PayloadTypes: map[string]wiregen.Type{"create": wiregen.TypeRef[unions.CreateChat]()},
			}}
			r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
			doc := mustGen(t, r.openapiFile)
			body, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"type":"create","payload":null}` {
				t.Fatalf("encoding/json wrote %s", body)
			}
			if err := validates(t, compiled(t, doc, "#/components/schemas/"+tc.typ.Name), string(body)); err != nil {
				t.Errorf("the encoding fails its response schema: %v", err)
			}
			if types := mustGen(t, r.typesFile); !strings.Contains(types, `; payload?: CreateChat }`) {
				t.Errorf("a payload that can be null is not optional:\n%s", types)
			}
		})
	}
}
