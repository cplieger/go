import { createFakeServer, type GeneratedEndpoint } from "./fake.js";
import type { TransportCase, TransportFixture } from "./transport.js";

// The rows' decoders, written as the generated client writes them: required members checked,
// and a row with several success statuses switching on the status.
function record(v: unknown): Readonly<Record<string, unknown>> {
  if (typeof v !== "object" || v === null) {
    throw new TypeError("not an object");
  }
  return v as Readonly<Record<string, unknown>>;
}

function text(o: Readonly<Record<string, unknown>>, name: string): string {
  const v = o[name];
  if (typeof v !== "string") {
    throw new TypeError(`${name} is not a string`);
  }
  return v;
}

interface Note {
  readonly id: string;
  readonly text: string;
  readonly tag: string;
}

function decodeNote(v: unknown): Note {
  const o = record(v);
  return { id: text(o, "id"), text: text(o, "text"), tag: text(o, "tag") };
}

interface User {
  readonly id: string;
  readonly name: string;
}

function decodeUser(v: unknown, status: number): User {
  if (status !== 201 && status !== 200) {
    throw new TypeError(`users.register declares no status ${String(status)}`);
  }
  const o = record(v);
  return { id: text(o, "id"), name: text(o, "name") };
}

interface Nudge {
  readonly kind: string;
}

function decodeNudge(v: unknown, status: number): Nudge | undefined {
  if (status === 204) {
    return undefined;
  }
  if (status !== 200) {
    throw new TypeError(`events.wait declares no status ${String(status)}`);
  }
  return { kind: text(record(v), "kind") };
}

const endpoints: Readonly<Record<string, GeneratedEndpoint<unknown>>> = {
  "notes.read": {
    name: "notes.read",
    method: "GET",
    path: "/api/notes/{id}",
    decode: decodeNote,
    codes: ["not_found", "rate_limited"],
    statuses: [200],
  },
  "notes.save": {
    name: "notes.save",
    method: "PUT",
    path: "/api/notes/{id}",
    decode: decodeNote,
    codes: [
      "bad_body",
      "if_match_invalid",
      "if_match_required",
      "not_found",
      "stale_version",
      "wire_mismatch",
    ],
    statuses: [200],
  },
  "users.register": {
    name: "users.register",
    method: "POST",
    path: "/api/users",
    decode: decodeUser,
    codes: [
      "bad_body",
      "idempotency_in_flight",
      "idempotency_key_invalid",
      "idempotency_key_required",
      "idempotency_key_reused",
      "wire_mismatch",
    ],
    statuses: [201, 200],
  },
  "events.wait": {
    name: "events.wait",
    method: "GET",
    path: "/api/events/wait",
    decode: decodeNudge,
    codes: ["held_limit"],
    statuses: [200, 204],
  },
};

/**
 * The shared contract case of a generated client and its transport, over four rows: a read whose
 * tag a save sends as `If-Match`, a `Keyed` 201-or-200 write, and a held 200-or-204 read. The
 * same cases sit in httpwire's Go test, which accepts every header they send.
 */
