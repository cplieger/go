import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  E1,
  advance,
  changed,
  harness,
  listening,
  position,
  sink,
} from "./test-helpers/feed-harness.js";
import { type ModelKind, ModelWorld } from "./test-helpers/model-server.js";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

const RUNS = { numRuns: 200 };

type Op =
  | {
      readonly op: "commit";
      readonly kind: "2fa" | "budgets" | "runs" | "settings";
      readonly key: string;
      readonly del: boolean;
    }
  | { readonly op: "bump" }
  | { readonly op: "open"; readonly kind: ModelKind }
  | { readonly op: "drop" }
  | { readonly op: "connect"; readonly resume: boolean }
  | { readonly op: "reopen"; readonly source: "store" | "settings" }
  | { readonly op: "restart" }
  | { readonly op: "trim"; readonly source: "store" | "settings" }
  | { readonly op: "replay"; readonly pick: number }
  | { readonly op: "refresh"; readonly pick: number }
  | { readonly op: "answer"; readonly pick: number }
  | { readonly op: "fail"; readonly pick: number; readonly how: "retry" | "refused" }
  | { readonly op: "tick"; readonly ms: number };

const kindArb = fc.constantFrom<ModelKind>("2fa", "budgets", "runs", "queue", "settings");
const sourceArb = fc.constantFrom<"store" | "settings">("store", "settings");

const opArb: fc.Arbitrary<Op> = fc.oneof(
  {
    weight: 6,
    arbitrary: fc.record({
      op: fc.constant("commit" as const),
      kind: fc.constantFrom<"2fa" | "budgets" | "runs" | "settings">(
        "2fa",
        "budgets",
        "runs",
        "settings",
      ),
      key: fc.constantFrom("a", "b", "c"),
      del: fc.nat(3).map((n) => n === 0),
    }),
  },
  { weight: 2, arbitrary: fc.constant({ op: "bump" as const }) },
  { weight: 1, arbitrary: fc.record({ op: fc.constant("open" as const), kind: kindArb }) },
  { weight: 1, arbitrary: fc.constant({ op: "drop" as const }) },
  {
    weight: 2,
    arbitrary: fc.record({ op: fc.constant("connect" as const), resume: fc.boolean() }),
  },
  { weight: 1, arbitrary: fc.record({ op: fc.constant("reopen" as const), source: sourceArb }) },
  { weight: 1, arbitrary: fc.constant({ op: "restart" as const }) },
  { weight: 1, arbitrary: fc.record({ op: fc.constant("trim" as const), source: sourceArb }) },
  { weight: 1, arbitrary: fc.record({ op: fc.constant("replay" as const), pick: fc.nat() }) },
  { weight: 1, arbitrary: fc.record({ op: fc.constant("refresh" as const), pick: fc.nat() }) },
  { weight: 6, arbitrary: fc.record({ op: fc.constant("answer" as const), pick: fc.nat() }) },
  {
    weight: 4,
    arbitrary: fc.record({
      op: fc.constant("fail" as const),
      pick: fc.nat(),
      how: fc.constantFrom<"retry" | "refused">("retry", "refused"),
    }),
  },
  {
    weight: 3,
    arbitrary: fc.record({
      op: fc.constant("tick" as const),
      ms: fc.integer({ min: 0, max: 700 }),
    }),
  },
);

const opsArb = fc.array(opArb, { minLength: 10, maxLength: 60 });

async function play(w: ModelWorld, op: Op): Promise<void> {
  switch (op.op) {
    case "commit":
      w.commit(op.kind, op.kind === "settings" ? "doc" : op.key, op.del && op.kind !== "settings");
      return;
    case "bump":
      w.commit("queue", "q", false);
      return;
    case "open":
      w.open(op.kind);
      return;
    case "drop":
      w.drop();
      return;
    case "connect":
      w.connect(op.resume);
      return;
    case "reopen":
      w.reopen(op.source);
      return;
    case "restart":
      w.restart();
      return;
    case "trim":
      w.trim(op.source);
      return;
    case "replay":
      w.replay(op.pick);
      return;
    case "refresh":
      w.refresh(op.pick);
      return;
    case "answer":
      await w.settle(op.pick);
      return;
    case "fail":
      await w.settle(op.pick, op.how);
      return;
    case "tick":
      await advance(op.ms);
      return;
  }
}

