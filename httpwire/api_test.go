package httpwire_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

func TestNewAcceptsFixtureTable(t *testing.T) {
	api := newCase(t).mustBuild(t)
	want, err := httpwire.Derive(fixtureTable(t), guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	got := api.Table()
	for _, name := range []string{"jobs.list", "jobs.start", "events.wait", "auth.sso", "auth.keys", "auth.keyCreate"} {
		if g, w := row(got, name).Errors, row(want, name).Errors; !slices.Equal(g, w) {
			t.Errorf("row %s Errors = %v, want Derive's %v", name, g, w)
		}
	}
	if got.MaxBody != 64<<10 {
		t.Errorf("MaxBody = %d, want the 64 KiB default", got.MaxBody)
	}
}

func TestTableAnswersACopy(t *testing.T) {
	api := newCase(t).mustBuild(t)
	first := api.Table()
	row(first, "jobs.start").Errors[0] = "changed"
	row(first, "jobs.start").Codes[0] = "changed"
	row(first, "auth.keys").Changes[0] = "changed"
	first.Endpoints[0].Name = "changed.name"
	again := api.Table()
	if c := row(again, "jobs.start").Errors[0]; c != "job_busy" {
		t.Errorf("an edit of Table's Errors reached the API: %q", c)
	}
	if c := row(again, "jobs.start").Codes[0]; c != "job_busy" {
		t.Errorf("an edit of Table's Codes reached the API: %q", c)
	}
	if c := row(again, "auth.keys").Changes[0]; c != "keys" {
		t.Errorf("an edit of a mount row reached the API: %q", c)
	}
	if again.Endpoints[0].Name != "jobs.list" {
		t.Errorf("an edit of a row name reached the API: %q", again.Endpoints[0].Name)
	}
}

func TestNewPublicTableNeedsNoPrincipalOptions(t *testing.T) {
	c := newCase(t)
	c.tb.Endpoints = []httpwire.Endpoint{*row(c.tb, "auth.sso")}
	c.tb.Mounts = nil
	c.tb.ClientHeader = ""
	c.binds = map[string]httpwire.Binding{"auth.sso": c.binds["auth.sso"]}
	c.opts = map[string]httpwire.Option{"schema": httpwire.WithSchema(editDoc(t, func(paths map[string]any) {
		for p := range paths {
			if p != "/api/sso" {
				delete(paths, p)
			}
		}
	}))}
	if _, err := c.build(); err != nil {
		t.Fatalf("New of a Public table with only WithSchema: %v", err)
	}
}

func TestNewRefusesNilTable(t *testing.T) {
	if _, err := httpwire.New(nil, nil); err == nil {
		t.Fatal("New(nil) answered no error")
	}
}

type newRefusal struct {
	edit func(t *testing.T, c *apiCase)
	want []string
}

func runNewRefusals(t *testing.T, cases map[string]newRefusal) {
	t.Helper()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCase(t)
			tc.edit(t, c)
			api, err := c.build()
			if err == nil {
				t.Fatalf("New accepted the table (api %v)", api != nil)
			}
			if api != nil {
				t.Error("New answered an API beside its error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not hold %q:\n%v", w, err)
				}
			}
		})
	}
}

func rawHandler() http.Handler { return http.NotFoundHandler() }

