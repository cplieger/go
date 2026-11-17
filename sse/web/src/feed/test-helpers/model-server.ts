import { vi } from "vitest";
import type { Frame } from "../../stream.js";
import { createFeed } from "../feed.js";
import type { CatchUp, ChangesAnswer, Feed, FeedSink, Subscription } from "../types.js";
import { FEED_VERSION } from "../version.js";
import { KINDS, LOGGED_SOURCES } from "./client.gen.js";
import { changed, listening, position } from "./feed-harness.js";

// A server for the feed's model-based properties: logged and unlogged sources, each with its
// epoch, revisions, per-kind floors, rows and change log, one stream that drops and reconnects,
// and reads and catch-ups answered from a snapshot taken when they are asked, delivered whenever
// the property picks them. Every breach of an invariant the feed promises is recorded in
// `violations` instead of thrown, because the feed calls the sinks inside its own promise chains.

export type ModelKind = keyof typeof KINDS;

interface ModelRow {
  readonly id: string;
  /** The revision of the commit that wrote the row. */
  readonly v: number;
  /** The epoch it was written in: revisions compare only within one. */
  readonly epoch: string;
}

type ModelRows = Readonly<Record<ModelKind, ModelRow>>;

const SIGNAL_KINDS: ReadonlySet<string> = new Set(["settings"]);
const WIRE = "0123456789abcdef";

interface Change {
  readonly rev: number;
  readonly kind: string;
  readonly key: string;
}

interface ModelSource {
  readonly name: string;
  readonly logged: boolean;
  readonly kinds: readonly ModelKind[];
  epoch: string;
  rev: number;
  /** The log holds every change above this revision. */
  trimmedTo: number;
  floors: Map<string, number>;
  readonly rows: Map<string, Map<string, ModelRow>>;
  log: Change[];
}

interface Sent {
  readonly frame: Frame;
  readonly source: string;
  readonly epoch: string;
}

interface Pending {
  readonly what: "open" | "read" | "refresh" | "catchup";
  readonly source: string;
  /** The sink whose `stale()` asked for this read. */
  readonly sink: ModelSink | null;
  /** The sink whose screen started this read on its own. */
  readonly screen?: ModelSink;
  /** Answers it; `answer` is the catch-up answer the sinks' writes come from. */
  deliver(): ChangesAnswer<ModelRows> | null;
  fail(refused: boolean): void;
}

type CatchUpResult = Awaited<ReturnType<CatchUp<ModelRows>>>;

interface Position {
  readonly epoch: string;
  readonly rev: number;
}

/** A screen's rows for one subscription, checked against the positions it is handed. */
class ModelSink implements FeedSink<ModelRow> {
  readonly rows = new Map<string, ModelRow>();
  sub: Subscription | null = null;
  /** Its newest `stale()` read failed, and no read since has answered. */
  failed = false;
  /** A frame naming its kind, or a listening frame of its source, arrived since its newest `stale()`. */
  heard = false;
  private at: Position | null = null;

  constructor(
    private readonly world: ModelWorld,
    readonly kind: ModelKind,
  ) {}

  upsert(rows: readonly ModelRow[]): void {
    this.move(this.world.answering());
    for (const r of rows) {
      this.rows.set(r.id, r);
    }
  }

  remove(keys: readonly string[]): void {
    this.move(this.world.answering());
    for (const k of keys) {
      this.rows.delete(k);
    }
  }

  stale(): Promise<void> {
    return this.world.reread(this);
  }

  /**
   * A full read's answer, taken at `at`. A screen's own read is latest-wins and may answer older
   * rows than the sink holds; only the feed's writes must never move it back.
   */
  replace(rows: readonly ModelRow[], at: Position, own = false): void {
    if (own) {
      this.at = at;
    } else {
      this.move(at);
    }
    this.rows.clear();
    for (const r of rows) {
      this.rows.set(r.id, r);
    }
  }

  private move(at: Position | null): void {
    if (at === null) {
      this.world.violations.push(`${this.kind}: rows written outside a catch-up answer`);
      return;
    }
    if (this.at !== null && this.at.epoch === at.epoch && at.rev < this.at.rev) {
      this.world.violations.push(
        `${this.kind}: moved back from ${String(this.at.rev)} to ${String(at.rev)} within one epoch`,
      );
    }
    this.at = at;
  }
}

