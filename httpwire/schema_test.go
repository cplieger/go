package httpwire_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

func readFixtureDoc() ([]byte, error) {
	return os.ReadFile("testdata/openapi/schema.json")
}

func fixtureSchema(t *testing.T) *httpwire.Schema {
	t.Helper()
	b, err := readFixtureDoc()
	if err != nil {
		t.Fatal(err)
	}
	s, err := httpwire.ParseSchema(b)
	if err != nil {
		t.Fatalf("ParseSchema(fixture): %v", err)
	}
	return s
}

// schemaDoc is a minimal document holding the given component schemas and
// one GET operation "op" answering 200 with the "Root" component.
func schemaDoc(schemas string) string {
	return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
"components":{"schemas":` + schemas + `},
"paths":{"/v1/x":{"get":{"operationId":"op","responses":{"200":{"description":"OK",
"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Root"}}}}}}}}}`
}

// opDoc is a minimal document with no component schema and the given
// operation object under GET /v1/x.
func opDoc(op string) string {
	return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/v1/x":{"get":` + op + `}}}`
}

func TestParseSchemaAcceptsFixture(t *testing.T) {
	fixtureSchema(t)
}

func TestParseSchemaAcceptsMinimalDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"no components":                opDoc(`{"operationId":"op","responses":{"204":{"description":"No Content"}}}`),
		"annotations":                  schemaDoc(`{"Root":{"type":"string","description":"d","format":"date-time","contentEncoding":"base64","default":"x"}}`),
		"empty schema":                 schemaDoc(`{"Root":{}}`),
		"recursive through a property": schemaDoc(`{"Root":{"type":"object","properties":{"kids":{"type":"array","items":{"$ref":"#/components/schemas/Root"}}}}}`),
		"recursive through a map":      schemaDoc(`{"Root":{"type":"object","additionalProperties":{"anyOf":[{"$ref":"#/components/schemas/Root"},{"type":"null"}]}}}`),
		"header pattern":               schemaDoc(`{"Root":{"type":"string","pattern":"^[\\x20-\\x7E]*$"}}`),
		"uint key pattern":             schemaDoc(`{"Root":{"type":"object","additionalProperties":{},"propertyNames":{"pattern":"^(?:0|[1-9][0-9]*)$"}}}`),
		"operation extension":          opDoc(`{"operationId":"op","x-anything":{"uniqueItems":true},"responses":{"204":{"description":"No Content"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := httpwire.ParseSchema([]byte(doc)); err != nil {
				t.Errorf("ParseSchema: %v", err)
			}
		})
	}
}

// The closed keyword set must cover every document wiregen writes; its
// golden documents live in the same repository.
func TestParseSchemaAcceptsWiregenGoldenDocuments(t *testing.T) {
	files, err := filepath.Glob("../wiregen/testdata/golden/*.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no wiregen golden OpenAPI document under ../wiregen/testdata/golden")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := httpwire.ParseSchema(b); err != nil {
				t.Errorf("ParseSchema: %v", err)
			}
		})
	}
}

func TestParseSchemaRefusals(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata/openapi", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ok := `"responses":{"204":{"description":"No Content"}}`
	for _, c := range []struct {
		name, doc, want string
	}{
		{"keyword outside the set in a fixture", read("uniqueitems.json"), "uniqueItems"},
		{"pattern outside the fixed set in a fixture", read("pattern.json"), "pattern"},
		{"capturing integer key pattern", schemaDoc(`{"Root":{"type":"object","propertyNames":{"pattern":"^(0|[1-9][0-9]*)$"}}}`), "pattern"},
		{"pattern not a string", schemaDoc(`{"Root":{"type":"string","pattern":5}}`), "pattern"},
		{"schema extension", schemaDoc(`{"Root":{"type":"string","x-other":1}}`), "x-other"},
		{"title", schemaDoc(`{"Root":{"type":"string","title":"t"}}`), "title"},
		{"allOf", schemaDoc(`{"Root":{"allOf":[{"type":"string"}]}}`), "allOf"},
		{"boolean schema", schemaDoc(`{"Root":{"type":"object","properties":{"a":true}}}`), "not an object"},
		{"unresolved ref", schemaDoc(`{"Root":{"$ref":"#/components/schemas/Missing"}}`), "Missing"},
		{"ref outside components", schemaDoc(`{"Root":{"$ref":"#/paths/~1v1~1x"}}`), "#/paths"},
		{"ref to another file", schemaDoc(`{"Root":{"$ref":"other.json#/components/schemas/Root"}}`), "other.json"},
		{"ref not a string", schemaDoc(`{"Root":{"$ref":5}}`), "$ref"},
		{"ref beside a keyword", schemaDoc(`{"Root":{"$ref":"#/components/schemas/B","type":"object"},"B":{"type":"object"}}`), "$ref"},
		{"ref cycle", schemaDoc(`{"Root":{"$ref":"#/components/schemas/B"},"B":{"$ref":"#/components/schemas/Root"}}`), "cycle"},
		{"ref cycle through anyOf", schemaDoc(`{"Root":{"anyOf":[{"$ref":"#/components/schemas/Root"},{"type":"null"}]}}`), "cycle"},
		{"ref cycle through a union", schemaDoc(`{"Root":{"oneOf":[{"$ref":"#/components/schemas/Root"}],"discriminator":{"propertyName":"k","mapping":{"a":"#/components/schemas/Root"}}}}`), "cycle"},
		{"unknown type", schemaDoc(`{"Root":{"type":"text"}}`), "type"},
		{"type twice in a list", schemaDoc(`{"Root":{"type":["string","string"]}}`), "type"},
		{"type not a string", schemaDoc(`{"Root":{"type":5}}`), "type"},
		{"properties not an object", schemaDoc(`{"Root":{"type":"object","properties":[]}}`), "properties"},
		{"required not strings", schemaDoc(`{"Root":{"type":"object","required":[1]}}`), "required"},
		{"additionalProperties not a schema", schemaDoc(`{"Root":{"type":"object","additionalProperties":1}}`), "additionalProperties"},
		{"enum not an array", schemaDoc(`{"Root":{"type":"string","enum":"a"}}`), "enum"},
		{"negative maxLength", schemaDoc(`{"Root":{"type":"string","maxLength":-1}}`), "maxLength"},
		{"fractional minItems", schemaDoc(`{"Root":{"type":"array","minItems":1.5}}`), "minItems"},
		{"minimum not a number", schemaDoc(`{"Root":{"type":"integer","minimum":"0"}}`), "minimum"},
		{"x-max-bytes not a count", schemaDoc(`{"Root":{"type":"string","x-max-bytes":"8"}}`), "x-max-bytes"},
		{"items as a list", schemaDoc(`{"Root":{"type":"array","items":[{"type":"string"}]}}`), "items"},
		{"oneOf with no discriminator", schemaDoc(`{"Root":{"oneOf":[{"$ref":"#/components/schemas/A"}]},"A":{"type":"object"}}`), "discriminator"},
		{"discriminator with no oneOf", schemaDoc(`{"Root":{"type":"object","discriminator":{"propertyName":"k","mapping":{}}}}`), "discriminator"},
		{"discriminator with no mapping", schemaDoc(`{"Root":{"oneOf":[{"$ref":"#/components/schemas/A"}],"discriminator":{"propertyName":"k"}},"A":{"type":"object"}}`), "mapping"},
		{"mapping off the oneOf list", schemaDoc(`{"Root":{"oneOf":[{"$ref":"#/components/schemas/A"}],"discriminator":{"propertyName":"k","mapping":{"a":"#/components/schemas/B"}}},"A":{"type":"object"},"B":{"type":"object"}}`), "mapping"},
		{"oneOf case inline", schemaDoc(`{"Root":{"oneOf":[{"type":"object"}],"discriminator":{"propertyName":"k","mapping":{"a":"#/components/schemas/Root"}}}}`), "oneOf"},
		{"anyOf off the null wrapper", schemaDoc(`{"Root":{"anyOf":[{"type":"string"},{"type":"integer"}]}}`), "anyOf"},
		{"anyOf of three", schemaDoc(`{"Root":{"anyOf":[{"type":"string"},{"type":"null"},{"type":"null"}]}}`), "anyOf"},
		{"not JSON", `{"openapi":`, "JSON"},
		{"trailing data", opDoc(`{"operationId":"op",`+ok+`}`) + `{}`, "JSON"},
		{"OpenAPI 3.0", strings.Replace(opDoc(`{"operationId":"op",`+ok+`}`), "3.1.0", "3.0.3", 1), "openapi"},
		{"webhooks", strings.Replace(opDoc(`{"operationId":"op",`+ok+`}`), `"paths"`, `"webhooks":{},"paths"`, 1), "webhooks"},
		{"component parameters", strings.Replace(opDoc(`{"operationId":"op",`+ok+`}`), `"paths"`, `"components":{"parameters":{}},"paths"`, 1), "parameters"},
		{"no operationId", opDoc(`{` + ok + `}`), "operationId"},
		{"operationId twice", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/a":{"get":{"operationId":"op",` + ok + `}},"/b":{"get":{"operationId":"op",` + ok + `}}}}`, "op"},
		{"path-item parameters", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/a":{"parameters":[],"get":{"operationId":"op",` + ok + `}}}}`, "parameters"},
		{"callbacks", opDoc(`{"operationId":"op","callbacks":{},` + ok + `}`), "callbacks"},
		{"status range key", opDoc(`{"operationId":"op","responses":{"2XX":{"description":"OK"}}}`), "2XX"},
		{"default response", opDoc(`{"operationId":"op","responses":{"default":{"description":"OK"}}}`), "default"},
		{"status out of range", opDoc(`{"operationId":"op","responses":{"600":{"description":"OK"}}}`), "600"},
		{"parameter ref", opDoc(`{"operationId":"op","parameters":[{"$ref":"#/components/parameters/p"}],` + ok + `}`), "$ref"},
		{"parameter with no schema", opDoc(`{"operationId":"op","parameters":[{"name":"p","in":"query"}],` + ok + `}`), "schema"},
		{"parameter in an unknown place", opDoc(`{"operationId":"op","parameters":[{"name":"p","in":"body","schema":{}}],` + ok + `}`), "body"},
		{"parameter twice", opDoc(`{"operationId":"op","parameters":[{"name":"p","in":"query","schema":{}},{"name":"p","in":"query","schema":{}}],` + ok + `}`), "twice"},
		{"bad keyword in a parameter", opDoc(`{"operationId":"op","parameters":[{"name":"p","in":"query","schema":{"uniqueItems":true}}],` + ok + `}`), "uniqueItems"},
		{"bad keyword in a request body", opDoc(`{"operationId":"op","requestBody":{"required":true,"content":{"application/json":{"schema":{"not":{}}}}},` + ok + `}`), "not"},
		{"bad keyword in a header", opDoc(`{"operationId":"op","responses":{"204":{"description":"No Content","headers":{"X":{"schema":{"const":1,"examples":[1]}}}}}}`), "examples"},
		{"bad keyword in an event", opDoc(`{"operationId":"op","responses":{"200":{"description":"OK","content":{"text/event-stream":{"schema":{"type":"string"},"x-events":{"e":{"if":{}}}}}}}}`), "if"},
		{"media example", opDoc(`{"operationId":"op","responses":{"200":{"description":"OK","content":{"application/json":{"schema":{},"example":1}}}}}`), "example"},
		{"response links", opDoc(`{"operationId":"op","responses":{"200":{"description":"OK","links":{}}}}`), "links"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := httpwire.ParseSchema([]byte(c.doc))
			if err == nil {
				t.Fatalf("ParseSchema accepted the document (%v)", s)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name %q", err, c.want)
			}
		})
	}
}

