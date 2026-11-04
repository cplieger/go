import { readFileSync } from "node:fs";
import { join as pathJoin } from "node:path";
import { describe, expect, it } from "vitest";
import fc from "fast-check";
import {
  ESCAPE,
  HashedKeyError,
  isHashed,
  join,
  MalformedKeyError,
  MAX_COMPONENT_BYTES,
  SEPARATOR,
  split,
} from "./keyenc.js";
import { Sha256 } from "./sha256.js";

interface ConformanceCase {
  name: string;
  parts: string[];
  key: string;
}

// The fixture is a REPO-ROOT sibling of web/, which is why stryker.config.json
// sets `inPlace: true`. Stryker otherwise copies web/ into
// .stryker-tmp/sandbox-*/ where this path does not resolve, and the throw lands
// at module load — so the whole file contributes zero tests SILENTLY and Stryker
// scores what is left (4 of 49 tests, 42% instead of 86%). Read it from
// anywhere outside web/ and that config option is load-bearing; do not relocate
// or duplicate the fixture to avoid it, both halves share it on purpose.
const fixture = JSON.parse(
  readFileSync(pathJoin(import.meta.dirname, "../../conformance/keys.json"), "utf8"),
) as { note: string; cases: ConformanceCase[] };

describe("cross-language conformance", () => {
  // This is the only mechanism keeping the two implementations byte-identical.
  // A failure here is either a deliberate grammar change (regenerate with
  // `UPDATE_GOLDEN=1 go test ./... -run TestConformanceFixture` and land both
  // halves in one commit) or a real divergence between Go and TypeScript.
  it.each(fixture.cases)("matches Go for $name", ({ parts, key }) => {
    expect(join(...parts)).toBe(key);
  });

  it("covers both the escaped and hashed regimes", () => {
    const hashed = fixture.cases.filter((c) => isHashed(c.key)).length;
    expect(hashed).toBeGreaterThan(0);
    expect(fixture.cases.length - hashed).toBeGreaterThan(0);
  });

  it("round-trips every non-hashed fixture case", () => {
    for (const c of fixture.cases) {
      if (isHashed(c.key)) {
        continue;
      }
      expect(split(c.key)).toEqual(c.parts);
    }
  });
});

describe("sha256", () => {
  // The digest is hand-rolled because the call sites are synchronous, so it
  // gets its own known-answer tests on top of the conformance fixture: an
  // implementation that is wrong only for multi-block or unaligned input would
  // otherwise ride on whichever lengths the fixture happens to contain.
  it.each([
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
  ])("matches the known answer for %o", (input, want) => {
    const got = new Sha256().update(new TextEncoder().encode(input)).hex();
    expect(got).toBe(want);
  });

  it("agrees with crypto.subtle across block boundaries", async () => {
    // The async WebCrypto digest is the oracle the synchronous one has to
    // match; the interesting lengths are around 55/56/64/119/120 bytes, where
    // the padding branch changes.
    for (const n of [0, 1, 54, 55, 56, 57, 63, 64, 65, 119, 120, 121, 200, 1000]) {
      const bytes = new Uint8Array(n).map((_, i) => (i * 31 + 7) & 0xff);
      const want = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
      expect(new Sha256().update(bytes).hex(), `length ${n}`).toBe(want);
    }
  });

  it("agrees with crypto.subtle when fed in arbitrary chunks", async () => {
    const bytes = new Uint8Array(300).map((_, i) => (i * 17 + 3) & 0xff);
    const want = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    for (const chunk of [1, 7, 32, 64, 100]) {
      const h = new Sha256();
      for (let i = 0; i < bytes.length; i += chunk) {
        h.update(bytes.subarray(i, i + chunk));
      }
      expect(h.hex(), `chunk ${chunk}`).toBe(want);
    }
  });
});

