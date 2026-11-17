import { afterEach, describe, expect, it } from "vitest";
import { createFeedSession } from "../test-helpers/feed-client.js";
import workerUrl from "../test-helpers/worker-entry.ts?sharedworker&url";
import {
  ACCOUNTS,
  CASE_MS,
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

const fixture = import.meta.env["SSE_FIXTURE"];
const sharedWorker = typeof SharedWorker !== "undefined";
const HB = 300;
const WATCHDOG_MS = 3 * HB;

const { open, detachAll } = pageOpener(workerUrl);
afterEach(detachAll);

/** The rows that reach both the worker setup and the per-tab fallback. */
function sharedRows(supported: boolean): void {
  it(
    "page load: a commit between the read and the join gives one catch-up from 100",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      await session.hold();
      const page = open(session, { supported });
      const runs = await page.watch("runs");
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await session.release();
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
      expect(page.attachment.mode()).toBe(supported ? "worker" : "fallback");
    },
    CASE_MS,
  );

  it(
    "page load: with no commit between the read and the join, no catch-up",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      await session.hold();
      const page = open(session, { supported });
      const runs = await page.watch("runs");
      await session.release();
      await waitFor("the listening frame", () => listenings(page) === 1);
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "hello that did not resume: a commit to A gives one catch-up naming only A and B sends nothing",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { supported });
      const runs = await page.watch("runs");
      const budgets = await page.watch("budgets");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.hold();
      await session.drop(false);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await session.release();
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(hellos(page).at(-1)?.verdict).toBe("gap_floor");
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect([runs.stales, budgets.stales]).toEqual([0, 0]);
    },
    CASE_MS,
  );

  it(
    "read in flight: answered after the new listening frame, one catch-up from 100",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { supported });
      await waitFor("the listening frame", () => listenings(page) === 1);
      const read = await readOrThrow(session, "runs");
      await session.hold();
      await session.drop(false);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await session.release();
      await waitFor("the new listening frame", () => listenings(page) === 2);
      const runs = await page.watch("runs", read);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "read in flight: answered before the new listening frame, one catch-up at the frame",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { supported });
      await waitFor("the listening frame", () => listenings(page) === 1);
      const read = await readOrThrow(session, "runs");
      await session.hold();
      await session.drop(false);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      const runs = await page.watch("runs", read);
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([]);
      await session.release();
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );
}

