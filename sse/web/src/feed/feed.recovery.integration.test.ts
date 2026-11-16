import { afterEach, describe, expect, it } from "vitest";
import { type FeedSession, createFeedSession } from "../test-helpers/feed-client.js";
import workerUrl from "../test-helpers/worker-entry.ts?sharedworker&url";
import {
  ACCOUNTS,
  CASE_MS,
  LIVE,
  QUIET_MS,
  STORE,
  fixtureUrl,
  hellos,
  listenings,
  pageOpener,
  queries,
  readOrThrow,
  sleep,
  waitFor,
} from "./test-helpers/feed-cases.js";
import type { FeedPage } from "./test-helpers/feed-page.js";

const fixture = import.meta.env["SSE_FIXTURE"];
const sharedWorker = typeof SharedWorker !== "undefined";

const { open, detachAll } = pageOpener(workerUrl);
afterEach(detachAll);

const changedFrames = (page: FeedPage): number =>
  page.frames.filter((f) => f.type === "changed").length;

async function epochOf(session: FeedSession, source: string): Promise<string> {
  const src = (await session.state()).sources.find((s) => s.name === source);
  if (src === undefined) {
    throw new Error(`no source ${source}`);
  }
  return src.epoch;
}

/** Lost track, in the worker setup and on the per-tab fallback. */
function lostTrackRows(supported: boolean): void {
  it(
    "lost track: one malformed frame gives one reconnect, then one catch-up per logged source and one stale() per unlogged subscription",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE, LIVE]);
      const page = open(session, { supported });
      const runs = await page.watch("runs");
      const budgets = await page.watch("budgets");
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const older = await readOrThrow(session, "runs");
      await session.hold();
      await session.badFrame("malformed", "store", {
        rows: [{ kind: "runs", key: "r1", value: "a" }],
      });
      await waitFor("the reconnect", () => page.reconnects() === 1);
      // Both reach the tab inside the episode, in the resumed replay before the listening frame.
      await session.bump("live", ["queue"]);
      await session.badFrame("malformed", "store");
      const detail = await page.watch("runs", older);
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([]);
      await session.release();
      await waitFor(
        "the commit's row in both runs subscriptions",
        () => runs.rows.get("r1") === "a" && detail.rows.get("r1") === "a",
      );
      await waitFor("the unlogged re-read", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(page.reconnects()).toBe(1);
      expect(page.throws()).toBe(2);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect([runs.stales, budgets.stales, detail.stales, queue.stales]).toEqual([0, 0, 0, 1]);
    },
    CASE_MS,
  );

  it(
    "lost track: a changed frame on another epoch gives one reconnect and one catch-up",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { supported });
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.badFrame("other-epoch", "store", {
        rows: [{ kind: "runs", key: "r1", value: "a" }],
      });
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(page.reconnects()).toBe(1);
      expect(page.throws()).toBe(1);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );
}

