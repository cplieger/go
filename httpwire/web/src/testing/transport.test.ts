import { describe, expect, it } from "vitest";
import transportFile from "../../testdata/transport.json?raw";
import { decodeEnvelope } from "../envelope.js";
import { idempotencyKeyHeader, ifMatchHeader } from "../headers.js";
import {
  createFakeServer,
  serveTransportCase,
  TRANSPORT_FIXTURE,
  type TransportCase,
} from "./index.js";

const corpus = JSON.parse(transportFile) as {
  readonly wireVersion: string;
  readonly errorCodes: unknown;
  readonly rows: readonly {
    readonly name: string;
    readonly method: string;
    readonly path: string;
    readonly codes: readonly string[];
    readonly statuses: readonly number[];
  }[];
  readonly cases: readonly unknown[];
};

type Result =
  | { readonly ok: true; readonly status: number; readonly data: unknown }
  | {
      readonly ok: false;
      readonly failure: string;
      readonly status: number;
      readonly code?: string;
    };

// A client written from the transport rules (one failure set, the status-taking decode, the
// header formatters), so each case's want is checked against an answer the fake really gives.
async function call(c: TransportCase, fetchFn: typeof fetch): Promise<Result> {
  const ep = TRANSPORT_FIXTURE.endpoints[c.row];
  if (ep === undefined) {
    throw new Error(`no row ${c.row}`);
  }
  const headers: Record<string, string> = {};
  if (c.tag !== undefined) {
    const h = ifMatchHeader(c.tag);
    if (h === undefined) {
      return { ok: false, failure: "invalid", status: 0 };
    }
    headers["If-Match"] = h;
  }
  if (c.key !== undefined) {
    const h = idempotencyKeyHeader(c.key);
    if (h === undefined) {
      return { ok: false, failure: "invalid", status: 0 };
    }
    headers["Idempotency-Key"] = h;
  }
  if (ep.method !== "GET" && ep.method !== "HEAD") {
    headers["Wire-Version"] = TRANSPORT_FIXTURE.wireVersion;
  }
  if (c.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const path = ep.path.replace(/\{(\w+)\}/g, (_: string, k: string) =>
    encodeURIComponent(c.params[k] ?? ""),
  );
  const res = await fetchFn(`https://app.invalid${path}`, {
    method: ep.method,
    headers,
    ...(c.body === undefined ? {} : { body: JSON.stringify(c.body) }),
  });
  const text = await res.text();
  if (res.ok) {
    try {
      return {
        ok: true,
        status: res.status,
        data: ep.decode(text === "" ? undefined : JSON.parse(text), res.status),
      };
    } catch {
      return { ok: false, failure: "decode", status: res.status };
    }
  }
  let env;
  try {
    env = decodeEnvelope(JSON.parse(text));
  } catch {
    env = undefined;
  }
  if (env === undefined || !ep.codes.includes(env.code)) {
    return { ok: false, failure: "decode", status: res.status };
  }
  return { ok: false, failure: "http", status: res.status, code: env.code };
}

describe("TRANSPORT_FIXTURE", () => {
  it("holds the shared corpus the Go parse checks", () => {
    expect(TRANSPORT_FIXTURE.wireVersion).toBe(corpus.wireVersion);
    expect(TRANSPORT_FIXTURE.errorCodes).toEqual(corpus.errorCodes);
    expect(TRANSPORT_FIXTURE.cases).toEqual(corpus.cases);
    const rows = Object.values(TRANSPORT_FIXTURE.endpoints).map(
      ({ name, method, path, codes, statuses }) => ({ name, method, path, codes, statuses }),
    );
    expect(rows).toEqual(corpus.rows);
  });

  it("keys each endpoint by its row name", () => {
    for (const [name, ep] of Object.entries(TRANSPORT_FIXTURE.endpoints)) {
      expect(ep.name).toBe(name);
    }
  });

  it("covers each row, an IfMatch row, a Keyed row and both two-status rows", () => {
    const rows = new Set(TRANSPORT_FIXTURE.cases.map((c) => c.row));
    expect([...rows].sort()).toEqual(Object.keys(TRANSPORT_FIXTURE.endpoints).sort());
    const statuses = (row: string) =>
      new Set(
        TRANSPORT_FIXTURE.cases.flatMap((c) => (c.row === row && c.want.ok ? [c.want.status] : [])),
      );
    expect(statuses("events.wait")).toEqual(new Set([200, 204]));
    expect(statuses("users.register")).toEqual(new Set([200, 201]));
    expect(TRANSPORT_FIXTURE.cases.some((c) => c.tag !== undefined && c.want.ok)).toBe(true);
    expect(TRANSPORT_FIXTURE.cases.some((c) => c.key !== undefined && c.want.ok)).toBe(true);
    const names = TRANSPORT_FIXTURE.cases.map((c) => c.name);
    expect(new Set(names).size).toBe(names.length);
  });

  // A raw answer is one the decoder-checked fake would refuse to give, so it never stands in
  // for an answer the fake can check.
  it.each(TRANSPORT_FIXTURE.cases.filter((c) => c.answer?.kind === "raw"))(
    "$name answers what the fake cannot",
    (c) => {
      const a = c.answer;
      const ep = TRANSPORT_FIXTURE.endpoints[c.row];
      if (a?.kind !== "raw" || ep === undefined) {
        throw new Error("not a raw case");
      }
      if (a.status >= 200 && a.status < 300) {
        const fake = createFakeServer(TRANSPORT_FIXTURE);
        expect(() => {
          fake.answer(ep, JSON.parse(a.body), { status: a.status });
        }).toThrow();
        return;
      }
      let env;
      try {
        env = decodeEnvelope(JSON.parse(a.body));
      } catch {
        env = undefined;
      }
      expect(env === undefined || !ep.codes.includes(env.code)).toBe(true);
    },
  );

  it.each(TRANSPORT_FIXTURE.cases)("$name", async (c) => {
    const served = serveTransportCase(c);
    const got = await call(c, served.fetch);
    expect(got).toEqual(c.want);
    if (c.request === undefined) {
      expect(served.sent).toEqual([]);
      return;
    }
    expect(served.sent).toHaveLength(1);
    const req = served.sent[0];
    if (req === undefined) {
      return;
    }
    expect(req.method).toBe(c.request.method);
    expect(new URL(req.url).pathname).toBe(c.request.path);
    for (const name of ["Content-Type", "Wire-Version", "If-Match", "Idempotency-Key"]) {
      expect(req.headers.get(name) ?? undefined, name).toBe(c.request.headers[name]);
    }
    const body = await req.text();
    expect(body === "" ? undefined : JSON.parse(body)).toEqual(c.body);
  });
});

describe("serveTransportCase", () => {
  const byName = (name: string): TransportCase => {
    const c = TRANSPORT_FIXTURE.cases.find((x) => x.name === name);
    if (c === undefined) {
      throw new Error(name);
    }
    return c;
  };
  const reported: unknown[] = [];
  const listen = (e: ErrorEvent) => {
    e.preventDefault();
    reported.push(e.error);
  };

  it("rejects and reports a request on a case that sends none", async () => {
    reported.length = 0;
    globalThis.addEventListener("error", listen);
    try {
      const served = serveTransportCase(byName("save with a bare revision sends nothing"));
      await expect(
        served.fetch("https://app.invalid/api/notes/n1", { method: "PUT" }),
      ).rejects.toThrow();
      expect(reported).toHaveLength(1);
      expect(served.sent).toHaveLength(1);
    } finally {
      globalThis.removeEventListener("error", listen);
    }
  });

  it("rejects and reports a raw case's request to another path", async () => {
    reported.length = 0;
    globalThis.addEventListener("error", listen);
    try {
      const served = serveTransportCase(byName("held wait answering an undeclared 202 is decode"));
      await expect(served.fetch("https://app.invalid/api/events/other")).rejects.toThrow();
      expect(reported).toHaveLength(1);
    } finally {
      globalThis.removeEventListener("error", listen);
    }
  });

  it("answers a raw case's status, headers and body as written", async () => {
    const served = serveTransportCase(
      byName("read refused with a body that is no envelope is decode"),
    );
    const res = await served.fetch("https://app.invalid/api/notes/n1");
    expect(res.status).toBe(500);
    expect(res.headers.get("Wire-Version")).toBe(TRANSPORT_FIXTURE.wireVersion);
    expect(res.headers.get("Content-Type")).toBe("text/plain; charset=utf-8");
    expect(await res.text()).toBe("oops");
  });

  it("answers a refusal with the catalog's envelope", async () => {
    const served = serveTransportCase(byName("save over a newer tag is stale"));
    const res = await served.fetch("https://app.invalid/api/notes/n1", { method: "PUT" });
    const env = decodeEnvelope(await res.json());
    expect(res.status).toBe(412);
    expect(env?.code).toBe("stale_version");
    expect(env?.remediation).toBe(TRANSPORT_FIXTURE.errorCodes["stale_version"]?.remediation);
  });

  it("answers only the case's path parameters", async () => {
    reported.length = 0;
    globalThis.addEventListener("error", listen);
    try {
      const read = serveTransportCase(byName("read answers the tag a save sends"));
      await expect(read.fetch("https://app.invalid/api/notes/n2")).rejects.toThrow();
      const save = serveTransportCase(byName("save over a newer tag is stale"));
      const other = save.fetch("https://app.invalid/api/notes/n2", { method: "PUT" });
      await expect(other).rejects.toThrow();
      expect(reported).toHaveLength(2);
    } finally {
      globalThis.removeEventListener("error", listen);
    }
  });
});
