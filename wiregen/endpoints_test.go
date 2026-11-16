package wiregen_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/client"
	"github.com/cplieger/go/wiregen/v2/testdata/unions"
)

// newEndpointRegistry returns a registry with the transport import set and
// the client fixtures' Item registered.
func newEndpointRegistry(eps ...wiregen.Endpoint) *testRegistry {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	r.Types = []wiregen.Type{wiregen.TypeRef[client.Item]()}
	r.Endpoints = eps
	return r
}

// get is a valid KindJSON GET endpoint named name at path.
func get(name, path string) wiregen.Endpoint {
	return wiregen.Endpoint{Name: name, Method: "GET", Path: path, Kind: wiregen.KindJSON}
}

// requireGenerateError requires generating r to fail with an error naming
// every one of wants.
func requireGenerateError(t *testing.T, r *testRegistry, wants ...string) {
	t.Helper()
	err := r.generate(t.Context(), t.TempDir())
	if err == nil {
		t.Fatalf("Generate() = nil error, want one naming %q", wants)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Generate() error = %q, want it to name %q", err, want)
		}
	}
}

func TestEndpoints_Validation(t *testing.T) {
	item := wiregen.TypeRef[client.Item]()
	params := wiregen.TypeRef[client.ItemParams]()
	with := func(e wiregen.Endpoint, fn func(*wiregen.Endpoint)) wiregen.Endpoint {
		fn(&e)
		return e
	}
	cases := []struct {
		name    string
		eps     []wiregen.Endpoint
		wantErr string
	}{
		{"bad method", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Method = "FETCH" })}, "unknown method"},
		{"bad name", []wiregen.Endpoint{get("1x", "/api/x")}, "not a valid TS identifier"},
		{"empty name", []wiregen.Endpoint{get("", "/api/x")}, "not a valid TS identifier"},
		{"reserved name", []wiregen.Endpoint{get("delete", "/api/x")}, "not a valid TS identifier"},
		{"globalThis name", []wiregen.Endpoint{get("globalThis", "/api/x")}, "not a valid TS identifier"},
		{"duplicate name", []wiregen.Endpoint{get("x", "/api/x"), get("x", "/api/y")}, "xEndpoint, which endpoint x already uses"},
		{"no kind", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Kind = 0 })}, "has no Kind"},
		{"unknown kind", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Kind = 9 })}, "unknown kind Kind(9)"},
		{"relative path", []wiregen.Endpoint{get("x", "api/x")}, "must start with /"},
		{"quote in path", []wiregen.Endpoint{get("x", `/api/"x`)}, "not a URL path character"},
		{"backtick in path", []wiregen.Endpoint{get("x", "/api/`x")}, "not a URL path character"},
		{"non-ASCII path", []wiregen.Endpoint{get("x", "/api/é")}, `"é", which is not a URL path character`},
		{"lone percent", []wiregen.Endpoint{get("x", "/api/%")}, "or a complete pct-encoding"},
		{"short pct-encoding", []wiregen.Endpoint{get("x", "/api/%2")}, "or a complete pct-encoding"},
		{"non-hex pct-encoding", []wiregen.Endpoint{get("x", "/api/%zz")}, "or a complete pct-encoding"},
		{"route by placeholder name", []wiregen.Endpoint{
			with(get("x", "/api/{id}"), func(e *wiregen.Endpoint) { e.Params = params }),
			with(get("y", "/api/{other}"), func(e *wiregen.Endpoint) { e.Params = wiregen.TypeRef[client.OtherParams]() }),
		}, "differ only in placeholder names"},
		{"path item by placeholder name", []wiregen.Endpoint{
			with(get("x", "/api/{id}"), func(e *wiregen.Endpoint) { e.Params = params }),
			with(get("y", "/api/{other}"), func(e *wiregen.Endpoint) { e.Method, e.Params = "DELETE", wiregen.TypeRef[client.OtherParams]() }),
		}, "differ only in placeholder names"},
		{"nested placeholder", []wiregen.Endpoint{get("x", "/api/{a{b}}")}, "nested '{'"},
		{"unbalanced open", []wiregen.Endpoint{get("x", "/api/{id")}, "unbalanced '{'"},
		{"unbalanced close", []wiregen.Endpoint{get("x", "/api/id}")}, "unbalanced '}'"},
		{"empty placeholder", []wiregen.Endpoint{get("x", "/api/{}")}, "not a valid identifier"},
		{"duplicate placeholder", []wiregen.Endpoint{with(get("x", "/api/{id}/sub/{id}"), func(e *wiregen.Endpoint) { e.Params = params })}, "duplicate placeholder"},
		{"placeholder without params", []wiregen.Endpoint{get("x", "/api/{id}")}, "Params exactly when its path has placeholders"},
		{"params without placeholder", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Params = params })}, "Params exactly when its path has placeholders"},
		{"get with body", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Request = item })}, "GET and cannot take a Request body"},
		{"array without response", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Array = true })}, "Array without a Response"},
		{"raw with response", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) {
			e.Kind, e.ContentType, e.Response = wiregen.KindRaw, "text/vtt", item
		})}, "cannot set Response or Array"},
		{"raw without content type", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Kind = wiregen.KindRaw })}, "ContentType exactly when it is kind raw"},
		{"json with content type", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.ContentType = "text/plain" })}, "ContentType exactly when it is kind raw"},
		{"event stream not a get", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Kind, e.Method = wiregen.KindEventStream, "POST" })}, "must be a GET"},
		{"redirect with header", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) {
			e.Kind, e.Headers = wiregen.KindRedirect, []wiregen.Header{{Name: "X-A", Arg: "a", MaxBytes: 1}}
		})}, "kind redirect"},
		{"extension outside x-", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Extensions = map[string]any{"access": "read"} })}, `extension "access", which must start with x-`},
		{"bare x- extension", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) { e.Extensions = map[string]any{"x-": 1} })}, "must start with x-"},
		{"name takes a registered type's name", []wiregen.Endpoint{get("decodeItem", "/api/x")}, "decodeItem, which type github.com/cplieger/go/wiregen/v2/testdata/client.Item already uses"},
		{"name takes a client binding", []wiregen.Endpoint{get("qs", "/api/x")}, "qs, which the client module already uses"},
		{"two names collide on their types", []wiregen.Endpoint{get("x", "/api/x"), get("xEndpoint", "/api/y")}, "already uses"},
		{"unregistered response", []wiregen.Endpoint{with(get("x", "/api/x"), func(e *wiregen.Endpoint) {
			e.Response = wiregen.Type{PkgPath: "github.com/cplieger/go/wiregen/v2/testdata/client", Name: "Nope"}
		})}, "endpoint x Response type github.com/cplieger/go/wiregen/v2/testdata/client.Nope is not found in its package"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireGenerateError(t, newEndpointRegistry(tc.eps...), tc.wantErr)
		})
	}
}

