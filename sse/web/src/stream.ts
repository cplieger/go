import type { HeldDiscardCause, LifecycleEvent, RevalidateCause } from "./lifecycle.js";
import { type OnlineManager, createOnlineManager } from "./online.js";
import {
  type ClientEvent,
  type ClientState,
  type Effect,
  type StreamEnd,
  type Timer,
  initialState,
  reduce,
} from "./reducer.js";
import { type StreamState, toStreamState } from "./state.js";
import {
  DEFAULT_RETRY_MS,
  MAX_SCOPE_HEADER_BYTES,
  MAX_SCOPE_TOPICS,
  type TimingConfig,
  WIRE,
  resolveTiming,
} from "./timing.js";
import { type Connection, type TransportFrame, connect } from "./transport.js";
import { type VersionMap, bindListener, staleListener } from "./versions.js";
import {
  type VisibilityInput,
  type VisibilityManager,
  adaptVisibility,
  createVisibilityManager,
} from "./visibility.js";
import { type Cursor, type Hello, compareOffset, validTopic } from "./wire.js";

/**
 * How one stream is built. Four fields are required — `url`, `versions`, `onFrame` and
 * `revalidate` — and the rest have defaults that make a plain browser tab work unconfigured: the
 * timing is filled from DEFAULT_TIMING, both wire bounds are this package's WIRE, and the
 * visibility and network readings are the DOM sources, taken lazily at start(). A consumer replaces
 * those two for a worker, a test, or a host folding several tabs into one connection.
 */
export interface StreamOptions {
  readonly url: string;
  /** Defaults to globalThis.fetch; the place to merge `credentials: "include"` or a bearer header. */
  readonly fetch?: typeof fetch;
  /** Read at every request, so a host may fill `SSE-Client` after construction. */
  readonly headers?: Record<string, string>;
  readonly timing?: Partial<TimingConfig>;
  readonly minWire?: number;
  readonly maxWire?: number;
  readonly visibility?: VisibilityManager;
  readonly online?: OnlineManager;
  /** Keepalive acknowledgement: POST `url` after every `everyBeats`-th keepalive (default 1). */
  readonly alive?: { readonly url: string; readonly everyBeats?: number };
  /** Bound to hello.epoch on every hello; the application fills it on commit. */
  readonly versions: VersionMap;
  /** Application frames only; a throw advances the cursor and schedules revalidate("hello"). */
  readonly onFrame: (frame: Frame) => void;
  readonly onLifecycle?: (ev: LifecycleEvent) => void;
  /** The application's reconciliation, run single-flight; it must pass ctx.signal to every fetch. */
  readonly revalidate: (ctx: RevalidateContext) => Promise<void>;
  /**
   * The server's scope endpoint (`ScopeHandler`). Without it the stream never sends a scope, so its
   * connection carries everything and every `setScope` settles at once.
   */
  readonly scopeUrl?: string;
  /** The scope the first connect presents; `null`, the default, is everything. Needs `scopeUrl`. */
  readonly initialScope?: readonly string[] | null;
}

/** Why a `setScope` promise rejected. */
export type ScopeFailure = "stopped" | "superseded" | "link" | "unsupported";

/** The rejection of a `setScope` promise; `failure` names why. */
export class ScopeError extends Error {
  readonly failure: ScopeFailure;
  constructor(failure: ScopeFailure) {
    super(`setScope: ${failure}`);
    this.name = "ScopeError";
    this.failure = failure;
  }
}

/**
 * One application frame, as `onFrame` receives it: the event name, the data payload exactly as it
 * arrived, and the frame's position when it carried one. Library frames are never delivered here,
 * `id` is null for a frame the server published without one, and a frame whose id is not a valid
 * `epoch:offset` is reported as `bad_cursor` and dropped instead. Delivering a frame advances the
 * cursor past it whether or not the handler threw: a throw earns a revalidation, never a redelivery.
 */
export interface Frame {
  readonly type: string;
  readonly data: string;
  readonly id: Cursor | null;
}

/**
 * The context of one reconciliation run. Runs are single-flight, so a body is never entered while
 * another is in flight, and at most one more run is ever queued behind the current one. Every fetch
 * the body makes must take `signal`: the runtime aborts it at revalidateTimeoutMs and ends the
 * connection rather than waiting, and a body that ignores the signal keeps running against state
 * the runtime has already given up on.
 */
export interface RevalidateContext {
  readonly cause: RevalidateCause;
  /** The version map's epoch when the run started. */
  readonly epoch: string | null;
  readonly generation: number;
  /** The map was just cleared by bind(); the application must run its full body without a digest. */
  readonly full: boolean;
  /** Aborted by stop() and at revalidateTimeoutMs, never by reconnect(). */
  readonly signal: AbortSignal;
}

