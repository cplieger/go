import type { Frame } from "../stream.js";
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

export interface FeedOptions<R extends Record<string, unknown>> {
  /** The generated `FEED_VERSION`; `createFeed` throws `FeedVersionMismatch` unless it is this package's. */
  readonly feedVersion: number;
  /** The generated `KINDS`: each kind's source. */
  readonly kinds: Readonly<Record<string, string>>;
  /** The generated `LOGGED_SOURCES`: the sources a catch-up can read. */
  readonly logged: readonly string[];
  readonly catchUp: CatchUp<R>;
  /** How long frames gather before one catch-up or re-read: default 50, 0 to 1,000. */
  readonly coalesceMs?: number;
  /** Receives each listening frame's `wire`. */
  readonly onWire?: (serverVersion: string) => void;
  /** Reconnects the stream; called once per lost-track episode. */
  readonly reconnect: () => void;
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
  /** `true` when the frame was the feed's (`changed` or `listening`). */
  onFrame(frame: Frame): boolean;
  /** Called after each applied listening frame of `source`; returns the unsubscribe. */
  onListening(source: string, listener: () => void): () => void;
}
