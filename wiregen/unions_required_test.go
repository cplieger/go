package wiregen_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/unions"
)

// checkedUnion is the Checked fixture's Union: gone and updated require
// stored, unchanged leaves it optional, and error requires its own fields.
func checkedUnion() wiregen.Union {
	return wiregen.Union{
		Type: wiregen.TypeRef[unions.Checked](),
		Tag:  "state",
		Cases: map[string][]string{
			"error":     {"failure", "detail"},
			"gone":      {"stored"},
			"unchanged": {"stored"},
			"updated":   {"stored"},
		},
		Required: map[string][]string{
			"error":   {"failure", "detail"},
			"gone":    {"stored"},
			"updated": {"stored"},
		},
	}
}

func checkedReg(opts ...wiregen.Option) *testRegistry {
	r := newTestRegistry(opts...)
	r.Types = []wiregen.Type{wiregen.TypeRef[unions.Stored]()}
	r.Unions = []wiregen.Union{checkedUnion()}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	return r
}

// requiredPayloadCommand is commandUnion with create's payload required.
func requiredPayloadCommand() wiregen.Union {
	u := commandUnion()
	u.Required = map[string][]string{"create": {"payload"}}
	return u
}

// caseBlock returns the body of the decoder's case for value.
func caseBlock(t *testing.T, decoders, value string) string {
	t.Helper()
	_, body, ok := strings.Cut(decoders, "    case \""+value+"\": {\n")
	if !ok {
		t.Fatalf("no case %q in:\n%s", value, decoders)
	}
	body, _, ok = strings.Cut(body, "\n    }\n")
	if !ok {
		t.Fatalf("case %q has no end in:\n%s", value, decoders)
	}
	return body
}

// requiredOf returns an object schema's required list, sorted.
func requiredOf(t *testing.T, schema map[string]any) []string {
	t.Helper()
	var out []string
	list, _ := schema["required"].([]any)
	for _, v := range list {
		out = append(out, v.(string))
	}
	slices.Sort(out)
	return out
}

func TestUnionRequired_TypeAlias(t *testing.T) {
	out := mustGen(t, checkedReg().typesFile)
	want := "export type Checked =\n" +
		"  | {\n" +
		"      state: \"error\";\n" +
		"      failure: string;\n" +
		"      detail: unknown;\n" +
		"    }\n" +
		"  | {\n" +
		"      state: \"gone\";\n" +
		"      stored: Stored;\n" +
		"    }\n" +
		"  | {\n" +
		"      state: \"unchanged\";\n" +
		"      stored?: Stored;\n" +
		"    }\n" +
		"  | {\n" +
		"      state: \"updated\";\n" +
		"      stored: Stored;\n" +
		"    };"
	if !strings.Contains(out, want) {
		t.Errorf("types missing %q, got:\n%s", want, out)
	}
}

// A required field is read into the case's object, so its decoder refuses
// it absent; the case that does not list it reads it as optional.
func TestUnionRequired_Decoder(t *testing.T) {
	out := mustGen(t, checkedReg().decodersFile)
	for _, value := range []string{"gone", "updated"} {
		t.Run(value+" reads stored as required", func(t *testing.T) {
			body := caseBlock(t, out, value)
			if !strings.Contains(body, `stored: decodeStored(own(o, "stored")),`) || strings.Contains(body, "const $v") {
				t.Errorf("case %s does not require stored:\n%s", value, body)
			}
		})
	}
	t.Run("unchanged reads stored as optional", func(t *testing.T) {
		body := caseBlock(t, out, "unchanged")
		if !strings.Contains(body, `const $v = own(o, "stored");`) || strings.Contains(body, `stored: decodeStored(`) {
			t.Errorf("case unchanged does not leave stored optional:\n%s", body)
		}
	})
	t.Run("error reads failure and detail as required", func(t *testing.T) {
		body := caseBlock(t, out, "error")
		for _, want := range []string{`failure: reqStr(o, "failure", "$.checked"),`, `detail: reqRaw(o, "detail", "$.checked"),`} {
			if !strings.Contains(body, want) {
				t.Errorf("case error missing %q:\n%s", want, body)
			}
		}
	})
}

// The required payload is its case's decoder called on the key, which
// refuses it absent or null; another case's payload stays optional.
func TestUnionRequired_PayloadDecoder(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{requiredPayloadCommand()}
	out := mustGen(t, r.decodersFile)
	if body := caseBlock(t, out, "create"); !strings.Contains(body, `payload: decodeCreateChat(own(o, "payload")),`) {
		t.Errorf("create does not require its payload:\n%s", body)
	}
	if body := caseBlock(t, out, "rename"); !strings.Contains(body, `const $v = own(o, "payload");`) {
		t.Errorf("rename does not leave its payload optional:\n%s", body)
	}
	types := mustGen(t, r.typesFile)
	for _, want := range []string{"      payload: CreateChat;\n", "      payload?: Rename;\n"} {
		if !strings.Contains(types, want) {
			t.Errorf("types missing %q:\n%s", want, types)
		}
	}
}