/**
 * The runtime's handle. Frames and records do not arrive through it — they go to the `onFrame` and
 * `onLifecycle` of the options the stream was created with — so what is left here is the four verbs
 * and the two readings. `start()` is ignored unless the stream is stopped, and `cursor()` is the
 * position the next connect will present as `Last-Event-ID`, or null when it will ask for a fresh
 * hello instead.
 */
export interface Stream {
  start(): void;
  /** For leaving the page: aborts the in-flight revalidate's signal. */
  stop(): void;
  /** Aborts the attempt and connects again in place; ignored in hidden_closed, offline and stopped. */
  reconnect(): void;
  /** The connection state, without the timers the stream keeps for itself. */
  state(): StreamState;
  cursor(): Cursor | null;
  /** Drops the cursor; the next connect presents none and earns a fresh hello. */
  resetCursor(): void;
  /**
   * Replaces the scope: `null` is everything, `[]` broadcasts only, a list those topics as well.
   * Throws a RangeError for a topic outside `validTopic` or more than MAX_SCOPE_TOPICS. Resolves
   * once the server is guaranteed to deliver the scope, with the offset after which it does: read
   * the topics' state after it resolves, including every frame at or below that offset. Rejects
   * with a ScopeError on stop(), or `superseded` when a later call replaced it uncovered.
   */
  setScope(scope: readonly string[] | null): Promise<Cursor>;
}

/** Host-only controls over a stream; reached through streamInternals, not the public interface. */
export interface StreamInternals {
  /** Schedules a revalidate under the single-flight rule, as an effect would. */
  revalidate(cause: RevalidateCause, full: boolean): void;
  /** Sets the queued slot's sticky full without starting a run. */
  queueFull(): void;
  /** The scope the server is guaranteed to hold now; `null` is everything. */
  assured(): readonly string[] | null;
  /** Called with `assured()` every time it changes. Returns the unsubscribe. */
  onScope(listener: (assured: readonly string[] | null) => void): () => void;
  /**
   * The offset after which the current connection delivers `scope`, at least the cursor; null
   * while `assured()` does not cover it or the cursor is null.
   */
  coveredSince(scope: readonly string[] | null): Cursor | null;
}

type HeldEntry =
  | {
      readonly kind: "frame";
      readonly generation: number;
      readonly frame: Frame;
    }
  | {
      readonly kind: "adopt";
      readonly generation: number;
      readonly epoch: string;
      readonly head: string;
    };

interface Slot {
  readonly cause: RevalidateCause;
  readonly full: boolean;
}

interface Run {
  readonly controller: AbortController;
  readonly cause: RevalidateCause;
  readonly full: boolean;
  readonly startedAt: number;
  timer: ReturnType<typeof setTimeout> | null;
}

/** A normalized scope: `null` is everything, otherwise its topics sorted and distinct. */
type ScopeValue = readonly string[] | null;

/** One connection's view of the server's scope, replaced at every hello. */
interface ConnScope {
  /** The hello's `conn`, or null for a connection the client cannot rescope. */
  readonly conn: string | null;
  confirmed: ScopeValue;
  owed: boolean;
  seq: number;
  post: { readonly sent: ScopeValue; readonly controller: AbortController } | null;
  retry: ReturnType<typeof setTimeout> | null;
  attempts: number;
}

interface PendingScope {
  readonly scope: ScopeValue;
  readonly resolve: (offset: Cursor) => void;
  readonly reject: (error: ScopeError) => void;
}

type PostOutcome =
  | { readonly kind: "ok"; readonly offset: string }
  | { readonly kind: "refused" | "transient" | "auth" };

type RuntimeEnd = Extract<StreamEnd, HeldDiscardCause>;

const ALIVE_TIMEOUT_MS = 5_000;
const SCOPE_TIMEOUT_MS = 5_000;
const SCOPE_HEADER = "sse-topics";

/** Validates and normalizes a scope; throws a RangeError for a set the server would refuse. */
export function normalizeScope(scope: readonly string[] | null): ScopeValue {
  if (scope === null) {
    return null;
  }
  for (const topic of scope) {
    if (!validTopic(topic)) {
      throw new RangeError(
        `setScope: a topic of ${topic.length} characters is outside the grammar`,
      );
    }
  }
  const topics = [...new Set(scope)].sort();
  if (topics.length > MAX_SCOPE_TOPICS) {
    throw new RangeError(`setScope: ${topics.length} topics, above MAX_SCOPE_TOPICS`);
  }
  return topics;
}

/** a ∩ b, where null (everything) is the identity. */
export function intersectScope(a: ScopeValue, b: ScopeValue): ScopeValue {
  if (a === null) {
    return b;
  }
  if (b === null) {
    return a;
  }
  const set = new Set(b);
  return a.filter((t) => set.has(t));
}

/** Whether a scope delivers everything b does. */
export function coversScope(a: ScopeValue, b: ScopeValue): boolean {
  if (a === null) {
    return true;
  }
  if (b === null) {
    return false;
  }
  const set = new Set(a);
  return b.every((t) => set.has(t));
}

