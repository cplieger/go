import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Frame } from "../stream.js";
import { retainListening } from "./retain.js";
import {
  E1,
  E2,
  advance,
  changed,
  harness,
  listening,
  position,
  sink,
} from "./test-helpers/feed-harness.js";
import { FEED_VERSION } from "./version.js";
import { parseListening, peekListening } from "./wire.js";

const W1 = "0123456789abcdef";
const W2 = "fedcba9876543210";

/** Folds the frames in order from nothing retained. */
const fold = (...frames: readonly Frame[]): Frame | null =>
  frames.reduce<Frame | null>(retainListening, null);

/** The retained frame read as a tab reads it: per source, its epoch and floors. */
function held(f: Frame | null): {
  wire: string | null;
  sources: Record<string, { epoch: string; floors: Record<string, number> }>;
} {
  expect(f?.type).toBe("listening");
  const p = parseListening(f?.data ?? "");
  if (p.kind !== "ok") {
    throw new Error(`retained frame does not parse: ${p.kind}`);
  }
  return {
    wire: p.listening.wire,
    sources: Object.fromEntries(
      p.listening.sources.map((s) => [
        s.source,
        { epoch: s.epoch, floors: Object.fromEntries(s.floors) },
      ]),
    ),
  };
}

const malformed = (type: string, data: string): Frame => ({ type, data, id: null });

function must(f: Frame | null): Frame {
  if (f === null) {
    throw new Error("nothing retained");
  }
  return f;
}

describe("retainListening", () => {
  it("retainListening folds a listening frame and later changed frames into the per-kind maxima", () => {
    const got = fold(
      listening([{ source: "store", epoch: E1, floors: { runs: 5, budgets: 3 } }], W1),
      changed("store", E1, 9, ["runs"]),
      changed("store", E1, 2, ["budgets"]),
      changed("store", E1, 7, ["2fa", "runs"]),
    );
    expect(held(got)).toEqual({
      wire: W1,
      sources: { store: { epoch: E1, floors: { "2fa": 7, budgets: 3, runs: 9 } } },
    });
  });

  it("a new epoch replaces only its source's entry", () => {
    const connect = listening(
      [
        { source: "store", epoch: E1, floors: { runs: 5, budgets: 3 } },
        { source: "live", epoch: E1, floors: { queue: 4 } },
      ],
      W1,
    );
    const got = fold(
      connect,
      listening([{ source: "store", epoch: E2, floors: { budgets: 2 } }]),
      listening([{ source: "live", epoch: E1, floors: { queue: 3 } }]),
    );
    expect(held(got).sources).toEqual({
      store: { epoch: E2, floors: { budgets: 2 } },
      live: { epoch: E1, floors: { queue: 4 } },
    });
    const raised = retainListening(
      got,
      listening([{ source: "live", epoch: E1, floors: { queue: 6 } }]),
    );
    expect(held(raised).sources["live"]).toEqual({ epoch: E1, floors: { queue: 6 } });
  });

  it("a changed frame for a source it holds nothing for, or on another epoch, leaves it unchanged", () => {
    const retained = fold(listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1));
    expect(retainListening(retained, changed("live", E1, 9, ["queue"]))).toBe(retained);
    expect(retainListening(retained, changed("store", E2, 9, ["runs"]))).toBe(retained);
    expect(retainListening(retained, changed("store", E1, 4, ["runs"]))).toBe(retained);
    expect(retainListening(null, changed("store", E1, 9, ["runs"]))).toBeNull();
  });

  it("every other frame leaves it unchanged", () => {
    const retained = fold(listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1));
    expect(retainListening(retained, { type: "frame", data: "not json", id: null })).toBe(retained);
    expect(retainListening(null, { type: "message", data: "{}", id: null })).toBeNull();
  });

  it("wire is the newest wire-bearing frame's, so a reopen frame never erases it", () => {
    const first = fold(
      listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1),
      listening([{ source: "store", epoch: E2, floors: { runs: 1 } }]),
    );
    expect(held(first).wire).toBe(W1);
    const next = retainListening(
      first,
      listening([{ source: "store", epoch: E2, floors: { runs: 2 } }], W2),
    );
    expect(held(next).wire).toBe(W2);
  });

  it("an other-format listening frame is replaced by its frozen members and a later own-format frame replaces it again", () => {
    const other = fold(
      listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1),
      listening([{ source: "store", epoch: E1, floors: { runs: 6 } }], W2, FEED_VERSION + 1),
    );
    expect(other?.data).toBe(`{"feed":${String(FEED_VERSION + 1)},"wire":"${W2}"}`);
    expect(peekListening(other?.data ?? "")).toEqual({
      ok: true,
      frozen: { feed: FEED_VERSION + 1, wire: W2 },
    });
    expect(retainListening(other, changed("store", E1, 9, ["runs"]))).toBe(other);
    expect(retainListening(other, malformed("changed", "{"))).toBe(other);
    expect(retainListening(other, malformed("listening", '{"sources":[]}'))).toBe(other);
    const unwired = retainListening(
      other,
      listening([{ source: "store", epoch: E2, floors: { runs: 1 } }], null, FEED_VERSION + 2),
    );
    expect(unwired?.data).toBe(`{"feed":${String(FEED_VERSION + 2)},"wire":"${W2}"}`);
    const own = retainListening(
      other,
      listening([{ source: "live", epoch: E2, floors: { queue: 1 } }]),
    );
    expect(held(own)).toEqual({
      wire: W2,
      sources: { live: { epoch: E2, floors: { queue: 1 } } },
    });
  });

  it("a malformed feed frame sets lost and never throws", () => {
    const retained = fold(listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1));
    for (const bad of [
      malformed("changed", "{"),
      malformed("changed", `{"source":"store","epoch":"${E1}","rev":0,"kinds":["runs"]}`),
      malformed("listening", `{"feed":${String(FEED_VERSION)},"sources":[{"source":"X"}]}`),
      malformed("listening", "[]"),
    ]) {
      const lost = retainListening(retained, bad);
      expect(lost?.data).toBe(
        `{"feed":${String(FEED_VERSION)},"wire":"${W1}","sources":[],"lost":true}`,
      );
      expect(parseListening(lost?.data ?? "")).toMatchObject({
        kind: "invalid",
        frozen: { feed: FEED_VERSION, wire: W1 },
      });
    }
    expect(retainListening(null, malformed("changed", "{"))?.data).toBe(
      `{"feed":${String(FEED_VERSION)},"sources":[],"lost":true}`,
    );
  });

  it("lost stays set for the rest of the connection, and only a newer wire is taken", () => {
    const lost = fold(
      listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1),
      malformed("changed", "{"),
    );
    const after = retainListening(
      lost,
      listening([{ source: "store", epoch: E2, floors: { runs: 1 } }], W2),
    );
    expect(after?.data).toBe(
      `{"feed":${String(FEED_VERSION)},"wire":"${W2}","sources":[],"lost":true}`,
    );
    expect(retainListening(after, changed("store", E2, 9, ["runs"]))).toBe(after);
  });

  it("a fold past 64 kinds sets lost", () => {
    const floors = Object.fromEntries(
      Array.from({ length: 64 }, (_, i) => [`k${String(i).padStart(2, "0")}`, 1]),
    );
    const full = fold(listening([{ source: "store", epoch: E1, floors }], W1));
    expect(Object.keys(held(full).sources["store"]?.floors ?? {})).toHaveLength(64);
    const over = retainListening(full, changed("store", E1, 2, ["zz"]));
    expect(parseListening(over?.data ?? "")).toMatchObject({ kind: "invalid" });
    expect(over?.data.endsWith(',"lost":true}')).toBe(true);
  });

  it("a fold past 64 KiB sets lost", () => {
    const sources = (from: number) =>
      Array.from({ length: 600 }, (_, i) => ({
        source: `s${String(from + i)}`,
        epoch: E1,
        floors: {},
      }));
    const half = fold(listening(sources(0), W1));
    expect(Object.keys(held(half).sources)).toHaveLength(600);
    const over = retainListening(half, listening(sources(600)));
    expect(over?.data).toBe(
      `{"feed":${String(FEED_VERSION)},"wire":"${W1}","sources":[],"lost":true}`,
    );
  });

  it("never throws on any frame", () => {
    const retained = fold(listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1));
    fc.assert(
      fc.property(
        fc.constantFrom("changed", "listening", "frame"),
        fc.oneof(fc.string(), fc.json()),
        fc.boolean(),
        (type, data, fromNothing) => {
          const got = retainListening(fromNothing ? null : retained, { type, data, id: null });
          expect(got === null || got.type === "listening").toBe(true);
        },
      ),
    );
  });
});