func TestEndpointHeadersValidated(t *testing.T) {
	cases := []struct {
		name    string
		headers []wiregen.Header
		wantErr string
	}{
		{"name not a token", []wiregen.Header{{Name: "Bad Name", Arg: "a", MaxBytes: 1}}, "not an HTTP field name"},
		{"empty name", []wiregen.Header{{Arg: "a", MaxBytes: 1}}, "not an HTTP field name"},
		{"twice in other case", []wiregen.Header{{Name: "X-A", Arg: "a", MaxBytes: 1}, {Name: "x-a", Arg: "b", MaxBytes: 1}}, "declares header x-a twice"},
		{"arg not an identifier", []wiregen.Header{{Name: "X-A", Arg: "a-b", MaxBytes: 1}}, "not a valid TS identifier"},
		{"two args alike", []wiregen.Header{{Name: "X-A", Arg: "a", MaxBytes: 1}, {Name: "X-B", Arg: "a", MaxBytes: 1}}, "two headers with Arg a"},
		{"no max", []wiregen.Header{{Name: "X-A", Arg: "a"}}, "MaxBytes >= 1"},
		{"min above max", []wiregen.Header{{Name: "X-A", Arg: "a", MinBytes: 3, MaxBytes: 2}}, "MinBytes <= MaxBytes"},
		{"negative min", []wiregen.Header{{Name: "X-A", Arg: "a", MinBytes: -1, MaxBytes: 2}}, "0 <= MinBytes"},
		{"arg takes the body's name", []wiregen.Header{{Name: "X-A", Arg: "body", MaxBytes: 1}}, "argument body from both Request and header X-A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := wiregen.Endpoint{
				Name: "x", Method: "POST", Path: "/api/x", Kind: wiregen.KindJSON,
				Request: wiregen.TypeRef[client.StartRequest](), Headers: tc.headers,
			}
			requireGenerateError(t, newEndpointRegistry(e), tc.wantErr)
		})
	}
}

