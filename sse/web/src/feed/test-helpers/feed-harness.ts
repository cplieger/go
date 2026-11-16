import { vi } from "vitest";
import type { Frame } from "../../stream.js";
import { createFeed } from "../feed.js";
import type {
  CatchUp,
  ChangesAnswer,
  CatchUpFailure,
  Feed,
  FeedOptions,
  FeedSink,
} from "../types.js";
import { FEED_VERSION } from "../version.js";
import { encodeChanged, encodeListening, formatPosition } from "./wire-encode.js";
import { KINDS, LOGGED_SOURCES } from "./client.gen.js";

export const E1 = "9f2c4e1a7b3d5f60819a2b3c4d5e6f70";
export const E2 = "41ab0c9d8e7f6a5b4c3d2e1f00112233";

interface Row {
  readonly id: string;
}
export type Rows = Readonly<Record<"2fa" | "budgets" | "runs" | "queue" | "settings", Row>>;

export interface SourceFloors {
  readonly source: string;
  readonly epoch: string;
  readonly floors: Readonly<Record<string, number>>;
}

export function listening(
  sources: readonly SourceFloors[],
  wire: string | null = null,
  feed: number = FEED_VERSION,
): Frame {
  const data = encodeListening(
    {
      wire,
      sources: sources.map((s) => ({
        source: s.source,
        epoch: s.epoch,
        floors: new Map(Object.entries(s.floors)),
      })),
    },
    feed,
  );
  return { type: "listening", data, id: null };
}

export function changed(
  source: string,
  epoch: string,
  rev: number,
  kinds: readonly string[],
): Frame {
  return { type: "changed", data: encodeChanged({ source, epoch, rev, kinds }), id: null };
}

/** Response headers carrying a Feed-Position of each `[source, epoch, rev]`. */
export function position(...ps: readonly (readonly [string, string, number])[]): Headers {
  const h = new Headers();
  if (ps.length > 0) {
    h.set(
      "Feed-Position",
      formatPosition(ps.map(([source, epoch, rev]) => ({ source, epoch, rev }))),
    );
  }
  return h;
}

interface Deferred<T> {
  readonly promise: Promise<T>;
  resolve(v: T): void;
  reject(e: unknown): void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

type CatchUpResult = Awaited<ReturnType<CatchUp<Rows>>>;

interface CatchUpCall {
  readonly q: Parameters<CatchUp<Rows>>[0];
  readonly signal: AbortSignal;
  readonly result: Deferred<CatchUpResult>;
  answer(a: Partial<ChangesAnswer<Rows>> & { readonly rev: number }): Promise<void>;
  fail(failure: CatchUpFailure): Promise<void>;
}

/** One `stale()` read, settled by the test: `resolve` without a seed is a failed read. */
interface Read {
  resolve(): void;
  reject(e: unknown): void;
}

/** A sink that records every call; each `stale()` returns a read the test settles. */
export interface RecordingSink extends FeedSink<Row> {
  readonly log: string[];
  readonly reads: Read[];
  readonly upserted: Row[];
  readonly removed: string[];
}

export function sink(
  opts: { readonly upsert?: boolean; readonly remove?: boolean } = {},
): RecordingSink {
  const log: string[] = [];
  const reads: Read[] = [];
  const upserted: Row[] = [];
  const removed: string[] = [];
  const s: RecordingSink = {
    log,
    reads,
    upserted,
    removed,
    stale() {
      log.push("stale");
      const d = deferred<undefined>();
      reads.push({
        resolve: () => {
          d.resolve(undefined);
        },
        reject: d.reject,
      });
      return d.promise;
    },
  };
  if (opts.remove !== false) {
    s.remove = (keys: readonly string[]): void => {
      log.push(`remove ${keys.join(",")}`);
      removed.push(...keys);
    };
  }
  if (opts.upsert !== false) {
    s.upsert = (rows: readonly Row[]): void => {
      log.push(`upsert ${rows.map((r) => r.id).join(",")}`);
      upserted.push(...rows);
    };
  }
  return s;
}

/** A sink whose every `stale()` fails at once; `times` holds the fake clock at each call. */
export function failingSink(
  how: "reject" | "noseed" = "reject",
): FeedSink<Row> & { readonly times: number[] } {
  const times: number[] = [];
  return {
    times,
    upsert() {
      // Rows are not this sink's subject.
    },
    stale() {
      times.push(Date.now());
      return how === "reject" ? Promise.reject(new Error("read failed")) : Promise.resolve();
    },
  };
}

/** Lets every settled promise run its continuations. */
export async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}
export async function advance(ms: number): Promise<void> {
  await vi.advanceTimersByTimeAsync(ms);
}

/** Sends one `changed` frame for `kind` every 10 ms, each at the next revision, until the clock reads `to`. */
export function bumper(
  feed: Feed<Rows>,
  kind: string,
  source: string,
  epoch: string,
  rev = 1_000,
): (to: number) => Promise<void> {
  let next = rev;
  return async (to) => {
    while (Date.now() < to) {
      await advance(10);
      feed.onFrame(changed(source, epoch, ++next, [kind]));
    }
  };
}

export interface Harness {
  readonly feed: Feed<Rows>;
  readonly calls: CatchUpCall[];
  readonly reconnect: ReturnType<typeof vi.fn<() => void>>;
  readonly wires: string[];
}

export function harness(over: Partial<FeedOptions<Rows>> = {}): Harness {
  const calls: CatchUpCall[] = [];
  const wires: string[] = [];
  const reconnect = vi.fn<() => void>();
  const catchUp: CatchUp<Rows> = (q, signal) => {
    const result = deferred<CatchUpResult>();
    calls.push({
      q,
      signal,
      result,
      async answer(a) {
        result.resolve({
          ok: true,
          answer: {
            source: q.source,
            epoch: q.epoch,
            full: false,
            upserts: {},
            removed: {},
            ...a,
          },
        });
        await settle();
      },
      async fail(failure) {
        result.resolve({ ok: false, failure });
        await settle();
      },
    });
    return result.promise;
  };
  const feed = createFeed<Rows>({
    feedVersion: FEED_VERSION,
    kinds: KINDS,
    logged: LOGGED_SOURCES,
    catchUp,
    reconnect,
    onWire: (w) => wires.push(w),
    ...over,
  });
  return { feed, calls, reconnect, wires };
}
