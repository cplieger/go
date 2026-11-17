package httpwire_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"
)

func inKind(k itemKind) bool { return k == "book" || k == "disc" }

func inLabel(l label) bool { return len(l) <= 8 && utf8.ValidString(string(l)) }

// FuzzDecode_acceptedValuesInBounds serves arbitrary path values, queries and
// bodies: no input panics, and every value a handler receives is within the
// bounds its type and the document declare.
func FuzzDecode_acceptedValuesInBounds(f *testing.F) {
	f.Add("book", "7", "limit=5&kind=disc&big=1&flag=true&keys=a,b", []byte(validItem))
	f.Add("tape", "07", "limit=0&limit=1&zz", []byte(`{"name":"abcdefghi"}`))
	f.Add("%FF", "-1", "limit=5;x", []byte(`{"name":"a","kind":"book","n":1,"copies":"5x","size":1,"tags":null,"shape":{"type":"square","side":1,"radius":2}}`))
	f.Add("disc", "100", "kind=%FF&limit=100", []byte(`{"slots":{"x":"s"},"tags":["a","b","c","d"]}`))
	f.Fuzz(func(t *testing.T, kind, n, query string, body []byte) {
		d := newDecodeAPI(t, decodeDoc(t))
		get := httptest.NewRequest(http.MethodGet, "/api/items", nil)
		get.URL.Path, get.URL.RawPath = "/api/items/"+kind+"/"+n, ""
		d.api.ServeHTTP(httptest.NewRecorder(), get)
		if p := d.params; p != nil && (!inKind(p.Kind) || p.N < 1 || p.N > 100) {
			t.Errorf("Params outside their bounds: %+v", *p)
		}
		list := httptest.NewRequest(http.MethodGet, "/api/items", nil)
		list.URL.RawQuery = query
		d.api.ServeHTTP(httptest.NewRecorder(), list)
		if q := d.query; q != nil && (q.Limit < 1 || q.Limit > 100 || q.Kind != nil && !inKind(*q.Kind) || len(q.Keys) > 32) {
			t.Errorf("Query outside its bounds: %+v", *q)
		}
		d.api.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/items", bytes.NewReader(body)))
		b := d.body
		if b == nil {
			return
		}
		if !inLabel(b.Name) || !inKind(b.Kind) || b.N < 1 || b.N > 100 || b.Size < 0 || b.Size > 1<<53 || len(b.Tags) < 1 || len(b.Tags) > 3 || len(b.Keys) > 32 {
			t.Errorf("Body outside its bounds: %+v", *b)
		}
		for _, tag := range b.Tags {
			if !inLabel(tag) {
				t.Errorf("tag %q outside its bound", tag)
			}
		}
		for _, s := range b.Slots {
			if !inLabel(s) {
				t.Errorf("slot %q outside its bound", s)
			}
		}
		if b.Shape.Type == "circle" && (b.Shape.Radius == nil || b.Shape.Side != nil) || b.Shape.Type == "square" && (b.Shape.Side == nil || b.Shape.Radius != nil) ||
			b.Shape.Type != "circle" && b.Shape.Type != "square" {
			t.Errorf("Shape outside its union: %+v", b.Shape)
		}
	})
}