export class ModelWorld {
  readonly violations: string[] = [];
  readonly pending: Pending[] = [];
  readonly sinks: ModelSink[] = [];
  readonly feed: Feed<ModelRows>;
  readonly reconnect = vi.fn<() => void>();
  /** Catch-ups and re-reads the feed has asked for. */
  requests = 0;
  private readonly sources = new Map<string, ModelSource>();
  private readonly issued = new Map<string, Set<number>>();
  private epochs = 0;
  private connected = false;
  private resumable = false;
  private missed: Sent[] = [];
  private readonly sent: Sent[] = [];
  private current: ChangesAnswer<ModelRows> | null = null;

  constructor() {
    for (const [kind, name] of Object.entries(KINDS) as [ModelKind, string][]) {
      let src = this.sources.get(name);
      if (src === undefined) {
        src = {
          name,
          logged: (LOGGED_SOURCES as readonly string[]).includes(name),
          kinds: [],
          epoch: this.newEpoch(),
          rev: 1,
          trimmedTo: 1,
          floors: new Map(),
          rows: new Map(),
          log: [],
        };
        this.sources.set(name, src);
      }
      (src.kinds as ModelKind[]).push(kind);
      const keys = SIGNAL_KINDS.has(kind) ? ["doc"] : src.logged ? ["a", "b"] : ["q"];
      src.rows.set(kind, new Map(keys.map((id) => [id, { id, v: 1, epoch: src.epoch }])));
    }
    this.feed = createFeed<ModelRows>({
      feedVersion: FEED_VERSION,
      kinds: KINDS,
      logged: LOGGED_SOURCES,
      catchUp: (q, signal) => this.catchUp(q, signal),
      reconnect: this.reconnect,
      random: () => 0.5,
    });
  }

  /** The catch-up answer being delivered, which every sink write must come from. */
  answering(): Position | null {
    return this.current === null ? null : { epoch: this.current.epoch, rev: this.current.rev };
  }

  /** What a fresh full read of `kind` returns now. */
  expected(kind: ModelKind): ReadonlyMap<string, ModelRow> {
    return this.rowsOf(kind);
  }

  commit(kind: ModelKind, key: string, del: boolean): void {
    const src = this.sourceOf(kind);
    src.rev++;
    src.floors.set(kind, src.rev);
    const rows = this.rowsOf(kind);
    if (del) {
      rows.delete(key);
    } else {
      rows.set(key, { id: key, v: src.rev, epoch: src.epoch });
    }
    src.log.push({ rev: src.rev, kind, key });
    this.publish(changed(src.name, src.epoch, src.rev, [kind]), src);
  }

  drop(): void {
    if (this.connected) {
      this.connected = false;
      this.missed = [];
    }
  }

  /** A connection whose hello resumed replays what it missed before its listening frame. */
  connect(resume: boolean): void {
    if (this.connected) {
      return;
    }
    this.connected = true;
    const replay = resume && this.resumable ? this.missed : [];
    this.missed = [];
    this.resumable = true;
    for (const s of replay) {
      this.send(s);
    }
    const all = [...this.sources.values()].map((s) => ({
      source: s.name,
      epoch: s.epoch,
      floors: Object.fromEntries(s.floors),
    }));
    this.deliverFrame(listening(all, WIRE));
  }

  /** A swap of the source's handle: a new epoch at a lower revision, its open hook's frame. */
  reopen(name: string): void {
    const src = this.mustSource(name);
    src.epoch = this.newEpoch();
    src.rev = Math.max(1, Math.floor(src.rev / 2));
    src.trimmedTo = src.rev;
    src.floors = new Map();
    src.log = [];
    for (const kind of src.kinds) {
      const rows = this.rowsOf(kind);
      if (SIGNAL_KINDS.has(kind)) {
        rows.set("doc", { id: "doc", v: src.rev, epoch: src.epoch });
      } else {
        rows.delete("a");
      }
    }
    this.publish(listening([{ source: src.name, epoch: src.epoch, floors: {} }]), src);
  }

  /** A process restart: every source gets a new epoch, the in-memory one its first revision. */
  restart(): void {
    for (const src of this.sources.values()) {
      src.epoch = this.newEpoch();
      src.floors = new Map();
      src.log = [];
      if (!src.logged) {
        src.rev = 1;
        for (const kind of src.kinds) {
          this.rowsOf(kind).set("q", { id: "q", v: 1, epoch: src.epoch });
        }
      }
      src.trimmedTo = src.rev;
    }
    this.connected = false;
    this.resumable = false;
    this.missed = [];
  }

  trim(name: string): void {
    const src = this.mustSource(name);
    src.trimmedTo = src.rev;
    src.log = [];
  }

