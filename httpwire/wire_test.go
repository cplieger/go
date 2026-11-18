package httpwire_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// otherVersion is a well-formed wire version no fixture document has.
const otherVersion = "0123456789abcdef"

// wireRequests reach every kind of answer the fixture API writes: a row's
// success of each kind, a refusal, the router's own answers and a mount's.
var wireRequests = map[string]struct{ method, path, body string }{
	"a JSON read":       {http.MethodGet, "/api/jobs", ""},
	"a bodiless write":  {http.MethodPatch, "/api/settings", `{"name":"x"}`},
	"a held 200":        {http.MethodGet, "/api/events/wait", ""},
	"a refused body":    {http.MethodPatch, "/api/settings", `{"name":1}`},
	"a refused path":    {http.MethodGet, "/api/jobs/" + strings.Repeat("a", 65), ""},
	"a Raw answer":      {http.MethodPost, "/api/archive", ""},
	"a Stream answer":   {http.MethodGet, "/api/stream", ""},
	"a Redirect answer": {http.MethodGet, "/api/sso", ""},
	"no route":          {http.MethodGet, "/api/nowhere", ""},
	"another method":    {http.MethodDelete, "/api/jobs", ""},
	"a mount read":      {http.MethodGet, "/api/auth/keys", ""},
	"a mount write":     {http.MethodPost, "/api/auth/keys", `{"name":"x"}`},
}

// mountReached points the fixture mount at a handler answering 200 and
// reports whether it ran.
func mountReached(c *apiCase) *bool {
	reached := false
	c.tb.Mounts[0].Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	return &reached
}

func TestEveryAnswerCarriesWireVersionAndRequestID(t *testing.T) {
	c := newCase(t)
	mountReached(c)
	api := c.mustBuild(t)
	want := httpwire.WireVersion(apiDoc(t))
	if len(want) != 16 {
		t.Fatalf("fixture wire version %q", want)
	}
	for name, tc := range wireRequests {
		t.Run(name, func(t *testing.T) {
			rec := serve(api, tc.method, tc.path, tc.body)
			if got := rec.Header().Values("Wire-Version"); len(got) != 1 || got[0] != want {
				t.Errorf("answer %d Wire-Version = %q, want [%s]", rec.Code, got, want)
			}
			ids := rec.Header().Values(httpserver.HeaderRequestID)
			if len(ids) != 1 || !httpserver.ValidRequestID(ids[0]) {
				t.Fatalf("answer %d X-Request-Id = %q", rec.Code, ids)
			}
			if rec.Code >= http.StatusBadRequest {
				if env := envelopeOf(t, rec); env.RequestID != ids[0] {
					t.Errorf("envelope request_id %q, header %q", env.RequestID, ids[0])
				}
			}
			for _, h := range []string{"X-Wire-Version", "X-Feed-Position"} {
				if v := rec.Header().Values(h); v != nil {
					t.Errorf("answer carries %s: %q", h, v)
				}
			}
		})
	}
}

func TestRequestIDIsTheRequestsOwn(t *testing.T) {
	var seen string
	c := newCase(t)
	c.binds["jobs.list"] = httpwire.Bind("jobs.list", func(ctx context.Context, _ httpwire.Call[noP, jobQuery, noB]) ([]job, error) {
		seen = httpserver.RequestIDFromContext(ctx)
		return nil, nil
	})
	api := c.mustBuild(t)
	t.Run("the context's id wins", func(t *testing.T) {
		r := request(http.MethodGet, "/api/jobs", "", httpserver.HeaderRequestID, "from-header")
		r = r.WithContext(httpserver.WithRequestID(r.Context(), "from-context"))
		rec := answer(api, r)
		if got := rec.Header().Get(httpserver.HeaderRequestID); got != "from-context" || seen != "from-context" {
			t.Errorf("header %q, handler saw %q, want from-context", got, seen)
		}
	})
	t.Run("a valid inbound id is kept", func(t *testing.T) {
		rec := answer(api, request(http.MethodGet, "/api/jobs", "", httpserver.HeaderRequestID, "abc-123"))
		if got := rec.Header().Get(httpserver.HeaderRequestID); got != "abc-123" || seen != "abc-123" {
			t.Errorf("header %q, handler saw %q, want abc-123", got, seen)
		}
	})
	t.Run("a refusal carries the inbound id", func(t *testing.T) {
		rec := answer(api, request(http.MethodGet, "/api/nowhere", "", httpserver.HeaderRequestID, "abc-124"))
		if env := envelopeOf(t, rec); env.RequestID != "abc-124" || rec.Header().Get(httpserver.HeaderRequestID) != "abc-124" {
			t.Errorf("request_id %q, header %q", env.RequestID, rec.Header().Get(httpserver.HeaderRequestID))
		}
	})
	for name, bad := range map[string]string{"a newline": "a\nb", "a space": "a b", "65 bytes": strings.Repeat("a", 65)} {
		t.Run("an inbound id with "+name+" is replaced", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
			r.Header[httpserver.HeaderRequestID] = []string{bad}
			rec := answer(api, r)
			got := rec.Header().Get(httpserver.HeaderRequestID)
			if !httpserver.ValidRequestID(got) || got != seen {
				t.Errorf("header %q, handler saw %q", got, seen)
			}
		})
	}
	t.Run("two requests get two ids", func(t *testing.T) {
		a := serve(api, http.MethodGet, "/api/jobs", "").Header().Get(httpserver.HeaderRequestID)
		b := serve(api, http.MethodGet, "/api/jobs", "").Header().Get(httpserver.HeaderRequestID)
		if a == b {
			t.Errorf("both requests got %q", a)
		}
	})
}

