package wiregen_test

import (
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/maps"
)

// mapReg registers the named map fixtures as an endpoint's request and
// response bodies.
func mapReg() *testRegistry {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.Ranked](), wiregen.TypeRef[maps.Holder](), wiregen.TypeRef[maps.Item]()}
	r.Endpoints = []wiregen.Endpoint{{
		Name: "patchSettings", Method: "PATCH", Path: "/api/settings", Kind: wiregen.KindJSON,
		Request: wiregen.TypeRef[maps.Settings](), Response: wiregen.TypeRef[maps.Labels](),
	}}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	return r
}

// A registered named map type is a type alias of its record shape, with the
// type's doc.
func TestMapType_TypesFile(t *testing.T) {
	out := mustGen(t, mapReg().typesFile)
	for _, want := range []string{
		"/** Settings is an open settings patch: any key, each value raw JSON. */\nexport type Settings = { [key: string]: unknown };",
		"export type Labels = { [key: string]: Item };",
		"export type Ranked = { [key: string]: string };",
		// A field of a registered map type references it by name.
		"  labels: Labels;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("types file missing %q, got:\n%s", want, out)
		}
	}
}

// The decoder of a named map type refuses a non-object, decodes each value
// with the value's decoder, and reads null as empty, as encoding/json writes a
// nil map.
func TestMapType_Decoder(t *testing.T) {
	out := mustGen(t, mapReg().decodersFile)
	for _, want := range []string{
		`export const decodeLabels: Decoder<Labels> = (v) => v === null ? {} : decodeRecord(v, decodeItem, "$.labels");`,
		`export const decodeSettings: Decoder<Settings> = (v) => v === null ? {} : decodeRecord(v, (v) => v as unknown, "$.settings");`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("decoders file missing %q, got:\n%s", want, out)
		}
	}
}

// Each named map type is a non-null object component of the value's schema;
// null is added on a body, and on a field or element encoding/json can write
// as null, so an omitempty field is a plain ref.
func TestMapType_OpenAPI(t *testing.T) {
	doc := mustGen(t, mapReg().openapiFile)
	if got := mustJSON(t, component(t, doc, "Labels")); got != `{"additionalProperties":{"$ref":"#/components/schemas/Item"},"type":"object"}` {
		t.Errorf("Labels component = %s", got)
	}
	// Raw values read alike both ways, so the request body shares one form.
	if got := mustJSON(t, component(t, doc, "Settings")); got != `{"additionalProperties":{},"type":"object"}` {
		t.Errorf("Settings component = %s, want an open object", got)
	}
	// A body adds the null a nil map is written as; so does a field that is
	// always written, and an omitempty one, never null, does not.
	op := operation(t, doc, "patch", "/api/settings")
	if got := mustJSON(t, op["responses"]); !strings.Contains(got, `"schema":{"anyOf":[{"$ref":"#/components/schemas/Labels"},{"type":"null"}]}`) {
		t.Errorf("response = %s, want a nullable Labels body", got)
	}
	if got := mustJSON(t, op["requestBody"]); !strings.Contains(got, `"schema":{"anyOf":[{"$ref":"#/components/schemas/Settings"},{"type":"null"}]}`) {
		t.Errorf("request body = %s, want a nullable Settings body", got)
	}
	holder := component(t, doc, "Holder")
	if got := prop(t, holder, "labels"); got != `{"anyOf":[{"$ref":"#/components/schemas/Labels"},{"type":"null"}]}` {
		t.Errorf("Holder.labels = %s, want a nullable ref", got)
	}
	if got := prop(t, holder, "extra"); got != `{"$ref":"#/components/schemas/Labels"}` {
		t.Errorf("Holder.extra = %s, want a plain ref", got)
	}
	for body, ok := range map[string]bool{
		`{"a":{"name":"x"}}`: true,
		`{}`:                 true,
		`null`:               false,
		`{"a":{"nom":"x"}}`:  false,
		`[]`:                 false,
	} {
		if err := validates(t, compiled(t, doc, "#/components/schemas/Labels"), body); (err == nil) != ok {
			t.Errorf("Labels %s: validates = %v, want ok %v", body, err, ok)
		}
	}
}