/**
 * Runs `ops` against a fresh model whose screens of every kind start their first reads before the
 * first connection, then reconnects a stream left down and lets the system quiesce.
 */
async function run(ops: readonly Op[], resumeAtEnd: boolean): Promise<ModelWorld> {
  const w = new ModelWorld();
  for (const kind of ["runs", "runs", "2fa", "budgets", "queue", "settings"] as const) {
    w.open(kind);
  }
  w.connect(false);
  for (const op of ops) {
    await play(w, op);
  }
  w.connect(resumeAtEnd);
  await w.quiesce();
  return w;
}

const sorted = (rows: ReadonlyMap<string, unknown>): [string, unknown][] =>
  [...rows].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));

describe("the feed against a model server", () => {
  it("at quiescence every sink equals a fresh full read, and no invariant was breached on the way", async () => {
    await fc.assert(
      fc.asyncProperty(opsArb, fc.boolean(), async (ops, resume) => {
        const w = await run(ops, resume);
        expect(w.violations).toEqual([]);
        expect(w.sinks.length).toBeGreaterThanOrEqual(6);
        for (const s of w.sinks) {
          expect(sorted(s.rows), s.kind).toEqual(sorted(w.expected(s.kind)));
        }
        expect(w.reconnect).not.toHaveBeenCalled();
      }),
      RUNS,
    );
  });

  it("a screen's older read answered during a catch-up loses no change", async () => {
    const ops: readonly Op[] = [
      { op: "answer", pick: 1352270679 },
      { op: "refresh", pick: 0 },
      { op: "commit", kind: "budgets", key: "a", del: true },
      { op: "tick", ms: 50 },
      { op: "answer", pick: 0 },
      { op: "answer", pick: 419838941 },
      { op: "commit", kind: "budgets", key: "b", del: true },
      { op: "answer", pick: 0 },
      { op: "connect", resume: false },
      { op: "tick", ms: 0 },
      { op: "commit", kind: "2fa", key: "a", del: true },
      { op: "tick", ms: 0 },
      { op: "commit", kind: "2fa", key: "a", del: true },
      { op: "replay", pick: 0 },
      { op: "bump" },
      { op: "tick", ms: 50 },
      { op: "open", kind: "2fa" },
    ];
    const w = await run(ops, false);
    expect(w.violations).toEqual([]);
    for (const s of w.sinks) {
      expect(sorted(s.rows), s.kind).toEqual(sorted(w.expected(s.kind)));
    }
    expect(w.reconnect).not.toHaveBeenCalled();
  });

  it("no request follows a quiet system without a new frame", async () => {
    await fc.assert(
      fc.asyncProperty(opsArb, fc.boolean(), async (ops, resume) => {
        const w = await run(ops, resume);
        const asked = w.requests;
        await advance(300_000);
        expect(w.pending).toEqual([]);
        expect(w.requests).toBe(asked);
        expect(w.violations).toEqual([]);
      }),
      RUNS,
    );
  });
});

describe("bursts", () => {
  it("a burst of 50 changed frames for one kind in one window gives one catch-up", async () => {
    const h = harness();
    const s = sink();
    h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 100 } }]));
    h.feed.watch("runs", s, position(["store", E1, 100]));
    for (let rev = 101; rev <= 150; rev++) {
      h.feed.onFrame(changed("store", E1, rev, ["runs"]));
    }
    await advance(50);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "r1" }] } });
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
    expect(s.log).toEqual(["upsert r1"]);
  });
});
