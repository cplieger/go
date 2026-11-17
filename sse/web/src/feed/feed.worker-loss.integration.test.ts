import { afterEach, describe, expect, it } from "vitest";
import { createFeedSession } from "../test-helpers/feed-client.js";
import workerUrl from "../test-helpers/worker-entry.ts?sharedworker&url";
import {
  CASE_MS,
  QUIET_MS,
  STORE,
  fixtureUrl,
  listenings,
  pageOpener,
  queries,
  sleep,
  waitFor,
} from "./test-helpers/feed-cases.js";

const fixture = import.meta.env["SSE_FIXTURE"];
const sharedWorker = typeof SharedWorker !== "undefined";
const HB = 300;

const { open, detachAll } = pageOpener(workerUrl);
afterEach(detachAll);

describe.skipIf(!fixture || !sharedWorker)("the feed when its worker is lost", () => {
  it(
    "a tab whose every respawned worker stays silent reaches its per-tab stream after two spawns and shows the commit",
    async () => {
      const session = await createFeedSession(fixtureUrl(), [STORE]);
      const page = open(session, {
        heartbeatMs: HB,
        freshRespawn: true,
        respawnQuery: { silent: "1" },
      });
      const runs = await page.watch("runs");
      await waitFor("the listening frame", () => listenings(page) === 1);
      // The first worker's heartbeats reach the tab before it goes silent, so only the respawns
      // count as spawns that never sent one.
      await sleep(2 * HB);
      page.send({ type: "test:silence" });
      await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
      await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
      await sleep(QUIET_MS);
      expect(page.attachment.mode()).toBe("fallback");
      expect(page.workers).toHaveLength(3);
      expect(page.events.filter((e) => e.kind === "worker_unavailable")).toEqual([
        { kind: "worker_unavailable", cause: "silent" },
      ]);
      expect(page.frames.filter((f) => f.type === "changed")).toEqual([]);
      expect(await queries(session)).toEqual([["store", 100, ["runs"], "rows"]]);
      expect(runs.stales).toBe(0);
    },
    CASE_MS,
  );

  it(
    "a tab attaches and receives a frame with crypto.randomUUID deleted and isSecureContext false",
    async () => {
      const uuid = Object.getOwnPropertyDescriptor(Crypto.prototype, "randomUUID");
      const secure = Object.getOwnPropertyDescriptor(globalThis, "isSecureContext");
      try {
        delete (Crypto.prototype as { randomUUID?: unknown }).randomUUID;
        Object.defineProperty(globalThis, "isSecureContext", { value: false, configurable: true });
        expect([typeof crypto.randomUUID, globalThis.isSecureContext]).toEqual([
          "undefined",
          false,
        ]);
        const session = await createFeedSession(fixtureUrl(), [STORE]);
        const page = open(session);
        const runs = await page.watch("runs");
        await waitFor("the listening frame", () => listenings(page) === 1);
        await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
        await waitFor("the commit's row", () => runs.rows.get("r1") === "a");
        expect(page.attachment.mode()).toBe("worker");
        expect(page.frames.map((f) => f.type)).toEqual(["listening", "changed"]);
      } finally {
        if (uuid !== undefined) {
          Object.defineProperty(Crypto.prototype, "randomUUID", uuid);
        }
        if (secure === undefined) {
          delete (globalThis as { isSecureContext?: boolean }).isSecureContext;
        } else {
          Object.defineProperty(globalThis, "isSecureContext", secure);
        }
      }
    },
    CASE_MS,
  );
});
