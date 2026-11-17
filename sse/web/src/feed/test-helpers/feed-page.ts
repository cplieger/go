import type { LifecycleEvent } from "../../lifecycle.js";
import type { Frame, RevalidateContext } from "../../stream.js";
import { createStream } from "../../stream.js";
import { type TabAttachment, type TabFallback, attachToWorker } from "../../tab.js";
import type { FeedRow, FeedSession } from "../../test-helpers/feed-client.js";
import type { TimingConfig } from "../../timing.js";
import { createVersionMap } from "../../versions.js";
import { createFeed } from "../feed.js";
import type { CatchUp, ChangesAnswer, Feed, FeedOptions, Subscription } from "../types.js";
import { FEED_VERSION } from "../version.js";

/** Every kind's row is the fixture's `{key, value}`. */
type Rows = Record<string, FeedRow>;

/** A full read of one kind: its rows and the headers that seed a subscription. */
export interface KindRead {
  readonly rows: readonly FeedRow[];
  readonly headers: Headers;
}

/** Where a held re-read waits: before its read, between its read and its seed, or after its seed. */
type PauseAt = "read" | "seed" | "settle";

interface Pause {
  /** Resolves once the re-read reached the pause. */
  readonly reached: Promise<void>;
  release(): void;
}

/** One `watch`: the rows its sink holds and what the feed asked of it. */
interface Watched {
  readonly kind: string;
  readonly rows: Map<string, string>;
  /** `stale()` calls, the first read excluded. */
  stales: number;
  /** `performance.now()` of each `stale()` call. */
  readonly staleAt: number[];
  /** Reads after the first that answered and seeded: the feed's re-reads and the screen's own. */
  seeds: number;
  readonly subscription: Subscription;
}

export interface FeedPage {
  readonly feed: Feed<Rows>;
  readonly attachment: TabAttachment;
  readonly frames: Frame[];
  readonly events: LifecycleEvent[];
  readonly wires: string[];
  /** `reconnect()` calls the feed made. */
  readonly reconnects: () => number;
  readonly workers: SharedWorker[];
  /** Watches the kind with `first`'s headers, or with a full read taken now. */
  watch(kind: string, first?: KindRead): Promise<Watched>;
  /** A read the screen starts for its own reasons: its rows replace the sink's and it seeds. */
  reread(watched: Watched): Promise<void>;
  /** Holds the next `stale()` of the watch at `at` until it is released. */
  pause(watched: Watched, at: PauseAt): Pause;
  /** Throws from `feed.onFrame`, which the stream runtime reports as `frame_rejected`. */
  readonly throws: () => number;
  /** Sends a test-only message to the newest worker this page spawned. */
  send(message: Readonly<Record<string, unknown>>): void;
  /** Stops every watch, then leaves and ends the stream, so a finished test holds no connection. */
  detach(): void;
}

export interface FeedPageOptions {
  readonly session: FeedSession;
  readonly fixtureUrl: string;
  /** Kind to source, as a generated client declares them. */
  readonly kinds: Readonly<Record<string, string>>;
  readonly logged: readonly string[];
  /** `test-helpers/worker-entry.ts?sharedworker&url`, which only a test file can import. */
  readonly workerUrl: string;
  /** Names the SharedWorker: pages with one name share a worker. */
  readonly worker: string;
  /** `false` takes the per-tab fallback. */
  readonly supported?: boolean;
  /** Worker URL query options of `worker-entry.ts` beyond the session. */
  readonly query?: Readonly<Record<string, string>>;
  readonly coalesceMs?: number;
  readonly random?: () => number;
  /** The feed's backoff timing. */
  readonly timing?: FeedOptions<Rows>["timing"];
  /** How a `stale()` whose read failed settles: rejected, or resolved with no seed (`ok: false`). */
  readonly failedRead?: "reject" | "resolve";
  /** `false` counts the feed's `reconnect()` calls without reconnecting the link. */
  readonly forwardReconnect?: boolean;
  /** The per-tab fallback stream's timing and revalidation. */
  readonly streamTiming?: Partial<TimingConfig>;
  readonly revalidate?: (ctx: RevalidateContext) => Promise<void>;
  /** Worker URL query options of every respawned worker, on top of `query`. */
  readonly respawnQuery?: Readonly<Record<string, string>>;
  /** The tab's and the worker's heartbeat; the watchdog fires after three silent beats. */
  readonly heartbeatMs?: number;
  /**
   * Each respawn takes a new worker name. The OS ending a worker frees its name, while a
   * silenced worker is still alive, so a respawn under its name would reach it again.
   */
  readonly freshRespawn?: boolean;
}

