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

// A screen's reads under the feed, one rule each: the per-view read sequencing the feed took over.
describe("a screen's reads under the feed", () => {
  it("changes during a re-read coalesce into one more check at its settling", async () => {
    const h = harness();
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    const sub = h.feed.watch("queue", s, position(["live", E1, 10]));
    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    await advance(COALESCE);
    expect(s.log).toEqual(["stale"]);

    for (const rev of [12, 13, 14]) {
      h.feed.onFrame(changed("live", E1, rev, ["queue"]));
    }
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);

    sub.seed(position(["live", E1, 12]));
    s.reads[0]?.resolve();
    await settle();
    await advance(COALESCE);
    expect(s.log).toEqual(["stale", "stale"]);
    sub.seed(position(["live", E1, 14]));
    s.reads[1]?.resolve();
    await advance(10_000);
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("a failed re-read keeps the sink's rows and leaves it uncovered", async () => {
    const h = harness({ random: () => 0 });
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 101, full: true });
    expect(s.log).toEqual(["stale"]);
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);

    // A frame at the old floor leaves a covered subscription alone; this one has no watermark.
    h.feed.onFrame(store({ runs: 100 }));
    await advance(COALESCE);
    expect(s.log).toEqual(["stale", "stale"]);
    expect(s.upserted).toEqual([]);
    expect(s.removed).toEqual([]);
  });

  it("a screen's retry during a re-read joins it", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 101, full: true });
    h.feed.onFrame(changed("store", E1, 130, ["runs"]));

    // The screen's own Retry reads twice while the feed's re-read is in flight.
    sub.seed(position(["store", E1, 110]));
    sub.seed(position(["store", E1, 120]));
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(1);

    s.reads[0]?.resolve();
    await settle();
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 120, kinds: ["runs"] },
    ]);
    expect(s.log).toEqual(["stale"]);
  });

  it("a change after a failed first re-read reads again after the wait", async () => {
    const h = harness({ random: () => 1 });
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    h.feed.watch("queue", s, position(["live", E2, 3]));
    expect(s.log).toEqual(["stale"]);
    s.reads[0]?.reject(new Error("read failed"));
    await settle();
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);

    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    await advance(COALESCE - 1);
    expect(s.log).toEqual(["stale"]);
    await advance(1);
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("nothing reads before the first seed", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    h.feed.onFrame(live({ queue: 10 }));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    await advance(10_000);
    expect(h.calls).toHaveLength(0);

    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
  });

  it("stop aborts the catch-up in flight and its answer changes nothing", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    sub.stop();
    expect(h.calls[0]?.signal.aborted).toBe(true);
    await h.calls[0]?.answer({ rev: 101, upserts: { runs: [{ id: "r1" }] } });
    h.feed.onFrame(changed("store", E1, 102, ["runs"]));
    await advance(10_000);
    expect(s.log).toEqual([]);
    expect(h.calls).toHaveLength(1);
  });

  it("stop during a re-read leaves its settling with nothing to send", async () => {
    const h = harness();
    h.feed.onFrame(live({ queue: 10 }));
    const s = sink();
    const sub = h.feed.watch("queue", s, position(["live", E1, 10]));
    h.feed.onFrame(changed("live", E1, 11, ["queue"]));
    await advance(COALESCE);
    h.feed.onFrame(changed("live", E1, 12, ["queue"]));
    sub.stop();
    sub.seed(position(["live", E1, 11]));
    s.reads[0]?.resolve();
    await settle();
    await advance(10_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("a check at the settling that no longer fits reads again", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 101, full: true });
    expect(s.log).toEqual(["stale"]);

    // A restore lands while the re-read is in flight, and the read was answered by the old file.
    h.feed.onFrame(store({ runs: 5 }, E2));
    sub.seed(position(["store", E1, 101]));
    s.reads[0]?.resolve();
    await settle();
    expect(s.log).toEqual(["stale", "stale"]);

    sub.seed(position(["store", E2, 5]));
    s.reads[1]?.resolve();
    await advance(10_000);
    expect(s.log).toEqual(["stale", "stale"]);
    expect(h.calls).toHaveLength(1);
  });
});