func TestUnionRequired_OpenAPI(t *testing.T) {
	doc := mustGen(t, checkedReg().openapiFile)
	for name, want := range map[string][]string{
		"Checked.error":     {"detail", "failure", "state"},
		"Checked.gone":      {"state", "stored"},
		"Checked.unchanged": {"state"},
		"Checked.updated":   {"state", "stored"},
	} {
		if got := requiredOf(t, component(t, doc, name)); !slices.Equal(got, want) {
			t.Errorf("%s required = %v, want %v", name, got, want)
		}
	}
	if got := prop(t, component(t, doc, "Checked.gone"), "stored"); got != `{"$ref":"#/components/schemas/Stored"}` {
		t.Errorf("gone's stored = %s, want a Stored $ref with no null", got)
	}
	s := compiled(t, doc, "#/components/schemas/Checked")
	for body, ok := range map[string]bool{
		`{"state":"gone","stored":{"path":"p"}}`:          true,
		`{"state":"gone"}`:                                false,
		`{"state":"updated","stored":null}`:               false,
		`{"state":"unchanged"}`:                           true,
		`{"state":"error","failure":"f","detail":null}`:   true,
		`{"state":"error","failure":"f"}`:                 false,
		`{"state":"error","detail":1}`:                    false,
		`{"state":"unchanged","stored":{"path":"p"}}`:     true,
		`{"state":"updated","stored":{"path":"p"},"x":1}`: false,
	} {
		if err := validates(t, s, body); (err == nil) != ok {
			t.Errorf("%s: validates = %v, want ok %v", body, err, ok)
		}
	}
}

// In a request body the required field is required and not null too, while
// the case that does not list it admits null, which decodes to nil.
func TestUnionRequired_OpenAPIRequest(t *testing.T) {
	r := checkedReg(wiregen.WithTransportImport("./api-client.js"))
	r.Endpoints = []wiregen.Endpoint{{
		Name: "check", Method: "POST", Path: "/api/check", Kind: wiregen.KindJSON,
		Request: wiregen.TypeRef[unions.Checked](),
	}}
	doc := mustGen(t, r.openapiFile)
	if got := requiredOf(t, component(t, doc, "CheckedInput.gone")); !slices.Equal(got, []string{"state", "stored"}) {
		t.Errorf("CheckedInput.gone required = %v, want [state stored]", got)
	}
	if got := prop(t, component(t, doc, "CheckedInput.gone"), "stored"); got != `{"$ref":"#/components/schemas/Stored"}` {
		t.Errorf("the request gone's stored = %s, want a Stored $ref with no null", got)
	}
	if got := prop(t, component(t, doc, "CheckedInput.unchanged"), "stored"); got != `{"anyOf":[{"$ref":"#/components/schemas/Stored"},{"type":"null"}]}` {
		t.Errorf("the request unchanged's stored = %s, want a nullable Stored $ref", got)
	}
}

func TestUnionRequired_PayloadOpenAPI(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{requiredPayloadCommand()}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	doc := mustGen(t, r.openapiFile)
	if got := requiredOf(t, component(t, doc, "Command.create")); !slices.Equal(got, []string{"payload", "type"}) {
		t.Errorf("Command.create required = %v, want [payload type]", got)
	}
	if got := prop(t, component(t, doc, "Command.create"), "payload"); got != `{"$ref":"#/components/schemas/CreateChat"}` {
		t.Errorf("create's payload = %s, want a CreateChat $ref with no null", got)
	}
	if got := prop(t, component(t, doc, "Command.rename"), "payload"); got != `{"anyOf":[{"$ref":"#/components/schemas/Rename"},{"type":"null"}]}` {
		t.Errorf("rename's payload = %s, want a nullable Rename $ref", got)
	}
}

// The arbitrary draws a required field in every value of its case.
func TestUnionRequired_Arbitrary(t *testing.T) {
	out := mustGen(t, checkedReg().arbitrariesFile)
	entry := letrecEntry(t, out, "Checked")
	for _, want := range []string{
		`{ requiredKeys: ["state", "failure", "detail"] }`,
		`{ requiredKeys: ["state", "stored"] }`,
		`{ requiredKeys: ["state"] }`,
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("Checked arbitrary missing %q:\n%s", want, entry)
		}
	}
}

