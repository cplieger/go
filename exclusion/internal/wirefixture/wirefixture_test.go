package wirefixture_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/go/exclusion"
	"github.com/cplieger/go/exclusion/internal/wirefixture"
	"github.com/cplieger/go/httpwire"
)

func TestTableDerives(t *testing.T) {
	tab := wirefixture.Table()
	if tab == nil || tab.Catalog == nil {
		t.Fatalf("Table() = %+v, want a table with its catalog", tab)
	}
	t.Run("the codes are in the catalog", func(t *testing.T) {
		for code, status := range map[string]int{"exclusion_invalid": http.StatusUnprocessableEntity, "auth_required": http.StatusUnauthorized} {
			if s, ok := tab.Catalog.Lookup(code); !ok || s.Status != status {
				t.Errorf("Lookup(%q) = %+v, %v; want status %d", code, s, ok, status)
			}
		}
	})
	t.Run("the row's union holds its code and the guard's", func(t *testing.T) {
		d, err := httpwire.Derive(tab, wirefixture.Guard{Catalog: tab.Catalog}.Codes)
		if err != nil {
			t.Fatalf("Derive: %v", err)
		}
		if len(d.Endpoints) != 1 || d.Endpoints[0].Name != wirefixture.Row || d.Endpoints[0].Method != http.MethodPost {
			t.Fatalf("Endpoints = %+v, want the one POST row %s", d.Endpoints, wirefixture.Row)
		}
		for _, code := range []string{"exclusion_invalid", "auth_required"} {
			if !slices.Contains(d.Endpoints[0].Errors, code) {
				t.Errorf("Errors = %v, lacks %s", d.Endpoints[0].Errors, code)
			}
		}
	})
	t.Run("the field set holds every kind", func(t *testing.T) {
		kinds := map[exclusion.Kind]bool{}
		for _, f := range wirefixture.Fields {
			kinds[f.Kind] = true
		}
		if len(kinds) != 4 {
			t.Errorf("Fields cover %d kinds, want the four", len(kinds))
		}
	})
}

func TestGuardAdmitsASession(t *testing.T) {
	tab := wirefixture.Table()
	if tab == nil {
		t.Fatal("Table() = nil")
	}
	g := wirefixture.Guard{Catalog: tab.Catalog}
	row := &tab.Endpoints[0]
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	t.Run("a session passes", func(t *testing.T) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/exclusion/matches", nil)
		r.Header.Set("Principal", "session")
		w := httptest.NewRecorder()
		g.Wrap(row, next).ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", w.Code)
		}
		if s := g.Shape(r); s.Principal != httpwire.ShapeSession {
			t.Errorf("Shape = %+v, want a session", s)
		}
	})
	t.Run("anyone else answers auth_required", func(t *testing.T) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/exclusion/matches", nil)
		w := httptest.NewRecorder()
		g.Wrap(row, next).ServeHTTP(w, r)
		var env struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != http.StatusUnauthorized || env.Code != "auth_required" {
			t.Errorf("answer %d %s (%v), want 401 auth_required", w.Code, w.Body.String(), err)
		}
		if s := g.Shape(r); s.Principal != httpwire.ShapeAnonymous {
			t.Errorf("Shape = %+v, want anonymous", s)
		}
	})
	t.Run("the shapes are anonymous and a session", func(t *testing.T) {
		want := []httpwire.ShapeKind{httpwire.ShapeAnonymous, httpwire.ShapeSession}
		var got []httpwire.ShapeKind
		for _, s := range g.Shapes() {
			got = append(got, s.Principal)
		}
		if !slices.Equal(got, want) {
			t.Errorf("Shapes = %v, want %v", got, want)
		}
	})
}
