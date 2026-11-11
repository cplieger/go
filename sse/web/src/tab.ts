import type { LifecycleEvent } from "./lifecycle.js";
import { type OnlineManager, createOnlineManager } from "./online.js";
import {
  type PortRevalidateContext,
  type TabToWorker,
  type WorkerToTab,
  isWorkerMessage,
} from "./port-protocol.js";
import type { StreamState } from "./state.js";
import {
  type Frame,
  ScopeError,
  type ScopeFailure,
  type Stream,
  coversScope,
  normalizeScope,
  statusOf,
} from "./stream.js";
import { DEFAULT_TIMING } from "./timing.js";
import type { Subject, VersionMap } from "./versions.js";
import type { Cursor } from "./wire.js";
import {
  type VisibilityInput,
  type VisibilityManager,
  adaptVisibility,
  createVisibilityManager,
} from "./visibility.js";

/** What a host-routed run hands this tab: the port context plus the tab's own signal. */
export type TabRevalidateContext = PortRevalidateContext & { readonly signal: AbortSignal };

/** What `spawn()` returns: a SharedWorker, or anything with its port and error event. */
export interface SharedWorkerLike {
  readonly port: MessagePort;
  addEventListener(type: "error", listener: () => void): void;
}

/** What `fallback()` returns: the per-tab stream and the two records the tab writes into it. */
export interface TabFallback {
  readonly stream: Stream;
  /** The map the stream was built on; `observe()` records into it. */
  readonly versions: VersionMap;
  /** The record the stream reads at every request; the tab writes `SSE-Client` into it. */
  readonly headers: Record<string, string>;
}

/**
 * How this tab attaches. `spawn` is called for every attempt at the worker — the first one, each respawn
 * after a silent window, the re-spawn on `pageshow` when the page came back with no live port, and each
 * recovery probe from fallback mode — so it must build a fresh worker each time rather than return a
 * held one; a throw from it is read as the worker being unavailable. `fallback` is called when the
 * ladder gives up, and immediately where `SharedWorker` is absent. `revalidate` is optional, and a tab
 * without one reports every routed run as done, which is correct only when the host's own body did the
 * reconciliation.
 */
export interface AttachOptions {
  /** Constructs the worker with the fixed URL, name and options; called on every spawn. */
  readonly spawn: () => SharedWorkerLike;
  /** Constructs the per-tab stream when the ladder gives up on the worker. */
  readonly fallback: () => TabFallback;
  readonly onFrame: (frame: Frame) => void;
  readonly onLifecycle?: (ev: LifecycleEvent) => void;
  /** The body, when the host routes a run to this tab. */
  readonly revalidate?: (ctx: TabRevalidateContext) => Promise<void>;
  readonly visibility?: VisibilityManager;
  readonly online?: OnlineManager;
  readonly heartbeatMs?: number;
  /** The SSE-Client value this tab computed for the profile. */
  readonly tag?: string;
  /** Overrides the `typeof SharedWorker` presence test. */
  readonly supported?: boolean;
  /** Bound on a run routed to this tab; defaults to revalidateTimeoutMs. */
  readonly revalidateTimeoutMs?: number;
  /** The scope this tab draws from its first attach; null, the default, is everything. */
  readonly scope?: readonly string[] | null;
}

/**
 * This tab's handle on the profile's stream. Every verb works the same in both modes, which is the point
 * of it: `mode()` reports whether they currently reach the worker host over a port or this tab's own
 * fallback stream, and a consumer needs it for reporting rather than for branching. `state()` is the
 * host's last broadcast in worker mode, so it reads null until the first one arrives, and the fallback
 * stream's own state otherwise.
 */
