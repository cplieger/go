package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// crossOriginRequest builds a request to example.com carrying the named
// Sec-Fetch-Site and Origin values; an empty value leaves the header off.
func crossOriginRequest(method, site, origin string) *http.Request {
	r := httptest.NewRequest(method, "http://example.com/api/thing", http.NoBody)
	if site != "" {
		r.Header.Set("Sec-Fetch-Site", site)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestCrossOriginRefusesUnsafeCrossSite(t *testing.T) {
	cases := []struct {
		name, method, site, origin string
	}{
		{"POST cross-site", http.MethodPost, "cross-site", "https://evil.example"},
		{"PUT cross-site", http.MethodPut, "cross-site", "https://evil.example"},
		{"PATCH cross-site", http.MethodPatch, "cross-site", "https://evil.example"},
		{"DELETE cross-site", http.MethodDelete, "cross-site", "https://evil.example"},
		{"POST same-site", http.MethodPost, "same-site", "https://sub.example.com"},
		{"POST foreign Origin without Sec-Fetch-Site", http.MethodPost, "", "https://evil.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })
			rec := httptest.NewRecorder()
			httpserver.CrossOrigin(next, nil).ServeHTTP(rec, crossOriginRequest(tc.method, tc.site, tc.origin))
			if ran {
				t.Fatal("the wrapped handler ran for a refused request")
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			env := strictEnvelope(t, rec)
			if env.Code != "cross_origin_refused" {
				t.Errorf("code = %q, want cross_origin_refused", env.Code)
			}
			if want := httpserver.CodeSpecs()["cross_origin_refused"].Remediation; env.Remediation != want {
				t.Errorf("remediation = %q, want %q", env.Remediation, want)
			}
		})
	}
}

func TestCrossOriginPassesSameOriginAndSafeMethods(t *testing.T) {
	cases := []struct {
		name, method, site, origin string
	}{
		{"POST same-origin", http.MethodPost, "same-origin", "http://example.com"},
		{"POST typed by the user", http.MethodPost, "none", ""},
		{"POST Origin equal to Host", http.MethodPost, "", "http://example.com"},
		{"POST with neither header", http.MethodPost, "", ""},
		{"GET cross-site", http.MethodGet, "cross-site", "https://evil.example"},
		{"HEAD cross-site", http.MethodHead, "cross-site", "https://evil.example"},
		{"OPTIONS cross-site", http.MethodOptions, "cross-site", "https://evil.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				ran = true
				w.WriteHeader(http.StatusNoContent)
			})
			rec := httptest.NewRecorder()
			httpserver.CrossOrigin(next, nil).ServeHTTP(rec, crossOriginRequest(tc.method, tc.site, tc.origin))
			if !ran || rec.Code != http.StatusNoContent {
				t.Errorf("ran = %v, status = %d; want the wrapped handler's 204", ran, rec.Code)
			}
		})
	}
}

func TestCrossOriginCallsResponderOnRefusal(t *testing.T) {
	type call struct {
		status int
		code   httpserver.ErrorCode
	}
	var calls []call
	errs := func(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, msg string) {
		calls = append(calls, call{status, code})
		httpserver.WriteError(w, r, status, code, msg)
	}
	h := httpserver.CrossOrigin(http.NotFoundHandler(), errs)

	h.ServeHTTP(httptest.NewRecorder(), crossOriginRequest(http.MethodGet, "cross-site", "https://evil.example"))
	if len(calls) != 0 {
		t.Fatalf("responder ran for an admitted GET: %v", calls)
	}
	h.ServeHTTP(httptest.NewRecorder(), crossOriginRequest(http.MethodPost, "cross-site", "https://evil.example"))
	if len(calls) != 1 || calls[0] != (call{http.StatusForbidden, "cross_origin_refused"}) {
		t.Fatalf("responder calls = %v, want one 403 cross_origin_refused", calls)
	}
}
