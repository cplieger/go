package httpwiretest_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/httpwiretest"
)

// poster is how the fixture's handler answers a Conditional row: the ETag
// it sets, the media type of a 200, and whether it reads If-None-Match.
type poster struct {
	etag, media string
	body304     string
	always304   bool
	ignoreINM   bool
}

func (p poster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.etag != "" {
		w.Header().Set("ETag", p.etag)
	}
	if p.always304 || !p.ignoreINM && p.etag != "" && r.Header.Get("If-None-Match") == p.etag {
		w.WriteHeader(http.StatusNotModified)
		_, _ = w.Write([]byte(p.body304))
		return
	}
	w.Header().Set("Content-Type", p.media)
	_, _ = w.Write([]byte("RIFF"))
}

// conditionalAPI serves a Conditional row of two media types and a plain
// Raw row, both answered by p.
func conditionalAPI(t *testing.T, p poster) *httpwire.API {
	t.Helper()
	doc, err := os.ReadFile("testdata/conditional.json")
	if err != nil {
		t.Fatal(err)
	}
	raw := func(name, path, summary string, types ...string) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: summary + ".",
			Kind: httpwire.Raw, ContentTypes: types, Access: httpwire.Access{Class: httpwire.Public},
		}
	}
	get := raw("poster.get", "/poster", "Read the poster", "image/jpeg", "image/webp")
	get.Conditional = true
	tb := &httpwire.Table{Catalog: httpwire.NewCatalog(), Prefix: "/api", Endpoints: []httpwire.Endpoint{
		get, raw("poster.plain", "/plain", "Read the plain poster", "image/png"),
	}}
	api, err := httpwire.New(tb, []httpwire.Binding{httpwire.BindRaw("poster.get", p), httpwire.BindRaw("poster.plain", p)}, httpwire.WithSchema(doc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api
}

func TestContractPassesAConditionalRowThatFollowsItsRule(t *testing.T) {
	api := conditionalAPI(t, poster{etag: `"v1"`, media: "image/webp"})
	cases := []httpwiretest.Case{
		{Row: "poster.get", Request: request(t, http.MethodGet, "/api/poster", "")},
		{Row: "poster.get", Request: request(t, http.MethodHead, "/api/poster", "")},
	}
	matching := request(t, http.MethodGet, "/api/poster", "")
	matching.Header.Set("If-None-Match", `"v1"`)
	cases = append(cases, httpwiretest.Case{Row: "poster.get", Request: matching})
	r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, api, cases) })
	for _, s := range r.failures {
		t.Errorf("Contract failed a correct answer: %s", s)
	}
}

func TestContractFailsAConditionalRowOffItsRule(t *testing.T) {
	for name, tc := range map[string]struct {
		p           poster
		row, path   string
		ifNoneMatch string
		want        []string
	}{
		"a 200 with no ETag":                       {p: poster{media: "image/webp"}, want: []string{"200", "no ETag"}},
		"an ETag that is no entity-tag":            {p: poster{etag: "v1", media: "image/webp"}, want: []string{`ETag "v1"`, "entity-tag"}},
		"a matching If-None-Match answered 200":    {p: poster{etag: `"v1"`, media: "image/webp", ignoreINM: true}, want: []string{`If-None-Match "v1"`, "200", "want 304"}},
		"a 304 to a request with no If-None-Match": {p: poster{etag: `"v1"`, always304: true}, want: []string{"304", "no If-None-Match"}},
		"a 304 with a body": {
			p: poster{etag: `"v1"`, media: "image/webp", body304: "RIFF"}, ifNoneMatch: `"v1"`, want: []string{"304", "body"},
		},
		"a 200 of a type outside the set": {p: poster{etag: `"v1"`, media: "image/png"}, want: []string{"Content-Type", "image/png"}},
		"a 304 on a row that is not Conditional": {
			p: poster{always304: true}, row: "poster.plain", path: "/api/plain", ifNoneMatch: `"v1"`, want: []string{"304", "not Conditional"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := conditionalAPI(t, tc.p)
			row, path := "poster.get", "/api/poster"
			if tc.row != "" {
				row, path = tc.row, tc.path
			}
			req := request(t, http.MethodGet, path, "")
			if tc.ifNoneMatch != "" {
				req.Header.Set("If-None-Match", tc.ifNoneMatch)
			}
			r := record(t, func(tb testing.TB) { httpwiretest.Contract(tb, api, []httpwiretest.Case{{Row: row, Request: req}}) })
			if !r.failed(append([]string{`row "` + row + `"`}, tc.want...)...) {
				t.Errorf("failures %q; want one naming row %q with %q", r.failures, row, tc.want)
			}
		})
	}
}
