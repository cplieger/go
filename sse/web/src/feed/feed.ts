import { jitter } from "../reducer.js";
import type { Frame } from "../stream.js";
import { createStream } from "../stream.js";
import { type SharedWorkerLike, type TabAttachment, attachToWorker } from "../tab.js";
import { DEFAULT_TIMING } from "../timing.js";
import { createVersionMap } from "../versions.js";
import { rootPath } from "./stream-path.js";
import type {
  CatchUp,
  ChangesAnswer,
  Feed,
  FeedOptions,
  FeedSink,
  FeedStream,
  Subscription,
} from "./types.js";
import { FEED_VERSION, FeedVersionMismatch } from "./version.js";
import type { ChangedFrame, ListeningFrame } from "./wire.js";
import { parseChanged, parseListening, parsePosition, validKind, validSource } from "./wire.js";

const DEFAULT_COALESCE_MS = 50;
const MAX_COALESCE_MS = 1_000;
/** One name for every tab: the worker URL, which carries the stream path, tells feeds apart. */
const WORKER_NAME = "@cplieger/sse/feed";
/**
 * Not the document's base: createFeed may run with none, and the sentinel has no query or
 * fragment, so the check sees only what the worker string carries.
 */
const ENTRY_CHECK_BASE = "https://feed.invalid";

interface Watermark {
  readonly epoch: string;
  readonly rev: number;
}

interface Sub {
  readonly kind: string;
  readonly src: Src;
  readonly sink: FeedSink<unknown>;
  watermark: Watermark | null;
  marked: boolean;
  rereading: boolean;
  /** The newest seed since `stale()`: `undefined` while none arrived, `null` for one with no position. */
  reseed: Watermark | null | undefined;
  /** Rule 1's allowance: an other-epoch seed was staled since the source's last listening frame. */
  otherEpochStaled: boolean;
  /** Consecutive failed re-reads; the next seed resets it. */
  failures: number;
  /** The backoff wait after a failed re-read. */
  wait: ReturnType<typeof setTimeout> | null;
  stopped: boolean;
}

interface Flight {
  readonly epoch: string;
  readonly since: number;
  readonly subs: readonly Sub[];
  readonly controller: AbortController;
}

interface Src {
  readonly name: string;
  readonly logged: boolean;
  epoch: string | null;
  floors: Map<string, number>;
  readonly subs: Set<Sub>;
  timer: ReturnType<typeof setTimeout> | null;
  inFlight: Flight | null;
  again: boolean;
  /** Consecutive `retry` catch-ups; an applied answer resets it. */
  retries: number;
  /** The backoff wait after a `retry` catch-up: no catch-up of the source goes out during it. */
  retryWait: ReturnType<typeof setTimeout> | null;
}

type ByKind<T> = Readonly<Record<string, readonly T[] | undefined>>;

const own = <T>(table: ByKind<T>, kind: string): readonly T[] =>
  (Object.hasOwn(table, kind) ? table[kind] : undefined) ?? [];

type SharedWorkerCtor = new (
  url: string,
  options: { readonly name: string; readonly type: WorkerType },
) => SharedWorkerLike;

// webbundle writes neither a fragment nor a stream parameter, so either means a misconfigured
// entry URL; refused rather than repaired.
function checkWorker(worker: string): void {
  let url: URL;
  try {
    url = new URL(worker, ENTRY_CHECK_BASE);
  } catch {
    throw new RangeError(`createFeed: stream worker "${worker}" is not a URL`);
  }
  // An empty fragment leaves `hash` empty.
  if (url.href.includes("#")) {
    throw new RangeError(`createFeed: stream worker "${worker}" has a fragment`);
  }
  if (url.searchParams.has("stream")) {
    throw new RangeError(`createFeed: stream worker "${worker}" has its own "stream" parameter`);
  }
}

