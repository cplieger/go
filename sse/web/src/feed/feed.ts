import type { Frame } from "../stream.js";
import type { CatchUp, ChangesAnswer, Feed, FeedOptions, FeedSink, Subscription } from "./types.js";
import { FEED_VERSION, FeedVersionMismatch } from "./version.js";
import type { ChangedFrame, ListeningFrame } from "./wire.js";
import { parseChanged, parseListening, parsePosition, validKind, validSource } from "./wire.js";

const DEFAULT_COALESCE_MS = 50;
const MAX_COALESCE_MS = 1_000;

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
  stopped: boolean;
}

interface Flight {
  readonly epoch: string;
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
}

type ByKind<T> = Readonly<Record<string, readonly T[] | undefined>>;

const own = <T>(table: ByKind<T>, kind: string): readonly T[] =>
  (Object.hasOwn(table, kind) ? table[kind] : undefined) ?? [];

/**
 * Builds the change feed of one tab. Throws `FeedVersionMismatch` when `o.feedVersion` is not
 * `FEED_VERSION`, then `RangeError` for a `coalesceMs` outside 0 to 1,000 or a kind, source or
 * logged name outside its grammar.
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
      };
      sources.set(source, src);
    }
    kindSource.set(kind, src);
  }
  const listeners = new Map<string, Set<() => void>>();

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
    } else if (sub.marked) {
      schedule(sub.src);
    }
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
    const ask: Sub[] = [];
    for (const sub of src.subs) {
      if (!sub.marked || sub.rereading) {
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
    const flight: Flight = { epoch, subs, controller: new AbortController() };
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
    // A subscription staled or re-seeded on another epoch since the request is no longer its.
    const subs = flight.subs.filter(
      (s) => !s.stopped && !s.rereading && s.watermark?.epoch === flight.epoch,
    );
    try {
      if (r.ok) {
        applyAnswer(flight.epoch, subs, r.answer);
      } else if (r.failure === "refused") {
        subs.forEach(stale);
      } else {
        for (const s of subs) {
          s.marked = true;
        }
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
      const wm = s.watermark;
      if (wm === null) {
        continue;
      }
      if (signals.has(s.kind) || s.sink.upsert === undefined) {
        stale(s);
        continue;
      }
      // A seed newer than the answer already holds these rows or newer ones.
      if (wm.rev <= a.rev) {
        const rows = own(upserts, s.kind);
        const keys = own(removed, s.kind);
        if (rows.length > 0) {
          s.sink.upsert(rows);
        }
        if (keys.length > 0) {
          s.sink.remove?.(keys);
        }
        s.watermark = { epoch, rev: a.rev };
      }
      compare(s, s.watermark ?? wm);
    }
  }

  function onListeningFrame(l: ListeningFrame): void {
    const applied: string[] = [];
    for (const ls of l.sources) {
      const src = sources.get(ls.source);
      if (src === undefined) {
        continue;
      }
      const floors = [...ls.floors].filter(([k]) => kindSource.get(k) === src);
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
        if (!sub.rereading && wm !== null && wm.epoch !== src.epoch) {
          sub.otherEpochStaled = true;
          stale(sub);
        } else {
          touch(sub);
        }
      }
      applied.push(src.name);
    }
    for (const name of applied) {
      for (const listener of [...(listeners.get(name) ?? [])]) {
        listener();
      }
    }
  }

  function onChangedFrame(c: ChangedFrame): void {
    const src = sources.get(c.source);
    if (src?.epoch !== c.epoch) {
      return;
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
    sub.src.subs.delete(sub);
    const flight = sub.src.inFlight;
    if (flight?.subs.every((s) => s.stopped) === true) {
      flight.controller.abort();
      endFlight(sub.src);
    }
  }

  return {
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
          if (sub.rereading) {
            sub.reseed = wm;
          } else {
            check(sub, wm);
          }
        },
      };
    },
    onFrame(frame: Frame): boolean {
      if (frame.type === "changed") {
        const r = parseChanged(frame.data);
        if (r.ok) {
          onChangedFrame(r.changed);
        }
        return true;
      }
      if (frame.type === "listening") {
        const r = parseListening(frame.data);
        if (r.kind === "ok") {
          onListeningFrame(r.listening);
        }
        return true;
      }
      return false;
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
