import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { reduce } from "../reducer.js";
import { DEFAULT_TIMING } from "../timing.js";
import {
  E1,
  E2,
  advance,
  bumper,
  changed,
  failingSink,
  harness,
  listening,
  position,
  settle,
  sink,
} from "./test-helpers/feed-harness.js";
import type { Harness } from "./test-helpers/feed-harness.js";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => {
  vi.useRealTimers();
});

const COALESCE = 50;
const live = (floors: Readonly<Record<string, number>>, epoch = E1) =>
  listening([{ source: "live", epoch, floors }]);
const store = (floors: Readonly<Record<string, number>>, epoch = E1) =>
  listening([{ source: "store", epoch, floors }]);

// setTimeout truncates its delay to an integer (WebIDL `long`), and the fake clock does the same.
const fired = (ms: readonly number[]): number[] => ms.map(Math.trunc);

/** A queue subscription staled at 0 by an other-epoch seed, then a Bump every 10 ms until `to`. */
async function churn(h: Harness, s: ReturnType<typeof failingSink>, to: number): Promise<void> {
  h.feed.onFrame(live({ queue: 10 }));
  h.feed.watch("queue", s, position(["live", E2, 10]));
  await bumper(h.feed, "queue", "live", E1)(to);
}

/** The stream runtime's reconnect delay after `n` failed attempts, drawn with `rand`. */
function reconnectDelay(n: number, rand: number): number {
  const r = reduce(
    { kind: "connecting", generation: 1, attempt: n - 1, visible: true, online: true },
    { type: "connect_failed", generation: 1, now: 0, reason: { kind: "network" }, rand },
    DEFAULT_TIMING,
  );
  const arm = r.effects.find((e) => e.kind === "arm" && e.timer === "backoff");
  if (arm?.kind !== "arm") {
    throw new Error("the reducer armed no backoff");
  }
  return arm.ms;
}

