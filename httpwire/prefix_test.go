package httpwire_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// prefixTable holds a row whose Path is relative to Prefix and one whose
// Path repeats Prefix, which is not stripped.
func prefixTable() *httpwire.Table {
	raw := func(name, path, summary, description string) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: description,
			Kind: httpwire.Raw, ContentTypes: []string{"text/plain"}, Access: public,
		}
	}
	return &httpwire.Table{Prefix: "/api", Catalog: httpwire.NewCatalog(), Endpoints: []httpwire.Endpoint{
		raw("notes.read", "/notes", "Read the notes", "Reads the notes."),
		raw("notes.again", "/api/notes", "Read the notes again", "Reads the notes again."),
	}}
}

func prefixBindings() []httpwire.Binding {
	answer := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(body))
		})
	}
	return []httpwire.Binding{
		httpwire.BindRaw("notes.read", answer("read")),
		httpwire.BindRaw("notes.again", answer("again")),
	}
}

func prefixDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/prefix.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRowPathIsUnderPrefix(t *testing.T) {
	api, err := httpwire.New(prefixTable(), prefixBindings(), httpwire.WithSchema(prefixDoc(t)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/notes", "read"},
		{"/api/api/notes", "again"},
	} {
		if rec := serve(api, http.MethodGet, tc.path, ""); rec.Code != http.StatusOK || rec.Body.String() != tc.body {
			t.Errorf("GET %s = %d %q, want 200 %q", tc.path, rec.Code, rec.Body.String(), tc.body)
		}
	}
	if rec := serve(api, http.MethodGet, "/notes", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /notes, a row's Path without its Prefix = %d, want 404", rec.Code)
	}
}

func TestDeriveAcceptsAPathUnderPrefix(t *testing.T) {
	if _, err := httpwire.Derive(prefixTable(), nil); err != nil {
		t.Fatalf("Derive: %v", err)
	}
}

// The document writes each operation at Prefix+Path, so a document still
// naming the row's own Path is refused.
func TestNewHoldsTheDocumentToTheFullPath(t *testing.T) {
	doc := strings.Replace(string(prefixDoc(t)), `"/api/notes"`, `"/notes"`, 1)
	_, err := httpwire.New(prefixTable(), prefixBindings(), httpwire.WithSchema([]byte(doc)))
	want := `row "notes.read" is GET /api/notes, but the document's operation is GET /notes`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("New over a document at /notes = %v, want %q", err, want)
	}
}

func TestDeriveRefusesARowPathOffThePattern(t *testing.T) {
	for name, path := range map[string]string{
		"no leading slash": "notes",
		"empty":            "",
		"only a slash":     "/",
		"trailing slash":   "/notes/",
		"empty segment":    "//notes",
	} {
		t.Run(name, func(t *testing.T) {
			tb := prefixTable()
			tb.Endpoints[0].Path = path
			_, err := httpwire.Derive(tb, nil)
			if err == nil || !strings.Contains(err.Error(), `row "notes.read"`) || !strings.Contains(err.Error(), "path") {
				t.Fatalf("Derive with Path %q = %v, want a refusal of row notes.read's path", path, err)
			}
		})
	}
}