func codes(entries []httpwire.FieldError) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path+"="+e.Code)
		if e.Message == "" {
			out = append(out, e.Path+" has no message")
		}
	}
	return out
}

// item is a valid createItem body with extra members spliced in before the
// closing brace.
func item(extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"name":"box","count":1` + extra + `}`
}

func TestCheckBody(t *testing.T) {
	s := fixtureSchema(t)
	for _, c := range []struct {
		name, op string
		body     string
		want     []string
		status   int
	}{
		{name: "valid", op: "createItem", body: item(`"color":"red","tags":["a"],"labels":{"-1":2},"quoted":"-5","ratio":1.5,"flag":true,"note":null,"raw":{"any":[1,null]},"big":18446744073709551615,"kind":"item","at":"not a date","blob":null`)},
		{name: "valid nested", op: "createItem", body: item(`"next":{"name":"b","count":2,"next":null},"shape":{"kind":"circle","radius":3}`)},
		{name: "whitespace around", op: "createItem", body: " \n" + item("") + "\n "},
		{name: "out-of-enum field", op: "createItem", body: item(`"color":"blue"`), want: []string{"color=enum"}},
		{name: "const miss", op: "createItem", body: item(`"kind":"other"`), want: []string{"kind=enum"}},
		{name: "a byte over x-max-bytes", op: "createItem", body: `{"name":"ééééé","count":1}`, want: []string{"name=range"}},
		{name: "a character over maxLength", op: "createItem", body: `{"name":"abcdefghi","count":1}`, want: []string{"name=range"}},
		{name: "at x-max-bytes", op: "createItem", body: `{"name":"éééé","count":1}`},
		{name: "under minLength", op: "createItem", body: `{"name":"","count":1}`, want: []string{"name=range"}},
		{name: "string integer off its grammar", op: "createItem", body: item(`"quoted":"5x"`), want: []string{"quoted=pattern"}},
		{name: "string integer with a leading zero", op: "createItem", body: item(`"quoted":"05"`), want: []string{"quoted=pattern"}},
		{name: "integer key not decimal", op: "createItem", body: item(`"labels":{"x":1}`), want: []string{"labels.x=pattern"}},
		{name: "integer key with a plus", op: "createItem", body: item(`"labels":{"+1":1}`), want: []string{"labels.+1=pattern"}},
		{name: "2^53 at the maximum", op: "createItem", body: `{"name":"a","count":9007199254740992}`},
		{name: "2^53+1 over the maximum", op: "createItem", body: `{"name":"a","count":9007199254740993}`, want: []string{"count=range"}},
		{name: "under the minimum", op: "createItem", body: `{"name":"a","count":0}`, want: []string{"count=range"}},
		{name: "fraction under integer", op: "createItem", body: `{"name":"a","count":1.5}`, want: []string{"count=malformed"}},
		{name: "exponent under integer", op: "createItem", body: `{"name":"a","count":1e3}`, want: []string{"count=malformed"}},
		{name: "whole fraction under integer", op: "createItem", body: `{"name":"a","count":1.0}`, want: []string{"count=malformed"}},
		{name: "integer past uint64", op: "createItem", body: item(`"big":18446744073709551616`), want: []string{"big=range"}},
		{name: "integer past int64 below", op: "createItem", body: item(`"big":-9223372036854775809`), want: []string{"big=range"}},
		{name: "negative under minimum 0", op: "createItem", body: item(`"big":-1`), want: []string{"big=range"}},
		{name: "number past float64", op: "createItem", body: item(`"ratio":1e400`), want: []string{"ratio=range"}},
		{name: "integer under number", op: "createItem", body: item(`"ratio":123456789012345678901234567890`)},
		{name: "string under number", op: "createItem", body: item(`"ratio":"1"`), want: []string{"ratio=malformed"}},
		{name: "string under integer", op: "createItem", body: `{"name":"a","count":"1"}`, want: []string{"count=malformed"}},
		{name: "number under boolean", op: "createItem", body: item(`"flag":1`), want: []string{"flag=malformed"}},
		{name: "missing required", op: "createItem", body: `{"name":"a"}`, want: []string{"count=required"}},
		{name: "null on a required field", op: "createItem", body: `{"name":null,"count":1}`, want: []string{"name=required"}},
		{name: "null on an optional non-null field", op: "createItem", body: item(`"color":null`), want: []string{"color=required"}},
		{name: "null on a union field", op: "createItem", body: item(`"shape":null`), want: []string{"shape=required"}},
		{name: "unknown field", op: "createItem", body: item(`"extra":1`), want: []string{"extra=unknown"}},
		{name: "unknown nested field", op: "createItem", body: item(`"next":{"name":"b","count":1,"next":{"name":"c","count":1,"bogus":true}}`), want: []string{"next.next.bogus=unknown"}},
		{name: "array item of the wrong type", op: "createItem", body: item(`"tags":["a",2]`), want: []string{"tags[1]=malformed"}},
		{name: "array past maxItems", op: "createItem", body: item(`"tags":["a","b","c","d"]`), want: []string{"tags=range"}},
		{name: "map value of the wrong type", op: "createItem", body: item(`"labels":{"1":"x"}`), want: []string{"labels.1=malformed"}},
		{name: "one entry per failing path", op: "createItem", body: `{"name":"abcdefghi","color":"blue","extra":1,"tags":[1,2]}`, want: []string{"color=enum", "count=required", "extra=unknown", "name=range", "tags[0]=malformed", "tags[1]=malformed"}},
		{name: "union case", op: "putShape", body: `{"kind":"square","side":2}`},
		{name: "union field of another case", op: "putShape", body: `{"kind":"circle","radius":1,"side":2}`, want: []string{"side=union"}},
		{name: "union field of no case", op: "putShape", body: `{"kind":"circle","radius":1,"other":2}`, want: []string{"other=unknown"}},
		{name: "union tag off the mapping", op: "putShape", body: `{"kind":"hexagon"}`, want: []string{"kind=union"}},
		{name: "union tag missing", op: "putShape", body: `{"radius":1}`, want: []string{"kind=union"}},
		{name: "union tag not a string", op: "putShape", body: `{"kind":1,"radius":1}`, want: []string{"kind=union"}},
		{name: "union case missing its field", op: "putShape", body: `{"kind":"circle"}`, want: []string{"radius=required"}},
		{name: "union case field null", op: "putShape", body: `{"kind":"circle","radius":null}`, want: []string{"radius=required"}},
		{name: "union not an object", op: "putShape", body: `[1]`, want: []string{"=malformed"}},
		{name: "root of the wrong type", op: "createItem", body: `"item"`, want: []string{"=malformed"}},
		{name: "root null", op: "createItem", body: `null`, want: []string{"=required"}},
		{name: "empty body", op: "createItem", body: ``, want: []string{"=required"}},
		{name: "not JSON", op: "createItem", body: `{"name":`, want: []string{"=malformed"}},
		{name: "trailing data", op: "createItem", body: item("") + `{}`, want: []string{"=malformed"}},
		{name: "response body", op: "createItem", status: 201, body: item("")},
		{name: "response body miss", op: "createItem", status: 201, body: `{"name":"a"}`, want: []string{"count=required"}},
		{name: "error response", op: "createItem", status: 400, body: `{"code":"bad_body","message":"m","request_id":"r","fields":[{"path":"a","code":"range"}]}`, want: []string{"fields[0].message=required"}},
		{name: "array response", op: "putShape", status: 200, body: `[{"kind":"circle","radius":1},{"kind":"square","radius":1}]`, want: []string{"[1].radius=union", "[1].side=required"}},
		{name: "array response null", op: "putShape", status: 200, body: `null`},
		{name: "no body status, no body", op: "getItem", status: 204, body: ``},
		{name: "no body status, a body", op: "getItem", status: 204, body: `{}`, want: []string{"=unknown"}},
		{name: "undeclared status", op: "getItem", status: 404, body: `{}`, want: []string{"=unknown"}},
		{name: "operation with no request body", op: "getItem", body: `{}`, want: []string{"=unknown"}},
		{name: "unknown operation", op: "nope", body: `{}`, want: []string{"=unknown"}},
		{name: "raw response", op: "download", status: 200, body: `{}`, want: []string{"=unknown"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := codes(s.CheckBody(c.op, c.status, []byte(c.body)))
			if c.want == nil {
				c.want = []string{}
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("CheckBody = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckParam(t *testing.T) {
	s := fixtureSchema(t)
	for _, c := range []struct {
		value               any
		name, op, in, param string
		want                string
	}{
		{name: "enum member", op: "createItem", in: "query", param: "color", value: "red"},
		{name: "out-of-enum value", op: "createItem", in: "query", param: "color", value: "blue", want: "color=enum"},
		{name: "integer in range", op: "createItem", in: "query", param: "limit", value: int64(200)},
		{name: "integer under minimum", op: "createItem", in: "query", param: "limit", value: int64(0), want: "limit=range"},
		{name: "integer over maximum", op: "createItem", in: "query", param: "limit", value: int64(201), want: "limit=range"},
		{name: "uint64 in range", op: "createItem", in: "query", param: "n", value: uint64(18446744073709551615)},
		{name: "int64 under minimum 0", op: "createItem", in: "query", param: "n", value: int64(-1), want: "n=range"},
		{name: "text under integer", op: "createItem", in: "query", param: "limit", value: "5", want: "limit=malformed"},
		{name: "string at x-max-bytes", op: "createItem", in: "query", param: "q", value: "abcd"},
		{name: "string past x-max-bytes", op: "createItem", in: "query", param: "q", value: "abcé", want: "q=range"},
		{name: "boolean", op: "createItem", in: "query", param: "on", value: true},
		{name: "text under boolean", op: "createItem", in: "query", param: "on", value: "true", want: "on=malformed"},
		{name: "header in printable ASCII", op: "createItem", in: "header", param: "X-Thing", value: "a b~"},
		{name: "header with a tab", op: "createItem", in: "header", param: "X-Thing", value: "a\tb", want: "X-Thing=pattern"},
		{name: "path value under minimum", op: "getItem", in: "path", param: "id", value: int64(-1), want: "id=range"},
		{name: "path value", op: "getItem", in: "path", param: "id", value: uint64(7)},
		{name: "a query name in the path", op: "getItem", in: "query", param: "id", value: int64(1), want: "id=unknown"},
		{name: "unknown parameter", op: "createItem", in: "query", param: "nope", value: "x", want: "nope=unknown"},
		{name: "unknown operation", op: "nope", in: "query", param: "color", value: "red", want: "color=unknown"},
		{name: "unsupported value type", op: "createItem", in: "query", param: "limit", value: 1.5, want: "limit=malformed"},
		{name: "nil value", op: "createItem", in: "query", param: "color", value: nil, want: "color=malformed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fe := s.CheckParam(c.op, c.in, c.param, c.value)
			var got string
			if fe != nil {
				got = fe.Path + "=" + fe.Code
				if fe.Message == "" {
					t.Errorf("entry %s has no message", got)
				}
			}
			if got != c.want {
				t.Errorf("CheckParam = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNilSchemaChecksNothingKnown(t *testing.T) {
	var s *httpwire.Schema
	if got := codes(s.CheckBody("op", 0, []byte(`{}`))); !slices.Equal(got, []string{"=unknown"}) {
		t.Errorf("CheckBody on a nil Schema = %v, want [=unknown]", got)
	}
	if fe := s.CheckParam("op", "query", "p", "x"); fe == nil || fe.Code != "unknown" {
		t.Errorf("CheckParam on a nil Schema = %v, want code unknown", fe)
	}
}