function sameScope(a: ScopeValue, b: ScopeValue): boolean {
  return coversScope(a, b) && coversScope(b, a);
}

/** The copied headers without any `SSE-Topics`, whatever its case: the stream owns that header. */
function withoutScopeHeader(
  headers: Readonly<Record<string, string>> | undefined,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(headers ?? {})) {
    if (name.toLowerCase() !== SCOPE_HEADER) {
      out[name] = value;
    }
  }
  return out;
}

const OFFSET_RE = /^(0|[1-9][0-9]{0,15})$/;

/** Classifies a scope POST answer; a 200 must carry this connection's epoch and an offset. */
async function classifyScopeAnswer(response: Response, epoch: string): Promise<PostOutcome> {
  const status = response.status;
  if (status === 401) {
    return { kind: "auth" };
  }
  let body: unknown;
  try {
    body = JSON.parse(await response.text());
  } catch {
    body = null;
  }
  const record = typeof body === "object" && body !== null ? (body as Record<string, unknown>) : {};
  if (status === 200) {
    const offset = record["offset"];
    if (
      !response.redirected &&
      record["epoch"] === epoch &&
      typeof offset === "string" &&
      OFFSET_RE.test(offset)
    ) {
      return { kind: "ok", offset };
    }
    return { kind: "transient" };
  }
  if (
    status === 409 ||
    status === 429 ||
    status >= 500 ||
    (status === 404 && record["code"] === "scope_conn")
  ) {
    return { kind: "transient" };
  }
  return { kind: "refused" };
}

const internals = new WeakMap<Stream, StreamInternals>();

function transportGeneration(event: ClientEvent): number | null {
  switch (event.type) {
    case "connected":
    case "connect_failed":
    case "byte":
    case "stream_ended":
      return event.generation;
    default:
      return null;
  }
}

/** The numeric `status` an error carries, or null. */
export function statusOf(error: unknown): number | null {
  if (typeof error !== "object" || error === null) {
    return null;
  }
  const status = (error as { status?: unknown }).status;
  return typeof status === "number" ? status : null;
}

