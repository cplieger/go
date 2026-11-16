import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Frame } from "../stream.js";
import { advance, harness, listening, position, sink } from "./test-helpers/feed-harness.js";
import type { Harness, RecordingSink } from "./test-helpers/feed-harness.js";
import { encodeChanged, encodeListening, formatPosition } from "./test-helpers/wire-encode.js";
import { retainListening } from "./retain.js";
import { FEED_VERSION } from "./version.js";
import {
  parseChanged,
  parseListening,
  parsePosition,
  peekListening,
  type ChangedFrame,
  type Frozen,
  type ListeningFrame,
} from "./wire.js";

// testdata/feed/cases.json is the set feedwire's Go tests read too. A case with pad_to_bytes has
// its data followed by spaces up to that many bytes; a valid case with canonical false is accepted
// but is not what the encoder writes.

interface FrameCase {
  readonly name: string;
  readonly data: string;
  readonly valid: boolean;
  readonly pad_to_bytes?: number;
  readonly canonical?: boolean;
  readonly decoded?: unknown;
  readonly frozen?: { readonly feed: number; readonly wire: string };
  readonly error?: string;
}

interface PositionCase {
  readonly name: string;
  readonly value: string;
  readonly valid: boolean;
  readonly canonical?: boolean;
  readonly decoded?: readonly { source: string; epoch: string; rev: number }[];
}

interface Cases {
  readonly feed_version: number;
  readonly declared: {
    readonly kinds: Readonly<Record<string, string>>;
    readonly logged: readonly string[];
  };
  readonly changed: readonly FrameCase[];
  readonly listening: readonly FrameCase[];
  readonly position: readonly PositionCase[];
}

interface DecodedListening {
  readonly wire: string;
  readonly sources: readonly {
    source: string;
    epoch: string;
    floors: Readonly<Record<string, number>> | null;
  }[];
}

const casesPath = join(import.meta.dirname, "..", "..", "..", "testdata", "feed", "cases.json");
const cases = JSON.parse(readFileSync(casesPath, "utf8")) as Cases;

const frameData = (c: FrameCase): string =>
  c.pad_to_bytes === undefined ? c.data : c.data.padEnd(c.pad_to_bytes, " ");

const frozenOf = (f: { feed: number; wire: string }): Frozen => ({
  feed: f.feed,
  wire: f.wire === "" ? null : f.wire,
});

const listeningOf = (d: DecodedListening): ListeningFrame => ({
  wire: d.wire === "" ? null : d.wire,
  sources: d.sources.map((s) => ({
    source: s.source,
    epoch: s.epoch,
    floors: new Map(Object.entries(s.floors ?? {})),
  })),
});

describe("feed fixtures parse as the Go half encodes", () => {
  it("the set's feed_version is FEED_VERSION", () => {
    expect(cases.feed_version).toBe(FEED_VERSION);
  });

  it.each(cases.changed)("changed $name", (c) => {
    const got = parseChanged(frameData(c));
    if (!c.valid) {
      expect(got.ok).toBe(false);
      return;
    }
    const want = c.decoded as ChangedFrame;
    expect(got).toEqual({ ok: true, changed: want });
    if (c.canonical !== false) {
      expect(encodeChanged(want)).toBe(c.data);
    }
  });

  it.each(cases.listening)("listening $name", (c) => {
    const data = frameData(c);
    const peeked = peekListening(data);
    const parsed = parseListening(data);
    if (c.frozen === undefined) {
      expect(peeked.ok).toBe(false);
      expect(parsed).toMatchObject({ kind: "invalid", frozen: null });
      return;
    }
    const frozen = frozenOf(c.frozen);
    expect(peeked).toEqual({ ok: true, frozen });
    if (c.error === "other_format") {
      expect(parsed).toEqual({ kind: "other_format", frozen });
      return;
    }
    if (!c.valid) {
      expect(parsed).toMatchObject({ kind: "invalid", frozen });
      return;
    }
    const want = listeningOf(c.decoded as DecodedListening);
    expect(parsed).toEqual({ kind: "ok", frozen, listening: want });
    if (c.canonical !== false) {
      expect(encodeListening(want)).toBe(c.data);
    }
  });

  it.each(cases.position)("position $name", (c) => {
    const got = parsePosition(c.value);
    if (!c.valid) {
      expect(got.ok).toBe(false);
      return;
    }
    const want = c.decoded ?? [];
    expect(got).toEqual({ ok: true, positions: want });
    if (c.canonical !== false) {
      expect(formatPosition(want)).toBe(c.value);
    }
  });
});

