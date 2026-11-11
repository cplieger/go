import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createOnlineManager } from "./online.js";
import {
  type RevalidateContext,
  ScopeError,
  type Stream,
  coversScope,
  createStream,
  intersectScope,
  normalizeScope,
  streamInternals,
} from "./stream.js";
import {
  FAKE_CLOCK,
  flush,
  frame,
  helloFrame,
  scriptedFetch,
} from "./test-helpers/scripted-fetch.js";
import {
  type Harness,
  fakeOnline,
  fakeVisibility,
  harness,
} from "./test-helpers/stream-harness.js";
import { MAX_SCOPE_HEADER_BYTES, MAX_SCOPE_TOPICS } from "./timing.js";
import { createVersionMap } from "./versions.js";
import { createVisibilityManager } from "./visibility.js";
import type { Cursor } from "./wire.js";

const A = "aaaaaaaaaaaaaaaa";
const B = "bbbbbbbbbbbbbbbb";
const CONN = "AbCdEfGhIjKlMnOpQrStUv";

/** A POST the test answers by hand; `body` is the decoded request. */
interface Posted {
  readonly body: { conn: string; seq: number; topics: string[] | null };
  answer(response: Response): void;
  fail(): void;
}

/** Routes the stream's scope POSTs into a queue the test answers. */
function postQueue(h: Harness): Posted[] {
  const posted: Posted[] = [];
  h.sf.onPost(
    (request) =>
      new Promise<Response>((resolve, reject) => {
        posted.push({
          body: JSON.parse(request.body ?? "null") as Posted["body"],
          answer: resolve,
          fail: () => {
            reject(new TypeError("scripted network failure"));
          },
        });
        request.signal?.addEventListener("abort", () => {
          reject(new DOMException("aborted", "AbortError"));
        });
      }),
  );
  return posted;
}

