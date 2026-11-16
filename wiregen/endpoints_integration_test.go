//go:build integration

package wiregen_test

import (
	"os"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/client"
)

// transportTS is the recording transport the generated client imports.
func transportTS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/client/api-client.ts")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const clientMain = `import { recorded, respondWith } from "./api-client.js";
import {
  adminUsers, deleteItem, eventsStreamEndpoint, getItem, listItems, oidcCallbackEndpoint, poster, register, startJob,
  startJobEndpoint, thumbnail, waitEvents,
} from "./client.gen.js";
import type { StartJobError } from "./client.gen.js";
import { decodeEventFrame } from "./events.gen.js";

const log = (label: string, v: unknown): void => console.log(label + " " + JSON.stringify(v));
const last = () => recorded[recorded.length - 1];

respondWith((spec) => spec.path.startsWith("/api/items/") && spec.method === "GET"
  ? { status: 200, body: { id: "a/b", name: "n" } }
  : { status: 200, body: [{ id: "1", name: "x" }] });
log("getItem", await getItem({ id: "a/b" }));
log("getItem.spec", last());
log("listItems", await listItems({ kind: "movie", limit: 5 }));
log("listItems.spec", last());
await listItems();
log("listItems.bare", last().path);
respondWith(() => ({ status: 200, body: null }));
log("listItems.null", await listItems());

respondWith(() => ({ status: 202, body: { id: "job-1" } }));
log("startJob", await startJob({ body: { item: "a" }, idempotencyKey: "k" }));
log("startJob.spec", last());
await startJob({ body: { item: "a" } });
log("startJob.nokey", last().headers ?? null);

const codes: readonly StartJobError[] = startJobEndpoint.codes;
log("startJob.codes", codes);

respondWith(() => ({ status: 429, body: { code: "job_capacity" } }));
log("startJob.429", await startJob({ body: { item: "a" } }));
respondWith(() => ({ status: 409, body: { code: "not_listed" } }));
log("startJob.409", await startJob({ body: { item: "a" } }));

respondWith(() => ({ status: 204 }));
log("deleteItem", await deleteItem({ id: "x" }));
log("adminUsers.spec", (await adminUsers({ key: 7 }), last().path));

respondWith(() => ({ status: 200, body: "jpeg" }));
const p = await poster({ id: "r1" });
log("poster", p.ok && p.data instanceof Blob ? [p.data.type, await p.data.text()] : p);
log("poster.spec", last());

respondWith(() => ({ status: 201, body: { id: "j1" } }));
log("register.201", await register({ body: { item: "a" } }));
respondWith(() => ({ status: 200, body: { id: "j1" } }));
log("register.200", await register({ body: { item: "a" } }));
respondWith(() => ({ status: 202, body: { id: "j1" } }));
log("register.202", await register({ body: { item: "a" } }));
respondWith(() => ({ status: 200, body: { rev: 4 } }));
log("waitEvents.200", await waitEvents());
respondWith(() => ({ status: 204 }));
log("waitEvents.204", await waitEvents());
respondWith(() => ({ status: 200, body: { rev: "x" } }));
log("waitEvents.bad", await waitEvents());
respondWith(() => ({ status: 204 }));
log("thumbnail.204", await thumbnail({ id: "r1" }));
respondWith(() => ({ status: 200, body: "png" }));
const th = await thumbnail({ id: "r1" });
log("thumbnail.200", th.ok && th.data instanceof Blob ? await th.data.text() : th);