/** A full read of the kind from the session, or null when it fails. */
export async function readKind(session: FeedSession, kind: string): Promise<KindRead | null> {
  try {
    const response = await fetch(`${session.base}/read?kind=${encodeURIComponent(kind)}`);
    if (!response.ok) {
      return null;
    }
    const body = (await response.json()) as { rows: FeedRow[] };
    return { rows: body.rows, headers: response.headers };
  } catch {
    return null;
  }
}

function catchUpOf(session: FeedSession): CatchUp<Rows> {
  return async (q, signal) => {
    const params = new URLSearchParams({
      source: q.source,
      epoch: q.epoch,
      since: String(q.since),
      kinds: q.kinds.join(","),
    });
    let response: Response;
    try {
      response = await fetch(`${session.base}/changes?${params.toString()}`, { signal });
    } catch {
      return { ok: false, failure: "retry" };
    }
    if (response.status === 503 || response.status === 429) {
      return { ok: false, failure: "retry" };
    }
    if (!response.ok) {
      return { ok: false, failure: "refused" };
    }
    return { ok: true, answer: (await response.json()) as ChangesAnswer<Rows> };
  };
}

/** A tab of an app on the change feed: `createFeed` fed by `attachToWorker` over a fixture session. */
export function feedPage(opts: FeedPageOptions): FeedPage {
  const frames: Frame[] = [];
  const events: LifecycleEvent[] = [];
  const wires: string[] = [];
  const workers: SharedWorker[] = [];
  let reconnects = 0;
  let throws = 0;
  let attachment: TabAttachment | null = null;
  const options: FeedOptions<Rows> = {
    feedVersion: FEED_VERSION,
    kinds: opts.kinds,
    logged: opts.logged,
    catchUp: catchUpOf(opts.session),
    onWire: (wire) => wires.push(wire),
    reconnect() {
      reconnects++;
      if (opts.forwardReconnect !== false) {
        attachment?.reconnect();
      }
    },
    ...(opts.coalesceMs === undefined ? {} : { coalesceMs: opts.coalesceMs }),
    ...(opts.random === undefined ? {} : { random: opts.random }),
    ...(opts.timing === undefined ? {} : { timing: opts.timing }),
  };
  const feed = createFeed(options);
  const onFrame = (frame: Frame): void => {
    frames.push(frame);
    try {
      feed.onFrame(frame);
    } catch (e: unknown) {
      throws++;
      throw e;
    }
  };
  const onLifecycle = (event: LifecycleEvent): void => {
    events.push(event);
  };
  const query = new URLSearchParams({
    fixture: opts.fixtureUrl,
    feed: opts.session.id,
    retain: "1",
    ...(opts.heartbeatMs === undefined ? {} : { heartbeat: String(opts.heartbeatMs) }),
    ...opts.query,
  });
  const urlOf = (q: URLSearchParams): string =>
    `${opts.workerUrl}${opts.workerUrl.includes("?") ? "&" : "?"}${q.toString()}`;
  const url = urlOf(query);
  const respawnUrl = urlOf(
    new URLSearchParams({ ...Object.fromEntries(query), ...opts.respawnQuery }),
  );
  attachment = attachToWorker({
    ...(opts.supported === undefined ? {} : { supported: opts.supported }),
    ...(opts.heartbeatMs === undefined ? {} : { heartbeatMs: opts.heartbeatMs }),
    spawn() {
      const name =
        opts.freshRespawn === true && workers.length > 0
          ? `${opts.worker}-respawn-${String(workers.length)}`
          : opts.worker;
      const worker = new SharedWorker(workers.length > 0 ? respawnUrl : url, {
        name,
        type: "module",
      });
      workers.push(worker);
      return worker;
    },
    fallback(): TabFallback {
      const versions = createVersionMap();
      const headers: Record<string, string> = {};
      const stream = createStream({
        url: `${opts.session.base}/events`,
        versions,
        headers,
        onFrame,
        onLifecycle,
        revalidate: opts.revalidate ?? (() => Promise.resolve()),
        ...(opts.streamTiming === undefined ? {} : { timing: opts.streamTiming }),
      });
      return { stream, versions, headers };
    },
    onFrame,
    onLifecycle,
  });
  const live = attachment;
  const refills = new Map<Watched, () => Promise<void>>();
  const pauses = new Map<Watched, { at: PauseAt; reach: () => void; released: Promise<void> }>();
  const subscriptions: Subscription[] = [];
  return {
    feed,
    attachment: live,
    frames,
    events,
    wires,
    reconnects: () => reconnects,
    workers,
    async watch(kind, given) {
      const first = given ?? (await readKind(opts.session, kind));
      if (first === null) {
        throw new Error(`the first read of ${kind} failed`);
      }
      const rows = new Map(first.rows.map((r) => [r.key, r.value]));
      let subscription: Subscription | null = null;
      const watched: Watched = {
        kind,
        rows,
        stales: 0,
        staleAt: [],
        seeds: 0,
        get subscription(): Subscription {
          if (subscription === null) {
            throw new Error("watch has not returned");
          }
          return subscription;
        },
      };
      subscription = feed.watch(
        kind,
        {
          upsert(upserts) {
            for (const r of upserts) {
              rows.set(r.key, r.value);
            }
          },
          remove(keys) {
            for (const k of keys) {
              rows.delete(k);
            }
          },
          async stale() {
            watched.stales++;
            watched.staleAt.push(performance.now());
            const pause = pauses.get(watched);
            pauses.delete(watched);
            const wait = async (at: PauseAt): Promise<void> => {
              if (pause?.at === at) {
                pause.reach();
                await pause.released;
              }
            };
            await wait("read");
            const again = await readKind(opts.session, kind);
            await wait("seed");
            if (again === null) {
              if (opts.failedRead === "reject") {
                throw new Error(`the re-read of ${kind} failed`);
              }
              return;
            }
            apply(again);
            await wait("settle");
          },
        },
        first.headers,
      );
      subscriptions.push(subscription);
      function apply(read: KindRead): void {
        rows.clear();
        for (const r of read.rows) {
          rows.set(r.key, r.value);
        }
        subscription?.seed(read.headers);
        watched.seeds++;
      }
      async function refill(): Promise<void> {
        const again = await readKind(opts.session, kind);
        if (again !== null) {
          apply(again);
        }
      }
      refills.set(watched, refill);
      return watched;
    },
    async reread(watched) {
      const refill = refills.get(watched);
      if (refill === undefined) {
        throw new Error("reread of a kind this page does not watch");
      }
      await refill();
    },
    pause(watched, at) {
      let reach = (): void => undefined;
      let release = (): void => undefined;
      const reached = new Promise<void>((resolve) => {
        reach = resolve;
      });
      const released = new Promise<void>((resolve) => {
        release = resolve;
      });
      pauses.set(watched, { at, reach, released });
      return { reached, release };
    },
    throws: () => throws,
    send(message) {
      workers[workers.length - 1]?.port.postMessage(message);
    },
    detach() {
      for (const s of subscriptions.splice(0)) {
        s.stop();
      }
      pauses.clear();
      live.detach("logout");
    },
  };
}
