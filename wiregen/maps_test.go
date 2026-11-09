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
		// A field of a registered map type keeps the inline map shape and
		// its null rule.
		"  labels: { [key: string]: Item };",
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

// Each named map type is an object component whose values have the value's
// schema, nullable in both directions because a nil map is null on the wire.
func TestMapType_OpenAPI(t *testing.T) {
	doc := mustGen(t, mapReg().openapiFile)
	if got := mustJSON(t, component(t, doc, "Labels")); got != `{"additionalProperties":{"$ref":"#/components/schemas/Item"},"type":["object","null"]}` {
		t.Errorf("Labels component = %s", got)
	}
	// Raw values read alike both ways, so the request body shares one form.
	if got := mustJSON(t, component(t, doc, "Settings")); got != `{"additionalProperties":{},"type":["object","null"]}` {
		t.Errorf("Settings component = %s, want an open object that may be null", got)
	}
	for body, ok := range map[string]bool{
		`{"a":{"name":"x"}}`: true,
		`{}`:                 true,
		`null`:               true,
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
	for _, want := range []string{"export const arbLabels: fc.Arbitrary<Labels>", "  Labels: arbLabels,", "  Settings: arbSettings,"} {
		if !strings.Contains(out, want) {
			t.Errorf("arbitraries file missing %q, got:\n%s", want, out)
		}
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