func TestNewRefusesBindings(t *testing.T) {
	runNewRefusals(t, map[string]newRefusal{
		"row with no binding": {func(_ *testing.T, c *apiCase) { delete(c.binds, "jobs.get") }, []string{`row "jobs.get" has no binding`}},
		"binding with no row": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.gone"] = httpwire.BindRaw("jobs.gone", rawHandler())
		}, []string{`binding "jobs.gone" names no row`}},
		"zero Binding": {func(_ *testing.T, c *apiCase) { c.extra = append(c.extra, httpwire.Binding{}) }, []string{`binding "" names no row`}},
		"name bound twice": {func(_ *testing.T, c *apiCase) {
			c.extra = append(c.extra, httpwire.BindRaw("auth.sso", rawHandler()))
		}, []string{`row "auth.sso" is bound twice`}},
		"binding of a mount row": {func(_ *testing.T, c *apiCase) {
			c.extra = append(c.extra, httpwire.BindRaw("auth.keys", rawHandler()))
		}, []string{`binding "auth.keys" names a mount row, which its mount's Handler serves`}},
		"nil handler": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.list"] = httpwire.Bind[noP, jobQuery, noB, noH, []job]("jobs.list", nil)
		}, []string{`binding "jobs.list" has a nil handler`}},
		"nil raw handler": {func(_ *testing.T, c *apiCase) {
			c.binds["auth.sso"] = httpwire.BindRaw("auth.sso", nil)
		}, []string{`binding "auth.sso" has a nil handler`}},
		"nil stream handler": {func(_ *testing.T, c *apiCase) {
			c.binds["live.stream"] = httpwire.BindStream("live.stream", nil)
		}, []string{`binding "live.stream" has a nil handler`}},
		"raw binding on a JSON row": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.list"] = httpwire.BindRaw("jobs.list", rawHandler())
		}, []string{`row "jobs.list" is a JSON row, bound with BindRaw; bind it with Bind`}},
		"stream binding on a JSON row": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.list"] = httpwire.BindStream("jobs.list", rawHandler())
		}, []string{`row "jobs.list" is a JSON row, bound with BindStream; bind it with Bind`}},
		"JSON binding on a Raw row": {func(_ *testing.T, c *apiCase) {
			c.binds["archive.export"] = httpwire.Bind("archive.export", func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (httpwire.None, error) {
				return httpwire.None{}, nil
			})
		}, []string{`row "archive.export" is a Raw row, bound with Bind; bind it with BindRaw`}},
		"stream binding on a Redirect row": {func(_ *testing.T, c *apiCase) {
			c.binds["auth.sso"] = httpwire.BindStream("auth.sso", rawHandler())
		}, []string{`row "auth.sso" is a Redirect row, bound with BindStream; bind it with BindRaw`}},
		"raw binding on a Stream row": {func(_ *testing.T, c *apiCase) {
			c.binds["live.stream"] = httpwire.BindRaw("live.stream", rawHandler())
		}, []string{`row "live.stream" is a Stream row, bound with BindRaw; bind it with BindStream`}},
	})
}

func TestNewRefusesBindingTypes(t *testing.T) {
	runNewRefusals(t, map[string]newRefusal{
		"wrong P": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[otherBody, noQ, noB, noH]) (job, error) { return job{}, nil })
		}, []string{`row "jobs.get": the binding's P is httpwire_test.otherBody, but the row's Params is httpwire_test.jobParams`}},
		"P where the row has no Params": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.list"] = httpwire.Bind("jobs.list", func(context.Context, httpwire.Call[jobParams, jobQuery, noB, noH]) ([]job, error) { return nil, nil })
		}, []string{`row "jobs.list": the binding's P is httpwire_test.jobParams, but the row's Params is none (httpwire.None)`}},
		"None where the row has Params": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (job, error) { return job{}, nil })
		}, []string{`row "jobs.get": the binding's P is httpwire.None, but the row's Params is httpwire_test.jobParams`}},
		"wrong Q": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.list"] = httpwire.Bind("jobs.list", func(context.Context, httpwire.Call[noP, jobParams, noB, noH]) ([]job, error) { return nil, nil })
		}, []string{`row "jobs.list": the binding's Q is httpwire_test.jobParams, but the row's Query is httpwire_test.jobQuery`}},
		"wrong B": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.start"] = httpwire.Bind("jobs.start", func(context.Context, httpwire.Call[noP, noQ, otherBody, noH]) (job, error) { return job{}, nil })
		}, []string{`row "jobs.start": the binding's B is httpwire_test.otherBody, but the row's Request is httpwire_test.startRequest`}},
		"wrong R": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB, noH]) (*job, error) { return nil, nil })
		}, []string{`row "jobs.get": the binding's R is *httpwire_test.job, but the row's Response is httpwire_test.job`}},
		"a body on a 204 row": {func(_ *testing.T, c *apiCase) {
			c.binds["settings.save"] = httpwire.Bind("settings.save", func(context.Context, httpwire.Call[noP, noQ, otherBody, noH]) (job, error) { return job{}, nil })
		}, []string{`row "settings.save": the binding's R is httpwire_test.job, but the row's Response is none (httpwire.None)`}},
		"plain R on a row with Successes": {func(_ *testing.T, c *apiCase) {
			c.binds["events.wait"] = httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (nudge, error) { return nudge{}, nil })
		}, []string{`row "events.wait": the row declares Successes, so the binding's R is httpwire.Status[T] with T the row's body httpwire_test.nudge, not httpwire_test.nudge`}},
		"Status on a one-status row": {func(_ *testing.T, c *apiCase) {
			c.binds["jobs.get"] = httpwire.Bind("jobs.get", func(context.Context, httpwire.Call[jobParams, noQ, noB, noH]) (httpwire.Status[job], error) {
				return httpwire.Answer(http.StatusOK, job{}), nil
			})
		}, []string{`row "jobs.get": the row declares no Successes, so the binding's R is its Response httpwire_test.job, not a Status`}},
		"Status of another body": {func(_ *testing.T, c *apiCase) {
			c.binds["events.wait"] = httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (httpwire.Status[job], error) {
				return httpwire.Answer(http.StatusOK, job{}), nil
			})
		}, []string{`row "events.wait": the binding's Status body is httpwire_test.job, but the row's body is httpwire_test.nudge`}},
		"a type embedding Status is not a Status": {func(_ *testing.T, c *apiCase) {
			type wrapped struct{ httpwire.Status[nudge] }
			c.binds["events.wait"] = httpwire.Bind("events.wait", func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (wrapped, error) {
				return wrapped{}, nil
			})
		}, []string{`row "events.wait": the row declares Successes, so the binding's R is httpwire.Status[T]`}},
	})
}

