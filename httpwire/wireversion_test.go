package httpwire_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/go/httpwire"
)

func wireFixture(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/wireversion.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// edited answers doc with edit applied to the object at path (member names,
// or decimal indexes into an array), written back with other whitespace.
func edited(t *testing.T, doc []byte, path []string, edit func(map[string]any)) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		t.Fatal(err)
	}
	at := root
	for _, k := range path {
		switch v := at.(type) {
		case map[string]any:
			at = v[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i >= len(v) {
				t.Fatalf("no index %q in %v", k, path)
			}
			at = v[i]
		}
	}
	m, ok := at.(map[string]any)
	if !ok {
		t.Fatalf("no object at %v", path)
	}
	edit(m)
	out, err := json.MarshalIndent(root, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func set(key string, value any) func(map[string]any) {
	return func(m map[string]any) { m[key] = value }
}

func del(key string) func(map[string]any) {
	return func(m map[string]any) { delete(m, key) }
}

type wireEdit struct {
	edit func(map[string]any)
	name string
	path []string
}

var (
	entryPath    = []string{"components", "schemas", "Entry"}
	getEntryPath = []string{"paths", "/v1/entries/{id}", "get"}
	putEntryPath = []string{"paths", "/v1/entries/{id}", "put"}
	notFoundPath = []string{"paths", "/v1/entries/{id}", "get", "responses", "404"}
	okPath       = []string{"paths", "/v1/entries/{id}", "get", "responses", "200"}
	feedPath     = []string{"paths", "/v1/events", "get", "x-feed"}
)

func under(base []string, more ...string) []string {
	return append(slices.Clone(base), more...)
}

func TestWireVersionFixtureParses(t *testing.T) {
	if _, err := httpwire.ParseSchema(wireFixture(t)); err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
}

func TestWireVersionKeepsWordsOnlyEdits(t *testing.T) {
	doc := wireFixture(t)
	want := httpwire.WireVersion(doc)
	if want == "" {
		t.Fatal("WireVersion(fixture) is empty")
	}
	edits := []wireEdit{
		{name: "layout only", edit: func(map[string]any) {}},
		{name: "app title", path: []string{"info"}, edit: set("title", "Other")},
		{name: "app version", path: []string{"info"}, edit: set("version", "2.0.0")},
		{name: "app description", path: []string{"info"}, edit: set("description", "Other words.")},
		{name: "no info", edit: del("info")},
		{name: "server description", path: []string{"servers", "0"}, edit: set("description", "Other words.")},
		{name: "server variable description", path: []string{"servers", "0", "variables", "description"}, edit: set("description", "Other words.")},
		{name: "type doc comment", path: entryPath, edit: set("description", "Other words.")},
		{name: "type doc comment removed", path: entryPath, edit: del("description")},
		{name: "component named description doc comment", path: []string{"components", "schemas", "description"}, edit: set("description", "Other words.")},
		{name: "security scheme description", path: []string{"components", "securitySchemes", "session"}, edit: set("description", "Other words.")},
		{name: "row summary", path: getEntryPath, edit: set("summary", "Other words.")},
		{name: "row description", path: getEntryPath, edit: set("description", "Other words.")},
		{name: "row description added", path: putEntryPath, edit: set("description", "New words.")},
		{name: "parameter description", path: under(getEntryPath, "parameters", "0"), edit: set("description", "Other words.")},
		{name: "request body description", path: under(putEntryPath, "requestBody"), edit: set("description", "Other words.")},
		{name: "response description", path: okPath, edit: set("description", "Fine")},
		{name: "error response description", path: notFoundPath, edit: set("description", "Not Found")},
		{name: "header description", path: under(okPath, "headers", "description"), edit: set("description", "Other words.")},
		{name: "remediation", path: under(notFoundPath, "x-error-codes", "not_found"), edit: set("remediation", "Other words.")},
	}
	for _, f := range []string{"description", "summary", "info"} {
		edits = append(edits, wireEdit{name: "field " + f + " doc comment", path: under(entryPath, "properties", f), edit: set("description", "Other words.")})
	}
	for _, e := range edits {
		t.Run(e.name, func(t *testing.T) {
			if got := httpwire.WireVersion(edited(t, doc, e.path, e.edit)); got != want {
				t.Errorf("WireVersion = %q, want %q kept", got, want)
			}
		})
	}
}

func TestWireVersionMovesOnWireEdits(t *testing.T) {
	doc := wireFixture(t)
	was := httpwire.WireVersion(doc)
	var edits []wireEdit
	for _, f := range []string{"description", "summary", "info"} {
		others := slices.DeleteFunc([]any{"description", "summary", "info"}, func(v any) bool { return v == f })
		edits = append(edits,
			wireEdit{name: "field " + f + " changes type", path: under(entryPath, "properties", f), edit: set("type", "integer")},
			wireEdit{name: "field " + f + " gains a bound", path: under(entryPath, "properties", f), edit: set("maxLength", json.Number("10"))},
			wireEdit{name: "field " + f + " becomes optional", path: entryPath, edit: set("required", others)},
			wireEdit{name: "field " + f + " removed", path: under(entryPath, "properties"), edit: del(f)},
		)
	}
	edits = append(edits,
		wireEdit{name: "bound past float64 precision", path: under(entryPath, "properties", "limit"), edit: set("maximum", json.Number("9007199254740992"))},
		wireEdit{name: "component named description changes type", path: []string{"components", "schemas", "description"}, edit: set("type", "integer")},
		wireEdit{name: "component named description removed", path: []string{"components", "schemas"}, edit: del("description")},
		wireEdit{name: "header named description removed", path: under(okPath, "headers"), edit: del("description")},
		wireEdit{name: "header named description changes type", path: under(okPath, "headers", "description"), edit: set("schema", map[string]any{"type": "integer"})},
		wireEdit{name: "server variable named description", path: []string{"servers", "0", "variables", "description"}, edit: set("default", "example.org")},
		wireEdit{name: "server url", path: []string{"servers", "0"}, edit: set("url", "https://example.org/")},
		wireEdit{name: "error code status", path: under(notFoundPath, "x-error-codes", "not_found"), edit: set("status", json.Number("410"))},
		wireEdit{name: "error code no effect", path: under(notFoundPath, "x-error-codes", "not_found"), edit: set("no_effect", false)},
		wireEdit{name: "error code added", path: under(notFoundPath, "x-error-codes"), edit: set("gone", map[string]any{"status": json.Number("404"), "no_effect": true, "remediation": "x"})},
		wireEdit{name: "feed format version", path: feedPath, edit: set("version", json.Number("2"))},
		wireEdit{name: "idempotency window", path: under(putEntryPath, "x-idempotency"), edit: set("window_s", json.Number("86400"))},
		wireEdit{name: "idempotency window dropped", path: putEntryPath, edit: del("x-idempotency")},
		wireEdit{name: "words inside an extension value", path: feedPath, edit: set("description", "Other words.")},
		wireEdit{name: "event schema description", path: []string{"paths", "/v1/events", "get", "responses", "200", "content", "text/event-stream", "x-events", "changed"}, edit: set("description", "Other words.")},
		wireEdit{name: "operation id", path: getEntryPath, edit: set("operationId", "readEntry")},
		wireEdit{name: "path", path: []string{"paths"}, edit: func(m map[string]any) { m["/v1/entry/{id}"] = m["/v1/entries/{id}"]; delete(m, "/v1/entries/{id}") }},
		wireEdit{name: "security scheme cookie", path: []string{"components", "securitySchemes", "session"}, edit: set("name", "session")},
		wireEdit{name: "openapi version", edit: set("openapi", "3.1.1")},
	)
	for _, e := range edits {
		t.Run(e.name, func(t *testing.T) {
			if got := httpwire.WireVersion(edited(t, doc, e.path, e.edit)); got == was || got == "" {
				t.Errorf("WireVersion = %q, want a new value (was %q)", got, was)
			}
		})
	}
}

// versionsOf answers the versions of tmpl with its %s written as two
// different words.
func versionsOf(t *testing.T, tmpl string) (a, b string) {
	t.Helper()
	a = httpwire.WireVersion(fmt.Appendf(nil, tmpl, "one"))
	b = httpwire.WireVersion(fmt.Appendf(nil, tmpl, "two"))
	if a == "" || b == "" {
		t.Fatalf("WireVersion of %s is empty", tmpl)
	}
	return a, b
}

func schemaAt(s string) string {
	return `{"openapi":"3.1.0","paths":{},"components":{"schemas":{"S":` + s + `}}}`
}

func TestWireVersionDropsDocsAtEveryOpenAPIPosition(t *testing.T) {
	cases := map[string]string{
		"path item":                `{"openapi":"3.1.0","paths":{"/a":{"summary":"%s","description":"%[1]s"}}}`,
		"path item parameter":      `{"openapi":"3.1.0","paths":{"/a":{"parameters":[{"name":"p","in":"query","description":"%s"}]}}}`,
		"path item server":         `{"openapi":"3.1.0","paths":{"/a":{"servers":[{"url":"/","description":"%s"}]}}}`,
		"operation server":         `{"openapi":"3.1.0","paths":{"/a":{"get":{"servers":[{"url":"/","description":"%s"}]}}}}`,
		"webhook":                  `{"openapi":"3.1.0","webhooks":{"w":{"post":{"summary":"%s","responses":{"200":{"description":"%[1]s"}}}}}}`,
		"callback":                 `{"openapi":"3.1.0","paths":{"/a":{"post":{"callbacks":{"c":{"{$request.body#/url}":{"post":{"description":"%s"}}}}}}}}`,
		"parameter content schema": `{"openapi":"3.1.0","paths":{"/a":{"get":{"parameters":[{"name":"p","in":"query","content":{"application/json":{"schema":{"description":"%s"}}}}]}}}}`,
		"encoding header":          `{"openapi":"3.1.0","paths":{"/a":{"post":{"requestBody":{"content":{"multipart/form-data":{"encoding":{"f":{"headers":{"h":{"description":"%s"}}}}}}}}}}}`,
		"header content schema":    `{"openapi":"3.1.0","paths":{"/a":{"get":{"responses":{"200":{"headers":{"h":{"content":{"text/plain":{"schema":{"description":"%s"}}}}}}}}}}}`,
		"component response":       `{"openapi":"3.1.0","components":{"responses":{"r":{"description":"%s","x-error-codes":{"c":{"status":400,"remediation":"%[1]s"}}}}}}`,
		"component parameter":      `{"openapi":"3.1.0","components":{"parameters":{"p":{"name":"p","in":"query","description":"%s","schema":{"description":"%[1]s"}}}}}`,
		"component request body":   `{"openapi":"3.1.0","components":{"requestBodies":{"b":{"description":"%s"}}}}`,
		"component header":         `{"openapi":"3.1.0","components":{"headers":{"h":{"description":"%s"}}}}`,
		"component path item":      `{"openapi":"3.1.0","components":{"pathItems":{"p":{"description":"%s","get":{"summary":"%[1]s"}}}}}`,
		"component callback":       `{"openapi":"3.1.0","components":{"callbacks":{"c":{"{$url}":{"post":{"description":"%s"}}}}}}`,
		"schema summary":           schemaAt(`{"summary":"%s","description":"%[1]s"}`),
		"schema items as an array": schemaAt(`{"items":[{"description":"%s"}]}`),
		"nested to any depth":      schemaAt(`{"properties":{"a":{"items":{"anyOf":[{"additionalProperties":{"description":"%s"}}]}}}}`),
	}
	for _, k := range []string{"items", "additionalProperties", "propertyNames", "contains", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema"} {
		cases["schema "+k] = schemaAt(`{"` + k + `":{"description":"%s"}}`)
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		cases["schema "+k] = schemaAt(`{"` + k + `":[{"description":"%s"}]}`)
	}
	for _, k := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		cases["schema "+k] = schemaAt(`{"` + k + `":{"m":{"description":"%s"}}}`)
	}
	for name, tmpl := range cases {
		t.Run(name, func(t *testing.T) {
			if a, b := versionsOf(t, tmpl); a != b {
				t.Errorf("WireVersion moved on a words-only edit: %q then %q", a, b)
			}
		})
	}
}

func TestWireVersionKeepsMembersNamedLikeDocs(t *testing.T) {
	for name, tmpl := range map[string]string{
		"extension value":                `{"openapi":"3.1.0","paths":{"/a":{"get":{"x-doc":{"description":"%s","summary":"%[1]s"}}}}}`,
		"remediation outside a response": `{"openapi":"3.1.0","x-error-codes":{"c":{"remediation":"%s"}}}`,
		"info below the top":             `{"openapi":"3.1.0","components":{"schemas":{"info":{"title":"%s"}}},"x-info":{"info":{"title":"%[1]s"}}}`,
		"schema default":                 schemaAt(`{"default":{"description":"%s"}}`),
		"schema const":                   schemaAt(`{"const":{"summary":"%s"}}`),
		"schema enum":                    schemaAt(`{"enum":[{"description":"%s"}]}`),
		"schema examples":                schemaAt(`{"examples":[{"description":"%s"}]}`),
		"schema title":                   schemaAt(`{"title":"%s"}`),
		"discriminator mapping":          schemaAt(`{"discriminator":{"propertyName":"k","mapping":{"description":"#/components/schemas/%s"}}}`),
		"property named summary":         schemaAt(`{"properties":{"summary":{"const":"%s"}}}`),
		"pattern property named info":    schemaAt(`{"patternProperties":{"info":{"pattern":"%s"}}}`),
		"path item ref":                  `{"openapi":"3.1.0","paths":{"/a":{"$ref":"#/components/pathItems/%s"}}}`,
		"callback expression":            `{"openapi":"3.1.0","paths":{"/a":{"post":{"callbacks":{"description":{"{$%s}":{}}}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if a, b := versionsOf(t, tmpl); a == b {
				t.Errorf("WireVersion kept %q across a wire edit", a)
			}
		})
	}
}

// The normal form is the document with the dropped keywords removed, written
// with sorted keys, no whitespace, no HTML escaping and each number's text as
// the document spells it.
func TestWireVersionHashesTheNormalForm(t *testing.T) {
	doc := `{ "paths": {"/a<b>&": {"get": {"summary": "s", "operationId": "o"}}},
	  "openapi": "3.1.0", "info": {"title": "t"}, "x-\u00e9": [1, 2.50, 1e400, 9007199254740993] }`
	form := `{"openapi":"3.1.0","paths":{"/a<b>&":{"get":{"operationId":"o"}}},"x-é":[1,2.50,1e400,9007199254740993]}`
	sum := sha256.Sum256([]byte(form))
	if got, want := httpwire.WireVersion([]byte(doc)), hex.EncodeToString(sum[:])[:16]; got != want {
		t.Errorf("WireVersion = %q, want %q (the hash of %s)", got, want, form)
	}
}

func TestWireVersionNotADocument(t *testing.T) {
	for _, doc := range []string{"", "  ", "null", "[]", `"x"`, "1", "{", "{} {}", "{}x", `{"a":}`} {
		t.Run(strconv.Quote(doc), func(t *testing.T) {
			if got := httpwire.WireVersion([]byte(doc)); got != "" {
				t.Errorf("WireVersion = %q, want empty", got)
			}
		})
	}
}