func TestWriteWithAnotherWireVersionIsMismatch(t *testing.T) {
	version := httpwire.WireVersion(apiDoc(t))
	for name, tc := range map[string]struct {
		method, path, body string
		pins               []string
	}{
		"a table write":                   {http.MethodPatch, "/api/settings", `{"name":"x"}`, []string{otherVersion}},
		"a table write with a stale body": {http.MethodPatch, "/api/settings", `{"title":"x"}`, []string{otherVersion}},
		"a table write with an empty pin": {http.MethodPatch, "/api/settings", `{"name":"x"}`, []string{""}},
		"a table write with two pins":     {http.MethodPatch, "/api/settings", `{"name":"x"}`, []string{version, otherVersion}},
		"a Raw write":                     {http.MethodPost, "/api/archive", "", []string{otherVersion}},
		"a mount write":                   {http.MethodPost, "/api/auth/keys", `{"name":"x"}`, []string{otherVersion}},
		"a mount write in upper case":     {http.MethodPost, "/api/auth/keys", `{"name":"x"}`, []string{strings.ToUpper(version)}},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrderCase(t)
			mounted := mountReached(o.apiCase)
			var header []string
			for _, p := range tc.pins {
				header = append(header, "Wire-Version", p)
			}
			rec := answer(o.mustBuild(t), request(tc.method, tc.path, tc.body, header...))
			if env := envelopeOf(t, rec); rec.Code != http.StatusConflict || env.Code != "wire_mismatch" {
				t.Errorf("answer %d %s, want 409 wire_mismatch", rec.Code, env.Code)
			}
			if len(o.handled) != 0 || *mounted {
				t.Errorf("the handler ran: %v, mount %v", o.handled, *mounted)
			}
		})
	}
	for name, tc := range map[string]struct {
		method, path, body string
		header             []string
		status             int
	}{
		"a table write at this version": {http.MethodPatch, "/api/settings", `{"name":"x"}`, []string{"Wire-Version", version}, http.StatusNoContent},
		"a table write with no pin":     {http.MethodPatch, "/api/settings", `{"name":"x"}`, nil, http.StatusNoContent},
		"a table read with another pin": {http.MethodGet, "/api/jobs", "", []string{"Wire-Version", otherVersion}, http.StatusOK},
		"a mount write at this version": {http.MethodPost, "/api/auth/keys", `{"name":"x"}`, []string{"Wire-Version", version}, http.StatusOK},
		"a mount read with another pin": {http.MethodGet, "/api/auth/keys", "", []string{"Wire-Version", otherVersion}, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrderCase(t)
			mountReached(o.apiCase)
			rec := answer(o.mustBuild(t), request(tc.method, tc.path, tc.body, tc.header...))
			if rec.Code != tc.status {
				t.Errorf("answer %d %q, want %d", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}
	t.Run("a table row's guard comes first", func(t *testing.T) {
		o := newOrderCase(t)
		rec := answer(o.mustBuild(t), request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Wire-Version", otherVersion, "Refuse", "yes"))
		if env := envelopeOf(t, rec); env.Code != "auth_required" {
			t.Errorf("answer %d %s, want the guard's auth_required", rec.Code, env.Code)
		}
	})
}

func TestMountRowsMeetTheClientFloor(t *testing.T) {
	for name, tc := range map[string]struct {
		method, body string
	}{"a mount read": {http.MethodGet, ""}, "a mount write": {http.MethodPost, `{"name":"x"}`}} {
		t.Run(name, func(t *testing.T) {
			c := newCase(t)
			c.opts["floor"] = floorOption()
			mounted := mountReached(c)
			api := c.mustBuild(t)
			rec := answer(api, request(tc.method, "/api/auth/keys", tc.body, "Fixture-Version", "1", "Wire-Version", otherVersion))
			if env := envelopeOf(t, rec); rec.Code != http.StatusBadRequest || env.Code != "upgrade_required" || *mounted {
				t.Errorf("answer %d %s, mount reached %v; want upgrade_required first", rec.Code, env.Code, *mounted)
			}
			if rec := answer(api, request(tc.method, "/api/auth/keys", tc.body, "Fixture-Version", "2")); rec.Code != http.StatusOK || !*mounted {
				t.Errorf("a client at the floor: answer %d, mount reached %v", rec.Code, *mounted)
			}
		})
	}
}

// orderFeed records, beside the handlers' runs, when its position is read
// and the kinds it was asked for.
type orderFeed struct {
	ran   *[]string
	kinds *[]string
}

func (f orderFeed) Position(_ *http.Request, kinds []string) string {
	*f.ran = append(*f.ran, "position")
	*f.kinds = kinds
	return `store=4;epoch="b"`
}

func newFeedCase(t *testing.T) (*orderCase, *[]string) {
	t.Helper()
	o := newOrderCase(t)
	var kinds []string
	o.opts["feed"] = httpwire.WithFeed(orderFeed{ran: &o.handled, kinds: &kinds})
	o.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		return job{}, errJobMissing
	})
	return o, &kinds
}

func TestFeedPositionSeedsAFeedRowsSuccess(t *testing.T) {
	t.Run("a feed row's 2xx, read before its handler", func(t *testing.T) {
		o, kinds := newFeedCase(t)
		rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", ""))
		if got := rec.Header().Values("Feed-Position"); rec.Code != http.StatusOK || len(got) != 1 || got[0] != `store=4;epoch="b"` {
			t.Errorf("answer %d Feed-Position %q", rec.Code, got)
		}
		if strings.Join(o.handled, ",") != "position,jobs.list" {
			t.Errorf("ran %v, want the position read before the handler", o.handled)
		}
		if strings.Join(*kinds, ",") != "jobs,status" {
			t.Errorf("kinds %v, want the row's Changes", *kinds)
		}
	})
	t.Run("a HEAD of a feed row", func(t *testing.T) {
		o, _ := newFeedCase(t)
		if rec := answer(o.mustBuild(t), request(http.MethodHead, "/api/jobs", "")); rec.Header().Get("Feed-Position") == "" {
			t.Errorf("answer %d with no Feed-Position", rec.Code)
		}
	})
	for name, tc := range map[string]struct {
		method, path string
		header       []string
	}{
		"a feed row's query refusal":     {http.MethodGet, "/api/jobs?state=" + strings.Repeat("a", 17), nil},
		"a feed row's guard refusal":     {http.MethodGet, "/api/jobs", []string{"Refuse", "yes"}},
		"a feed row's handler error":     {http.MethodGet, "/api/jobs/missing", nil},
		"a feed row's path refusal":      {http.MethodGet, "/api/jobs/" + strings.Repeat("a", 65), nil},
		"another method on a feed route": {http.MethodDelete, "/api/jobs", nil},
		"a row with no Changes":          {http.MethodGet, "/api/events/wait", nil},
		"a mount row with Changes":       {http.MethodGet, "/api/auth/keys", nil},
	} {
		t.Run(name, func(t *testing.T) {
			o, _ := newFeedCase(t)
			mountReached(o.apiCase)
			rec := answer(o.mustBuild(t), request(tc.method, tc.path, "", tc.header...))
			if v := rec.Header().Values("Feed-Position"); v != nil {
				t.Errorf("answer %d carries Feed-Position %q", rec.Code, v)
			}
		})
	}
}
