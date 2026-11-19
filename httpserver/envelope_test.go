package httpserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
)

// strictEnvelope decodes rec's body as the v2 error envelope: exactly the keys
// code, message, remediation and request_id, each a string, a well-formed
// non-empty code, and a request_id equal to the X-Request-ID header.
func strictEnvelope(t *testing.T, rec *httptest.ResponseRecorder) httpserver.ErrorResponse {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", rec.Body.String(), err)
	}
	want := []string{"code", "message", "remediation", "request_id"}
	if len(raw) != len(want) {
		t.Errorf("envelope keys = %v, want exactly %v", raw, want)
	}
	for _, k := range want {
		if _, ok := raw[k].(string); !ok {
			t.Errorf("envelope key %q = %#v, want a string", k, raw[k])
		}
	}
	var env httpserver.ErrorResponse
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Code == "" || strings.Trim(string(env.Code), "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		t.Errorf("code = %q, want a lowercase snake-case token", env.Code)
	}
	if env.RequestID == "" {
		t.Error("request_id is empty")
	}
	if h := rec.Header().Get(httpserver.HeaderRequestID); h != env.RequestID {
		t.Errorf("%s header = %q, want the body's request_id %q", httpserver.HeaderRequestID, h, env.RequestID)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	return env
}

func TestCodeSpecsMatchSource(t *testing.T) {
	httpservertest.CheckCodeSpecs(t, ".", httpserver.CodeSpecs())
}

// The statuses, NoEffect flags and the two fixed remediations are the ones
// the envelope contract assigns to each code this package owns.
func TestCodeSpecsDeclaredValues(t *testing.T) {
	specs := httpserver.CodeSpecs()
	cases := []struct {
		code        httpserver.ErrorCode
		status      int
		noEffect    bool
		remediation string
	}{
		{code: "internal_error", status: 500},
		{code: "method_not_allowed", status: 405},
		{code: "bad_request", status: 400},
		{code: "loopback_only", status: 403},
		{code: "too_many_auth_failures", status: 429, noEffect: true},
		{code: "rate_limited", status: 429, noEffect: true},
		{code: "host_not_allowed", status: 403, remediation: "Use one of the server's configured host names."},
		{code: "timeout", status: 503, remediation: "Retry the request; it may have run."},
		{code: "not_found", status: 404},
		{code: "ui_unbuilt", status: 503},
		{code: "cross_origin_refused", status: 403, noEffect: true},
		{code: "precondition_failed", status: 412, noEffect: true},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			s, ok := specs[tc.code]
			if !ok {
				t.Fatalf("CodeSpecs has no %q", tc.code)
			}
			if s.Status != tc.status || s.NoEffect != tc.noEffect {
				t.Errorf("spec = %+v, want status %d, NoEffect %v", s, tc.status, tc.noEffect)
			}
			if s.Remediation == "" {
				t.Error("remediation is empty")
			}
			if tc.remediation != "" && s.Remediation != tc.remediation {
				t.Errorf("remediation = %q, want %q", s.Remediation, tc.remediation)
			}
		})
	}
	t.Run("invalid_error_code is not owned", func(t *testing.T) {
		if _, ok := specs[httpserver.InvalidErrorCode]; ok {
			t.Error("CodeSpecs holds invalid_error_code, which no writer passes")
		}
	})
	t.Run("a returned map is a copy", func(t *testing.T) {
		delete(specs, "timeout")
		if _, ok := httpserver.CodeSpecs()["timeout"]; !ok {
			t.Error("deleting from one CodeSpecs result changed the next one")
		}
	})
}

