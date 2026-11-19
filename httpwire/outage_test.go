package httpwire_test

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// outageCase serves the fixture table under orderGuard with WithOutage
// answering *file, logging to the returned sink.
func outageCase(t *testing.T, file *string) (*orderCase, *bool, *logSink) {
	t.Helper()
	o := newOrderCase(t)
	sink := &logSink{}
	o.opts["logger"] = httpwire.WithLogger(slog.New(sink))
	o.opts["outage"] = httpwire.WithOutage(func() string { return *file })
	return o, mountReached(o.apiCase), sink
}

func TestOutageRefusesRowsBeforeTheGuard(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		code httpserver.ErrorCode
	}{
		"the store cannot open":    {"store", "store_unavailable"},
		"the accounts cannot open": {"accounts", "accounts_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			file := tc.file
			o, _, _ := outageCase(t, &file)
			api := o.mustBuild(t)
			for _, r := range []struct{ method, path, body string }{
				{http.MethodGet, "/api/jobs", ""},
				{http.MethodPatch, "/api/settings", `{"name":"x"}`},
				{http.MethodGet, "/api/events/wait", ""},
				{http.MethodGet, "/api/stream", ""},
			} {
				rec := answer(api, request(r.method, r.path, r.body))
				if env := envelopeOf(t, rec); rec.Code != http.StatusServiceUnavailable || env.Code != tc.code {
					t.Errorf("%s %s: answer %d %s, want 503 %s", r.method, r.path, rec.Code, env.Code, tc.code)
				}
			}
			if len(o.guarded) != 0 || len(o.handled) != 0 {
				t.Errorf("the guard ran for %v and the handler for %v", o.guarded, o.handled)
			}
		})
	}
	t.Run("the file is read per request", func(t *testing.T) {
		file := "store"
		o, _, _ := outageCase(t, &file)
		api := o.mustBuild(t)
		if rec := answer(api, request(http.MethodGet, "/api/jobs", "")); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("answer %d during the outage", rec.Code)
		}
		file = ""
		if rec := answer(api, request(http.MethodGet, "/api/jobs", "")); rec.Code != http.StatusOK {
			t.Errorf("answer %d once the file opened", rec.Code)
		}
	})
	t.Run("the client floor comes first", func(t *testing.T) {
		file := "accounts"
		o, _, _ := outageCase(t, &file)
		o.opts["floor"] = floorOption()
		rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", "", "Fixture-Version", "1"))
		if env := envelopeOf(t, rec); env.Code != "upgrade_required" {
			t.Errorf("answer %d %s, want upgrade_required", rec.Code, env.Code)
		}
	})
}

func TestDuringOutageRowsAndTheMount(t *testing.T) {
	for name, tc := range map[string]struct {
		file       string
		mountCode  httpserver.ErrorCode
		mountReach bool
	}{
		"no outage":                {"", "", true},
		"the store cannot open":    {"store", "", true},
		"the accounts cannot open": {"accounts", "accounts_unavailable", false},
	} {
		t.Run(name, func(t *testing.T) {
			file := tc.file
			o, mounted, _ := outageCase(t, &file)
			api := o.mustBuild(t)
			if rec := answer(api, request(http.MethodGet, "/api/sso", "")); rec.Code != http.StatusFound || len(o.guarded) != 1 {
				t.Errorf("a DuringOutage row: answer %d, guarded %v; want it guarded and served", rec.Code, o.guarded)
			}
			rec := answer(api, request(http.MethodGet, "/api/auth/keys", ""))
			if *mounted != tc.mountReach {
				t.Errorf("mount reached %v, want %v", *mounted, tc.mountReach)
			}
			if tc.mountCode != "" {
				if env := envelopeOf(t, rec); rec.Code != http.StatusServiceUnavailable || env.Code != tc.mountCode {
					t.Errorf("mount answer %d %s, want 503 %s", rec.Code, env.Code, tc.mountCode)
				}
			}
		})
	}
}

func TestOutageNamingNoFileIsAWiringBug(t *testing.T) {
	file := "settings"
	o, mounted, sink := outageCase(t, &file)
	api := o.mustBuild(t)
	for _, path := range []string{"/api/jobs", "/api/auth/keys"} {
		rec := answer(api, request(http.MethodGet, path, ""))
		if env := envelopeOf(t, rec); env.Code != "internal_error" {
			t.Errorf("%s: answer %d %s, want internal_error", path, rec.Code, env.Code)
		}
	}
	if *mounted || len(o.handled) != 0 || !sink.has("ERROR", "WithOutage", "settings") {
		t.Errorf("mount reached %v, handled %v, log %v", *mounted, o.handled, sink.all())
	}
	if rec := answer(api, request(http.MethodGet, "/api/sso", "")); rec.Code != http.StatusFound {
		t.Errorf("a DuringOutage row: answer %d", rec.Code)
	}
}
