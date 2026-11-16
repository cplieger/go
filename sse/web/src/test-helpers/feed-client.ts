/** Typed client for the sse fixture's change-feed sessions (`internal/fixture/feed.go`). */

import { FixtureError } from "./fixture-client.js";

export interface FeedSourceSpec {
  readonly name: string;
  readonly kinds: readonly string[];
  /** A logged source answers catch-ups; an unlogged one only publishes changed frames. */
  readonly log?: boolean;
  /** The source's revision when the session starts. */
  readonly rev?: number;
  /** Declared but not started until `startSource`. */
  readonly pending?: boolean;
}

export interface FeedRow {
  readonly key: string;
  readonly value: string;
}

interface CommitRow {
  readonly kind: string;
  readonly key: string;
  readonly value?: string;
  readonly removed?: boolean;
}

interface FeedQuery {
  readonly source: string;
  readonly epoch: string;
  readonly since: number;
  readonly kinds: string[];
  readonly answer: "rows" | "full" | "maintenance" | "bad_query";
}

interface FeedSessionState {
  readonly reads: Readonly<Record<string, number>>;
  readonly sources: {
    readonly name: string;
    readonly epoch: string;
    readonly rev: number;
    readonly floors: Readonly<Record<string, number>>;
    readonly started: boolean;
  }[];
  readonly queries: FeedQuery[];
  readonly connects: number;
  readonly connected: number;
}

interface Position {
  readonly epoch: string;
  readonly rev: number;
}

export interface FeedSession {
  readonly id: string;
  readonly wire: string;
  /** The session's routes under the fixture URL: `events`, `read`, `changes`. */
  readonly base: string;
  commit(source: string, rows: readonly CommitRow[]): Promise<Position>;
  bump(source: string, kinds: readonly string[], count?: number): Promise<Position>;
  /** A new epoch at `rev`, announced by a listening frame with an id and no wire. */
  reopen(source: string, rev: number): Promise<Position>;
  startSource(source: string): Promise<Position>;
  /** New streams wait before they join the hub until `release`. */
  hold(): Promise<void>;
  release(): Promise<void>;
  /** Ends every stream; without resume each reconnect's hello answers gap_floor. */
  drop(resume: boolean): Promise<void>;
  /** A new hub and a new epoch for every source: each reconnect answers epoch_changed. */
  restart(): Promise<void>;
  badFrame(kind: "malformed" | "other-epoch", source: string): Promise<void>;
  /** The feed format the connect hook writes from the next connection. */
  format(feed: number): Promise<void>;
  /** From the next connection, the listening frame names a kind and a source no tab declared. */
  newerBuild(): Promise<void>;
  /** The next `count` catch-ups answer 503 maintenance. */
  changesFail(count: number): Promise<void>;
  changesHold(on: boolean): Promise<void>;
  readFail(count: number, mode: "reject" | "unavailable"): Promise<void>;
  state(): Promise<FeedSessionState>;
}

async function call<T>(doFetch: typeof fetch, url: string, body?: unknown): Promise<T> {
  const response = await doFetch(url, {
    method: body === undefined ? "GET" : "POST",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? null : JSON.stringify(body),
  });
  if (!response.ok) {
    throw new FixtureError(url, response.status, await response.text());
  }
  return response.status === 204 ? (undefined as T) : ((await response.json()) as T);
}

export async function createFeedSession(
  fixtureUrl: string,
  sources: readonly FeedSourceSpec[],
  wire = "",
  doFetch: typeof fetch = (input, init) => globalThis.fetch(input, init),
): Promise<FeedSession> {
  const created = await call<{ id: string; wire: string }>(
    doFetch,
    `${fixtureUrl}/control/feed/sessions`,
    { sources, wire },
  );
  const control = <T>(verb: string, body: unknown = {}): Promise<T> =>
    call<T>(doFetch, `${fixtureUrl}/control/feed/${created.id}/${verb}`, body);
  return {
    id: created.id,
    wire: created.wire,
    base: `${fixtureUrl}/feed/${created.id}`,
    commit: (source, rows) => control("commit", { source, rows }),
    bump: (source, kinds, count = 1) => control("bump", { source, kinds, count }),
    reopen: (source, rev) => control("reopen", { source, rev }),
    startSource: (source) => control("start-source", { source }),
    hold: () => control("hold"),
    release: () => control("release"),
    drop: (resume) => control("drop", { resume }),
    restart: () => control("restart"),
    badFrame: (kind, source) => control("bad-frame", { kind, source }),
    format: (feed) => control("format", { feed }),
    newerBuild: () => control("newer-build"),
    changesFail: (count) => control("changes-fail", { count }),
    changesHold: (on) => control("changes-hold", { on }),
    readFail: (count, mode) => control("read-fail", { count, mode }),
    state: () => call(doFetch, `${fixtureUrl}/control/feed/${created.id}/state`),
  };
}
