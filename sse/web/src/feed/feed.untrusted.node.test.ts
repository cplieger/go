import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Frame } from "../stream.js";
import {
  E1,
  E2,
  advance,
  changed,
  failingSink,
  harness,
  listening,
  position,
  settle,
  sink,
  type Harness,
} from "./test-helpers/feed-harness.js";
import { FEED_VERSION } from "./version.js";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const COALESCE = 50;
const W1 = "3f9a0c1d2e4b5a67";
const W2 = "0123456789abcdef";
const E3 = "c07e5d4c3b2a19087f6e5d4c3b2a1908";

const raw = (type: string, data: string): Frame => ({ type, data, id: null });
const malformed = raw("changed", '{"source":"store"');

const both = (
  store: Readonly<Record<string, number>>,
  live: Readonly<Record<string, number>>,
  wire: string | null = null,
) =>
  listening(
    [
      { source: "store", epoch: E1, floors: store },
      { source: "live", epoch: E2, floors: live },
    ],
    wire,
  );

const queries = (h: Harness) => h.calls.map((c) => c.q);

describe("rule 8: another feed format", () => {
  it("another feed format shows the banner, applies no frame and never reconnects", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100 }, { queue: 5 }));
    const runs = sink();
    const queue = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.watch("queue", queue, position(["live", E2, 5]));

    expect(
      h.feed.onFrame(
        listening([{ source: "store", epoch: E1, floors: { runs: 300 } }], W1, FEED_VERSION + 1),
      ),
    ).toBe(true);
    const unreadable = [
      changed("store", E1, 200, ["runs"]),
      changed("live", E2, 9, ["queue"]),
      raw("changed", "{"),
      raw("changed", '{"store":{"rev":201}}'),
      raw("listening", '{"feed":2,"views":[1]}'),
      raw("listening", "not json"),
      raw("listening", '{"sources":[]}'),
    ];
    for (const f of unreadable) {
      expect(h.feed.onFrame(f)).toBe(true);
    }
    await advance(10_000);
    expect(h.wires).toEqual([W1]);
    expect(h.reconnect).not.toHaveBeenCalled();
    expect(h.calls).toHaveLength(0);
    expect(runs.log).toEqual([]);
    expect(queue.log).toEqual([]);

    // A rolled-back server: its frame checks every subscription as a first frame does.
    h.feed.onFrame(both({ runs: 200 }, { queue: 5 }));
    await advance(COALESCE);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 100, kinds: ["runs"] }]);
    expect(queue.log).toEqual([]);
    await advance(10_000);
    expect(h.calls).toHaveLength(1);
  });

  it("under another format no frame throws, and each frame's wire still reaches onWire", () => {
    const h = harness();
    h.feed.onFrame(listening([], null, FEED_VERSION + 1));
    expect(() => h.feed.onFrame(malformed)).not.toThrow();
    expect(() => h.feed.onFrame(changed("store", E2, 5, ["runs"]))).not.toThrow();
    expect(() => h.feed.onFrame(raw("listening", "{"))).not.toThrow();
    h.feed.onFrame(listening([], W2, FEED_VERSION + 1));
    h.feed.onFrame(listening([], W1, 0));
    expect(h.wires).toEqual([W2, W1]);
    expect(h.reconnect).not.toHaveBeenCalled();
  });

  it("entering another format sends none of the reads and catch-ups already scheduled", async () => {
    const h = harness({ random: () => 0.999 });
    h.feed.onFrame(both({ runs: 150 }, {}));
    const queue = sink();
    h.feed.watch("queue", queue, position());
    const runs = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.onFrame(changed("live", E2, 1, ["queue"]));
    await advance(COALESCE);
    expect(queue.log).toEqual(["stale"]);
    await h.calls[0]?.fail("retry");
    queue.reads[0]?.resolve();
    await settle();

    // queue's read failed and waits on the backoff, runs waits on the retry, and frames mark both.
    h.feed.onFrame(changed("store", E1, 160, ["runs"]));
    h.feed.onFrame(changed("live", E2, 2, ["queue"]));
    const unlogged = sink();
    h.feed.watch("queue", unlogged, position());
    h.feed.onFrame(changed("live", E2, 3, ["queue"]));
    h.feed.onFrame(listening([], W1, FEED_VERSION + 1));
    await advance(60_000);
    expect(h.calls).toHaveLength(1);
    expect(queue.log).toEqual(["stale"]);
    expect(unlogged.log).toEqual([]);
    expect(runs.log).toEqual([]);
  });

  it("an own-format frame that does not parse ends another format's path and is lost track", () => {
    const h = harness();
    h.feed.onFrame(listening([], W1, FEED_VERSION + 1));
    expect(() =>
      h.feed.onFrame(raw("listening", `{"feed":${String(FEED_VERSION)},"sources":{}}`)),
    ).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
  });
});

