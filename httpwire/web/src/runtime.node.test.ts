import { describe, expect, it } from "vitest";
import {
  decodeEnvelope,
  idempotencyKeyHeader,
  ifMatchHeader,
  newIdempotencyKey,
  pages,
  parseInstant,
} from "./index.js";
import { ENVELOPE_FIXTURES, serveTransportCase, TRANSPORT_FIXTURE } from "./testing/index.js";

// The README names Node.js as a supported runtime: both entries run here with no DOM.
describe("both entries in Node.js", () => {
  it("decodes every envelope fixture as marked", () => {
    for (const f of ENVELOPE_FIXTURES) {
      let v: unknown;
      try {
        v = JSON.parse(f.body);
      } catch {
        v = undefined;
      }
      expect(decodeEnvelope(v) !== undefined, f.name).toBe(f.valid);
    }
  });

  it("reads instants and writes the request headers", () => {
    expect(parseInstant("2026-11-18T10:00:00.123456789+01:00")).toBe(
      Date.UTC(2026, 10, 18, 9, 0, 0, 123),
    );
    const key = newIdempotencyKey();
    expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(idempotencyKeyHeader(key)).toBe(`"${key}"`);
    expect(ifMatchHeader("0123456789abcdef.18446744073709551615")).toBe(
      '"0123456789abcdef.18446744073709551615"',
    );
  });

  it("reads every page of a list", async () => {
    const seen: number[] = [];
    const all = pages<number, { readonly ok: false }>(
      (cursor) =>
        Promise.resolve({
          ok: true,
          data:
            cursor === undefined
              ? { items: [1], offset: 0, total: 2, next_cursor: "n" }
              : { items: [2], offset: 1, total: 2, prev_cursor: "p" },
        }),
      { signal: new AbortController().signal },
    );
    for await (const p of all) {
      seen.push(...p.items);
    }
    expect(seen).toEqual([1, 2]);
  });

  it("serves every transport case a client sends", async () => {
    const sent = TRANSPORT_FIXTURE.cases.filter((c) => c.request !== undefined);
    expect(sent.length).toBeGreaterThan(0);
    for (const c of sent) {
      const req = c.request as NonNullable<typeof c.request>;
      const s = serveTransportCase(c);
      const res = await s.fetch(new URL(req.path, "http://api.test").href, {
        method: req.method,
        headers: req.headers,
        ...(c.body === undefined ? {} : { body: JSON.stringify(c.body) }),
      });
      await res.arrayBuffer();
      expect(res.status, c.name).toBe(c.want.status);
      expect(s.sent, c.name).toHaveLength(1);
    }
  });
});
