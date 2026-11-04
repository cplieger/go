import { describe, expect, it } from "vitest";
import { Sha256 } from "./sha256.js";

// Uint8Array<ArrayBuffer> rather than a bare Uint8Array: crypto.subtle.digest
// takes BufferSource, which excludes a SharedArrayBuffer-backed view.
const hex = (bytes: Uint8Array<ArrayBuffer>): Promise<string> =>
  crypto.subtle
    .digest("SHA-256", bytes)
    .then((d) => [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join(""));

const enc = new TextEncoder();

describe("Sha256.hex is non-consuming", () => {
  // Go's hash.Hash.Sum contract: "it does not change the underlying state".
  // This class exists to mirror Go's digest byte-for-byte, so a divergence in
  // reuse semantics would be a second, quieter way for the two implementations
  // to disagree. Before this was fixed, a second hex() folded a second padding
  // block into the already-finalized state and returned garbage.
  it("returns the same digest when called repeatedly", () => {
    const h = new Sha256().update(enc.encode("abc"));
    const first = h.hex();
    expect(h.hex()).toBe(first);
    expect(h.hex()).toBe(first);
    expect(first).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });

  it("leaves the stream usable, so an interim digest is still correct", async () => {
    const h = new Sha256().update(enc.encode("abc"));
    const interim = h.hex();
    expect(interim).toBe(await hex(enc.encode("abc")));

    h.update(enc.encode("def"));
    expect(h.hex()).toBe(await hex(enc.encode("abcdef")));

    // And the interim digest was not retroactively invalidated.
    expect(interim).toBe(await hex(enc.encode("abc")));
  });

  it("is non-consuming across every padding branch", async () => {
    // The padding branch turns at 55/56 and again at 119/120 bytes, and a
    // reuse bug can hide in whichever branch the fixtures happen to miss.
    for (const n of [0, 1, 55, 56, 57, 63, 64, 65, 119, 120, 121, 200]) {
      const bytes = new Uint8Array(n).map((_, i) => (i * 31 + 7) & 0xff);
      const want = await hex(bytes);
      const h = new Sha256().update(bytes);
      expect(h.hex(), `length ${n}, first call`).toBe(want);
      expect(h.hex(), `length ${n}, second call`).toBe(want);
      // Continuing the stream after a digest still tracks the full input.
      h.update(enc.encode("x"));
      expect(h.hex(), `length ${n}, after continuing`).toBe(
        await hex(new Uint8Array([...bytes, ...enc.encode("x")])),
      );
    }
  });

  it("digests an empty stream without consuming it", async () => {
    const h = new Sha256();
    const empty = await hex(new Uint8Array(0));
    expect(h.hex()).toBe(empty);
    expect(h.hex()).toBe(empty);
    h.update(enc.encode("abc"));
    expect(h.hex()).toBe(await hex(enc.encode("abc")));
  });
});