describe("retainListening into a tab's feed", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("a newer build's undeclared sources and kinds are folded without lost, and the tab drops them", async () => {
    const retained = fold(
      listening(
        [
          { source: "store", epoch: E1, floors: { runs: 9, newkind: 9 } },
          { source: "newsource", epoch: E1, floors: { other: 9 } },
        ],
        W2,
      ),
      changed("store", E1, 11, ["newkind", "runs"]),
      changed("newsource", E1, 12, ["other"]),
    );
    expect(held(retained).sources).toEqual({
      store: { epoch: E1, floors: { newkind: 11, runs: 11 } },
      newsource: { epoch: E1, floors: { other: 12 } },
    });
    const h = harness();
    const runs = sink();
    h.feed.watch("runs", runs, position(["store", E1, 10]));
    expect(() => h.feed.onFrame(must(retained))).not.toThrow();
    await advance(1_000);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 10, kinds: ["runs"] },
    ]);
    expect(h.wires).toEqual([W2]);
    expect(h.reconnect).not.toHaveBeenCalled();
  });

  it("a tab attaching to a lost retained frame is lost track and reconnects once", () => {
    const lost = fold(
      listening([{ source: "store", epoch: E1, floors: { runs: 5 } }], W1),
      malformed("changed", "{"),
    );
    const h = harness();
    h.feed.watch("runs", sink(), position(["store", E1, 10]));
    expect(() => h.feed.onFrame(must(lost))).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
    expect(h.wires).toEqual([W1]);
  });
});
