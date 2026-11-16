package wiregen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/generics"
	dotted "github.com/cplieger/go/wiregen/v2/testdata/generics/dotted.v2"
	"github.com/cplieger/go/wiregen/v2/testdata/generics/other"
)

const genericsPkg = "github.com/cplieger/go/wiregen/v2/testdata/generics"

// genericsReg registers instantiations whose type arguments are a registered
// struct, a builtin, a builtin and a struct, another instantiation and the
// instance itself, used again as a field and as an endpoint's answer.
func genericsReg(opts ...wiregen.Option) *testRegistry {
	r := newTestRegistry(append([]wiregen.Option{wiregen.WithTransportImport("./api-client.js")}, opts...)...)
	r.Types = []wiregen.Type{
		wiregen.TypeRef[generics.Peer](),
		wiregen.TypeRef[generics.List[generics.Peer]](),
		wiregen.TypeRef[generics.List[int]](),
		wiregen.TypeRef[generics.List[generics.List[generics.Peer]]](),
		wiregen.TypeRef[generics.Pair[string, generics.Peer]](),
		wiregen.TypeRef[generics.Tree[int]](),
		wiregen.TypeRef[generics.Holder](),
	}
	r.Endpoints = []wiregen.Endpoint{{
		Name: "listPeers", Method: "GET", Path: "/api/peers", Kind: wiregen.KindJSON,
		Response: wiregen.TypeRef[generics.List[generics.Peer]](),
	}}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	return r
}

func TestGenerics_TypesNameEachInstance(t *testing.T) {
	out := mustGen(t, genericsReg().typesFile)
	cases := []struct {
		iface string
		want  []string
	}{
		{"List_Peer", []string{"items: Peer[];", "next?: string;"}},
		{"List_int", []string{"items: number[];", "next?: string;"}},
		{"List_List_Peer", []string{"items: List_Peer[];"}},
		{"Pair_string_Peer", []string{"key: string;", "val: Peer;"}},
		{"Tree_int", []string{"kids: Tree_int[];", "value: number;"}},
		{"Holder", []string{"peers: List_Peer;", "counts: List_int;"}},
	}
	for _, tc := range cases {
		t.Run(tc.iface, func(t *testing.T) {
			body := ifaceBody(out, tc.iface)
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("interface %s missing %q, got:\n%s", tc.iface, want, out)
				}
			}
		})
	}
	if got := strings.Count(out, "export interface List_Peer {"); got != 1 {
		t.Errorf("List[Peer] is registered, a field and a type argument, want one interface, got %d:\n%s", got, out)
	}
}

