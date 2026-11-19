package httpwire_test

import (
	"bytes"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// libraryCodes is httpwire's own code set with each status and whether a
// resend is safe, as the package documents it.
var libraryCodes = map[string]struct {
	status   int
	noEffect bool
}{
	"bad_body":                     {400, false},
	"bad_query":                    {400, false},
	"bad_header":                   {400, false},
	"body_too_large":               {413, false},
	"unsupported_media_type":       {415, true},
	"cursor_invalid":               {400, false},
	"if_match_required":            {428, false},
	"if_match_invalid":             {400, false},
	"stale_version":                {412, false},
	"wire_mismatch":                {409, false},
	"idempotency_key_invalid":      {400, false},
	"idempotency_key_required":     {400, false},
	"idempotency_key_unsupported":  {400, false},
	"idempotency_key_reused":       {422, false},
	"idempotency_in_flight":        {409, true},
	"idempotency_store_full":       {503, true},
	"idempotency_answer_too_large": {409, false},
	"upgrade_required":             {400, true},
	"held_limit":                   {429, true},
	"person_required":              {403, false},
	"store_unavailable":            {503, true},
	"accounts_unavailable":         {503, true},
	"maintenance":                  {503, true},
	"outcome_unknown":              {502, false},
}

// TestOutcomeUnknownIsTheLibrarysCode holds outcome_unknown to its spec: 502,
// kept by a Keyed row's replay since it is not NoEffect, with the default
// words an app replaces, and never added a second time by an app.
func TestOutcomeUnknownIsTheLibrarysCode(t *testing.T) {
	c := httpwire.NewCatalog()
	want := httpserver.CodeSpec{Status: http.StatusBadGateway, Remediation: "Check the result before sending it again; the action may have run."}
	if got, ok := c.Lookup("outcome_unknown"); !ok || got != want {
		t.Errorf("Lookup = %+v, %v; want %+v", got, ok, want)
	}
	if err := c.Add("outcome_unknown", httpserver.CodeSpec{Status: http.StatusBadGateway, Remediation: "Check the arr's queue."}); err == nil {
		t.Error("Add of outcome_unknown succeeded; the catalog already holds it")
	}
	if err := c.SetRemediation("outcome_unknown", "Check the arr's queue or history before retrying; the command may have run."); err != nil {
		t.Errorf("SetRemediation: %v", err)
	}
}

func TestNewCatalogHoldsLibraryAndServerCodes(t *testing.T) {
	c := httpwire.NewCatalog()
	for code, want := range libraryCodes {
		t.Run(code, func(t *testing.T) {
			got, ok := c.Lookup(code)
			if !ok {
				t.Fatalf("Lookup(%q) found nothing", code)
			}
			if got.Status != want.status || got.NoEffect != want.noEffect {
				t.Errorf("Lookup(%q) = status %d, NoEffect %v; want %d, %v", code, got.Status, got.NoEffect, want.status, want.noEffect)
			}
			if got.Remediation == "" {
				t.Errorf("Lookup(%q) has no default remediation", code)
			}
		})
	}
	for code, want := range httpserver.CodeSpecs() {
		t.Run("server_"+string(code), func(t *testing.T) {
			if _, mine := libraryCodes[string(code)]; mine {
				t.Fatalf("%q is owned by both httpserver and httpwire", code)
			}
			got, ok := c.Lookup(string(code))
			if !ok || got != want {
				t.Errorf("Lookup(%q) = %+v, %v; want httpserver's %+v", code, got, ok, want)
			}
		})
	}
	t.Run("no_substitute_code", func(t *testing.T) {
		if _, ok := c.Lookup(string(httpserver.InvalidErrorCode)); ok {
			t.Errorf("the catalog holds %q", httpserver.InvalidErrorCode)
		}
	})
}

func TestCatalogAddRefusals(t *testing.T) {
	good := httpserver.CodeSpec{Status: 502, Remediation: "Check the Sonarr connection in Settings."}
	cases := []struct {
		name string
		code string
		spec httpserver.CodeSpec
	}{
		{"library_code", "stale_version", good},
		{"server_code", "not_found", good},
		{"empty_code", "", good},
		{"capital", "Arr_refused", good},
		{"hyphen", "arr-refused", good},
		{"substitute_code", string(httpserver.InvalidErrorCode), good},
		{"code_over_cap", strings.Repeat("a", 4097), good},
		{"status_success", "arr_refused", httpserver.CodeSpec{Status: 200, Remediation: good.Remediation}},
		{"status_below_400", "arr_refused", httpserver.CodeSpec{Status: 399, Remediation: good.Remediation}},
		{"status_above_599", "arr_refused", httpserver.CodeSpec{Status: 600, Remediation: good.Remediation}},
		{"empty_remediation", "arr_refused", httpserver.CodeSpec{Status: 502}},
		{"remediation_two_lines", "arr_refused", httpserver.CodeSpec{Status: 502, Remediation: "Check it.\nThen retry."}},
		{"remediation_control", "arr_refused", httpserver.CodeSpec{Status: 502, Remediation: "Check \x1b[31mit."}},
		{"remediation_invalid_utf8", "arr_refused", httpserver.CodeSpec{Status: 502, Remediation: "Check \xffit."}},
		{"remediation_over_cap", "arr_refused", httpserver.CodeSpec{Status: 502, Remediation: strings.Repeat("a", 4097)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := httpwire.NewCatalog()
			before, had := c.Lookup(tc.code)
			if err := c.Add(tc.code, tc.spec); err == nil {
				t.Fatalf("Add(%q, %+v) = nil; want a refusal", tc.code, tc.spec)
			}
			after, has := c.Lookup(tc.code)
			if has != had || after != before {
				t.Errorf("a refused Add changed Lookup(%q): %+v, %v -> %+v, %v", tc.code, before, had, after, has)
			}
		})
	}
	t.Run("app_code_added", func(t *testing.T) {
		c := httpwire.NewCatalog()
		if err := c.Add("arr_refused", good); err != nil {
			t.Fatalf("Add = %v", err)
		}
		if got, ok := c.Lookup("arr_refused"); !ok || got != good {
			t.Errorf("Lookup = %+v, %v; want %+v", got, ok, good)
		}
		if err := c.Add("arr_refused", good); err == nil {
			t.Errorf("a second Add of the same code = nil; want a refusal")
		}
	})
}

func TestCatalogAddOwned(t *testing.T) {
	owned := map[httpserver.ErrorCode]httpserver.CodeSpec{
		"sse_unavailable":       {Status: 503, Remediation: "Retry the stream after a short wait.", NoEffect: true},
		"streaming_unsupported": {Status: 500, Remediation: "Serve the stream without a buffering proxy."},
	}
	t.Run("added", func(t *testing.T) {
		c := httpwire.NewCatalog()
		if err := c.AddOwned(owned); err != nil {
			t.Fatalf("AddOwned = %v", err)
		}
		for code, want := range owned {
			if got, ok := c.Lookup(string(code)); !ok || got != want {
				t.Errorf("Lookup(%q) = %+v, %v; want %+v", code, got, ok, want)
			}
		}
	})
	t.Run("identical_again_is_a_no_op", func(t *testing.T) {
		c := httpwire.NewCatalog()
		if err := c.AddOwned(owned); err != nil {
			t.Fatalf("AddOwned = %v", err)
		}
		if err := c.AddOwned(owned); err != nil {
			t.Errorf("a second identical AddOwned = %v; want nil", err)
		}
	})
	t.Run("server_codes_again_are_a_no_op", func(t *testing.T) {
		c := httpwire.NewCatalog()
		if err := c.AddOwned(httpserver.CodeSpecs()); err != nil {
			t.Errorf("AddOwned(httpserver.CodeSpecs()) = %v; want nil", err)
		}
	})
	t.Run("identical_after_reworded_is_a_no_op", func(t *testing.T) {
		c := httpwire.NewCatalog()
		if err := c.AddOwned(owned); err != nil {
			t.Fatalf("AddOwned = %v", err)
		}
		if err := c.SetRemediation("sse_unavailable", "Reload the page."); err != nil {
			t.Fatalf("SetRemediation = %v", err)
		}
		if err := c.AddOwned(owned); err != nil {
			t.Errorf("AddOwned after SetRemediation = %v; want nil", err)
		}
		if got, _ := c.Lookup("sse_unavailable"); got.Remediation != "Reload the page." {
			t.Errorf("AddOwned replaced the app's remediation: %q", got.Remediation)
		}
	})
	refusals := []struct {
		name string
		with map[httpserver.ErrorCode]httpserver.CodeSpec
	}{
		{"differing_status", map[httpserver.ErrorCode]httpserver.CodeSpec{"sse_unavailable": {Status: 500, Remediation: owned["sse_unavailable"].Remediation, NoEffect: true}}},
		{"differing_remediation", map[httpserver.ErrorCode]httpserver.CodeSpec{"sse_unavailable": {Status: 503, Remediation: "Other words.", NoEffect: true}}},
		{"differing_no_effect", map[httpserver.ErrorCode]httpserver.CodeSpec{"sse_unavailable": {Status: 503, Remediation: owned["sse_unavailable"].Remediation}}},
		{"differing_from_library", map[httpserver.ErrorCode]httpserver.CodeSpec{"stale_version": {Status: 409, Remediation: "Reload."}}},
		{"differing_from_server", map[httpserver.ErrorCode]httpserver.CodeSpec{"rate_limited": {Status: 503, Remediation: "Wait."}}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			c := httpwire.NewCatalog()
			if err := c.AddOwned(owned); err != nil {
				t.Fatalf("AddOwned = %v", err)
			}
			if err := c.AddOwned(tc.with); err == nil {
				t.Errorf("AddOwned(%v) = nil; want a refusal", tc.with)
			}
		})
	}
	t.Run("refused_adds_nothing", func(t *testing.T) {
		c := httpwire.NewCatalog()
		mixed := maps.Clone(owned)
		mixed["Bad-Code"] = httpserver.CodeSpec{Status: 500, Remediation: "x"}
		err := c.AddOwned(mixed)
		if err == nil {
			t.Fatalf("AddOwned with a bad code = nil; want a refusal")
		}
		if !strings.Contains(err.Error(), "Bad-Code") {
			t.Errorf("AddOwned error %q does not name the bad code", err)
		}
		if _, ok := c.Lookup("sse_unavailable"); ok {
			t.Errorf("a refused AddOwned added sse_unavailable")
		}
	})
}