function describe(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

/**
 * Creates the client runtime. Throws RangeError when `timing.maxBufferBytes` is below MAX_FRAME_BYTES.
 * A 401 stops the stream and emits `auth_lost`, with route `stream` for the stream request, `alive`
 * for the alive acknowledgement and `digest` for a revalidate rejecting with `{status: 401}`; any
 * other status backs off, or fails the run.
 */
export function createStream(opts: StreamOptions): Stream {
  const cfg = resolveTiming(opts.timing);
  if (Object.keys(opts.headers ?? {}).some((name) => name.toLowerCase() === SCOPE_HEADER)) {
    throw new RangeError("createStream: the stream owns SSE-Topics; set the scope with setScope");
  }
  const initialScope = normalizeScope(opts.initialScope ?? null);
  if (initialScope !== null && opts.scopeUrl === undefined) {
    throw new RangeError("createStream: initialScope needs a scopeUrl");
  }
  const doFetch: typeof fetch = opts.fetch ?? ((input, init) => globalThis.fetch(input, init));
  const minWire = opts.minWire ?? WIRE;
  const maxWire = opts.maxWire ?? WIRE;
  const everyBeats = Math.max(1, opts.alive?.everyBeats ?? cfg.aliveEveryBeats);

  let state: ClientState = initialState();
  let cursor: Cursor | null = null;
  const timers = new Map<Timer, ReturnType<typeof setTimeout>>();

  let connection: { readonly generation: number; readonly conn: Connection } | null = null;
  let hello: Hello | null = null;
  let adopted = false;
  let retryMs = DEFAULT_RETRY_MS;
  let beats = 0;
  let aliveScope: AbortController | null = null;

  let held: HeldEntry[] = [];
  let heldFrames = 0;
  let heldBytes = 0;

  let inflight: Run | null = null;
  let queued: Slot | null = null;
  let unverified = false;
  let unverifiedFull = false;
  let binding = false;
  let knownEpoch = opts.versions.epoch();
  const helloRun = { full: false, revalidated: false };
  const reducerRevalidated = (): boolean => helloRun.revalidated;
  let wasHiddenClosed = false;

  let defaultVisibility: VisibilityManager | null = null;
  let defaultOnline: OnlineManager | null = null;
  let unsubscribes: (() => void)[] = [];

  let current: ScopeValue = initialScope;
  let degraded = false;
  let connScope: ConnScope | null = null;
  const presented = new Map<number, { readonly header: boolean; readonly scope: ScopeValue }>();
  const records = new Map<string, Cursor>();
  let wildcard: Cursor | null = null;
  let pending: PendingScope[] = [];
  const scopeListeners = new Set<(assured: ScopeValue) => void>();

  const now = (): number => Date.now();
  const rand = (): number => Math.random();

  function emit(event: LifecycleEvent): void {
    opts.onLifecycle?.(event);
  }

  function authLost(route: "stream" | "alive" | "digest" | "scope"): void {
    stream.stop();
    emit({ kind: "auth_lost", route, status: 401 });
  }

  bindListener(opts.versions, (epoch) => {
    const previous = knownEpoch;
    knownEpoch = epoch;
    if (binding || inflight === null || !queued?.full) {
      return;
    }
    if (epoch === previous) {
      queued = { cause: queued.cause, full: false };
    }
  });
  staleListener(opts.versions, (subject, epoch) => {
    emit({ kind: "stale_stamp", subject, epoch });
  });

  function arm(timer: Timer, ms: number): void {
    disarm(timer);
    timers.set(
      timer,
      setTimeout(() => {
        timers.delete(timer);
        fire(timer);
      }, ms),
    );
  }

  function disarm(timer: Timer): void {
    const handle = timers.get(timer);
    if (handle !== undefined) {
      clearTimeout(handle);
      timers.delete(timer);
    }
  }

  function fire(timer: Timer): void {
    switch (timer) {
      case "watchdog":
        dispatch({ type: "watchdog_fired", now: now(), rand: rand(), retryMs });
        return;
      case "hidden":
        dispatch({ type: "hidden_timer_fired", now: now() });
        if (state.kind === "hidden_closed") {
          wasHiddenClosed = true;
          emit({ kind: "hidden_closed", arm: "timer" });
        }
        return;
      case "backoff":
        dispatch({ type: "backoff_elapsed", now: now() });
        return;
      case "stable":
        dispatch({ type: "stable_elapsed", now: now() });
        return;
    }
  }

  function execute(effect: Effect): void {
    switch (effect.kind) {
      case "connect":
        startConnection(effect.generation);
        return;
      case "abort":
        abortConnection(effect.generation, effect.reason);
        return;
      case "arm":
        arm(effect.timer, effect.ms);
        return;
      case "disarm":
        disarm(effect.timer);
        return;
      case "revalidate":
        if (effect.cause === "hello") {
          helloRun.revalidated = true;
          schedule("hello", helloRun.full);
        } else {
          schedule(effect.cause, false);
        }
        return;
      case "emit":
        emit(effect.event);
        return;
    }
  }

  function dispatch(event: ClientEvent): void {
    const eventGeneration = transportGeneration(event);
    if (eventGeneration !== null && eventGeneration !== state.generation) {
      emit({ kind: "stale_event", type: event.type, generation: eventGeneration });
      return;
    }
    const before = state;
    const reduction = reduce(state, event, cfg);
    state = reduction.state;
    for (const effect of reduction.effects) {
      execute(effect);
    }
    if (before.kind !== state.kind) {
      emit({ kind: "state", from: before.kind, to: state.kind, generation: state.generation });
    }
  }

  function startConnection(generation: number): void {
    hello = null;
    adopted = false;
    retryMs = DEFAULT_RETRY_MS;
    beats = 0;
    aliveScope?.abort();
    aliveScope = new AbortController();
    leaveConnScope();
    const headers = withoutScopeHeader(opts.headers);
    let presentedHeader = false;
    let presentedScope: ScopeValue = null;
    if (opts.scopeUrl !== undefined && !degraded && current !== null) {
      const encoded = JSON.stringify(current);
      if (encoded.length <= MAX_SCOPE_HEADER_BYTES) {
        headers["SSE-Topics"] = encoded;
        presentedHeader = true;
        presentedScope = current;
      }
    }
    presented.set(generation, { header: presentedHeader, scope: presentedScope });
    const conn = connect({
      url: opts.url,
      fetch: doFetch,
      headers,
      cursor,
      minWire,
      maxWire,
      timing: cfg,
      generation,
      callbacks: {
        onByte(g) {
          dispatch({ type: "byte", generation: g, now: now() });
        },
        onConnected: handleConnected,
        onConnectFailed(g, reason) {
          if (g === state.generation) {
            emit({ kind: "connect_failed", reason });
            if (reason.kind === "status" && reason.status === 401) {
              authLost("stream");
              return;
            }
            if (
              reason.kind === "status" &&
              reason.status === 400 &&
              presented.get(g)?.header === true
            ) {
              degrade();
            }
          }
          dispatch({ type: "connect_failed", generation: g, now: now(), reason, rand: rand() });
        },
        onFrame: handleFrame,
        onKeepalive: handleKeepalive,
        onMark: handleMark,
        onUnknownFrame(g, type) {
          if (g === state.generation) {
            emit({ kind: "unknown_frame", type });
          }
        },
        onBadCursor(g, id) {
          if (g === state.generation) {
            emit({ kind: "bad_cursor", id });
          }
        },
        onRetry(g, ms) {
          if (g === state.generation) {
            retryMs = ms;
          }
        },
        onEnd: handleEnd,
      },
    });
    connection = { generation, conn };
  }

  function dropConnection(generation: number, reason: string): void {
    if (connection !== null && connection.generation === generation) {
      connection.conn.abort(reason);
      connection = null;
    }
    aliveScope?.abort();
    aliveScope = null;
  }

  function abortConnection(generation: number, reason: string): void {
    dropConnection(generation, reason);
    discardHeld("abort");
  }

  function handleConnected(g: number, h: Hello): void {
    if (g !== state.generation) {
      emit({ kind: "stale_event", type: "connected", generation: g });
      return;
    }
    hello = h;
    adopted = !h.resumed;
    binding = true;
    const dropped = opts.versions.bind(h.epoch);
    binding = false;
    helloRun.full = dropped > 0;
    helloRun.revalidated = false;
    if (!h.resumed) {
      cursor = { epoch: h.epoch, offset: h.head };
    }
    dispatch({ type: "connected", generation: g, now: now(), hello: h });
    if (state.kind === "open") {
      enterConnScope(g, h);
    }
    if (state.kind !== "open") {
      return;
    }
    if (wasHiddenClosed) {
      wasHiddenClosed = false;
      emit({ kind: "reopened" });
    }
    if (reducerRevalidated()) {
      return;
    }
    if (unverified || helloRun.full) {
      schedule("hello", helloRun.full);
    } else if (queued !== null && inflight === null) {
      const slot = queued;
      queued = null;
      run(slot);
    }
  }

  function handleEnd(g: number, reason: StreamEnd): void {
    if (g !== state.generation) {
      emit({ kind: "stale_event", type: "stream_ended", generation: g });
      return;
    }
    if (reason === "reset:slow" || reason === "reset:shutdown") {
      emit({ kind: "reset", reason });
    }
    dropConnection(g, reason);
    discardHeld("stream_ended");
    dispatch({ type: "stream_ended", generation: g, now: now(), reason, rand: rand(), retryMs });
  }

  function endConnection(reason: RuntimeEnd): void {
    if (state.kind !== "open") {
      return;
    }
    const g = state.generation;
    dropConnection(g, reason);
    discardHeld(reason);
    dispatch({ type: "stream_ended", generation: g, now: now(), reason, rand: rand(), retryMs });
  }

  function isReplay(frame: TransportFrame, h: Hello): boolean {
    return (
      frame.id !== null &&
      h.resumed &&
      frame.id.epoch === h.epoch &&
      compareOffset(frame.id.offset, h.head) <= 0
    );
  }

  function handleFrame(g: number, tf: TransportFrame): void {
    if (g !== state.generation || hello === null) {
      emit({ kind: "stale_event", type: "frame", generation: g });
      return;
    }
    if (!adopted && !isReplay(tf, hello)) {
      observeNonReplay(hello);
    }
    const frame: Frame = { type: tf.type, data: tf.data, id: tf.id };
    if (inflight !== null) {
      hold(frame, tf.bytes);
    } else {
      deliver(frame);
    }
  }

  function observeNonReplay(h: Hello): void {
    adopted = true;
    if (inflight !== null) {
      held.push({ kind: "adopt", generation: state.generation, epoch: h.epoch, head: h.head });
    } else {
      adopt(h.epoch, h.head);
    }
  }

  function adopt(epoch: string, head: string): void {
    if (cursor?.epoch !== epoch || compareOffset(head, cursor.offset) > 0) {
      cursor = { epoch, offset: head };
    }
  }

  function advance(id: Cursor): void {
    if (cursor?.epoch !== id.epoch || compareOffset(id.offset, cursor.offset) > 0) {
      cursor = id;
    }
  }

  function hold(frame: Frame, bytes: number): void {
    held.push({ kind: "frame", generation: state.generation, frame });
    heldFrames++;
    heldBytes += bytes;
    if (heldFrames > cfg.heldMaxFrames || heldBytes > cfg.heldMaxBytes) {
      endConnection("hold_overflow");
    }
  }

  function deliver(frame: Frame): void {
    let threw = false;
    let error: unknown;
    try {
      opts.onFrame(frame);
    } catch (e: unknown) {
      threw = true;
      error = e;
    }
    if (frame.id !== null) {
      advance(frame.id);
    }
    if (threw) {
      emit({ kind: "frame_rejected", type: frame.type, error });
      schedule("hello", false);
    }
  }

  function discardHeld(cause: HeldDiscardCause): void {
    if (held.length === 0) {
      return;
    }
    const length = held.length;
    held = [];
    heldFrames = 0;
    heldBytes = 0;
    emit({ kind: "held_discarded", cause, length });
  }

  function drain(): void {
    if (held.length === 0) {
      return;
    }
    const entries = held;
    held = [];
    heldFrames = 0;
    heldBytes = 0;
    let dropped = 0;
    for (const entry of entries) {
      if (entry.generation !== state.generation) {
        dropped++;
        continue;
      }
      if (entry.kind === "frame") {
        deliver(entry.frame);
      } else {
        adopt(entry.epoch, entry.head);
      }
    }
    emit({ kind: "drain", length: entries.length, dropped });
  }

  /** A position mark advances the cursor under the same hold as a frame: never past a held one. */
  function handleMark(g: number, id: Cursor): void {
    if (g !== state.generation || hello?.epoch !== id.epoch) {
      return;
    }
    if (!adopted) {
      observeNonReplay(hello);
    }
    if (inflight !== null || held.length > 0) {
      held.push({ kind: "adopt", generation: state.generation, epoch: id.epoch, head: id.offset });
    } else {
      advance(id);
    }
  }

  function handleKeepalive(g: number): void {
    if (g !== state.generation || hello === null) {
      return;
    }
    if (!adopted) {
      observeNonReplay(hello);
    }
    if (
      state.kind === "open" &&
      state.hiddenAt !== null &&
      now() - state.hiddenAt >= cfg.hiddenCloseAfterMs
    ) {
      dispatch({ type: "hidden_timer_fired", now: now() });
      if (stream.state().kind === "hidden_closed") {
        wasHiddenClosed = true;
        emit({ kind: "hidden_closed", arm: "read" });
      }
      return;
    }
    if (opts.alive === undefined) {
      return;
    }
    beats++;
    if (beats % everyBeats === 0) {
      acknowledge(opts.alive.url);
    }
  }

  function acknowledge(url: string): void {
    const scope = aliveScope;
    if (scope === null) {
      return;
    }
    const controller = new AbortController();
    const onScopeAbort = (): void => {
      controller.abort();
    };
    scope.signal.addEventListener("abort", onScopeAbort, { once: true });
    const timer = setTimeout(() => {
      controller.abort(new DOMException("alive acknowledgement timed out", "TimeoutError"));
    }, ALIVE_TIMEOUT_MS);
    void doFetch(url, {
      method: "POST",
      headers: { ...opts.headers },
      credentials: "same-origin",
      cache: "no-store",
      signal: controller.signal,
    })
      .then(
        (response) => {
          emit({ kind: "alive_ack", ok: response.ok, status: response.status });
          if (response.status === 401 && state.kind !== "stopped") {
            authLost("alive");
          }
        },
        () => {
          if (!scope.signal.aborted) {
            emit({ kind: "alive_ack", ok: false, status: null });
          }
        },
      )
      .finally(() => {
        clearTimeout(timer);
        scope.signal.removeEventListener("abort", onScopeAbort);
      });
  }

  function schedule(cause: RevalidateCause, full: boolean): void {
    if (inflight !== null) {
      const coalesces =
        cause !== "hello" &&
        inflight.cause !== "hello" &&
        now() - inflight.startedAt < cfg.wakeThrottleMs;
      if (coalesces && !full) {
        return;
      }
      queued = { cause, full: (queued?.full ?? false) || full };
      return;
    }
    const slot: Slot = { cause, full: (queued?.full ?? false) || full };
    queued = null;
    run(slot);
  }

  function run(queuedSlot: Slot): void {
    // A full run that failed stays owed: every start, schedule's or settle's, carries it.
    const slot: Slot = { cause: queuedSlot.cause, full: queuedSlot.full || unverifiedFull };
    const controller = new AbortController();
    const ctx: RevalidateContext = {
      cause: slot.cause,
      epoch: opts.versions.epoch(),
      generation: state.generation,
      full: slot.full,
      signal: controller.signal,
    };
    const current: Run = {
      controller,
      cause: slot.cause,
      full: slot.full,
      startedAt: now(),
      timer: null,
    };
    current.timer = setTimeout(() => {
      timeout(current);
    }, cfg.revalidateTimeoutMs);
    inflight = current;
    emit({ kind: "revalidate", cause: slot.cause, full: slot.full });
    let promise: Promise<void>;
    try {
      promise = Promise.resolve(opts.revalidate(ctx));
    } catch (e: unknown) {
      promise = Promise.reject(e instanceof Error ? e : new Error(String(e)));
    }
    promise.then(
      () => {
        settle(current, false, undefined);
      },
      (e: unknown) => {
        settle(current, true, e);
      },
    );
  }

  function settle(current: Run, rejected: boolean, error: unknown): void {
    if (inflight !== current) {
      return;
    }
    if (rejected && statusOf(error) === 401) {
      authLost("digest");
      return;
    }
    if (current.timer !== null) {
      clearTimeout(current.timer);
    }
    inflight = null;
    if (!rejected) {
      unverified = false;
      if (current.full) {
        unverifiedFull = false;
      }
      drain();
    } else {
      unverified = true;
      unverifiedFull ||= current.full;
      const ended = state.kind === "open";
      emit({ kind: "revalidate_failed", cause: describe(error), latch: true, ended });
      if (ended) {
        endConnection("revalidate_failed");
      } else {
        drain();
      }
    }
    if (queued !== null) {
      const slot = queued;
      queued = null;
      run(slot);
    }
  }

  function timeout(current: Run): void {
    if (inflight !== current) {
      return;
    }
    inflight = null;
    current.controller.abort(new DOMException("revalidate timed out", "TimeoutError"));
    unverified = true;
    unverifiedFull ||= current.full;
    emit({ kind: "revalidate_timeout" });
    if (state.kind === "open") {
      endConnection("hold_timeout");
    } else {
      discardHeld("hold_timeout");
    }
  }

  function assured(): ScopeValue {
    if (connScope === null) {
      return [];
    }
    const post = connScope.post;
    return post === null ? connScope.confirmed : intersectScope(connScope.confirmed, post.sent);
  }

  /** Drops the coverage records of topics that left `assured`, then tells the listeners. */
  function assuredChanged(): void {
    const a = assured();
    if (a !== null) {
      // A topic that stays covered keeps the offset the wildcard gave it.
      if (wildcard !== null) {
        for (const topic of a) {
          if (!records.has(topic)) {
            records.set(topic, wildcard);
          }
        }
        wildcard = null;
      }
      const keep = new Set(a);
      for (const topic of [...records.keys()]) {
        if (!keep.has(topic)) {
          records.delete(topic);
        }
      }
    }
    for (const listener of scopeListeners) {
      listener(a);
    }
  }

  function coveredSince(scope: ScopeValue): Cursor | null {
    if (cursor === null || !coversScope(assured(), scope)) {
      return null;
    }
    return offsetFor(scope, cursor);
  }

  /** The offset after which this connection delivers `scope`: at least the cursor. */
  function offsetFor(scope: ScopeValue, at: Cursor): Cursor {
    let best = at.offset;
    const consider = (rec: Cursor | null): void => {
      if (rec !== null && rec.epoch === at.epoch && compareOffset(rec.offset, best) > 0) {
        best = rec.offset;
      }
    };
    if (scope === null) {
      consider(wildcard);
    } else {
      for (const topic of scope) {
        consider(records.get(topic) ?? wildcard);
      }
    }
    return { epoch: at.epoch, offset: best };
  }

  /** Settles every call `assured` covers, and the earlier ones they superseded. */
  function settleScopes(): void {
    if (connScope === null || cursor === null || pending.length === 0) {
      return;
    }
    const a = assured();
    let last = -1;
    for (let i = pending.length - 1; i >= 0; i--) {
      if (coversScope(a, pending[i]?.scope ?? null)) {
        last = i;
        break;
      }
    }
    if (last < 0) {
      return;
    }
    const at = cursor;
    const settled = pending.slice(0, last + 1);
    pending = pending.slice(last + 1);
    for (const call of settled) {
      if (coversScope(a, call.scope)) {
        call.resolve(offsetFor(call.scope, at));
      } else {
        call.reject(new ScopeError("superseded"));
      }
    }
  }

  function enterConnScope(g: number, h: Hello): void {
    const attempt = presented.get(g) ?? { header: false, scope: null };
    presented.clear();
    records.clear();
    wildcard = null;
    const resume = cursor ?? { epoch: h.epoch, offset: h.head };
    let confirmed: ScopeValue;
    if (h.conn === undefined) {
      confirmed = null;
    } else if (h.conn === null) {
      confirmed = [];
    } else {
      confirmed = attempt.scope;
    }
    connScope = {
      conn: typeof h.conn === "string" && !degraded ? h.conn : null,
      confirmed,
      owed: false,
      seq: 0,
      post: null,
      retry: null,
      attempts: 0,
    };
    if (confirmed === null) {
      wildcard = resume;
    } else {
      for (const topic of confirmed) {
        records.set(topic, resume);
      }
    }
    assuredChanged();
    if (h.conn === null) {
      degrade();
      return;
    }
    settleScopes();
    postOwed();
  }

  function leaveConnScope(): void {
    if (connScope === null) {
      return;
    }
    connScope.post?.controller.abort();
    if (connScope.retry !== null) {
      clearTimeout(connScope.retry);
    }
    connScope = null;
    assuredChanged();
  }

  /** Stops presenting and posting for the stream's life, and leaves a scoped connection. */
  function degrade(): void {
    if (!degraded) {
      degraded = true;
      emit({ kind: "scope_refused" });
    }
    const c = connScope;
    if (c === null) {
      return;
    }
    c.post?.controller.abort();
    c.post = null;
    if (c.retry !== null) {
      clearTimeout(c.retry);
      c.retry = null;
    }
    if (c.confirmed !== null) {
      stream.reconnect();
      return;
    }
    assuredChanged();
    settleScopes();
  }

  function postOwed(): void {
    const c = connScope;
    if (c?.conn == null || degraded || opts.scopeUrl === undefined) {
      return;
    }
    if (c.post !== null || c.retry !== null) {
      return;
    }
    if (sameScope(current, c.confirmed) && !c.owed) {
      return;
    }
    sendScope(c, opts.scopeUrl, current);
  }

  function sendScope(c: ConnScope, url: string, sent: ScopeValue): void {
    const epoch = hello?.epoch ?? "";
    const controller = new AbortController();
    c.seq++;
    const post = { sent, controller };
    c.post = post;
    assuredChanged();
    const timer = setTimeout(() => {
      controller.abort(new DOMException("scope request timed out", "TimeoutError"));
    }, SCOPE_TIMEOUT_MS);
    const headers = withoutScopeHeader(opts.headers);
    headers["Content-Type"] = "application/json";
    void doFetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify({ conn: c.conn, seq: c.seq, topics: sent }),
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
      signal: controller.signal,
    })
      .then(
        (response) => classifyScopeAnswer(response, epoch),
        (): PostOutcome => ({ kind: "transient" }),
      )
      .then((outcome) => {
        scopeOutcome(c, post, outcome);
      })
      .finally(() => {
        clearTimeout(timer);
      });
  }

  function scopeOutcome(
    c: ConnScope,
    post: NonNullable<ConnScope["post"]>,
    outcome: PostOutcome,
  ): void {
    // A connection change or stop() aborted it; the next hello presents the current scope.
    if (connScope !== c || c.post !== post) {
      return;
    }
    const sent = post.sent;
    c.post = null;
    switch (outcome.kind) {
      case "ok": {
        const at: Cursor = { epoch: hello?.epoch ?? "", offset: outcome.offset };
        if (sent === null) {
          wildcard ??= at;
        } else {
          for (const topic of sent) {
            if (!records.has(topic)) {
              records.set(topic, wildcard ?? at);
            }
          }
        }
        c.confirmed = sent;
        c.owed = false;
        c.attempts = 0;
        assuredChanged();
        settleScopes();
        postOwed();
        return;
      }
      case "auth":
        authLost("scope");
        return;
      case "refused":
        degrade();
        return;
      case "transient":
        c.confirmed = intersectScope(c.confirmed, sent);
        c.owed = true;
        assuredChanged();
        if (!coversScope(assured(), current)) {
          stream.reconnect();
          return;
        }
        c.attempts++;
        c.retry = setTimeout(
          () => {
            if (connScope === c) {
              c.retry = null;
              postOwed();
            }
          },
          Math.min(cfg.capMs, cfg.baseMs * 2 ** (c.attempts - 1)),
        );
        return;
    }
  }

  function onVisibility(input: VisibilityInput): void {
    switch (input) {
      case "visible":
        dispatch({ type: "visible", now: now(), rand: rand(), retryMs });
        return;
      case "pageshow":
        dispatch({ type: "pageshow", now: now(), rand: rand(), retryMs });
        return;
      default:
        dispatch({ type: input, now: now() });
    }
  }

  const stream: Stream = {
    start() {
      if (state.kind !== "stopped") {
        return;
      }
      const visibility = opts.visibility ?? (defaultVisibility ??= createVisibilityManager());
      const online = opts.online ?? (defaultOnline ??= createOnlineManager());
      unsubscribes.push(
        adaptVisibility(
          visibility,
          { visible: () => visibility.isVisible(), listen: () => () => undefined },
          onVisibility,
        ),
        online.subscribe((value) => {
          dispatch({ type: value ? "online" : "offline", now: now() });
        }),
      );
      dispatch({
        type: "start",
        now: now(),
        visible: visibility.isVisible(),
        online: online.isOnline(),
      });
    },
    stop() {
      dispatch({ type: "stop" });
      leaveConnScope();
      const rejected = pending;
      pending = [];
      for (const call of rejected) {
        call.reject(new ScopeError("stopped"));
      }
      for (const unsubscribe of unsubscribes) {
        unsubscribe();
      }
      unsubscribes = [];
      if (inflight !== null) {
        const current = inflight;
        inflight = null;
        if (current.timer !== null) {
          clearTimeout(current.timer);
        }
        current.controller.abort(new DOMException("stream stopped", "AbortError"));
      }
      queued = null;
    },
    reconnect() {
      dispatch({ type: "reconnect", now: now() });
    },
    state: () => toStreamState(state),
    cursor: () => cursor,
    resetCursor() {
      cursor = null;
    },
    setScope(scope) {
      const next = normalizeScope(scope);
      current = next;
      return new Promise<Cursor>((resolve, reject) => {
        pending.push({ scope: next, resolve, reject });
        settleScopes();
        postOwed();
      });
    },
  };
  internals.set(stream, {
    revalidate: schedule,
    queueFull() {
      queued = { cause: queued?.cause ?? "hello", full: true };
    },
    assured,
    onScope(listener) {
      scopeListeners.add(listener);
      return () => {
        scopeListeners.delete(listener);
      };
    },
    coveredSince,
  });
  return stream;
}

/** The host-side controls of a stream created by createStream. Throws on a foreign object. */
export function streamInternals(stream: Stream): StreamInternals {
  const controls = internals.get(stream);
  if (controls === undefined) {
    throw new TypeError("streamInternals: stream was not created by createStream");
  }
  return controls;
}