// TestEndpointErrorsValidated: a code is lower snake case, listed once per
// endpoint, has a 4xx or 5xx status, x- extensions, and the same status and
// extensions on every endpoint listing it.
func TestEndpointErrorsValidated(t *testing.T) {
	withErrors := func(errs ...[]wiregen.ErrorRef) []wiregen.Endpoint {
		var eps []wiregen.Endpoint
		for i, es := range errs {
			name := string(rune('a' + i))
			e := get(name, "/api/"+name)
			e.Errors = es
			eps = append(eps, e)
		}
		return eps
	}
	ref := func(code string, status int) wiregen.ErrorRef { return wiregen.ErrorRef{Code: code, Status: status} }
	cases := []struct {
		name    string
		eps     []wiregen.Endpoint
		wantErr string
	}{
		{"upper case code", withErrors([]wiregen.ErrorRef{ref("Not_Found", 404)}), `"Not_Found", which must be lower snake case`},
		{"code with a quote", withErrors([]wiregen.ErrorRef{ref(`a"b`, 404)}), "lower snake case"},
		{"empty code", withErrors([]wiregen.ErrorRef{ref("", 404)}), "lower snake case"},
		{"listed twice", withErrors([]wiregen.ErrorRef{ref("gone", 410), ref("gone", 410)}), "lists error gone twice"},
		{"success status", withErrors([]wiregen.ErrorRef{ref("gone", 200)}), "status 200, outside 400 to 599"},
		{"status over 599", withErrors([]wiregen.ErrorRef{ref("gone", 600)}), "status 600"},
		{"bad extension", withErrors([]wiregen.ErrorRef{{Code: "gone", Status: 410, Extensions: map[string]any{"no_effect": true}}}), `error gone has extension "no_effect"`},
		{"status differs", withErrors([]wiregen.ErrorRef{ref("gone", 410)}, []wiregen.ErrorRef{ref("gone", 404)}), "error gone differs between endpoints a and b"},
		{"extensions differ", withErrors(
			[]wiregen.ErrorRef{{Code: "gone", Status: 410, Extensions: map[string]any{"x-no-effect": true}}},
			[]wiregen.ErrorRef{{Code: "gone", Status: 410, Extensions: map[string]any{"x-no-effect": false}}},
		), "error gone differs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireGenerateError(t, newEndpointRegistry(tc.eps...), tc.wantErr)
		})
	}

	t.Run("nil and empty extensions agree", func(t *testing.T) {
		r := newEndpointRegistry(withErrors(
			[]wiregen.ErrorRef{{Code: "gone", Status: 410}},
			[]wiregen.ErrorRef{{Code: "gone", Status: 410, Extensions: map[string]any{}}},
		)...)
		if err := r.generate(t.Context(), t.TempDir()); err != nil {
			t.Errorf("Generate() = %v, want nil", err)
		}
	})
	t.Run("union lists the codes in order", func(t *testing.T) {
		out := mustGen(t, newEndpointRegistry(withErrors([]wiregen.ErrorRef{ref("gone", 410), ref("internal_error", 500)})...).clientFile)
		for _, want := range []string{
			`const aCodes = ["gone", "internal_error"] as const;`,
			"export type AError = (typeof aCodes)[number];",
			"  codes: aCodes,\n",
			"      codes: aCodes,\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("client missing %q:\n%s", want, out)
			}
		}
	})
}

// TestQueryTypeArgument: Params and Query structs become the members of one
// typed arguments object, each with its field's JSDoc; only scalar fields may
// cross a URL.
func TestQueryTypeArgument(t *testing.T) {
	t.Run("emits members and the query fold", func(t *testing.T) {
		e := get("listItems", "/api/items/{id}")
		e.Params = wiregen.TypeRef[client.ItemParams]()
		e.Query = wiregen.TypeRef[client.ListQuery]()
		r := newEndpointRegistry(e)
		r.Enums = []wiregen.EnumRef{wiregen.StringEnum[client.Kind]()}
		out := mustGen(t, r.clientFile)
		for _, want := range []string{
			"export interface ListItemsArgs {\n  readonly id: string;\n  /** Cursor resumes the list after the item it names. */\n  readonly cursor?: string;\n  readonly kind?: Kind;\n  readonly limit?: number;\n  readonly fresh?: boolean;\n}",
			"path: `/api/items/${globalThis.encodeURIComponent(globalThis.String(args.id))}` + qs([[\"cursor\", args.cursor], [\"kind\", args.kind], [\"limit\", args.limit], [\"fresh\", args.fresh]]),",
			"import type { Kind } from \"./types.gen.js\";",
			"export function listItems(args: ListItemsArgs, opts?: ClientOpts)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("client missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("an all-optional query may be omitted", func(t *testing.T) {
		e := get("listItems", "/api/items")
		e.Query = wiregen.TypeRef[client.ListQuery]()
		out := mustGen(t, newEndpointRegistry(e).clientFile)
		if want := "export function listItems(args: ListItemsArgs = {}, opts?: ClientOpts)"; !strings.Contains(out, want) {
			t.Errorf("client missing %q:\n%s", want, out)
		}
	})
	t.Run("a required query field keeps the argument required", func(t *testing.T) {
		e := get("adminUsers", "/api/users")
		e.Query = wiregen.TypeRef[client.RequiredQuery]()
		out := mustGen(t, newEndpointRegistry(e).clientFile)
		for _, want := range []string{"  readonly key: number;", "export function adminUsers(args: AdminUsersArgs, opts?: ClientOpts)"} {
			if !strings.Contains(out, want) {
				t.Errorf("client missing %q:\n%s", want, out)
			}
		}
	})
	refusals := []struct {
		name    string
		mutate  func(*wiregen.Endpoint)
		wantErr string
	}{
		{"slice params field", func(e *wiregen.Endpoint) {
			e.Path, e.Params = "/api/{ids}", wiregen.TypeRef[client.SliceParams]()
		}, "Params field SliceParams.IDs is string[]; a URL argument is a string, number, boolean or enum"},
		{"struct query field", func(e *wiregen.Endpoint) { e.Query = wiregen.TypeRef[client.NestedQuery]() }, "Query field NestedQuery.Item is Item"},
		{"optional params field", func(e *wiregen.Endpoint) {
			e.Path, e.Params = "/api/{id}", wiregen.TypeRef[client.OptionalParams]()
		}, "Params field id is optional"},
		{"params fields differ from placeholders", func(e *wiregen.Endpoint) {
			e.Path, e.Params = "/api/{key}", wiregen.TypeRef[client.ItemParams]()
		}, "Params fields [id] differ from its path placeholders [key]"},
		{"query field shares a param's name", func(e *wiregen.Endpoint) {
			e.Path, e.Params, e.Query = "/api/{id}", wiregen.TypeRef[client.ItemParams](), wiregen.TypeRef[client.ItemParams]()
		}, "argument id from both Params and Query"},
		{"query field named body beside a body", func(e *wiregen.Endpoint) {
			e.Method, e.Query, e.Request = "POST", wiregen.TypeRef[client.BodyQuery](), wiregen.TypeRef[client.StartRequest]()
		}, "argument body from both Query and Request"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			e := get("x", "/api/x")
			tc.mutate(&e)
			requireGenerateError(t, newEndpointRegistry(e), tc.wantErr)
		})
	}
	t.Run("union params", func(t *testing.T) {
		e := get("x", "/api/{type}")
		e.Params = wiregen.TypeRef[unions.Shape]()
		r := newEndpointRegistry(e)
		r.Unions = []wiregen.Union{shapeUnion()}
		requireGenerateError(t, r, "Params type Shape is a union")
	})
}

// TestHeadersArgument: a declared header is an argument of its endpoint's
// function, required or not, documented by its Description, sent in the
// spec's headers only when given.
func TestHeadersArgument(t *testing.T) {
	e := wiregen.Endpoint{
		Name: "startJob", Method: "POST", Path: "/api/jobs", Kind: wiregen.KindJSON,
		Headers: []wiregen.Header{
			{Name: "Idempotency-Key", Arg: "idempotencyKey", MinBytes: 1, MaxBytes: 255, Description: "Deduplicates a retried start."},
			{Name: "X-Trace", Arg: "trace", MaxBytes: 64, Required: true},
		},
	}
	out := mustGen(t, newEndpointRegistry(e).clientFile)
	for _, want := range []string{
		"  /** Deduplicates a retried start. */\n  readonly idempotencyKey?: string;\n  readonly trace: string;\n",
		"    if (args.idempotencyKey !== undefined) {\n      headers[\"Idempotency-Key\"] = args.idempotencyKey;\n    }\n",
		"    headers[\"X-Trace\"] = args.trace;\n",
		"      headers,\n",
		"export function startJob(args: StartJobArgs, opts?: ClientOpts)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("client missing %q:\n%s", want, out)
		}
	}
	plain := mustGen(t, newEndpointRegistry(get("ping", "/api/ping")).clientFile)
	for _, unwanted := range []string{"headers", "PingArgs", "args"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("an endpoint with no arguments emits %q:\n%s", unwanted, plain)
		}
	}
}

