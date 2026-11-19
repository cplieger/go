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
  type RecordingSink,
} from "./test-helpers/feed-harness.js";
import type { Subscription } from "./types.js";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const COALESCE = 50;
const store = (floors: Readonly<Record<string, number>>, epoch = E1) =>
  listening([{ source: "store", epoch, floors }]);

describe("rule 1: seed", () => {
  it("a seed before the first listening frame waits and the frame checks it", async () => {
    const h = harness();
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
    expect(s.log).toEqual([]);

    h.feed.onFrame(store({ runs: 150 }));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
  });

  it("a seed before the first listening frame at or above its floor sends nothing", async () => {
    const h = harness();
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(store({ runs: 100 }));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
    expect(s.log).toEqual([]);
  });

  it("a seed on the current epoch below its floor is caught up from the seed", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 150 }));
    h.feed.watch("runs", sink(), position(["store", E1, 120]));
    h.feed.watch("runs", sink(), position(["store", E1, 150]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 120, kinds: ["runs"] },
    ]);
  });

  it("a seed on another epoch is staled once per listening frame and a second waits for the next frame", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E2, 7]));
    expect(s.log).toEqual(["stale"]);

    sub.seed(position(["store", E2, 8]));
    s.reads[0]?.resolve();
    await settle();
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(0);

    // The next frame names the epoch the server serves: the seed's, so it is checked.
    h.feed.onFrame(store({ runs: 9 }, E2));
    await advance(COALESCE);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E2, since: 8, kinds: ["runs"] },
    ]);
  });

  it("a waiting other-epoch seed is staled by a next frame that names another epoch", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E2, 7]));
    sub.seed(position(["store", E2, 8]));
    s.reads[0]?.resolve();
    await settle();

    h.feed.onFrame(store({ runs: 100 }));
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("each listening frame gives an other-epoch seed one more stale()", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E2, 7]));
    sub.seed(position(["store", E1, 100]));
    s.reads[0]?.resolve();
    await settle();
    h.feed.onFrame(store({ runs: 100 }));
    expect(s.log).toEqual(["stale"]);

    sub.seed(position(["store", E2, 8]));
    expect(s.log).toEqual(["stale", "stale"]);
  });

  it("a read with no Feed-Position gives no watermark", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const none = sink();
    const garbled = sink();
    h.feed.watch("runs", none, new Headers());
    h.feed.watch("runs", garbled, new Headers({ "Feed-Position": "store=1" }));
    await advance(1_000);
    expect(none.log).toEqual([]);
    expect(h.calls).toHaveLength(0);

    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    expect(none.log).toEqual(["stale"]);
    expect(garbled.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(0);
  });

  it("a seed for another source's position gives no watermark", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["settings", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(0);
  });
});

describe("rule 2: epoch", () => {
  it("a listening frame's new epoch stales every subscription of that source on another epoch and leaves another source's watermarks", async () => {
    const h = harness();
    h.feed.onFrame(
      listening([
        { source: "store", epoch: E1, floors: { runs: 100 } },
        { source: "settings", epoch: E1, floors: { settings: 5 } },
      ]),
    );
    const runs = sink();
    const budgets = sink();
    const settings = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.watch("budgets", budgets, position(["store", E1, 100]));
    h.feed.watch("settings", settings, position(["settings", E1, 5]));

    h.feed.onFrame(store({ runs: 3 }, E2));
    expect(runs.log).toEqual(["stale"]);
    expect(budgets.log).toEqual(["stale"]);
    expect(settings.log).toEqual([]);

    h.feed.onFrame(changed("settings", E1, 6, ["settings"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "settings", epoch: E1, since: 5, kinds: ["settings"] },
    ]);
  });

  it("a changed frame on an epoch the source does not have yet is ignored", async () => {
    const h = harness();
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
    expect(s.log).toEqual([]);

    h.feed.onFrame(store({ runs: 100 }));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
  });
});

