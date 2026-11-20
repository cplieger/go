package httpwire_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/go/httpwire"
)

func inKind(k itemKind) bool { return k == "book" || k == "disc" }

func inLabel(l label) bool { return len(l) <= 8 && utf8.ValidString(string(l)) }

// clientAnswer fails t unless rec is a success or a 4xx envelope: a request
// the client got wrong is never answered as a server fault.
func clientAnswer(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code/100 == 2 {
		return
	}
	if rec.Code/100 != 4 {
		t.Errorf("%s answered %d %q, want a success or a 4xx refusal", what, rec.Code, rec.Body)
		return
	}
	if _, err := httpwire.DecodeEnvelope(rec.Body.Bytes()); err != nil {
		t.Errorf("%s answered %d with a body that is not an envelope: %v", what, rec.Code, err)
	}
}

// FuzzDecode_acceptedValuesInBounds serves arbitrary path values, queries and
// bodies: no input panics, each answer is a success or a 4xx envelope, and
// every value a handler receives is within the bounds its type and the
// document declare.
func FuzzDecode_acceptedValuesInBounds(f *testing.F) {
	f.Add("book", "7", "limit=5&kind=disc&big=1&flag=true&keys=a,b", []byte(validItem))
	f.Add("tape", "07", "limit=0&limit=1&zz", []byte(`{"name":"abcdefghi"}`))
	f.Add("%FF", "-1", "limit=5;x", []byte(`{"name":"a","kind":"book","n":1,"copies":"5x","size":1,"tags":null,"shape":{"type":"square","side":1,"radius":2}}`))
	f.Add("disc", "100", "kind=%FF&limit=100", []byte(`{"slots":{"x":"s"},"tags":["a","b","c","d"]}`))
	f.Add("book/..", "", "limit=1e2&big=-0&flag=yes", []byte{0x00, 0xff, 0xfe})
	f.Fuzz(func(t *testing.T, kind, n, query string, body []byte) {
		d := newDecodeAPI(t, decodeDoc(t))
		get := httptest.NewRequest(http.MethodGet, "/api/items", nil)
		get.URL.Path, get.URL.RawPath = "/items/"+kind+"/"+n, ""
		rec := httptest.NewRecorder()
		d.api.ServeHTTP(rec, get)
		clientAnswer(t, "the path", rec)
		if p := d.params; p != nil && (!inKind(p.Kind) || p.N < 1 || p.N > 100) {
			t.Errorf("Params outside their bounds: %+v", *p)
		}
		list := httptest.NewRequest(http.MethodGet, "/api/items", nil)
		list.URL.RawQuery = query
		rec = httptest.NewRecorder()
		d.api.ServeHTTP(rec, list)
		clientAnswer(t, "the query", rec)
		if q := d.query; q != nil && (q.Limit < 1 || q.Limit > 100 || q.Kind != nil && !inKind(*q.Kind) || len(q.Keys) > 32) {
			t.Errorf("Query outside its bounds: %+v", *q)
		}
		rec = httptest.NewRecorder()
		d.api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/items", bytes.NewReader(body)))
		clientAnswer(t, "the body", rec)
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
