package httpwire_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// orderGuard records each row it guards a request of, and refuses one
// carrying the header Refuse with auth_required.
type orderGuard struct {
	fixtureGuard
	catalog *httpwire.Catalog
	ran     *[]string
}

func (g orderGuard) Wrap(ep *httpwire.Endpoint, next http.Handler) http.Handler {
	name := ep.Name
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*g.ran = append(*g.ran, name)
		if r.Header.Get("Refuse") != "" {
			g.catalog.WriteError(w, r, &httpwire.Error{Code: "auth_required", Message: "sign in"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// orderCase is the fixture table under orderGuard, each handler recording
// that it ran.
type orderCase struct {
	*apiCase
	guarded []string
	handled []string
}

func newOrderCase(t *testing.T) *orderCase {
	t.Helper()
	o := &orderCase{apiCase: newCase(t)}
	o.opts["guard"] = httpwire.WithGuard(orderGuard{catalog: o.tb.Catalog, ran: &o.guarded})
	o.binds["jobs.list"] = httpwire.Bind("jobs.list", func(context.Context, httpwire.Call[noP, jobQuery, noB]) ([]job, error) {
		o.handled = append(o.handled, "jobs.list")
		return nil, nil
	})
	o.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB]) (job, error) {
		o.handled = append(o.handled, "jobs.get")
		return job{ID: "one"}, nil
	})
	o.binds["settings.save"] = httpwire.Bind("settings.save", func(context.Context, httpwire.Call[noP, noQ, otherBody]) (httpwire.None, error) {
		o.handled = append(o.handled, "settings.save")
		return httpwire.None{}, nil
	})
	o.binds["events.wait"] = httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB]) (httpwire.Status[nudge], error) {
		o.handled = append(o.handled, "events.wait")
		return httpwire.Answer(http.StatusNoContent, nudge{}), nil
	})
	return o
}

func request(method, target, body string, header ...string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	return r
}