func TestRecovererAnswersInternalError(t *testing.T) {
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := serve(httpserver.Recoverer(httpserver.WithRecoverLogger(discardLogger()))(panicky), http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	env := strictEnvelope(t, rec)
	if env.Code != "internal_error" || env.Message != "internal error" {
		t.Errorf("envelope = %+v, want internal_error saying %q", env, "internal error")
	}
	if want := httpserver.CodeSpecs()["internal_error"].Remediation; want == "" || env.Remediation != want {
		t.Errorf("remediation = %q, want the CodeSpecs default %q", env.Remediation, want)
	}
}

func TestHostPolicyRefusalIsEnvelope(t *testing.T) {
	refused := func(t *testing.T, p *httpserver.HostPolicy) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "evil.example"
		rec := httptest.NewRecorder()
		p.Middleware()(okHandler()).ServeHTTP(rec, req)
		return rec
	}
	t.Run("default answer", func(t *testing.T) {
		p, _ := httpserver.ParseHostList([]string{"app.example"}, httpserver.WithHostAllowlistLogger(discardLogger()))
		rec := refused(t, p)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
		env := strictEnvelope(t, rec)
		if env.Code != "host_not_allowed" || env.Message != "host not allowed" {
			t.Errorf("envelope = %+v, want host_not_allowed", env)
		}
		if env.Remediation != "Use one of the server's configured host names." {
			t.Errorf("remediation = %q", env.Remediation)
		}
	})
	t.Run("WithHostResponder writes the refusal", func(t *testing.T) {
		var gotStatus int
		var gotCode httpserver.ErrorCode
		var gotMsg string
		responder := func(w http.ResponseWriter, _ *http.Request, status int, code httpserver.ErrorCode, msg string) {
			gotStatus, gotCode, gotMsg = status, code, msg
			w.WriteHeader(status)
			_, _ = w.Write([]byte("app body"))
		}
		p, _ := httpserver.ParseHostList([]string{"app.example"},
			httpserver.WithHostAllowlistLogger(discardLogger()), httpserver.WithHostResponder(responder))
		rec := refused(t, p)
		if gotStatus != http.StatusForbidden || gotCode != "host_not_allowed" || gotMsg != "host not allowed" {
			t.Errorf("responder got (%d, %q, %q), want (403, host_not_allowed, host not allowed)", gotStatus, gotCode, gotMsg)
		}
		if rec.Body.String() != "app body" {
			t.Errorf("body = %q, want the responder's", rec.Body.String())
		}
	})
	t.Run("a nil responder keeps the default", func(t *testing.T) {
		p, _ := httpserver.ParseHostList([]string{"app.example"},
			httpserver.WithHostAllowlistLogger(discardLogger()), httpserver.WithHostResponder(nil))
		if env := strictEnvelope(t, refused(t, p)); env.Code != "host_not_allowed" {
			t.Errorf("code = %q, want host_not_allowed", env.Code)
		}
	})
}

// TestErrorResponseKeys sends one request to each refusal this package writes
// and decodes every answer as the v2 envelope.
func TestErrorResponseKeys(t *testing.T) {
	hosts, _ := httpserver.ParseHostList([]string{"app.example"}, httpserver.WithHostAllowlistLogger(discardLogger()))
	limited := httpserver.RateLimiter(1, time.Hour)(okHandler())
	authLimited := httpserver.FailedAuthRateLimit(nil, "")(okHandler())
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") })
	cases := []struct {
		name    string
		handler http.Handler
		before  int // requests sent first, to drain a limiter
		host    string
		status  int
		code    httpserver.ErrorCode
	}{
		{"Recoverer", httpserver.Recoverer(httpserver.WithRecoverLogger(discardLogger()))(panicky), 0, "", 500, "internal_error"},
		{"MethodNotAllowed", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpserver.MethodNotAllowed(w, r, http.MethodPost)
		}), 0, "", 405, "method_not_allowed"},
		{"RequireMethod", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpserver.RequireMethod(w, r, http.MethodPost)
		}), 0, "", 405, "method_not_allowed"},
		{"DecodeBody", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var v struct{}
			httpserver.DecodeBody(w, r, &v, "bad body")
		}), 0, "", 400, "bad_request"},
		{"LoopbackOnly", httpserver.LoopbackOnly()(okHandler()), 0, "", 403, "loopback_only"},
		{"RateLimiter", limited, 1, "", 429, "rate_limited"},
		{"FailedAuthRateLimit", authLimited, 10, "", 429, "too_many_auth_failures"},
		{"HostPolicy", hosts.Middleware()(okHandler()), 0, "evil.example", 403, "host_not_allowed"},
		{"RouteTimeout", httpserver.RouteTimeout(slow, time.Millisecond, ""), 0, "", 503, "timeout"},
	}
	specs := httpserver.CodeSpecs()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader("not json"))
				if tc.host != "" {
					req.Host = tc.host
				}
				rec := httptest.NewRecorder()
				tc.handler.ServeHTTP(rec, req)
				return rec
			}
			for range tc.before {
				send()
			}
			rec := send()
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			env := strictEnvelope(t, rec)
			if env.Code != tc.code {
				t.Errorf("code = %q, want %q", env.Code, tc.code)
			}
			if want := specs[tc.code].Remediation; want == "" || env.Remediation != want {
				t.Errorf("remediation = %q, want the CodeSpecs default %q", env.Remediation, want)
			}
		})
	}
}