describe("join", () => {
  it("distinguishes a shifted boundary", () => {
    expect(join("a:b", "c")).not.toBe(join("a", "b:c"));
    expect(join("a", "b", "c")).not.toBe(join("a:b", "c"));
  });

  it("encodes separator-free components verbatim", () => {
    // The property that lets an existing key adopt keyenc without re-keying.
    expect(join("github", "github.com")).toBe("github:github.com");
    expect(join("streams", "u-42", "1234", "3", "5")).toBe("streams:u-42:1234:3:5");
  });

  it("keeps no components distinct from one empty component", () => {
    expect(join()).toBe("");
    expect(join("")).not.toBe("");
    expect(isHashed(join(""))).toBe(true);
  });

  it("hashes above the byte bound and measures UTF-8 bytes", () => {
    expect(isHashed(join("x".repeat(MAX_COMPONENT_BYTES)))).toBe(false);
    expect(isHashed(join("x".repeat(MAX_COMPONENT_BYTES + 1)))).toBe(true);
    // Two bytes per character: half the bound in characters is the whole bound
    // in bytes, so one character more must cross it. An implementation
    // measuring String.length stays raw here and diverges from Go.
    expect(isHashed(join("é".repeat(MAX_COMPONENT_BYTES / 2 + 1)))).toBe(true);
  });

  it("keeps the raw and hashed domains disjoint", () => {
    const digest = join("x".repeat(MAX_COMPONENT_BYTES + 1));
    expect(join(digest)).not.toBe(digest);
    expect(join("sha256", digest.slice("sha256:".length))).not.toBe(digest);
  });

  it("nests by composition at depth", () => {
    const inner = join("in:a", "in:b");
    const key = join("outer", inner);
    const outer = split(key);
    expect(outer).toEqual(["outer", inner]);
    expect(split(outer[1]!)).toEqual(["in:a", "in:b"]);
  });
});

describe("the size bound counts UTF-8 bytes per length class", () => {
  // The bound is measured in UTF-8 bytes because the Go half measures len(s),
  // so the raw/hashed decision has to cross at the same input in both halves.
  // Each case below pins ONE of the four UTF-8 length classes at the exact
  // crossing point: a counter charging the wrong width for a single class moves
  // the threshold for that class alone, which the conformance fixture would
  // catch only if it happened to carry a case of that width at that size.

  it("charges one byte for the top of the 1-byte range", () => {
    // U+007F is the last code point UTF-8 spells in one byte. Charging it two
    // would hash a set that Go still encodes raw.
    expect(isHashed(join("\u007f".repeat(MAX_COMPONENT_BYTES)))).toBe(false);
    expect(isHashed(join("\u007f".repeat(MAX_COMPONENT_BYTES + 1)))).toBe(true);
  });

  it("charges two bytes for the top of the 2-byte range", () => {
    // U+07FF is the last 2-byte code point, so it is where a `<` written for a
    // `<=` shows up: charging it three bytes crosses the bound 1366 characters
    // early.
    expect(isHashed(join("\u07ff".repeat(MAX_COMPONENT_BYTES / 2)))).toBe(false);
    expect(isHashed(join("\u07ff".repeat(MAX_COMPONENT_BYTES / 2 + 1)))).toBe(true);
  });

  it("charges three bytes for a 3-byte code point", () => {
    // U+3042 is 3 bytes. 2730 of them are 8190 bytes, just inside the 8192-byte
    // bound; 2731 are 8193, just outside. Charging two bytes (or none) keeps the
    // second one raw while Go hashes it.
    expect(isHashed(join("あ".repeat(2730)))).toBe(false);
    expect(isHashed(join("あ".repeat(2731)))).toBe(true);
  });

  it("charges four bytes for an astral code point", () => {
    // U+1F511 arrives from the code-point iterator as one two-unit string. Read
    // as a lone surrogate instead it would be charged three bytes, the width
    // TextEncoder uses for the replacement character.
    expect(isHashed(join("🔑".repeat(MAX_COMPONENT_BYTES / 4)))).toBe(false);
    expect(isHashed(join("🔑".repeat(MAX_COMPONENT_BYTES / 4 + 1)))).toBe(true);
  });
});