func TestGenerics_DecodersNameEachInstance(t *testing.T) {
	out := mustGen(t, genericsReg().decodersFile)
	for _, want := range []string{
		"export const decodeList_Peer: Decoder<List_Peer>",
		"export const decodeList_List_Peer: Decoder<List_List_Peer>",
		"export const decodePair_string_Peer: Decoder<Pair_string_Peer>",
		"export const decodeTree_int: Decoder<Tree_int>",
		`peers: decodeList_Peer(reqRaw(o, "peers", "$.holder")),`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("decoders missing %q, got:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "export const decodeList_Peer:"); got != 1 {
		t.Errorf("want one decodeList_Peer, got %d:\n%s", got, out)
	}
}

// The endpoint's answer references the instance's component, and a value
// encoding/json writes for the Go instantiation validates against it.
func TestGenerics_OpenAPIComponentPerInstance(t *testing.T) {
	doc := mustGen(t, genericsReg().openapiFile)
	var d struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema map[string]any `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, doc)
	}
	for _, name := range []string{"List_Peer", "List_int", "List_List_Peer", "Pair_string_Peer", "Tree_int", "Holder"} {
		if _, ok := d.Components.Schemas[name]; !ok {
			t.Errorf("components.schemas has no %s:\n%s", name, doc)
		}
	}
	ref := d.Paths["/api/peers"]["get"].Responses["200"].Content["application/json"].Schema["$ref"]
	if ref != "#/components/schemas/List_Peer" {
		t.Errorf("listPeers 200 $ref = %v, want #/components/schemas/List_Peer", ref)
	}
	s := compiled(t, doc, "#/components/schemas/List_Peer")
	body := mustJSON(t, generics.List[generics.Peer]{Next: "c", Items: []generics.Peer{{Name: "a", ID: 1}}})
	if err := validates(t, s, body); err != nil {
		t.Errorf("%s fails List_Peer: %v", body, err)
	}
	if err := validates(t, s, `{"items":[{"name":1,"id":1}]}`); err == nil {
		t.Error("an item with a numeric name validates against List_Peer")
	}
}

// An extra success's instantiation decodes and is documented under its
// derived name, leaving the caller's registry as written.
func TestGenerics_ExtraSuccessInstance(t *testing.T) {
	listPeer := wiregen.TypeRef[generics.List[generics.Peer]]()
	r := genericsReg()
	r.Endpoints[0].ExtraSuccesses = []wiregen.Success{{Status: 206, Response: listPeer}}
	if out := mustGen(t, r.clientFile); !strings.Contains(out, "      case 206:\n        return decodeList_Peer(v);\n") {
		t.Errorf("client missing the 206 case decoding List_Peer:\n%s", out)
	}
	op := operation(t, mustGen(t, r.openapiFile), "get", "/api/peers")
	want := `{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/List_Peer"}}},"description":"Partial Content"}`
	if got := mustJSON(t, op["responses"].(map[string]any)["206"]); got != want {
		t.Errorf("206 = %s\nwant %s", got, want)
	}
	if got := r.Endpoints[0].ExtraSuccesses[0].Response; got != listPeer {
		t.Errorf("Generate rewrote the caller's extra success to %+v, want %+v", got, listPeer)
	}
}

func TestGenerics_TSNameOverrideKeysTheDerivedName(t *testing.T) {
	r := genericsReg()
	r.TSNameOverride = map[string]string{"List_Peer": "PeerPage"}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "export interface PeerPage {") {
		t.Errorf("List_Peer should be renamed PeerPage, got:\n%s", out)
	}
	if !strings.Contains(ifaceBody(out, "Holder"), "peers: PeerPage;") {
		t.Errorf("Holder.peers should reference PeerPage, got:\n%s", out)
	}
}

// TestGolden_Generics pins the whole types, decoders and OpenAPI files of
// genericsReg; the tests above pin its names independently.
func TestGolden_Generics(t *testing.T) {
	r := genericsReg()
	goldenCompare(t, "testdata/golden/generics.types.gen.ts", mustGen(t, r.typesFile))
	goldenCompare(t, "testdata/golden/generics.decoders.gen.ts", mustGen(t, r.decodersFile))
	goldenCompare(t, "testdata/golden/generics.openapi.json", mustGen(t, r.openapiFile))
}

// embedReg registers a struct embedding an instantiation; its goldens pin the
// embedding form's output.
func embedReg(extra ...wiregen.Type) *testRegistry {
	r := newTestRegistry()
	r.Types = append([]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.Page]()}, extra...)
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	return r
}

func TestGolden_GenericEmbed(t *testing.T) {
	r := embedReg()
	types := mustGen(t, r.typesFile)
	body := ifaceBody(types, "Page")
	for _, want := range []string{"next?: string;", "items: Peer[];", "total: number;"} {
		if !strings.Contains(body, want) {
			t.Errorf("Page should flatten List[Peer], missing %q:\n%s", want, types)
		}
	}
	goldenCompare(t, "testdata/golden/generic-embed.types.gen.ts", types)
	goldenCompare(t, "testdata/golden/generic-embed.decoders.gen.ts", mustGen(t, r.decodersFile))
	goldenCompare(t, "testdata/golden/generic-embed.openapi.json", mustGen(t, r.openapiFile))
}

// Registering the embedded instantiation too leaves the embedding struct
// flattened, never a reference to the instance.
func TestGenerics_EmbedUnchangedBesideTheInstance(t *testing.T) {
	alone := mustGen(t, embedReg().typesFile)
	beside := mustGen(t, embedReg(wiregen.TypeRef[generics.List[generics.Peer]]()).typesFile)
	if got, want := ifaceBody(beside, "Page"), ifaceBody(alone, "Page"); got != want {
		t.Errorf("Page beside List[Peer]:\n%s\nwant:\n%s", got, want)
	}
}