// fast-check's oneof draws only its first arbitrary once maxDepth is reached
// (https://fast-check.dev/docs/core-blocks/arbitraries/combiners/any/#oneof),
// so a capped union's first branch must be a case that requires nothing
// leading back to it, or a draw at the cap never ends.
func TestUnionRequired_RecursiveArbitraryEnds(t *testing.T) {
	r := newTestRegistry()
	r.Unions = []wiregen.Union{{
		Type: wiregen.TypeRef[unions.Chain](), Tag: "type",
		Cases:    map[string][]string{"branch": {"next"}, "leaf": {"value"}},
		Required: map[string][]string{"branch": {"next"}},
	}}
	entry := letrecEntry(t, mustGen(t, r.arbitrariesFile), "Chain")
	_, branches, ok := strings.Cut(entry, "fc.oneof(\n    { maxDepth: 5")
	if !ok {
		t.Fatalf("Chain arbitrary is not depth-capped:\n%s", entry)
	}
	leaf, branch := strings.Index(branches, `type: fc.constant("leaf" as const)`), strings.Index(branches, `type: fc.constant("branch" as const)`)
	if leaf < 0 || branch < 0 || leaf > branch {
		t.Errorf("the capped Chain's first branch is not the leaf case:\n%s", entry)
	}
	if !strings.Contains(entry, `{ requiredKeys: ["type", "next"] }`) {
		t.Errorf("branch does not require next:\n%s", entry)
	}
}

func TestUnionRequired_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    func() wiregen.Union
		edit func(*wiregen.Union)
		want string
	}{
		{"a key that is no case", checkedUnion, func(u *wiregen.Union) { u.Required["missing"] = []string{"stored"} }, `Required key "missing" is no case`},
		{"another case's field", checkedUnion, func(u *wiregen.Union) { u.Required["gone"] = []string{"failure"} }, `case "gone" requires "failure", which the case does not carry`},
		{"no field of the type", checkedUnion, func(u *wiregen.Union) { u.Required["gone"] = []string{"nope"} }, `case "gone" requires "nope", which the case does not carry`},
		{"the tag", checkedUnion, func(u *wiregen.Union) { u.Required["gone"] = []string{"state", "stored"} }, `case "gone" requires "state", which every case already requires`},
		{"a field every case already requires", shapeUnion, func(u *wiregen.Union) { u.Required = map[string][]string{"circle": {"tags"}} }, `case "circle" requires "tags", which every case already requires`},
		{"a field twice", checkedUnion, func(u *wiregen.Union) { u.Required["gone"] = []string{"stored", "stored"} }, `case "gone" requires "stored" twice`},
		{"a payload in a case with none", commandUnion, func(u *wiregen.Union) { u.Required = map[string][]string{"close": {"payload"}} }, `case "close" requires "payload", which the case does not carry`},
		{"every case requiring its way back", func() wiregen.Union {
			return wiregen.Union{Type: wiregen.TypeRef[unions.Loop](), Tag: "type"}
		}, func(u *wiregen.Union) {
			u.Cases = map[string][]string{"a": {"link"}, "b": {"link"}}
			u.Required = map[string][]string{"a": {"link"}, "b": {"link"}}
		}, "every case requires a field no finite value fills"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			r := newTestRegistry()
			r.Types = []wiregen.Type{wiregen.TypeRef[unions.Stored](), wiregen.TypeRef[unions.Link]()}
			u := tc.u()
			tc.edit(&u)
			r.Unions = []wiregen.Union{u}
			requireGenerateError(t, r, tc.want)
		})
	}
}

// A union with one case that requires nothing leading back to it has a
// finite value, so it is generated.
func TestUnionRequired_OneEndingCaseSuffices(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[unions.Link]()}
	r.Unions = []wiregen.Union{{
		Type: wiregen.TypeRef[unions.Loop](), Tag: "type",
		Cases:    map[string][]string{"a": {"link"}, "b": {"link"}},
		Required: map[string][]string{"a": {"link"}},
	}}
	if err := r.generate(t.Context(), t.TempDir()); err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
}

// A required field still crosses as encoding/json writes it: the Go side
// is unchanged, so a value the server builds for the case validates.
func TestUnionRequired_GoEncodingValidates(t *testing.T) {
	doc := mustGen(t, checkedReg().openapiFile)
	s := compiled(t, doc, "#/components/schemas/Checked")
	for _, v := range []unions.Checked{
		{State: "gone", Stored: &unions.Stored{Path: "p"}},
		{State: "unchanged"},
		{State: "error", Failure: "f", Detail: json.RawMessage(`{"code":1}`)},
	} {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := validates(t, s, string(body)); err != nil {
			t.Errorf("%s fails its response schema: %v", body, err)
		}
	}
}

// TestGolden_UnionRequired pins the whole types, decoders and OpenAPI files
// of a union with required fields.
func TestGolden_UnionRequired(t *testing.T) {
	r := checkedReg()
	goldenCompare(t, "testdata/golden/union-required.types.gen.ts", mustGen(t, r.typesFile))
	goldenCompare(t, "testdata/golden/union-required.decoders.gen.ts", mustGen(t, r.decodersFile))
	goldenCompare(t, "testdata/golden/union-required.openapi.json", mustGen(t, r.openapiFile))
}
