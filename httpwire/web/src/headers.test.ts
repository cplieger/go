import { afterEach, describe, expect, it, vi } from "vitest";
import keysFile from "../testdata/idempotency-keys.json?raw";
import tagsFile from "../testdata/tags.json?raw";
import {
  IDEMPOTENCY_HEADER,
  idempotencyKeyHeader,
  ifMatchHeader,
  newIdempotencyKey,
  REQUEST_ID_HEADER,
  WIRE_VERSION_HEADER,
} from "./headers.js";

// Go's httpwire test reads both files through its own If-Match and Idempotency-Key
// parse, so an accepted value here is one the server accepts.
const tags = JSON.parse(tagsFile) as { tag: string; valid: boolean }[];
const keys = JSON.parse(keysFile) as { key: string; header: string | null }[];

// RFC 9562 section 5.4: version 4 in the 13th hex digit, variant 10 in the 17th.
const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("the header names", () => {
  it("are the names the Go binder reads and writes", () => {
    expect(WIRE_VERSION_HEADER).toBe("Wire-Version");
    expect(IDEMPOTENCY_HEADER).toBe("Idempotency-Key");
    expect(REQUEST_ID_HEADER).toBe("X-Request-Id");
  });
});

describe("ifMatchHeader", () => {
  it("quotes a tag", () => {
    expect(ifMatchHeader("0123456789abcdef.7")).toBe('"0123456789abcdef.7"');
    expect(ifMatchHeader("ffffffffffffffff.18446744073709551615")).toBe(
      '"ffffffffffffffff.18446744073709551615"',
    );
  });

  it("answers undefined for a value outside the tag grammar", () => {
    for (const tag of [
      "7",
      "0123456789ABCDEF.7",
      "0123456789abcde.7",
      "0123456789abcdef.07",
      "0123456789abcdef.18446744073709551616",
    ]) {
      expect(ifMatchHeader(tag), tag).toBeUndefined();
    }
  });

  it("gives every corpus tag the Go parse's verdict", () => {
    expect(tags.length).toBeGreaterThan(20);
    for (const { tag, valid } of tags) {
      expect(ifMatchHeader(tag), JSON.stringify(tag)).toBe(valid ? `"${tag}"` : undefined);
    }
  });
});

describe("idempotencyKeyHeader", () => {
  it("writes every corpus key as the Go parse reads it, or undefined", () => {
    expect(keys.length).toBeGreaterThan(10);
    for (const { key, header } of keys) {
      expect(idempotencyKeyHeader(key), JSON.stringify(key)).toBe(header ?? undefined);
    }
  });
});

describe("newIdempotencyKey", () => {
  it("writes 16 random bytes as a version 4 UUID", () => {
    const fill = (bytes: number[]) =>
      vi.spyOn(crypto, "getRandomValues").mockImplementation((a) => {
        new Uint8Array(a.buffer, a.byteOffset, a.byteLength).set(bytes);
        return a;
      });
    fill([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15]);
    expect(newIdempotencyKey()).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
    fill(Array.from({ length: 16 }, () => 0xff));
    expect(newIdempotencyKey()).toBe("ffffffff-ffff-4fff-bfff-ffffffffffff");
  });

  it("answers the v4 form on 10,000 draws with no repeat", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 10_000; i++) {
      const k = newIdempotencyKey();
      expect(V4.test(k), k).toBe(true);
      seen.add(k);
    }
    expect(seen.size).toBe(10_000);
  });

  describe("on a page that is not a secure context", () => {
    const own = Object.getOwnPropertyDescriptor(Crypto.prototype, "randomUUID");

    afterEach(() => {
      if (own !== undefined) {
        Object.defineProperty(Crypto.prototype, "randomUUID", own);
      }
    });

    it("mints a key the Go parse accepts with crypto.randomUUID gone", () => {
      Reflect.deleteProperty(Crypto.prototype, "randomUUID");
      vi.stubGlobal("isSecureContext", false);
      expect(Reflect.get(crypto, "randomUUID")).toBeUndefined();
      expect(globalThis.isSecureContext).toBe(false);

      const k = newIdempotencyKey();
      expect(V4.test(k), k).toBe(true);
      expect(idempotencyKeyHeader(k)).toBe(`"${k}"`);
      // The corpus's UUID-shaped key is the case the Go parse checks for this form.
      expect(keys.some(({ key, header }) => V4.test(key) && header === `"${key}"`)).toBe(true);
    });
  });
});