// An instantiation's fields follow the field rules: a pointer to a type
// parameter with no omit option is required and nullable, omitempty keeps it
// optional, and a const tag pins its literal.
func TestGenerics_InstanceFieldsFollowTheFieldRules(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.Slot[generics.Peer]]()}
	body := ifaceBody(mustGen(t, r.typesFile), "Slot_Peer")
	for _, want := range []string{"val: Peer | null;", "opt?: Peer;", `kind: "slot";`} {
		if !strings.Contains(body, want) {
			t.Errorf("Slot_Peer missing %q, got:\n%s", want, body)
		}
	}
	sent := mustJSON(t, generics.Slot[generics.Peer]{Kind: "slot"})
	got := decodeResults(t, r, "decodeSlot_Peer", []string{sent, `{"kind":"slot"}`, `{"val":null,"kind":"other"}`})
	want := []string{
		`{"val":null,"kind":"slot"}`,
		`$.slot_peer.val: expected a JSON value, got undefined`,
		`$.slot_peer.kind: expected one of slot, got "other"`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("body %d decoded to %s, want %s", i, got[i], want[i])
		}
	}
}

// A registered enum is a type argument as a registered struct is.
func TestGenerics_EnumArgument(t *testing.T) {
	r := newTestRegistry()
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[generics.Status]()}
	r.Types = []wiregen.Type{wiregen.TypeRef[generics.List[generics.Status]]()}
	types := mustGen(t, r.typesFile)
	if body := ifaceBody(types, "List_Status"); !strings.Contains(body, "items: Status[];") {
		t.Errorf("List_Status should hold Status items, got:\n%s", types)
	}
	if !strings.Contains(types, `export type Status = "up" | "down";`) {
		t.Errorf("types missing the Status union, got:\n%s", types)
	}
}

// A builtin argument is named as reflect spells it, byte and rune as uint8 and
// int32 whether registered or written as a field, and every integer type is
// one, uintptr included.
func TestGenerics_BuiltinArguments(t *testing.T) {
	t.Run("aliases", func(t *testing.T) {
		r := newTestRegistry()
		r.Types = []wiregen.Type{
			wiregen.TypeRef[generics.List[byte]](), wiregen.TypeRef[generics.List[rune]](), wiregen.TypeRef[generics.Aliased](),
		}
		types := mustGen(t, r.typesFile)
		for _, want := range []string{"export interface List_uint8 {", "export interface List_int32 {"} {
			if !strings.Contains(types, want) {
				t.Errorf("types missing %q, got:\n%s", want, types)
			}
		}
		body := ifaceBody(types, "Aliased")
		for _, want := range []string{"bytes: List_uint8;", "runes: List_int32;"} {
			if !strings.Contains(body, want) {
				t.Errorf("Aliased missing %q, got:\n%s", want, types)
			}
		}
	})
	cases := []struct {
		iface string
		typ   wiregen.Type
	}{
		{"List_uintptr", wiregen.TypeRef[generics.List[uintptr]]()},
		{"List_float32", wiregen.TypeRef[generics.List[float32]]()},
		{"List_bool", wiregen.TypeRef[generics.List[bool]]()},
	}
	for _, tc := range cases {
		t.Run(tc.iface, func(t *testing.T) {
			r := newTestRegistry()
			r.Types = []wiregen.Type{tc.typ}
			types := mustGen(t, r.typesFile)
			if !strings.Contains(types, "export interface "+tc.iface+" {") {
				t.Errorf("types missing interface %s, got:\n%s", tc.iface, types)
			}
		})
	}
}