export const TRANSPORT_FIXTURE: TransportFixture = {
  wireVersion: "0123456789abcdef",
  errorCodes: {
    bad_body: {
      status: 400,
      remediation: "Fix the request body as the fields describe, then resend it.",
    },
    held_limit: {
      status: 429,
      remediation: "Wait for an open request or stream to end, then retry.",
    },
    idempotency_in_flight: {
      status: 409,
      remediation:
        "Wait, then resend with the same Idempotency-Key to get the first request's answer.",
    },
    idempotency_key_invalid: {
      status: 400,
      remediation: 'Send the key as a quoted string: Idempotency-Key: "<key>".',
    },
    idempotency_key_required: {
      status: 400,
      remediation: 'Resend with an Idempotency-Key header: Idempotency-Key: "<key>".',
    },
    idempotency_key_reused: {
      status: 422,
      remediation: "Use a new Idempotency-Key for a different request.",
    },
    if_match_invalid: {
      status: 400,
      remediation: "Send If-Match as the quoted tag a read of the row returned.",
    },
    if_match_required: {
      status: 428,
      remediation: "Read the row, then resend with If-Match set to the tag the read returned.",
    },
    not_found: { status: 404, remediation: "Check the path; nothing is served at it." },
    rate_limited: { status: 429, remediation: "Wait for the Retry-After delay, then retry." },
    stale_version: {
      status: 412,
      remediation: "Read the row again, reapply the change, and resend with the new tag.",
    },
    wire_mismatch: {
      status: 409,
      remediation: "Reload the client: the server's API changed since it loaded.",
    },
  },
  endpoints,
  cases: [
    {
      name: "read answers the tag a save sends",
      row: "notes.read",
      params: { id: "n1" },
      request: { method: "GET", path: "/api/notes/n1", headers: {} },
      answer: {
        kind: "answer",
        status: 200,
        data: { id: "n1", text: "first", tag: "0123456789abcdef.7" },
      },
      want: { ok: true, status: 200, data: { id: "n1", text: "first", tag: "0123456789abcdef.7" } },
    },
    {
      name: "save sends the read's tag as If-Match",
      row: "notes.save",
      params: { id: "n1" },
      body: { text: "second" },
      tag: "0123456789abcdef.7",
      request: {
        method: "PUT",
        path: "/api/notes/n1",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"0123456789abcdef.7"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: {
        kind: "answer",
        status: 200,
        data: { id: "n1", text: "second", tag: "0123456789abcdef.8" },
      },
      want: {
        ok: true,
        status: 200,
        data: { id: "n1", text: "second", tag: "0123456789abcdef.8" },
      },
    },
    {
      name: "save over a newer tag is stale",
      row: "notes.save",
      params: { id: "n1" },
      body: { text: "third" },
      tag: "0123456789abcdef.7",
      request: {
        method: "PUT",
        path: "/api/notes/n1",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"0123456789abcdef.7"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: { kind: "refuse", code: "stale_version" },
      want: { ok: false, failure: "http", status: 412, code: "stale_version" },
    },
    {
      name: "save with a bare revision sends nothing",
      row: "notes.save",
      params: { id: "n1" },
      body: { text: "fourth" },
      tag: "7",
      want: { ok: false, failure: "invalid", status: 0 },
    },
    {
      name: "save with a revision past 2^64-1 sends nothing",
      row: "notes.save",
      params: { id: "n1" },
      body: { text: "fifth" },
      tag: "0123456789abcdef.18446744073709551616",
      want: { ok: false, failure: "invalid", status: 0 },
    },
    {
      name: "register first run answers 201",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "00000000-0000-4000-8000-000000000000",
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"00000000-0000-4000-8000-000000000000"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: { kind: "answer", status: 201, data: { id: "u1", name: "ada" } },
      want: { ok: true, status: 201, data: { id: "u1", name: "ada" } },
    },
    {
      name: "register resent with its key replays 200",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "00000000-0000-4000-8000-000000000000",
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"00000000-0000-4000-8000-000000000000"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: { kind: "answer", status: 200, data: { id: "u1", name: "ada" } },
      want: { ok: true, status: 200, data: { id: "u1", name: "ada" } },
    },
    {
      name: "register escapes a quote and a backslash in its key",
      row: "users.register",
      params: {},
      body: { name: "grace" },
      key: 'a"b\\c',
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"a\\"b\\\\c"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: { kind: "answer", status: 201, data: { id: "u2", name: "grace" } },
      want: { ok: true, status: 201, data: { id: "u2", name: "grace" } },
    },
    {
      name: "register while its first run is in flight",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "00000000-0000-4000-8000-000000000000",
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"00000000-0000-4000-8000-000000000000"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: { kind: "refuse", code: "idempotency_in_flight" },
      want: { ok: false, failure: "http", status: 409, code: "idempotency_in_flight" },
    },
    {
      name: "register with an empty key sends nothing",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "",
      want: { ok: false, failure: "invalid", status: 0 },
    },
    {
      name: "register with a 256-byte key sends nothing",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk",
      want: { ok: false, failure: "invalid", status: 0 },
    },
    {
      name: "register refused with a code outside its union is decode",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "00000000-0000-4000-8000-000000000000",
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"00000000-0000-4000-8000-000000000000"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: {
        kind: "raw",
        status: 404,
        headers: {
          "Content-Type": "application/json",
          "Wire-Version": "0123456789abcdef",
          "X-Request-Id": "req-1",
        },
        body: '{"code":"not_found","message":"not found","remediation":"Check the path; nothing is served at it.","request_id":"req-1"}',
      },
      want: { ok: false, failure: "decode", status: 404 },
    },
    {
      name: "held wait answers 200 with a body",
      row: "events.wait",
      params: {},
      request: { method: "GET", path: "/api/events/wait", headers: {} },
      answer: { kind: "answer", status: 200, data: { kind: "jobs" } },
      want: { ok: true, status: 200, data: { kind: "jobs" } },
    },
    {
      name: "held wait answers 204 with none",
      row: "events.wait",
      params: {},
      request: { method: "GET", path: "/api/events/wait", headers: {} },
      answer: { kind: "answer", status: 204 },
      want: { ok: true, status: 204 },
    },
    {
      name: "held wait answering an undeclared 202 is decode",
      row: "events.wait",
      params: {},
      request: { method: "GET", path: "/api/events/wait", headers: {} },
      answer: {
        kind: "raw",
        status: 202,
        headers: {
          "Content-Type": "application/json",
          "Wire-Version": "0123456789abcdef",
          "X-Request-Id": "req-1",
        },
        body: '{"kind":"jobs"}',
      },
      want: { ok: false, failure: "decode", status: 202 },
    },
    {
      name: "read refused with a body that is no envelope is decode",
      row: "notes.read",
      params: { id: "n1" },
      request: { method: "GET", path: "/api/notes/n1", headers: {} },
      answer: {
        kind: "raw",
        status: 500,
        headers: {
          "Content-Type": "text/plain; charset=utf-8",
          "Wire-Version": "0123456789abcdef",
          "X-Request-Id": "req-1",
        },
        body: "oops",
      },
      want: { ok: false, failure: "decode", status: 500 },
    },
    {
      name: "register answering an undeclared 202 is decode",
      row: "users.register",
      params: {},
      body: { name: "ada" },
      key: "00000000-0000-4000-8000-000000000000",
      request: {
        method: "POST",
        path: "/api/users",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": '"00000000-0000-4000-8000-000000000000"',
          "Wire-Version": "0123456789abcdef",
        },
      },
      answer: {
        kind: "raw",
        status: 202,
        headers: {
          "Content-Type": "application/json",
          "Wire-Version": "0123456789abcdef",
          "X-Request-Id": "req-1",
        },
        body: '{"id":"u1","name":"ada"}',
      },
      want: { ok: false, failure: "decode", status: 202 },
    },
    {
      name: "read answering a note with no tag is decode",
      row: "notes.read",
      params: { id: "n1" },
      request: { method: "GET", path: "/api/notes/n1", headers: {} },
      answer: {
        kind: "raw",
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "Wire-Version": "0123456789abcdef",
          "X-Request-Id": "req-1",
        },
        body: '{"id":"n1","text":"first"}',
      },
      want: { ok: false, failure: "decode", status: 200 },
    },
  ] satisfies readonly TransportCase[],
};

