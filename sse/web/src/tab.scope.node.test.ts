import { afterEach, describe, expect, it } from "vitest";
import type { TabToWorker, WorkerToTab } from "./port-protocol.js";
import type { ClientState } from "./reducer.js";
import { ScopeError, type Stream } from "./stream.js";
import { type SharedWorkerLike, type TabAttachment, attachToWorker } from "./tab.js";
import { createVersionMap } from "./versions.js";
import {
  type VisibilityEvent,
  type VisibilitySource,
  createVisibilityManager,
} from "./visibility.js";
import type { Cursor } from "./wire.js";

// Real timers: the ports deliver on the event loop, which faked timers would stop. HB is short for
// the tests that need the heartbeat watchdog to fire; STEADY_HB keeps it quiet under load in the rest.
const HB = 30;
const STEADY_HB = 5_000;
const A = "aaaaaaaaaaaaaaaa";

function tick(ms = 0): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function waitFor(what: string, pred: () => boolean, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await tick(5);
  }
}

interface FakeWorker extends SharedWorkerLike {
  readonly sent: TabToWorker[];
  host(message: WorkerToTab): void;
}

function fakeWorker(): FakeWorker {
  const channel = new MessageChannel();
  const sent: TabToWorker[] = [];
  channel.port2.onmessage = (event: MessageEvent) => {
    sent.push(event.data as TabToWorker);
  };
  channel.port2.start();
  return {
    port: channel.port1 as unknown as MessagePort,
    sent,
    addEventListener() {
      // The fake never errors.
    },
    host(message) {
      channel.port2.postMessage(message);
    },
  };
}

/** A fallback stream whose setScope calls the test settles by hand. */
interface FakeFallback extends Stream {
  readonly scopes: {
    readonly scope: readonly string[] | null;
    resolve(c: Cursor): void;
    reject(e: unknown): void;
  }[];
  readonly calls: string[];
}

const STOPPED: ClientState = { kind: "stopped", generation: 0 };

function fakeFallback(): FakeFallback {
  const scopes: FakeFallback["scopes"] = [];
  const calls: string[] = [];
  return {
    scopes,
    calls,
    start: () => void calls.push("start"),
    stop: () => {
      calls.push("stop");
      for (const s of scopes.splice(0)) {
        s.reject(new ScopeError("stopped"));
      }
    },
    reconnect: () => void calls.push("reconnect"),
    resetCursor: () => void calls.push("resetCursor"),
    state: () => STOPPED,
    cursor: () => null,
    setScope(scope) {
      return new Promise<Cursor>((resolve, reject) => {
        scopes.push({ scope, resolve, reject });
      });
    },
  };
}

function hiddenThenVisible(): {
  readonly manager: ReturnType<typeof createVisibilityManager>;
  fire(ev: VisibilityEvent): void;
} {
  let visible = true;
  let emit: ((ev: VisibilityEvent) => void) | null = null;
  const source: VisibilitySource = {
    visible: () => visible,
    listen(cb) {
      emit = cb;
      return () => {
        emit = null;
      };
    },
  };
  return {
    manager: createVisibilityManager(source),
    fire(ev) {
      visible = ev !== "hidden" && ev !== "pagehide";
      emit?.(ev);
    },
  };
}

const live: TabAttachment[] = [];

afterEach(() => {
  for (const a of live.splice(0)) {
    a.detach();
  }
});

function track(p: Promise<Cursor | null>): { value?: Cursor | null; error?: unknown } {
  const out: { value?: Cursor | null; error?: unknown } = {};
  p.then(
    (v) => {
      out.value = v;
    },
    (e: unknown) => {
      out.error = e;
    },
  );
  return out;
}

const STATE: Extract<WorkerToTab, { type: "state" }>["state"] = {
  kind: "open",
  generation: 1,
  attempt: 0,
  keepaliveMs: 15000,
  visible: true,
  online: true,
};