describe("feed fixtures through retainListening", () => {
  const frameOf = (type: string, c: FrameCase): Frame => ({ type, data: frameData(c), id: null });
  const lostData = (wire: string | null): string =>
    `{"feed":${String(FEED_VERSION)}${wire === null ? "" : `,"wire":"${wire}"`},"sources":[],"lost":true}`;

  it.each(cases.listening.filter((c) => c.valid))("listening $name is retained", (c) => {
    const want = listeningOf(c.decoded as DecodedListening);
    const got = retainListening(null, frameOf("listening", c));
    expect(got?.type).toBe("listening");
    expect(parseListening(got?.data ?? "")).toEqual({
      kind: "ok",
      frozen: { feed: FEED_VERSION, wire: want.wire },
      listening: want,
    });
    if (c.canonical !== false) {
      expect(got?.data).toBe(c.data);
    }
  });

  it.each(cases.listening.filter((c) => c.error === "other_format"))(
    "listening $name is retained as its frozen members",
    (c) => {
      const frozen = frozenOf(c.frozen ?? { feed: 0, wire: "" });
      const got = retainListening(null, frameOf("listening", c));
      expect(parseListening(got?.data ?? "")).toEqual({ kind: "other_format", frozen });
    },
  );

  it.each(cases.listening.filter((c) => !c.valid && c.error === undefined))(
    "listening $name sets lost",
    (c) => {
      const got = retainListening(null, frameOf("listening", c));
      expect(got?.data).toBe(lostData(c.frozen === undefined ? null : frozenOf(c.frozen).wire));
    },
  );

  it.each(cases.changed.filter((c) => c.valid))(
    "changed $name raises its kinds' floors to its rev",
    (c) => {
      const d = c.decoded as ChangedFrame;
      const retained = retainListening(null, {
        type: "listening",
        data: encodeListening({
          wire: null,
          sources: [{ source: d.source, epoch: d.epoch, floors: new Map() }],
        }),
        id: null,
      });
      const got = retainListening(retained, frameOf("changed", c));
      const floors = new Map(d.kinds.map((k) => [k, d.rev]));
      expect(parseListening(got?.data ?? "")).toEqual({
        kind: "ok",
        frozen: { feed: FEED_VERSION, wire: null },
        listening: { wire: null, sources: [{ source: d.source, epoch: d.epoch, floors }] },
      });
    },
  );

  it.each(cases.changed.filter((c) => !c.valid))("changed $name sets lost", (c) => {
    expect(retainListening(null, frameOf("changed", c))?.data).toBe(lostData(null));
  });
});

