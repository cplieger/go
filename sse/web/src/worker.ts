import type { Changed, Removed } from "./digest.js";
import { createProfileOnlineSource, createProfileVisibilitySource } from "./fold.js";
import type { LifecycleEvent } from "./lifecycle.js";
import { createOnlineManager } from "./online.js";
import {
  type PortRevalidateContext,
  type TabToWorker,
  type WorkerToTab,
  isTabMessage,
} from "./port-protocol.js";
import {
  type Frame,
  type RevalidateContext,
  type Stream,
  type StreamOptions,
  coversScope,
  createStream,
  normalizeScope,
  streamInternals,
} from "./stream.js";
import { DEFAULT_TIMING, MAX_SCOPE_TOPICS, resolveTiming } from "./timing.js";
import { createVisibilityManager } from "./visibility.js";

/** The changed and removed sets of one host-side digest, fanned to the tabs with the run. */
export interface DigestVerdict {
  readonly changed: readonly Changed[];
  readonly removed: readonly Removed[];
}

/** The attached tabs a profile revalidation may be routed to. */
export interface TabSet {
  /**
   * Fans the run to every acknowledging tab; settles when each answered, expired or left.
   * With `verdict`, each tab's context carries `changed` and `removed`, so one digest serves them all.
   * A failed run rejects with a tab's cause and its `status`, a 401 first, so a revalidate that
   * rethrows it reaches `auth_lost` route `digest`.
   */
  run(ctx: RevalidateContext, verdict?: DigestVerdict): Promise<void>;
  size(): number;
}

/**
 * The host's options: a stream's, minus the three it supplies itself — the visibility and network
 * readings, which it folds from its tabs, and `onFrame`, which it spends fanning frames to the ports.
 * `revalidate` is widened to receive the attached TabSet beside the context, so the application, not
 * this package, decides whether the reconciliation body runs in the worker or in the tabs. Everything
 * else is a stream's, `versions` included: the profile has one map, and it lives here.
 */
export interface WorkerHostOptions extends Omit<
  StreamOptions,
  "visibility" | "online" | "onFrame" | "onLifecycle" | "revalidate" | "initialScope"
> {
  /** The application decides whether the body runs here or in the tabs it names. */
  readonly revalidate: (ctx: RevalidateContext, tabs: TabSet) => Promise<void>;
  readonly onLifecycle?: (ev: LifecycleEvent) => void;
  /** Seed for the profile's network reading; defaults to the worker's navigator.onLine. */
  readonly onlineSeed?: boolean;
  readonly heartbeatMs?: number;
  /**
   * Whether a port whose scope is `scope` draws `frame`; absent delivers every frame, and a port
   * whose scope is everything is never asked. Frames carry no topic, so this reads the app's data,
   * and it must pass a frame the app published as a broadcast. A throw delivers the frame.
   */
  readonly deliver?: (frame: Frame, scope: ReadonlySet<string>) => boolean;
}

/** The host's stream: the host owns its scope, the union of its tabs' scopes, so no setScope. */
export type HostStream = Omit<Stream, "setScope">;

/**
 * The profile's stream owner: one stream, one cursor and one version map behind however many tabs.
 * The stream starts when the first tab attaches, and it is stopped by `close()`, by a `logout` detach
 * or by a 401 (createStream's `auth_lost`, which every tab receives), not by the last port going
 * away, since the browser keeps a SharedWorker alive for its owner documents and a tab that
 * navigates is expected back.
 */
export interface WorkerHost {
  /** Called from the SharedWorker's onconnect with each new port. */
  attach(port: MessagePort): void;
  stream(): HostStream;
  /** Stops the stream and closes every port. */
  close(): void;
}

interface PortEntry {
  readonly port: MessagePort;
  tabId: string | null;
  visible: boolean;
  online: boolean;
  lastAckAt: number;
  unacked: number;
  expired: boolean;
  /** The tab's scope; null, the default, is everything. */
  scope: readonly string[] | null;
  /** The set `deliver` checks, rebuilt with the scope. */
  scopeSet: ReadonlySet<string> | null;
  /** The tab's scope calls awaiting coverage, oldest first. */
  pendingScopes: { readonly seq: number; readonly scope: readonly string[] | null }[];
  /** A full run this port is owed once the stream's assured scope covers it. */
  owedRun: boolean;
  /** A released run of this port failed; retried at the next hello or stream-level run. */
  retryRun: boolean;
}