log("events.spec", eventsStreamEndpoint.request());
log("oidc.spec", oidcCallbackEndpoint.request());
log("frame", decodeEventFrame("position", { rev: 3 }));
try {
  decodeEventFrame("nope", {});
} catch (e) {
  log("frame.unknown", (e as Error).message);
}
try {
  decodeEventFrame("tick", { at: "x" });
} catch (e) {
  log("frame.bad", (e as Error).message);
}
`

// clientRegistry is a registry over every kind, with params, a query, a
// body, a declared header, an array response, no-body responses, extra
// successes and events.
func clientRegistry() *testRegistry {
	r := newTestRegistry(wiregen.WithTransportImport("./api-client.js"))
	params := wiregen.TypeRef[client.ItemParams]()
	internal := wiregen.ErrorRef{Code: "internal_error", Status: 500}
	r.Endpoints = []wiregen.Endpoint{
		{
			Name: "getItem", Method: "GET", Path: "/api/items/{id}", Kind: wiregen.KindJSON,
			Params: params, Response: wiregen.TypeRef[client.Item](),
			Errors: []wiregen.ErrorRef{{Code: "item_unknown", Status: 404}, internal},
		},
		{
			Name: "listItems", Method: "GET", Path: "/api/items", Kind: wiregen.KindJSON,
			Query: wiregen.TypeRef[client.ListQuery](), Response: wiregen.TypeRef[client.Item](), Array: true,
			Errors: []wiregen.ErrorRef{internal},
		},
		{
			Name: "startJob", Method: "POST", Path: "/api/jobs", Kind: wiregen.KindJSON,
			Request: wiregen.TypeRef[client.StartRequest](), Response: wiregen.TypeRef[client.Job](),
			Headers: []wiregen.Header{{Name: "Idempotency-Key", Arg: "idempotencyKey", MinBytes: 1, MaxBytes: 255}},
			Errors:  []wiregen.ErrorRef{{Code: "job_capacity", Status: 429}, internal},
		},
		{
			Name: "deleteItem", Method: "DELETE", Path: "/api/items/{id}", Kind: wiregen.KindJSON,
			Params: params, Errors: []wiregen.ErrorRef{internal},
		},
		{
			Name: "adminUsers", Method: "GET", Path: "/api/users", Kind: wiregen.KindJSON,
			Query: wiregen.TypeRef[client.RequiredQuery](), Response: wiregen.TypeRef[client.Item](), Array: true,
		},
		{
			Name: "poster", Method: "GET", Path: "/api/items/{id}/poster", Kind: wiregen.KindRaw,
			Params: params, ContentType: "image/jpeg", Errors: []wiregen.ErrorRef{internal},
		},
		{
			Name: "register", Method: "POST", Path: "/api/register", Kind: wiregen.KindJSON, Status: 201,
			Request: wiregen.TypeRef[client.StartRequest](), Response: wiregen.TypeRef[client.Job](),
			ExtraSuccesses: []wiregen.Success{{Status: 200, Response: wiregen.TypeRef[client.Job]()}},
			Errors:         []wiregen.ErrorRef{internal},
		},
		{
			Name: "waitEvents", Method: "POST", Path: "/api/events/wait", Kind: wiregen.KindJSON,
			Response: wiregen.TypeRef[client.Position](), ExtraSuccesses: []wiregen.Success{{Status: 204}},
		},
		{
			Name: "thumbnail", Method: "GET", Path: "/api/items/{id}/thumbnail", Kind: wiregen.KindRaw,
			Params: params, ContentType: "image/jpeg", ExtraSuccesses: []wiregen.Success{{Status: 204}},
		},
		{Name: "eventsStream", Method: "GET", Path: "/api/events", Kind: wiregen.KindEventStream},
		{Name: "oidcCallback", Method: "GET", Path: "/api/oidc/callback", Kind: wiregen.KindRedirect},
	}
	r.Events = []wiregen.Event{
		{Name: "position", Data: wiregen.TypeRef[client.Position]()},
		{Name: "tick", Data: wiregen.TypeRef[client.Tick]()},
	}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[client.Kind]()}
	r.ErrorEnvelope = wiregen.TypeRef[client.ErrorBody]()
	return r
}

// TestClientAgainstTransport compiles the generated client against a
// transport exporting only clientRequest, EndpointSpec and ApiResult, and
// runs every endpoint kind through it under node.
func TestClientAgainstTransport(t *testing.T) {
	out := runTS(t, clientRegistry(), map[string]string{"api-client.ts": transportTS(t), "main.ts": clientMain})
	want := strings.Join([]string{
		`getItem {"ok":true,"data":{"id":"a/b","name":"n"}}`,
		`getItem.spec {"method":"GET","path":"/api/items/a%2Fb","codes":["item_unknown","internal_error"]}`,
		`listItems {"ok":true,"data":[{"id":"1","name":"x"}]}`,
		`listItems.spec {"method":"GET","path":"/api/items?kind=movie&limit=5","codes":["internal_error"]}`,
		`listItems.bare "/api/items"`,
		`listItems.null {"ok":true,"data":[]}`,
		`startJob {"ok":true,"data":{"id":"job-1"}}`,
		`startJob.spec {"method":"POST","path":"/api/jobs","body":{"item":"a"},"headers":{"Idempotency-Key":"k"},"codes":["job_capacity","internal_error"]}`,
		`startJob.nokey {}`,
		`startJob.codes ["job_capacity","internal_error"]`,
		`startJob.429 {"ok":false,"status":429,"code":"job_capacity"}`,
		`startJob.409 {"ok":false,"status":409,"code":"http"}`,
		`deleteItem {"ok":true}`,
		`adminUsers.spec "/api/users?key=7"`,
		`poster ["image/jpeg","jpeg"]`,
		`poster.spec {"method":"GET","path":"/api/items/r1/poster","raw":true,"codes":["internal_error"]}`,
		`register.201 {"ok":true,"data":{"id":"j1"}}`,
		`register.200 {"ok":true,"data":{"id":"j1"}}`,
		`register.202 {"ok":false,"status":202,"code":"decode"}`,
		`waitEvents.200 {"ok":true,"data":{"rev":4}}`,
		`waitEvents.204 {"ok":true}`,
		`waitEvents.bad {"ok":false,"status":200,"code":"decode"}`,
		`thumbnail.204 {"ok":true}`,
		`thumbnail.200 "png"`,
		`events.spec {"method":"GET","path":"/api/events","codes":[]}`,
		`oidc.spec {"method":"GET","path":"/api/oidc/callback","codes":[]}`,
		`frame {"event":"position","data":{"rev":3}}`,
		`frame.unknown "unknown event \"nope\""`,
		`frame.bad "$.tick.at: expected number, got string"`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// TestClientRefusedByTypes pins what the generated types forbid: each
// fixture must fail tsc naming its cause.
func TestClientRefusedByTypes(t *testing.T) {
	importing := func(body string) string {
		return `import { getItem, listItems, startJob } from "./client.gen.js";