func TestCatalogSetRemediation(t *testing.T) {
	c := httpwire.NewCatalog()
	for _, code := range []string{"stale_version", "host_not_allowed"} {
		before, _ := c.Lookup(code)
		if err := c.SetRemediation(code, "Ask the admin of this server."); err != nil {
			t.Fatalf("SetRemediation(%q) = %v", code, err)
		}
		got, _ := c.Lookup(code)
		want := before
		want.Remediation = "Ask the admin of this server."
		if got != want {
			t.Errorf("after SetRemediation, Lookup(%q) = %+v; want %+v", code, got, want)
		}
	}
	for name, tc := range map[string]struct{ code, words string }{
		"unknown_code":  {"no_such_code", "Retry."},
		"empty":         {"stale_version", ""},
		"two_lines":     {"stale_version", "Reload.\nRetry."},
		"invalid_utf8":  {"stale_version", "Reload\xff."},
		"over_the_cap":  {"stale_version", strings.Repeat("a", 4097)},
		"bidi_override": {"stale_version", "Reload\u202e."},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.SetRemediation(tc.code, tc.words); err == nil {
				t.Errorf("SetRemediation(%q, %q) = nil; want a refusal", tc.code, tc.words)
			}
		})
	}
}

// writeOne answers e through a fresh catalog and returns the recorder and the
// decoded body.
func writeOne(t *testing.T, c *httpwire.Catalog, r *http.Request, e *httpwire.Error) (*httptest.ResponseRecorder, httpwire.Envelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.WriteError(rec, r, e)
	env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("DecodeEnvelope(%s) = %v", rec.Body.Bytes(), err)
	}
	return rec, env
}

