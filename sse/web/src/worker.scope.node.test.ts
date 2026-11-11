import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LifecycleEvent } from "./lifecycle.js";
import type { TabToWorker, WorkerToTab } from "./port-protocol.js";
import type { Frame, RevalidateContext } from "./stream.js";
import {
  FAKE_CLOCK,
  type ScriptedFetch,
  flush,
  frame,
  helloFrame,
  scriptedFetch,
} from "./test-helpers/scripted-fetch.js";
import { createVersionMap } from "./versions.js";
import { type TabSet, type WorkerHost, createWorkerHost } from "./worker.js";

const A = "aaaaaaaaaaaaaaaa";
const CONN = "AbCdEfGhIjKlMnOpQrStUv";

interface HostRun {
  readonly ctx: RevalidateContext;
  readonly tabs: TabSet;
  resolve(): void;
  reject(reason: unknown): void;
}

interface Posted {
  readonly body: { conn: string; seq: number; topics: string[] | null };
  answer(response: Response): void;
}

interface Host {
  readonly host: WorkerHost;
  readonly sf: ScriptedFetch;
  readonly runs: HostRun[];
  readonly posted: Posted[];
  ofKind<K extends LifecycleEvent["kind"]>(kind: K): Extract<LifecycleEvent, { kind: K }>[];
}

const hosts: WorkerHost[] = [];

/** Frames carry their topic in their data for this harness: `deliver` reads it back. */
function topicOf(f: Frame): string {
  return (JSON.parse(f.data) as { topic: string }).topic;
}

function hostHarness(
  opts: {
    readonly deliver?: (f: Frame, scope: ReadonlySet<string>) => boolean;
    readonly scopeUrl?: string | null;
  } = {},
): Host {
  const sf = scriptedFetch();
  const events: LifecycleEvent[] = [];
  const runs: HostRun[] = [];
  const posted: Posted[] = [];
  sf.onPost(
    (request) =>
      new Promise<Response>((resolve) => {
        posted.push({
          body: JSON.parse(request.body ?? "null") as Posted["body"],
          answer: resolve,
        });
      }),
  );
  const host = createWorkerHost({
    url: "/events",
    fetch: sf.fetch,
    versions: createVersionMap(),
    heartbeatMs: 1000,
    ...(opts.scopeUrl === null ? {} : { scopeUrl: opts.scopeUrl ?? "/scope" }),
    deliver:
      opts.deliver ??
      ((f, scope) => {
        const topic = topicOf(f);
        return topic === "" || scope.has(topic);
      }),
    onLifecycle(ev) {
      events.push(ev);
    },
    revalidate(ctx, tabs) {
      return new Promise<void>((resolve, reject) => {
        runs.push({ ctx, tabs, resolve, reject });
      });
    },
  });
  hosts.push(host);
  return {
    host,
    sf,
    runs,
    posted,
    ofKind(kind) {
      return events.filter((e) => e.kind === kind) as Extract<
        LifecycleEvent,
        { kind: typeof kind }
      >[];
    },
  };
}

interface Tab {
  send(message: TabToWorker): void;
  attach(over?: Partial<Extract<TabToWorker, { type: "attach" }>>): void;
  ofType<K extends WorkerToTab["type"]>(type: K): Extract<WorkerToTab, { type: K }>[];
}

function tab(h: Host, tabId: string): Tab {
  const channel = new MessageChannel();
  const received: WorkerToTab[] = [];
  channel.port2.onmessage = (event: MessageEvent) => {
    received.push(event.data as WorkerToTab);
  };
  h.host.attach(channel.port1);
  const send = (message: TabToWorker): void => {
    channel.port2.postMessage(message);
  };
  return {
    send,
    attach(over = {}) {
      send({
        type: "attach",
        tabId,
        visible: true,
        online: true,
        hadWorker: false,
        tag: "",
        ...over,
      });
    },
    ofType(type) {
      return received.filter((m) => m.type === type) as Extract<
        WorkerToTab,
        { type: typeof type }
      >[];
    },
  };
}

async function settle(): Promise<void> {
  await flush(10);
}

async function hello(h: Host, overrides: Record<string, unknown> = {}): Promise<void> {
  await settle();
  const conn = h.sf.connections.at(-1);
  if (conn === undefined) {
    throw new Error("host opened no connection");
  }
  conn.push(helloFrame({ epoch: A, head: "5", conn: CONN, ...overrides }));
  await settle();
}