export interface TabAttachment {
  /** Leaves the host; `logout` also ends the profile's stream, or this tab's fallback stream. */
  detach(cause?: "unload" | "logout"): void;
  /** The host's last state broadcast, or the fallback stream's own state. */
  state(): StreamState | null;
  mode(): "worker" | "fallback";
  /** Reports a stamp this tab applied, into the host's version map or the fallback's. */
  observe(subject: Subject, version: string, epoch: string): void;
  /** Reconnects the stream this tab is attached to; `resetCursor` drops the cursor first. */
  reconnect(opts?: { readonly resetCursor?: boolean }): void;
  /** Presents `tag` as SSE-Client from the next connect, reconnecting once; "" is ignored. */
  setTag(tag: string): void;
  /**
   * Replaces the scope this tab draws, as `Stream.setScope` does: in worker mode through the host,
   * whose union of every tab's scope is the stream's, and in fallback mode on the tab's own stream.
   * Throws a RangeError for a set the server would refuse. Resolves once the tab is delivered the
   * scope, with the offset after which it is; null only against an older host, which delivers
   * everything, and means read in full. Rejects with a ScopeError on detach (`link`), when the
   * stream stops, or `superseded`.
   */
  setScope(scope: readonly string[] | null): Promise<Cursor | null>;
}

interface PendingTabScope {
  readonly seq: number;
  readonly scope: readonly string[] | null;
  readonly resolve: (offset: Cursor | null) => void;
  readonly reject: (error: ScopeError) => void;
}

const WINDOW_BEATS = 3;

interface Link {
  readonly worker: SharedWorkerLike;
  readonly port: MessagePort;
}

function randomId(): string {
  return crypto.randomUUID();
}

function hasSharedWorker(): boolean {
  return typeof (globalThis as { SharedWorker?: unknown }).SharedWorker !== "undefined";
}