import type { StartJobError } from "./client.gen.js";
void getItem; void listItems; void startJob;
` + body
	}
	cases := []struct {
		name, main, want string
		transport        func(string) string
	}{
		{"header argument on an endpoint without it", importing(`void listItems({ idempotencyKey: "k" });`), "idempotencyKey", nil},
		{"code outside the union", importing(`const e: StartJobError = "item_unknown"; void e;`), `"item_unknown"`, nil},
		{"function for an event stream", `import { eventsStream } from "./client.gen.js"; void eventsStream;`, "eventsStream", nil},
		{"function for a redirect", `import { oidcCallback } from "./client.gen.js"; void oidcCallback;`, "oidcCallback", nil},
		{"required param missing", importing(`void getItem({});`), "id", nil},
		{"transport without codes", importing(""), "codes", func(ts string) string {
			return strings.Replace(ts, "  readonly codes: readonly string[];\n", "", 1)
		}},
		{"transport with the v3 functions only", importing(""), "clientRequest", func(ts string) string {
			return strings.Replace(ts, "export function clientRequest<", "export function clientRequestV3<", 1)
		}},
		{"transport whose decode takes no status", `import { register } from "./client.gen.js"; void register;`, "Target signature provides too few arguments", func(ts string) string {
			ts = strings.Replace(ts, "  decode: (v: unknown, status: number) => T,\n", "  decode: (v: unknown) => T,\n", 1)
			return strings.Replace(ts, "decode(body, answer.status)", "decode(body)", 1)
		}},
		{"bodiless status in the union", `import { waitEvents } from "./client.gen.js";
import type { Position } from "./types.gen.js";
const r = await waitEvents();
if (r.ok) {
  const p: Position = r.data;
  void p;
}
`, "Position | undefined", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := transportTS(t)
			if tc.transport != nil {
				transport = tc.transport(transport)
			}
			tscFails(t, clientRegistry(), map[string]string{"api-client.ts": transport, "main.ts": tc.main}, tc.want)
		})
	}
}
