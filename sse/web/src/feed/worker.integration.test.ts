import { afterEach, describe, expect, it, vi } from "vitest";
import type { LifecycleEvent } from "../lifecycle.js";
import { type FeedRow, type FeedSession, createFeedSession } from "../test-helpers/feed-client.js";
import { createFeed } from "./feed.js";
import { CASE_MS, QUIET_MS, STORE, fixtureUrl, sleep, waitFor } from "./test-helpers/feed-cases.js";
import { catchUpOf, readKind, streamPath } from "./test-helpers/feed-page.js";
import type { Feed } from "./types.js";
import { FEED_VERSION } from "./version.js";
import entryUrl from "./worker.ts?sharedworker&url";

const fixture = import.meta.env["SSE_FIXTURE"];
const sharedWorker = typeof SharedWorker !== "undefined";

type Rows = Record<string, FeedRow>;

const feeds: Feed<Rows>[] = [];
afterEach(() => {
  for (const f of feeds.splice(0)) {
    f.attachment?.detach("logout");
  }
});

interface Tab {
  readonly feed: Feed<Rows>;
  readonly listenings: () => number;
  readonly events: readonly LifecycleEvent[];
}

/** A tab: `createFeed` sharing the shipped entry, counting the listening frames it applied. */
function tab(session: FeedSession): Tab {
  let listenings = 0;
  const events: LifecycleEvent[] = [];
  const feed = createFeed<Rows>({
    feedVersion: FEED_VERSION,
    kinds: { runs: "store", budgets: "store" },
    logged: ["store"],
    catchUp: catchUpOf(session),
    stream: {
      path: streamPath(session),
      worker: entryUrl,
      type: "module",
      onLifecycle: (ev) => events.push(ev),
    },
  });
  feeds.push(feed);
  feed.onListening("store", () => {
    listenings++;
  });
  return { feed, listenings: () => listenings, events };
}

const opened = (t: Tab): boolean => t.events.some((e) => e.kind === "state" && e.to === "open");

async function watchRuns(session: FeedSession, feed: Feed<Rows>): Promise<Map<string, string>> {
  const first = await readKind(session, "runs");
  if (first === null) {
    throw new Error("the first read of runs failed");
  }
  const rows = new Map(first.rows.map((r) => [r.key, r.value]));
  feed.watch(
    "runs",
    {
      upsert(upserts) {
        for (const r of upserts) {
          rows.set(r.key, r.value);
        }
      },
      stale: () => Promise.resolve(),
    },
    first.headers,
  );
  return rows;
}

/** The message of the first error a dedicated worker running the entry at `query` reports. */
function entryError(query: string): Promise<string> {
  const url = `${entryUrl}${entryUrl.includes("?") ? "&" : "?"}${query}`;
  const worker = new Worker(url, { type: "module" });
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      worker.terminate();
      resolve("no error within 3 s");
    }, 3000);
    worker.addEventListener("error", (event) => {
      event.preventDefault();
      clearTimeout(timer);
      worker.terminate();
      resolve(event.message);
    });
  });
}