describe("rule 3: frames", () => {
  it("a changed frame raises the floor and marks only subscriptions below it", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const list = sink();
    const detail = sink();
    const budgets = sink();
    h.feed.watch("runs", list, position(["store", E1, 100]));
    h.feed.watch("runs", detail, position(["store", E1, 200]));
    h.feed.watch("budgets", budgets, position(["store", E1, 100]));

    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "a" }] } });
    expect(list.log).toEqual(["upsert a"]);
    expect(detail.log).toEqual([]);
    expect(budgets.log).toEqual([]);

    // The raised floor checks a later seed.
    h.feed.watch("runs", sink(), position(["store", E1, 120]));
    await advance(COALESCE);
    expect(h.calls[1]?.q).toEqual({ source: "store", epoch: E1, since: 120, kinds: ["runs"] });
  });

  it("a changed frame never lowers a floor", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 200 }));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    h.feed.watch("runs", sink(), position(["store", E1, 180]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([180]);
  });

  it("a listening frame on the current epoch keeps the higher floor", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    h.feed.onFrame(store({ runs: 120 }));
    h.feed.watch("runs", sink(), position(["store", E1, 140]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([140]);
  });

  it("a subscription with no watermark is marked by the next frame and staled once after coalesceMs", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, new Headers());
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE - 1);
    h.feed.onFrame(changed("store", E1, 102, ["runs"]));
    h.feed.onFrame(changed("store", E1, 103, ["runs"]));
    expect(s.log).toEqual([]);
    await advance(1);
    expect(s.log).toEqual(["stale"]);
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);
  });

  it("a frame naming only an undeclared kind or source changes nothing", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["jobs"]));
    h.feed.onFrame(changed("other", E1, 150, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
  });
});

describe("rule 4: one read in flight per source", () => {
  it("one catch-up in flight per source with one queued", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);

    h.feed.onFrame(changed("store", E1, 160, ["runs"]));
    await advance(COALESCE);
    h.feed.onFrame(changed("store", E1, 170, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);

    await h.calls[0]?.answer({ rev: 150 });
    expect(h.calls.map((c) => c.q.since)).toEqual([100, 150]);
    await h.calls[1]?.answer({ rev: 170 });
    await advance(1_000);
    expect(h.calls).toHaveLength(2);
  });

  it("catch-ups of two sources run side by side", async () => {
    const h = harness();
    h.feed.onFrame(
      listening([
        { source: "store", epoch: E1, floors: { runs: 150 } },
        { source: "settings", epoch: E2, floors: { settings: 9 } },
      ]),
    );
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    h.feed.watch("settings", sink(), position(["settings", E2, 5]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.source).sort()).toEqual(["settings", "store"]);
  });

  it("since is the lowest marked watermark and no request pairs one epoch with another's revision", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const other = sink();
    const sub = h.feed.watch("runs", other, position(["store", E2, 5]));
    sub.seed(position(["store", E2, 5]));
    other.reads[0]?.resolve();
    await settle();

    h.feed.watch("runs", sink(), position(["store", E1, 120]));
    h.feed.watch("budgets", sink(), position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 200, ["budgets", "runs"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["budgets", "runs"] },
    ]);
  });
});