function ok(offset: number, epoch = A): Response {
  return new Response(JSON.stringify({ epoch, offset: String(offset) }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function refusal(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: code, code }), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/** Collects how a promise settles, without awaiting it. */
function track(p: Promise<Cursor>): { value?: Cursor; error?: unknown } {
  const out: { value?: Cursor; error?: unknown } = {};
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

function scoped(initialScope: readonly string[] | null = ["run:a"]): Harness {
  return harness({ scopeUrl: "/scope", initialScope });
}

/**
 * A bare stream whose lifecycle listener stops it at the hello's open (`state`) or its hello
 * event while `stopAt.at` names one; each revalidate waits for the test to settle it.
 */
function stoppable() {
  const sf = scriptedFetch();
  const versions = createVersionMap();
  const ctxs: RevalidateContext[] = [];
  const settles: { resolve(): void; reject(e: unknown): void }[] = [];
  const stopAt: { at: "state" | "hello" | "none" } = { at: "none" };
  const box: { s?: Stream } = {};
  const stream = createStream({
    url: "/events",
    fetch: sf.fetch,
    versions,
    visibility: createVisibilityManager(fakeVisibility().source),
    online: createOnlineManager(fakeOnline().source),
    onFrame: () => undefined,
    revalidate(ctx) {
      ctxs.push(ctx);
      return new Promise<void>((resolve, reject) => settles.push({ resolve, reject }));
    },
    onLifecycle(ev) {
      if (
        (stopAt.at === "state" && ev.kind === "state" && ev.to === "open") ||
        (stopAt.at === "hello" && ev.kind === "hello")
      ) {
        box.s!.stop();
      }
    },
  });
  box.s = stream;
  return {
    sf,
    versions,
    ctxs,
    settles,
    stopAt,
    stream,
    /** Starts the stopped stream, checks it presents `epoch:head`, and answers a resumed hello. */
    async resumeAt(epoch: string, head: string): Promise<void> {
      stream.start();
      await flush();
      expect(sf.connections.at(-1)!.request.headers["Last-Event-ID"]).toBe(`${epoch}:${head}`);
      sf.connections.at(-1)!.push(helloFrame({ epoch, head, resumed: true, verdict: "resumed" }));
      await flush();
    },
    /** The cause and fullness of every run from `from` on that no stop aborted. */
    live(from = 0): [string, boolean][] {
      return ctxs
        .slice(from)
        .filter((c) => !c.signal.aborted)
        .map((c) => [c.cause, c.full]);
    },
  };
}

const helloWithConn = (extra: Record<string, unknown> = {}): Record<string, unknown> => ({
  head: "5",
  conn: CONN,
  ...extra,
});

describe("scope algebra", () => {
  it("null is the identity of intersection, and covers everything", () => {
    expect(intersectScope(null, ["a"])).toEqual(["a"]);
    expect(intersectScope(["a"], null)).toEqual(["a"]);
    expect(intersectScope(null, null)).toBeNull();
    expect(intersectScope([], ["a"])).toEqual([]);
    expect(intersectScope(["a", "b"], ["b", "c"])).toEqual(["b"]);
    expect(coversScope(null, ["a"])).toBe(true);
    expect(coversScope(["a"], null)).toBe(false);
    expect(coversScope(["a", "b"], ["b"])).toBe(true);
    expect(coversScope(["a"], [])).toBe(true);
  });

  it("normalizes a scope and refuses one the server would refuse", () => {
    expect(normalizeScope(["b", "a", "b"])).toEqual(["a", "b"]);
    expect(normalizeScope(null)).toBeNull();
    expect(() => normalizeScope(["run a"])).toThrow(RangeError);
    expect(() => normalizeScope(["run:é"])).toThrow(RangeError);
    expect(() =>
      normalizeScope(Array.from({ length: MAX_SCOPE_TOPICS + 1 }, (_, i) => `t${i}`)),
    ).toThrow(RangeError);
  });
});

describe("stream scope", () => {
  beforeEach(() => {
    vi.useFakeTimers(FAKE_CLOCK);
    vi.spyOn(Math, "random").mockReturnValue(0);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("refuses an SSE-Topics header of the app's own, in any case, and an initial scope with no endpoint", () => {
    const base = {
      url: "/events",
      versions: createVersionMap(),
      onFrame: () => undefined,
      revalidate: () => Promise.resolve(),
    };
    expect(() => createStream({ ...base, headers: { "sse-topics": "[]" } })).toThrow(RangeError);
    expect(() => createStream({ ...base, initialScope: ["run:a"] })).toThrow(RangeError);
    expect(() => createStream({ ...base, scopeUrl: "/scope", initialScope: ["run a"] })).toThrow(
      RangeError,
    );
  });

  it("presents the scope at connect, and a hello with conn confirms it by presentation", async () => {
    const h = scoped(["run:b", "run:a"]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    expect(h.last().request.headers["SSE-Topics"]).toBe('["run:a","run:b"]');
    const same = track(h.stream.setScope(["run:a", "run:b"]));
    await flush();
    expect(same.value).toEqual({ epoch: A, offset: "5" });
    expect(posted).toHaveLength(0);
  });

  it("widens by POST and resolves at the offset the server swapped at", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    const widen = track(h.stream.setScope(["run:a", "run:b"]));
    await flush();
    expect(posted).toHaveLength(1);
    expect(posted[0]!.body).toEqual({ conn: CONN, seq: 1, topics: ["run:a", "run:b"] });
    expect(widen.value).toBeUndefined();
    posted[0]!.answer(ok(9));
    await flush();
    expect(widen.value).toEqual({ epoch: A, offset: "9" });
    expect(streamInternals(h.stream).coveredSince(["run:b"])).toEqual({ epoch: A, offset: "9" });
  });

  it("a narrowing resolves at once, and its POST follows", async () => {
    const h = scoped(["run:a", "run:b"]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    const narrow = track(h.stream.setScope(["run:a"]));
    await flush();
    expect(narrow.value).toEqual({ epoch: A, offset: "5" });
    expect(posted[0]!.body.topics).toEqual(["run:a"]);
  });

  // An in-flight narrowing may already have landed, so a re-widen inside its round trip is not
  // covered until a POST that sent it is confirmed.
  it("a re-widen inside a narrowing's round trip resolves only at the POST that sent it", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope([]));
    await flush();
    const back = track(h.stream.setScope(["run:a"]));
    await flush();
    expect(back.value).toBeUndefined();
    expect(streamInternals(h.stream).assured()).toEqual([]);
    posted[0]!.answer(ok(7));
    await flush();
    expect(back.value).toBeUndefined();
    expect(posted[1]!.body).toEqual({ conn: CONN, seq: 2, topics: ["run:a"] });
    posted[1]!.answer(ok(11));
    await flush();
    expect(back.value).toEqual({ epoch: A, offset: "11" });
  });

  it("a narrowing whose POST times out is retried, though its scope already equals the confirmed one", async () => {
    const h = scoped(["run:a", "run:b"]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope(["run:a"]));
    await flush();
    vi.advanceTimersByTime(5_000);
    await flush();
    expect(posted).toHaveLength(1);
    vi.advanceTimersByTime(1_000);
    await flush();
    expect(posted).toHaveLength(2);
    expect(posted[1]!.body).toEqual({ conn: CONN, seq: 2, topics: ["run:a"] });
    expect(h.sf.connections).toHaveLength(1);
  });

  it("a 409 for a resent copy re-asserts the scope with a later seq", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope([]));
    await flush();
    const back = track(h.stream.setScope(["run:a"]));
    posted[0]!.answer(refusal(409, "scope_stale"));
    await flush();
    // A widening is owed after an unknown outcome, so the stream reconnects and presents it.
    expect(h.sf.connections).toHaveLength(2);
    expect(h.last().request.headers["SSE-Topics"]).toBe('["run:a"]');
    h.last().push(helloFrame(helloWithConn({ head: "12" })));
    await flush();
    expect(back.value).toEqual({ epoch: A, offset: "12" });
  });

  it("a 200 that is not the scope answer is a transient failure, never a confirmation", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    const widen = track(h.stream.setScope(["run:a", "run:b"]));
    await flush();
    posted[0]!.answer(
      new Response("<html>login</html>", { status: 200, headers: { "content-type": "text/html" } }),
    );
    await flush();
    expect(widen.value).toBeUndefined();
    expect(h.sf.connections).toHaveLength(2);
  });

  it("a deterministic refusal degrades the stream to everything, once, and the call resolves", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope([]));
    await flush();
    posted[0]!.answer(refusal(403, "scope_forbidden"));
    await flush();
    expect(h.ofKind("scope_refused")).toHaveLength(1);
    expect(h.sf.connections).toHaveLength(2);
    expect(h.last().request.headers["SSE-Topics"]).toBeUndefined();
    const later = track(h.stream.setScope(["run:z"]));
    h.last().push(helloFrame(helloWithConn({ head: "20" })));
    await flush();
    expect(later.value).toEqual({ epoch: A, offset: "20" });
    expect(posted).toHaveLength(1);
  });

  it("a 400 at a connect that presented a scope degrades; a 403 there keeps the backoff", async () => {
    const h = scoped();
    h.sf.expect({ status: 400 });
    h.stream.start();
    await flush();
    expect(h.ofKind("scope_refused")).toHaveLength(1);
    vi.advanceTimersByTime(60_000);
    await flush();
    expect(h.last().request.headers["SSE-Topics"]).toBeUndefined();

    const g = scoped();
    g.sf.expect({ status: 403 });
    g.stream.start();
    await flush();
    vi.advanceTimersByTime(60_000);
    await flush();
    expect(g.ofKind("scope_refused")).toHaveLength(0);
    expect(g.last().request.headers["SSE-Topics"]).toBe('["run:a"]');
  });

  it("a hello without conn is an unscoped connection: no POST, and every call settles", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open({ head: "3" });
    const any = track(h.stream.setScope(["run:z"]));
    await flush();
    expect(any.value).toEqual({ epoch: A, offset: "3" });
    expect(posted).toHaveLength(0);
    expect(h.sf.connections).toHaveLength(1);
  });

  it("a hello with a malformed conn degrades instead of failing the attempt", async () => {
    const h = scoped();
    await h.open(helloWithConn({ conn: "x" }));
    expect(h.ofKind("scope_refused")).toHaveLength(1);
    expect(h.sf.connections).toHaveLength(2);
    expect(h.last().request.headers["SSE-Topics"]).toBeUndefined();
  });

  it("a malformed conn on a degraded stream, or with no scopeUrl, reconnects no more", async () => {
    const h = scoped();
    await h.open(helloWithConn({ conn: "x" }));
    expect(h.sf.connections).toHaveLength(2);
    h.last().push(helloFrame(helloWithConn({ conn: "y".repeat(32), head: "6" })));
    await flush();
    expect(h.sf.connections).toHaveLength(2);
    expect(h.stream.state().kind).toBe("open");
    h.last().end();
    await flush();
    vi.advanceTimersByTime(1_500);
    await flush();
    h.last().push(helloFrame(helloWithConn({ conn: "y".repeat(32), head: "7" })));
    await flush();
    expect(h.sf.connections).toHaveLength(3);
    expect(h.stream.state().kind).toBe("open");
    expect(h.ofKind("scope_refused")).toHaveLength(1);

    const plain = harness();
    await plain.open(helloWithConn({ conn: "y".repeat(32) }));
    expect(plain.sf.connections).toHaveLength(1);
    expect(plain.ofKind("scope_refused")).toHaveLength(0);
  });

  it("a 408 or a proxy's status is transient; only the named refusals degrade", async () => {
    for (const answer of [
      refusal(408, "timeout"),
      refusal(421, "misdirected"),
      refusal(400, "bad_gateway_form"),
    ]) {
      const h = scoped(["run:a", "run:b"]);
      const posted = postQueue(h);
      await h.open(helloWithConn());
      track(h.stream.setScope(["run:a"]));
      await flush();
      posted[0]!.answer(answer);
      await flush();
      expect(h.ofKind("scope_refused")).toHaveLength(0);
      expect(h.sf.connections).toHaveLength(1);
    }
  });

  it("a redirected 200 or one from another epoch confirms nothing", async () => {
    const redirected = ok(8);
    Object.defineProperty(redirected, "redirected", { value: true });
    for (const answer of [redirected, ok(8, "bbbbbbbbbbbbbbbb")]) {
      const h = scoped([]);
      const posted = postQueue(h);
      await h.open(helloWithConn());
      const widen = track(h.stream.setScope(["run:a"]));
      await flush();
      posted[0]!.answer(answer);
      await flush();
      expect(widen.value).toBeUndefined();
      expect(h.sf.connections).toHaveLength(2);
    }
  });

  it("a connection's end drops its coverage records, so the next hello starts from its own", async () => {
    const h = scoped([]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    const a = track(h.stream.setScope(["run:a"]));
    await flush();
    posted[0]!.answer(ok(9));
    await flush();
    expect(a.value).toEqual({ epoch: A, offset: "9" });
    h.last().end();
    await flush();
    vi.advanceTimersByTime(1_500);
    await flush();
    expect(h.last().request.headers["SSE-Topics"]).toBe('["run:a"]');
    // A hello with no conn is everything, which keeps no per-topic record of its own.
    h.last().push(helloFrame({ resumed: true, verdict: "resumed", head: "9" }));
    await flush();
    const again = track(h.stream.setScope(["run:a"]));
    await flush();
    expect(again.value).toEqual({ epoch: A, offset: "5" });
  });

  it("between connections nothing is assured: a hidden close sends no POST and a widening waits for the backoff", async () => {
    const h = scoped(["run:a", "run:b"]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    h.visibility.emit("hidden");
    vi.advanceTimersByTime(60_000);
    await flush();
    expect(h.stream.state().kind).toBe("hidden_closed");
    const narrow = track(h.stream.setScope(["run:a"]));
    vi.advanceTimersByTime(20 * 60_000);
    await flush();
    expect(posted).toHaveLength(0);
    expect(narrow.value).toBeUndefined();

    const g = scoped(["run:a"]);
    const gPosted = postQueue(g);
    await g.open(helloWithConn());
    g.last().end();
    await flush();
    expect(g.stream.state().kind).toBe("backoff");
    track(g.stream.setScope(["run:a", "run:b"]));
    await flush();
    expect(gPosted).toHaveLength(0);
    expect(g.sf.connections).toHaveLength(1);
  });

  it("a call made while stopped rejects stopped, and the next start presents its scope", async () => {
    const h = scoped([]);
    await h.open(helloWithConn());
    h.stream.stop();
    const late = h.stream.setScope(["run:z"]);
    await expect(late).rejects.toMatchObject({ failure: "stopped" });
    h.stream.start();
    await flush();
    expect(h.last().request.headers["SSE-Topics"]).toBe('["run:z"]');
  });

  it("a call before the first start waits for the first hello", async () => {
    const h = scoped([]);
    const early = track(h.stream.setScope(["run:a"]));
    await flush();
    expect(early.error).toBeUndefined();
    await h.open(helloWithConn());
    expect(h.sf.connections[0]!.request.headers["SSE-Topics"]).toBe('["run:a"]');
    expect(early.value).toEqual({ epoch: A, offset: "5" });
  });

  it("a hello whose malformed conn degrades still runs its hello run", async () => {
    const h = scoped();
    await h.open({ head: "5", conn: null });
    expect(h.stream.state().kind).toBe("connecting");
    expect(h.runs.map((r) => r.ctx.cause)).toEqual(["hello"]);
  });

  it("a stop made from a lifecycle listener on the hello leaves the hello run owed to the next start", async () => {
    for (const at of ["state", "hello"] as const) {
      const t = stoppable();
      t.stopAt.at = at;
      t.stream.start();
      await flush();
      t.sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5" }));
      await flush();
      expect(t.stream.state().kind).toBe("stopped");
      expect(t.ctxs).toEqual([]);
      // The hello adopted head 5, so the restart's resumed hello must still reconcile.
      t.stopAt.at = "none";
      await t.resumeAt(A, "5");
      expect(t.live()).toEqual([["hello", false]]);
    }
  });

  it("a stop while an epoch change's full run is owed or in flight leaves it owed full", async () => {
    for (const at of ["state", "hello", "outside"] as const) {
      const t = stoppable();
      t.stream.start();
      await flush();
      t.sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5" }));
      await flush();
      t.settles.at(-1)!.resolve();
      await flush();
      t.versions.observe({ kind: "chat", ref: "c1" }, "3", A);
      if (at !== "outside") {
        t.stopAt.at = at;
      }
      t.stream.reconnect();
      await flush();
      // The new epoch's bind drops the held version, so this hello's run is full.
      t.sf.connections.at(-1)!.push(helloFrame({ epoch: B, head: "9" }));
      await flush();
      if (at === "outside") {
        expect(t.ctxs.at(-1)!.full).toBe(true);
        t.stream.stop();
      }
      t.stopAt.at = "none";
      const before = t.ctxs.length;
      await t.resumeAt(B, "9");
      expect(t.live(before)).toEqual([["hello", true]]);
    }
  });

  it("a stop while the hello's run is in flight leaves it owed to the restart's resumed hello", async () => {
    const t = stoppable();
    t.stream.start();
    await flush();
    t.sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5" }));
    await flush();
    expect(t.ctxs).toHaveLength(1);
    t.stream.stop();
    expect(t.ctxs[0]!.signal.aborted).toBe(true);
    await t.resumeAt(A, "5");
    expect(t.live(1)).toEqual([["hello", false]]);
  });

  it("a queued full run a stop clears before its hello stays owed full", async () => {
    const t = stoppable();
    t.stream.start();
    await flush();
    t.sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5" }));
    await flush();
    t.settles.at(-1)!.resolve();
    await flush();
    t.stream.stop();
    // A worker attach on a stopped stream queues a full run, then starts it.
    streamInternals(t.stream).queueFull();
    t.stream.start();
    await flush();
    t.stream.stop();
    const before = t.ctxs.length;
    await t.resumeAt(A, "5");
    expect(t.live(before)).toEqual([["hello", true]]);
  });

  it("a hello run that ends in a 401 stays owed to the next start", async () => {
    const t = stoppable();
    t.stream.start();
    await flush();
    t.sf.connections.at(-1)!.push(helloFrame({ epoch: A, head: "5" }));
    await flush();
    t.settles.at(-1)!.reject({ status: 401 });
    await flush();
    expect(t.stream.state().kind).toBe("stopped");
    await t.resumeAt(A, "5");
    expect(t.live(1)).toEqual([["hello", false]]);
  });

  it("stop ends degraded mode, so the next start presents the scope again", async () => {
    const h = scoped();
    await h.open(helloWithConn({ conn: "x" }));
    expect(h.ofKind("scope_refused")).toHaveLength(1);
    h.stream.stop();
    h.stream.start();
    await flush();
    expect(h.last().request.headers["SSE-Topics"]).toBe('["run:a"]');
  });

  it("a scope over the header bound connects as everything, narrows by POST, and retries a 503 in place", async () => {
    const topics = Array.from({ length: 40 }, (_, i) => `run:${String(i).padStart(100, "x")}`);
    expect(JSON.stringify(normalizeScope(topics)).length).toBeGreaterThan(MAX_SCOPE_HEADER_BYTES);
    const h = scoped(topics);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    expect(h.last().request.headers["SSE-Topics"]).toBeUndefined();
    expect(posted).toHaveLength(1);
    posted[0]!.answer(refusal(503, "unavailable"));
    await flush();
    vi.advanceTimersByTime(1_000);
    await flush();
    expect(posted).toHaveLength(2);
    expect(h.sf.connections).toHaveLength(1);
  });

  it("a call a later one replaced before coverage rejects superseded", async () => {
    const h = scoped([]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope(["run:a"]));
    await flush();
    const b = track(h.stream.setScope(["run:b"]));
    const none = track(h.stream.setScope([]));
    await flush();
    posted[0]!.answer(ok(8));
    await flush();
    expect(none.value).toBeDefined();
    expect(b.error).toBeInstanceOf(ScopeError);
    expect((b.error as ScopeError).failure).toBe("superseded");
  });

  it("stop rejects every pending call", async () => {
    const h = scoped([]);
    postQueue(h);
    await h.open(helloWithConn());
    const pending = track(h.stream.setScope(["run:a"]));
    await flush();
    h.stream.stop();
    await flush();
    expect((pending.error as ScopeError).failure).toBe("stopped");
  });

  it("a 401 on the scope POST is auth_lost with route scope", async () => {
    const h = scoped([]);
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope(["run:a"]));
    await flush();
    posted[0]!.answer(refusal(401, "unauthorized"));
    await flush();
    expect(h.ofKind("auth_lost")).toEqual([{ kind: "auth_lost", route: "scope", status: 401 }]);
  });

  it("a widening to everything gives every topic the confirming offset", async () => {
    const h = scoped();
    const posted = postQueue(h);
    await h.open(helloWithConn());
    track(h.stream.setScope(null));
    await flush();
    posted[0]!.answer(ok(14));
    await flush();
    const internals = streamInternals(h.stream);
    expect(internals.assured()).toBeNull();
    expect(internals.coveredSince(["run:b"])).toEqual({ epoch: A, offset: "14" });
    expect(internals.coveredSince(["run:a"])).toEqual({ epoch: A, offset: "5" });
  });

  it("onScope reports assured at the send, the answer and the hello", async () => {
    const h = scoped();
    const posted = postQueue(h);
    const seen: (readonly string[] | null)[] = [];
    streamInternals(h.stream).onScope((a) => seen.push(a));
    await h.open(helloWithConn());
    track(h.stream.setScope(["run:a", "run:b"]));
    await flush();
    posted[0]!.answer(ok(9));
    await flush();
    expect(seen).toEqual([["run:a"], ["run:a"], ["run:a", "run:b"]]);
  });
});

