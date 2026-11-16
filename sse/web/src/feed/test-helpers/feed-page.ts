import type { LifecycleEvent } from "../../lifecycle.js";
import type { Frame } from "../../stream.js";
import { createStream } from "../../stream.js";
import { type TabAttachment, type TabFallback, attachToWorker } from "../../tab.js";
import type { FeedRow, FeedSession } from "../../test-helpers/feed-client.js";
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

/** One `watch`: the rows its sink holds and what the feed asked of it. */
interface Watched {
  readonly kind: string;
  readonly rows: Map<string, string>;
  /** `stale()` calls, the first read excluded. */
  stales: number;
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
  /** Sends a test-only message to the newest worker this page spawned. */
  send(message: Readonly<Record<string, unknown>>): void;
  /** Leaves and ends the stream, so a finished test holds no connection on its session. */
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
  let attachment: TabAttachment | null = null;
  const options: FeedOptions<Rows> = {
    feedVersion: FEED_VERSION,
    kinds: opts.kinds,
    logged: opts.logged,
    catchUp: catchUpOf(opts.session),
    onWire: (wire) => wires.push(wire),
    reconnect() {
      reconnects++;
      attachment?.reconnect();
    },
    ...(opts.coalesceMs === undefined ? {} : { coalesceMs: opts.coalesceMs }),
    ...(opts.random === undefined ? {} : { random: opts.random }),
  };
  const feed = createFeed(options);
  const onFrame = (frame: Frame): void => {
    frames.push(frame);
    feed.onFrame(frame);
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
  const url = `${opts.workerUrl}${opts.workerUrl.includes("?") ? "&" : "?"}${query.toString()}`;
  attachment = attachToWorker({
    ...(opts.supported === undefined ? {} : { supported: opts.supported }),
    ...(opts.heartbeatMs === undefined ? {} : { heartbeatMs: opts.heartbeatMs }),
    spawn() {
      const name =
        opts.freshRespawn === true && workers.length > 0
          ? `${opts.worker}-respawn-${String(workers.length)}`
          : opts.worker;
      const worker = new SharedWorker(url, { name, type: "module" });
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
        revalidate: () => Promise.resolve(),
      });
      return { stream, versions, headers };
    },
    onFrame,
    onLifecycle,
  });
  const live = attachment;
  const refills = new Map<Watched, () => Promise<void>>();
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
            await refill();
          },
        },
        first.headers,
      );
      async function refill(): Promise<void> {
        const again = await readKind(opts.session, kind);
        if (again === null) {
          return;
        }
        rows.clear();
        for (const r of again.rows) {
          rows.set(r.key, r.value);
        }
        subscription?.seed(again.headers);
        watched.seeds++;
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
    send(message) {
      workers[workers.length - 1]?.port.postMessage(message);
    },
    detach() {
      live.detach("logout");
    },
  };
}