describe("rule 5: apply", () => {
  it("an answer upserts then removes and moves the watermark", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "a" }, { id: "b" }] },
      removed: { runs: ["x"] },
    });
    expect(s.log).toEqual(["upsert a,b", "remove x"]);

    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
    h.feed.onFrame(changed("store", E1, 151, ["runs"]));
    await advance(COALESCE);
    expect(h.calls[1]?.q.since).toBe(150);
  });

  it("a seed newer than the answer keeps its watermark and takes none of its rows", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    sub.seed(position(["store", E1, 300]));
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "old" }] } });
    expect(s.log).toEqual([]);

    h.feed.onFrame(changed("store", E1, 200, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
  });

  it("a seed older than its catch-up's since takes none of the answer and is caught up from the seed", async () => {
    const h = harness();
    h.feed.onFrame(store({ budgets: 100 }));
    const s = sink();
    const sub = h.feed.watch("budgets", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["budgets"]));
    await advance(COALESCE);
    sub.seed(position(["store", E1, 90]));
    await h.calls[0]?.answer({ rev: 150, removed: { budgets: ["b"] } });
    expect(s.log).toEqual([]);

    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([100, 90]);
    await h.calls[1]?.answer({ rev: 150, removed: { budgets: ["a", "b"] } });
    expect(s.log).toEqual(["remove a,b"]);
  });

  it("a later subscription a sink's upsert re-seeds below since takes none of the answer and is caught up from the seed", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    let later: Subscription | null = null;
    const base = sink();
    const runs: RecordingSink = {
      ...base,
      upsert(rows) {
        base.upsert?.(rows);
        later?.seed(position(["store", E1, 90]));
      },
    };
    const budgets = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    later = h.feed.watch("budgets", budgets, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "r" }] },
      removed: { budgets: ["b"] },
    });
    expect(runs.log).toEqual(["upsert r"]);
    expect(budgets.log).toEqual([]);

    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["budgets", "runs"] },
      { source: "store", epoch: E1, since: 90, kinds: ["budgets"] },
    ]);
  });

  it("a later subscription a sink's upsert re-seeds on another epoch takes none of the answer and the next frame reads it", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    let later: Subscription | null = null;
    const base = sink();
    const runs: RecordingSink = {
      ...base,
      upsert(rows) {
        base.upsert?.(rows);
        later?.seed(position(["store", E2, 8]));
      },
    };
    const budgets = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    // An other-epoch seed spends rule 1's allowance until the next listening frame.
    later = h.feed.watch("budgets", budgets, position(["store", E2, 7]));
    later.seed(position(["store", E1, 100]));
    budgets.reads[0]?.resolve();
    await settle();
    h.feed.onFrame(changed("store", E1, 150, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "r" }] },
      removed: { budgets: ["b"] },
    });
    expect(budgets.log).toEqual(["stale"]);

    h.feed.onFrame(store({ runs: 150, budgets: 150 }));
    expect(budgets.log).toEqual(["stale", "stale"]);
  });

  it("a later subscription a sink's upsert stops takes none of the answer", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    let later: Subscription | null = null;
    const base = sink();
    const runs: RecordingSink = {
      ...base,
      upsert(rows) {
        base.upsert?.(rows);
        later?.stop();
      },
    };
    const budgets = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    later = h.feed.watch("budgets", budgets, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "r" }], budgets: [{ id: "x" }] },
    });
    expect(runs.log).toEqual(["upsert r"]);
    expect(budgets.log).toEqual([]);
  });

  it("a sink's upsert that re-seeds its own subscription below the answer keeps that seed", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    let self: Subscription | null = null;
    let reseeded = false;
    const base = sink();
    const runs: RecordingSink = {
      ...base,
      upsert(rows) {
        base.upsert?.(rows);
        if (!reseeded) {
          reseeded = true;
          self?.seed(position(["store", E1, 90]));
        }
      },
    };
    self = h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "r" }] } });

    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([100, 90]);
  });

  it("an answer gives each kind only its own rows", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const runs = sink();
    const budgets = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.watch("budgets", budgets, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "r" }] },
      removed: { budgets: ["b"] },
    });
    expect(runs.log).toEqual(["upsert r"]);
    expect(budgets.log).toEqual(["remove b"]);
  });

  it("a signal kind or a sink with no upsert is staled instead", async () => {
    const h = harness();
    h.feed.onFrame(
      listening([
        { source: "store", epoch: E1, floors: { runs: 100 } },
        { source: "settings", epoch: E1, floors: { settings: 5 } },
      ]),
    );
    const settings = sink();
    const bare = sink({ upsert: false });
    h.feed.watch("settings", settings, position(["settings", E1, 5]));
    h.feed.watch("runs", bare, position(["store", E1, 100]));
    h.feed.onFrame(changed("settings", E1, 6, ["settings"]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    const bySource = (s: string) => h.calls.find((c) => c.q.source === s);
    await bySource("settings")?.answer({ rev: 6, signals: ["settings"] });
    await bySource("store")?.answer({ rev: 101, upserts: { runs: [{ id: "a" }] } });
    expect(settings.log).toEqual(["stale"]);
    expect(bare.log).toEqual(["stale"]);
  });

  it("a sink with no remove is staled by an answer that removes keys of its kind, before any upsert", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink({ remove: false });
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "a" }] },
      removed: { runs: ["x"] },
    });
    expect(s.log).toEqual(["stale"]);

    // The re-read's seed below the answer's rev is the watermark: the answer's rev is not.
    sub.seed(position(["store", E1, 140]));
    s.reads[0]?.resolve();
    await settle();
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q.since)).toEqual([100, 140]);
  });

  it("a sink with no remove takes an answer that removes no key of its kind", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const s = sink({ remove: false });
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({
      rev: 150,
      upserts: { runs: [{ id: "a" }] },
      removed: { budgets: ["b"] },
    });
    expect(s.log).toEqual(["upsert a"]);
  });

  it("a sink's throw is reported and staled, and the answer's other subscriptions still apply", async () => {
    const reported: unknown[] = [];
    vi.stubGlobal("reportError", (e: unknown) => reported.push(e));
    const boom = new Error("sink");
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const bad = {
      ...sink(),
      upsert: (): void => {
        throw boom;
      },
    };
    const good = sink();
    h.feed.watch("runs", bad, position(["store", E1, 100]));
    h.feed.watch("runs", good, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "a" }] } });
    expect(reported).toEqual([boom]);
    expect(bad.log).toEqual(["stale"]);
    expect(good.log).toEqual(["upsert a"]);

    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
  });

  it("a sink that stops its own subscription and then throws is reported and not read again", async () => {
    const reported: unknown[] = [];
    vi.stubGlobal("reportError", (e: unknown) => reported.push(e));
    const boom = new Error("sink");
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    let self: Subscription | null = null;
    const bad = {
      ...sink(),
      upsert: (): void => {
        self?.stop();
        throw boom;
      },
    };
    self = h.feed.watch("runs", bad, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "a" }] } });
    expect(reported).toEqual([boom]);
    expect(bad.log).toEqual([]);
  });

  it("an answer on another epoch than its request's stales the subscription as a full answer does", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 3, epoch: E2, upserts: { runs: [{ id: "a" }] } });
    expect(s.log).toEqual(["stale"]);
  });

  it("two subscriptions of one kind seeded at 100 and 200 both show a commit at 150 after one catch-up from 100", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const list = sink();
    const detail = sink();
    h.feed.watch("runs", list, position(["store", E1, 100]));
    h.feed.watch("runs", detail, position(["store", E1, 200]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "c150" }] } });
    // The detail's own read at 200 holds the commit at 150, so only the list needed the rows.
    expect(list.log).toEqual(["upsert c150"]);
    expect(detail.log).toEqual([]);
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
  });

  it("a commit above both seeds is one catch-up from the lower and moves both watermarks", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const list = sink();
    const detail = sink();
    h.feed.watch("runs", list, position(["store", E1, 100]));
    h.feed.watch("runs", detail, position(["store", E1, 200]));
    h.feed.onFrame(changed("store", E1, 250, ["runs"]));
    await advance(COALESCE);
    expect(h.calls.map((c) => c.q)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
    await h.calls[0]?.answer({ rev: 250, upserts: { runs: [{ id: "c150" }, { id: "c250" }] } });
    expect(list.log).toEqual(["upsert c150,c250"]);
    expect(detail.log).toEqual(["upsert c150,c250"]);

    h.feed.onFrame(changed("store", E1, 250, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
  });

  it("a full answer stales every subscription in the request", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    const runs = sink();
    const budgets = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.watch("budgets", budgets, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["budgets", "runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 150, full: true, upserts: { runs: [{ id: "a" }] } });
    expect(runs.log).toEqual(["stale"]);
    expect(budgets.log).toEqual(["stale"]);

    // Re-reading: frames only mark, no catch-up is sent.
    h.feed.onFrame(changed("store", E1, 160, ["budgets", "runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);
  });

  it("a full answer on another epoch spends the rule 1 allowance", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    await h.calls[0]?.answer({ rev: 3, epoch: E2, full: true });
    expect(s.log).toEqual(["stale"]);

    sub.seed(position(["store", E2, 3]));
    s.reads[0]?.resolve();
    await settle();
    expect(s.log).toEqual(["stale"]);

    h.feed.onFrame(store({ runs: 3 }, E2));
    await advance(1_000);
    expect(s.log).toEqual(["stale"]);
    expect(h.calls).toHaveLength(1);
  });

  it("a catch-up for stopped subscriptions only is aborted and its settling changes nothing", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100 }));
    const a = sink();
    const b = sink();
    const subA = h.feed.watch("runs", a, position(["store", E1, 100]));
    const subB = h.feed.watch("runs", b, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    const first = h.calls[0];

    const c = sink();
    h.feed.watch("runs", c, position(["store", E1, 120]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);

    subA.stop();
    expect(first?.signal.aborted).toBe(false);
    subB.stop();
    expect(first?.signal.aborted).toBe(true);
    expect(h.calls.map((x) => x.q.since)).toEqual([100, 120]);

    await first?.answer({ rev: 150, upserts: { runs: [{ id: "a" }] } });
    expect(a.log).toEqual([]);
    expect(b.log).toEqual([]);
    expect(c.log).toEqual([]);

    // The queued request is still the one in flight: a frame now waits for it.
    h.feed.onFrame(changed("store", E1, 170, ["runs"]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(2);
  });

  it("a sink that stops the last subscription of its catch-up during the apply leaves the queued catch-up the one in flight", async () => {
    const h = harness();
    h.feed.onFrame(store({ runs: 100, budgets: 100 }));
    let self: Subscription | null = null;
    const base = sink();
    const runs: RecordingSink = {
      ...base,
      upsert(rows) {
        base.upsert?.(rows);
        self?.stop();
      },
    };
    self = h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.watch("budgets", sink(), position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 150, ["runs"]));
    await advance(COALESCE);
    h.feed.onFrame(changed("store", E1, 160, ["budgets"]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);

    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "r" }] } });
    expect(h.calls.map((c) => c.q.kinds)).toEqual([["runs"], ["budgets"]]);

    h.feed.onFrame(changed("store", E1, 170, ["budgets"]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(2);
  });
});

describe("frames that are not the feed's", () => {
  it("onFrame returns false for an app event", () => {
    const h = harness();
    expect(h.feed.onFrame({ type: "frame", data: "{}", id: null })).toBe(false);
    expect(h.feed.onFrame({ type: "frame_admin", data: "x", id: null })).toBe(false);
    expect(h.feed.onFrame(store({ runs: 1 }))).toBe(true);
    expect(h.feed.onFrame(changed("store", E1, 2, ["runs"]))).toBe(true);
  });
});
