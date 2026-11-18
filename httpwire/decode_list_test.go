package httpwire_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

type (
	// heldPaths is a query list of up to three paths, sent as one path=
	// per value.
	heldPaths    []label
	holdersQuery struct {
		Kind  *itemKind `json:"kind"`
		Paths heldPaths `json:"path"`
	}
	optionalHolders struct {
		Paths heldPaths `json:"path,omitempty"`
	}
	pointerHolders struct {
		Paths *heldPaths `json:"path,omitempty"`
	}
	noCap      []label
	floatList  []float64
	zeroCap    []label
	listParams struct {
		Paths heldPaths `json:"path"`
	}
	noCapQuery struct {
		Paths noCap `json:"path"`
	}
	floatQuery struct {
		Paths floatList `json:"path"`
	}
	zeroCapQuery struct {
		Paths zeroCap `json:"path"`
	}
)

func (heldPaths) MaxItems() int { return 3 }
func (floatList) MaxItems() int { return 3 }
func (zeroCap) MaxItems() int   { return 0 }

func TestDecodeQueryList(t *testing.T) {
	t.Run("every occurrence decodes in order", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		rec := serve(d.api, http.MethodGet, "/api/holders?path=b1&kind=disc&path=a2&path=c3", "")
		if rec.Code != http.StatusOK || d.held == nil {
			t.Fatalf("answer %d %q", rec.Code, rec.Body.String())
		}
		if !slices.Equal(d.held.Paths, heldPaths{"b1", "a2", "c3"}) || d.held.Kind == nil || *d.held.Kind != "disc" {
			t.Errorf("Query = %+v", *d.held)
		}
	})
	t.Run("one occurrence is a list of one", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		if rec := serve(d.api, http.MethodGet, "/api/holders?path=a1", ""); rec.Code != http.StatusOK || d.held == nil || !slices.Equal(d.held.Paths, heldPaths{"a1"}) {
			t.Errorf("answer %d, Query %+v", rec.Code, d.held)
		}
	})
	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"none sent":                     {"kind=disc", []string{"path=required"}},
		"past MaxItems":                 {"path=a1&path=a2&path=a3&path=a4", []string{"path=range"}},
		"an element its type refuses":   {"path=a1&path=" + strings.Repeat("a", 9), []string{"path[1]=range"}},
		"an element the schema refuses": {"path=a1&path=a2&path=x", []string{"path[2]=range"}},
		"an element of invalid UTF-8":   {"path=%FF&path=a2", []string{"path[0]=malformed"}},
		"each refused element named":    {"path=x&path=a2&path=%FF", []string{"path[0]=range", "path[2]=malformed"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDecodeAPI(t, decodeDoc(t))
			rec := serve(d.api, http.MethodGet, "/api/holders?"+tc.query, "")
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_query" || !slices.Equal(fieldCodes(env), tc.want) {
				t.Errorf("answer %d %s %v, want 400 bad_query %v", rec.Code, env.Code, fieldCodes(env), tc.want)
			}
			if d.ran() {
				t.Error("the handler ran")
			}
		})
	}
}

// holdersParam edits the holders row's path parameter in the decode
// document.
func holdersParam(t *testing.T, edit func(p map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(decodeDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	op := doc["paths"].(map[string]any)["/api/holders"].(map[string]any)["get"].(map[string]any)
	edit(op["parameters"].([]any)[0].(map[string]any))
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewRefusesAQueryListTheDocumentDescribesOtherwise(t *testing.T) {
	schema := func(p map[string]any) map[string]any { return p["schema"].(map[string]any) }
	for name, tc := range map[string]struct {
		edit func(p map[string]any)
		want string
	}{
		"a scalar parameter":    {func(p map[string]any) { p["schema"] = map[string]any{"type": "string"} }, "array"},
		"no maxItems":           {func(p map[string]any) { delete(schema(p), "maxItems") }, "maxItems 3"},
		"another maxItems":      {func(p map[string]any) { schema(p)["maxItems"] = 4 }, "maxItems 3"},
		"no style":              {func(p map[string]any) { delete(p, "style") }, "style form"},
		"no explode":            {func(p map[string]any) { delete(p, "explode") }, "explode true"},
		"a minItems":            {func(p map[string]any) { schema(p)["minItems"] = 2 }, "minItems"},
		"explode false":         {func(p map[string]any) { p["explode"] = false }, "explode true"},
		"another style":         {func(p map[string]any) { p["style"] = "pipeDelimited" }, "style"},
		"explode not a boolean": {func(p map[string]any) { p["explode"] = "yes" }, "explode"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := httpwire.New(decodeTable(), nil, httpwire.WithSchema(holdersParam(t, tc.edit)))
			if err == nil || !strings.Contains(err.Error(), `row "items.holders"`) && !strings.Contains(err.Error(), "ParseSchema") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("New answered %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
	t.Run("an array for a scalar field", func(t *testing.T) {
		doc := holdersParam(t, func(map[string]any) {})
		var m map[string]any
		_ = json.Unmarshal(doc, &m)
		params := m["paths"].(map[string]any)["/api/holders"].(map[string]any)["get"].(map[string]any)["parameters"].([]any)
		params[1].(map[string]any)["schema"] = map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "string"}}
		doc, _ = json.Marshal(m)
		if _, err := httpwire.New(decodeTable(), nil, httpwire.WithSchema(doc)); err == nil || !strings.Contains(err.Error(), `"kind"`) {
			t.Errorf("New answered %v, want a refusal naming the kind parameter", err)
		}
	})
}

func TestDeriveRefusesQueryLists(t *testing.T) {
	list := func(name string, params, query reflect.Type) func(*httpwire.Table) {
		path := "/api/holders"
		if params != nil {
			path += "/{path}"
		}
		return addRow(httpwire.Endpoint{Name: name, Method: http.MethodGet, Path: path, Params: params, Query: query})
	}
	runRefusals(t, map[string]refusal{
		"a list in the path":      {list("holders.path", reflect.TypeFor[listParams](), nil), []string{`row "holders.path"`, `"path"`, "query"}},
		"a list behind a pointer": {list("holders.pointer", nil, reflect.TypeFor[pointerHolders]()), []string{`row "holders.pointer"`, `"path"`, "pointer"}},
		"a list with no MaxItems": {list("holders.nocap", nil, reflect.TypeFor[noCapQuery]()), []string{`row "holders.nocap"`, `"path"`}},
		"a list of floats":        {list("holders.floats", nil, reflect.TypeFor[floatQuery]()), []string{`row "holders.floats"`, `"path"`}},
		"a list of no items":      {list("holders.zero", nil, reflect.TypeFor[zeroCapQuery]()), []string{`row "holders.zero"`, "MaxItems"}},
	})
	tb := fixtureTable(t)
	addRow(httpwire.Endpoint{Name: "holders.optional", Method: http.MethodGet, Path: "/api/holders", Query: reflect.TypeFor[optionalHolders]()})(tb)
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Errorf("Derive of an optional query list = %v, want nil", err)
	}
}
