import { describe, expect, it } from "vitest";
import {
  MAX_FEED_FRAME_BYTES,
  MAX_REV,
  parseChanged,
  parseListening,
  parsePosition,
  peekListening,
  validEpoch,
  validKind,
  validSource,
  validWire,
} from "./wire.js";

const EPOCH = "9f2c4e1a7b3d5f60819a2b3c4d5e6f70";
const OTHER_EPOCH = "41ab0c9d8e7f6a5b4c3d2e1f00112233";
const WIRE = "3f9a0c1d2e4b5a67";

describe("numbers read as IEEE doubles", () => {
  it("accepts 1.0 and 1e0 as the integer 1", () => {
    for (const r of ["1.0", "1e0", "0.1e1"]) {
      const data = `{"source":"store","epoch":"${EPOCH}","rev":${r},"kinds":["runs"]}`;
      expect(parseChanged(data)).toEqual({
        ok: true,
        changed: { source: "store", epoch: EPOCH, rev: 1, kinds: ["runs"] },
      });
    }
  });

  it("refuses a rev that overflows to Infinity or has a fraction", () => {
    for (const r of ["1e400", "1.5", "999999999999999.5"]) {
      const data = `{"source":"store","epoch":"${EPOCH}","rev":${r},"kinds":["runs"]}`;
      expect(parseChanged(data).ok).toBe(false);
    }
  });

  it("accepts MAX_REV and refuses the next integer", () => {
    const at = `{"feed":1,"sources":[{"source":"store","epoch":"${EPOCH}","floors":{"runs":${String(MAX_REV)}}}]}`;
    const past = at.replace(String(MAX_REV), String(MAX_REV + 1));
    expect(parseListening(at).kind).toBe("ok");
    expect(parseListening(past).kind).toBe("invalid");
  });

  it("reads a feed of -0 as the other format 0", () => {
    expect(peekListening(`{"feed":-0,"sources":[]}`)).toEqual({
      ok: true,
      frozen: { feed: 0, wire: null },
    });
    expect(parseListening(`{"feed":-0,"sources":[]}`).kind).toBe("other_format");
  });
});

describe("the 64 KiB bound counts UTF-8 bytes", () => {
  it("refuses a frame whose UTF-16 length fits but whose UTF-8 length does not", () => {
    const data = `{"source":"store","epoch":"${EPOCH}","rev":7,"kinds":["runs"],"x":"${"\u{1f600}".repeat(16_400)}"}`;
    expect(data.length).toBeLessThanOrEqual(MAX_FEED_FRAME_BYTES);
    expect(peekListening(data)).toEqual({ ok: false, reason: "frame data above 64 KiB" });
  });

  it("measures a string member's UTF-8 bytes at the bound", () => {
    const name = `{"feed":1,"wire":"${WIRE}","sources":[],"pad":"`;
    const room = MAX_FEED_FRAME_BYTES - name.length - 2;
    const at = `${name}${"\u20ac".repeat(Math.floor(room / 3))}${"a".repeat(room % 3)}"}`;
    const past = at.replace('"}', 'a"}');
    expect(peekListening(at)).toEqual({ ok: true, frozen: { feed: 1, wire: WIRE } });
    expect(peekListening(past).ok).toBe(false);
  });
});

describe("member names are read as data", () => {
  it("refuses a __proto__ or constructor member as unknown", () => {
    for (const extra of ['"__proto__":{}', '"constructor":1']) {
      const data = `{"source":"store","epoch":"${EPOCH}","rev":7,"kinds":["runs"],${extra}}`;
      expect(parseChanged(data).ok).toBe(false);
    }
  });

  it("reads kinds named like Object.prototype members as floors", () => {
    const data = `{"feed":1,"sources":[{"source":"store","epoch":"${EPOCH}","floors":{"constructor":5,"__proto__":6}}]}`;
    const got = parseListening(data);
    expect(got.kind).toBe("ok");
    if (got.kind === "ok") {
      expect([...(got.listening.sources[0]?.floors ?? [])]).toEqual([
        ["constructor", 5],
        ["__proto__", 6],
      ]);
    }
  });

  it("does not read a missing feed member from the prototype", () => {
    expect(peekListening(`{"sources":[]}`).ok).toBe(false);
    expect(peekListening(`{"valueOf":1,"sources":[]}`).ok).toBe(false);
  });
});

