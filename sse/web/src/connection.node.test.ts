import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type Connection, connectionOf } from "./connection.js";
import type { StreamState } from "./state.js";
import { createStream } from "./stream.js";
import { FAKE_CLOCK, flush, helloFrame, scriptedFetch } from "./test-helpers/scripted-fetch.js";
import { createVersionMap } from "./versions.js";

describe("connectionOf", () => {
  it.each<[StreamState, Connection]>([
    [
      { kind: "open", generation: 1, attempt: 0, keepaliveMs: 15000, visible: true, online: true },
      { state: "live", since: null },
    ],
    [
      { kind: "stopped", generation: 1 },
      { state: "lost", since: 5 },
    ],
    [
      { kind: "hidden_closed", generation: 1, attempt: 0, online: true },
      { state: "lost", since: 5 },
    ],
    [
      { kind: "connecting", generation: 1, attempt: 0, visible: true, online: true },
      { state: "lost", since: 5 },
    ],
    [
      { kind: "connecting", generation: 2, attempt: 1, visible: true, online: true },
      { state: "lost", since: 5 },
    ],
    [
      { kind: "backoff", generation: 2, attempt: 1, until: 9, visible: true, online: true },
      { state: "lost", since: 5 },
    ],
    [
      { kind: "offline", generation: 2, attempt: 0, visible: true },
      { state: "lost", since: 5 },
    ],
  ])("reads %o as %o", (state, want) => {
    expect(connectionOf(state, 5)).toEqual(want);
  });
});

describe("connectionOf over a running stream", () => {
  beforeEach(() => {
    vi.useFakeTimers(FAKE_CLOCK);
    vi.setSystemTime(1_000);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("says since when the stream dropped after it was open, and a quick reconnect is lost until it opens", async () => {
    const sf = scriptedFetch();
    let lostAt: number | null = null;
    const stream = createStream({
      url: "/events",
      fetch: sf.fetch,
      versions: createVersionMap(),
      onFrame: () => undefined,
      revalidate: () => Promise.resolve(),
      onLifecycle(ev) {
        if (ev.kind === "state" && ev.from === "open") {
          lostAt = Date.now();
        }
      },
    });
    const now = (): Connection => connectionOf(stream.state(), lostAt);
    stream.start();
    await flush(10);
    expect(now()).toEqual({ state: "lost", since: null });

    sf.connections.at(-1)?.push(helloFrame({ resumed: true }));
    await flush(10);
    expect(now()).toEqual({ state: "live", since: null });

    vi.advanceTimersByTime(234);
    sf.connections.at(-1)?.end();
    await flush(10);
    expect(stream.state().kind).not.toBe("open");
    expect(now()).toEqual({ state: "lost", since: 1_234 });

    const dropped = sf.connections.length;
    while (sf.connections.length === dropped) {
      await vi.advanceTimersByTimeAsync(100);
    }
    await flush(10);
    expect(stream.state().kind).toBe("connecting");
    expect(now()).toEqual({ state: "lost", since: 1_234 });
    sf.connections.at(-1)?.push(helloFrame({ resumed: true }));
    await flush(10);
    expect(now()).toEqual({ state: "live", since: null });
    stream.stop();
  });
});
