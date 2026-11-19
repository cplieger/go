import { afterEach, describe, expect, it } from "vitest";
import { decodeEnvelope } from "../envelope.js";
import { createFakeServer, type GeneratedEndpoint } from "./index.js";

const WIRE = "0123456789abcdef";
const EPOCH = "0123456789abcdef0123456789abcdef";

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function row<T>(
  name: string,
  method: string,
  path: string,
  decode: (v: unknown, status: number) => T,
  statuses: readonly number[],
  codes: readonly string[] = [],
): GeneratedEndpoint<T> {
  return { name, method, path, decode, statuses, codes };
}

interface Job {
  readonly id: string;
  readonly name: string;
}
function decodeJob(v: unknown): Job {
  if (!isRecord(v) || typeof v["id"] !== "string" || typeof v["name"] !== "string") {
    throw new TypeError("not a job");
  }
  return { id: v["id"], name: v["name"] };
}

const jobsGet = row("jobs.get", "GET", "/api/jobs/{id}", decodeJob, [200], ["job_missing"]);
const jobsNew = row("jobs.new", "GET", "/api/jobs/new", decodeJob, [200]);
const jobsSave = row("jobs.save", "POST", "/api/jobs", decodeJob, [200], ["stale_version"]);
const filesGet = row("files.get", "GET", "/api/files/{rest...}", (v) => v, [200]);
const home = row("home.read", "GET", "/api/{$}", (v) => v, [200]);
const wait = row(
  "events.wait",
  "GET",
  "/api/wait",
  (v, status): string | undefined => {
    if (status === 204) {
      if (v !== undefined) {
        throw new TypeError("a 204 has no body");
      }
      return undefined;
    }
    if (typeof v !== "string") {
      throw new TypeError("not an event");
    }
    return v;
  },
  [200, 204],
);
const register = row("users.register", "POST", "/api/users", decodeJob, [201, 200], ["taken"]);
const streamRow = row("events.stream", "GET", "/api/events", (v) => v, [200], ["sse_unavailable"]);

const endpoints = {
  "jobs.get": jobsGet,
  "jobs.new": jobsNew,
  "jobs.save": jobsSave,
  "files.get": filesGet,
  "home.read": home,
  "events.wait": wait,
  "users.register": register,
  "events.stream": streamRow,
};
const errorCodes = {
  job_missing: { status: 404, remediation: "List the jobs and pick one that exists." },
  sse_unavailable: { status: 503, remediation: "Reconnect later." },
};

function fake() {
  return createFakeServer({ endpoints, wireVersion: WIRE, errorCodes });
}

// Each failing request reports an error event; a test that expects one listens for it.
const reported: unknown[] = [];
const onError = (e: ErrorEvent): void => {
  reported.push(e.error);
  e.preventDefault();
};
function expectReports(): void {
  window.addEventListener("error", onError);
}
afterEach(() => {
  window.removeEventListener("error", onError);
  reported.length = 0;
});

async function json(r: Response): Promise<unknown> {
  return JSON.parse(await r.text()) as unknown;
}

