import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { retainListening } from "./feed/retain.js";
import type { TabToWorker, WorkerToTab } from "./port-protocol.js";
import type { Frame } from "./stream.js";
import {
  FAKE_CLOCK,
  type ScriptedFetch,
  flush,
  frame,
  helloFrame,
  scriptedFetch,
} from "./test-helpers/scripted-fetch.js";
import { createVersionMap } from "./versions.js";
import { type WorkerHost, type WorkerHostOptions, createWorkerHost } from "./worker.js";

const A = "aaaaaaaaaaaaaaaa";
const E1 = "9f2c4e1a7b3d5f60819a2b3c4d5e6f70";
const W1 = "0123456789abcdef";

const LISTENING = `{"feed":1,"wire":"${W1}","sources":[{"source":"store","epoch":"${E1}","floors":{"runs":5}}]}`;
const changedData = (rev: number): string =>
  `{"source":"store","epoch":"${E1}","rev":${String(rev)},"kinds":["runs"]}`;

const hosts: WorkerHost[] = [];

function hostWith(retain: WorkerHostOptions["retain"]): { host: WorkerHost; sf: ScriptedFetch } {
  const sf = scriptedFetch();
  const host = createWorkerHost({
    url: "/events",
    fetch: sf.fetch,
    versions: createVersionMap(),
    heartbeatMs: 60_000,
    revalidate: () => Promise.resolve(),
    ...(retain === undefined ? {} : { retain }),
  });
  hosts.push(host);
  return { host, sf };
}

/** A tab's port: every frame and state message the host posts it, in order. */
function tab(host: WorkerHost, tabId: string): { readonly got: WorkerToTab[] } {
  const channel = new MessageChannel();
  const got: WorkerToTab[] = [];
  channel.port2.onmessage = (event: MessageEvent) => {
    const m = event.data as WorkerToTab;
    if (m.type === "frame" || m.type === "state") {
      got.push(m);
    }
  };
  host.attach(channel.port1);
  const attach: TabToWorker = {
    type: "attach",
    tabId,
    visible: true,
    online: true,
    hadWorker: false,
    tag: "",
  };
  channel.port2.postMessage(attach);
  return { got };
}

const settle = (): Promise<void> => flush(10);

async function open(sf: ScriptedFetch): Promise<void> {
  await settle();
  sf.connections.at(-1)?.push(helloFrame({ epoch: A }));
  await settle();
}

async function push(sf: ScriptedFetch, type: string, data: string, id: string | null) {
  sf.connections.at(-1)?.push(frame(type, data, id));
  await settle();
}

const frames = (got: readonly WorkerToTab[]): Frame[] =>
  got.flatMap((m) => (m.type === "frame" ? [m.frame] : []));

beforeEach(() => {
  vi.useFakeTimers(FAKE_CLOCK);
});

afterEach(async () => {
  for (const host of hosts.splice(0)) {
    host.close();
  }
  await flush(4);
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("createWorkerHost: retain", () => {
  it("an attaching tab receives the retained frame inside onAttach before any later frame", async () => {
    const { host, sf } = hostWith(retainListening);
    const first = tab(host, "t1");
    await open(sf);
    await push(sf, "listening", LISTENING, null);
    await push(sf, "changed", changedData(9), `${A}:1`);
    const second = tab(host, "t2");
    await settle();
    await push(sf, "changed", changedData(10), `${A}:2`);

    const retained = `{"feed":1,"wire":"${W1}","sources":[{"source":"store","epoch":"${E1}","floors":{"runs":9}}]}`;
    const generation = host.stream().state().generation;
    expect(second.got.map((m) => m.type)).toEqual(["frame", "state", "frame"]);
    expect(second.got[0]).toEqual({
      type: "frame",
      frame: { type: "listening", data: retained, id: null },
      generation,
    });
    expect(frames(second.got)[1]?.data).toBe(changedData(10));
    expect(frames(first.got).map((f) => f.type)).toEqual(["listening", "changed", "changed"]);
  });

  it("a host without retain posts no frame at the attach", async () => {
    const { host, sf } = hostWith(undefined);
    tab(host, "t1");
    await open(sf);
    await push(sf, "listening", LISTENING, null);
    const second = tab(host, "t2");
    await settle();
    expect(frames(second.got)).toEqual([]);
  });

  it("the retained frame is cleared when the connection ends", async () => {
    const { host, sf } = hostWith(retainListening);
    tab(host, "t1");
    await open(sf);
    await push(sf, "listening", LISTENING, null);
    sf.connections.at(-1)?.end();
    await settle();
    expect(host.stream().state().kind).not.toBe("open");
    const second = tab(host, "t2");
    await settle();
    expect(frames(second.got)).toEqual([]);
  });

  it("a throw from retain drops it and reconnects the host's stream", async () => {
    const reported: unknown[] = [];
    vi.stubGlobal("reportError", (e: unknown) => reported.push(e));
    const boom = new Error("retain failed");
    const { host, sf } = hostWith((r, f) => {
      if (f.type === "boom") {
        throw boom;
      }
      return retainListening(r, f);
    });
    const first = tab(host, "t1");
    await open(sf);
    await push(sf, "listening", LISTENING, null);
    await push(sf, "boom", "{}", null);
    expect(reported).toEqual([boom]);
    expect(sf.connections).toHaveLength(2);
    expect(frames(first.got).map((f) => f.type)).toEqual(["listening", "boom"]);
    const second = tab(host, "t2");
    await settle();
    expect(frames(second.got)).toEqual([]);
  });
});
