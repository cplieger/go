import type { LifecycleEvent } from "../../lifecycle.js";
import type { FeedSession, FeedSourceSpec } from "../../test-helpers/feed-client.js";
import { FIXTURE_PROXY_PREFIX } from "../../test-helpers/fixture-client.js";
import {
  type FeedPage,
  type FeedPageOptions,
  type KindRead,
  feedPage,
  readKind,
} from "./feed-page.js";

// Long enough for coalesceMs (50) and a catch-up's round trip: a request the feed was going to
// send has been sent by then, so an empty query log after it means none follows.
export const QUIET_MS = 400;
export const CASE_MS = 20_000;

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function waitFor(
  what: string,
  pred: () => boolean | Promise<boolean>,
  timeoutMs = 10_000,
): Promise<void> {
  const deadline = performance.now() + timeoutMs;
  while (!(await pred())) {
    if (performance.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await sleep(20);
  }
}

export const fixtureUrl = (): string => `${location.origin}${FIXTURE_PROXY_PREFIX}`;

export const STORE: FeedSourceSpec = {
  name: "store",
  kinds: ["runs", "budgets"],
  log: true,
  rev: 100,
};
export const ACCOUNTS: FeedSourceSpec = { name: "accounts", kinds: ["users"], log: true, rev: 10 };
export const LIVE: FeedSourceSpec = { name: "live", kinds: ["queue"], rev: 7 };
const KINDS = { runs: "store", budgets: "store", users: "accounts", queue: "live" } as const;
const LOGGED = ["store", "accounts"];

export type OpenOptions = Partial<Omit<FeedPageOptions, "session">>;

/**
 * Opens feed pages on `workerUrl` unless a page names its own, and detaches every one of them at
 * `detachAll`.
 */
export function pageOpener(workerUrl: string): {
  open(session: FeedSession, extra?: OpenOptions): FeedPage;
  detachAll(): void;
} {
  const pages: FeedPage[] = [];
  return {
    open(session, extra = {}) {
      const page = feedPage({
        kinds: KINDS,
        logged: LOGGED,
        worker: `feed-${session.id}`,
        workerUrl,
        ...extra,
        session,
      });
      pages.push(page);
      return page;
    },
    detachAll() {
      for (const p of pages.splice(0)) {
        p.detach();
      }
    },
  };
}

export const listenings = (page: FeedPage): number =>
  page.frames.filter((f) => f.type === "listening").length;

export const hellos = (page: FeedPage): Extract<LifecycleEvent, { kind: "hello" }>[] =>
  page.events.filter((e): e is Extract<LifecycleEvent, { kind: "hello" }> => e.kind === "hello");

/** The session's catch-ups as `[source, since, kinds, answer]`. */
export async function queries(session: FeedSession): Promise<[string, number, string[], string][]> {
  const state = await session.state();
  return state.queries.map((q) => [q.source, q.since, q.kinds, q.answer]);
}

export async function readOrThrow(session: FeedSession, kind: string): Promise<KindRead> {
  const r = await readKind(session, kind);
  if (r === null) {
    throw new Error(`read of ${kind} failed`);
  }
  return r;
}