describe("position marks", () => {
  beforeEach(() => {
    vi.useFakeTimers(FAKE_CLOCK);
    vi.spyOn(Math, "random").mockReturnValue(0);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("a mark advances the cursor at once when nothing is held", async () => {
    const h = harness();
    const conn = await h.open({ head: "2", resumed: true });
    h.runs.splice(0);
    conn.push(`id: ${A}:40\n\n`);
    await flush();
    expect(h.stream.cursor()).toEqual({ epoch: A, offset: "40" });
    expect(h.frames).toHaveLength(0);
  });

  it("a mark held during a revalidation lands after the held frames when it succeeds", async () => {
    const h = harness();
    const conn = await h.open({ head: "2" });
    expect(h.runs).toHaveLength(1);
    conn.push(frame("message", "x", `${A}:3`));
    conn.push(`id: ${A}:40\n\n`);
    await flush();
    expect(h.stream.cursor()).toEqual({ epoch: A, offset: "2" });
    h.runs[0]!.resolve();
    await flush();
    expect(h.stream.cursor()).toEqual({ epoch: A, offset: "40" });
  });

  it("a mark held during a revalidation that times out is dropped with the held frames", async () => {
    const h = harness();
    const conn = await h.open({ head: "2" });
    conn.push(frame("message", "x", `${A}:3`));
    conn.push(`id: ${A}:40\n\n`);
    await flush();
    vi.advanceTimersByTime(30_000);
    await flush();
    expect(h.stream.cursor()).toEqual({ epoch: A, offset: "2" });
    expect(h.last().request.headers["Last-Event-ID"]).toBe(`${A}:2`);
  });

  it("a mark from another epoch moves nothing", async () => {
    const h = harness();
    const conn = await h.open({ head: "2" });
    conn.push(`id: bbbbbbbbbbbbbbbb:40\n\n`);
    await flush();
    h.runs[0]!.resolve();
    await flush();
    expect(h.stream.cursor()).toEqual({ epoch: A, offset: "2" });
  });
});

describe("a full run that fails stays owed", () => {
  beforeEach(() => {
    vi.useFakeTimers(FAKE_CLOCK);
    vi.spyOn(Math, "random").mockReturnValue(0);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("the queued run that starts after a failed full run is full", async () => {
    const h = harness();
    await h.open({ head: "2", resumed: true });
    const internals = streamInternals(h.stream);
    internals.revalidate("hello", true);
    expect(h.runs.at(-1)!.ctx.full).toBe(true);
    internals.revalidate("visible", false);
    h.runs.at(-1)!.reject(new Error("read failed"));
    await flush();
    expect(h.runs.at(-1)!.ctx.full).toBe(true);
  });

  it("a full run that fails is retried full at the next hello, whatever a same-epoch bind does", async () => {
    const h = harness();
    await h.open({ head: "2", resumed: true });
    streamInternals(h.stream).revalidate("hello", true);
    h.versions.bind(A);
    h.runs.at(-1)!.reject(new Error("read failed"));
    await flush();
    h.last().push(helloFrame({ head: "2", resumed: true }));
    await flush();
    expect(h.runs.at(-1)!.ctx.full).toBe(true);
  });
});