func TestTwoBodyTypesRefused(t *testing.T) {
	c := newCase(t)
	row(c.tb, "events.wait").Successes = []httpwire.Success{
		{Status: http.StatusNoContent},
		{Status: http.StatusCreated, Response: reflect.TypeFor[otherBody]()},
	}
	_, err := c.build()
	if err == nil {
		t.Fatal("New accepted a row declaring two body types")
	}
	for _, w := range []string{`row "events.wait"`, "two body types", "httpwire_test.nudge", "httpwire_test.otherBody"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not hold %q:\n%v", w, err)
		}
	}
}

func TestNewRefusesMissingOptions(t *testing.T) {
	runNewRefusals(t, map[string]newRefusal{
		"no guard": {func(_ *testing.T, c *apiCase) { delete(c.opts, "guard") }, []string{
			`row "jobs.list": a row whose class is not Public needs WithGuard`,
		}},
		"no credential": {func(_ *testing.T, c *apiCase) { delete(c.opts, "credential") }, []string{
			`row "jobs.list": a row whose class is not Public needs WithCredential`,
		}},
		"no cursor owner": {func(_ *testing.T, c *apiCase) { delete(c.opts, "cursorOwner") }, []string{
			`row "jobs.list": a row whose class is not Public needs WithCursorOwner`,
		}},
		"no key rate": {func(_ *testing.T, c *apiCase) { delete(c.opts, "keyRate") }, []string{
			`row "jobs.start": a row whose class admits a key needs WithKeyRate`,
		}},
		"a key rate with no limiter": {func(_ *testing.T, c *apiCase) {
			c.opts["keyRate"] = httpwire.WithKeyRate(nil, func(*http.Request) (string, bool) { return "", false })
		}, []string{"WithKeyRate needs a limiter and a key function"}},
		"a key rate with no key function": {func(t *testing.T, c *apiCase) {
			c.opts["keyRate"] = httpwire.WithKeyRate(fixtureLimiter(t), nil)
		}, []string{"WithKeyRate needs a limiter and a key function"}},
		"no hold": {func(_ *testing.T, c *apiCase) { delete(c.opts, "hold") }, []string{
			`row "events.wait": a Held row needs WithHold`,
			`row "jobs.start": a Keyed row needs WithHold, which holds its join waiters`,
		}},
		"no client floor": {func(_ *testing.T, c *apiCase) { delete(c.opts, "floor") }, []string{
			`ClientHeader "Fixture-Version" needs WithClientFloor`,
		}},
		"a client floor with no header": {func(_ *testing.T, c *apiCase) { c.tb.ClientHeader = "" }, []string{
			"WithClientFloor is set, but the table has no ClientHeader",
		}},
		"a client floor with no Below": {func(_ *testing.T, c *apiCase) {
			c.opts["floor"] = httpwire.WithClientFloor(httpwire.ClientFloor{Message: func(string) string { return "x" }})
		}, []string{"WithClientFloor needs both Below and Message"}},
		"a client floor with no Message": {func(_ *testing.T, c *apiCase) {
			c.opts["floor"] = httpwire.WithClientFloor(httpwire.ClientFloor{Below: func(string) bool { return true }})
		}, []string{"WithClientFloor needs both Below and Message"}},
		"no feed": {func(_ *testing.T, c *apiCase) { delete(c.opts, "feed") }, []string{
			`row "jobs.list": a row with Changes needs WithFeed`,
		}},
		"a Keyed Raw row with no spool directory": {func(_ *testing.T, c *apiCase) { row(c.tb, "archive.export").Keyed = true }, []string{
			`row "archive.export": a Keyed Raw row needs WithSpoolDir`,
		}},
	})
}