describe("tab scope", () => {
  it("attaches with its scope, waits for the first state, then sends set_scope and settles on scope_set", async () => {
    const workers: FakeWorker[] = [];
    const attachment = attachToWorker({
      heartbeatMs: STEADY_HB,
      supported: true,
      scope: ["run:a"],
      spawn: () => {
        const w = fakeWorker();
        workers.push(w);
        return w;
      },
      fallback: () => ({ stream: fakeFallback(), versions: createVersionMap(), headers: {} }),
      onFrame: () => undefined,
    });
    live.push(attachment);
    await waitFor("attach", () => workers[0]!.sent.length > 0);
    expect(workers[0]!.sent[0]).toMatchObject({ type: "attach", scope: ["run:a"] });
    const call = track(attachment.setScope(["run:b", "run:a"]));
    await tick(20);
    expect(workers[0]!.sent.some((m) => m.type === "set_scope")).toBe(false);
    workers[0]!.host({ type: "state", state: STATE, scope: true });
    await waitFor("set_scope", () => workers[0]!.sent.some((m) => m.type === "set_scope"));
    const sent = workers[0]!.sent.find((m) => m.type === "set_scope") as Extract<
      TabToWorker,
      { type: "set_scope" }
    >;
    expect(sent.topics).toEqual(["run:a", "run:b"]);
    workers[0]!.host({ type: "scope_set", seq: sent.seq, epoch: A, offset: "9" });
    await waitFor("resolve", () => call.value !== undefined);
    expect(call.value).toEqual({ epoch: A, offset: "9" });
  });

  it("an answer settles every earlier call, and one its scope does not cover rejects superseded", async () => {
    const workers: FakeWorker[] = [];
    const attachment = attachToWorker({
      heartbeatMs: STEADY_HB,
      supported: true,
      scope: [],
      spawn: () => {
        const w = fakeWorker();
        workers.push(w);
        return w;
      },
      fallback: () => ({ stream: fakeFallback(), versions: createVersionMap(), headers: {} }),
      onFrame: () => undefined,
    });
    live.push(attachment);
    await waitFor("attach", () => workers[0]!.sent.length > 0);
    workers[0]!.host({ type: "state", state: STATE, scope: true });
    await tick(10);
    const wide = track(attachment.setScope(["run:a", "run:b"]));
    const narrow = track(attachment.setScope(["run:a"]));
    const sets = (): Extract<TabToWorker, { type: "set_scope" }>[] =>
      workers[0]!.sent.filter((m) => m.type === "set_scope") as Extract<
        TabToWorker,
        { type: "set_scope" }
      >[];
    await waitFor("two set_scope", () => sets().length === 2);
    workers[0]!.host({ type: "scope_set", seq: sets()[1]!.seq, epoch: A, offset: "7" });
    await waitFor("settled", () => narrow.value !== undefined && wide.error !== undefined);
    expect(narrow.value).toEqual({ epoch: A, offset: "7" });
    expect((wide.error as ScopeError).failure).toBe("superseded");
  });

  it("against an older host, a call resolves with the last frame's cursor, or null before any frame", async () => {
    const workers: FakeWorker[] = [];
    const attachment = attachToWorker({
      heartbeatMs: STEADY_HB,
      supported: true,
      spawn: () => {
        const w = fakeWorker();
        workers.push(w);
        return w;
      },
      fallback: () => ({ stream: fakeFallback(), versions: createVersionMap(), headers: {} }),
      onFrame: () => undefined,
    });
    live.push(attachment);
    const before = track(attachment.setScope(["run:a"]));
    await waitFor("attach", () => workers[0]!.sent.length > 0);
    workers[0]!.host({ type: "state", state: STATE });
    await waitFor("old-host resolve", () => before.value !== undefined);
    expect(before.value).toBeNull();
    workers[0]!.host({
      type: "frame",
      frame: { type: "message", data: "x", id: { epoch: A, offset: "4" } },
      generation: 1,
    });
    await tick(20);
    const after = track(attachment.setScope(["run:b"]));
    await waitFor("resolve", () => after.value !== undefined);
    expect(after.value).toEqual({ epoch: A, offset: "4" });
    expect(workers[0]!.sent.some((m) => m.type === "set_scope")).toBe(false);
  });

  it("a call pending on a worker that dies is carried to the respawned host's attach", async () => {
    const workers: FakeWorker[] = [];
    const attachment = attachToWorker({
      heartbeatMs: HB,
      supported: true,
      spawn: () => {
        const w = fakeWorker();
        workers.push(w);
        return w;
      },
      fallback: () => ({ stream: fakeFallback(), versions: createVersionMap(), headers: {} }),
      onFrame: () => undefined,
    });
    live.push(attachment);
    await waitFor("attach", () => workers[0]!.sent.length > 0);
    workers[0]!.host({ type: "state", state: STATE, scope: true });
    const call = track(attachment.setScope(["run:a"]));
    await waitFor("set_scope", () => workers[0]!.sent.some((m) => m.type === "set_scope"));
    // The worker goes silent; the watchdog respawns it.
    await waitFor("respawn", () => workers.length === 2, 2000);
    await waitFor("second attach", () => workers[1]!.sent.some((m) => m.type === "attach"));
    const attach = workers[1]!.sent.find((m) => m.type === "attach") as Extract<
      TabToWorker,
      { type: "attach" }
    >;
    expect(attach.scope).toEqual(["run:a"]);
    expect(attach.scopeSeq).toBeGreaterThan(0);
    workers[1]!.host({ type: "state", state: STATE, scope: true });
    workers[1]!.host({ type: "scope_set", seq: attach.scopeSeq!, epoch: A, offset: "12" });
    await waitFor("resolve", () => call.value !== undefined);
    expect(call.value).toEqual({ epoch: A, offset: "12" });
  });

  it("in fallback mode a call goes to the tab's own stream, and the first connect carries the scope", async () => {
    const fallbacks: FakeFallback[] = [];
    const attachment = attachToWorker({
      heartbeatMs: HB,
      supported: false,
      scope: ["run:a"],
      spawn: () => fakeWorker(),
      fallback: () => {
        const f = fakeFallback();
        fallbacks.push(f);
        return { stream: f, versions: createVersionMap(), headers: {} };
      },
      onFrame: () => undefined,
    });
    live.push(attachment);
    expect(fallbacks[0]!.scopes.map((s) => s.scope)).toEqual([["run:a"]]);
    expect(fallbacks[0]!.calls).toEqual(["start"]);
    const call = track(attachment.setScope(["run:b"]));
    await tick(5);
    fallbacks[0]!.scopes.at(-1)!.resolve({ epoch: A, offset: "3" });
    await waitFor("resolve", () => call.value !== undefined);
    expect(call.value).toEqual({ epoch: A, offset: "3" });
  });

  it("a call pending in the fallback when a recovery probe wins resolves on the worker, never stopped", async () => {
    const workers: FakeWorker[] = [];
    const fallbacks: FakeFallback[] = [];
    const visibility = hiddenThenVisible();
    let failSpawn = true;
    const attachment = attachToWorker({
      heartbeatMs: HB,
      supported: true,
      visibility: visibility.manager,
      spawn: () => {
        if (failSpawn) {
          throw new Error("no worker");
        }
        const w = fakeWorker();
        workers.push(w);
        return w;
      },
      fallback: () => {
        const f = fakeFallback();
        fallbacks.push(f);
        return { stream: f, versions: createVersionMap(), headers: {} };
      },
      onFrame: () => undefined,
    });
    live.push(attachment);
    expect(attachment.mode()).toBe("fallback");
    const call = track(attachment.setScope(["run:a"]));
    await tick(5);
    failSpawn = false;
    visibility.fire("hidden");
    visibility.fire("visible");
    await waitFor("probe", () => workers.length === 1);
    workers[0]!.host({ type: "heartbeat", seq: 1 });
    await waitFor("recovered attach", () => workers[0]!.sent.some((m) => m.type === "attach"));
    expect(call.error).toBeUndefined();
    const attach = workers[0]!.sent.find((m) => m.type === "attach") as Extract<
      TabToWorker,
      { type: "attach" }
    >;
    expect(attach.scope).toEqual(["run:a"]);
    workers[0]!.host({ type: "state", state: STATE, scope: true });
    workers[0]!.host({ type: "scope_set", seq: attach.scopeSeq!, epoch: A, offset: "7" });
    await waitFor("resolve", () => call.value !== undefined);
    expect(call.value).toEqual({ epoch: A, offset: "7" });
  });

  it("detach rejects every pending call with link, and a bad set throws", async () => {
    const attachment = attachToWorker({
      heartbeatMs: STEADY_HB,
      supported: true,
      spawn: () => fakeWorker(),
      fallback: () => ({ stream: fakeFallback(), versions: createVersionMap(), headers: {} }),
      onFrame: () => undefined,
    });
    expect(() => attachment.setScope(["run a"])).toThrow(RangeError);
    const call = track(attachment.setScope(["run:a"]));
    attachment.detach();
    await waitFor("reject", () => call.error !== undefined);
    expect((call.error as ScopeError).failure).toBe("link");
  });
});