// The fallback takes no revalidation, as the entry's host takes none: the listening frame decides.
function attachStream(s: FeedStream, onFrame: (frame: Frame) => boolean): TabAttachment {
  const type = s.type ?? "classic";
  const onLifecycle = s.onLifecycle === undefined ? {} : { onLifecycle: s.onLifecycle };
  return attachToWorker({
    spawn: () => {
      // Both exist only in a page, as SharedWorker is exposed to Window alone
      // (https://html.spec.whatwg.org/multipage/workers.html#sharedworker), and attachToWorker
      // spawns only where SharedWorker is present.
      const page = globalThis as unknown as {
        readonly SharedWorker: SharedWorkerCtor;
        readonly document: { readonly baseURI: string };
      };
      const url = new URL(s.worker, page.document.baseURI);
      url.searchParams.set("stream", s.path);
      return new page.SharedWorker(url.href, { name: WORKER_NAME, type });
    },
    fallback() {
      const versions = createVersionMap();
      const headers: Record<string, string> = {};
      const stream = createStream({
        url: s.path,
        versions,
        headers,
        onFrame,
        ...onLifecycle,
        revalidate: () => Promise.resolve(),
      });
      return { stream, versions, headers };
    },
    onFrame,
    ...onLifecycle,
  });
}

/**
 * Builds the change feed of one tab. Throws `FeedVersionMismatch` when `o.feedVersion` is not
 * `FEED_VERSION`, then `RangeError` for a `coalesceMs` outside 0 to 1,000, a kind, source or
 * logged name outside its grammar, a `stream.path` that is not root-relative, or a `stream.worker`
 * that is not a URL or has a fragment or its own `stream` parameter. With `stream`, it attaches the
 * tab before it returns.
 */
