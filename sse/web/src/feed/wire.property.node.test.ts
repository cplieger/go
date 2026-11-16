import fc from "fast-check";
import { describe, expect, it } from "vitest";
import { encodeChanged, encodeListening, formatPosition } from "./test-helpers/wire-encode.js";
import {
  MAX_REV,
  parseChanged,
  parseListening,
  parsePosition,
  peekListening,
  type ChangedFrame,
  type ListeningFrame,
  type Position,
} from "./wire.js";

const lower = "abcdefghijklmnopqrstuvwxyz";
const nameChars = `${lower}0123456789_`;
const hex = "0123456789abcdef";

const chars = (alphabet: string, min: number, max: number): fc.Arbitrary<string> =>
  fc.string({ unit: fc.constantFrom(...alphabet), minLength: min, maxLength: max });

const source = fc.tuple(fc.constantFrom(...lower), chars(nameChars, 0, 31)).map(([a, b]) => a + b);
const kind = chars(nameChars, 1, 32);
const epoch = chars(hex, 32, 32);
const wire = chars(hex, 16, 16);
const rev = fc.integer({ min: 1, max: MAX_REV });

const byCodeUnit = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

const changed: fc.Arbitrary<ChangedFrame> = fc.record({
  source,
  epoch,
  rev,
  kinds: fc
    .uniqueArray(kind, { minLength: 1, maxLength: 64 })
    .map((ks) => [...ks].sort(byCodeUnit)),
});

const listening: fc.Arbitrary<ListeningFrame> = fc
  .record({
    wire: fc.option(wire, { nil: null }),
    sources: fc.uniqueArray(
      fc.record({
        source,
        epoch,
        floors: fc.uniqueArray(fc.tuple(kind, rev), { maxLength: 16, selector: ([k]) => k }),
      }),
      { maxLength: 4, selector: (s) => s.source },
    ),
  })
  .map((l) => ({
    wire: l.wire,
    sources: l.sources.map((s) => ({ ...s, floors: new Map(s.floors) })),
  }));

const positions: fc.Arbitrary<Position[]> = fc.uniqueArray(
  fc.record({ source, epoch, rev: fc.integer({ min: 0, max: MAX_REV }) }),
  { minLength: 1, maxLength: 8, selector: (p) => p.source },
);

// Strings shaped like the wire reach the parsers' deeper branches more often than arbitrary text.
const frameish = fc.oneof(
  fc.string(),
  fc.string({ unit: "binary" }),
  changed.map(encodeChanged),
  listening.map((l) => encodeListening(l)),
  fc
    .tuple(changed.map(encodeChanged), fc.nat(), fc.string({ maxLength: 4 }))
    .map(([s, at, ins]) => s.slice(0, at % (s.length + 1)) + ins + s.slice(at % (s.length + 1))),
);
const headerish = fc.oneof(
  fc.string(),
  positions.map(formatPosition),
  fc
    .tuple(positions.map(formatPosition), fc.nat(), fc.string({ maxLength: 3 }))
    .map(([s, at, ins]) => s.slice(0, at % (s.length + 1)) + ins + s.slice(at % (s.length + 1))),
);

describe("the feed wire parsers", () => {
  it("answer a result for any frame data and never throw", () => {
    fc.assert(
      fc.property(frameish, (data) => {
        expect(typeof parseChanged(data).ok).toBe("boolean");
        expect(typeof peekListening(data).ok).toBe("boolean");
        expect(["ok", "other_format", "invalid"]).toContain(parseListening(data).kind);
      }),
    );
  });

  it("answer a result for any Feed-Position value and never throw", () => {
    fc.assert(
      fc.property(headerish, (value) => {
        expect(typeof parsePosition(value).ok).toBe("boolean");
      }),
    );
  });

  it("parse a changed frame written as the Go encoder writes it back to its value", () => {
    fc.assert(
      fc.property(changed, (c) => {
        expect(parseChanged(encodeChanged(c))).toEqual({ ok: true, changed: c });
      }),
    );
  });

  it("parse a listening frame written as the Go encoder writes it back to its value", () => {
    fc.assert(
      fc.property(listening, (l) => {
        const frozen = { feed: 1, wire: l.wire };
        expect(peekListening(encodeListening(l))).toEqual({ ok: true, frozen });
        expect(parseListening(encodeListening(l))).toEqual({ kind: "ok", frozen, listening: l });
      }),
    );
  });

  it("peek a listening frame of any other format to its frozen members only", () => {
    fc.assert(
      fc.property(
        listening,
        fc.integer({ min: -(2 ** 53) + 1, max: 2 ** 53 - 1 }).filter((n) => n !== 1),
        (l, feed) => {
          const frozen = { feed, wire: l.wire };
          expect(parseListening(encodeListening(l, feed))).toEqual({
            kind: "other_format",
            frozen,
          });
        },
      ),
    );
  });

  it("parse a Feed-Position value written as the Go formatter writes it back to its members", () => {
    fc.assert(
      fc.property(positions, (ps) => {
        expect(parsePosition(formatPosition(ps))).toEqual({ ok: true, positions: ps });
      }),
    );
  });

  it("refuse a changed frame once any one kind is out of order", () => {
    fc.assert(
      fc.property(
        changed.filter((c) => c.kinds.length > 1),
        fc.nat(),
        (c, at) => {
          const i = at % (c.kinds.length - 1);
          const kinds = [...c.kinds];
          [kinds[i], kinds[i + 1]] = [kinds[i + 1] ?? "", kinds[i] ?? ""];
          expect(parseChanged(encodeChanged({ ...c, kinds })).ok).toBe(false);
        },
      ),
    );
  });
});