function ok(offset: number): Response {
  return new Response(JSON.stringify({ epoch: A, offset: String(offset) }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function topicFrame(topic: string, offset: number): string {
  return frame("message", JSON.stringify({ topic }), `${A}:${String(offset)}`);
}

beforeEach(() => {
  vi.useFakeTimers(FAKE_CLOCK);
});

afterEach(async () => {
  for (const host of hosts.splice(0)) {
    host.close();
  }
  await flush();
  vi.useRealTimers();
});

describe("worker host scope", () => {
  it("presents the union of its tabs' scopes at connect: an empty union is broadcasts only", async () => {
    const h = hostHarness();
    tab(h, "a").attach({ scope: [] });
    await settle();
    expect(h.sf.connections[0]!.request.headers["SSE-Topics"]).toBe("[]");

    const g = hostHarness();
    tab(g, "a").attach({ scope: ["run:a"] });
    tab(g, "b").attach({ scope: null });
    await settle();
    await hello(g);
    expect(g.posted.at(-1)?.body.topics ?? "no POST").toBe("no POST");
  });

  it("delivers each port only the frames its scope draws, broadcasts included, and counts nothing else", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    const b = tab(h, "b");
    a.attach({ scope: ["run:a"] });
    b.attach({ scope: ["run:b"] });
    await settle();
    await hello(h, { resumed: true });
    const conn = h.sf.connections.at(-1)!;
    conn.push(topicFrame("run:a", 6));
    conn.push(topicFrame("run:b", 7));
    conn.push(topicFrame("", 8));
    await settle();
    expect(a.ofType("frame").map((m) => topicOf(m.frame))).toEqual(["run:a", ""]);
    expect(b.ofType("frame").map((m) => topicOf(m.frame))).toEqual(["run:b", ""]);
  });

  it("a frame a port does not draw never counts toward its expiry", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    tab(h, "b").attach({ scope: ["run:b"] });
    await settle();
    await hello(h, { resumed: true });
    const conn = h.sf.connections.at(-1)!;
    for (let i = 0; i < 2_100; i++) {
      conn.push(topicFrame("run:b", 6 + i));
    }
    await settle();
    expect(h.ofKind("port_expired")).toHaveLength(1);
    expect(a.ofType("frame")).toHaveLength(0);
  });

  it("a throwing deliver fails open and is recorded once per generation", async () => {
    const h = hostHarness({
      deliver: () => {
        throw new Error("bad predicate");
      },
    });
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h, { resumed: true });
    const conn = h.sf.connections.at(-1)!;
    conn.push(topicFrame("run:b", 6));
    conn.push(topicFrame("run:b", 7));
    await settle();
    expect(a.ofType("frame")).toHaveLength(2);
    expect(h.ofKind("deliver_failed")).toEqual([
      { kind: "deliver_failed", type: "message", error: "bad predicate" },
    ]);
  });

  it("every state message carries the scope bit, the one start broadcasts included", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach();
    await settle();
    const states = a.ofType("state");
    expect(states.length).toBeGreaterThan(0);
    expect(states.every((m) => m.scope === true)).toBe(true);
  });

  it("a second tab on a topic the union already holds resolves in that turn, with no POST", async () => {
    const h = hostHarness();
    tab(h, "a").attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    const b = tab(h, "b");
    b.attach({ scope: ["run:a"], scopeSeq: 1 });
    await settle();
    expect(b.ofType("scope_set")).toEqual([{ type: "scope_set", seq: 1, epoch: A, offset: "5" }]);
    expect(h.posted).toHaveLength(0);
  });

  it("a widening answers scope_set once the stream's POST is confirmed, at the swap's offset", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    a.send({ type: "set_scope", seq: 3, topics: ["run:a", "run:b"] });
    await settle();
    expect(a.ofType("scope_set")).toHaveLength(0);
    expect(h.posted[0]!.body).toEqual({ conn: CONN, seq: 1, topics: ["run:a", "run:b"] });
    h.posted[0]!.answer(ok(9));
    await settle();
    expect(a.ofType("scope_set")).toEqual([{ type: "scope_set", seq: 3, epoch: A, offset: "9" }]);
  });

  // After a respawn the first tab's attach starts the stream with its scope alone; a second tab
  // attaching while it connects must not read its run before the server carries its topic.
  it("holds a tab's run until the stream is assured of its scope", async () => {
    const h = hostHarness();
    tab(h, "a").attach({ scope: ["run:a"], hadWorker: true });
    await settle();
    const b = tab(h, "b");
    b.attach({ scope: ["run:b"], hadWorker: true });
    await settle();
    await hello(h, { resumed: false });
    // The hello run excludes b, which is still uncovered.
    expect(h.runs).toHaveLength(1);
    expect(b.ofType("revalidate_run")).toHaveLength(0);
    h.posted[0]!.answer(ok(9));
    await settle();
    expect(b.ofType("revalidate_run")).toMatchObject([{ ctx: { cause: "hello", full: true } }]);
  });

  it("a released run that fails is posted full again at the next hello", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    const b = tab(h, "b");
    b.attach({ scope: ["run:a"], hadWorker: true });
    await settle();
    const first = b.ofType("revalidate_run");
    expect(first).toHaveLength(1);
    b.send({ type: "revalidate_failed", runId: first[0]!.runId, cause: "503" });
    await settle();
    h.sf.connections.at(-1)!.end();
    await settle();
    vi.advanceTimersByTime(2_000);
    await settle();
    expect(h.sf.connections).toHaveLength(2);
    await hello(h);
    expect(b.ofType("revalidate_run").map((m) => m.ctx.full)).toEqual([true, true]);
  });

  it("a tab the stream is not assured of sits out a run started while offline", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h, { resumed: true });
    const b = tab(h, "b");
    b.attach({ scope: ["run:b"], scopeSeq: 1, hadWorker: true });
    await settle();
    a.send({ type: "network", online: false });
    await settle();
    a.send({ type: "visibility", ev: "pageshow" });
    await settle();
    const run = h.runs.at(-1)!;
    expect(run.ctx.full).toBe(false);
    void run.tabs.run(run.ctx);
    await settle();
    expect(b.ofType("revalidate_run")).toHaveLength(0);
    expect(b.ofType("scope_set")).toHaveLength(0);
  });

  it("a scope call after a logout stopped the stream fails stopped at once", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    const b = tab(h, "b");
    b.attach({ scope: ["run:a"] });
    await settle();
    b.send({ type: "detach", cause: "logout" });
    await settle();
    a.send({ type: "set_scope", seq: 4, topics: ["run:b"] });
    await settle();
    expect(a.ofType("scope_failed")).toEqual([{ type: "scope_failed", seq: 4, cause: "stopped" }]);
  });

  it("a stop drops the runs held for coverage", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    const b = tab(h, "b");
    b.attach({ scope: ["run:b"], hadWorker: true });
    await settle();
    expect(b.ofType("revalidate_run")).toHaveLength(0);
    a.send({ type: "detach", cause: "logout" });
    await settle();
    vi.advanceTimersByTime(60_000);
    await settle();
    expect(b.ofType("revalidate_run")).toHaveLength(0);
  });

  it("a scope the host's grammar refuses draws everything, and its call still settles", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    a.send({ type: "set_scope", seq: 5, topics: ["run a"] });
    await settle();
    expect(h.posted.at(-1)?.body.topics).toBeNull();
    h.posted.at(-1)!.answer(ok(9));
    await settle();
    expect(a.ofType("scope_set")).toEqual([{ type: "scope_set", seq: 5, epoch: A, offset: "9" }]);
  });

  it("an attach that starts a stopped host settles the scope call it carries at the hello", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"], scopeSeq: 3, hadWorker: true });
    await settle();
    await hello(h);
    expect(a.ofType("scope_failed")).toEqual([]);
    expect(a.ofType("scope_set")).toEqual([{ type: "scope_set", seq: 3, epoch: A, offset: "5" }]);
  });

  it("a host whose revalidate calls tabs.run at once reaches a covered tab in the hello run", async () => {
    const sf = scriptedFetch();
    const got: WorkerToTab[] = [];
    const host = createWorkerHost({
      url: "/events",
      fetch: sf.fetch,
      versions: createVersionMap(),
      heartbeatMs: 1000,
      scopeUrl: "/scope",
      revalidate: (ctx, tabs) => tabs.run(ctx),
    });
    hosts.push(host);
    const channel = new MessageChannel();
    channel.port2.onmessage = (e: MessageEvent) => {
      got.push(e.data as WorkerToTab);
    };
    host.attach(channel.port1);
    channel.port2.postMessage({
      type: "attach",
      tabId: "a",
      visible: true,
      online: true,
      hadWorker: false,
      tag: "",
      scope: ["run:a"],
    } satisfies TabToWorker);
    await settle();
    sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5", conn: CONN, resumed: false }));
    await settle();
    const runs = got.filter((m) => m.type === "revalidate_run") as Extract<
      WorkerToTab,
      { type: "revalidate_run" }
    >[];
    // The shared hello run (not full: nothing was dropped), never a per-port full run instead.
    expect(runs.map((r) => [r.ctx.cause, r.ctx.full])).toEqual([["hello", false]]);
  });

  it("a port repaired while stopped gets its full run after the next start", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    const b = tab(h, "b");
    a.attach({ scope: ["run:a"] });
    b.attach({ scope: ["run:a"] });
    await settle();
    await hello(h, { resumed: true });
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000);
      await settle();
      const hb = a.ofType("heartbeat").at(-1);
      if (hb) {
        a.send({ type: "heartbeat_ack", seq: hb.seq });
      }
      await settle();
    }
    expect(h.ofKind("port_expired")).toHaveLength(1);
    const z = tab(h, "z");
    z.attach({ scope: ["run:a"] });
    await settle();
    z.send({ type: "detach", cause: "logout" });
    await settle();
    expect(h.host.stream().state().kind).toBe("stopped");
    b.send({ type: "heartbeat_ack", seq: b.ofType("heartbeat").at(-1)!.seq });
    await settle();
    expect(h.ofKind("port_repaired")).toHaveLength(1);
    const before = b.ofType("revalidate_run").length;
    tab(h, "c").attach({ scope: ["run:a"] });
    await settle();
    await hello(h, { resumed: true });
    for (const r of h.runs) {
      void r.tabs.run(r.ctx);
    }
    await settle();
    expect(
      b
        .ofType("revalidate_run")
        .slice(before)
        .map((m) => m.ctx.full),
    ).toContain(true);
  });

  it("a call settles at the next hello's offset, never at a cursor from before a stop", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: [] });
    await settle();
    await hello(h);
    const z = tab(h, "z");
    z.attach({ scope: [] });
    await settle();
    z.send({ type: "detach", cause: "logout" });
    await settle();
    const b = tab(h, "b");
    b.attach({ scope: [], scopeSeq: 3, hadWorker: true });
    await settle();
    expect(h.host.stream().state().kind).toBe("connecting");
    expect(b.ofType("scope_set")).toEqual([]);
    await hello(h, { head: "9" });
    expect(b.ofType("scope_set")).toEqual([{ type: "scope_set", seq: 3, epoch: A, offset: "9" }]);
  });

  async function expire(h: Host, keeper: Tab): Promise<void> {
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(1000);
      await settle();
      const hb = keeper.ofType("heartbeat").at(-1);
      if (hb) {
        keeper.send({ type: "heartbeat_ack", seq: hb.seq });
      }
      await settle();
    }
  }

  for (const twice of [false, true]) {
    it(`a retry owed at a stop is posted once after the restart${twice ? ", with a repair owed too" : ""}`, async () => {
      const h = hostHarness();
      const a = tab(h, "a");
      const b = tab(h, "b");
      a.attach({ scope: ["run:a"] });
      b.attach({ scope: ["run:a"] });
      await settle();
      await hello(h, { resumed: true });
      await expire(h, a);
      b.send({ type: "heartbeat_ack", seq: b.ofType("heartbeat").at(-1)!.seq });
      await settle();
      const run = b.ofType("revalidate_run").at(-1)!;
      expect(run.ctx.full).toBe(true);
      b.send({ type: "revalidate_failed", runId: run.runId, cause: "boom" });
      await settle();
      const z = tab(h, "z");
      z.attach({ scope: ["run:a"] });
      await settle();
      z.send({ type: "detach", cause: "logout" });
      await settle();
      if (twice) {
        await expire(h, a);
        b.send({ type: "heartbeat_ack", seq: b.ofType("heartbeat").at(-1)!.seq });
        await settle();
        expect(h.ofKind("port_repaired")).toHaveLength(2);
      }
      const before = b.ofType("revalidate_run").length;
      tab(h, "c").attach({ scope: ["run:a"] });
      await settle();
      await hello(h, { resumed: true });
      const after = b.ofType("revalidate_run").slice(before);
      expect(after.map((m) => [m.ctx.cause, m.ctx.full])).toEqual([["hello", true]]);
    });
  }

  it("without a scope endpoint, a port repaired while stopped gets no run until the next start", async () => {
    const h = hostHarness({ scopeUrl: null });
    const a = tab(h, "a");
    const b = tab(h, "b");
    a.attach();
    b.attach();
    await settle();
    await hello(h, { resumed: true, conn: undefined });
    await expire(h, a);
    const z = tab(h, "z");
    z.attach();
    await settle();
    z.send({ type: "detach", cause: "logout" });
    await settle();
    const before = b.ofType("revalidate_run").length;
    b.send({ type: "heartbeat_ack", seq: b.ofType("heartbeat").at(-1)!.seq });
    await settle();
    expect(h.ofKind("port_repaired")).toHaveLength(1);
    expect(b.ofType("revalidate_run")).toHaveLength(before);
    tab(h, "c").attach();
    await settle();
    await hello(h, { resumed: true, conn: undefined });
    expect(
      b
        .ofType("revalidate_run")
        .slice(before)
        .map((m) => m.ctx.full),
    ).toEqual([true]);
  });

  it("a stopped stream fails every pending scope call with stopped", async () => {
    const h = hostHarness();
    const a = tab(h, "a");
    a.attach({ scope: ["run:a"] });
    await settle();
    await hello(h);
    a.send({ type: "set_scope", seq: 2, topics: ["run:a", "run:b"] });
    await settle();
    const b = tab(h, "b");
    b.attach({ scope: ["run:a"] });
    await settle();
    b.send({ type: "detach", cause: "logout" });
    await settle();
    expect(a.ofType("scope_failed")).toEqual([{ type: "scope_failed", seq: 2, cause: "stopped" }]);
  });
});