func fixtureLimiter(t *testing.T) *httpserver.KeyedRateLimiter {
	t.Helper()
	l, err := httpserver.NewKeyedRateLimiter(httpserver.KeyedRate{})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewAcceptsAKeyedRawRowWithASpoolDirectory(t *testing.T) {
	c := newCase(t)
	row(c.tb, "archive.export").Keyed = true
	c.opts["spool"] = httpwire.WithSpoolDir(t.TempDir())
	c.opts["schema"] = httpwire.WithSchema(editDoc(t, func(paths map[string]any) {
		paths["/api/archive"].(map[string]any)["post"].(map[string]any)["x-idempotency"] = policy(600, nil)
	}))
	c.mustBuild(t)
}

func TestNewLastOptionWins(t *testing.T) {
	c := newCase(t)
	c.opts["zz-no-guard"] = httpwire.WithGuard(nil)
	_, err := c.build()
	if err == nil || !strings.Contains(err.Error(), "needs WithGuard") {
		t.Errorf("a later WithGuard(nil) did not clear the guard: %v", err)
	}
}

func deleteOp(path, method string) func(map[string]any) {
	return func(paths map[string]any) {
		item := paths[path].(map[string]any)
		delete(item, method)
		if len(item) == 0 {
			delete(paths, path)
		}
	}
}

func TestNewRefusesTheDocument(t *testing.T) {
	runNewRefusals(t, map[string]newRefusal{
		"no schema": {func(_ *testing.T, c *apiCase) { delete(c.opts, "schema") }, []string{
			"New needs WithSchema: the table's committed OpenAPI document",
		}},
		"a document ParseSchema refuses": {func(_ *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema([]byte(`{"openapi": "3.1.0", "paths": {}, "webhooks": {}}`))
		}, []string{"WithSchema: httpwire: ParseSchema", `"webhooks"`}},
		"a row with no operation": {func(t *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema(editDoc(t, deleteOp("/api/jobs/{id}", "get")))
		}, []string{`row "jobs.get" has no operation of that operationId in the document`}},
		"a mount row with no operation": {func(t *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema(editDoc(t, deleteOp("/api/auth/keys", "get")))
		}, []string{`row "auth.keys" has no operation of that operationId in the document`}},
		"an operation under another method": {func(t *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema(editDoc(t, func(paths map[string]any) {
				item := paths["/api/jobs/{id}"].(map[string]any)
				item["put"] = item["get"]
				delete(item, "get")
			}))
		}, []string{`row "jobs.get" is GET /api/jobs/{id}, but the document's operation is PUT /api/jobs/{id}`}},
		"an operation under another path": {func(t *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema(editDoc(t, func(paths map[string]any) {
				paths["/api/job/{id}"] = paths["/api/jobs/{id}"]
				delete(paths, "/api/jobs/{id}")
			}))
		}, []string{`row "jobs.get" is GET /api/jobs/{id}, but the document's operation is GET /api/job/{id}`}},
		"an operation with no row": {func(t *testing.T, c *apiCase) {
			c.opts["schema"] = httpwire.WithSchema(editDoc(t, func(paths map[string]any) {
				paths["/api/extra"] = map[string]any{"get": map[string]any{
					"operationId": "jobs.extra",
					"responses":   map[string]any{"204": map[string]any{"description": "No Content"}},
				}}
			}))
		}, []string{`the document's operation "jobs.extra" (GET /api/extra) has no row`}},
	})
}

// opOf answers the fixture document's operation at path and method.
func opOf(paths map[string]any, path, method string) map[string]any {
	return paths[path].(map[string]any)[method].(map[string]any)
}

func setParams(path, method string, params ...map[string]any) func(map[string]any) {
	return func(paths map[string]any) {
		list := make([]any, len(params))
		for i, p := range params {
			list[i] = p
		}
		opOf(paths, path, method)["parameters"] = list
	}
}

func param(in, name string, required bool) map[string]any {
	return map[string]any{"in": in, "name": name, "required": required, "schema": map[string]any{"type": "string"}}
}

func TestNewRefusesADocumentOffTheRowTypes(t *testing.T) {
	doc := func(f func(map[string]any)) func(*testing.T, *apiCase) {
		return func(t *testing.T, c *apiCase) { c.opts["schema"] = httpwire.WithSchema(editDoc(t, f)) }
	}
	runNewRefusals(t, map[string]newRefusal{
		"a query field the document lacks": {doc(setParams("/api/jobs", "get")), []string{
			`row "jobs.list": Query field "state" has no query parameter in the document`,
		}},
		"a query parameter no field reads": {doc(setParams("/api/jobs", "get", param("query", "state", false), param("query", "page", false))), []string{
			`row "jobs.list": the document's query parameter "page" is no Query field`,
		}},
		"a query parameter required for an optional field": {doc(setParams("/api/jobs", "get", param("query", "state", true))), []string{
			`row "jobs.list": Query field "state" is optional, but the document's parameter is required`,
		}},
		"a path parameter the document lacks": {doc(setParams("/api/jobs/{id}", "get")), []string{
			`row "jobs.get": Params field "id" has no path parameter in the document`,
		}},
		"a path parameter the document leaves optional": {doc(setParams("/api/jobs/{id}", "get", param("path", "id", false))), []string{
			`row "jobs.get": Params field "id" is required, but the document's parameter is optional`,
		}},
		"a request body the document lacks": {doc(func(paths map[string]any) { delete(opOf(paths, "/api/jobs", "post"), "requestBody") }), []string{
			`row "jobs.start": the row decodes a Request, but the document's operation has no JSON request body`,
		}},
		"a request body the document leaves optional": {doc(func(paths map[string]any) {
			opOf(paths, "/api/jobs", "post")["requestBody"].(map[string]any)["required"] = false
		}), []string{`row "jobs.start": the document's request body is not required, but the row always decodes one`}},
		"a request body the row lacks": {doc(func(paths map[string]any) {
			opOf(paths, "/api/sso", "get")["requestBody"] = map[string]any{
				"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}},
			}
		}), []string{`row "auth.sso": the document's operation has a JSON request body, but the row has no Request`}},
	})
}

func TestNewIncludesDeriveRefusals(t *testing.T) {
	runNewRefusals(t, map[string]newRefusal{
		"an empty Summary":                {func(_ *testing.T, c *apiCase) { row(c.tb, "jobs.list").Summary = "" }, []string{`row "jobs.list": Summary is empty`}},
		"a mount row with no Description": {func(_ *testing.T, c *apiCase) { row(c.tb, "auth.keys").Description = "" }, []string{`row "auth.keys": Description is empty`}},
		"two rows on one pattern": {func(t *testing.T, c *apiCase) {
			dup := *row(c.tb, "jobs.get")
			dup.Name = "jobs.again"
			c.tb.Endpoints = append(c.tb.Endpoints, dup)
			c.binds["jobs.again"] = httpwire.Bind("jobs.again", func(context.Context, httpwire.Call[jobParams, noQ, noB, noH]) (job, error) { return job{}, nil })
		}, []string{`rows "jobs.get" and "jobs.again"`}},
	})
}

func TestNewListsEveryRefusalInOneError(t *testing.T) {
	c := newCase(t)
	delete(c.binds, "jobs.get")
	delete(c.opts, "guard")
	row(c.tb, "jobs.list").Summary = ""
	c.opts["schema"] = httpwire.WithSchema(editDoc(t, deleteOp("/api/sso", "get")))
	_, err := c.build()
	if err == nil {
		t.Fatal("New accepted the table")
	}
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) || len(joined.Unwrap()) < 4 {
		t.Errorf("want one joined error of at least 4 refusals, got %v", err)
	}
	for _, w := range []string{
		`row "jobs.get" has no binding`, "needs WithGuard", `row "jobs.list": Summary is empty`,
		`row "auth.sso" has no operation`,
	} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not hold %q:\n%v", w, err)
		}
	}
}
