import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createFeed } from "./feed.js";
import type { Feed, FeedStream } from "./types.js";
import { FEED_VERSION } from "./version.js";

interface Spawn {
  readonly url: URL;
  readonly options: unknown;
}

let spawns: Spawn[] = [];
let feed: Feed<Record<string, never>> | null = null;
let base: HTMLBaseElement | null = null;

class RecordingSharedWorker {
  readonly port = new MessageChannel().port1;
  readonly addEventListener = (): void => undefined;

  constructor(url: string, options: unknown) {
    spawns.push({ url: new URL(url, document.baseURI), options });
  }
}

beforeEach(() => {
  spawns = [];
  vi.stubGlobal("SharedWorker", RecordingSharedWorker);
});

afterEach(() => {
  feed?.attachment?.detach("logout");
  feed = null;
  base?.remove();
  base = null;
});

function open(stream: FeedStream): void {
  feed = createFeed({
    feedVersion: FEED_VERSION,
    kinds: {},
    logged: [],
    catchUp: () => Promise.resolve({ ok: false, failure: "refused" }),
    stream,
  });
}

function spawnOf(stream: FeedStream): Spawn {
  open(stream);
  expect(spawns).toHaveLength(1);
  return spawns[0] as Spawn;
}

describe("createFeed's stream option opens the shared worker", () => {
  it("as a classic worker at the entry URL, the stream path its only query parameter", () => {
    const { url, options } = spawnOf({ path: "/api/a b&c", worker: "/assets/feed_worker-abc.js" });
    expect(url.origin).toBe(location.origin);
    expect(url.pathname).toBe("/assets/feed_worker-abc.js");
    expect([...url.searchParams]).toEqual([["stream", "/api/a b&c"]]);
    expect(url.hash).toBe("");
    expect(options).toEqual({ name: "@cplieger/sse/feed", type: "classic" });
  });

  it("keeps the entry URL's own query parameters beside the stream path", () => {
    const { url } = spawnOf({ path: "/events", worker: "/w.js?v=1" });
    expect(url.pathname).toBe("/w.js");
    expect([...url.searchParams]).toEqual([
      ["v", "1"],
      ["stream", "/events"],
    ]);
  });

  it("resolves a relative entry URL against the document's base URL", () => {
    base = document.createElement("base");
    base.href = "/sub/";
    document.head.append(base);
    const { url } = spawnOf({ path: "/events", worker: "w.js" });
    expect(url.pathname).toBe("/sub/w.js");
  });

  it("as a module worker when the stream says so", () => {
    const { options } = spawnOf({ path: "/events", worker: "/w.js", type: "module" });
    expect(options).toEqual({ name: "@cplieger/sse/feed", type: "module" });
  });

  it.each([
    ["a stream query parameter", "/w.js?stream=/old"],
    ["a stream query parameter after another", "/w.js?v=1&stream=/old"],
    ["a fragment", "/w.js#h"],
    ["an empty fragment", "/w.js#"],
    ["no URL the parser accepts", "http://["],
  ])("refuses an entry URL with %s, spawning nothing", (_, worker) => {
    expect(() => {
      open({ path: "/events", worker });
    }).toThrow(RangeError);
    expect(() => {
      open({ path: "/events", worker });
    }).toThrow(`"${worker}"`);
    expect(spawns).toEqual([]);
  });
});