// TestKindsEmit: JSON and raw endpoints get a descriptor and a function; an
// event stream and a redirect only a descriptor.
func TestKindsEmit(t *testing.T) {
	item := wiregen.TypeRef[client.Item]()
	r := newEndpointRegistry(
		wiregen.Endpoint{Name: "getItem", Method: "GET", Path: "/api/item", Kind: wiregen.KindJSON, Response: item},
		wiregen.Endpoint{Name: "listItems", Method: "GET", Path: "/api/items", Kind: wiregen.KindJSON, Response: item, Array: true},
		wiregen.Endpoint{Name: "deleteItem", Method: "DELETE", Path: "/api/item", Kind: wiregen.KindJSON},
		wiregen.Endpoint{Name: "render", Method: "POST", Path: "/api/render", Kind: wiregen.KindRaw, ContentType: "text/vtt"},
		wiregen.Endpoint{Name: "stream", Method: "GET", Path: "/api/events", Kind: wiregen.KindEventStream},
		wiregen.Endpoint{Name: "callback", Method: "GET", Path: "/api/cb", Kind: wiregen.KindRedirect},
	)
	out := mustGen(t, r.clientFile)
	for _, want := range []string{
		"  decode: decodeItem,\n",
		"export function getItem(opts?: ClientOpts): globalThis.Promise<ApiResult<Item, GetItemError>> {\n  return clientRequest<Item, GetItemError>(getItemEndpoint.request(), getItemEndpoint.decode, opts);\n}",
		`  decode: (v: unknown): Item[] => v === null ? [] : decodeArray(v, decodeItem, "$"),`,
		"import { decodeArray } from \"./validators.gen.js\";",
		"  decode: (): void => undefined,\n",
		"globalThis.Promise<ApiResult<void, DeleteItemError>>",
		"  contentType: \"text/vtt\",\n",
		"      raw: true,\n",
		"  decode: decodeBlob,\n",
		"function decodeBlob(v: unknown): globalThis.Blob {",
		"globalThis.Promise<ApiResult<globalThis.Blob, RenderError>>",
		"export const streamEndpoint = {",
		"export const callbackEndpoint = {",
		"import { clientRequest } from \"./api-client.js\";\nimport type { ApiResult, EndpointSpec } from \"./api-client.js\";",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("client missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"function stream(", "function callback(", "PATH_"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("client emits %q:\n%s", unwanted, out)
		}
	}
	descOnly := mustGen(t, newEndpointRegistry(wiregen.Endpoint{Name: "stream", Method: "GET", Path: "/api/events", Kind: wiregen.KindEventStream}).clientFile)
	if !strings.HasPrefix(descOnly, "// Code generated by wiregen. DO NOT EDIT.\n\nimport type { EndpointSpec } from \"./api-client.js\";\n\nconst streamCodes") {
		t.Errorf("a descriptor-only client imports more than EndpointSpec:\n%s", descOnly)
	}
}

// TestExtraSuccessesValidated: each extra success is a status its endpoint's
// kind can answer, distinct from the primary and from the other extras, and
// each refusal names the endpoint, the status and the fix.
func TestExtraSuccessesValidated(t *testing.T) {
	item := wiregen.TypeRef[client.Item]()
	jsonEP := func(primary int, response wiregen.Type, extras ...wiregen.Success) wiregen.Endpoint {
		return wiregen.Endpoint{Name: "x", Method: "POST", Path: "/api/x", Kind: wiregen.KindJSON, Status: primary, Response: response, ExtraSuccesses: extras}
	}
	raw := func(extras ...wiregen.Success) wiregen.Endpoint {
		return wiregen.Endpoint{Name: "x", Method: "GET", Path: "/api/x", Kind: wiregen.KindRaw, ContentType: "text/vtt", ExtraSuccesses: extras}
	}
	stream := func(extras ...wiregen.Success) wiregen.Endpoint {
		return wiregen.Endpoint{Name: "x", Method: "GET", Path: "/api/x", Kind: wiregen.KindEventStream, ExtraSuccesses: extras}
	}
	redirect := func(primary int, extras ...wiregen.Success) wiregen.Endpoint {
		return wiregen.Endpoint{Name: "x", Method: "GET", Path: "/api/x", Kind: wiregen.KindRedirect, Status: primary, ExtraSuccesses: extras}
	}
	cases := []struct {
		name    string
		e       wiregen.Endpoint
		wantErr string
	}{
		{
			"raw extra with a body", raw(wiregen.Success{Status: 204, Response: item}),
			"wiregen: endpoint x is kind raw, so its extra success 204 cannot set Response or Array; drop them, since only kind json answers a JSON body",
		},
		{
			"raw extra with Array alone", raw(wiregen.Success{Status: 204, Array: true}),
			"wiregen: endpoint x is kind raw, so its extra success 204 cannot set Response or Array",
		},
		{
			"event-stream extra with a body", stream(wiregen.Success{Status: 204, Response: item}),
			"wiregen: endpoint x is kind event-stream, so its extra success 204 cannot set Response or Array",
		},
		{
			"redirect extra with a body", redirect(303, wiregen.Success{Status: 307, Response: item}),
			"wiregen: endpoint x is kind redirect, so its extra success 307 cannot set Response or Array",
		},
		{
			"array without a response", jsonEP(0, item, wiregen.Success{Status: 201, Array: true}),
			"wiregen: endpoint x extra success 201 sets Array without a Response; set Response to the element type or drop Array",
		},
		{
			"2xx on a redirect", redirect(303, wiregen.Success{Status: 200}),
			"wiregen: endpoint x is kind redirect and has extra success status 200, which is not a redirect; use 301, 302, 303, 307 or 308",
		},
		{
			"304 on a redirect", redirect(303, wiregen.Success{Status: 304}),
			"wiregen: endpoint x is kind redirect and has extra success status 304, which is not a redirect; use 301, 302, 303, 307 or 308",
		},
		{
			"zero status", jsonEP(0, item, wiregen.Success{Response: item}),
			"wiregen: endpoint x has extra success status 0, which is not a 2xx other than 205; set Status to one, and list a failure in Errors instead",
		},
		{
			"zero status without a body", jsonEP(0, item, wiregen.Success{}),
			"wiregen: endpoint x has extra success status 0, which is not a 2xx other than 205",
		},
		{
			"3xx on json", jsonEP(0, item, wiregen.Success{Status: 302, Response: item}),
			"wiregen: endpoint x has extra success status 302, which is not a 2xx other than 205",
		},
		{
			"4xx on json", jsonEP(0, item, wiregen.Success{Status: 404, Response: item}),
			"wiregen: endpoint x has extra success status 404, which is not a 2xx other than 205",
		},
		{
			"205 on json", jsonEP(0, item, wiregen.Success{Status: 205}),
			"wiregen: endpoint x has extra success status 205, which is not a 2xx other than 205",
		},
		{
			"3xx on raw", raw(wiregen.Success{Status: 302}),
			"wiregen: endpoint x has extra success status 302, which is not a 2xx other than 205",
		},
		{
			"204 with a body", jsonEP(0, item, wiregen.Success{Status: 204, Response: item}),
			"wiregen: endpoint x extra success 204 sets a Response, but a 204 has no body; drop Response or use another 2xx",
		},
		{
			"2xx without a body", jsonEP(0, item, wiregen.Success{Status: 201}),
			"wiregen: endpoint x extra success 201 has no Response, but only a 204 has no body; set Response or use 204",
		},
		{
			"raw extra other than 204", raw(wiregen.Success{Status: 206}),
			"wiregen: endpoint x is kind raw, whose only extra success is a 204 with no body, and has extra success status 206; use 204",
		},
		{
			"event-stream extra other than 204", stream(wiregen.Success{Status: 201}),
			"wiregen: endpoint x is kind event-stream, whose only extra success is a 204 with no body, and has extra success status 201; use 204",
		},
		{
			"the explicit primary", jsonEP(201, item, wiregen.Success{Status: 201, Response: item}),
			"wiregen: endpoint x extra success 201 is its primary success status; drop it from ExtraSuccesses, or give the primary another Status",
		},
		{
			"the defaulted 200 primary", jsonEP(0, item, wiregen.Success{Status: 200, Response: item}),
			"wiregen: endpoint x extra success 200 is its primary success status",
		},
		{
			"the defaulted 204 primary", jsonEP(0, wiregen.Type{}, wiregen.Success{Status: 204}),
			"wiregen: endpoint x extra success 204 is its primary success status",
		},
		{
			"the defaulted 302 primary", redirect(0, wiregen.Success{Status: 302}),
			"wiregen: endpoint x extra success 302 is its primary success status",
		},
		{
			"listed twice", jsonEP(0, item, wiregen.Success{Status: 201, Response: item}, wiregen.Success{Status: 201, Response: item}),
			"wiregen: endpoint x lists extra success 201 twice; drop one",
		},
		{
			"unregistered type", jsonEP(0, item, wiregen.Success{Status: 201, Response: wiregen.Type{PkgPath: "github.com/cplieger/go/wiregen/v2/testdata/client", Name: "Nope"}}),
			"wiregen: endpoint x extra success 201 Response type github.com/cplieger/go/wiregen/v2/testdata/client.Nope is not found in its package",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireGenerateError(t, newEndpointRegistry(tc.e), tc.wantErr)
		})
	}
}

// TestExtraSuccessesEmit: a function endpoint with extra successes decodes
// the body its answer's status declares, returns the union of the declared
// bodies and throws on any other status; a descriptor-only kind stays one.
func TestExtraSuccessesEmit(t *testing.T) {
	item := wiregen.TypeRef[client.Item]()
	job := wiregen.TypeRef[client.Job]()
	r := newEndpointRegistry(
		wiregen.Endpoint{
			Name: "register", Method: "POST", Path: "/api/register", Kind: wiregen.KindJSON, Status: 201, Response: job,
			ExtraSuccesses: []wiregen.Success{{Status: 200, Response: job}},
		},
		wiregen.Endpoint{
			Name: "waitEvents", Method: "POST", Path: "/api/events/wait", Kind: wiregen.KindJSON,
			Response: wiregen.TypeRef[client.Position](), ExtraSuccesses: []wiregen.Success{{Status: 204}},
		},
		wiregen.Endpoint{
			Name: "batch", Method: "POST", Path: "/api/batch", Kind: wiregen.KindJSON, Response: item,
			ExtraSuccesses: []wiregen.Success{{Status: 202, Response: job}, {Status: 206, Response: item, Array: true}},
		},
		wiregen.Endpoint{
			Name: "touch", Method: "POST", Path: "/api/touch", Kind: wiregen.KindJSON,
			ExtraSuccesses: []wiregen.Success{{Status: 200, Response: item}},
		},
		wiregen.Endpoint{
			Name: "thumb", Method: "GET", Path: "/api/thumb", Kind: wiregen.KindRaw, ContentType: "image/png",
			ExtraSuccesses: []wiregen.Success{{Status: 204}},
		},
		wiregen.Endpoint{
			Name: "feed", Method: "GET", Path: "/api/feed", Kind: wiregen.KindEventStream,
			ExtraSuccesses: []wiregen.Success{{Status: 204}},
		},
		wiregen.Endpoint{
			Name: "signOut", Method: "GET", Path: "/api/sign-out", Kind: wiregen.KindRedirect, Status: 303,
			ExtraSuccesses: []wiregen.Success{{Status: 307}},
		},
	)
	out := mustGen(t, r.clientFile)
	for _, want := range []string{
		"  decode: (v: unknown, status: number): Job => {\n" +
			"    switch (status) {\n" +
			"      case 201:\n        return decodeJob(v);\n" +
			"      case 200:\n        return decodeJob(v);\n" +
			"      default:\n        throw new globalThis.TypeError(`$: expected status 201 or 200, got ${status}`);\n" +
			"    }\n  },\n",
		"export function register(opts?: ClientOpts): globalThis.Promise<ApiResult<Job, RegisterError>> {\n" +
			"  return clientRequest<Job, RegisterError>(registerEndpoint.request(), registerEndpoint.decode, opts);\n}",
		"  decode: (v: unknown, status: number): Position | undefined => {\n" +
			"    switch (status) {\n" +
			"      case 200:\n        return decodePosition(v);\n" +
			"      case 204:\n        return undefined;\n" +
			"      default:\n        throw new globalThis.TypeError(`$: expected status 200 or 204, got ${status}`);\n" +
			"    }\n  },\n",
		"globalThis.Promise<ApiResult<Position | undefined, WaitEventsError>>",
		"  decode: (v: unknown, status: number): Item | Job | Item[] => {\n" +
			"    switch (status) {\n" +
			"      case 200:\n        return decodeItem(v);\n" +
			"      case 202:\n        return decodeJob(v);\n" +
			"      case 206:\n        return v === null ? [] : decodeArray(v, decodeItem, \"$\");\n" +
			"      default:\n        throw new globalThis.TypeError(`$: expected status 200, 202 or 206, got ${status}`);\n" +
			"    }\n  },\n",
		"globalThis.Promise<ApiResult<Item | Job | Item[], BatchError>>",
		"import { decodeArray } from \"./validators.gen.js\";",
		"  decode: (v: unknown, status: number): undefined | Item => {\n" +
			"    switch (status) {\n" +
			"      case 204:\n        return undefined;\n" +
			"      case 200:\n        return decodeItem(v);\n" +
			"      default:\n        throw new globalThis.TypeError(`$: expected status 204 or 200, got ${status}`);\n" +
			"    }\n  },\n",
		"globalThis.Promise<ApiResult<undefined | Item, TouchError>>",
		"  decode: (v: unknown, status: number): globalThis.Blob | undefined => {\n" +
			"    switch (status) {\n" +
			"      case 200:\n        return decodeBlob(v);\n" +
			"      case 204:\n        return undefined;\n" +
			"      default:\n        throw new globalThis.TypeError(`$: expected status 200 or 204, got ${status}`);\n" +
			"    }\n  },\n",
		"function decodeBlob(v: unknown): globalThis.Blob {",
		"globalThis.Promise<ApiResult<globalThis.Blob | undefined, ThumbError>>",
		"export const feedEndpoint = {",
		"export const signOutEndpoint = {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("client missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"function feed(", "function signOut(", "ApiResult<Job | Job,"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("client emits %q:\n%s", unwanted, out)
		}
	}
}