export interface ServedCase {
  readonly fetch: typeof fetch;
  /** Each request the client sent, unread. */
  readonly sent: readonly Request[];
}

function fail(message: string): Promise<never> {
  const err = new Error(`transport fixture: ${message}`);
  (globalThis as { readonly reportError?: (e: unknown) => void }).reportError?.(err);
  return Promise.reject(err);
}

/**
 * A `fetch` that answers one case of `TRANSPORT_FIXTURE` and records what the client sent. A request the
 * case does not answer rejects and is reported through `reportError`, as the fake server does.
 */
export function serveTransportCase(c: TransportCase): ServedCase {
  const fixture = TRANSPORT_FIXTURE;
  const sent: Request[] = [];
  const ep = fixture.endpoints[c.row];
  if (ep === undefined) {
    throw new Error(`transport fixture: ${c.name} names no row ${c.row}`);
  }
  const fake = createFakeServer(fixture);
  const a = c.answer;
  if (a?.kind === "answer") {
    fake.answer(ep, a.data, { status: a.status, params: c.params });
  } else if (a?.kind === "refuse") {
    fake.refuse(ep, a.code, { params: c.params });
  }
  const served = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const req = new Request(input, init);
    sent.push(req.clone());
    if (a?.kind !== "raw") {
      return fake.fetch(req);
    }
    const path = new URL(req.url).pathname;
    if (req.method !== c.request?.method || path !== c.request.path) {
      return fail(
        `${c.name} answers ${String(c.request?.method)} ${String(c.request?.path)}, got ${req.method} ${path}`,
      );
    }
    return Promise.resolve(new Response(a.body, { status: a.status, headers: a.headers }));
  };
  return { fetch: served, sent };
}