describe("split", () => {
  it("refuses a hashed identity", () => {
    expect(() => split(join("x".repeat(MAX_COMPONENT_BYTES + 1)))).toThrow(HashedKeyError);
  });

  it("refuses a dangling escape", () => {
    expect(() => split("a" + ESCAPE)).toThrow(MalformedKeyError);
  });

  it("refuses an uppercase sha256 digest as malformed", () => {
    // join hashes any set whose escaped join would start with the prefix, so an
    // uppercase digest is in neither its hashed image (lowercase only) nor its
    // raw image, and matches isHashed, which also rejects it.
    expect(() => split("sha256:" + "A".repeat(64))).toThrow(MalformedKeyError);
  });

  it("refuses a wrong-length sha256 digest as malformed", () => {
    expect(() => split("sha256:" + "a".repeat(63))).toThrow(MalformedKeyError);
  });

  it("refuses an escape before an unreserved character", () => {
    // A lenient decoder would normalize this to "ab" and accept a key join can
    // never emit, so a tampered key would round-trip as if it were canonical.
    expect(() => split("a" + ESCAPE + "b")).toThrow(MalformedKeyError);
  });

  it("splits the empty key to no components", () => {
    expect(split("")).toEqual([]);
  });
});

describe("isHashed", () => {
  // isHashed is what a caller consults before split, so its answer decides
  // whether a key is treated as parseable data or as an opaque identity. Every
  // rejection below is one character away from the accepted form, because the
  // interesting inputs are not random strings but near-misses: a raw key that
  // happens to look digest-shaped, and a forged key built to be accepted.

  it("accepts the prefix followed by 64 hex characters", () => {
    expect(isHashed("sha256:" + "a".repeat(64))).toBe(true);
  });

  it("requires the hashed prefix rather than a digest-shaped tail", () => {
    // An ordinary two-component key whose first component is six characters
    // long puts 64 hex characters exactly where the digest sits. Classifying by
    // shape rather than by prefix would call this raw key hashed, and its
    // caller would skip the split that recovers ["abcdef", "aaa…"].
    expect(isHashed(join("abcdef", "a".repeat(64)))).toBe(false);
    expect(isHashed("sha256_" + "a".repeat(64))).toBe(false);
  });

  it("requires exactly 64 digest characters", () => {
    expect(isHashed("sha256:" + "a".repeat(63))).toBe(false);
    expect(isHashed("sha256:" + "a".repeat(65))).toBe(false);
  });

  it("requires every digest character to be hex", () => {
    expect(isHashed("sha256:" + "z".repeat(64))).toBe(false);
  });

  it("rejects an uppercase hex digest", () => {
    expect(isHashed("sha256:" + "A".repeat(64))).toBe(false);
  });

  it("anchors the digest match at both ends", () => {
    // Unanchored, a non-hex character at either end still leaves a 63-character
    // hex run for the match to find, so a key of the right length carrying a
    // forged prefix or suffix would pass as a hashed identity.
    expect(isHashed("sha256:z" + "a".repeat(63))).toBe(false);
    expect(isHashed("sha256:" + "a".repeat(63) + "z")).toBe(false);
  });
});

describe("properties", () => {
  // fast-check 4 replaced stringOf with string({ unit }); the unit generator
  // is what keeps the alphabet dominated by the two reserved characters, so the
  // budget is spent on inputs the escaping exists for.
  const component = fc.string({
    unit: fc.constantFrom("a", "b", SEPARATOR, ESCAPE, "é", "🔑"),
    maxLength: 6,
  });

  it("split inverts join", () => {
    fc.assert(
      fc.property(fc.array(component, { minLength: 2, maxLength: 6 }), (parts) => {
        const key = join(...parts);
        fc.pre(!isHashed(key));
        expect(split(key)).toEqual(parts);
      }),
    );
  });

  it("moving a boundary into a component always changes the key", () => {
    fc.assert(
      fc.property(
        fc.array(component, { minLength: 2, maxLength: 5 }),
        fc.nat(),
        (parts, rawIndex) => {
          const i = rawIndex % (parts.length - 1);
          const merged = [...parts];
          merged.splice(i, 2, parts[i]! + SEPARATOR + parts[i + 1]!);
          expect(join(...merged)).not.toBe(join(...parts));
        },
      ),
    );
  });

  it("accepts only keys it could have produced", () => {
    fc.assert(
      fc.property(fc.string({ maxLength: 24 }), (key) => {
        let parts: string[];
        try {
          parts = split(key);
        } catch (e) {
          expect(e === null || e instanceof HashedKeyError || e instanceof MalformedKeyError).toBe(
            true,
          );
          return;
        }
        expect(join(...parts)).toBe(key);
      }),
    );
  });
});