// An event's data, a union, a union's payload and the ErrorEnvelope each name
// an instantiation as TypeRef spells it, and generate under its derived name,
// leaving the caller's registry as written.
func TestGenerics_EveryRoleNamesAnInstance(t *testing.T) {
	listPeer := wiregen.TypeRef[generics.List[generics.Peer]]()
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[generics.Peer]()}
	r.Events = []wiregen.Event{{Name: "page", Data: listPeer}}
	r.ErrorEnvelope = wiregen.TypeRef[generics.List[int]]()
	r.Unions = []wiregen.Union{
		{
			Type: wiregen.TypeRef[generics.Pair[string, generics.Peer]](), Tag: "key",
			Cases: map[string][]string{"peer": {"val"}},
		},
		{
			Type: wiregen.TypeRef[generics.Command](), Tag: "type",
			Cases: map[string][]string{"page": {}}, Payload: "payload",
			PayloadTypes: map[string]wiregen.Type{"page": listPeer},
		},
	}
	types := mustGen(t, r.typesFile)
	for _, want := range []string{
		"export interface List_Peer {",
		"export interface List_int {",
		"export type Pair_string_Peer =",
		"payload?: List_Peer;",
	} {
		if !strings.Contains(types, want) {
			t.Errorf("types missing %q, got:\n%s", want, types)
		}
	}
	if got := strings.Count(types, "export interface List_Peer {"); got != 1 {
		t.Errorf("List[Peer] is an event's data and a payload, want one interface, got %d:\n%s", got, types)
	}
	if events := mustGen(t, r.eventsFile); !strings.Contains(events, "decodeList_Peer") {
		t.Errorf("the page event should decode with decodeList_Peer, got:\n%s", events)
	}
	if got := r.Unions[1].PayloadTypes["page"]; got != listPeer {
		t.Errorf("Generate rewrote the caller's PayloadTypes to %+v, want %+v", got, listPeer)
	}
}

func TestGenerics_Refusals(t *testing.T) {
	inst := func(name string) string { return genericsPkg + "." + name }
	cases := []struct {
		name  string
		types []wiregen.Type
		wants []string
	}{
		{
			"a derived name taken by a declared type",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List_Peer](), wiregen.TypeRef[generics.List[generics.Peer]]()},
			[]string{inst("List_Peer"), inst("List[" + genericsPkg + ".Peer]")},
		},
		{
			"two instantiations deriving one name",
			[]wiregen.Type{
				wiregen.TypeRef[generics.A](), wiregen.TypeRef[generics.A_B](), wiregen.TypeRef[generics.B_C](), wiregen.TypeRef[generics.C](),
				wiregen.TypeRef[generics.Pair[generics.A_B, generics.C]](), wiregen.TypeRef[generics.Pair[generics.A, generics.B_C]](),
			},
			[]string{inst("Pair[" + genericsPkg + ".A_B," + genericsPkg + ".C]"), inst("Pair[" + genericsPkg + ".A," + genericsPkg + ".B_C]"), "Pair_A_B_C"},
		},
		{
			"a derived name taken by a type of another package",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[other.List_Peer](), wiregen.TypeRef[generics.List[generics.Peer]]()},
			[]string{genericsPkg + "/other.List_Peer", inst("List[" + genericsPkg + ".Peer]")},
		},
		{
			"an unregistered struct argument",
			[]wiregen.Type{wiregen.TypeRef[generics.List[generics.Unregistered]]()},
			[]string{"type argument " + inst("Unregistered"), inst("List[" + genericsPkg + ".Unregistered]")},
		},
		{
			"a pointer argument",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List[*generics.Peer]]()},
			[]string{"type argument *" + inst("Peer")},
		},
		{
			"an unregistered instantiation argument",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List[generics.List[generics.Peer]]]()},
			[]string{"type argument " + inst("List["+genericsPkg+".Peer]")},
		},
		{
			"an unregistered enum argument",
			[]wiregen.Type{wiregen.TypeRef[generics.List[generics.Status]]()},
			[]string{"type argument " + inst("Status"), inst("List[" + genericsPkg + ".Status]")},
		},
		{
			"a complex argument",
			[]wiregen.Type{wiregen.TypeRef[generics.List[complex128]]()},
			[]string{"type argument complex128"},
		},
		{
			"an unregistered instantiation as a field",
			[]wiregen.Type{wiregen.TypeRef[generics.HasUnregistered]()},
			[]string{inst("List[string]") + " is neither registered nor mapped"},
		},
		{
			"a field instance whose argument is another package's namesake",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List[generics.Peer]](), wiregen.TypeRef[generics.HasOtherPeers]()},
			[]string{inst("List["+genericsPkg+"/other.Peer]") + " is neither registered nor mapped"},
		},
		{
			"a field instance whose derived name a declared type holds",
			[]wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List_Peer](), wiregen.TypeRef[generics.HasPeerList]()},
			[]string{inst("List["+genericsPkg+".Peer]") + " is neither registered nor mapped"},
		},
		{
			"the generic declaration itself",
			[]wiregen.Type{{PkgPath: genericsPkg, Name: "List"}},
			[]string{inst("List") + " is generic; register an instantiation of it"},
		},
		{
			"an instantiation of a generic alias",
			[]wiregen.Type{{PkgPath: genericsPkg, Name: "ListAlias[int]"}},
			[]string{inst("ListAlias") + " is an alias; register the type it stands for"},
		},
		{
			"too many type arguments",
			[]wiregen.Type{{PkgPath: genericsPkg, Name: "List[int,string]"}},
			[]string{inst("List") + " has 1 type parameters, not 2"},
		},
		{
			"type arguments to a type that takes none",
			[]wiregen.Type{{PkgPath: genericsPkg, Name: "Unregistered[int]"}},
			[]string{inst("Unregistered") + " has 0 type parameters, not 1"},
		},
		{
			"an argument its constraint refuses",
			[]wiregen.Type{{PkgPath: genericsPkg, Name: "Num[string]"}},
			[]string{"instantiate " + inst("Num[string]"), "string does not satisfy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			r.Types = tc.types
			requireGenerateError(t, r, tc.wants...)
		})
	}
	t.Run("an enum argument whose namesake of another package is registered", func(t *testing.T) {
		r := newTestRegistry()
		r.Enums = []wiregen.EnumRef{wiregen.StringEnum[other.Status]()}
		r.Types = []wiregen.Type{wiregen.TypeRef[generics.List[generics.Status]]()}
		requireGenerateError(t, r, "type argument "+inst("Status"), inst("List["+genericsPkg+".Status]"))
	})
	t.Run("a derived name taken by an enum", func(t *testing.T) {
		r := newTestRegistry()
		r.Enums = []wiregen.EnumRef{wiregen.StringEnum[generics.Pair_int_int]()}
		r.Types = []wiregen.Type{wiregen.TypeRef[generics.Pair[int, int]]()}
		requireGenerateError(t, r, inst("Pair_int_int")+" and "+inst("Pair[int,int]")+" both take the name Pair_int_int")
	})
}