  /** Delivers again a frame of the current connection's epochs, as a duplicate would arrive. */
  replay(pick: number): void {
    if (!this.connected) {
      return;
    }
    const live = this.sent.filter((s) => this.mustSource(s.source).epoch === s.epoch);
    const s = live[pick % Math.max(1, live.length)];
    if (s !== undefined) {
      this.deliverFrame(s.frame);
    }
  }

  /** A screen starts its first read of `kind`; its delivery creates the subscription. */
  open(kind: ModelKind): void {
    const snap = this.snapshot(kind);
    const deliver = (): null => {
      const sink = new ModelSink(this, kind);
      sink.replace(snap.rows, snap.at);
      this.sinks.push(sink);
      sink.sub = this.feed.watch(kind, sink, snap.headers);
      return null;
    };
    this.pending.push({ what: "open", source: KINDS[kind], sink: null, deliver, fail: deliver });
  }

  /**
   * The screen of one sink reads again on its own, as a Retry button or a refresh does. Its reads
   * are latest-wins: a refresh joins a read in flight, and the feed's `stale()` aborts a refresh.
   */
  refresh(pick: number): void {
    const sink = this.sinks[pick % Math.max(1, this.sinks.length)];
    if (sink === undefined || this.pending.some((p) => p.sink === sink || p.screen === sink)) {
      return;
    }
    const snap = this.snapshot(sink.kind);
    this.pending.push({
      what: "refresh",
      source: KINDS[sink.kind],
      sink: null,
      screen: sink,
      deliver: () => {
        sink.replace(snap.rows, snap.at, true);
        sink.sub?.seed(snap.headers);
        return null;
      },
      fail: () => {
        // The screen shows its own failure; the feed hears nothing of it.
      },
    });
  }

  /**
   * Quiesces: answers everything, then gives each sink whose failed re-read nothing has retried
   * one commit of its kind, since only a frame retries a failed read that heard none (rule 3).
   */
  async quiesce(): Promise<void> {
    await this.drain();
    for (const s of this.sinks.filter((x) => x.failed && !x.heard)) {
      this.commit(s.kind, s.kind === "settings" ? "doc" : s.kind === "queue" ? "q" : "a", false);
    }
    await this.drain();
  }

  /** Answers (or fails) one pending read or catch-up and lets the feed apply it. */
  async settle(pick: number, fail: false | "retry" | "refused" = false): Promise<void> {
    const [p] = this.pending.splice(pick % Math.max(1, this.pending.length), 1);
    if (p === undefined) {
      return;
    }
    if (fail === false) {
      this.current = p.deliver();
    } else {
      p.fail(fail === "refused");
    }
    await vi.advanceTimersByTimeAsync(0);
    this.current = null;
  }

  /** Answers everything pending and runs every timer, until nothing is left to send. */
  async drain(): Promise<void> {
    for (let round = 0; round < 200; round++) {
      while (this.pending.length > 0) {
        await this.settle(0);
      }
      await vi.advanceTimersByTimeAsync(60_000);
      if (this.pending.length === 0) {
        return;
      }
    }
    this.violations.push("the feed never stops asking");
  }

  reread(sink: ModelSink): Promise<void> {
    this.requests++;
    if (this.pending.some((p) => p.sink === sink)) {
      this.violations.push(`${sink.kind}: a second re-read while one is in flight`);
    }
    const own = this.pending.findIndex((p) => p.screen === sink);
    if (own >= 0) {
      this.pending.splice(own, 1);
    }
    sink.heard = false;
    const snap = this.snapshot(sink.kind);
    const { promise, resolve, reject } = Promise.withResolvers<undefined>();
    this.pending.push({
      what: "read",
      source: KINDS[sink.kind],
      sink,
      deliver: () => {
        sink.failed = false;
        sink.replace(snap.rows, snap.at);
        sink.sub?.seed(snap.headers);
        resolve(undefined);
        return null;
      },
      fail: (noSeed) => {
        sink.failed = true;
        if (noSeed) {
          resolve(undefined);
        } else {
          reject(new Error("read failed"));
        }
      },
    });
    return promise;
  }