func TestWriteErrorEnvelopeFields(t *testing.T) {
	write := func(r *http.Request, code httpserver.ErrorCode) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		httpserver.WriteError(rec, r, http.StatusConflict, code, "a sentence")
		return rec
	}
	t.Run("a code outside CodeSpecs has an empty remediation", func(t *testing.T) {
		env := strictEnvelope(t, write(httptest.NewRequest(http.MethodGet, "/", nil), "app_own_code"))
		if env.Code != "app_own_code" || env.Message != "a sentence" || env.Remediation != "" {
			t.Errorf("envelope = %+v, want app_own_code with an empty remediation", env)
		}
	})
	t.Run("an empty code is replaced", func(t *testing.T) {
		env := strictEnvelope(t, write(httptest.NewRequest(http.MethodGet, "/", nil), ""))
		if env.Code != httpserver.InvalidErrorCode {
			t.Errorf("code = %q, want %q", env.Code, httpserver.InvalidErrorCode)
		}
	})
	t.Run("the context's request id is kept", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(httpserver.WithRequestID(req.Context(), "rid-7"))
		if env := strictEnvelope(t, write(req, "app_own_code")); env.RequestID != "rid-7" {
			t.Errorf("request_id = %q, want rid-7", env.RequestID)
		}
	})
	t.Run("a minted id differs per answer", func(t *testing.T) {
		a := strictEnvelope(t, write(httptest.NewRequest(http.MethodGet, "/", nil), "app_own_code"))
		b := strictEnvelope(t, write(nil, "app_own_code"))
		if a.RequestID == b.RequestID {
			t.Errorf("two answers share request_id %q", a.RequestID)
		}
	})
	t.Run("a header the caller set is kept", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.Header().Set("WWW-Authenticate", `Bearer realm="app"`)
		httpserver.WriteError(rec, nil, http.StatusUnauthorized, "app_own_code", "sign in")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="app"` {
			t.Errorf("WWW-Authenticate = %q, want the caller's", got)
		}
	})
}

// The constructor and writer write what WriteError writes for the same
// request, with the request id in the body equal to X-Request-ID, and keep
// a remediation the caller replaced.
func TestErrorResponseMatchesWriteError(t *testing.T) {
	const remediation = "Add the name to ALLOWED_HOSTS, then reload."
	for name, ctxID := range map[string]string{"id in context": "rid-7", "no id in context": ""} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			if ctxID != "" {
				r = r.WithContext(httpserver.WithRequestID(r.Context(), ctxID))
			}
			viaWriteError := httptest.NewRecorder()
			httpserver.WriteError(viaWriteError, r, http.StatusForbidden, "host_not_allowed", "host refused")
			want := strictEnvelope(t, viaWriteError)

			viaBody := httptest.NewRecorder()
			resp := httpserver.NewErrorResponse(r, "host_not_allowed", "host refused")
			resp.Remediation = remediation
			httpserver.WriteErrorBody(viaBody, http.StatusForbidden, resp)
			if viaBody.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", viaBody.Code)
			}
			got := strictEnvelope(t, viaBody)
			if got.Code != want.Code || got.Message != want.Message {
				t.Errorf("code, message = %q, %q; want WriteError's %q, %q", got.Code, got.Message, want.Code, want.Message)
			}
			if got.Remediation != remediation {
				t.Errorf("remediation = %q, want the caller's %q", got.Remediation, remediation)
			}
			if ctxID != "" && (got.RequestID != ctxID || want.RequestID != ctxID) {
				t.Errorf("request_id = %q (WriteError %q), want the context's %q", got.RequestID, want.RequestID, ctxID)
			}
		})
	}
}

// An application's own fields sit beside the four standard keys in one flat
// object, and X-Request-ID is the body's request_id whether r's context held
// one or a fresh one was minted.
func TestErrorBodyMarshalsFlat(t *testing.T) {
	type conflict struct {
		httpserver.ErrorResponse
		Dependents []string `json:"dependents"`
	}
	t.Run("embedded body is one flat object", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
		r = r.WithContext(httpserver.WithRequestID(r.Context(), "rid-7"))
		body := conflict{ErrorResponse: httpserver.NewErrorResponse(r, "has_dependents", "in use"), Dependents: []string{"a"}}
		rec := httptest.NewRecorder()
		httpserver.WriteErrorBody(rec, http.StatusConflict, body)
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		}
		want := `{"code":"has_dependents","message":"in use","remediation":"","request_id":"rid-7","dependents":["a"]}` + "\n"
		if got := rec.Body.String(); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		if h := rec.Header().Get(httpserver.HeaderRequestID); h != "rid-7" {
			t.Errorf("%s = %q, want rid-7", httpserver.HeaderRequestID, h)
		}
	})
	t.Run("a minted id is the header's too", func(t *testing.T) {
		body := conflict{ErrorResponse: httpserver.NewErrorResponse(nil, "has_dependents", "in use"), Dependents: []string{"a"}}
		rec := httptest.NewRecorder()
		httpserver.WriteErrorBody(rec, http.StatusConflict, body)
		var got struct {
			RequestID  string   `json:"request_id"`
			Dependents []string `json:"dependents"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if got.RequestID == "" || rec.Header().Get(httpserver.HeaderRequestID) != got.RequestID {
			t.Errorf("request_id = %q, %s = %q, want one non-empty id", got.RequestID, httpserver.HeaderRequestID, rec.Header().Get(httpserver.HeaderRequestID))
		}
		if len(got.Dependents) != 1 || got.Dependents[0] != "a" {
			t.Errorf("dependents = %q, want [a]", got.Dependents)
		}
	})
}