func answer(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestCrossSiteWriteIsRefusedBeforeTheGuard(t *testing.T) {
	t.Run("a table write row", func(t *testing.T) {
		o := newOrderCase(t)
		rec := answer(o.mustBuild(t), request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Sec-Fetch-Site", "cross-site"))
		if env := envelopeOf(t, rec); rec.Code != http.StatusForbidden || env.Code != "cross_origin_refused" {
			t.Errorf("answer %d %s, want 403 cross_origin_refused", rec.Code, env.Code)
		}
		if len(o.guarded) != 0 || len(o.handled) != 0 {
			t.Errorf("the guard ran for %v and the handler for %v", o.guarded, o.handled)
		}
	})
	t.Run("a same-origin write passes", func(t *testing.T) {
		o := newOrderCase(t)
		if rec := answer(o.mustBuild(t), request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Sec-Fetch-Site", "same-origin")); rec.Code != http.StatusNoContent {
			t.Errorf("answer %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a cross-site read passes", func(t *testing.T) {
		o := newOrderCase(t)
		if rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", "", "Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusOK {
			t.Errorf("answer %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a mount's write row is the mount's to check", func(t *testing.T) {
		o := newOrderCase(t)
		reached := false
		o.tb.Mounts[0].Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusCreated)
		})
		answer(o.mustBuild(t), request(http.MethodPost, "/api/auth/keys", `{"name":"x"}`, "Sec-Fetch-Site", "cross-site"))
		if !reached {
			t.Error("the binder refused a mount's cross-site write")
		}
	})
}

func floorOption() httpwire.Option {
	return httpwire.WithClientFloor(httpwire.ClientFloor{
		Below:   func(v string) bool { return v < "2" },
		Message: func(v string) string { return "This server needs client 2 or later; you run " + v + "." },
	})
}

func TestClientFloorRefusesBeforeTheGuard(t *testing.T) {
	t.Run("a client below the floor", func(t *testing.T) {
		o := newOrderCase(t)
		o.opts["floor"] = floorOption()
		rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", "", "Fixture-Version", "1.4", "Refuse", "yes"))
		env := envelopeOf(t, rec)
		if rec.Code != http.StatusBadRequest || env.Code != "upgrade_required" || env.Message != "This server needs client 2 or later; you run 1.4." {
			t.Errorf("answer %d %s %q", rec.Code, env.Code, env.Message)
		}
		if rec.Header().Get("Upgrade") != "" {
			t.Errorf("Upgrade = %q, want none", rec.Header().Get("Upgrade"))
		}
		if len(o.guarded) != 0 || len(o.handled) != 0 {
			t.Errorf("the guard ran for %v and the handler for %v", o.guarded, o.handled)
		}
	})
	t.Run("any value below the floor refuses", func(t *testing.T) {
		o := newOrderCase(t)
		o.opts["floor"] = floorOption()
		rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", "", "Fixture-Version", "3", "Fixture-Version", "1"))
		if env := envelopeOf(t, rec); env.Code != "upgrade_required" {
			t.Errorf("answer %d %s", rec.Code, env.Code)
		}
	})
	for name, header := range map[string][]string{
		"a client at the floor": {"Fixture-Version", "2"},
		"no client header":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrderCase(t)
			o.opts["floor"] = floorOption()
			if rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", "", header...)); rec.Code != http.StatusOK {
				t.Errorf("answer %d %q", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("the cross-origin check comes first", func(t *testing.T) {
		o := newOrderCase(t)
		o.opts["floor"] = floorOption()
		rec := answer(o.mustBuild(t), request(http.MethodPatch, "/api/settings", `{"name":"x"}`, "Sec-Fetch-Site", "cross-site", "Fixture-Version", "1"))
		if env := envelopeOf(t, rec); env.Code != "cross_origin_refused" {
			t.Errorf("answer %d %s, want cross_origin_refused", rec.Code, env.Code)
		}
	})
}

func TestGuardRunsBeforeDecoding(t *testing.T) {
	o := newOrderCase(t)
	api := o.mustBuild(t)
	for name, r := range map[string]*http.Request{
		"a bad body":   request(http.MethodPatch, "/api/settings", `{"zz":1}`, "Refuse", "yes"),
		"a bad query":  request(http.MethodGet, "/api/jobs?zz=1", "", "Refuse", "yes"),
		"a stream row": request(http.MethodGet, "/api/stream", "", "Refuse", "yes"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := answer(api, r)
			if env := envelopeOf(t, rec); rec.Code != http.StatusUnauthorized || env.Code != "auth_required" {
				t.Errorf("answer %d %s, want the guard's 401 auth_required", rec.Code, env.Code)
			}
		})
	}
	if len(o.handled) != 0 {
		t.Errorf("a handler ran: %v", o.handled)
	}
}

func TestEmptyCredentialIsInternalError(t *testing.T) {
	for name, opt := range map[string]func(c *orderCase){
		"the credential": func(c *orderCase) {
			c.opts["credential"] = httpwire.WithCredential(func(*http.Request) string { return "" })
		},
		"the cursor owner": func(c *orderCase) {
			c.opts["cursorOwner"] = httpwire.WithCursorOwner(func(*http.Request) string { return "" })
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrderCase(t)
			opt(o)
			sink := &logSink{}
			o.opts["logger"] = httpwire.WithLogger(slog.New(sink))
			api := o.mustBuild(t)
			rec := answer(api, request(http.MethodGet, "/api/jobs?zz=1", ""))
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusInternalServerError || env.Code != "internal_error" || len(env.Fields) != 0 {
				t.Errorf("answer %d %s %v, want 500 internal_error before any decode", rec.Code, env.Code, env.Fields)
			}
			if len(o.guarded) != 1 || len(o.handled) != 0 {
				t.Errorf("guarded %v, handled %v; want the guard once and no handler", o.guarded, o.handled)
			}
			if !sink.has("ERROR", `row=jobs.list`) {
				t.Errorf("no ERROR line names the row: %v", sink.all())
			}
			if rec := answer(api, request(http.MethodGet, "/api/sso", "")); rec.Code != http.StatusFound {
				t.Errorf("a Public row answered %d; the empty answer is its own", rec.Code)
			}
		})
	}
}

// keyRateCase makes jobs.get admit a key and passes a limiter whose key
// function reads the X-Key header.
func keyRateCase(t *testing.T, interval time.Duration) *orderCase {
	t.Helper()
	o := newOrderCase(t)
	row(o.tb, "jobs.get").Access = httpwire.Access{Class: httpwire.Scoped, Scope: "read"}
	l, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{Burst: 1, Interval: interval})
	if err != nil {
		t.Fatal(err)
	}
	o.opts["keyRate"] = httpwire.WithKeyRate(l, func(r *http.Request) (string, bool) {
		k := r.Header.Get("X-Key")
		return k, k != ""
	})
	return o
}

func TestKeyRate(t *testing.T) {
	t.Run("a key past its bucket", func(t *testing.T) {
		o := keyRateCase(t, time.Hour)
		api := o.mustBuild(t)
		if rec := answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1")); rec.Code != http.StatusOK {
			t.Fatalf("first request answered %d", rec.Code)
		}
		rec := answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1"))
		if env := envelopeOf(t, rec); rec.Code != http.StatusTooManyRequests || env.Code != "rate_limited" || rec.Header().Get("Retry-After") != "3600" {
			t.Errorf("answer %d %s Retry-After %q, want 429 rate_limited 3600", rec.Code, env.Code, rec.Header().Get("Retry-After"))
		}
		if rec := answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k2")); rec.Code != http.StatusOK {
			t.Errorf("another key answered %d", rec.Code)
		}
	})
	t.Run("Retry-After is at least a second", func(t *testing.T) {
		o := keyRateCase(t, time.Millisecond)
		api := o.mustBuild(t)
		var rec *httptest.ResponseRecorder
		for range 1000 {
			if rec = answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1")); rec.Code == http.StatusTooManyRequests {
				break
			}
		}
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
			t.Errorf("answer %d Retry-After %q, want 429 and 1", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
	t.Run("a session is never counted", func(t *testing.T) {
		o := keyRateCase(t, time.Hour)
		api := o.mustBuild(t)
		for i := range 5 {
			if rec := answer(api, request(http.MethodGet, "/api/jobs/one", "")); rec.Code != http.StatusOK {
				t.Fatalf("session request %d answered %d", i, rec.Code)
			}
		}
	})
	t.Run("a request the guard refuses spends no token", func(t *testing.T) {
		o := keyRateCase(t, time.Hour)
		api := o.mustBuild(t)
		answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1", "Refuse", "yes"))
		if rec := answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1")); rec.Code != http.StatusOK {
			t.Errorf("the admitted request answered %d", rec.Code)
		}
	})
	t.Run("the rate comes before decoding", func(t *testing.T) {
		o := keyRateCase(t, time.Hour)
		api := o.mustBuild(t)
		answer(api, request(http.MethodGet, "/api/jobs/one", "", "X-Key", "k1"))
		rec := answer(api, request(http.MethodGet, "/api/jobs/"+strings.Repeat("x", 65), "", "X-Key", "k1"))
		if env := envelopeOf(t, rec); env.Code != "rate_limited" {
			t.Errorf("answer %d %s, want rate_limited before the path decode", rec.Code, env.Code)
		}
	})
}

func TestHeldRowRunsThroughHold(t *testing.T) {
	newHold := func(o *orderCase, refuse bool) *int {
		held := 0
		o.opts["hold"] = httpwire.WithHold(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				held++
				if refuse {
					o.tb.Catalog.WriteError(w, r, &httpwire.Error{Code: "held_limit", Message: "too many waits", RetryAfter: 5 * time.Second})
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		return &held
	}
	t.Run("an admitted wait is held", func(t *testing.T) {
		o := newOrderCase(t)
		held := newHold(o, false)
		if rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/events/wait", "")); rec.Code != http.StatusNoContent || *held != 1 {
			t.Errorf("answer %d, held %d times", rec.Code, *held)
		}
	})
	t.Run("a wait past the cap runs no handler", func(t *testing.T) {
		o := newOrderCase(t)
		newHold(o, true)
		rec := answer(o.mustBuild(t), request(http.MethodGet, "/api/events/wait", ""))
		if env := envelopeOf(t, rec); env.Code != "held_limit" || len(o.handled) != 0 {
			t.Errorf("answer %d %s, handled %v", rec.Code, env.Code, o.handled)
		}
	})
	t.Run("a refused principal is never held", func(t *testing.T) {
		o := newOrderCase(t)
		held := newHold(o, false)
		answer(o.mustBuild(t), request(http.MethodGet, "/api/events/wait", "", "Refuse", "yes"))
		if *held != 0 {
			t.Errorf("held %d times", *held)
		}
	})
	t.Run("a row that is not Held is not held", func(t *testing.T) {
		o := newOrderCase(t)
		held := newHold(o, false)
		answer(o.mustBuild(t), request(http.MethodGet, "/api/jobs", ""))
		if *held != 0 {
			t.Errorf("held %d times", *held)
		}
	})
}