describe("rule 8: lost track", () => {
  it("a wire-only difference keeps applying and recovers from a bad frame", async () => {
    const h = harness();
    h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 100 } }], W2));
    expect(h.wires).toEqual([W2]);
    const runs = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 100, kinds: ["runs"] }]);
    await h.calls[0]?.answer({ rev: 101, upserts: { runs: [{ id: "r1" }] } });
    expect(runs.log).toEqual(["upsert r1"]);

    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
    h.feed.onFrame(changed("store", E1, 102, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(1);

    h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 102 } }], W2));
    await advance(COALESCE);
    expect(queries(h)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 101, kinds: ["runs"] },
    ]);
    expect(h.wires).toEqual([W2, W2]);
  });

  it("lost track: one reconnect per episode, one catch-up per logged source and one stale() per unlogged subscription after the reconnect's listening frame, none after without a new frame", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100, budgets: 100 }, { queue: 100 }));
    const subs = {
      runs: sink(),
      runsAhead: sink(),
      budgets: sink(),
      queueA: sink(),
      queueB: sink(),
      queueAhead: sink(),
    };
    h.feed.watch("runs", subs.runs, position(["store", E1, 100]));
    h.feed.watch("runs", subs.runsAhead, position(["store", E1, 120]));
    h.feed.watch("budgets", subs.budgets, position(["store", E1, 100]));
    h.feed.watch("queue", subs.queueA, position(["live", E2, 100]));
    h.feed.watch("queue", subs.queueB, position(["live", E2, 100]));
    h.feed.watch("queue", subs.queueAhead, position(["live", E2, 110]));

    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
    await advance(1_000);
    expect(h.calls).toHaveLength(0);

    h.feed.onFrame(both({ runs: 120, budgets: 130 }, { queue: 110 }));
    await advance(COALESCE);
    expect(queries(h)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["budgets", "runs"] },
    ]);
    expect(subs.queueA.log).toEqual(["stale"]);
    expect(subs.queueB.log).toEqual(["stale"]);
    expect(subs.queueAhead.log).toEqual([]);
    expect(subs.runsAhead.log).toEqual([]);

    await advance(10_000);
    expect(h.calls).toHaveLength(1);
    expect(subs.queueA.log).toEqual(["stale"]);
    expect(h.reconnect).toHaveBeenCalledTimes(1);
  });

  it("a seed during an episode waits and is checked once", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100 }, {}));
    const s = sink();
    const sub = h.feed.watch("runs", s, position(["store", E1, 100]));
    expect(() => h.feed.onFrame(malformed)).toThrow();

    sub.seed(position(["store", E1, 90]));
    h.feed.watch("runs", sink(), position(["store", E1, 95]));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);

    h.feed.onFrame(both({ runs: 100 }, {}));
    await advance(COALESCE);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 90, kinds: ["runs"] }]);
    await h.calls[0]?.answer({ rev: 100 });
    await advance(10_000);
    expect(h.calls).toHaveLength(1);
    expect(s.log).toEqual([]);
  });

  it("a second bad frame inside an episode calls no second reconnect()", () => {
    const h = harness();
    h.feed.onFrame(both({}, {}));
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(() => h.feed.onFrame(raw("listening", "{"))).toThrow();
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
  });

  it("the episode ends once every source known when it began has a listening frame again", () => {
    const h = harness();
    h.feed.onFrame(both({}, {}));
    expect(() => h.feed.onFrame(malformed)).toThrow();

    // A reopen frame for one source leaves the episode open.
    h.feed.onFrame(listening([{ source: "store", epoch: E3, floors: {} }]));
    expect(() => h.feed.onFrame(malformed)).toThrow();
    h.feed.onFrame(listening([{ source: "store", epoch: E3, floors: {} }]));
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);

    h.feed.onFrame(both({}, {}));
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(2);
  });

  it("an episode entered before any listening frame ends at the first valid one", () => {
    const h = harness();
    expect(() => h.feed.onFrame(malformed)).toThrow();
    h.feed.onFrame(listening([{ source: "live", epoch: E2, floors: {} }]));
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(2);
  });

  it("a changed frame on another epoch is lost track", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100 }, {}));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    expect(() => h.feed.onFrame(changed("store", E3, 101, ["runs"]))).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);

    // The floors are forgotten: a frame of the old epoch is now one for a source with no epoch.
    h.feed.onFrame(changed("store", E1, 102, ["runs"]));
    await advance(1_000);
    expect(h.calls).toHaveLength(0);
    expect(s.log).toEqual([]);

    h.feed.onFrame(both({ runs: 102 }, {}));
    await advance(COALESCE);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 100, kinds: ["runs"] }]);
  });

  it("onFrame throws once per bad frame", () => {
    const h = harness();
    h.feed.onFrame(both({}, {}));
    const bad = [
      changed("live", E3, 4, ["queue"]),
      malformed,
      raw("changed", '{"source":"store","epoch":"' + E1 + '","rev":0,"kinds":["runs"]}'),
      raw("listening", '{"feed":' + String(FEED_VERSION) + ',"sources":[],"lost":true}'),
      raw("listening", "[]"),
    ];
    for (const f of bad) {
      expect(() => h.feed.onFrame(f)).toThrow(Error);
    }
    expect(() => h.feed.onFrame(changed("store", E1, 4, ["runs"]))).not.toThrow();
    expect(() => h.feed.onFrame(raw("app", "{"))).not.toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
  });

  it("the reconnect's frame waits coalesceMs from its own marks, not a window opened before the bad frame", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100 }, {}));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(20);
    expect(() => h.feed.onFrame(malformed)).toThrow();
    await advance(10);
    h.feed.onFrame(both({ runs: 101 }, {}));
    await advance(COALESCE - 1);
    expect(h.calls).toHaveLength(0);
    await advance(1);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 100, kinds: ["runs"] }]);
  });

  it("a catch-up queued before the bad frame is not sent when the one in flight settles", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 100 }, {}));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));
    h.feed.onFrame(changed("store", E1, 101, ["runs"]));
    await advance(COALESCE);
    h.feed.onFrame(changed("store", E1, 102, ["runs"]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);
    expect(() => h.feed.onFrame(malformed)).toThrow();

    h.feed.onFrame(both({ runs: 102 }, {}));
    await advance(10);
    await h.calls[0]?.answer({ rev: 101 });
    expect(h.calls).toHaveLength(1);
    await advance(COALESCE - 11);
    expect(h.calls).toHaveLength(1);
    await advance(1);
    expect(queries(h)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 101, kinds: ["runs"] },
    ]);
  });

  it("a catch-up in flight when track is lost changes nothing at its settling", async () => {
    const h = harness();
    h.feed.onFrame(both({ runs: 150 }, {}));
    const s = sink();
    h.feed.watch("runs", s, position(["store", E1, 100]));
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);
    expect(() => h.feed.onFrame(malformed)).toThrow();
    expect(h.calls[0]?.signal.aborted).toBe(false);

    await h.calls[0]?.answer({ rev: 150, upserts: { runs: [{ id: "r1" }] } });
    await advance(1_000);
    expect(s.log).toEqual([]);
    expect(h.calls).toHaveLength(1);

    h.feed.onFrame(both({ runs: 150 }, {}));
    await advance(COALESCE);
    expect(queries(h)).toEqual([
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
      { source: "store", epoch: E1, since: 100, kinds: ["runs"] },
    ]);
  });

  it("a failed re-read's wait running when track is lost is ended by the reconnect's frame", async () => {
    const h = harness({ random: () => 0.999 });
    h.feed.onFrame(both({}, { queue: 5 }));
    const s = failingSink();
    h.feed.watch("queue", s, position());
    h.feed.onFrame(changed("live", E2, 6, ["queue"]));
    await advance(COALESCE);
    expect(s.times).toHaveLength(1);
    expect(() => h.feed.onFrame(malformed)).toThrow();
    await advance(100);
    expect(s.times).toHaveLength(1);
    h.feed.onFrame(both({}, { queue: 7 }));
    await settle();
    expect(s.times).toHaveLength(2);
  });
});

