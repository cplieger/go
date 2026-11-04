/**
 * Synchronous SHA-256 over bytes.
 *
 * `crypto.subtle.digest` is the obvious choice and cannot be used here: it is
 * async, and every keyenc call site is a synchronous render, dedupe or
 * map-key path. Making `join` async to reach the hashed branch — taken only
 * above `MAX_COMPONENT_BYTES` — would colour the whole call graph for a case
 * most callers never hit. So the digest is implemented here instead, in ~60
 * lines with no dependencies, and pinned against the Go implementation by the
 * cross-language conformance fixture rather than trusted on inspection.
 *
 * FIPS 180-4. Not constant-time and not intended to be: keyenc hashes to bound
 * a key's size, never to authenticate anything.
 */

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

const rotr = (x: number, n: number): number => (x >>> n) | (x << (32 - n));

/** Streaming SHA-256. Feed bytes with {@link update}, read the digest with {@link hex}. */
export class Sha256 {
  #h = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  #buf = new Uint8Array(64);
  #buflen = 0;
  #total = 0;
  #w = new Uint32Array(64);

  update(bytes: Uint8Array): this {
    this.#total += bytes.length;
    let offset = 0;
    // Top up a partial block first, then consume whole blocks in place.
    if (this.#buflen > 0) {
      const need = 64 - this.#buflen;
      const take = Math.min(need, bytes.length);
      this.#buf.set(bytes.subarray(0, take), this.#buflen);
      this.#buflen += take;
      offset = take;
      if (this.#buflen < 64) {
        return this;
      }
      this.#block(this.#buf, 0);
      this.#buflen = 0;
    }
    for (; offset + 64 <= bytes.length; offset += 64) {
      this.#block(bytes, offset);
    }
    if (offset < bytes.length) {
      this.#buf.set(bytes.subarray(offset), 0);
      this.#buflen = bytes.length - offset;
    }
    return this;
  }

  /**
   * The digest of everything fed so far, as lowercase hex.
   *
   * Non-consuming: the padding is folded into a snapshot, so calling this twice
   * returns the same value and the stream stays usable afterwards. That is Go's
   * `hash.Hash.Sum` contract ("it does not change the underlying state"), and
   * matching it matters here because this class exists to mirror Go's digest
   * byte-for-byte — an API that diverged on reuse semantics would be a second,
   * quieter way for the two implementations to disagree.
   */
  hex(): string {
    // Snapshot every mutable field the padding touches. #w is scratch, rebuilt
    // per block, so it is deliberately not saved.
    const h = this.#h.slice();
    const buf = this.#buf.slice();
    const buflen = this.#buflen;
    const total = this.#total;

    // Pad: 0x80, zeroes, then the 64-bit big-endian bit length.
    const bitLen = total * 8;
    const padLen = buflen < 56 ? 56 - buflen : 120 - buflen;
    const tail = new Uint8Array(padLen + 8);
    tail[0] = 0x80;
    // Bit lengths beyond 2^53 are unreachable for an in-memory string, so a
    // float split into two 32-bit halves is exact here.
    const view = new DataView(tail.buffer);
    view.setUint32(padLen, Math.floor(bitLen / 0x100000000), false);
    view.setUint32(padLen + 4, bitLen >>> 0, false);
    this.update(tail);

    let out = "";
    for (let i = 0; i < 8; i++) {
      out += this.#h[i]!.toString(16).padStart(8, "0");
    }

    // Restore. Nothing above can throw (the block function is pure integer
    // arithmetic over fixed-size arrays), so a plain restore is sufficient.
    this.#h = h;
    this.#buf = buf;
    this.#buflen = buflen;
    this.#total = total;
    return out;
  }

  #block(data: Uint8Array, offset: number): void {
    const w = this.#w;
    for (let i = 0; i < 16; i++) {
      const j = offset + i * 4;
      w[i] = ((data[j]! << 24) | (data[j + 1]! << 16) | (data[j + 2]! << 8) | data[j + 3]!) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15]!;
      const b = w[i - 2]!;
      const s0 = rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3);
      const s1 = rotr(b, 17) ^ rotr(b, 19) ^ (b >>> 10);
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0;
    }
    const h = this.#h;
    let [a, b, c, d, e, f, g, hh] = [h[0]!, h[1]!, h[2]!, h[3]!, h[4]!, h[5]!, h[6]!, h[7]!];
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + K[i]! + w[i]!) >>> 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0]! + a) >>> 0;
    h[1] = (h[1]! + b) >>> 0;
    h[2] = (h[2]! + c) >>> 0;
    h[3] = (h[3]! + d) >>> 0;
    h[4] = (h[4]! + e) >>> 0;
    h[5] = (h[5]! + f) >>> 0;
    h[6] = (h[6]! + g) >>> 0;
    h[7] = (h[7]! + hh) >>> 0;
  }
}