describe.skipIf(!fixture || !sharedWorker)("the feed's path table over a SharedWorker", () => {
  sharedRows(true);

  it(
    "attach to a live worker: a frame at 101 then a tab seeded at 100 gives one catch-up from 100",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const first = open(session);
      await waitFor("the listening frame", () => listenings(first) === 1);
      const read = await readOrThrow(session, "runs");
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the changed frame", () => first.frames.some((f) => f.type === "changed"));
      const second = open(session);
      const runs = await second.watch("runs", read);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(second.workers).toHaveLength(1);
      expect((await session.state()).connects).toBe(1);
    },
    CASE_MS,
  );

  it(
    "attach to a live worker: a tab seeded at the frame's revision sends nothing",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const first = open(session);
      await waitFor("the listening frame", () => listenings(first) === 1);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the changed frame", () => first.frames.some((f) => f.type === "changed"));
      const second = open(session);
      const runs = await second.watch("runs");
      await waitFor("the retained frame", () => listenings(second) === 1);
      await sleep(QUIET_MS);
      expect(runs.rows.get("r1")).toBe("a");
      expect(await queries(session)).toEqual([]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "resumed hello: no catch-up",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.drop(true);
      await waitFor("the reconnect's listening frame", () => listenings(page) === 2);
      await sleep(QUIET_MS);
      expect(hellos(page).at(-1)?.resumed).toBe(true);
      expect(await queries(session)).toEqual([]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "worker restart: a silent worker's tab shows the first kind's commit after one catch-up from its own watermark",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, { heartbeatMs: HB, freshRespawn: true });
      const runs = await page.watch("runs");
      const budgets = await page.watch("budgets");
      await waitFor("the listening frame", () => listenings(page) === 1);
      page.send({ type: "test:silence" });
      const silencedAt = performance.now();
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      // The second kind's fresh read passes the commit, so a catch-up from the highest
      // watermark would miss the first kind's row.
      await page.reread(budgets);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      const shownAfterMs = performance.now() - silencedAt;
      await sleep(QUIET_MS);
      expect(page.events.filter((e) => e.kind === "worker_dead")).toHaveLength(1);
      expect(page.workers).toHaveLength(2);
      expect(page.attachment.mode()).toBe("worker");
      expect(page.frames.filter((f) => f.type === "changed")).toEqual([]);
      expect(shownAfterMs).toBeLessThan(WATCHDOG_MS + 3000);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect([runs.stales, budgets.stales]).toEqual([0, 0]);
      expect((await session.state()).reads).toEqual({ runs: 1, budgets: 2 });
    },
    CASE_MS,
  );

  it(
    "epoch change across connections: an epoch_changed hello stales each subscription once and their re-reads send nothing more",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      const runs = await page.watch("runs");
      const budgets = await page.watch("budgets");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.restart();
      await waitFor("both re-reads", () => runs.seeds === 1 && budgets.seeds === 1);
      await sleep(QUIET_MS);
      expect(hellos(page).at(-1)?.verdict).toBe("epoch_changed");
      expect([runs.stales, budgets.stales]).toEqual([1, 1]);
      expect(await queries(session)).toEqual([]);
      expect((await session.state()).reads).toEqual({ runs: 2, budgets: 2 });
    },
    CASE_MS,
  );

  it(
    "reopen: one source's subscriptions are staled once, another's keep their watermarks, and the next frame is caught up",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE, ACCOUNTS]);
      const page = open(session);
      const runs = await page.watch("runs");
      const users = await page.watch("users");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.reopen("store", 50);
      await waitFor("the re-read", () => runs.seeds === 1);
      const reopenFrame = page.frames.filter((f) => f.type === "listening")[1];
      expect(reopenFrame?.id).not.toBeNull();
      expect(page.wires).toEqual([session.wire]);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await session.commit("accounts", [{ kind: "users", key: "u1", value: "b" }]);
      await waitFor("the other source's row", () => users.rows.get("u1") === "b");
      await sleep(QUIET_MS);
      expect([runs.stales, users.stales]).toEqual([1, 0]);
      expect(await queries(session)).toEqual([
        ["store", 50, ["runs"], "rows"],
        ["accounts", 10, ["users"], "rows"],
      ]);
    },
    CASE_MS,
  );

  it(
    "seed on another epoch: an older seed received after the reopen's frame is staled once and its re-read is covered",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      await waitFor("the listening frame", () => listenings(page) === 1);
      const older = await readOrThrow(session, "runs");
      await session.reopen("store", 50);
      await waitFor("the reopen's frame", () => listenings(page) === 2);
      const runs = await page.watch("runs", older);
      await waitFor("the re-read", () => runs.seeds === 1);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      expect(await queries(session)).toEqual([]);
    },
    CASE_MS,
  );

  it(
    "seed on another epoch: a newer seed received before the frame is staled once, its re-read waits and the frame covers it",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.hold();
      await session.drop(true);
      await session.reopen("store", 50);
      const runs = await page.watch("runs");
      await waitFor("the re-read", () => runs.seeds === 1);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      await session.release();
      await waitFor("the replayed reopen frame", () => listenings(page) >= 3);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      expect(await queries(session)).toEqual([]);
      expect((await session.state()).reads).toEqual({ runs: 2 });
    },
    CASE_MS,
  );

  it(
    "seed on another epoch: a full answer on another epoch costs one re-read",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session);
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.changesHold(true);
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the held catch-up", async () => (await session.state()).heldChanges === 1);
      await session.hold();
      await session.drop(true);
      await session.reopen("store", 50);
      await session.changesHold(false);
      await waitFor("the re-read", () => runs.seeds === 1);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      await session.release();
      await waitFor("the replayed reopen frame", () => listenings(page) >= 3);
      await sleep(QUIET_MS);
      expect(runs.stales).toBe(1);
      expect(runs.rows.get("r1")).toBe("a");
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "full"]]);
    },
    CASE_MS,
  );

  it(
    "source added: a seed of a source started after the join waits and the start's frame covers it",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [
        STORE,
        { ...ACCOUNTS, pending: true },
      ]);
      const page = open(session);
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.hold();
      await session.drop(true);
      await session.startSource("accounts");
      const users = await page.watch("users");
      await sleep(QUIET_MS);
      expect(users.stales).toBe(0);
      await session.release();
      await waitFor("the start's frame", () => listenings(page) >= 3);
      await sleep(QUIET_MS);
      expect(users.stales).toBe(0);
      expect(await queries(session)).toEqual([]);
      await session.commit("accounts", [{ kind: "users", key: "u1", value: "b" }]);
      await waitFor("the new source's row", () => users.rows.get("u1") === "b");
      expect(await queries(session)).toEqual([["accounts", 10, ["users"], "rows"]]);
    },
    CASE_MS,
  );

  it(
    "source added: a read before the start carries no position, and the start's frame re-reads it once",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [
        STORE,
        { ...ACCOUNTS, pending: true },
      ]);
      const page = open(session);
      const users = await page.watch("users");
      await waitFor("the listening frame", () => listenings(page) === 1);
      await session.startSource("accounts");
      await waitFor("the re-read", () => users.seeds === 1);
      await sleep(QUIET_MS);
      expect(users.stales).toBe(1);
      expect(await queries(session)).toEqual([]);
    },
    CASE_MS,
  );
});

describe.skipIf(!fixture)("the feed's path table on the per-tab fallback", () => {
  sharedRows(false);
});