// NewErrorResponse checks the code and fills the remediation as WriteError's
// envelope contract states for it.
func TestNewErrorResponseCodes(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	cases := []struct {
		name        string
		code        httpserver.ErrorCode
		wantCode    httpserver.ErrorCode
		remediation string
	}{
		{"a code outside CodeSpecs has an empty remediation", "app_own_code", "app_own_code", ""},
		{"an empty code is replaced", "", httpserver.InvalidErrorCode, ""},
		{"a code with a space is replaced", "has space", httpserver.InvalidErrorCode, ""},
		{"a CodeSpecs code takes its remediation", "rate_limited", "rate_limited", "Wait for the Retry-After delay, then retry."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := httpserver.NewErrorResponse(r, tc.code, "a sentence")
			if got.Code != tc.wantCode || got.Message != "a sentence" || got.Remediation != tc.remediation {
				t.Errorf("envelope = %+v, want code %q, message %q, remediation %q", got, tc.wantCode, "a sentence", tc.remediation)
			}
			if got.RequestID == "" {
				t.Error("request_id is empty")
			}
		})
	}
}

func TestWriteErrorBodyKeepsSetHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set(httpserver.HeaderRequestID, "upstream-id")
	rec.Header().Set("Retry-After", "60")
	httpserver.WriteErrorBody(rec, http.StatusServiceUnavailable, httpserver.NewErrorResponse(nil, "app_own_code", "m"))
	if h := rec.Header().Get(httpserver.HeaderRequestID); h != "upstream-id" {
		t.Errorf("%s = %q, want the caller's upstream-id", httpserver.HeaderRequestID, h)
	}
	if h := rec.Header().Get("Retry-After"); h != "60" {
		t.Errorf("Retry-After = %q, want the caller's 60", h)
	}
}