func TestEndpointDocs(t *testing.T) {
	e := get("getItem", "/api/item")
	e.Summary = "Read one item."
	e.Description = "Answers the item.\nA closing */ stays inside."
	out := mustGen(t, newEndpointRegistry(e).clientFile)
	doc := "/**\n * Read one item.\n *\n * Answers the item.\n * A closing *\\/ stays inside.\n */\n"
	if n := strings.Count(out, doc); n != 2 {
		t.Errorf("client has the JSDoc %d times, want 2 (descriptor and function):\n%s", n, out)
	}
}

// TestEndpointTypesJoinRegistry: a type an endpoint, an event or the error
// envelope names is generated without being listed in Types.
func TestEndpointTypesJoinRegistry(t *testing.T) {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	e := wiregen.Endpoint{
		Name: "startJob", Method: "POST", Path: "/api/jobs", Kind: wiregen.KindJSON,
		Request: wiregen.TypeRef[client.StartRequest](), Response: wiregen.TypeRef[client.Job](),
	}
	r.Endpoints = []wiregen.Endpoint{e}
	r.Events = []wiregen.Event{{Name: "tick", Data: wiregen.TypeRef[client.Tick]()}}
	r.ErrorEnvelope = wiregen.TypeRef[client.ErrorBody]()
	types := mustGen(t, r.typesFile)
	for _, want := range []string{"export interface StartRequest {", "export interface Job {", "export interface Tick {", "export interface ErrorBody {"} {
		if !strings.Contains(types, want) {
			t.Errorf("types missing %q:\n%s", want, types)
		}
	}
	t.Run("named in two places once", func(t *testing.T) {
		r.Types = []wiregen.Type{wiregen.TypeRef[client.Job]()}
		r.Events = append(r.Events, wiregen.Event{Name: "job", Data: wiregen.TypeRef[client.Job]()})
		types := mustGen(t, r.typesFile)
		if n := strings.Count(types, "export interface Job {"); n != 1 {
			t.Errorf("Job emitted %d times, want 1", n)
		}
	})
	t.Run("named only by an extra success", func(t *testing.T) {
		r := newEndpointRegistry(wiregen.Endpoint{
			Name: "startJob", Method: "POST", Path: "/api/jobs", Kind: wiregen.KindJSON, Response: wiregen.TypeRef[client.Item](),
			ExtraSuccesses: []wiregen.Success{{Status: 202, Response: wiregen.TypeRef[client.Job]()}},
		})
		if types := mustGen(t, r.typesFile); !strings.Contains(types, "export interface Job {") {
			t.Errorf("types missing Job:\n%s", types)
		}
		if decoders := mustGen(t, r.decodersFile); !strings.Contains(decoders, "export const decodeJob: Decoder<Job>") {
			t.Errorf("decoders missing decodeJob:\n%s", decoders)
		}
		if arbs := mustGen(t, r.arbitrariesFile); !strings.Contains(arbs, "export const arbJob: fc.Arbitrary<Job>") {
			t.Errorf("arbitraries missing arbJob:\n%s", arbs)
		}
	})
	t.Run("unnamed reference refused", func(t *testing.T) {
		r := newEndpointRegistry(wiregen.Endpoint{
			Name: "x", Method: "GET", Path: "/api/x", Kind: wiregen.KindJSON,
			Response: wiregen.TypeOf(reflect.TypeFor[[]int]()),
		})
		requireGenerateError(t, r, "endpoint x Response type", "[]int", "names no package-level type")
	})
	t.Run("same name from another package refused", func(t *testing.T) {
		r := newEndpointRegistry(wiregen.Endpoint{
			Name: "x", Method: "GET", Path: "/api/x", Kind: wiregen.KindJSON,
			Response: wiregen.Type{PkgPath: "github.com/cplieger/go/wiregen/v2/testdata/crossref", Name: "Item"},
		})
		requireGenerateError(t, r, "registered from two packages")
	})
}