export function createFeed<R extends Record<string, unknown>>(o: FeedOptions<R>): Feed<R> {
  type CatchUpResult = Awaited<ReturnType<CatchUp<R>>>;
  if (o.feedVersion !== FEED_VERSION) {
    throw new FeedVersionMismatch(o.feedVersion, FEED_VERSION);
  }
  const coalesceMs = o.coalesceMs ?? DEFAULT_COALESCE_MS;
  if (!(coalesceMs >= 0 && coalesceMs <= MAX_COALESCE_MS)) {
    throw new RangeError(`createFeed: coalesceMs ${String(coalesceMs)} is not 0 to 1,000`);
  }
  if (o.stream !== undefined && !rootPath(o.stream.path)) {
    throw new RangeError(`createFeed: stream path "${o.stream.path}" is not a root-relative path`);
  }
  if (o.stream !== undefined) {
    checkWorker(o.stream.worker);
  }
  for (const name of o.logged) {
    if (!validSource(name)) {
      throw new RangeError(`createFeed: logged source "${name}" is not a source name`);
    }
  }
  const logged = new Set(o.logged);
  const sources = new Map<string, Src>();
  const kindSource = new Map<string, Src>();
  for (const [kind, source] of Object.entries(o.kinds)) {
    if (!validKind(kind)) {
      throw new RangeError(`createFeed: kind "${kind}" is not a kind name`);
    }
    if (!validSource(source)) {
      throw new RangeError(`createFeed: kind "${kind}" names "${source}", not a source name`);
    }
    let src = sources.get(source);
    if (src === undefined) {
      src = {
        name: source,
        logged: logged.has(source),
        epoch: null,
        floors: new Map(),
        subs: new Set(),
        timer: null,
        inFlight: null,
        again: false,
        retries: 0,
        retryWait: null,
      };
      sources.set(source, src);
    }
    kindSource.set(kind, src);
  }
  const listeners = new Map<string, Set<() => void>>();
  // Rule 8. `otherFormat`: the newest listening frame was written in another feed format.
  // `lostTrack`: the sources whose next valid listening frame ends the lost-track episode, or
  // `null` outside one; a connection's frame ends it whatever it names.
  let otherFormat = false;
  let lostTrack: Set<Src> | null = null;
  const timing = {
    baseMs: o.timing?.baseMs ?? DEFAULT_TIMING.baseMs,
    capMs: o.timing?.capMs ?? DEFAULT_TIMING.capMs,
  };
  const random = o.random ?? Math.random;
  let attachment: TabAttachment | null = null;
  const reconnect =
    o.stream === undefined
      ? o.reconnect
      : () => {
          attachment?.reconnect();
        };

  // The wait after the nth consecutive failure: the stream runtime's reconnect delay for attempt n.
  function backoff(n: number): number {
    return Math.max(coalesceMs, jitter(n - 1, random(), timing));
  }

  function endWait(sub: Sub): void {
    if (sub.wait !== null) {
      clearTimeout(sub.wait);
      sub.wait = null;
    }
  }

  function positionOf(src: Src, headers: Headers): Watermark | null {
    const value = headers.get("Feed-Position");
    const parsed = value === null ? null : parsePosition(value);
    const p = parsed?.ok === true ? parsed.positions.find((m) => m.source === src.name) : undefined;
    return p === undefined ? null : { epoch: p.epoch, rev: p.rev };
  }

  function schedule(src: Src): void {
    src.timer ??= setTimeout(() => {
      src.timer = null;
      flush(src);
    }, coalesceMs);
  }

  function mark(sub: Sub): void {
    sub.marked = true;
    schedule(sub.src);
  }

  function compare(sub: Sub, wm: Watermark): void {
    if (wm.rev >= (sub.src.floors.get(sub.kind) ?? 0)) {
      sub.marked = false;
    } else {
      mark(sub);
    }
  }

  // Rule 1. A seed with no watermark waits for rule 3's next frame; one before the source's first
  // listening frame waits for that frame.
  function check(sub: Sub, wm: Watermark | null): void {
    sub.watermark = wm;
    const epoch = sub.src.epoch;
    if (wm === null || epoch === null) {
      return;
    }
    if (wm.epoch === epoch) {
      compare(sub, wm);
      return;
    }
    sub.marked = false;
    if (!sub.otherEpochStaled) {
      sub.otherEpochStaled = true;
      stale(sub);
    }
  }

  function stale(sub: Sub): void {
    endWait(sub);
    sub.watermark = null;
    sub.marked = false;
    sub.rereading = true;
    sub.reseed = undefined;
    let read: PromiseLike<unknown>;
    try {
      read = sub.sink.stale();
    } catch {
      // A throw is a read that settles with no seed: a failed read.
      read = Promise.resolve();
    }
    const done = (): void => {
      settleReread(sub);
    };
    Promise.resolve(read).then(done, done);
  }

  function settleReread(sub: Sub): void {
    if (sub.stopped) {
      return;
    }
    sub.rereading = false;
    const wm = sub.reseed;
    sub.reseed = undefined;
    if (wm !== undefined) {
      sub.marked = false;
      check(sub, wm);
      return;
    }
    sub.failures++;
    sub.wait = setTimeout(() => {
      sub.wait = null;
      // During the source's retry wait the subscription stays marked: the wait's end flushes it.
      if (sub.marked && sub.src.retryWait === null) {
        stale(sub);
      }
    }, backoff(sub.failures));
  }

  // Rule 3's check of one subscription of a kind a frame names. A watermark on another epoch is a
  // seed rule 1 holds for the source's next listening frame.
  function touch(sub: Sub): void {
    const wm = sub.watermark;
    if (sub.rereading) {
      sub.marked = true;
    } else if (wm === null) {
      mark(sub);
    } else if (wm.epoch === sub.src.epoch) {
      compare(sub, wm);
    }
  }

  // Rules 4 and 6: after coalesceMs, one catch-up of the source's marked subscriptions, and one
  // stale() for each marked one a catch-up cannot answer.
  function flush(src: Src): void {
    // A retry wait holds every subscription of the source, a no-watermark one's stale() included.
    if (src.retryWait !== null) {
      return;
    }
    const ask: Sub[] = [];
    for (const sub of src.subs) {
      if (!sub.marked || sub.rereading || sub.wait !== null) {
        continue;
      }
      if (sub.watermark === null || !src.logged) {
        stale(sub);
      } else if (sub.watermark.epoch === src.epoch) {
        ask.push(sub);
      }
    }
    if (ask.length === 0 || src.epoch === null) {
      return;
    }
    if (src.inFlight !== null) {
      src.again = true;
      return;
    }
    send(src, src.epoch, ask);
  }

  function send(src: Src, epoch: string, subs: readonly Sub[]): void {
    const kinds = [...new Set(subs.map((s) => s.kind))].sort();
    const since = Math.min(...subs.map((s) => s.watermark?.rev ?? 0));
    for (const s of subs) {
      s.marked = false;
    }
    const flight: Flight = { epoch, since, subs, controller: new AbortController() };
    src.inFlight = flight;
    let answer: Promise<CatchUpResult>;
    try {
      answer = o.catchUp({ source: src.name, epoch, since, kinds }, flight.controller.signal);
    } catch {
      answer = Promise.resolve({ ok: false, failure: "retry" });
    }
    answer.then(
      (r) => {
        settleCatchUp(src, flight, r);
      },
      () => {
        settleCatchUp(src, flight, { ok: false, failure: "retry" });
      },
    );
  }

  function endFlight(src: Src): void {
    src.inFlight = null;
    if (src.again) {
      src.again = false;
      if (src.timer !== null) {
        clearTimeout(src.timer);
        src.timer = null;
      }
      flush(src);
    }
  }

  function settleCatchUp(src: Src, flight: Flight, r: CatchUpResult): void {
    if (flight.controller.signal.aborted) {
      return;
    }
    // The source's epoch moved or was forgotten since the request: its listening frame decides.
    if (src.epoch !== flight.epoch) {
      endFlight(src);
      return;
    }
    // A subscription staled or re-seeded on another epoch since the request is no longer its, nor
    // is one re-seeded below its `since`: the answer lacks the changes between the two, and the
    // seed's own check has marked it for a catch-up from the seed.
    const subs = flight.subs.filter(
      (s) =>
        !s.stopped &&
        !s.rereading &&
        s.watermark?.epoch === flight.epoch &&
        s.watermark.rev >= flight.since,
    );
    try {
      if (r.ok) {
        src.retries = 0;
        applyAnswer(flight.epoch, subs, r.answer);
      } else if (r.failure === "refused") {
        subs.forEach(stale);
      } else {
        for (const s of subs) {
          s.marked = true;
        }
        src.retries++;
        src.retryWait = setTimeout(() => {
          src.retryWait = null;
          flush(src);
        }, backoff(src.retries));
      }
    } finally {
      endFlight(src);
    }
  }

  // Rule 5. An answer on another epoch than its request's cannot be placed in the request's
  // numbering, so it reads as a full answer from the server that restored the source.
  function applyAnswer(epoch: string, subs: readonly Sub[], a: ChangesAnswer<R>): void {
    if (a.full || a.epoch !== epoch) {
      for (const s of subs) {
        s.otherEpochStaled ||= a.epoch !== epoch;
        stale(s);
      }
      return;
    }
    const signals = new Set(a.signals ?? []);
    const upserts = a.upserts as ByKind<unknown>;
    const removed = a.removed as ByKind<string>;
    for (const s of subs) {
      try {
        applyRows(s, epoch, a.rev, signals.has(s.kind), own(upserts, s.kind), own(removed, s.kind));
      } catch (e: unknown) {
        reportError(e);
        stale(s);
      }
    }
  }

  // A sink that cannot take the answer's rows of its kind (no upsert, or removals and no remove)
  // is re-read: moving its watermark would show rows the answer changed as covered.
  function applyRows(
    s: Sub,
    epoch: string,
    rev: number,
    signal: boolean,
    rows: readonly unknown[],
    keys: readonly string[],
  ): void {
    const wm = s.watermark;
    if (wm === null) {
      return;
    }
    const sink = s.sink;
    if (signal || sink.upsert === undefined || (keys.length > 0 && sink.remove === undefined)) {
      stale(s);
      return;
    }
    // A seed newer than the answer already holds these rows or newer ones.
    if (wm.rev <= rev) {
      if (rows.length > 0) {
        sink.upsert(rows);
      }
      if (keys.length > 0) {
        sink.remove?.(keys);
      }
      s.watermark = { epoch, rev };
    }
    compare(s, s.watermark ?? wm);
  }

  // An app callback's throw is reported, never left to stop the frame that called it.
  function callApp(fn: () => void): void {
    try {
      fn();
    } catch (e: unknown) {
      reportError(e);
    }
  }

  // Every subscription waits as before the source's first listening frame (rule 1), so nothing
  // scheduled under the old floors is sent. A running retry wait ends with nothing marked to send.
  function forget(): void {
    for (const src of sources.values()) {
      src.epoch = null;
      src.floors = new Map();
      src.again = false;
      if (src.timer !== null) {
        clearTimeout(src.timer);
        src.timer = null;
      }
      for (const sub of src.subs) {
        sub.marked = false;
      }
    }
  }

  // Throws so the stream runtime reports the frame as rejected.
  function loseTrack(reason: string): never {
    const known = [...sources.values()].filter((s) => s.epoch !== null);
    const first = lostTrack === null;
    lostTrack = new Set([...(lostTrack ?? []), ...known]);
    forget();
    if (first) {
      callApp(reconnect);
    }
    throw new Error(`feed: ${reason}`);
  }

  function onListeningData(data: string): void {
    const r = parseListening(data);
    const wire = r.frozen?.wire ?? null;
    if (wire !== null && o.onWire !== undefined) {
      const onWire = o.onWire;
      callApp(() => {
        onWire(wire);
      });
    }
    if (r.kind === "other_format") {
      if (!otherFormat) {
        otherFormat = true;
        forget();
      }
      return;
    }
    if (otherFormat && r.frozen === null) {
      return;
    }
    otherFormat = false;
    if (r.kind === "invalid") {
      loseTrack(r.reason);
    }
    onListeningFrame(r.listening);
  }

  function onChangedData(data: string): void {
    if (otherFormat) {
      return;
    }
    const r = parseChanged(data);
    if (!r.ok) {
      loseTrack(r.reason);
    }
    onChangedFrame(r.changed);
  }

  function onListeningFrame(l: ListeningFrame): void {
    const applied: string[] = [];
    for (const ls of l.sources) {
      const src = sources.get(ls.source);
      if (src === undefined) {
        continue;
      }
      const floors = [...ls.floors].filter(([k]) => kindSource.get(k) === src);
      if (src.retryWait !== null) {
        clearTimeout(src.retryWait);
        src.retryWait = null;
      }
      if (src.epoch === ls.epoch) {
        for (const [k, f] of floors) {
          src.floors.set(k, Math.max(f, src.floors.get(k) ?? 0));
        }
      } else {
        src.epoch = ls.epoch;
        src.floors = new Map(floors);
      }
      for (const sub of src.subs) {
        sub.otherEpochStaled = false;
        const wm = sub.watermark;
        if (sub.wait !== null) {
          stale(sub);
        } else if (!sub.rereading && wm !== null && wm.epoch !== src.epoch) {
          sub.otherEpochStaled = true;
          stale(sub);
        } else {
          touch(sub);
        }
      }
      applied.push(src.name);
      lostTrack?.delete(src);
    }
    // Only a connection's frame carries wire, and it names every source of that connection's view:
    // a source the new view no longer holds never sends a frame again.
    if (l.wire !== null || lostTrack?.size === 0) {
      lostTrack = null;
    }
    for (const name of applied) {
      for (const listener of [...(listeners.get(name) ?? [])]) {
        callApp(listener);
      }
    }
  }

  function onChangedFrame(c: ChangedFrame): void {
    const src = sources.get(c.source);
    if (src === undefined || src.epoch === null) {
      return;
    }
    if (src.epoch !== c.epoch) {
      loseTrack("changed frame on another epoch than its source's current one");
    }
    const kinds = new Set(c.kinds.filter((k) => kindSource.get(k) === src));
    for (const k of kinds) {
      src.floors.set(k, Math.max(c.rev, src.floors.get(k) ?? 0));
    }
    for (const sub of src.subs) {
      if (kinds.has(sub.kind)) {
        touch(sub);
      }
    }
  }

  function stop(sub: Sub): void {
    if (sub.stopped) {
      return;
    }
    sub.stopped = true;
    endWait(sub);
    sub.src.subs.delete(sub);
    const flight = sub.src.inFlight;
    if (flight?.subs.every((s) => s.stopped) === true) {
      flight.controller.abort();
      endFlight(sub.src);
    }
  }

  function onFrame(frame: Frame): boolean {
    if (frame.type === "changed") {
      onChangedData(frame.data);
      return true;
    }
    if (frame.type === "listening") {
      onListeningData(frame.data);
      return true;
    }
    return false;
  }

  if (o.stream !== undefined) {
    attachment = attachStream(o.stream, onFrame);
  }

  return {
    attachment,
    onFrame,
    watch(kind, sink, seed): Subscription {
      const src = kindSource.get(kind);
      if (src === undefined) {
        throw new RangeError(`createFeed: kind "${kind}" is not declared`);
      }
      const sub: Sub = {
        kind,
        src,
        sink,
        watermark: null,
        marked: false,
        rereading: false,
        reseed: undefined,
        otherEpochStaled: false,
        failures: 0,
        wait: null,
        stopped: false,
      };
      src.subs.add(sub);
      check(sub, positionOf(src, seed));
      return {
        stop: () => {
          stop(sub);
        },
        seed: (headers) => {
          if (sub.stopped) {
            return;
          }
          const wm = positionOf(src, headers);
          sub.failures = 0;
          if (sub.rereading) {
            sub.reseed = wm;
          } else {
            endWait(sub);
            check(sub, wm);
          }
        },
      };
    },
    onListening(source, listener) {
      let set = listeners.get(source);
      if (set === undefined) {
        set = new Set();
        listeners.set(source, set);
      }
      const entry = (): void => {
        listener();
      };
      set.add(entry);
      return () => {
        set.delete(entry);
      };
    },
  };
}