describe.skipIf(!fixture || !sharedWorker)("the feed's shipped SharedWorker entry", () => {
  it(
    "two tabs share one stream and each applies a commit",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      const b = tab(session);
      const rowsA = await watchRuns(session, a.feed);
      const rowsB = await watchRuns(session, b.feed);
      await waitFor("both listening frames", () => a.listenings() === 1 && b.listenings() === 1);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor(
        "the commit in both tabs",
        () => rowsA.get("r1") === "a" && rowsB.get("r1") === "a",
      );
      const state = await session.state();
      expect([state.connects, state.connected]).toEqual([1, 1]);
      expect([a.feed.attachment?.mode(), b.feed.attachment?.mode()]).toEqual(["worker", "worker"]);
    },
    CASE_MS,
  );

  it(
    "a tab attaching later receives the retained listening frame over the same connection",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      await waitFor("the first tab's listening frame", () => a.listenings() === 1);
      const b = tab(session);
      await waitFor("the retained listening frame", () => b.listenings() === 1);
      const rowsB = await watchRuns(session, b.feed);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit in the later tab", () => rowsB.get("r1") === "a");
      await sleep(QUIET_MS);
      expect([a.listenings(), b.listenings()]).toEqual([1, 1]);
      expect((await session.state()).connects).toBe(1);
    },
    CASE_MS,
  );

  it(
    "lost track reconnects the shared stream, whose listening frame brings the commit",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      const rows = await watchRuns(session, a.feed);
      await waitFor("the listening frame", () => a.listenings() === 1);
      await session.badFrame("other-epoch", "store", {
        rows: [{ kind: "runs", key: "r1", value: "a" }],
      });
      await waitFor("the commit's row", () => rows.get("r1") === "a");
      expect(a.listenings()).toBe(2);
      expect((await session.state()).connects).toBe(2);
    },
    CASE_MS,
  );

  it(
    "a tab with no SharedWorker streams on its own",
    async () => {
      vi.stubGlobal("SharedWorker", undefined);
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      await waitFor("the listening frame", () => a.listenings() === 1);
      expect(a.feed.attachment?.mode()).toBe("fallback");
      expect((await session.state()).connects).toBe(1);
    },
    CASE_MS,
  );

  it(
    "a tab sharing the worker hears the shared stream's state records",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      await waitFor("the state record to open", () => opened(a));
      expect(a.feed.attachment?.mode()).toBe("worker");
    },
    CASE_MS,
  );

  it(
    "a tab with no SharedWorker hears its own stream's state records",
    async () => {
      vi.stubGlobal("SharedWorker", undefined);
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const a = tab(session);
      await waitFor("the state record to open", () => opened(a));
      expect(a.feed.attachment?.mode()).toBe("fallback");
    },
    CASE_MS,
  );

  it.each([
    ["no stream parameter", "", "stream"],
    ["an empty path", "stream=", "stream"],
    ["a relative path", "stream=events", '"events"'],
    ["a scheme-relative path", "stream=%2F%2Fevil.example%2Fevents", '"//evil.example/events"'],
    [
      "a scheme-relative path to the first sentinel host",
      "stream=%2F%2Ffeed.invalid%2Fevents",
      '"//feed.invalid/events"',
    ],
    [
      "a scheme-relative path to the second sentinel host",
      "stream=%2F%2Fsecond.feed.invalid%2Fevents",
      '"//second.feed.invalid/events"',
    ],
    [
      "a tab inside the authority",
      "stream=%2F%09%2Fevil.example%2Fevents",
      '"/\t/evil.example/events"',
    ],
    [
      "an absolute URL",
      "stream=https%3A%2F%2Fevil.example%2Fevents",
      '"https://evil.example/events"',
    ],
    ["a path the URL parser refuses", "stream=%2F%2F", '"//"'],
  ])("the entry refuses %s, naming it", async (_, query, named) => {
    const message = await entryError(query);
    expect(message).toContain("stream");
    expect(message).toContain(named);
  });

  it.each([
    ["a relative path", "events"],
    ["a scheme-relative path", "//evil.example/events"],
    ["a backslash authority", "/\\evil.example/events"],
    ["a scheme-relative path to the first sentinel host", "//feed.invalid/events"],
    ["a backslash path to the first sentinel host", "/\\feed.invalid/events"],
    ["a scheme-relative path to the second sentinel host", "//second.feed.invalid/events"],
    ["a tab inside the authority", "/\t/evil.example/events"],
    ["an absolute URL", "https://evil.example/events"],
    ["an empty path", ""],
    ["a path the URL parser refuses", "//"],
  ])("createFeed refuses %s as the stream path", (_, path) => {
    expect(() =>
      createFeed<Rows>({
        feedVersion: FEED_VERSION,
        kinds: { runs: "store" },
        logged: ["store"],
        catchUp: () => Promise.resolve({ ok: false, failure: "refused" }),
        stream: { path, worker: entryUrl, type: "module" },
      }),
    ).toThrow(RangeError);
  });
});