// TestEventUnion: Events emit one discriminated frame union, a decoder per
// name and a frame decoder.
func TestEventUnion(t *testing.T) {
	r := newTestRegistry()
	r.Events = []wiregen.Event{
		{Name: "position", Data: wiregen.TypeRef[client.Position]()},
		{Name: "status-resync", Data: wiregen.TypeRef[client.Tick]()},
	}
	out := mustGen(t, r.eventsFile)
	for _, want := range []string{
		"export type EventFrame =\n  | { readonly event: \"position\"; readonly data: Position }\n  | { readonly event: \"status-resync\"; readonly data: Tick };\n",
		"export type EventName = EventFrame[\"event\"];",
		"  position: decodePosition,\n  \"status-resync\": decodeTick,\n",
		"    case \"status-resync\":\n      return { event: \"status-resync\", data: decodeTick(data) };\n",
		"import { decodePosition, decodeTick } from \"./decoders.gen.js\";",
		"import type { Position, Tick } from \"./types.gen.js\";",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("events module missing %q:\n%s", want, out)
		}
	}
	for _, tc := range []struct {
		name    string
		events  []wiregen.Event
		wantErr string
	}{
		{"empty name", []wiregen.Event{{Data: wiregen.TypeRef[client.Tick]()}}, `event name ""`},
		{"line break", []wiregen.Event{{Name: "a\nb", Data: wiregen.TypeRef[client.Tick]()}}, "no line break"},
		{"twice", []wiregen.Event{{Name: "a", Data: wiregen.TypeRef[client.Tick]()}, {Name: "a", Data: wiregen.TypeRef[client.Tick]()}}, `event "a" is registered twice`},
		{"no data", []wiregen.Event{{Name: "a"}}, `event "a" has no Data type`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			r.Events = tc.events
			requireGenerateError(t, r, tc.wantErr)
		})
	}
	t.Run("type shadowing an export refused", func(t *testing.T) {
		r := newTestRegistry()
		r.Events = []wiregen.Event{{Name: "a", Data: wiregen.TypeRef[client.Tick]()}}
		r.TSNameOverride = map[string]string{"Tick": "EventFrame"}
		requireGenerateError(t, r, "the events module and type github.com/cplieger/go/wiregen/v2/testdata/client.Tick both emit the TS identifier EventFrame")
	})
}