describe.skipIf(!fixture || !sharedWorker)("the feed's recovery paths over a SharedWorker", () => {
  lostTrackRows(true);

  it(
    "lost track: a malformed frame retained as lost makes a tab attaching then reconnect once",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const first = open(session, { forwardReconnect: false });
      await waitFor("the listening frame", () => listenings(first) === 1);
      const read = await readOrThrow(session, "runs");
      await session.badFrame("malformed", "store", {
        rows: [{ kind: "runs", key: "r1", value: "a" }],
      });
      await waitFor("the bad frame", () => first.throws() === 1);
      const second = open(session);
      const runs = await second.watch("runs", read);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(second.frames[0]?.data).toContain('"lost":true');
      expect(second.reconnects()).toBe(1);
      expect(second.throws()).toBe(1);
      expect((await session.state()).connects).toBe(2);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
    },
    CASE_MS,
  );

  async function attachBetweenConnections(commitAfterRead: boolean) {
    const session = await createFeedSession(fixtureUrl(), [{ ...STORE, rev: 98 }]);
    const first = open(session);
    await waitFor("the listening frame", () => listenings(first) === 1);
    await session.hold();
    await session.drop(true);
    await waitFor("the connection's end", () =>
      first.events.some((e) => e.kind === "state" && e.from === "open"),
    );
    await session.commit("store", [{ kind: "runs", key: "r99", value: "a" }]);
    await session.commit("store", [{ kind: "runs", key: "r100", value: "b" }]);
    const second = open(session);
    const runs = await second.watch("runs");
    if (commitAfterRead) {
      await session.commit("store", [{ kind: "runs", key: "r101", value: "c" }]);
    }
    await sleep(QUIET_MS);
    expect(listenings(second)).toBe(0);
    await session.release();
    await waitFor("the resumed connection's listening frame", () => listenings(second) === 1);
    return { session, second, runs };
  }

  it(
    "attach between connections: nothing retained, a seed at 100 and a resumed replay up to 100 give no lost track and no catch-up",
    async () => {
      const { session, second, runs } = await attachBetweenConnections(false);
      await sleep(QUIET_MS);
      expect(hellos(second).at(-1)?.resumed).toBe(true);
      expect(changedFrames(second)).toBe(2);
      expect([second.reconnects(), second.throws(), runs.stales]).toEqual([0, 0, 0]);
      expect(await queries(session)).toEqual([]);
      const third = open(session);
      await waitFor("the retained frame", () => listenings(third) === 1);
      expect([third.reconnects(), third.throws()]).toEqual([0, 0]);
    },
    CASE_MS,
  );

  it(
    "attach between connections: with 101 in the resumed replay, one catch-up from 100",
    async () => {
      const { session, second, runs } = await attachBetweenConnections(true);
      await waitFor("the commit's row", () => runs.rows.get("r101") === "c");
      await sleep(QUIET_MS);
      expect([second.reconnects(), second.throws(), runs.stales]).toEqual([0, 0, 0]);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
    },
    CASE_MS,
  );

  it(
    "newer build: the declared kinds apply, onWire is called, nothing is lost track, and the retained frame folds every name for the tab to drop",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      await session.newerBuild();
      const read = await readOrThrow(session, "runs");
      const first = open(session);
      const runs = await first.watch("runs", read);
      await waitFor("the listening frame", () => listenings(first) === 1);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      const second = open(session);
      const detail = await second.watch("runs", read);
      await waitFor("the second tab's row", () => detail.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      const retained = JSON.parse(second.frames[0]?.data ?? "null") as {
        sources: { source: string; floors: Record<string, number> }[];
      };
      expect(retained.sources.map((s) => [s.source, s.floors])).toEqual([
        ["store", { runs: 101, zz_newer_kind: 101 }],
        ["zz_newer_source", { zz_newer_only: 1 }],
      ]);
      expect([first.wires, second.wires]).toEqual([[session.wire], [session.wire]]);
      expect([first.reconnects(), first.throws(), second.reconnects(), second.throws()]).toEqual([
        0, 0, 0, 0,
      ]);
      expect(await queries(session)).toEqual([
        ["store", 100, ["runs"], "rows"],
        ["store", 100, ["runs"], "rows"],
      ]);
    },
    CASE_MS,
  );

  it(
    "wire kept: after a reopen's frame, a tab attaching receives the earlier wire",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE, ACCOUNTS]);
      const first = open(session);
      await waitFor("the listening frame", () => listenings(first) === 1);
      await session.reopen("store", 50);
      await waitFor("the reopen's frame", () => listenings(first) === 2);
      const second = open(session);
      await waitFor("the retained frame", () => listenings(second) === 1);
      expect((JSON.parse(second.frames[0]?.data ?? "null") as { wire: string }).wire).toBe(
        session.wire,
      );
      expect(second.wires).toEqual([session.wire]);
    },
    CASE_MS,
  );

  it(
    "unlogged churn: 20 Bumps give one stale(), 20 more during its re-read give none, and a seed at the floor gives none",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "read");
      await session.bump("live", ["queue"], 20);
      await paused.reached;
      await session.bump("live", ["queue"], 20);
      await waitFor("the 40 frames", () => changedFrames(page) === 40);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      paused.release();
      await waitFor("the re-read's seed", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      expect((await session.state()).reads).toEqual({ queue: 2 });
    },
    CASE_MS,
  );

  it(
    "unlogged churn: a re-read's seed below the post-burst floor gives exactly one more stale() after coalesceMs",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "seed");
      await session.bump("live", ["queue"], 20);
      await paused.reached;
      await session.bump("live", ["queue"], 20);
      await waitFor("the 40 frames", () => changedFrames(page) === 40);
      const releasedAt = performance.now();
      paused.release();
      await waitFor("the second re-read's seed", () => queue.seeds === 2);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
      expect((queue.staleAt[1] ?? 0) - releasedAt).toBeGreaterThanOrEqual(45);
    },
    CASE_MS,
  );

  it(
    "unlogged churn: a re-read that rejects with a mark set gives one more round after the first backoff wait",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session, { failedRead: "reject", random: () => 1 });
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "read");
      await session.bump("live", ["queue"], 20);
      await paused.reached;
      await session.readFail(1, "unavailable", "queue");
      await session.bump("live", ["queue"]);
      await waitFor("the 21 frames", () => changedFrames(page) === 21);
      const releasedAt = performance.now();
      paused.release();
      await waitFor("the retry's seed", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
      expect((queue.staleAt[1] ?? 0) - releasedAt).toBeGreaterThanOrEqual(490);
    },
    CASE_MS,
  );

  it(
    "re-read in flight: an unlogged source's epoch change then 20 Bumps give one stale(), and a seed at the last Bump's floor gives none",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "read");
      await session.reopen("live", 3);
      await paused.reached;
      await session.bump("live", ["queue"], 20);
      await waitFor("the 20 frames", () => changedFrames(page) === 20);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      paused.release();
      await waitFor("the re-read's seed", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
    },
    CASE_MS,
  );

  it(
    "re-read in flight: an unlogged source's epoch change then 20 Bumps, and a seed below their floor gives one more stale()",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "seed");
      await session.reopen("live", 3);
      await paused.reached;
      await session.bump("live", ["queue"], 20);
      await waitFor("the 20 frames", () => changedFrames(page) === 20);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      paused.release();
      await waitFor("the second re-read's seed", () => queue.seeds === 2);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
    },
    CASE_MS,
  );

  it(
    "re-read in flight: a full answer then 20 changed frames give one stale(), and a seed below the raised floor gives one catch-up from the seed",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.changesHold(true);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the held catch-up", async () => (await session.state()).heldChanges === 1);
      await session.trim("store");
      const paused = page.pause(runs, "seed");
      await session.changesHold(false);
      await paused.reached;
      await session.bump("store", ["runs"], 19);
      await session.commit("store", [{ kind: "runs", key: "r2", value: "b" }]);
      await waitFor("the 21 frames", () => changedFrames(page) === 21);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "full"]]);
      paused.release();
      await waitFor("the caught-up row", () => runs.rows.get("r2") === "b");
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      expect(await queries(session)).toEqual([
        ["store", 100, ["runs"], "full"],
        ["store", 101, ["runs"], "rows"],
      ]);
    },
    CASE_MS,
  );

  it(
    "re-read in flight: a rejected re-read with frames meanwhile gives one retry stale() when its 500 ms wait ends",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session, { failedRead: "reject", random: () => 1 });
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "read");
      await session.reopen("live", 3);
      await paused.reached;
      await session.readFail(1, "unavailable", "queue");
      await session.bump("live", ["queue"], 3);
      await waitFor("the 3 frames", () => changedFrames(page) === 3);
      const releasedAt = performance.now();
      paused.release();
      await waitFor("the retry's seed", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
      expect((queue.staleAt[1] ?? 0) - releasedAt).toBeGreaterThanOrEqual(490);
    },
    CASE_MS,
  );

  it(
    "re-read in flight: a rejected re-read with no frames gives none until a listening frame at the old floor, which gives one",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session, { failedRead: "reject", random: () => 1 });
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.readFail(1, "unavailable", "queue");
      await session.bump("live", ["queue"]);
      await waitFor("the failed re-read", async () => (await session.state()).reads["queue"] === 2);
      await sleep(1500);
      expect(queue.stales).toBe(1);
      await session.drop(true);
      await waitFor("the re-read's seed", () => queue.seeds === 1);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
      expect(listenings(page)).toBe(2);
      expect((await session.state()).sources[0]?.floors).toEqual({ queue: 8 });
    },
    CASE_MS,
  );

  it("failed re-read backs off: under a Bump every 10 ms for 10 s, 5 stale() calls at 0.999 and at most 6 at 0.5, rejected or resolved with no seed", async () => {
    async function run(random: number, failedRead: "reject" | "resolve"): Promise<number> {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session, { failedRead, random: () => random });
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.readFail(100, "unavailable", "queue");
      await session.churn("live", ["queue"], 10, 10_000);
      await sleep(10_300);
      return queue.stales;
    }
    const counts = await Promise.all([
      run(0.999, "reject"),
      run(0.999, "resolve"),
      run(0.5, "reject"),
      run(0.5, "resolve"),
    ]);
    expect(counts.slice(0, 2)).toEqual([5, 5]);
    for (const n of counts.slice(2)) {
      expect(n).toBeGreaterThanOrEqual(5);
      expect(n).toBeLessThanOrEqual(6);
    }
  }, 40_000);

  it(
    "one end condition: a screen's own seed below the floor during a re-read starts nothing until it settles, then one more stale() after coalesceMs",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "settle");
      await session.bump("live", ["queue"]);
      await paused.reached;
      const screen = await readOrThrow(session, "queue");
      await session.bump("live", ["queue"], 2);
      await waitFor("the 3 frames", () => changedFrames(page) === 3);
      queue.subscription.seed(screen.headers);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      const releasedAt = performance.now();
      paused.release();
      await waitFor("the second re-read's seed", () => queue.seeds === 2);
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(2);
      expect((queue.staleAt[1] ?? 0) - releasedAt).toBeGreaterThanOrEqual(45);
    },
    CASE_MS,
  );

  it(
    "one end condition: a screen's own seed at the floor during a re-read gives none at the settling",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [LIVE]);
      const page = open(session);
      const queue = await page.watch("queue");
      await waitFor("the listening frame", () => listenings(page) === 1);
      const paused = page.pause(queue, "settle");
      await session.bump("live", ["queue"]);
      await paused.reached;
      await session.bump("live", ["queue"], 2);
      await waitFor("the 3 frames", () => changedFrames(page) === 3);
      await page.reread(queue);
      paused.release();
      await sleep(QUIET_MS);
      expect(queue.stales).toBe(1);
      expect((await session.state()).reads).toEqual({ queue: 3 });
    },
    CASE_MS,
  );

  it(
    "rules: subscriptions of one kind seeded at 100 and 200 both show the commit at 150 after one catch-up from 100",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      await session.hold();
      const list = await readOrThrow(session, "runs");
      await session.bump("store", ["budgets"], 49);
      await session.commit("store", [{ kind: "runs", key: "r150", value: "a" }]);
      await session.bump("store", ["budgets"], 50);
      const detail = await readOrThrow(session, "runs");
      await session.commit("store", [{ kind: "runs", key: "r201", value: "b" }]);
      const page = open(session);
      const atList = await page.watch("runs", list);
      const atDetail = await page.watch("runs", detail);
      await session.release();
      await waitFor("both subscriptions' rows", () =>
        [atList, atDetail].every((w) => w.rows.get("r150") === "a" && w.rows.get("r201") === "b"),
      );
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect([atList.stales, atDetail.stales]).toEqual([0, 0]);
    },
    CASE_MS,
  );

  it(
    "rules: after an epoch change one kind's re-read fails while the other re-seeds, and no catch-up pairs the first's revision with the new epoch",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { random: () => 1 });
      const runs = await page.watch("runs");
      const budgets = await page.watch("budgets");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.readFail(1, "unavailable", "runs");
      await session.reopen("store", 50);
      await waitFor("the other kind's re-read", () => budgets.seeds === 1);
      await session.commit("store", [
        { kind: "runs", key: "r1", value: "a" },
        { kind: "budgets", key: "b1", value: "1" },
      ]);
      await waitFor(
        "both rows",
        () => runs.rows.get("r1") === "a" && budgets.rows.get("b1") === "1",
      );
      await sleep(QUIET_MS);
      const state = await session.state();
      const epoch = await epochOf(session, "store");
      expect(state.queries.map((q) => [q.epoch, q.since, q.kinds])).toEqual([
        [epoch, 50, ["budgets"]],
      ]);
      expect([runs.stales, budgets.stales]).toEqual([2, 1]);
    },
    CASE_MS,
  );

  it(
    "a catch-up answered maintenance on a quiet source is sent again on the curve and the first answer after it applies",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { random: () => 1 });
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.changesFail(2);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([
        ["store", 100, ["runs"], "maintenance"],
        ["store", 100, ["runs"], "maintenance"],
        ["store", 100, ["runs"], "rows"],
      ]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "a catch-up answered maintenance waits on the curve, frames only mark, and a listening frame ends the wait at once",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { random: () => 1, timing: { baseMs: 3000, capMs: 30_000 } });
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.changesFail(1);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the first catch-up", async () => (await queries(session)).length === 1);
      await session.commit("store", [{ kind: "runs", key: "r2", value: "b" }]);
      await waitFor("the second frame", () => changedFrames(page) === 2);
      await sleep(QUIET_MS);
      expect(await queries(session)).toHaveLength(1);
      const droppedAt = performance.now();
      await session.drop(true);
      await waitFor("the caught-up rows", () => runs.rows.get("r2") === "b");
      expect(performance.now() - droppedAt).toBeLessThan(2000);
      expect(await queries(session)).toEqual([
        ["store", 100, ["runs"], "maintenance"],
        ["store", 100, ["runs"], "rows"],
      ]);
    },
    CASE_MS,
  );

  it(
    "a catch-up answered maintenance, then the reopen's listening frame ends the wait and re-reads the source once",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { random: () => 1, timing: { baseMs: 3000, capMs: 30_000 } });
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.changesFail(5);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the first catch-up", async () => (await queries(session)).length === 1);
      await session.reopen("store", 50);
      await waitFor("the re-read", () => runs.seeds === 1);
      // Past the end of the wait the reopen replaced: no catch-up of the old epoch follows.
      await sleep(3500);
      expect(runs.stales).toBe(1);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "maintenance"]]);
    },
    CASE_MS,
  );
});

describe.skipIf(!fixture)("the feed's recovery paths on the per-tab fallback", () => {
  lostTrackRows(false);

  it(
    "held frames discarded: a revalidation that times out with a commit's frame held ends the connection, and the next listening frame catches the commit up",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      let hang = true;
      const page = open(session, {
        supported: false,
        streamTiming: { revalidateTimeoutMs: 1000 },
        revalidate: () => {
          if (hang) {
            hang = false;
            return new Promise<void>(() => undefined);
          }
          return Promise.resolve();
        },
      });
      const runs = await page.watch("runs");
      await waitFor("the first revalidation", () =>
        page.events.some((e) => e.kind === "revalidate"),
      );
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      const discarded = page.events.find((e) => e.kind === "held_discarded");
      expect(discarded).toMatchObject({ cause: "hold_timeout" });
      // The first connection's listening frame was held and discarded with the commit's frame.
      expect(listenings(page)).toBe(1);
      expect(hellos(page)).toHaveLength(2);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );
});
