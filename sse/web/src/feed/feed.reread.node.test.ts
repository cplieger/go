import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  E1,
  E2,
  advance,
  changed,
  harness,
  listening,
  position,
  settle,
  sink,
} from "./test-helpers/feed-harness.js";
import type { Harness } from "./test-helpers/feed-harness.js";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

const COALESCE = 50;
const live = (floors: Readonly<Record<string, number>>, epoch = E1) =>
  listening([{ source: "live", epoch, floors }]);
const store = (floors: Readonly<Record<string, number>>, epoch = E1) =>
  listening([{ source: "store", epoch, floors }]);

function bumps(h: Harness, from: number, to: number, epoch = E1): void {
  for (let rev = from; rev <= to; rev++) {
    h.feed.onFrame(changed("live", epoch, rev, ["queue"]));
  }
}

describe("rule 6: unlogged churn", () => {
  it("unlogged churn: 20 Bumps in one window give one stale()", async () => {
    const h = harness();
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E1, 10]));
    bumps(h, 11, 30);
    await advance(COALESCE - 1);
    expect(s.log).toEqual([]);
    await advance(1);
    expect(s.log).toEqual(["stale"]);
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("20 more during that re-read give none", async () => {
    const h = harness();
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E1, 10]));
    bumps(h, 11, 30);
    await advance(COALESCE);
    bumps(h, 31, 50);
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("its seed below the post-burst floor gives exactly one more after coalesceMs and one at or above it gives none", async () => {
    for (const [seed, more] of [
      [40, 1],
      [50, 0],
    ] as const) {
      const h = harness();
      h.feed.onFrame(live({ queue: 10 }));
      const s = sink();
      const sub = h.feed.watch("queue", s, position(["live", E1, 10]));
      bumps(h, 11, 30);
      await advance(COALESCE);
      bumps(h, 31, 50);
      sub.seed(position(["live", E1, seed]));
      s.reads[0]?.resolve();
      await settle();
      await advance(COALESCE - 1);
      expect(s.log).toEqual(["stale"]);
      await advance(1_000);
      expect(s.log).toHaveLength(1 + more);
    }
  });

  it("a re-read that rejects with a mark set gives one more round after the first backoff wait", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E1, 10]));
    bumps(h, 11, 30);
    await advance(COALESCE);
    bumps(h, 31, 50);
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await advance(499);
    expect(s.log).toEqual(["stale"]);
    await advance(1);
    expect(s.log).toEqual(["stale", "stale"]);
    await advance(10_000);
    expect(s.log).toEqual(["stale", "stale"]);
  });
});

describe("a re-read in flight", () => {
  it("an epoch change on an unlogged source then 20 Bumps during the re-read give one stale()", async () => {
    const h = harness();
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    const sub = h.feed.watch("queue", s, position(["live", E1, 10]));
    h.feed.onFrame(live({ queue: 5 }, E2));
    expect(s.log).toEqual(["stale"]);
    bumps(h, 6, 25, E2);
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);

    sub.seed(position(["live", E2, 25]));
    s.reads[0]?.resolve();
    await settle();
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("a full answer then 20 changed frames during the re-read give one stale() and a seed below the raised floor gives one catch-up", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 150 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, full: true });
    expect(s.log).toEqual(["stale"]);
    for (let rev = 151; rev <= 170; rev++) {
      h.feed.onFrame(changed("store", E1, rev, ["runs"]));
    }
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(1);

    sub.seed(position(["store", E1, 160]));
    s.reads[0]?.resolve();
    await settle();
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 160, kinds: ["runs"] },
    ]);
    expect(s.log).toEqual(["stale"]);
  });

  it("a rejected re-read with frames retries once when its wait ends (random 1: 500 ms)", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E2, 10]));
    expect(s.log).toEqual(["stale"]);
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    bumps(h, 11, 30);
    await advance(499);
    expect(s.log).toEqual(["stale"]);
    await advance(1);
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("a rejected re-read with no frames waits for the next listening frame", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E2, 10]));
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);

    h.feed.onFrame(live({ queue: 10 }));
    await advance(COALESCE - 1);
    expect(s.log).toEqual(["stale"]);
    await advance(1);
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("after a rejected re-read the subscription is not covered", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(store({ runs: 5 }, E2));
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);

    // A frame below every revision the subscription ever held still finds it uncovered.
    h.feed.onFrame(changed("store", E2, 6, ["runs"]));
    await advance(COALESCE);
    expect(s.log).toEqual(["stale", "stale"]);
    expect(h.calls).toHaveLength(0);
  });
});

describe("one end condition", () => {
  it("a screen's own seed during a re-read starts no second stale() before the first settles", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 150 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, full: true });
    h.feed.onFrame(changed("store", E1, 170, ["runs"]));
    sub.seed(position(["store", E1, 155]));
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(1);

    s.reads[0]?.resolve();
    await settle();
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([100, 155]);
    expect(s.log).toEqual(["stale"]);
  });

  it("a seed at or above the floor during the re-read gives none at the settling", async () => {
    for (const kind of ["runs", "queue"] as const) {
      const h = harness();
      const source = kind === "runs" ? "store" : "live";
      h.feed.onFrame(listening([{ source, epoch: E1, floors: { [kind]: 10 } }]));
      const s = sink();
      const sub = h.feed.watch(kind, s, position([source, E2, 10]));
      expect(s.log).toEqual(["stale"]);
      h.feed.onFrame(changed(source, E1, 20, [kind]));
      sub.seed(position([source, E1, 20]));
      s.reads[0]?.resolve();
      await settle();
      await advance(10_000);
      expect(s.log).toEqual(["stale"]);
      expect(h.calls).toHaveLength(0);
    }
  });
});
