import { describe, expect, it } from "vitest";
import { parseInstant } from "./instant.js";

const SECOND = 1000;
const HOUR = 3600 * SECOND;
// 2026-11-17T10:00:05Z, from ECMA-262's own Date.UTC.
const BASE = Date.UTC(2026, 10, 17, 10, 0, 5);
// RFC 3339's first and last representable instants, 0000-01-01T00:00:00Z and
// 9999-12-31T23:59:59Z, as Unix seconds.
const FIRST = -62167219200 * SECOND;
const LAST = 253402300799 * SECOND;

describe("parseInstant", () => {
  it("orders Go's trimmed fractions by time, digits past the third truncated", () => {
    const texts = [
      "2026-11-17T10:00:05Z",
      "2026-11-17T10:00:05.1Z",
      "2026-11-17T10:00:05.12Z",
      "2026-11-17T10:00:05.123456789Z",
    ];
    expect(texts.map(parseInstant)).toEqual([BASE, BASE + 100, BASE + 120, BASE + 123]);
    expect(parseInstant("2026-11-17T10:00:05.123456789Z")).toBe(
      parseInstant("2026-11-17T10:00:05.123Z"),
    );
    expect(parseInstant("2026-11-17T10:00:05.05Z")).toBe(BASE + 50);
    expect(parseInstant("2026-11-17T10:00:05.006Z")).toBe(BASE + 6);
    expect(parseInstant("2026-11-17T10:00:05.05Z")).toBeGreaterThan(
      parseInstant("2026-11-17T10:00:05.006Z"),
    );
    expect(parseInstant("2026-11-17T10:00:05.1239Z")).toBe(BASE + 123);
  });

  it("reads an offset as that many hours and minutes ahead of UTC", () => {
    expect(parseInstant("2026-11-17T10:00:05.1+01:00")).toBe(
      parseInstant("2026-11-17T10:00:05.1Z") - HOUR,
    );
    expect(parseInstant("2026-11-17T10:00:05.1+01:00")).toBe(Date.UTC(2026, 10, 17, 9, 0, 5, 100));
    expect(parseInstant("2026-11-17T04:30:05-05:30")).toBe(BASE);
    expect(parseInstant("2026-11-18T09:59:05+23:59")).toBe(BASE);
    expect(parseInstant("2026-11-17T10:00:05-00:00")).toBe(BASE);
  });

  it("truncates a fraction toward the earlier millisecond", () => {
    expect(parseInstant("2026-11-17T10:00:05.9999999Z")).toBe(BASE + 999);
    expect(parseInstant("1969-12-31T23:59:59.9999Z")).toBe(-1);
    expect(parseInstant("1970-01-01T00:00:00.0009Z")).toBe(0);
  });

  it("answers the calendar's ends and its leap days", () => {
    expect(parseInstant("0000-01-01T00:00:00Z")).toBe(FIRST);
    // Date.UTC reads year 1 as 1901, so a parser built on it fails here.
    expect(parseInstant("0001-01-01T00:00:00Z")).toBe(-62135596800000);
    expect(parseInstant("9999-12-31T23:59:59.999999999Z")).toBe(LAST + 999);
    expect(parseInstant("2024-02-29T00:00:00Z")).toBe(Date.UTC(2024, 1, 29));
    expect(parseInstant("2000-02-29T00:00:00Z")).toBe(Date.UTC(2000, 1, 29));
    expect(parseInstant("0000-02-29T00:00:00Z")).toBe(FIRST + 59 * 24 * HOUR);
  });

  it("reads back every millisecond toISOString writes, with or without Go's trimmed zeros", () => {
    let seed = 0x2545f491;
    const next = (): number => {
      seed = (Math.imul(seed, 1103515245) + 12345) >>> 0;
      return seed / 2 ** 32;
    };
    const d = new Date(0);
    for (let i = 0; i < 2000; i++) {
      const ms = FIRST + Math.floor(next() * (LAST + 1000 - FIRST));
      d.setTime(ms);
      const iso = d.toISOString();
      expect(parseInstant(iso), iso).toBe(ms);
      const trimmed = iso.replace(/\.?0+Z$/, "Z");
      expect(parseInstant(trimmed), trimmed).toBe(ms);
    }
  });

  it.each([
    ["a space for the T", "2026-11-17 10:00:05Z"],
    ["no offset", "2026-11-17T10:00:05"],
    ["a 10-digit fraction", "2026-11-17T10:00:05.1234567890Z"],
    ["the empty string", ""],
    ["a lowercase t", "2026-11-17t10:00:05Z"],
    ["a lowercase z", "2026-11-17T10:00:05z"],
    ["a dot with no digits", "2026-11-17T10:00:05.Z"],
    ["a comma fraction", "2026-11-17T10:00:05,1Z"],
    ["no seconds", "2026-11-17T10:00Z"],
    ["a date alone", "2026-11-17"],
    ["an offset without a colon", "2026-11-17T10:00:05+0100"],
    ["an offset of hours alone", "2026-11-17T10:00:05+01"],
    ["an offset hour of 24", "2026-11-17T10:00:05+24:00"],
    ["an offset minute of 60", "2026-11-17T10:00:05+01:60"],
    ["month 00", "2026-00-17T10:00:05Z"],
    ["month 13", "2026-13-17T10:00:05Z"],
    ["day 00", "2026-11-00T10:00:05Z"],
    ["November 31", "2026-11-31T10:00:05Z"],
    ["February 29 of a common year", "2026-02-29T10:00:05Z"],
    ["February 29 of a century", "1900-02-29T10:00:05Z"],
    ["hour 24", "2026-11-17T24:00:00Z"],
    ["minute 60", "2026-11-17T10:60:05Z"],
    ["a leap second, which Go refuses", "2026-12-31T23:59:60Z"],
    ["an expanded year", "+002026-11-17T10:00:05Z"],
    ["a three-digit year", "026-11-17T10:00:05Z"],
    ["one-digit fields", "2026-1-7T1:0:5Z"],
    ["non-ASCII digits", "２026-11-17T10:00:05Z"],
    ["a leading space", " 2026-11-17T10:00:05Z"],
    ["a trailing newline", "2026-11-17T10:00:05Z\n"],
    ["a Unix time", "1794909605000"],
    ["an HTTP date", "Tue, 17 Nov 2026 10:00:05 GMT"],
  ])("throws on %s, naming the text", (_, text) => {
    expect(() => parseInstant(text)).toThrow(JSON.stringify(text));
  });

  it("names a long text by its start only", () => {
    const text = "x".repeat(10_000);
    expect(() => parseInstant(text)).toThrow(/^parseInstant: not an RFC 3339 date-time: "x{64}"…$/);
  });
});