describe("listening frames", () => {
  it("peeks a frame of another format without reading its other members", () => {
    const data = `{"feed":2,"wire":"${WIRE}","sources":"not an array","next":{"x":[1]}}`;
    expect(parseListening(data)).toEqual({
      kind: "other_format",
      frozen: { feed: 2, wire: WIRE },
    });
  });

  it("reports the frozen members of an own-format frame that fails the parse", () => {
    const data = `{"feed":1,"wire":"${WIRE}","sources":[],"lost":true}`;
    expect(parseListening(data)).toMatchObject({
      kind: "invalid",
      frozen: { feed: 1, wire: WIRE },
    });
  });

  it("refuses 65 kinds across two sources", () => {
    const floors = (from: number, n: number): string =>
      Array.from({ length: n }, (_, i) => `"k${String(from + i)}":1`).join(",");
    const frame = (b: number): string =>
      `{"feed":1,"sources":[{"source":"a","epoch":"${EPOCH}","floors":{${floors(0, 32)}}},{"source":"b","epoch":"${OTHER_EPOCH}","floors":{${floors(32, b)}}}]}`;
    expect(parseListening(frame(32)).kind).toBe("ok");
    expect(parseListening(frame(33)).kind).toBe("invalid");
  });
});

describe("Feed-Position", () => {
  const p = (source: string, rev: number, epoch = EPOCH): string =>
    `${source}=${String(rev)};epoch="${epoch}"`;

  it("overwrites a repeated key in its first place", () => {
    expect(
      parsePosition(`${p("store", 1)}, ${p("live", 2, OTHER_EPOCH)}, ${p("store", 3)}`),
    ).toEqual({
      ok: true,
      positions: [
        { source: "store", epoch: EPOCH, rev: 3 },
        { source: "live", epoch: OTHER_EPOCH, rev: 2 },
      ],
    });
  });

  it("reads -0 as revision 0", () => {
    expect(parsePosition(`store=-0;epoch="${EPOCH}"`)).toEqual({
      ok: true,
      positions: [{ source: "store", epoch: EPOCH, rev: 0 }],
    });
  });

  it("accepts a tab after a comma and leading and trailing spaces", () => {
    expect(parsePosition(`  ${p("store", 1)},\t${p("live", 2)}  `).ok).toBe(true);
  });

  it("refuses a tab before the first member", () => {
    expect(parsePosition(`\t${p("store", 1)}`).ok).toBe(false);
  });

  it("refuses a key RFC 9651 admits that is not a source name", () => {
    for (const key of ["*store", "st.ore", "st-ore"]) {
      expect(parsePosition(p(key, 1)).ok).toBe(false);
    }
  });

  it("skips well-formed parameters of every RFC 9651 type", () => {
    const params = ';a=?1;b=:aGk=:;c=@1700000000;d=%"caf%c3%a9";e=1.25;f=tok/en:x;g';
    expect(parsePosition(`store=4${params};epoch="${EPOCH}"`)).toEqual({
      ok: true,
      positions: [{ source: "store", epoch: EPOCH, rev: 4 }],
    });
  });

  it("refuses the whole value on a malformed parameter", () => {
    for (const bad of [
      ";a=?2",
      ";b=:a!:",
      ";c=@1.5",
      ';d=%"%c3"',
      ';d=%"%C3%A9"',
      ";e=1.2345",
      ";f=",
    ]) {
      expect(parsePosition(`store=4${bad};epoch="${EPOCH}"`).ok).toBe(false);
    }
  });

  it("refuses an epoch parameter that is not a String", () => {
    expect(parsePosition(`store=4;epoch=:${btoa(EPOCH)}:`).ok).toBe(false);
    expect(parsePosition(`store=4;epoch`).ok).toBe(false);
  });

  it("takes the last epoch parameter", () => {
    expect(parsePosition(`store=4;epoch="${OTHER_EPOCH}";epoch="${EPOCH}"`)).toEqual({
      ok: true,
      positions: [{ source: "store", epoch: EPOCH, rev: 4 }],
    });
  });
});

describe("name grammars", () => {
  it("bounds each name at its length", () => {
    expect(validSource(`a${"b".repeat(31)}`)).toBe(true);
    expect(validSource(`a${"b".repeat(32)}`)).toBe(false);
    expect(validKind("2".repeat(32))).toBe(true);
    expect(validKind("2".repeat(33))).toBe(false);
    expect(validEpoch(EPOCH)).toBe(true);
    expect(validEpoch(`${EPOCH}0`)).toBe(false);
    expect(validWire(WIRE)).toBe(true);
    expect(validWire(WIRE.toUpperCase())).toBe(false);
  });

  it("refuses a name with a trailing newline", () => {
    expect(validSource("store\n")).toBe(false);
    expect(validKind("runs\n")).toBe(false);
    expect(validEpoch(`${EPOCH}\n`)).toBe(false);
    expect(validWire(`${WIRE}\n`)).toBe(false);
  });
});