interface Quorum {
  readonly pending: Set<PortEntry>;
  readonly failures: { readonly cause: string; readonly status: number | null }[];
  readonly resolve: () => void;
  readonly reject: (error: Error) => void;
}

const EXPIRY_BEATS = 3;

function cloneSafe(event: LifecycleEvent): LifecycleEvent {
  if (event.kind === "frame_rejected") {
    return {
      ...event,
      error: event.error instanceof Error ? event.error.message : String(event.error),
    };
  }
  return event;
}

function defaultOnline(): boolean {
  const onLine = (globalThis as { readonly navigator?: { readonly onLine?: boolean } }).navigator
    ?.onLine;
  return onLine !== false;
}

/** Creates the profile's stream owner: one stream, one cursor, one version map, many tabs. */
export function createWorkerHost(opts: WorkerHostOptions): WorkerHost {
  const cfg = resolveTiming(opts.timing);
  const heartbeatMs = opts.heartbeatMs ?? DEFAULT_TIMING.heartbeatMs;
  const headers: Record<string, string> = { ...opts.headers };
  const visibility = createProfileVisibilitySource();
  const online = createProfileOnlineSource(opts.onlineSeed ?? defaultOnline());
  const entries = new Map<MessagePort, PortEntry>();
  const byTab = new Map<string, PortEntry>();
  const quorums = new Map<number, Quorum>();
  let runSeq = 0;
  let heartbeatSeq = 0;
  let heartbeat: ReturnType<typeof setInterval> | null = null;
  let tag: string | null = null;
  let lastFrameFailed = -1;
  let lastDeliverFailed = -1;
  let union: readonly string[] | null = null;
  let helloGeneration = -1;

  const now = (): number => Date.now();

  function active(): PortEntry[] {
    const out: PortEntry[] = [];
    for (const entry of entries.values()) {
      if (entry.tabId !== null && !entry.expired) {
        out.push(entry);
      }
    }
    return out;
  }

  function post(entry: PortEntry, message: WorkerToTab): void {
    entry.port.postMessage(message);
  }

  function broadcast(message: WorkerToTab): void {
    for (const entry of active()) {
      post(entry, message);
    }
  }

  /** Every record, the host's own included, reaches the application and every attached tab. */
  function emit(event: LifecycleEvent): void {
    opts.onLifecycle?.(event);
    broadcast({ type: "lifecycle", event: cloneSafe(event) });
  }

  function portContext(ctx: RevalidateContext, verdict?: DigestVerdict): PortRevalidateContext {
    const base = { cause: ctx.cause, epoch: ctx.epoch, generation: ctx.generation, full: ctx.full };
    return verdict === undefined
      ? base
      : { ...base, changed: [...verdict.changed], removed: [...verdict.removed] };
  }

  const tabs: TabSet = {
    run(ctx, verdict) {
      // A port the stream does not yet cover sits the run out and is owed its own, released at
      // coverage, so it never reads state the server is still filtering from it.
      const members: PortEntry[] = [];
      for (const entry of active()) {
        if (covered(entry)) {
          members.push(entry);
          if (entry.retryRun) {
            entry.retryRun = false;
            perPortFull(entry);
          }
        } else {
          entry.owedRun = true;
        }
      }
      if (members.length === 0) {
        return Promise.resolve();
      }
      const runId = ++runSeq;
      return new Promise<void>((resolve, reject) => {
        const quorum: Quorum = { pending: new Set(members), failures: [], resolve, reject };
        quorums.set(runId, quorum);
        ctx.signal.addEventListener(
          "abort",
          () => {
            if (quorums.delete(runId)) {
              reject(new Error("revalidate run aborted"));
            }
          },
          { once: true },
        );
        const message: WorkerToTab = {
          type: "revalidate_run",
          runId,
          ctx: portContext(ctx, verdict),
        };
        for (const member of members) {
          post(member, message);
        }
      });
    },
    size: () => active().length,
  };

  function settleQuorum(runId: number, quorum: Quorum): void {
    if (quorum.pending.size > 0) {
      return;
    }
    quorums.delete(runId);
    const failure = quorum.failures.find((f) => f.status === 401) ?? quorum.failures[0];
    if (failure === undefined) {
      quorum.resolve();
    } else if (failure.status === null) {
      quorum.reject(new Error(failure.cause));
    } else {
      quorum.reject(Object.assign(new Error(failure.cause), { status: failure.status }));
    }
  }

  function leaveQuorums(entry: PortEntry): void {
    for (const [runId, quorum] of quorums) {
      if (quorum.pending.delete(entry)) {
        settleQuorum(runId, quorum);
      }
    }
  }

  const stream = createStream({
    ...opts,
    headers,
    visibility: createVisibilityManager(visibility),
    online: createOnlineManager(online),
    onFrame(frame) {
      const message: WorkerToTab = { type: "frame", frame, generation: stream.state().generation };
      for (const entry of active()) {
        if (!draws(entry, frame)) {
          continue;
        }
        if (entry.unacked >= cfg.heldMaxFrames) {
          expire(entry);
          continue;
        }
        post(entry, message);
        entry.unacked++;
      }
    },
    onLifecycle(event) {
      if (event.kind === "state") {
        broadcast({ type: "state", state: stream.state(), scope: true });
        if (event.to === "stopped") {
          failPendingScopes("stopped");
        }
      }
      emit(event);
    },
    revalidate: (ctx) => opts.revalidate(ctx, tabs),
  });
  const controls = streamInternals(stream);
  controls.onScope(() => {
    const generation = stream.state().generation;
    const hello = generation !== helloGeneration && stream.state().kind === "open";
    if (hello) {
      helloGeneration = generation;
    }
    for (const entry of entries.values()) {
      settlePort(entry);
      if (hello && entry.retryRun && covered(entry)) {
        entry.retryRun = false;
        perPortFull(entry);
      }
    }
  });

  /** Whether the port's scope draws the frame; a throwing predicate fails open, recorded once. */
  function draws(entry: PortEntry, frame: Frame): boolean {
    if (entry.scopeSet === null || opts.deliver === undefined) {
      return true;
    }
    try {
      return opts.deliver(frame, entry.scopeSet);
    } catch (e: unknown) {
      const generation = stream.state().generation;
      if (lastDeliverFailed !== generation) {
        lastDeliverFailed = generation;
        emit({
          kind: "deliver_failed",
          type: frame.type,
          error: e instanceof Error ? e.message : String(e),
        });
      }
      return true;
    }
  }

  /**
   * Whether a run may read this port's state now. With no scope endpoint the connection carries
   * everything. Between connections the next connect presents the union, this port included, and
   * replays from the cursor, so a read now misses nothing. An open or connecting stream must be
   * assured of the port's scope: an attempt in flight presented the union as it stood then.
   */
  function covered(entry: PortEntry): boolean {
    if (opts.scopeUrl === undefined) {
      return true;
    }
    const kind = stream.state().kind;
    if (kind !== "open" && kind !== "connecting") {
      return true;
    }
    return coversScope(controls.assured(), entry.scope);
  }

  /** The union of the attached tabs' scopes, expired ones included; past the bound, everything. */
  function computeUnion(): readonly string[] | null {
    const topics = new Set<string>();
    for (const entry of entries.values()) {
      if (entry.tabId === null) {
        continue;
      }
      if (entry.scope === null) {
        return null;
      }
      for (const topic of entry.scope) {
        topics.add(topic);
      }
    }
    return topics.size > MAX_SCOPE_TOPICS ? null : [...topics].sort();
  }

  function updateUnion(): void {
    const next = computeUnion();
    if (
      next === union ||
      (next !== null && union !== null && next.join("\n") === union.join("\n"))
    ) {
      return;
    }
    union = next;
    // The host owns the stream's scope; a superseded call is expected and needs no handling.
    stream.setScope(next).catch(() => undefined);
  }

  /** Answers the port's pending scope calls the stream now covers, and releases its owed run. */
  function settlePort(entry: PortEntry): void {
    if (!covered(entry)) {
      return;
    }
    if (entry.owedRun) {
      entry.owedRun = false;
      perPortFull(entry);
    }
    // The offset needs the cursor, so a call made while it is null waits for the next hello.
    const at = entry.pendingScopes.length > 0 ? controls.coveredSince(entry.scope) : null;
    if (at === null) {
      return;
    }
    const settled = entry.pendingScopes;
    entry.pendingScopes = [];
    const latest = settled[settled.length - 1];
    for (const call of settled) {
      if (call === latest || coversScope(controls.assured(), call.scope)) {
        post(entry, { type: "scope_set", seq: call.seq, epoch: at.epoch, offset: at.offset });
      } else {
        post(entry, { type: "scope_failed", seq: call.seq, cause: "superseded" });
      }
    }
  }

  /** Sets a port's scope from its tab, recomputes the union and answers what is already covered. */
  function setPortScope(
    entry: PortEntry,
    raw: readonly string[] | null,
    seq: number | undefined,
  ): void {
    let scope: readonly string[] | null;
    try {
      scope = normalizeScope(raw);
    } catch {
      return;
    }
    entry.scope = scope;
    entry.scopeSet = scope === null ? null : new Set(scope);
    if (seq !== undefined) {
      entry.pendingScopes.push({ seq, scope });
    }
    updateUnion();
    settlePort(entry);
  }

  function failPendingScopes(cause: "stopped"): void {
    for (const entry of entries.values()) {
      for (const call of entry.pendingScopes) {
        post(entry, { type: "scope_failed", seq: call.seq, cause });
      }
      entry.pendingScopes = [];
    }
  }

  function expire(entry: PortEntry): void {
    if (entry.expired || entry.tabId === null) {
      return;
    }
    entry.expired = true;
    visibility.detach(entry.tabId);
    online.detach(entry.tabId);
    leaveQuorums(entry);
    emit({ kind: "port_expired", sinceLastAckMs: now() - entry.lastAckAt });
  }

  function repair(entry: PortEntry): void {
    if (entry.tabId === null) {
      return;
    }
    entry.expired = false;
    entry.unacked = 0;
    visibility.attach(entry.tabId, entry.visible);
    online.attach(entry.tabId, entry.online);
    post(entry, { type: "stale" });
    perPortFull(entry);
    emit({ kind: "port_repaired" });
  }

  /**
   * Posts the port a full run of its own, a quorum of one, once the stream covers its scope; until
   * then the run is owed. A failure is retried full at the next hello or stream-level run: a
   * non-full run on the shared map could certify a version this port never read.
   */
  function perPortFull(entry: PortEntry): void {
    if (!covered(entry)) {
      entry.owedRun = true;
      return;
    }
    const runId = ++runSeq;
    quorums.set(runId, {
      pending: new Set([entry]),
      failures: [],
      resolve: () => undefined,
      reject: () => {
        entry.retryRun = true;
      },
    });
    post(entry, {
      type: "revalidate_run",
      runId,
      ctx: {
        cause: "hello",
        epoch: opts.versions.epoch(),
        generation: stream.state().generation,
        full: true,
      },
    });
  }

  // A replaced member keeps its fold entry: the new port takes it over without a hidden flip.
  function remove(entry: PortEntry, cause: string, replaced = false): void {
    entries.delete(entry.port);
    if (entry.tabId !== null) {
      if (byTab.get(entry.tabId) === entry) {
        byTab.delete(entry.tabId);
      }
      if (!replaced) {
        visibility.detach(entry.tabId);
        online.detach(entry.tabId);
      }
      leaveQuorums(entry);
    }
    entry.port.onmessage = null;
    entry.port.close();
    updateUnion();
    emit({ kind: "tab_detached", cause });
    if (entries.size === 0) {
      stopHeartbeat();
    }
  }

  // The empty tag means "none", so it never displaces one a tab already presented.
  function adoptTag(next: string): void {
    if (next === "" || next === tag) {
      return;
    }
    tag = next;
    headers["SSE-Client"] = next;
    stream.reconnect();
  }

  function onAttach(entry: PortEntry, message: Extract<TabToWorker, { type: "attach" }>): void {
    const previous = byTab.get(message.tabId);
    const replaced = previous !== undefined && previous !== entry;
    if (replaced) {
      remove(previous, "replaced", true);
    }
    adoptTag(message.tag);
    entry.tabId = message.tabId;
    entry.visible = message.visible;
    entry.online = message.online;
    entry.lastAckAt = now();
    entry.unacked = 0;
    entry.expired = false;
    byTab.set(message.tabId, entry);
    // The union holds this tab's scope before the stream starts or a run is posted.
    setPortScope(entry, message.scope ?? null, message.scopeSeq);
    const stopped = stream.state().kind === "stopped";
    if (stopped && message.hadWorker) {
      controls.queueFull();
    }
    visibility.attach(message.tabId, message.visible);
    online.attach(message.tabId, message.online);
    if (stopped) {
      stream.start();
    } else if (message.hadWorker) {
      perPortFull(entry);
    }
    const state = stream.state();
    post(entry, { type: "state", state, scope: true });
    emit({ kind: "tab_attached", replaced, state: state.kind });
  }

  function onMessage(entry: PortEntry, message: TabToWorker): void {
    if (message.type === "attach") {
      onAttach(entry, message);
      return;
    }
    if (message.type === "ping") {
      post(entry, { type: "pong", seq: message.seq });
      return;
    }
    if (entry.tabId === null) {
      return;
    }
    switch (message.type) {
      case "visibility":
        if (message.ev === "pagehide") {
          remove(entry, "pagehide");
          return;
        }
        if (message.ev === "visible") {
          entry.visible = true;
        } else if (message.ev === "hidden") {
          entry.visible = false;
        }
        if (!entry.expired) {
          visibility.report(entry.tabId, message.ev);
        }
        return;
      case "network":
        entry.online = message.online;
        if (!entry.expired) {
          online.report(entry.tabId, message.online);
        }
        return;
      case "detach":
        remove(entry, message.cause);
        if (message.cause === "logout") {
          stream.stop();
        }
        return;
      case "heartbeat_ack":
        entry.lastAckAt = now();
        entry.unacked = 0;
        if (entry.expired) {
          repair(entry);
        }
        return;
      case "observed":
        opts.versions.observe(message.subject, message.version, message.epoch);
        return;
      case "reconnect":
        if (message.resetCursor) {
          stream.resetCursor();
        }
        stream.reconnect();
        return;
      case "set_tag":
        adoptTag(message.tag);
        return;
      case "set_scope":
        setPortScope(entry, message.topics, message.seq);
        return;
      case "revalidate_done":
      case "revalidate_failed": {
        const quorum = quorums.get(message.runId);
        if (!quorum?.pending.delete(entry)) {
          return;
        }
        if (message.type === "revalidate_failed") {
          quorum.failures.push({
            cause: message.cause,
            status: typeof message.status === "number" ? message.status : null,
          });
        }
        settleQuorum(message.runId, quorum);
        return;
      }
      case "frame_failed": {
        const generation = stream.state().generation;
        if (message.generation !== generation || lastFrameFailed === generation) {
          return;
        }
        lastFrameFailed = generation;
        emit({ kind: "frame_rejected", type: "tab", error: message.cursor });
        controls.revalidate("hello", false);
        return;
      }
    }
  }

  function tick(): void {
    const seq = ++heartbeatSeq;
    const threshold = EXPIRY_BEATS * heartbeatMs;
    for (const entry of [...entries.values()]) {
      post(entry, { type: "heartbeat", seq });
      if (entry.tabId !== null && !entry.expired && now() - entry.lastAckAt > threshold) {
        expire(entry);
      }
    }
  }

  function startHeartbeat(): void {
    heartbeat ??= setInterval(tick, heartbeatMs);
  }

  function stopHeartbeat(): void {
    if (heartbeat !== null) {
      clearInterval(heartbeat);
      heartbeat = null;
    }
  }

  return {
    attach(port) {
      const entry: PortEntry = {
        port,
        tabId: null,
        visible: false,
        online: true,
        lastAckAt: now(),
        unacked: 0,
        expired: false,
        scope: null,
        scopeSet: null,
        pendingScopes: [],
        owedRun: false,
        retryRun: false,
      };
      entries.set(port, entry);
      port.onmessage = (event: MessageEvent) => {
        const data: unknown = event.data;
        if (entries.get(port) === entry && isTabMessage(data)) {
          onMessage(entry, data);
        }
      };
      port.start();
      startHeartbeat();
    },
    stream: () => stream,
    close() {
      stopHeartbeat();
      for (const entry of [...entries.values()]) {
        remove(entry, "closed");
      }
      stream.stop();
    },
  };
}