describe("feed fixtures through createFeed, with the set's declared kinds", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const { kinds, logged } = cases.declared;
  const declaredSources = [...new Set(Object.values(kinds))];
  const sourceOf = (kind: string): string | undefined =>
    Object.hasOwn(kinds, kind) ? kinds[kind] : undefined;
  const UNNAMED = "a".repeat(32);

  const frameOf = (type: string, c: FrameCase): Frame => ({ type, data: frameData(c), id: null });
  const every = (epochOf: (source: string) => string): Frame =>
    listening(declaredSources.map((source) => ({ source, epoch: epochOf(source), floors: {} })));

  // One subscription per declared kind, seeded at `rev` on its source's `epochOf`.
  function watchAll(
    h: Harness,
    epochOf: (source: string) => string,
    rev: number,
  ): Map<string, RecordingSink> {
    const sinks = new Map<string, RecordingSink>();
    for (const [kind, source] of Object.entries(kinds)) {
      const s = sink();
      h.feed.watch(kind, s, position([source, epochOf(source), rev]));
      sinks.set(kind, s);
    }
    return sinks;
  }

  // The kinds a frame moved: those a catch-up asked for and those whose sink was staled.
  function acted(h: Harness, sinks: ReadonlyMap<string, RecordingSink>): string[] {
    const asked = h.calls.flatMap((c) => c.q.kinds);
    const staled = [...sinks].filter(([, s]) => s.log.includes("stale")).map(([k]) => k);
    return [...new Set([...asked, ...staled])].sort();
  }

  it.each(cases.changed.filter((c) => c.valid))(
    "changed $name moves only its declared kinds",
    async (c) => {
      const d = c.decoded as ChangedFrame;
      const h = harness({ kinds, logged });
      const epochOf = (source: string): string => (source === d.source ? d.epoch : UNNAMED);
      h.feed.onFrame(every(epochOf));
      const sinks = watchAll(h, epochOf, d.rev - 1);
      expect(() => h.feed.onFrame(frameOf("changed", c))).not.toThrow();
      await advance(1_000);
      expect(acted(h, sinks)).toEqual(d.kinds.filter((k) => sourceOf(k) === d.source).sort());
      expect(h.reconnect).not.toHaveBeenCalled();
    },
  );

  it.each(cases.listening.filter((c) => c.valid))(
    "listening $name moves only its declared sources' declared kinds",
    async (c) => {
      const d = listeningOf(c.decoded as DecodedListening);
      const h = harness({ kinds, logged });
      const epochOf = (source: string): string =>
        d.sources.find((s) => s.source === source)?.epoch ?? UNNAMED;
      const sinks = watchAll(h, epochOf, 0);
      expect(() => h.feed.onFrame(frameOf("listening", c))).not.toThrow();
      await advance(1_000);
      const want = d.sources.flatMap((s) =>
        [...s.floors.keys()].filter((k) => sourceOf(k) === s.source),
      );
      expect(acted(h, sinks)).toEqual(want.sort());
      expect(h.wires).toEqual(d.wire === null ? [] : [d.wire]);
      expect(h.reconnect).not.toHaveBeenCalled();
    },
  );

  it.each(cases.listening.filter((c) => c.error === "other_format"))(
    "listening $name is another format: its wire is noted and no later frame applies",
    async (c) => {
      const h = harness({ kinds, logged });
      h.feed.onFrame(every(() => UNNAMED));
      const sinks = watchAll(h, () => UNNAMED, 0);
      expect(() => h.feed.onFrame(frameOf("listening", c))).not.toThrow();
      for (const source of declaredSources) {
        const named = Object.keys(kinds)
          .filter((k) => sourceOf(k) === source)
          .sort();
        const data = encodeChanged({ source, epoch: UNNAMED, rev: 5, kinds: named });
        h.feed.onFrame({ type: "changed", data, id: null });
      }
      await advance(1_000);
      expect(acted(h, sinks)).toEqual([]);
      const wire = c.frozen?.wire ?? "";
      expect(h.wires).toEqual(wire === "" ? [] : [wire]);
      expect(h.reconnect).not.toHaveBeenCalled();
    },
  );

  const refused = [
    ...cases.changed.filter((c) => !c.valid).map((c) => ({ ...c, type: "changed" })),
    ...cases.listening
      .filter((c) => !c.valid && c.error === undefined)
      .map((c) => ({ ...c, type: "listening" })),
  ];

  it.each(refused)("$type $name is lost track", (c) => {
    const h = harness({ kinds, logged });
    h.feed.onFrame(every(() => UNNAMED));
    expect(() => h.feed.onFrame(frameOf(c.type, c))).toThrow();
    expect(h.reconnect).toHaveBeenCalledTimes(1);
    const wire = c.frozen?.wire ?? "";
    expect(h.wires).toEqual(wire === "" ? [] : [wire]);
  });
});
