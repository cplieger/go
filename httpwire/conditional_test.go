package httpwire_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// conditionalTable holds a Conditional Raw GET row of two media types and
// a plain Raw GET row.
func conditionalTable() *httpwire.Table {
	raw := func(name, path, summary string, types ...string) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: summary + ".",
			Kind: httpwire.Raw, ContentTypes: types, Access: public,
		}
	}
	poster := raw("poster.get", "/poster", "Read the poster", "image/jpeg", "image/webp")
	poster.Conditional = true
	return &httpwire.Table{Prefix: "/api", Catalog: httpwire.NewCatalog(), Endpoints: []httpwire.Endpoint{
		poster, raw("poster.plain", "/plain", "Read the plain poster", "image/png"),
	}}
}

func conditionalDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/conditional.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// editConditionalDoc answers the document after f edits the operation of
// one method at path.
func editConditionalDoc(t *testing.T, path string, f func(op map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(conditionalDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	f(doc["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any))
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// conditionalAPI binds both rows to h, logging to the returned sink.
func conditionalAPI(t *testing.T, h http.HandlerFunc) (*httpwire.API, *logSink) {
	t.Helper()
	sink := &logSink{}
	api, err := httpwire.New(conditionalTable(), []httpwire.Binding{
		httpwire.BindRaw("poster.get", h), httpwire.BindRaw("poster.plain", h),
	}, httpwire.WithSchema(conditionalDoc(t)), httpwire.WithLogger(slog.New(sink)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api, sink
}

func TestDeriveAcceptsContentTypesAndConditional(t *testing.T) {
	if _, err := httpwire.Derive(conditionalTable(), nil); err != nil {
		t.Fatalf("Derive: %v", err)
	}
}

func TestDeriveRefusesContentTypesAndConditional(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"raw with an empty type":    {edit("archive.export", func(e *httpwire.Endpoint) { e.ContentTypes = []string{""} }), []string{`row "archive.export"`, "ContentTypes"}},
		"raw with a malformed type": {edit("archive.export", func(e *httpwire.Endpoint) { e.ContentTypes = []string{"zip"} }), []string{`row "archive.export"`, `"zip"`}},
		"raw with a type twice": {edit("archive.export", func(e *httpwire.Endpoint) {
			e.ContentTypes = []string{"application/zip", "image/png", "Application/ZIP"}
		}), []string{`row "archive.export"`, "twice"}},
		"raw with an empty set":        {edit("archive.export", func(e *httpwire.Endpoint) { e.ContentTypes = []string{} }), []string{`row "archive.export"`, "ContentTypes"}},
		"conditional on a JSON row":    {edit("jobs.list", func(e *httpwire.Endpoint) { e.Conditional = true }), []string{`row "jobs.list"`, "Conditional"}},
		"conditional on a Raw POST":    {edit("archive.export", func(e *httpwire.Endpoint) { e.Conditional = true }), []string{`row "archive.export"`, "Conditional"}},
		"conditional on a Stream row":  {edit("live.stream", func(e *httpwire.Endpoint) { e.Conditional = true }), []string{`row "live.stream"`, "Conditional"}},
		"conditional on a Redirect":    {edit("auth.sso", func(e *httpwire.Endpoint) { e.Conditional = true }), []string{`row "auth.sso"`, "Conditional"}},
		"conditional on a mount write": {edit("auth.keyCreate", func(e *httpwire.Endpoint) { e.Conditional = true }), []string{`row "auth.keyCreate"`, "Conditional"}},
	})
}

func TestNewHoldsContentTypesAndConditionalToTheDocument(t *testing.T) {
	responses := func(op map[string]any) map[string]any { return op["responses"].(map[string]any) }
	content := func(op map[string]any) map[string]any {
		return responses(op)["200"].(map[string]any)["content"].(map[string]any)
	}
	for name, tc := range map[string]struct {
		doc  func(t *testing.T) []byte
		want string
	}{
		"a type the document lacks": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/poster", func(op map[string]any) { delete(content(op), "image/webp") })
		}, `row "poster.get" answers image/webp, but the document's 200 does not declare it`},
		"a type the row lacks": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/plain", func(op map[string]any) { content(op)["image/gif"] = map[string]any{} })
		}, `row "poster.plain": the document's 200 declares image/gif, which is not one of the row's ContentTypes`},
		"no 304 on a Conditional row": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/poster", func(op map[string]any) { delete(responses(op), "304") })
		}, `row "poster.get" is Conditional, but the document's operation declares no 304`},
		"a 304 with content": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/poster", func(op map[string]any) {
				responses(op)["304"].(map[string]any)["content"] = map[string]any{"image/jpeg": map[string]any{}}
			})
		}, `row "poster.get": the document's 304 declares a body`},
		"no If-None-Match on a Conditional row": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/poster", func(op map[string]any) { delete(op, "parameters") })
		}, `row "poster.get" is Conditional, but the document's operation takes no If-None-Match header`},
		"a required If-None-Match": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/poster", func(op map[string]any) {
				op["parameters"].([]any)[0].(map[string]any)["required"] = true
			})
		}, `row "poster.get": the document's If-None-Match header parameter is required`},
		"a 304 on a plain row": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/plain", func(op map[string]any) {
				responses(op)["304"] = map[string]any{"description": "Not Modified"}
			})
		}, `row "poster.plain" is not Conditional, but the document's operation declares a 304`},
		"If-None-Match on a plain row": {func(t *testing.T) []byte {
			return editConditionalDoc(t, "/api/plain", func(op map[string]any) {
				op["parameters"] = []any{map[string]any{"in": "header", "name": "if-none-match", "required": false, "schema": map[string]any{"type": "string"}}}
			})
		}, `row "poster.plain" is not Conditional, but the document's operation takes an If-None-Match header`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := httpwire.New(conditionalTable(), nil, httpwire.WithSchema(tc.doc(t)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("New = %v, want %q", err, tc.want)
			}
		})
	}
}