// A collision found after an instantiation is renamed names it by its Go
// spelling, as the app registered it.
func TestGenerics_LateCollisionsNameTheGoType(t *testing.T) {
	listPeer := "type " + genericsPkg + ".List[" + genericsPkg + ".Peer]"
	t.Run("a TS identifier", func(t *testing.T) {
		r := newTestRegistry()
		r.Types = []wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.List[generics.Peer]]()}
		r.TSNameOverride = map[string]string{"List_Peer": "Peer"}
		requireGenerateError(t, r, listPeer+" and type "+genericsPkg+".Peer both emit the TS identifier Peer")
	})
	t.Run("a request component", func(t *testing.T) {
		dirPeer := wiregen.TypeRef[generics.Dir[generics.Peer]]()
		r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
		r.Types = []wiregen.Type{wiregen.TypeRef[generics.Peer](), wiregen.TypeRef[generics.Dir_PeerInput]()}
		r.Endpoints = []wiregen.Endpoint{{
			Name: "putDir", Method: "POST", Path: "/api/dir", Kind: wiregen.KindJSON, Request: dirPeer, Response: dirPeer,
		}}
		r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
		_, err := r.openapiFile(t.Context())
		for _, want := range []string{
			"OpenAPI component Dir_PeerInput is named by both",
			"type " + genericsPkg + ".Dir[" + genericsPkg + ".Peer]",
			"type " + genericsPkg + ".Dir_PeerInput",
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("openapi error = %v, want one naming %q", err, want)
			}
		}
	})
}

