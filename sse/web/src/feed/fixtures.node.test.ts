import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { encodeChanged, encodeListening, formatPosition } from "./test-helpers/wire-encode.js";
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