// answers writes the status and ETag named by the request's Want header,
// with a body on a 200.
func answers(w http.ResponseWriter, r *http.Request) {
	status, etag, _ := strings.Cut(r.Header.Get("Want"), " ")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if status == "304" {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	_, _ = w.Write([]byte("RIFF"))
}

func TestConditionalRowLogsAnAnswerOffItsRule(t *testing.T) {
	for name, tc := range map[string]struct {
		path, want, ifNoneMatch string
		status                  int
		logged                  string
	}{
		"a 200 with an ETag":                     {"/api/poster", `200 "v1"`, "", http.StatusOK, ""},
		"a 304 to a matching If-None-Match":      {"/api/poster", `304 "v1"`, `"v1"`, http.StatusNotModified, ""},
		"a 200 with no ETag":                     {"/api/poster", "200", "", http.StatusOK, "no ETag"},
		"a 304 to a request with none":           {"/api/poster", `304 "v1"`, "", http.StatusNotModified, "no If-None-Match"},
		"a 304 on a row that is not Conditional": {"/api/plain", `304 "v1"`, `"v1"`, http.StatusNotModified, "not Conditional"},
		"a plain 200 with no ETag":               {"/api/plain", "200", "", http.StatusOK, ""},
	} {
		t.Run(name, func(t *testing.T) {
			api, sink := conditionalAPI(t, answers)
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.Header.Set("Want", tc.want)
			if tc.ifNoneMatch != "" {
				r.Header.Set("If-None-Match", tc.ifNoneMatch)
			}
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, r)
			if rec.Code != tc.status {
				t.Errorf("answer %d, want the handler's %d written as it is", rec.Code, tc.status)
			}
			switch logged := sink.has("ERROR", "row="); {
			case tc.logged == "" && logged:
				t.Errorf("logged %v, want nothing", sink.all())
			case tc.logged != "" && !sink.has("ERROR", tc.logged, "row="):
				t.Errorf("logged %v, want an ERROR naming the row and %q", sink.all(), tc.logged)
			}
		})
	}
}