describe("a failed re-read backs off on the reconnect curve", () => {
  it("failed re-read backs off: random 0.999 and a Bump every 10 ms for 10 s give stale() at 0, 499.5, 1498.5, 3496.5 and 7492.5 ms", async () => {
    const h = harness({ random: () => 0.999 });
    const s = failingSink();
    await churn(h, s, 10_000);
    expect(s.times).toEqual(fired([0, 499.5, 1498.5, 3496.5, 7492.5]));
  });

  it("the same with ok: false", async () => {
    const h = harness({ random: () => 0.999 });
    const s = failingSink("noseed");
    await churn(h, s, 10_000);
    expect(s.times).toEqual(fired([0, 499.5, 1498.5, 3496.5, 7492.5]));
  });

  it("random 0.5 gives at most 6", async () => {
    const h = harness({ random: () => 0.5 });
    const s = failingSink();
    await churn(h, s, 10_000);
    expect(s.times).toEqual([0, 250, 750, 1_750, 3_750, 7_750]);
  });

  it("the feed's wait for each n equals the stream runtime's reconnect delay", async () => {
    const draws = [0.25, 0.5, 0.75, 0.125, 0.5, 0.25, 0.75];
    let next = 0;
    const h = harness({ coalesceMs: 0, random: () => draws[next++] ?? 1 });
    const s = failingSink();
    await churn(h, s, 40_000);
    const waits = s.times.slice(1).map((t, i) => t - (s.times[i] ?? 0));
    expect(waits).toEqual(draws.map((r, i) => reconnectDelay(i + 1, r)));
  });

  it("a listening frame during a wait gives one stale() at once and the next wait is the following step", async () => {
    const h = harness({ random: () => 1 });
    const s = failingSink();
    h.feed.onFrame(live({ queue: 10 }));
    h.feed.watch("queue", s, position(["live", E2, 10]));
    const bump = bumper(h.feed, "queue", "live", E1);
    await bump(200);
    h.feed.onFrame(live({ queue: 10 }));
    await settle();
    await bump(3_000);
    expect(s.times).toEqual([0, 200, 1_200]);
  });

  it("a wait is never shorter than coalesceMs", async () => {
    const h = harness({ random: () => 0.01 });
    const s = failingSink();
    await churn(h, s, 200);
    expect(s.times).toEqual([0, 50, 100, 150, 200]);
  });

  it("another subscription's round leaves a waiting subscription to its wait", async () => {
    const h = harness({ random: () => 1 });
    const waiting = failingSink();
    const other = sink();
    h.feed.onFrame(live({ queue: 10 }));
    h.feed.watch("queue", waiting, position(["live", E2, 10]));
    h.feed.watch("queue", other, position(["live", E1, 10]));
    await settle();
    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    await advance(COALESCE);
    expect(other.log).toEqual(["stale"]);
    expect(waiting.times).toEqual([0]);
    await advance(450);
    expect(waiting.times).toEqual([0, 500]);
  });

  it("a stopped subscription's wait ends with no read", async () => {
    const h = harness({ random: () => 1 });
    const s = failingSink();
    h.feed.onFrame(live({ queue: 10 }));
    const sub = h.feed.watch("queue", s, position(["live", E2, 10]));
    await settle();
    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    sub.stop();
    await advance(10_000);
    expect(s.times).toEqual([0]);
  });

  it("a seed during a wait ends it and is checked at once", async () => {
    const h = harness({ random: () => 1 });
    const s = sink();
    h.feed.onFrame(store({ runs: 100 }));
    const sub = h.feed.watch("runs", s, position(["store", E2, 10]));
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    h.feed.onFrame(changed("store", E1, 120, ["runs"]));
    sub.seed(position(["store", E1, 110]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 110, kinds: ["runs"] },
    ]);
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("a seed resets the count", async () => {
    const h = harness({ random: () => 1 });
    const s = sink();
    h.feed.onFrame(live({ queue: 10 }));
    const sub = h.feed.watch("queue", s, position(["live", E2, 10]));
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await bumper(h.feed, "queue", "live", E1)(500);
    expect(s.reads).toHaveLength(2);
    sub.seed(position(["live", E1, 900_000]));
    s.reads[1]?.resolve();
    await advance(500);
    expect(s.reads).toHaveLength(2);

    h.feed.onFrame(live({ queue: 10 }, E2));
    expect(s.reads).toHaveLength(3);
    s.reads[2]?.reject(new Error("read failed"));
    await settle();
    const bump = bumper(h.feed, "queue", "live", E2);
    await bump(1_490);
    expect(s.reads).toHaveLength(3);
    await bump(1_500);
    expect(s.reads).toHaveLength(4);
  });
});

describe("rule 7: a catch-up that fails as retry", () => {
  it("a catch-up answered maintenance three times then 200 on a quiet source applies after three waits on the curve", async () => {
    const h = harness({ random: () => 1 });
    const s = sink();
    h.feed.onFrame(store({ runs: 150 }));
    h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    const at: number[] = [];
    for (const wait of [500, 1_000, 2_000]) {
      at.push(Date.now());
      await h.calls.at(-1)?.fail("retry");
      const sent = h.calls.length;
      await advance(wait - 1);
      expect(h.calls).toHaveLength(sent);
      await advance(1);
      expect(h.calls).toHaveLength(sent + 1);
    }
    at.push(Date.now());
    expect(at).toEqual([50, 550, 1_550, 3_550]);
    expect(h.calls.map((c) => c.q)).toEqual(
      Array.from({ length: 4 }, () => ({
        source: "store",
        epoch: E1,
        since: 100,
        kinds: ["runs"],
      })),
    );
    await h.calls[3]?.answer({ rev: 200, upserts: { runs: [{ id: "r1" }] } });
    expect(s.log).toEqual(["upsert r1"]);

    // The watermark is now 200, so a frame at 200 is covered.
    h.feed.onFrame(changed("store", E1, 200, ["runs"]));
    await advance(10_000);
    expect(h.calls).toHaveLength(4);

    // The applied answer reset the count: the next retry waits the first step again.
    h.feed.onFrame(changed("store", E1, 201, ["runs"]));
    await advance(COALESCE);
    await h.calls[4]?.fail("retry");
    await advance(499);
    expect(h.calls).toHaveLength(5);
    await advance(1);
    expect(h.calls).toHaveLength(6);
  });

  it("frames during a retry wait only mark", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(store({ runs: 150 }));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    await advance(COALESCE);
    await h.calls[0]?.fail("retry");
    for (let t = 1; t <= 49; t++) {
      await advance(10);
      h.feed.onFrame(changed("store", E1, 150 + t, ["runs"]));
    }
    expect(h.calls).toHaveLength(1);
    await advance(10);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
  });

  it("a listening frame ends a retry wait at once", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(store({ runs: 150 }));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    await advance(COALESCE);
    await h.calls[0]?.fail("retry");
    await advance(50);
    h.feed.onFrame(store({ runs: 160 }));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(2);

    // The count was not reset: the next wait is the second step.
    await h.calls[1]?.fail("retry");
    await advance(999);
    expect(h.calls).toHaveLength(2);
    await advance(1);
    expect(h.calls).toHaveLength(3);
  });

  it("a refused catch-up's re-read that fails backs off and never loops at the frame rate", async () => {
    const h = harness({ random: () => 1 });
    const s = failingSink();
    h.feed.onFrame(store({ runs: 150 }));
    h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    await h.calls[0]?.fail("refused");
    await bumper(h.feed, "runs", "store", E1)(5_000);
    expect(s.times).toEqual([50, 550, 1_550, 3_550]);
    expect(h.calls).toHaveLength(1);
  });

  it("after an epoch change kind A's re-read fails as retry while kind B re-seeds, and no request pairs A's revision with B's epoch", async () => {
    const h = harness({ random: () => 1 });
    const a = sink();
    const b = sink();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const subA = h.feed.watch("runs", a, position(["store", E1, 100]));
    const subB = h.feed.watch("budgets", b, position(["store", E1, 100]));
    h.feed.onFrame(store({ runs: 50, budgets: 50 }, E2));
    expect([a.log, b.log]).toEqual([["stale"], ["stale"]]);

    a.reads[0]?.reject(new Error("network"));
    subB.seed(position(["store", E2, 40]));
    b.reads[0]?.resolve();
    await settle();
    h.feed.onFrame(changed("store", E2, 60, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 60 });

    await advance(500);
    expect(a.log).toEqual(["stale", "stale"]);
    subA.seed(position(["store", E2, 55]));
    a.reads[1]?.resolve();
    await settle();
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E2, since: 40, kinds: ["budgets"] },
      { source: "store", epoch: E2, since: 55, kinds: ["runs"] },
    ]);
  });
});
