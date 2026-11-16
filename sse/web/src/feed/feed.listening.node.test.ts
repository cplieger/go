import { describe, expect, it, vi } from "vitest";
import {
  E1,
  E2,
  changed,
  harness,
  listening,
  position,
  sink,
} from "./test-helpers/feed-harness.js";

describe("onListening", () => {
  it("onListening fires once per connection and per reopen frame and stops after its unsubscribe", () => {
    const h = harness();
    const heard: string[] = [];
    const off = h.feed.onListening("store", () => heard.push("store"));
    h.feed.onListening("settings", () => heard.push("settings"));

    h.feed.onFrame(
      listening([
        { source: "store", epoch: E1, floors: { runs: 1 } },
        { source: "settings", epoch: E1, floors: { settings: 1 } },
      ]),
    );
    expect(heard).toEqual(["store", "settings"]);

    h.feed.onFrame(changed("store", E1, 2, ["runs"]));
    h.feed.onFrame(listening([{ source: "store", epoch: E2, floors: {} }]));
    expect(heard).toEqual(["store", "settings", "store"]);

    off();
    h.feed.onFrame(listening([{ source: "store", epoch: E2, floors: { runs: 3 } }]));
    expect(heard).toEqual(["store", "settings", "store"]);
  });

  it("a listener runs after the frame applied, so a read it starts is checked against the new epoch", async () => {
    vi.useFakeTimers();
    try {
      const h = harness();
      h.feed.onFrame(listening([{ source: "store", epoch: E1, floors: { runs: 1 } }]));
      const s = sink();
      h.feed.onListening("store", () => {
        h.feed.watch("runs", s, position(["store", E2, 1]));
      });
      h.feed.onFrame(listening([{ source: "store", epoch: E2, floors: { runs: 5 } }]));
      await vi.advanceTimersByTimeAsync(50);
      expect(s.log).toEqual([]);
      expect(h.calls.map((c) => c.q)).toEqual([
        { source: "store", epoch: E2, since: 1, kinds: ["runs"] },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a frame that does not parse, or names an undeclared source, calls no listener", () => {
    const h = harness();
    const heard: string[] = [];
    h.feed.onListening("store", () => heard.push("store"));
    h.feed.onListening("jobs", () => heard.push("jobs"));
    h.feed.onFrame({ type: "listening", data: "{", id: null });
    h.feed.onFrame(listening([{ source: "jobs", epoch: E1, floors: {} }]));
    expect(heard).toEqual([]);
  });
});