// A union or URL-argument refusal names an instantiation by its Go spelling.
func TestGenerics_RoleRefusalsNameTheGoType(t *testing.T) {
	t.Run("a union", func(t *testing.T) {
		r := newTestRegistry()
		r.Types = []wiregen.Type{wiregen.TypeRef[generics.Peer]()}
		r.Unions = []wiregen.Union{{
			Type: wiregen.TypeRef[generics.Pair[string, generics.Peer]](), Tag: "nosuch",
			Cases: map[string][]string{"peer": {"val"}},
		}}
		requireGenerateError(t, r, "union "+genericsPkg+".Pair[string,"+genericsPkg+`.Peer]: has no field "nosuch"`)
	})
	t.Run("an endpoint's query", func(t *testing.T) {
		listInt := wiregen.TypeRef[generics.List[int]]()
		r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
		r.Endpoints = []wiregen.Endpoint{{
			Name: "listInts", Method: "GET", Path: "/api/ints", Kind: wiregen.KindJSON, Query: listInt, Response: listInt,
		}}
		requireGenerateError(t, r, "Query field "+genericsPkg+".List[int].Items is number[]")
	})
}

// reflect escapes the '.' in a package path's last element as %2e, and the
// argument still names the registered type.
func TestGenerics_DottedPackageArgument(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[dotted.Peer](), wiregen.TypeRef[generics.List[dotted.Peer]]()}
	out := mustGen(t, r.typesFile)
	for _, want := range []string{"export interface List_Peer {", "items: Peer[];"} {
		if !strings.Contains(out, want) {
			t.Errorf("types missing %q, got:\n%s", want, out)
		}
	}
}

// A Mappings key spelled with type arguments maps that instantiation, and
// the generic's key maps every other one.
func TestGenerics_MappingsKeys(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[generics.Holder]()}
	r.Mappings = map[string]wiregen.Mapping{
		genericsPkg + ".List[" + genericsPkg + ".Peer]": {TS: "ByInstance"},
		genericsPkg + ".List":                           {TS: "ByGeneric"},
	}
	out := mustGen(t, r.typesFile)
	for _, want := range []string{"peers: ByInstance;", "counts: ByGeneric;"} {
		if !strings.Contains(out, want) {
			t.Errorf("types missing %q, got:\n%s", want, out)
		}
	}
}

// A component name outside OpenAPI's set is refused for the document, a
// derived name and a plain type's name alike, while the TS files still
// generate.
func TestGenerics_OpenAPIRefusesANonASCIIComponent(t *testing.T) {
	cases := []struct {
		name, component, goType string
		types                   []wiregen.Type
	}{
		{"a plain type", "Maß", genericsPkg + ".Maß", []wiregen.Type{wiregen.TypeRef[generics.Maß]()}},
		{"an instance", "List_Maß", genericsPkg + ".List[" + genericsPkg + ".Maß]", []wiregen.Type{wiregen.TypeRef[generics.Maß](), wiregen.TypeRef[generics.List[generics.Maß]]()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			r.Types = tc.types
			r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
			mustGen(t, r.typesFile)
			_, err := r.openapiFile(t.Context())
			want := `OpenAPI component name "` + tc.component + `", of type ` + tc.goType + ","
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("openapi error = %v, want one naming %q", err, want)
			}
		})
	}
}

const genericWireSource = `package wire

type Peer struct {
	Name string ` + "`json:\"name\"`" + `
	ID   int    ` + "`json:\"id\"`" + `
}

type List[T any] struct {
	Next  string ` + "`json:\"next,omitempty\"`" + `
	Items []T    ` + "`json:\"items\"`" + `
}
`