func TestEndpoints_TransportImportRequired(t *testing.T) {
	r := newTestRegistry()
	r.Endpoints = []wiregen.Endpoint{get("getItem", "/api/item")}
	requireGenerateError(t, r, "WithTransportImport must be set")
}

// TestGenerateWritesUnderTSDir: every TS file lands under WithTSDir's
// directory and nothing outside it; with no WithTSDir Generate refuses.
func TestGenerateWritesUnderTSDir(t *testing.T) {
	root := t.TempDir()
	tsDir := filepath.Join(root, "web", "wire")
	r := newEndpointRegistry(get("getItem", "/api/item"))
	r.Events = []wiregen.Event{{Name: "tick", Data: wiregen.TypeRef[client.Tick]()}}
	r.Constants = []wiregen.Const{{TSName: "X", Value: 1}}
	r.opts = append(r.opts, wiregen.WithArbitrariesFile("arbitraries.gen.ts"))
	if err := r.Generate(t.Context(), append(r.opts, wiregen.WithTSDir(tsDir))...); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var got []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			got = append(got, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"web/wire/arbitraries.gen.ts", "web/wire/client.gen.ts", "web/wire/constants.gen.ts", "web/wire/decoders.gen.ts",
		"web/wire/events.gen.ts", "web/wire/types.gen.ts", "web/wire/validators.gen.ts",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Generate wrote:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if err := (&wiregen.Registry{}).Generate(t.Context()); err == nil || !strings.Contains(err.Error(), "WithTSDir must be set") {
		t.Errorf("Generate() without WithTSDir = %v, want the WithTSDir refusal", err)
	}
}

// TestGenerate_ClientFileOnlyWithEndpoints: the client file is written
// exactly when endpoints are registered.
func TestGenerate_ClientFileOnlyWithEndpoints(t *testing.T) {
	dir := t.TempDir()
	if err := newEndpointRegistry(get("getItem", "/api/item")).generate(t.Context(), dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "client.gen.ts")); err != nil {
		t.Errorf("client.gen.ts not written: %v", err)
	}
	dir2 := t.TempDir()
	if err := newEndpointRegistry().generate(t.Context(), dir2); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "client.gen.ts")); !os.IsNotExist(err) {
		t.Errorf("client.gen.ts written with no endpoints registered (stat err=%v)", err)
	}
}