// A named map type has an arbitrary and a row in the arbitrary table, like a
// struct.
func TestMapType_Arbitraries(t *testing.T) {
	out := mustGen(t, mapReg().arbitrariesFile)
	for _, want := range []string{
		"export const arbLabels: fc.Arbitrary<Labels>",
		"  Labels: arbLabels,",
		"  Settings: arbSettings,",
		// It draws keys and values, not only the empty map.
		`  Labels: fc.dictionary(fc.string(), tie("Item"), `,
		// A field of a registered map ties to the map's own arbitrary.
		`labels: tie("Labels")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("arbitraries file missing %q, got:\n%s", want, out)
		}
	}
}

// A registered map that reaches itself, directly, through another map or
// through a struct, is referenced by name, so it generates, and its arbitrary
// is depth-capped with the empty map to end it.
func TestMapType_Recursion(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{
		wiregen.TypeRef[maps.Tree](), wiregen.TypeRef[maps.Left](), wiregen.TypeRef[maps.Right](),
		wiregen.TypeRef[maps.Node](), wiregen.TypeRef[maps.Kids](),
	}
	types := mustGen(t, r.typesFile)
	for _, want := range []string{
		"export type Tree = { [key: string]: Tree };",
		"export type Left = { [key: string]: Right };",
		"  kids: Kids;",
		"export type Kids = { [key: string]: Node };",
	} {
		if !strings.Contains(types, want) {
			t.Errorf("types file missing %q, got:\n%s", want, types)
		}
	}
	if dec := mustGen(t, r.decodersFile); !strings.Contains(dec, `export const decodeTree: Decoder<Tree> = (v) => v === null ? {} : decodeRecord(v, decodeTree, "$.tree");`) {
		t.Errorf("Tree decoder does not call itself by name:\n%s", dec)
	}
	arbs := mustGen(t, r.arbitrariesFile)
	if !strings.Contains(arbs, "  Tree: fc.oneof(\n    { maxDepth: 5") || !strings.Contains(arbs, "fc.constant({}),") {
		t.Errorf("Tree arbitrary is not capped by the empty map:\n%s", arbs)
	}
}

// An unregistered named map that reaches itself cannot be planned inline, so
// the field is refused with the fix.
func TestMapType_UnregisteredSelfMapIsRefused(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.HasLoop]()}
	requireGenerateError(t, r, "github.com/cplieger/go/wiregen/v2/testdata/maps.Loop reaches itself; register it, so it is referenced by name")
}

// A map body's element path names the body itself, with no trailing dot.
func TestMapType_ElementPath(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.Lists]()}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, `"$.lists")`) || strings.Contains(dec, `"$.lists."`) {
		t.Errorf("Lists decoder path is not $.lists:\n%s", dec)
	}
}

// An error envelope that is a named map is the schema of each declared error
// status, and has a decoder.
func TestMapType_ErrorEnvelope(t *testing.T) {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.Item]()}
	r.Endpoints = []wiregen.Endpoint{{
		Name: "item", Method: "GET", Path: "/api/item", Kind: wiregen.KindJSON,
		Response: wiregen.TypeRef[maps.Item](), Errors: []wiregen.ErrorRef{{Code: "item_unknown", Status: 404}},
	}}
	r.ErrorEnvelope = wiregen.TypeRef[maps.Settings]()
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	doc := mustGen(t, r.openapiFile)
	op := operation(t, doc, "get", "/api/item")
	if got := mustJSON(t, op["responses"]); !strings.Contains(got, `"404":{"content":{"application/json":{"schema":{"anyOf":[{"$ref":"#/components/schemas/Settings"},{"type":"null"}]}}}`) {
		t.Errorf("404 response = %s, want the Settings envelope", got)
	}
	if dec := mustGen(t, r.decodersFile); !strings.Contains(dec, "export const decodeSettings: Decoder<Settings>") {
		t.Errorf("no decoder for the map envelope:\n%s", dec)
	}
}

// A role that needs a struct's fields refuses a named map type, and a map type
// with its own marshaler is refused like a struct with one.
func TestMapType_Refusals(t *testing.T) {
	const pkg = "github.com/cplieger/go/wiregen/v2/testdata/maps"
	for _, tc := range []struct {
		name string
		edit func(*testRegistry)
		want string
	}{
		{"a union over a map", func(r *testRegistry) {
			r.Unions = []wiregen.Union{{Type: wiregen.TypeRef[maps.Union](), Tag: "type", Cases: map[string][]string{"a": {}}}}
		}, "union " + pkg + ".Union: a union type is a struct"},
		{"a map as path parameters", func(r *testRegistry) {
			r.Endpoints[0].Path, r.Endpoints[0].Params = "/api/settings/{id}", wiregen.TypeRef[maps.Ranked]()
		}, "endpoint patchSettings Params type Ranked is a map; URL arguments are a plain struct's fields"},
		{"a map as a query", func(r *testRegistry) {
			r.Endpoints[0].Query = wiregen.TypeRef[maps.Ranked]()
		}, "endpoint patchSettings Query type Ranked is a map; URL arguments are a plain struct's fields"},
		{"a map with its own marshaler", func(r *testRegistry) {
			r.Endpoints[0].Response = wiregen.TypeRef[maps.Custom]()
		}, pkg + ".Custom: is the endpoint patchSettings Response type, but implements json.Marshaler"},
		{"an alias of a map", func(r *testRegistry) {
			r.Types = append(r.Types, wiregen.Type{PkgPath: pkg, Name: "Alias"})
		}, "registered type " + pkg + ".Alias is an alias; register the type it stands for"},
		{"an alias of a struct", func(r *testRegistry) {
			r.Types = append(r.Types, wiregen.Type{PkgPath: pkg, Name: "ItemAlias"})
		}, "registered type " + pkg + ".ItemAlias is an alias; register the type it stands for"},
		{"a Count bound on a map", func(r *testRegistry) {
			r.Types = append(r.Types, wiregen.TypeRef[maps.Counted]())
		}, pkg + ".Counted: declares Count, which bounds a slice type"},
		{"a key a request cannot decode", func(r *testRegistry) {
			r.Endpoints[0].Request = wiregen.TypeRef[maps.Keyed]()
		}, pkg + ".Keyed: map key " + pkg + ".Key writes through MarshalText but has no UnmarshalText, so encoding/json reads it back as a decimal integer, which MarshalText need not write, and a request carries it"},
		{"a string key read through UnmarshalText", func(r *testRegistry) {
			r.Endpoints[0].Request = wiregen.TypeRef[maps.ByPrefixed]()
		}, pkg + ".ByPrefixed: map key " + pkg + ".Prefixed is written as its string but read through UnmarshalText, which reads MarshalText's form, and a request carries it"},
		{"a field read only from a JSON string", func(r *testRegistry) {
			r.Endpoints[0].Request = wiregen.TypeRef[maps.HasLevel]()
		}, pkg + ".HasLevel.Level: " + pkg + ".Level has a pointer UnmarshalText, which encoding/json calls only for a JSON string, so it cannot decode the value it writes, and a request carries it"},
		{"a map body read only from a JSON string", func(r *testRegistry) {
			r.Endpoints[0].Request = wiregen.TypeRef[maps.TextMap]()
		}, pkg + ".TextMap: " + pkg + ".TextMap has a pointer UnmarshalText, which encoding/json calls only for a JSON string, so it cannot decode the value it writes, and a request carries it"},
		{"a decimal key read through a pointer UnmarshalText", func(r *testRegistry) {
			r.Endpoints[0].Request = wiregen.TypeRef[maps.ByPtrID]()
		}, pkg + ".ByPtrID: map key " + pkg + ".PtrID is written as a decimal integer but read through UnmarshalText, which need not accept it, and a request carries it"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			r := mapReg()
			tc.edit(r)
			requireGenerateError(t, r, tc.want)
		})
	}
}

// A named map serves every role that names a whole body: an event's data, a
// union case's payload and the error envelope.
func TestMapType_BodyRoles(t *testing.T) {
	r := mapReg()
	r.Events = []wiregen.Event{{Name: "labels", Data: wiregen.TypeRef[maps.Labels]()}}
	r.ErrorEnvelope = wiregen.TypeRef[maps.Settings]()
	u := commandUnion()
	u.PayloadTypes["create"] = wiregen.TypeRef[maps.Labels]()
	r.Unions = []wiregen.Union{u}
	if events := mustGen(t, r.eventsFile); !strings.Contains(events, "labels: decodeLabels,") {
		t.Errorf("events module does not decode the map's data:\n%s", events)
	}
	if types := mustGen(t, r.typesFile); !strings.Contains(types, `{ type: "create"; payload?: Labels }`) {
		t.Errorf("types file does not type the payload as the map:\n%s", types)
	}
	doc := mustGen(t, r.openapiFile)
	if got := prop(t, component(t, doc, "Command.create"), "payload"); !strings.Contains(got, `"$ref":"#/components/schemas/Labels"`) {
		t.Errorf("create's payload = %s, want the Labels $ref", got)
	}
}

// A named map whose value reads differently by direction has a request form
// whose values are the value type's request form.
func TestMapType_RequestForm(t *testing.T) {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.Opt]()}
	r.Endpoints = []wiregen.Endpoint{{
		Name: "opts", Method: "PUT", Path: "/api/opts", Kind: wiregen.KindJSON,
		Request: wiregen.TypeRef[maps.Opts](), Response: wiregen.TypeRef[maps.Opts](),
	}}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	doc := mustGen(t, r.openapiFile)
	if got := mustJSON(t, component(t, doc, "OptsInput")); !strings.Contains(got, `"additionalProperties":{"$ref":"#/components/schemas/OptInput"}`) {
		t.Errorf("OptsInput = %s, want values of OptInput", got)
	}
	component(t, doc, "Opt")
}

// An integer key is decimal text on the wire: the schema says so, and the
// arbitrary draws keys encoding/json can read back.
func TestMapType_IntegerKeys(t *testing.T) {
	r := mapReg()
	r.opts = append(r.opts, wiregen.WithArbitrariesFile("arbitraries.gen.ts"))
	doc := mustGen(t, r.openapiFile)
	if got := mustJSON(t, component(t, doc, "Ranked")); !strings.Contains(got, `"propertyNames":{"pattern":"^-?(0|[1-9][0-9]*)$"}`) {
		t.Errorf("Ranked = %s, want decimal property names", got)
	}
	if arbs := mustGen(t, r.arbitrariesFile); !strings.Contains(arbs, "Ranked: fc.dictionary(fc.integer({ min: -9007199254740991, max: 9007199254740991 }).map(globalThis.String), ") {
		t.Errorf("Ranked arbitrary does not draw integer keys:\n%s", arbs)
	}
}

// A role that needs fields names only a struct; one that takes a whole body
// names a struct or a map.
func TestMapType_RoleShape(t *testing.T) {
	const pkg = "github.com/cplieger/go/wiregen/v2/testdata/maps"
	query := mapReg()
	query.Endpoints[0].Query = wiregen.TypeRef[maps.Word]()
	event := mapReg()
	event.Events = []wiregen.Event{{Name: "w", Data: wiregen.TypeRef[maps.Word]()}}
	for name, tc := range map[string]struct {
		r    *testRegistry
		want string
	}{
		"Query":      {query, "wiregen: endpoint patchSettings Query type " + pkg + ".Word is not a struct"},
		"event data": {event, `wiregen: event "w" data type ` + pkg + ".Word is not a struct or a map"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.r.generate(t.Context(), t.TempDir()); err == nil || err.Error() != tc.want {
				t.Errorf("Generate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

// Nested instances of one generic map or slice are two types, not a cycle;
// an instance that reaches itself, or a slice that does, is refused with the
// fix its shape takes.
func TestMapType_GenericsAndSelfSlices(t *testing.T) {
	const pkg = "github.com/cplieger/go/wiregen/v2/testdata/maps"
	ok := newTestRegistry()
	ok.Types = []wiregen.Type{wiregen.TypeRef[maps.Nested]()}
	types := mustGen(t, ok.typesFile)
	for _, want := range []string{"maps: { [key: string]: { [key: string]: number } };", "slices: string[][];"} {
		if !strings.Contains(types, want) {
			t.Errorf("types file missing %q:\n%s", want, types)
		}
	}
	loop := newTestRegistry()
	loop.Types = []wiregen.Type{wiregen.TypeRef[maps.HasGLoop]()}
	requireGenerateError(t, loop, pkg+".GLoop reaches itself; register it, so it is referenced by name")
	sl := newTestRegistry()
	sl.Types = []wiregen.Type{wiregen.TypeRef[maps.HasSL]()}
	requireGenerateError(t, sl, pkg+".SL reaches itself, which no inline shape can hold; route the cycle through a registered struct or named map")
}

// A bounded map value is checked at the body's path, with no trailing dot.
func TestMapType_BoundedValuePath(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[maps.Slugs]()}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, `"$.slugs"`) || strings.Contains(dec, `"$.slugs."`) {
		t.Errorf("Slugs decoder does not check values at $.slugs:\n%s", dec)
	}
}

// An array response of named maps has nullable items, as a nil map element is
// written as null, and an event of a named map is nullable; a response-only
// map keyed through UnmarshalText generates.
func TestMapType_NullByUse(t *testing.T) {
	r := mapReg()
	r.Endpoints = append(r.Endpoints, wiregen.Endpoint{
		Name: "listLabels", Method: "GET", Path: "/api/labels", Kind: wiregen.KindJSON,
		Response: wiregen.TypeRef[maps.Labels](), Array: true,
	}, wiregen.Endpoint{
		Name: "prefixed", Method: "GET", Path: "/api/prefixed", Kind: wiregen.KindJSON,
		Response: wiregen.TypeRef[maps.ByPrefixed](),
	}, wiregen.Endpoint{
		Name: "stream", Method: "GET", Path: "/api/stream", Kind: wiregen.KindEventStream,
	})
	r.Events = []wiregen.Event{{Name: "labels", Data: wiregen.TypeRef[maps.Labels]()}}
	doc := mustGen(t, r.openapiFile)
	if got := mustJSON(t, operation(t, doc, "get", "/api/labels")["responses"]); !strings.Contains(got, `"schema":{"items":{"anyOf":[{"$ref":"#/components/schemas/Labels"},{"type":"null"}]},"type":"array"}`) {
		t.Errorf("array response = %s, want nullable Labels items", got)
	}
	if got := mustJSON(t, operation(t, doc, "get", "/api/stream")["responses"]); !strings.Contains(got, `"labels":{"anyOf":[{"$ref":"#/components/schemas/Labels"},{"type":"null"}]}`) {
		t.Errorf("event stream = %s, want a nullable labels event", got)
	}
}
