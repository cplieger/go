import { describe, expect, it } from "vitest";
import { createFeed } from "./feed.js";
import * as gen from "./test-helpers/generated-client.js";
import type { FeedOptions } from "./types.js";
import { FEED_VERSION, FeedVersionMismatch } from "./version.js";

const base: FeedOptions<Record<string, unknown>> = {
  feedVersion: FEED_VERSION,
  kinds: gen.KINDS,
  logged: gen.LOGGED_SOURCES,
  catchUp: () => new Promise(() => undefined),
  reconnect: () => undefined,
};

describe("createFeed", () => {
  it("createFeed throws FeedVersionMismatch naming both numbers", () => {
    expect(gen.FEED_VERSION).toBe(FEED_VERSION + 1);
    let thrown: unknown;
    try {
      createFeed({ ...base, feedVersion: gen.FEED_VERSION });
    } catch (e) {
      thrown = e;
    }
    expect(thrown).toBeInstanceOf(FeedVersionMismatch);
    const err = thrown as FeedVersionMismatch;
    expect(err.generated).toBe(gen.FEED_VERSION);
    expect(err.library).toBe(FEED_VERSION);
    expect(err.message).toContain(`FEED_VERSION is ${String(gen.FEED_VERSION)}`);
    expect(err.message).toContain(`reads ${String(FEED_VERSION)}`);
  });

  it("checks the version before any other option", () => {
    expect(() => createFeed({ ...base, feedVersion: gen.FEED_VERSION, coalesceMs: -1 })).toThrow(
      FeedVersionMismatch,
    );
  });

  it("refuses coalesceMs outside 0 to 1,000", () => {
    for (const ms of [-1, 1_001, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(() => createFeed({ ...base, coalesceMs: ms })).toThrow(RangeError);
    }
    for (const ms of [0, 1_000]) {
      expect(() => createFeed({ ...base, coalesceMs: ms })).not.toThrow();
    }
  });

  it("refuses a kind, source or logged name outside its grammar", () => {
    expect(() => createFeed({ ...base, kinds: { Runs: "store" } })).toThrow(/Runs/);
    expect(() => createFeed({ ...base, kinds: { runs: "_store" } })).toThrow(/_store/);
    expect(() => createFeed({ ...base, logged: ["2x"] })).toThrow(/2x/);
    expect(() => createFeed({ ...base, kinds: { ["a".repeat(33)]: "store" } })).toThrow(RangeError);
  });

  it("refuses to watch an undeclared kind, naming it", () => {
    const feed = createFeed(base);
    const sink = { stale: () => Promise.resolve() };
    expect(() => feed.watch("constructor", sink, new Headers())).toThrow(
      new RangeError('createFeed: kind "constructor" is not declared'),
    );
  });
});
