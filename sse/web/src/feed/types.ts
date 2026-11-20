import type { LifecycleEvent } from "../lifecycle.js";
import type { Frame } from "../stream.js";
import type { TabAttachment } from "../tab.js";
import type { TimingConfig } from "../timing.js";

/** The `changes.read` answer: the rows of every asked kind changed since the request's revision. */
export interface ChangesAnswer<R extends Record<string, unknown>> {
  readonly source: string;
  readonly epoch: string;
  readonly rev: number;
  /** The log no longer holds `since`, or the answer would pass its cap: every asked kind re-reads in full. */
  readonly full: boolean;
  readonly upserts: { readonly [K in keyof R]?: readonly R[K][] };
  readonly removed: { readonly [K in keyof R]?: readonly string[] };
  /** Asked kinds with no row form on the wire: each re-reads in full. */
  readonly signals?: readonly string[];
}

/**
 * `retry`: the request may succeed if sent again (network, timeout, a `NoEffect` 429 or 503).
 * `refused`: the server or the client refuses it (invalid, decode, any other http code).
 */
export type CatchUpFailure = "retry" | "refused";

/** Sends one `changes.read` request. The feed aborts `signal` once every subscription in the request has stopped. */
export type CatchUp<R extends Record<string, unknown>> = (
  q: {
    readonly source: string;
    readonly epoch: string;
    readonly since: number;
    readonly kinds: readonly string[];
  },
  signal: AbortSignal,
) => Promise<
  | { readonly ok: true; readonly answer: ChangesAnswer<R> }
  | { readonly ok: false; readonly failure: CatchUpFailure }
>;

/** Where one subscription's changes go. */
export interface FeedSink<Row> {
  upsert?(rows: readonly Row[]): void;
  remove?(keys: readonly string[]): void;
  /**
   * Re-reads in full and returns that read's promise, calling the subscription's `seed` with the
   * read's headers before it settles. A settling with no `seed` is a failed read.
   */
  stale(): PromiseLike<unknown>;
}

/** The stream every tab shares through the shipped `@cplieger/sse/feed/worker` entry. */
export interface FeedStream {
  /** The stream's root-relative path, such as `/api/events`. */
  readonly path: string;
  /** The entry's served URL: the app bundle's `BUNDLE_URLS` value for it. */
  readonly worker: string;
  /** How the worker loads; default `"classic"`, as a webbundle `Classic` entry is built. */
  readonly type?: WorkerType;
  /**
   * Receives the tab's lifecycle records, from the shared stream or the tab's own: `state` for a
   * connection status, `auth_lost` for a credential the server no longer takes.
   */
  readonly onLifecycle?: (ev: LifecycleEvent) => void;
}

/**
 * `reconnect`: the app attaches the stream itself and passes each frame to `onFrame`. `stream`:
 * `createFeed` attaches the tab to the shared worker, or to its own stream where there is no
 * `SharedWorker`.
 */
export type FeedOptions<R extends Record<string, unknown>> = FeedOptionsBase<R> &
  (
    | {
        /** Reconnects the stream; called once per lost-track episode. */
        readonly reconnect: () => void;
        readonly stream?: never;
      }
    | { readonly stream: FeedStream; readonly reconnect?: never }
  );

interface FeedOptionsBase<R extends Record<string, unknown>> {
  /** The generated `FEED_VERSION`; `createFeed` throws `FeedVersionMismatch` unless it is this package's. */
  readonly feedVersion: number;
  /** The generated `KINDS`: each kind's source. */
  readonly kinds: Readonly<Record<string, string>>;
  /** The generated `LOGGED_SOURCES`: the sources a catch-up can read. */
  readonly logged: readonly string[];
  readonly catchUp: CatchUp<R>;
  /** How long frames gather before one catch-up or re-read: default 50, 0 to 1,000. */
  readonly coalesceMs?: number;
  /** Receives each listening frame's `wire`, in any feed format, before anything else is read. A throw is reported with `reportError`. */
  readonly onWire?: (serverVersion: string) => void;
  /** A failed read's backoff curve; default `DEFAULT_TIMING`'s. */
  readonly timing?: Pick<TimingConfig, "baseMs" | "capMs">;
  /** In [0, 1): the backoff's jitter; default `Math.random`. */
  readonly random?: () => number;
}

export interface Subscription {
  /** The sink no longer listens. */
  stop(): void;
  /** A full re-read's response headers: its `Feed-Position` becomes this subscription's watermark. */
  seed(headers: Headers): void;
}

export interface Feed<R extends Record<string, unknown>> {
  /** `seed` is the first read's response headers. Throws `RangeError` for an undeclared kind. */
  watch<K extends string>(
    kind: K,
    sink: FeedSink<K extends keyof R ? R[K] : never>,
    seed: Headers,
  ): Subscription;
  /**
   * `true` when the frame was the feed's (`changed` or `listening`). Throws for a feed frame that
   * loses track (it fails the parse, or is a `changed` frame on another epoch than its source's),
   * so the stream runtime reports it; a frame written in another feed format never throws.
   */
  onFrame(frame: Frame): boolean;
  /** Called after each applied listening frame of `source`; returns the unsubscribe. */
  onListening(source: string, listener: () => void): () => void;
  /** This tab's attachment to the stream `stream` named; null when the app attaches it itself. */
  readonly attachment: TabAttachment | null;
}
