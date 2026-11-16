import { afterEach, describe, expect, it } from "vitest";
import { createFeedSession } from "../test-helpers/feed-client.js";
import { FIXTURE_PROXY_PREFIX } from "../test-helpers/fixture-client.js";
import workerUrl from "../test-helpers/worker-entry.ts?sharedworker&url";
import { type FeedPage, feedPage } from "./test-helpers/feed-page.js";

const fixture = import.meta.env["SSE_FIXTURE"];
const available = Boolean(fixture) && typeof SharedWorker !== "undefined";

async function waitFor(what: string, pred: () => boolean | Promise<boolean>): Promise<void> {
  const deadline = performance.now() + 8000;
  while (!(await pred())) {
    if (performance.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

describe.skipIf(!available)("the change feed over a worker against the sse fixture", () => {
  const pages: FeedPage[] = [];

  afterEach(() => {
    for (const p of pages.splice(0)) {
      p.detach();
    }
  });

  it("a commit reaches a watching tab through one catch-up", async () => {
    const fixtureUrl = `${location.origin}${FIXTURE_PROXY_PREFIX}`;
    const session = await createFeedSession(fixtureUrl, [
      { name: "store", kinds: ["runs"], log: true, rev: 100 },
    ]);
    const page = feedPage({
      session,
      fixtureUrl,
      kinds: { runs: "store" },
      logged: ["store"],
      workerUrl,
      worker: `feed-${session.id}`,
    });
    pages.push(page);
    const runs = await page.watch("runs");
    await waitFor("the listening frame", () => page.frames.some((f) => f.type === "listening"));
    expect(page.wires).toEqual([session.wire]);

    await session.commit("store", [{ kind: "runs", key: "r1", value: "a" }]);
    await waitFor("the commit's row", () => runs.rows.get("r1") === "a");

    const state = await session.state();
    expect(state.queries.map((q) => [q.source, q.since, q.kinds, q.answer])).toEqual([
      ["store", 100, ["runs"], "rows"],
    ]);
    expect(state.reads).toEqual({ runs: 1 });
    expect(runs.stales).toBe(0);
    expect(page.attachment.mode()).toBe("worker");
  });
});