  private catchUp(
    q: Parameters<CatchUp<ModelRows>>[0],
    signal: AbortSignal,
  ): Promise<CatchUpResult> {
    this.requests++;
    if (this.pending.some((p) => p.what === "catchup" && p.source === q.source)) {
      this.violations.push(`${q.source}: a second catch-up while one is in flight`);
    }
    if (this.issued.get(`${q.source}/${q.epoch}`)?.has(q.since) !== true) {
      this.violations.push(
        `${q.source}: catch-up pairs epoch ${q.epoch} with ${String(q.since)}, never issued in it`,
      );
    }
    const src = this.mustSource(q.source);
    if (!src.logged) {
      this.violations.push(`${q.source}: catch-up of an unlogged source`);
    }
    const answer = this.changes(src, q);
    const { promise, resolve } = Promise.withResolvers<CatchUpResult>();
    this.pending.push({
      what: "catchup",
      source: q.source,
      sink: null,
      deliver: () => {
        if (signal.aborted) {
          this.violations.push(`${q.source}: an aborted catch-up was answered`);
        }
        resolve({ ok: true, answer });
        return answer;
      },
      fail: (refused) => {
        resolve({ ok: false, failure: refused ? "refused" : "retry" });
      },
    });
    return promise;
  }

  private changes(
    src: ModelSource,
    q: Parameters<CatchUp<ModelRows>>[0],
  ): ChangesAnswer<ModelRows> {
    this.record(src);
    const head = { source: src.name, epoch: src.epoch, rev: src.rev };
    if (q.epoch !== src.epoch || q.since < src.trimmedTo || q.since > src.rev) {
      return { ...head, full: true, upserts: {}, removed: {} };
    }
    const upserts: Partial<Record<ModelKind, ModelRow[]>> = {};
    const removed: Partial<Record<ModelKind, string[]>> = {};
    const signals: string[] = [];
    for (const kind of q.kinds as readonly ModelKind[]) {
      const keys = [
        ...new Set(src.log.filter((c) => c.rev > q.since && c.kind === kind).map((c) => c.key)),
      ];
      if (keys.length === 0) {
        continue;
      }
      if (SIGNAL_KINDS.has(kind)) {
        signals.push(kind);
        continue;
      }
      const rows = this.rowsOf(kind);
      upserts[kind] = keys.flatMap((k) => {
        const r = rows.get(k);
        return r === undefined ? [] : [r];
      });
      removed[kind] = keys.filter((k) => !rows.has(k));
    }
    return { ...head, full: false, upserts, removed, signals };
  }

  private snapshot(kind: ModelKind): {
    readonly rows: readonly ModelRow[];
    readonly at: Position;
    readonly headers: Headers;
  } {
    const src = this.sourceOf(kind);
    this.record(src);
    return {
      rows: [...this.rowsOf(kind).values()],
      at: { epoch: src.epoch, rev: src.rev },
      headers: position([src.name, src.epoch, src.rev]),
    };
  }

  private record(src: ModelSource): void {
    const key = `${src.name}/${src.epoch}`;
    let set = this.issued.get(key);
    if (set === undefined) {
      set = new Set();
      this.issued.set(key, set);
    }
    set.add(src.rev);
  }

  private publish(frame: Frame, src: ModelSource): void {
    const s = { frame, source: src.name, epoch: src.epoch };
    if (this.connected) {
      this.send(s);
    } else {
      this.missed.push(s);
    }
  }

  private send(s: Sent): void {
    this.sent.push(s);
    this.deliverFrame(s.frame);
  }

  private deliverFrame(frame: Frame): void {
    const names = frameNames(frame);
    for (const s of this.sinks) {
      if (names.sources.has(KINDS[s.kind]) || names.kinds.has(s.kind)) {
        s.heard = true;
      }
    }
    try {
      this.feed.onFrame(frame);
    } catch (e: unknown) {
      this.violations.push(`onFrame threw: ${String(e)}`);
    }
  }

  private newEpoch(): string {
    return (++this.epochs).toString(16).padStart(32, "0");
  }

  private sourceOf(kind: ModelKind): ModelSource {
    return this.mustSource(KINDS[kind]);
  }

  private mustSource(name: string): ModelSource {
    const src = this.sources.get(name);
    if (src === undefined) {
      throw new Error(`no source ${name}`);
    }
    return src;
  }

  private rowsOf(kind: ModelKind): Map<string, ModelRow> {
    const rows = this.sourceOf(kind).rows.get(kind);
    if (rows === undefined) {
      throw new Error(`no rows of ${kind}`);
    }
    return rows;
  }
}

/** The sources a listening frame names, or the kinds a changed frame names. */
function frameNames(frame: Frame): { sources: Set<string>; kinds: Set<string> } {
  const data = JSON.parse(frame.data) as {
    readonly kinds?: readonly string[];
    readonly sources?: readonly { readonly source: string }[];
  };
  return {
    sources: new Set((data.sources ?? []).map((x) => x.source)),
    kinds: new Set(data.kinds ?? []),
  };
}