func requestWithID(t *testing.T, id string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/rows", http.NoBody)
	return r.WithContext(httpserver.WithRequestID(r.Context(), id))
}

func TestWriteError(t *testing.T) {
	t.Run("catalog_status_and_words", func(t *testing.T) {
		c := httpwire.NewCatalog()
		rec, env := writeOne(t, c, requestWithID(t, "req-1"), &httpwire.Error{Code: "stale_version", Message: "Another tab saved this row first.", Reason: "row_changed"})
		spec, _ := c.Lookup("stale_version")
		if rec.Code != spec.Status {
			t.Errorf("status = %d; want %d", rec.Code, spec.Status)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		want := httpwire.Envelope{Code: "stale_version", Message: "Another tab saved this row first.", Remediation: spec.Remediation, Reason: "row_changed", RequestID: "req-1"}
		if !envelopeEqual(env, want) {
			t.Errorf("envelope = %+v; want %+v", env, want)
		}
		if got := rec.Header().Get(httpserver.HeaderRequestID); got != "req-1" {
			t.Errorf("%s = %q; want req-1", httpserver.HeaderRequestID, got)
		}
	})
	t.Run("keeps_caller_headers", func(t *testing.T) {
		c := httpwire.NewCatalog()
		rec := httptest.NewRecorder()
		rec.Header().Set("WWW-Authenticate", `Bearer realm="app"`)
		rec.Header().Set("Cache-Control", "no-store")
		c.WriteError(rec, requestWithID(t, "req-1"), &httpwire.Error{Code: "person_required", Message: "Sign in."})
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="app"` {
			t.Errorf("WWW-Authenticate = %q; want the caller's challenge", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want the caller's no-store", got)
		}
	})
	for _, tc := range []struct {
		after  time.Duration
		header string
		ms     int64
	}{
		{1500 * time.Millisecond, "2", 1500},
		{time.Nanosecond, "1", 1},
		{5 * time.Second, "5", 5000},
		{1001 * time.Microsecond, "1", 2},
	} {
		t.Run("retry_after_"+tc.after.String(), func(t *testing.T) {
			rec, env := writeOne(t, httpwire.NewCatalog(), requestWithID(t, "req-1"), &httpwire.Error{Code: "rate_limited", Message: "Slow down.", RetryAfter: tc.after})
			if got := rec.Header().Get("Retry-After"); got != tc.header {
				t.Errorf("Retry-After = %q; want %q", got, tc.header)
			}
			if env.RetryAfterMS != tc.ms {
				t.Errorf("retry_after_ms = %d; want %d", env.RetryAfterMS, tc.ms)
			}
		})
	}
	t.Run("no_retry_after_when_unset_or_negative", func(t *testing.T) {
		for _, d := range []time.Duration{0, -time.Second} {
			rec, env := writeOne(t, httpwire.NewCatalog(), requestWithID(t, "req-1"), &httpwire.Error{Code: "rate_limited", Message: "Slow down.", RetryAfter: d})
			if got, ok := rec.Header()["Retry-After"]; ok || env.RetryAfterMS != 0 {
				t.Errorf("RetryAfter %v wrote Retry-After %q and retry_after_ms %d", d, got, env.RetryAfterMS)
			}
		}
	})
	t.Run("internal_error_says_internal_error", func(t *testing.T) {
		rec, env := writeOne(t, httpwire.NewCatalog(), requestWithID(t, "req-1"), &httpwire.Error{Code: "internal_error", Message: "open /var/lib/app/store.db: permission denied", Reason: "disk"})
		if rec.Code != 500 || env.Message != "internal error" || env.Reason != "" {
			t.Errorf("internal_error answered %d %+v; want 500 with the message \"internal error\" and no reason", rec.Code, env)
		}
	})
	t.Run("fields_capped_at_100", func(t *testing.T) {
		fields := make([]httpwire.FieldError, 150)
		for i := range fields {
			fields[i] = httpwire.FieldError{Path: "items[" + strconv.Itoa(i) + "]", Code: "required", Message: "Name it."}
		}
		_, env := writeOne(t, httpwire.NewCatalog(), requestWithID(t, "req-1"), &httpwire.Error{Code: "bad_body", Message: "Fix the body.", Fields: fields})
		if len(env.Fields) != 100 {
			t.Fatalf("wrote %d fields; want the first 100", len(env.Fields))
		}
		if env.Fields[99].Path != "items[99]" {
			t.Errorf("wrote %d fields; want the first 100", len(env.Fields))
		}
	})
	t.Run("long_and_invalid_text_capped", func(t *testing.T) {
		long := strings.Repeat("\xff", 5000)
		_, env := writeOne(t, httpwire.NewCatalog(), requestWithID(t, "req-1"), &httpwire.Error{
			Code: "bad_body", Message: long,
			Fields: []httpwire.FieldError{{Path: long, Code: long, Message: long}},
		})
		if len(env.Fields) != 1 {
			t.Fatalf("wrote %d fields; want 1", len(env.Fields))
		}
		for name, s := range map[string]string{"message": env.Message, "path": env.Fields[0].Path, "code": env.Fields[0].Code, "field message": env.Fields[0].Message} {
			if len(s) > 4096 || !utf8.ValidString(s) {
				t.Errorf("%s is %d bytes, valid UTF-8 %v; want at most 4096 valid bytes", name, len(s), utf8.ValidString(s))
			}
		}
	})
	t.Run("request_id_fresh_without_one", func(t *testing.T) {
		for name, r := range map[string]*http.Request{
			"no_id":       httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/rows", http.NoBody),
			"nil_request": nil,
			"invalid_id":  requestWithID(t, "bad id\n"),
		} {
			t.Run(name, func(t *testing.T) {
				rec, env := writeOne(t, httpwire.NewCatalog(), r, &httpwire.Error{Code: "not_found", Message: "No such row."})
				if !httpserver.ValidRequestID(env.RequestID) {
					t.Errorf("request_id = %q; want a fresh valid id", env.RequestID)
				}
				if got := rec.Header().Get(httpserver.HeaderRequestID); got != env.RequestID {
					t.Errorf("%s = %q; want the body's %q", httpserver.HeaderRequestID, got, env.RequestID)
				}
			})
		}
	})
	t.Run("request_id_from_response_header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.Header().Set(httpserver.HeaderRequestID, "set-upstream")
		httpwire.NewCatalog().WriteError(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody), &httpwire.Error{Code: "not_found"})
		env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
		if err != nil || env.RequestID != "set-upstream" {
			t.Errorf("request_id = %q, %v; want the response header's set-upstream", env.RequestID, err)
		}
	})
	t.Run("request_id_context_wins_over_header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rec.Header().Set(httpserver.HeaderRequestID, "stale")
		httpwire.NewCatalog().WriteError(rec, requestWithID(t, "req-1"), &httpwire.Error{Code: "not_found"})
		env, _ := httpwire.DecodeEnvelope(rec.Body.Bytes())
		if env.RequestID != "req-1" || rec.Header().Get(httpserver.HeaderRequestID) != "req-1" {
			t.Errorf("body %q, header %q; want req-1 on both", env.RequestID, rec.Header().Get(httpserver.HeaderRequestID))
		}
	})
}

// TestWriteErrorWiringBugsLogToDefault swaps slog's default, so it is not
// parallel.
func TestWriteErrorWiringBugsLogToDefault(t *testing.T) {
	c := httpwire.NewCatalog()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cases := []struct {
		name  string
		e     *httpwire.Error
		code  string
		log   string
		check func(t *testing.T, env httpwire.Envelope)
	}{
		{"unlisted_code", &httpwire.Error{Code: "no_such_code", Message: "x"}, "internal_error", "no_such_code", func(t *testing.T, env httpwire.Envelope) {
			if env.Message != "internal error" {
				t.Errorf("message = %q", env.Message)
			}
		}},
		{"nil_error", nil, "internal_error", "nil", func(t *testing.T, env httpwire.Envelope) {
			if env.Message != "internal error" {
				t.Errorf("message = %q", env.Message)
			}
		}},
		{"bad_reason", &httpwire.Error{Code: "stale_version", Message: "x", Reason: "Not A Code"}, "stale_version", "Not A Code", func(t *testing.T, env httpwire.Envelope) {
			if env.Reason != "" {
				t.Errorf("reason = %q; want it dropped", env.Reason)
			}
		}},
		{"reason_over_cap", &httpwire.Error{Code: "stale_version", Message: "x", Reason: strings.Repeat("a", 65)}, "stale_version", "reason", func(t *testing.T, env httpwire.Envelope) {
			if env.Reason != "" {
				t.Errorf("reason = %q; want it dropped", env.Reason)
			}
		}},
		{"bad_upstream_service", &httpwire.Error{Code: "stale_version", Message: "x", Upstream: &httpwire.Upstream{Service: "Sonarr", Status: 401, Message: "no"}}, "stale_version", "Sonarr", func(t *testing.T, env httpwire.Envelope) {
			if env.Upstream != nil {
				t.Errorf("upstream = %+v; want it dropped", env.Upstream)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			_, env := writeOne(t, c, requestWithID(t, "req-1"), tc.e)
			if env.Code != tc.code {
				t.Errorf("code = %q; want %q", env.Code, tc.code)
			}
			tc.check(t, env)
			line := buf.String()
			if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, tc.log) {
				t.Errorf("log = %q; want an ERROR line naming %q", line, tc.log)
			}
		})
	}
}

func TestResponder(t *testing.T) {
	c := httpwire.NewCatalog()
	if err := c.SetRemediation("host_not_allowed", "Open the app at its configured address."); err != nil {
		t.Fatal(err)
	}
	respond := c.Responder()
	t.Run("catalog_status_and_words", func(t *testing.T) {
		rec := httptest.NewRecorder()
		respond(rec, requestWithID(t, "req-1"), http.StatusTeapot, "host_not_allowed", "This host is not served.")
		env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want := httpwire.Envelope{Code: "host_not_allowed", Message: "This host is not served.", Remediation: "Open the app at its configured address.", RequestID: "req-1"}
		if rec.Code != http.StatusForbidden || !envelopeEqual(env, want) {
			t.Errorf("answered %d %+v; want 403 %+v", rec.Code, env, want)
		}
	})
	t.Run("missing_code_is_internal_error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		respond(rec, requestWithID(t, "req-1"), http.StatusBadRequest, "no_such_code", "x")
		env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
		if err != nil || rec.Code != 500 || env.Code != "internal_error" {
			t.Errorf("answered %d %+v, %v; want 500 internal_error", rec.Code, env, err)
		}
	})
}
