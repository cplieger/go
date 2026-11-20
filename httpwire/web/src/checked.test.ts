import { describe, expect, it } from "vitest";
import checkedFile from "../testdata/checked.json?raw";
import { CHECK_ANSWERS, type Checked, checkChecked, NOT_STORED } from "./checked.js";

// Go's httpwire test writes the file from the answers its binder serves or refuses, so a rule
// changed on one side fails both.
const fixture = JSON.parse(checkedFile) as readonly {
  readonly name: string;
  readonly body: unknown;
  readonly valid: boolean;
}[];

const same = { source: "sonarr", answer: "same", stored: false };
const position = 'store=4;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70"';
const updated = { source: "sonarr", answer: "updated", row: { name: "x" }, stored: true, position };
const failed = { source: "sonarr", answer: "error", code: "upstream_held", stored: false };
const upstream = { service: "sonarr", status: 503, message: "Service Unavailable" };

function one(entry: Record<string, unknown>): unknown {
  return { sources: [entry] };
}

// Shapes no Go value encodes to, each breaking one rule of the wire.
const broken: Record<string, unknown> = {
  "not an object": [],
  null: null,
  "no sources": {},
  "sources null": { sources: null },
  "sources an object": { sources: { 0: same } },
  "entry not an object": { sources: ["sonarr"] },
  "entry null": { sources: [null] },
  "source a number": one({ ...same, source: 7 }),
  "no source": one({ answer: "same", stored: false }),
  "answer an inherited name": one({ ...same, answer: "toString" }),
  "answer a number": one({ ...same, answer: 0 }),
  "no stored": one({ source: "sonarr", answer: "same" }),
  "stored as text": one({ ...updated, stored: "true" }),
  "stored as a number": one({ ...updated, stored: 1 }),
  "row null on updated": one({ ...updated, row: null }),
  "row null on same": one({ ...same, row: null }),
  "not_stored an inherited name": one({ ...updated, stored: false, not_stored: "constructor" }),
  "not_stored empty": one({ ...updated, stored: false, not_stored: "" }),
  "position a number": one({ ...updated, position: 4 }),
  "position null": one({ ...updated, position: null }),
  "position empty on a stored answer": one({ ...updated, position: "" }),
  "position empty on an answer not stored": one({ ...same, position: "" }),
  "position past 4096 bytes": one({ ...updated, position: "a".repeat(4097) }),
  "code a number": one({ ...failed, code: 502 }),
  "code empty": one({ ...failed, code: "" }),
  "code null": one({ ...failed, code: null }),
  "upstream not an object": one({ ...failed, upstream: "sonarr" }),
  "upstream null": one({ ...failed, upstream: null }),
  "upstream service outside the code grammar": one({
    ...failed,
    upstream: { ...upstream, service: "Sonarr 4" },
  }),
  "upstream with no message": one({ ...failed, upstream: { service: "sonarr", status: 503 } }),
  "upstream message past 512 bytes": one({
    ...failed,
    upstream: { ...upstream, message: "é".repeat(257) },
  }),
  "upstream status 600": one({ ...failed, upstream: { ...upstream, status: 600 } }),
  "upstream status 99": one({ ...failed, upstream: { ...upstream, status: 99 } }),
  "upstream status fractional": one({ ...failed, upstream: { ...upstream, status: 503.5 } }),
  "upstream on same": one({ ...same, upstream }),
  "the same source twice, with different answers": {
    sources: [same, { ...updated, source: "sonarr" }],
  },
};

// Shapes the wire allows that the Go fixture does not spell, each at a bound.
const kept: Record<string, Checked<unknown>> = {
  "upstream message of 512 bytes": one({
    ...failed,
    upstream: { ...upstream, message: "é".repeat(256) },
  }) as Checked<unknown>,
  "upstream status 100 and 599": {
    sources: [
      { ...failed, upstream: { ...upstream, status: 100 } },
      { ...failed, source: "radarr", upstream: { ...upstream, status: 599 } },
    ],
  } as Checked<unknown>,
  "a member the wire does not name": one({ ...same, seen_at: 1 }) as Checked<unknown>,
  "sources named alike in another case": {
    sources: [same, { ...same, source: "Sonarr" }],
  } as Checked<unknown>,
};

describe("CHECK_ANSWERS and NOT_STORED", () => {
  it("list the open check's answers and why one was not stored", () => {
    expect([...CHECK_ANSWERS]).toEqual(["same", "updated", "gone", "error"]);
    expect([...NOT_STORED]).toEqual(["superseded", "store_unwritable"]);
    expect(Object.isFrozen(CHECK_ANSWERS) && Object.isFrozen(NOT_STORED)).toBe(true);
  });
});

describe("checkChecked", () => {
  it("answers every answer the Go binder writes", () => {
    const valid = fixture.filter((f) => f.valid);
    const answered = new Set(
      valid.flatMap((f) => (f.body as Checked<unknown>).sources.map((s) => s.answer)),
    );
    expect([...answered].sort()).toEqual(["error", "gone", "same", "updated"]);
    for (const f of valid) {
      const body = f.body as Checked<unknown>;
      expect(checkChecked(body), f.name).toBe(body);
    }
  });

  it("refuses with a TypeError every answer the Go binder refuses to write", () => {
    const refused = fixture.filter((f) => !f.valid);
    expect(refused.length).toBeGreaterThan(20);
    for (const f of refused) {
      expect(() => checkChecked(f.body as Checked<unknown>), f.name).toThrow(TypeError);
    }
  });

  it("refuses with a TypeError a shape no Go answer encodes to", () => {
    for (const [name, a] of Object.entries(broken)) {
      expect(() => checkChecked(a as Checked<unknown>), name).toThrow(TypeError);
    }
  });

  it("answers a shape at the wire's bounds", () => {
    for (const [name, a] of Object.entries(kept)) {
      expect(checkChecked(a), name).toBe(a);
    }
  });
});