/** Attaches this tab to the profile's worker host, or runs the per-tab stream when it cannot. */
export function attachToWorker(opts: AttachOptions): TabAttachment {
  const heartbeatMs = opts.heartbeatMs ?? DEFAULT_TIMING.heartbeatMs;
  const windowMs = WINDOW_BEATS * heartbeatMs;
  const runTimeoutMs = opts.revalidateTimeoutMs ?? DEFAULT_TIMING.revalidateTimeoutMs;
  const visibility = opts.visibility ?? createVisibilityManager();
  const online = opts.online ?? createOnlineManager();
  const tabId = randomId();
  let tag = opts.tag ?? "";

  let mode: "worker" | "fallback" = "worker";
  let link: Link | null = null;
  let fallback: TabFallback | null = null;
  let lastState: StreamState | null = null;
  let watchdog: ReturnType<typeof setTimeout> | null = null;
  let lastPortMessageAt = Date.now();
  let heartbeatSeen = false;
  let silentSpawns = 0;
  let pingSeq = 0;
  let probe: { readonly link: Link; readonly timer: ReturnType<typeof setTimeout> } | null = null;
  let triedThisVisible = false;
  let detached = false;

  let scope = normalizeScope(opts.scope ?? null);
  let scopeSeq = 0;
  let pendingScopes: PendingTabScope[] = [];
  /** Whether this link's host takes set_scope, learnt from its first state message. */
  let capability: "unknown" | "yes" | "no" = "unknown";
  let sentSeq = 0;
  let lastFrameCursor: Cursor | null = null;

  const now = (): number => Date.now();

  function emit(event: LifecycleEvent): void {
    opts.onLifecycle?.(event);
  }

  function post(message: TabToWorker): void {
    link?.port.postMessage(message);
  }

  function disarmWatchdog(): void {
    if (watchdog !== null) {
      clearTimeout(watchdog);
      watchdog = null;
    }
  }

  function armWatchdog(): void {
    disarmWatchdog();
    watchdog = setTimeout(onWatchdog, windowMs);
  }

  function abandon(): void {
    disarmWatchdog();
    if (link === null) {
      return;
    }
    link.port.onmessage = null;
    link.port.close();
    link = null;
  }

  function attachMessage(hadWorker: boolean): TabToWorker {
    capability = "unknown";
    sentSeq = 0;
    const carried =
      pendingScopes.length > 0
        ? pendingTabScope(
            scope,
            () => undefined,
            () => undefined,
          )
        : null;
    if (carried !== null) {
      sentSeq = carried.seq;
    }
    return {
      type: "attach",
      tabId,
      visible: visibility.isVisible(),
      online: online.isOnline(),
      hadWorker,
      tag,
      scope,
      ...(carried === null ? {} : { scopeSeq: carried.seq }),
    };
  }

  /** Records a call under a fresh seq; the newest pending call carries the tab's current scope. */
  function pendingTabScope(
    next: readonly string[] | null,
    resolve: PendingTabScope["resolve"],
    reject: PendingTabScope["reject"],
  ): PendingTabScope {
    const call: PendingTabScope = { seq: ++scopeSeq, scope: next, resolve, reject };
    pendingScopes.push(call);
    return call;
  }

  /** Settles every pending call up to `seq` by the answer to `seq`, superseding what it misses. */
  function settleScopes(
    seq: number,
    answer: { readonly offset: Cursor | null } | ScopeFailure,
  ): void {
    const answered = pendingScopes.find((c) => c.seq === seq);
    const settled = pendingScopes.filter((c) => c.seq <= seq);
    pendingScopes = pendingScopes.filter((c) => c.seq > seq);
    for (const call of settled) {
      if (typeof answer === "string") {
        call.reject(new ScopeError(call === answered ? answer : "superseded"));
      } else if (answered === undefined || coversScope(answered.scope, call.scope)) {
        call.resolve(answer.offset);
      } else {
        call.reject(new ScopeError("superseded"));
      }
    }
  }

  function failScopes(cause: ScopeFailure): void {
    const rejected = pendingScopes;
    pendingScopes = [];
    for (const call of rejected) {
      call.reject(new ScopeError(cause));
    }
  }

  /** Sends the newest pending call to a host that takes it, or answers it the old-host way. */
  function flushScopes(): void {
    const latest = pendingScopes[pendingScopes.length - 1];
    if (mode !== "worker" || link === null || latest === undefined) {
      return;
    }
    if (capability === "no") {
      // An older host never presents SSE-Topics, so its connection already carries everything.
      settleScopes(latest.seq, { offset: lastFrameCursor });
      return;
    }
    if (capability === "yes" && latest.seq > sentSeq) {
      sentSeq = latest.seq;
      post({ type: "set_scope", seq: latest.seq, topics: latest.scope });
    }
  }

  /** Puts the tab's scope on its fallback stream, settling the pending calls from that stream. */
  function scopeFallback(stream: Stream): void {
    const latest = pendingScopes[pendingScopes.length - 1];
    const seq = latest?.seq ?? 0;
    stream.setScope(scope).then(
      (offset) => {
        if (fallback?.stream === stream && seq > 0) {
          settleScopes(seq, { offset });
        }
      },
      (e: unknown) => {
        // A rejection arrives after the tab replaced or dropped the stream it stopped itself, so
        // the calls move on to the next seat rather than failing `stopped`.
        if (fallback?.stream !== stream || seq === 0) {
          return;
        }
        settleScopes(seq, e instanceof ScopeError ? e.failure : "stopped");
      },
    );
  }

  function spawnLink(): Link | null {
    let worker: SharedWorkerLike;
    try {
      worker = opts.spawn();
    } catch {
      return null;
    }
    return { worker, port: worker.port };
  }

  function spawnAndAttach(hadWorker: boolean): void {
    const next = spawnLink();
    if (next === null) {
      fallbackNow("error");
      return;
    }
    link = next;
    heartbeatSeen = false;
    next.worker.addEventListener("error", () => {
      if (link === next) {
        fallbackNow("error");
      }
    });
    next.port.onmessage = (event: MessageEvent) => {
      if (link === next) {
        onMessage(event.data);
      }
    };
    next.port.start();
    post(attachMessage(hadWorker));
    emit({ kind: "worker_spawned" });
    lastPortMessageAt = now();
    if (visibility.isVisible()) {
      armWatchdog();
    }
  }

  function runRevalidate(port: MessagePort, runId: number, ctx: PortRevalidateContext): void {
    const reply = (message: TabToWorker): void => {
      if (link?.port === port) {
        port.postMessage(message);
      }
    };
    if (opts.revalidate === undefined) {
      reply({ type: "revalidate_done", runId });
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort(new DOMException("revalidate timed out", "TimeoutError"));
    }, runTimeoutMs);
    let promise: Promise<void>;
    try {
      promise = Promise.resolve(opts.revalidate({ ...ctx, signal: controller.signal }));
    } catch (e: unknown) {
      promise = Promise.reject(e instanceof Error ? e : new Error(String(e)));
    }
    promise
      .then(
        () => {
          reply({ type: "revalidate_done", runId });
        },
        (e: unknown) => {
          const status = statusOf(e);
          reply({
            type: "revalidate_failed",
            runId,
            cause: e instanceof Error ? e.message : String(e),
            ...(status === null ? {} : { status }),
          });
        },
      )
      .finally(() => {
        clearTimeout(timer);
      });
  }

  function onMessage(data: unknown): void {
    if (!isWorkerMessage(data) || link === null) {
      return;
    }
    lastPortMessageAt = now();
    if (visibility.isVisible()) {
      armWatchdog();
    }
    const message: WorkerToTab = data;
    switch (message.type) {
      case "heartbeat":
        heartbeatSeen = true;
        silentSpawns = 0;
        post({ type: "heartbeat_ack", seq: message.seq });
        return;
      case "pong":
      case "stale":
        return;
      case "frame":
        if (message.frame.id !== null) {
          lastFrameCursor = message.frame.id;
        }
        try {
          opts.onFrame(message.frame);
        } catch {
          post({ type: "frame_failed", generation: message.generation, cursor: message.frame.id });
        }
        return;
      case "lifecycle":
        emit(message.event);
        return;
      case "revalidate_run":
        runRevalidate(link.port, message.runId, message.ctx);
        return;
      case "state":
        lastState = message.state;
        if (capability === "unknown") {
          capability = message.scope === true ? "yes" : "no";
          flushScopes();
        }
        return;
      case "scope_set":
        settleScopes(message.seq, { offset: { epoch: message.epoch, offset: message.offset } });
        return;
      case "scope_failed":
        settleScopes(message.seq, message.cause);
        return;
    }
  }

  function onWatchdog(): void {
    watchdog = null;
    if (link === null || !visibility.isVisible()) {
      return;
    }
    emit({ kind: "worker_dead", sinceLastPortMessageMs: now() - lastPortMessageAt });
    silentSpawns = heartbeatSeen ? 0 : silentSpawns + 1;
    if (silentSpawns >= 2) {
      fallbackNow("silent");
      return;
    }
    post({ type: "detach", cause: "respawn" });
    abandon();
    spawnAndAttach(true);
  }

  function startFallback(): void {
    fallback = opts.fallback();
    if (tag !== "") {
      fallback.headers["SSE-Client"] = tag;
    }
    // The first connect presents the tab's scope, and the pending calls settle from it.
    if (scope !== null || pendingScopes.length > 0) {
      scopeFallback(fallback.stream);
    }
    fallback.stream.start();
  }

  function fallbackNow(cause: "silent" | "error"): void {
    post({ type: "detach", cause: "fallback" });
    abandon();
    mode = "fallback";
    triedThisVisible = true;
    startFallback();
    emit({ kind: "worker_unavailable", cause });
  }

  function closeProbe(): void {
    if (probe === null) {
      return;
    }
    clearTimeout(probe.timer);
    probe.link.port.onmessage = null;
    probe.link.port.close();
    probe = null;
  }

  function tryRecover(): void {
    const next = spawnLink();
    if (next === null) {
      return;
    }
    const current: { readonly link: Link; readonly timer: ReturnType<typeof setTimeout> } = {
      link: next,
      timer: setTimeout(closeProbe, windowMs),
    };
    probe = current;
    next.worker.addEventListener("error", () => {
      if (probe === current) {
        closeProbe();
      }
    });
    next.port.onmessage = (event: MessageEvent) => {
      const data: unknown = event.data;
      if (probe !== current || !isWorkerMessage(data) || data.type !== "heartbeat") {
        return;
      }
      clearTimeout(current.timer);
      probe = null;
      fallback?.stream.stop();
      fallback = null;
      mode = "worker";
      link = next;
      heartbeatSeen = true;
      silentSpawns = 0;
      next.port.onmessage = (inner: MessageEvent) => {
        if (link === next) {
          onMessage(inner.data);
        }
      };
      post({ type: "heartbeat_ack", seq: data.seq });
      post(attachMessage(true));
      lastPortMessageAt = now();
      if (visibility.isVisible()) {
        armWatchdog();
      }
      emit({ kind: "worker_recovered" });
    };
    next.port.start();
  }

  function onVisibility(input: VisibilityInput): void {
    if (mode === "fallback") {
      if (input === "visible" && !triedThisVisible) {
        triedThisVisible = true;
        tryRecover();
      } else if (input === "hidden" || input === "pagehide") {
        triedThisVisible = false;
        closeProbe();
      }
      return;
    }
    switch (input) {
      case "visible":
        post({ type: "visibility", ev: "visible" });
        post({ type: "ping", seq: ++pingSeq });
        if (link !== null) {
          lastPortMessageAt = now();
          armWatchdog();
        }
        return;
      case "hidden":
        post({ type: "visibility", ev: "hidden" });
        disarmWatchdog();
        return;
      case "pagehide":
        post({ type: "visibility", ev: "pagehide" });
        abandon();
        return;
      case "pageshow":
        if (link === null) {
          spawnAndAttach(true);
        } else {
          post({ type: "visibility", ev: "pageshow" });
        }
        return;
    }
  }

  const unsubscribes = [
    adaptVisibility(
      visibility,
      { visible: () => visibility.isVisible(), listen: () => () => undefined },
      onVisibility,
    ),
    online.subscribe((value) => {
      post({ type: "network", online: value });
    }),
  ];

  if (opts.supported ?? hasSharedWorker()) {
    spawnAndAttach(false);
  } else {
    mode = "fallback";
    startFallback();
  }

  return {
    detach(cause = "unload") {
      if (detached) {
        return;
      }
      detached = true;
      post({ type: "detach", cause });
      abandon();
      closeProbe();
      for (const unsubscribe of unsubscribes) {
        unsubscribe();
      }
      fallback?.stream.stop();
      failScopes("link");
    },
    state: () => (mode === "fallback" ? (fallback?.stream.state() ?? null) : lastState),
    mode: () => mode,
    observe(subject, version, epoch) {
      if (mode === "fallback") {
        fallback?.versions.observe(subject, version, epoch);
        return;
      }
      post({ type: "observed", subject, version, epoch });
    },
    reconnect(reconnectOpts) {
      const resetCursor = reconnectOpts?.resetCursor ?? false;
      if (mode === "fallback") {
        if (resetCursor) {
          fallback?.stream.resetCursor();
        }
        fallback?.stream.reconnect();
        return;
      }
      post({ type: "reconnect", resetCursor });
    },
    setTag(next) {
      if (next === "") {
        return;
      }
      const changed = next !== tag;
      tag = next;
      if (mode === "fallback") {
        if (changed && fallback !== null) {
          fallback.headers["SSE-Client"] = next;
          fallback.stream.reconnect();
        }
        return;
      }
      post({ type: "set_tag", tag: next });
    },
    setScope(next) {
      const normalized = normalizeScope(next);
      if (detached) {
        return Promise.reject(new ScopeError("link"));
      }
      scope = normalized;
      return new Promise<Cursor | null>((resolve, reject) => {
        pendingTabScope(normalized, resolve, reject);
        if (mode === "fallback") {
          if (fallback !== null) {
            scopeFallback(fallback.stream);
          }
          return;
        }
        flushScopes();
      });
    },
  };
}