describe("rule 8: newer build", () => {
  it("newer build: undeclared kinds and sources are dropped, the rest applied, onWire called, no lost track", async () => {
    const h = harness();
    const heard: string[] = [];
    h.feed.onListening("store", () => heard.push("store"));
    const runs = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    const frame = listening(
      [
        { source: "store", epoch: E1, floors: { jobs: 500, runs: 100 } },
        { source: "accounts", epoch: E3, floors: { users: 1 } },
      ],
      W2,
    );
    expect(() => h.feed.onFrame(frame)).not.toThrow();
    expect(h.wires).toEqual([W2]);
    expect(heard).toEqual(["store"]);
    await advance(1_000);
    expect(h.calls).toHaveLength(0);

    expect(() => h.feed.onFrame(changed("accounts", E3, 2, ["users"]))).not.toThrow();
    expect(() => h.feed.onFrame(changed("store", E1, 501, ["jobs"]))).not.toThrow();
    await advance(1_000);
    expect(h.calls).toHaveLength(0);

    h.feed.onFrame(changed("store", E1, 150, ["jobs", "runs"]));
    await advance(COALESCE);
    expect(queries(h)).toEqual([{ source: "store", epoch: E1, since: 100, kinds: ["runs"] }]);
    expect(h.reconnect).not.toHaveBeenCalled();
  });

  it("a changed frame for a source with no current epoch is ignored", async () => {
    const h = harness();
    const runs = sink();
    h.feed.watch("runs", runs, position());
    const queue = sink();
    h.feed.watch("queue", queue, position());
    expect(() => h.feed.onFrame(changed("store", E1, 5, ["runs"]))).not.toThrow();
    h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: {} }]));
    await advance(COALESCE);
    expect(runs.log).toEqual(["stale"]);

    expect(() => h.feed.onFrame(changed("live", E2, 5, ["queue"]))).not.toThrow();
    await advance(1_000);
    expect(queue.log).toEqual([]);
    expect(h.reconnect).not.toHaveBeenCalled();
  });
});

