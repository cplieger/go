package httpwire_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// RFC 9110 section 15.5.6: a 405 carries Allow, listing the methods the
// target resource supports; a GET row also answers HEAD.
func TestRoutingAnswersMethodNotAllowedWithAllow(t *testing.T) {
	api := newCase(t).mustBuild(t)
	for name, tc := range map[string]struct{ method, path, allow string }{
		"a read path":                    {http.MethodPost, "/api/jobs/abc", "GET, HEAD"},
		"a path with a read and a write": {http.MethodDelete, "/api/jobs", "GET, HEAD, POST"},
		"a write path":                   {http.MethodGet, "/api/settings", "PATCH"},
		"a mount path":                   {http.MethodDelete, "/api/auth/keys", "GET, HEAD, POST"},
		"an OPTIONS request":             {http.MethodOptions, "/api/jobs", "GET, HEAD, POST"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(api, tc.method, tc.path, "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Errorf("Allow = %q, want %q", got, tc.allow)
			}
			if env := envelopeOf(t, rec); env.Code != "method_not_allowed" {
				t.Errorf("code = %q, want method_not_allowed", env.Code)
			}
		})
	}
}

func TestRoutingAnswersNotFound(t *testing.T) {
	api := newCase(t).mustBuild(t)
	for name, path := range map[string]string{
		"an unknown path":        "/api/nothing",
		"the prefix":             "/api",
		"the prefix with slash":  "/api/",
		"outside the prefix":     "/other",
		"a trailing slash":       "/api/jobs/",
		"a deeper path":          "/api/jobs/a/b",
		"an empty segment":       "/api//jobs",
		"a dot segment":          "/api/./jobs",
		"a mount path's subpath": "/api/auth/keys/x",
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(api, http.MethodGet, path, "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if rec.Header().Get("Allow") != "" || rec.Header().Get("Location") != "" {
				t.Errorf("a not_found answer carries Allow %q, Location %q", rec.Header().Get("Allow"), rec.Header().Get("Location"))
			}
			if env := envelopeOf(t, rec); env.Code != "not_found" {
				t.Errorf("code = %q, want not_found", env.Code)
			}
		})
	}
}

func TestRoutingReachesEachKindOfRow(t *testing.T) {
	c := newCase(t)
	c.tb.Mounts[0].Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Mounted", r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	api := c.mustBuild(t)
	t.Run("a JSON row", func(t *testing.T) {
		if rec := serve(api, http.MethodGet, "/api/jobs/abc", ""); rec.Code != http.StatusOK || rec.Body.String() != "{\"id\":\"one\"}\n" {
			t.Errorf("GET jobs.get = %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a GET row answers HEAD", func(t *testing.T) {
		if rec := serve(api, http.MethodHead, "/api/jobs/abc", ""); rec.Code != http.StatusOK {
			t.Errorf("HEAD jobs.get = %d", rec.Code)
		}
	})
	t.Run("a Raw row", func(t *testing.T) {
		rec := serve(api, http.MethodPost, "/api/archive", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "PK" || rec.Header().Get("Content-Type") != "application/zip" {
			t.Errorf("POST archive.export = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	})
	t.Run("a Redirect row", func(t *testing.T) {
		rec := serve(api, http.MethodGet, "/api/sso", "")
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/signed-in" {
			t.Errorf("GET auth.sso = %d Location %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("a Stream row", func(t *testing.T) {
		rec := serve(api, http.MethodGet, "/api/stream", "")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
			t.Errorf("GET live.stream = %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
	})
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		t.Run("a mount row by "+m, func(t *testing.T) {
			rec := serve(api, m, "/api/auth/keys", "")
			if got := rec.Header().Get("Mounted"); got != m+" /api/auth/keys" {
				t.Errorf("%s auth.keys reached the mount as %q", m, got)
			}
		})
	}
}

func TestRoutingAMatchedWildcardIsTheRow(t *testing.T) {
	c := newCase(t)
	var got string
	c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		got = "reached"
		return job{}, nil
	})
	api := c.mustBuild(t)
	if rec := serve(api, http.MethodGet, "/api/jobs/a%2Fb", ""); rec.Code != http.StatusOK || got != "reached" {
		t.Errorf("an escaped slash inside one segment answered %d, handler %q", rec.Code, got)
	}
}