describe("answer", () => {
  it("answers decoder-checked data with the wire headers", async () => {
    const f = fake();
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    const r = await f.fetch("https://x.invalid/api/jobs/a");
    expect(r.status).toBe(200);
    expect(r.headers.get("Content-Type")).toBe("application/json");
    expect(r.headers.get("Wire-Version")).toBe(WIRE);
    expect(r.headers.get("X-Request-Id")).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(await json(r)).toEqual({ id: "a", name: "Alpha" });
  });

  it("gives each answer its own request id", async () => {
    const f = fake();
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    const a = await f.fetch("https://x.invalid/api/jobs/a");
    const b = await f.fetch("https://x.invalid/api/jobs/a");
    expect(a.headers.get("X-Request-Id")).not.toBe(b.headers.get("X-Request-Id"));
  });

  it("refuses data the row's decoder refuses, naming the row", () => {
    const f = fake();
    expect(() => {
      f.answer(jobsGet, { id: "a" } as unknown as Job);
    }).toThrow(/jobs\.get/);
  });

  it("refuses data whose JSON form the decoder refuses", () => {
    const f = fake();
    const lossy = { id: "a", name: "Alpha", toJSON: () => ({ id: "a" }) };
    expect(decodeJob(lossy)).toEqual({ id: "a", name: "Alpha" });
    expect(() => {
      f.answer(jobsGet, lossy);
    }).toThrow(/jobs\.get/);
  });

  it("refuses a status the row does not declare", () => {
    const f = fake();
    expect(() => {
      f.answer(jobsGet, { id: "a", name: "Alpha" }, { status: 201 });
    }).toThrow(/jobs\.get declares no status 201/);
    expect(() => {
      f.answer(wait, "x", { status: 202 });
    }).toThrow(/events\.wait/);
  });

  it("answers a held row's 200 with a body and its 204 with none", async () => {
    const f = fake();
    f.answer(wait, "ping");
    const ok = await f.fetch("https://x.invalid/api/wait");
    expect(ok.status).toBe(200);
    expect(await json(ok)).toBe("ping");
    f.answer(wait, undefined, { status: 204 });
    const none = await f.fetch("https://x.invalid/api/wait");
    expect(none.status).toBe(204);
    expect(none.body).toBeNull();
    expect(none.headers.get("Content-Type")).toBeNull();
    expect(none.headers.get("Wire-Version")).toBe(WIRE);
  });

  it("refuses a body on a bodiless status", () => {
    const f = fake();
    expect(() => {
      f.answer(wait, "ping", { status: 204 });
    }).toThrow(/events\.wait answers 204 with no body/);
  });

  it("answers the primary status unless told another declared one", async () => {
    const f = fake();
    f.answer(register, { id: "u", name: "Ann" });
    expect((await f.fetch("https://x.invalid/api/users", { method: "POST" })).status).toBe(201);
    f.answer(register, { id: "u", name: "Ann" }, { status: 200 });
    expect((await f.fetch("https://x.invalid/api/users", { method: "POST" })).status).toBe(200);
  });

  it("sends the headers it is given, a Content-Type included", async () => {
    const f = fake();
    f.answer(
      jobsGet,
      { id: "a", name: "Alpha" },
      { headers: { ETag: '"x"', "Content-Type": "application/json; charset=utf-8" } },
    );
    const r = await f.fetch("https://x.invalid/api/jobs/a");
    expect(r.headers.get("ETag")).toBe('"x"');
    expect(r.headers.get("Content-Type")).toBe("application/json; charset=utf-8");
  });

  it("answers by path parameters, the newest registration first", async () => {
    expectReports();
    const f = fake();
    f.answer(jobsGet, { id: "a", name: "Alpha" }, { params: { id: "a" } });
    f.answer(jobsGet, { id: "b", name: "Beta" }, { params: { id: "b" } });
    expect(await json(await f.fetch("https://x.invalid/api/jobs/b"))).toEqual({
      id: "b",
      name: "Beta",
    });
    expect(await json(await f.fetch("https://x.invalid/api/jobs/a"))).toEqual({
      id: "a",
      name: "Alpha",
    });
    await expect(f.fetch("https://x.invalid/api/jobs/c")).rejects.toThrow(
      /jobs\.get has no answer/,
    );
    f.answer(jobsGet, { id: "a", name: "Again" }, { params: { id: "a" } });
    expect(await json(await f.fetch("https://x.invalid/api/jobs/a"))).toEqual({
      id: "a",
      name: "Again",
    });
  });

  it("refuses an endpoint that is not the fake's", () => {
    const f = fake();
    expect(() => {
      f.answer({ ...jobsGet }, { id: "a", name: "Alpha" });
    }).toThrow(/jobs\.get is not one of the fake's endpoints/);
  });
});

describe("refuse", () => {
  it("writes an envelope decodeEnvelope accepts, with the catalog's status and words", async () => {
    const f = fake();
    f.refuse(jobsGet, "job_missing", {
      reason: "deleted",
      fields: [{ path: "id", code: "unknown", message: "No such job." }],
    });
    const r = await f.fetch("https://x.invalid/api/jobs/a");
    expect(r.status).toBe(404);
    expect(r.headers.get("Content-Type")).toBe("application/json");
    expect(r.headers.get("Wire-Version")).toBe(WIRE);
    const env = decodeEnvelope(await json(r));
    expect(env).toEqual({
      code: "job_missing",
      message: "job_missing",
      remediation: "List the jobs and pick one that exists.",
      request_id: r.headers.get("X-Request-Id"),
      reason: "deleted",
      fields: [{ path: "id", code: "unknown", message: "No such job." }],
    });
  });

  it("refuses a code outside the row's union, naming the row", () => {
    const f = fake();
    expect(() => {
      f.refuse(jobsGet, "taken");
    }).toThrow(/taken is not in jobs\.get's union/);
  });

  it("needs a status for a code the catalog lacks, and holds the catalog's", async () => {
    const f = fake();
    expect(() => {
      f.refuse(jobsSave, "stale_version");
    }).toThrow(/stale_version needs a status/);
    expect(() => {
      f.refuse(jobsSave, "stale_version", { status: 200 });
    }).toThrow(/stale_version needs a status/);
    expect(() => {
      f.refuse(jobsGet, "job_missing", { status: 410 });
    }).toThrow(/catalog answers job_missing as 404/);
    f.refuse(jobsSave, "stale_version", { status: 412 });
    const r = await f.fetch("https://x.invalid/api/jobs", { method: "POST" });
    expect(r.status).toBe(412);
    expect(decodeEnvelope(await json(r))?.remediation).toBe("");
  });

  it("refuses a reason or field list the envelope cannot carry", () => {
    const f = fake();
    expect(() => {
      f.refuse(jobsGet, "job_missing", { reason: "Not A Code" });
    }).toThrow(/not a valid envelope/);
    const fields = Array.from({ length: 101 }, () => ({ path: "", code: "x", message: "" }));
    expect(() => {
      f.refuse(jobsGet, "job_missing", { fields });
    }).toThrow(/not a valid envelope/);
  });

  it("refuses by path parameters beside an answer", async () => {
    const f = fake();
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    f.refuse(jobsGet, "job_missing", { params: { id: "gone" } });
    expect((await f.fetch("https://x.invalid/api/jobs/gone")).status).toBe(404);
    expect((await f.fetch("https://x.invalid/api/jobs/a")).status).toBe(200);
  });
});

describe("routing", () => {
  it("fails a request to a row with no answer, naming the row", async () => {
    expectReports();
    const f = fake();
    await expect(f.fetch("https://x.invalid/api/jobs/a")).rejects.toThrow(
      "fake server: jobs.get has no answer for GET /api/jobs/a",
    );
    expect(reported).toHaveLength(1);
    expect(String(reported[0])).toMatch(/jobs\.get/);
  });

  it("fails a request no row routes", async () => {
    expectReports();
    const f = fake();
    await expect(f.fetch("https://x.invalid/api/nothing")).rejects.toThrow(
      "fake server: GET /api/nothing matches no row",
    );
    await expect(f.fetch("https://x.invalid/api/jobs/a", { method: "DELETE" })).rejects.toThrow(
      /DELETE \/api\/jobs\/a matches no row/,
    );
    await expect(f.fetch("https://x.invalid/api/jobs/")).rejects.toThrow(/matches no row/);
    await expect(f.fetch("https://x.invalid/api/jobs/a/b")).rejects.toThrow(/matches no row/);
    await expect(f.fetch("https://x.invalid/api")).rejects.toThrow(/matches no row/);
    expect(reported).toHaveLength(5);
    expect(f.calls).toEqual([]);
  });

  it("routes a literal row before a parameter row", async () => {
    const f = fake();
    f.answer(jobsNew, { id: "", name: "" });
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    await f.fetch("https://x.invalid/api/jobs/new");
    await f.fetch("https://x.invalid/api/jobs/news");
    expect(f.calls.map((c) => c.name)).toEqual(["jobs.new", "jobs.get"]);
  });

  it("decodes a segment into its parameter", async () => {
    const f = fake();
    f.answer(jobsGet, { id: "a b", name: "Spaced" }, { params: { id: "a b" } });
    expect((await f.fetch("https://x.invalid/api/jobs/a%20b")).status).toBe(200);
  });

  it("routes the rest of the path and the trailing slash", async () => {
    const f = fake();
    f.answer(filesGet, "deep", { params: { rest: "a/b/c.txt" } });
    f.answer(filesGet, "empty", { params: { rest: "" } });
    f.answer(home, "home");
    expect(await json(await f.fetch("https://x.invalid/api/files/a/b/c.txt"))).toBe("deep");
    expect(await json(await f.fetch("https://x.invalid/api/files/"))).toBe("empty");
    expect(await json(await f.fetch("https://x.invalid/api/"))).toBe("home");
  });

  it("answers HEAD on a read row with no body", async () => {
    const f = fake();
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    const r = await f.fetch("https://x.invalid/api/jobs/a", { method: "HEAD" });
    expect(r.status).toBe(200);
    expect(r.body).toBeNull();
    expect(r.headers.get("Wire-Version")).toBe(WIRE);
  });
});

describe("calls", () => {
  it("records each routed request with its body", async () => {
    const f = fake();
    f.answer(jobsSave, { id: "a", name: "Alpha" });
    f.answer(jobsGet, { id: "a", name: "Alpha" });
    await f.fetch("https://x.invalid/api/jobs?x=1", {
      method: "POST",
      body: JSON.stringify({ name: "Alpha" }),
    });
    await f.fetch(new Request("https://x.invalid/api/jobs", { method: "POST", body: "plain" }));
    await f.fetch(new URL("https://x.invalid/api/jobs/a"));
    expect(f.calls).toEqual([
      { name: "jobs.save", url: "https://x.invalid/api/jobs?x=1", body: { name: "Alpha" } },
      { name: "jobs.save", url: "https://x.invalid/api/jobs", body: "plain" },
      { name: "jobs.get", url: "https://x.invalid/api/jobs/a" },
    ]);
    expect(Object.hasOwn(f.calls[2] ?? {}, "body")).toBe(false);
  });
});

async function readUntil(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  needle: string,
  seen = { text: "" },
): Promise<string> {
  const utf8 = new TextDecoder();
  while (!seen.text.includes(needle)) {
    const { value, done } = await reader.read();
    if (done) {
      throw new Error(`stream ended before ${needle}`);
    }
    seen.text += utf8.decode(value);
  }
  return seen.text;
}

function hello(text: string): Record<string, unknown> {
  const m = /event: sse:hello\ndata: (.*)\n\n/.exec(text);
  return JSON.parse(m?.[1] ?? "null") as Record<string, unknown>;
}

const changed = { source: "store", epoch: EPOCH, rev: 3, kinds: ["budgets", "runs"] };
const listening = {
  feed: 1,
  wire: WIRE,
  sources: [{ source: "store", epoch: EPOCH, floors: { runs: 2 } }],
};

describe("stream", () => {
  it("needs the stream row", () => {
    const { "events.stream": _, ...rest } = endpoints;
    const f = createFakeServer({ endpoints: rest, wireVersion: WIRE });
    expect(() => f.stream()).toThrow(/no events\.stream row/);
  });

  it("serves a hello, then the frames pushed, with ids on all but listening", async () => {
    const f = fake();
    const s = f.stream();
    expect(f.stream()).toBe(s);
    const r = await f.fetch("https://x.invalid/api/events");
    expect(r.status).toBe(200);
    expect(r.headers.get("Content-Type")).toBe("text/event-stream");
    expect(r.headers.get("Wire-Version")).toBe(WIRE);
    expect(s.open).toBe(1);
    const reader = (r.body as ReadableStream<Uint8Array>).getReader();
    const seen = { text: "" };
    const h = hello(await readUntil(reader, "sse:hello", seen));
    expect(h).toMatchObject({ wire: 1, resumed: false, verdict: "fresh", head: "0" });
    expect(h["epoch"]).toMatch(/^[0-9a-f]{16}$/);
    s.push("listening", listening);
    s.push("changed", changed);
    s.push("chart", { points: [1, 2] });
    const text = await readUntil(reader, "chart", seen);
    expect(text).toContain(`event: listening\ndata: ${JSON.stringify(listening)}\n\n`);
    expect(text).not.toMatch(/id: [^\n]*\nevent: listening/);
    expect(text).toContain(
      `id: ${String(h["epoch"])}:1\nevent: changed\ndata: ${JSON.stringify(changed)}\n\n`,
    );
    expect(text).toContain(`id: ${String(h["epoch"])}:2\nevent: chart\n`);
    s.close();
    expect((await reader.read()).done).toBe(true);
    expect(s.open).toBe(0);
  });

  it("answers each cursor with the verdict a hub holding no frames gives", async () => {
    const f = fake();
    const s = f.stream();
    const first = await f.fetch("https://x.invalid/api/events");
    const reader = (first.body as ReadableStream<Uint8Array>).getReader();
    const epoch = String(hello(await readUntil(reader, "sse:hello"))["epoch"]);
    s.push("changed", changed);
    s.push("changed", { ...changed, rev: 4 });
    const verdict = async (cursor: string): Promise<unknown> => {
      const r = await f.fetch("https://x.invalid/api/events", {
        headers: { "Last-Event-ID": cursor },
      });
      const h = hello(
        await readUntil((r.body as ReadableStream<Uint8Array>).getReader(), "sse:hello"),
      );
      return [h["verdict"], h["resumed"], h["head"]];
    };
    expect(await verdict(`${epoch}:2`)).toEqual(["resumed", true, "2"]);
    expect(await verdict(`${epoch}:1`)).toEqual(["gap_floor", false, "2"]);
    expect(await verdict(`${epoch}:3`)).toEqual(["gap_ahead", false, "2"]);
    expect(await verdict(`fedcba9876543210:2`)).toEqual(["epoch_changed", false, "2"]);
    expect(await verdict(`${epoch}:02`)).toEqual(["cursor_invalid", false, "2"]);
    expect(await verdict("junk")).toEqual(["cursor_invalid", false, "2"]);
    s.close();
  });

  it("drops a connection whose request is aborted", async () => {
    const f = fake();
    const s = f.stream();
    const ctl = new AbortController();
    await f.fetch("https://x.invalid/api/events", { signal: ctl.signal });
    expect(s.open).toBe(1);
    ctl.abort();
    expect(s.open).toBe(0);
  });

  it("drops a connection whose reader cancels", async () => {
    const f = fake();
    const s = f.stream();
    const r = await f.fetch("https://x.invalid/api/events");
    await (r.body as ReadableStream<Uint8Array>).cancel();
    expect(s.open).toBe(0);
  });

  it("lets a registered refusal answer the stream row first", async () => {
    const f = fake();
    f.stream();
    f.refuse(streamRow, "sse_unavailable");
    const r = await f.fetch("https://x.invalid/api/events");
    expect(r.status).toBe(503);
    expect(decodeEnvelope(await json(r))?.code).toBe("sse_unavailable");
  });

  it("refuses a push with no connection open", () => {
    const s = fake().stream();
    expect(() => {
      s.push("changed", changed);
    }).toThrow(/no connection open/);
  });

  const kinds65 = Array.from({ length: 65 }, (_, i) => `k${String(i).padStart(2, "0")}`);
  const floors65 = Object.fromEntries(kinds65.map((k) => [k, 1]));
  const brokenFrames: readonly (readonly [string, string, unknown])[] = [
    ["sse:hello", "the hub's own name", {}],
    ["sse:keepalive", "the hub's own name", {}],
    ["", "an empty name", {}],
    ["a\nb", "a name with a newline", {}],
    ["chart", "data that is not JSON", undefined],
    ["changed", "not an object", [changed]],
    ["changed", "an extra member", { ...changed, rows: [] }],
    ["changed", "a missing source", { epoch: EPOCH, rev: 3, kinds: ["runs"] }],
    ["changed", "a source starting with a digit", { ...changed, source: "1store" }],
    ["changed", "a 33-byte source", { ...changed, source: `s${"a".repeat(32)}` }],
    ["changed", "an uppercase epoch", { ...changed, epoch: EPOCH.toUpperCase() }],
    ["changed", "a short epoch", { ...changed, epoch: EPOCH.slice(1) }],
    ["changed", "rev 0", { ...changed, rev: 0 }],
    ["changed", "rev past the bound", { ...changed, rev: 1_000_000_000_000_000 }],
    ["changed", "a fractional rev", { ...changed, rev: 1.5 }],
    ["changed", "rev as text", { ...changed, rev: "3" }],
    ["changed", "no kinds", { ...changed, kinds: [] }],
    ["changed", "65 kinds", { ...changed, kinds: kinds65 }],
    ["changed", "unsorted kinds", { ...changed, kinds: ["runs", "budgets"] }],
    ["changed", "a repeated kind", { ...changed, kinds: ["runs", "runs"] }],
    ["changed", "a kind outside the grammar", { ...changed, kinds: ["Runs"] }],
    ["changed", "a 33-byte kind", { ...changed, kinds: ["a".repeat(33)] }],
    ["changed", "a kind that is not text", { ...changed, kinds: [1] }],
    [
      "listening",
      "over 64 KiB",
      {
        feed: 1,
        sources: Array.from({ length: 1200 }, (_, n) => ({
          source: `s${String(n)}`,
          epoch: EPOCH,
          floors: {},
        })),
      },
    ],
    ["listening", "another feed format", { ...listening, feed: 2 }],
    ["listening", "no feed", { sources: [] }],
    ["listening", "a bad wire", { ...listening, wire: "ABCDEF0123456789" }],
    ["listening", "an extra member", { ...listening, rows: [] }],
    ["listening", "sources not a list", { feed: 1, sources: {} }],
    ["listening", "a source not an object", { feed: 1, sources: ["store"] }],
    [
      "listening",
      "a repeated source",
      { feed: 1, sources: [listening.sources[0], listening.sources[0]] },
    ],
    [
      "listening",
      "a source with an extra member",
      { feed: 1, sources: [{ source: "store", epoch: EPOCH, floors: {}, rev: 1 }] },
    ],
    [
      "listening",
      "a bad epoch",
      { feed: 1, sources: [{ source: "store", epoch: "x", floors: {} }] },
    ],
    [
      "listening",
      "floors not an object",
      { feed: 1, sources: [{ source: "store", epoch: EPOCH, floors: [] }] },
    ],
    [
      "listening",
      "a floor of 0",
      { feed: 1, sources: [{ source: "store", epoch: EPOCH, floors: { runs: 0 } }] },
    ],
    [
      "listening",
      "a floor kind outside the grammar",
      { feed: 1, sources: [{ source: "store", epoch: EPOCH, floors: { "a-b": 1 } }] },
    ],
    [
      "listening",
      "65 kinds over two sources",
      {
        feed: 1,
        sources: [
          {
            source: "a",
            epoch: EPOCH,
            floors: Object.fromEntries(kinds65.slice(0, 33).map((k) => [k, 1])),
          },
          {
            source: "b",
            epoch: EPOCH,
            floors: Object.fromEntries(kinds65.slice(33).map((k) => [k, 1])),
          },
        ],
      },
    ],
    [
      "listening",
      "65 kinds in one source",
      { feed: 1, sources: [{ source: "a", epoch: EPOCH, floors: floors65 }] },
    ],
  ];

  it.each(brokenFrames)("refuses a %s frame with %s", async (event, _why, data) => {
    const f = fake();
    const s = f.stream();
    await f.fetch("https://x.invalid/api/events");
    expect(() => {
      s.push(event, data);
    }).toThrow(TypeError);
    s.close();
  });

  const goodFrames: readonly (readonly [string, string, unknown])[] = [
    [
      "changed",
      "the largest rev and 64 kinds",
      { ...changed, rev: 999_999_999_999_999, kinds: kinds65.slice(1) },
    ],
    ["listening", "no wire, as an open hook writes it", { feed: 1, sources: listening.sources }],
    ["listening", "no sources", { feed: 1, wire: WIRE, sources: [] }],
    [
      "listening",
      "64 kinds over two sources",
      {
        feed: 1,
        sources: [
          {
            source: "a",
            epoch: EPOCH,
            floors: Object.fromEntries(kinds65.slice(0, 32).map((k) => [k, 1])),
          },
          {
            source: "b",
            epoch: EPOCH,
            floors: Object.fromEntries(kinds65.slice(33).map((k) => [k, 999_999_999_999_999])),
          },
        ],
      },
    ],
    ["chart", "any JSON for an app event", [1, "two", null]],
  ];

  it.each(goodFrames)("sends a %s frame with %s", async (event, _why, data) => {
    const f = fake();
    const s = f.stream();
    const r = await f.fetch("https://x.invalid/api/events");
    s.push(event, data);
    const text = await readUntil(
      (r.body as ReadableStream<Uint8Array>).getReader(),
      `event: ${event}`,
    );
    expect(text).toContain(`data: ${JSON.stringify(data)}\n\n`);
    s.close();
  });
});