describe("rule 9: wire version", () => {
  it("a listening frame without wire parses and calls no onWire", async () => {
    const h = harness();
    const runs = sink();
    h.feed.watch("runs", runs, position(["store", E1, 100]));
    h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 101 } }]));
    await advance(COALESCE);
    expect(h.wires).toEqual([]);
    expect(h.calls).toHaveLength(1);
  });

  it("onWire gets the wire of an own-format frame whose sources do not parse, before it is lost track", () => {
    const order: string[] = [];
    const h = harness({
      onWire: (w) => order.push(`wire ${w}`),
      reconnect: () => order.push("reconnect"),
    });
    const data = `{"feed":${String(FEED_VERSION)},"wire":"${W1}","sources":[{"source":"Store"}]}`;
    expect(() => h.feed.onFrame(raw("listening", data))).toThrow();
    expect(order).toEqual([`wire ${W1}`, "reconnect"]);
  });

  it("a throw from onWire or a listener is reported and the frame still applies", async () => {
    const reported: unknown[] = [];
    vi.stubGlobal("reportError", (e: unknown) => reported.push(e));
    const wireError = new Error("wire");
    const listenerError = new Error("listener");
    const h = harness({
      onWire: () => {
        throw wireError;
      },
    });
    const heard: string[] = [];
    h.feed.onListening("store", () => {
      throw listenerError;
    });
    h.feed.onListening("store", () => heard.push("second"));
    h.feed.watch("runs", sink(), position(["store", E1, 100]));

    expect(() =>
      h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 101 } }], W1)),
    ).not.toThrow();
    expect(reported).toEqual([wireError, listenerError]);
    expect(heard).toEqual(["second"]);
    await advance(COALESCE);
    expect(h.calls).toHaveLength(1);
  });
});