// An instance's type and fields pair with the generic's declaration, as
// deadset-go's internal/inventory walkTypeSpec and appendField record a
// generic's fields under its declared name, while each instance keeps its own
// edge ids and TS symbol.
func TestDeadsetEdges_PairsAnInstanceWithItsOrigin(t *testing.T) {
	root := moduleTempDir(t)
	for _, d := range []string{"wire", filepath.Join("web", "src", "wire")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "wire", "wire.go"), genericWireSource)
	writeFile(t, filepath.Join(root, "web", "package.json"), `{"name": "app"}`)
	pkg := "github.com/cplieger/go/wiregen/v2/" + filepath.Base(root) + "/wire"
	r := &wiregen.Registry{Types: []wiregen.Type{
		{PkgPath: pkg, Name: "Peer"},
		{PkgPath: pkg, Name: "List[" + pkg + ".Peer]"},
		{PkgPath: pkg, Name: "List[int]"},
		{PkgPath: pkg, Name: "List[" + pkg + ".List[" + pkg + ".Peer]]"},
	}}
	got := mustGenerateEdges(t, root, r)
	want := strings.ReplaceAll(`{
  "description": "Edges between Go wire symbols and the TypeScript wiregen generates from them; wiregen rewrites every edge under wiregen/.",
  "edges": {
    "wiregen/List_List_Peer": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List",
      "used_by": "ts://app/src/wire/types.gen.ts#List_List_Peer"
    },
    "wiregen/List_List_Peer/Items": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Items",
      "used_by": "ts://app/src/wire/types.gen.ts#List_List_Peer.items"
    },
    "wiregen/List_List_Peer/Next": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Next",
      "used_by": "ts://app/src/wire/types.gen.ts#List_List_Peer.next"
    },
    "wiregen/List_Peer": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List",
      "used_by": "ts://app/src/wire/types.gen.ts#List_Peer"
    },
    "wiregen/List_Peer/Items": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Items",
      "used_by": "ts://app/src/wire/types.gen.ts#List_Peer.items"
    },
    "wiregen/List_Peer/Next": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Next",
      "used_by": "ts://app/src/wire/types.gen.ts#List_Peer.next"
    },
    "wiregen/List_int": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List",
      "used_by": "ts://app/src/wire/types.gen.ts#List_int"
    },
    "wiregen/List_int/Items": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Items",
      "used_by": "ts://app/src/wire/types.gen.ts#List_int.items"
    },
    "wiregen/List_int/Next": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#List.Next",
      "used_by": "ts://app/src/wire/types.gen.ts#List_int.next"
    },
    "wiregen/Peer": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Peer",
      "used_by": "ts://app/src/wire/types.gen.ts#Peer"
    },
    "wiregen/Peer/ID": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Peer.ID",
      "used_by": "ts://app/src/wire/types.gen.ts#Peer.id"
    },
    "wiregen/Peer/Name": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Peer.Name",
      "used_by": "ts://app/src/wire/types.gen.ts#Peer.name"
    }
  }
}
`, "@PKG@", pkg)
	if got != want {
		t.Errorf("edges document:\n%s\nwant:\n%s", got, want)
	}
}

// A struct embedding an instantiation keeps its edges: the embed is a field
// of the struct and the promoted fields pair with the generic's declaration.
func TestDeadsetEdges_EmbeddedInstanceUnchanged(t *testing.T) {
	root := moduleTempDir(t)
	for _, d := range []string{"wire", filepath.Join("web", "src", "wire")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "wire", "wire.go"), genericWireSource+"\ntype Page struct {\n\tList[Peer]\n\tTotal int `json:\"total\"`\n}\n")
	writeFile(t, filepath.Join(root, "web", "package.json"), `{"name": "app"}`)
	pkg := "github.com/cplieger/go/wiregen/v2/" + filepath.Base(root) + "/wire"
	edgesOf := func(types ...string) map[string]edgeSides {
		r := &wiregen.Registry{}
		for _, name := range types {
			r.Types = append(r.Types, wiregen.Type{PkgPath: pkg, Name: name})
		}
		_, edges := readEdges(t, mustGenerateEdges(t, root, r))
		return edges
	}
	alone := edgesOf("Peer", "Page")
	want := map[string]string{
		"wiregen/Page":            "go://" + pkg + "#Page",
		"wiregen/Page/List":       "go://" + pkg + "#Page.List",
		"wiregen/Page/List.Items": "go://" + pkg + "#List.Items",
		"wiregen/Page/List.Next":  "go://" + pkg + "#List.Next",
		"wiregen/Page/Total":      "go://" + pkg + "#Page.Total",
	}
	for id, provides := range want {
		if got := alone[id].Provides; got != provides {
			t.Errorf("%s provides %q, want %q", id, got, provides)
		}
	}
	beside := edgesOf("Peer", "Page", "List["+pkg+".Peer]")
	for id := range want {
		if got, want := beside[id], alone[id]; got.Provides != want.Provides || got.UsedBy != want.UsedBy {
			t.Errorf("%s beside List[Peer] = %s used by %s, want %s used by %s", id, got.Provides, got.UsedBy, want.Provides, want.UsedBy)
		}
	}
}
