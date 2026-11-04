/**
 * keyenc — join several untrusted strings into one key that no component's
 * content can forge.
 *
 * The TypeScript half of github.com/cplieger/go/keyenc. It produces
 * byte-identical keys to the Go half for every valid-UTF-8 input, pinned by the
 * shared conformance fixture in `conformance/keys.json`. That parity is
 * load-bearing wherever a key built in one language is looked up against keys
 * built in the other.
 *
 * See the Go package documentation for the grammar, the nesting rule and the
 * reasoning behind the size bound. The short version: `:` separates components
 * and `\` escapes, both escaped element-wise inside every component, so a
 * component carrying a separator cannot shift the split. A component carrying
 * neither is emitted verbatim, which is what lets an existing key adopt this
 * package without changing its bytes.
 */

import { Sha256 } from "./sha256.js";

/**
 * Total raw size of a component set, in UTF-8 BYTES, above which {@link join}
 * returns a fixed-size hashed identity instead of an escaped join.
 *
 * Bytes, not UTF-16 code units: the Go half measures `len(s)`, so measuring
 * `String.length` here would cross the threshold at a different point for any
 * multibyte input and break parity. The conformance fixture pins a case that
 * fails exactly that way.
 */
export const MAX_COMPONENT_BYTES = 8 << 10;

/** The character {@link join} places between components. */
export const SEPARATOR = ":";

/** The character {@link join} prefixes to a literal separator or escape. */
export const ESCAPE = "\\";

const HASHED_PREFIX = "sha256:";
const HASHED_HEX_LEN = 64;

/** Thrown by {@link split} when a key is a hashed identity, whose components are not recoverable. */
export class HashedKeyError extends Error {
  constructor() {
    super("keyenc: key is a hashed identity and cannot be split");
    this.name = "HashedKeyError";
  }
}

/** Thrown by {@link split} when a key was not produced by {@link join}. */
export class MalformedKeyError extends Error {
  constructor() {
    super("keyenc: key was not produced by join");
    this.name = "MalformedKeyError";
  }
}

const encoder = new TextEncoder();

/**
 * UTF-8 byte length, counted without allocating an encoded copy.
 *
 * Matches `TextEncoder` exactly, including its treatment of a lone surrogate as
 * the 3-byte replacement character — the hashing path encodes through
 * `TextEncoder`, so a counter that disagreed would put the size decision and
 * the digest on different views of the same string.
 */
function utf8Length(s: string): number {
  let n = 0;
  for (const ch of s) {
    // Iteration yields code points, so a well-formed surrogate pair arrives as
    // one two-unit string: 4 UTF-8 bytes. A lone surrogate arrives as one unit
    // and falls through to the 3-byte case, which is what TextEncoder charges
    // for the replacement character it substitutes.
    if (ch.length === 2) {
      n += 4;
      continue;
    }
    const cu = ch.charCodeAt(0);
    if (cu <= 0x7f) {
      n += 1;
    } else if (cu <= 0x7ff) {
      n += 2;
    } else {
      n += 3;
    }
  }
  return n;
}

function escapePart(part: string): string {
  // Escape order matters: doubling backslashes first keeps the mapping
  // injective, because the escapes introduced for separators must not
  // themselves be doubled afterwards. Both reserved characters are ASCII, so
  // iterating code points rather than code units cannot split one.
  let out = "";
  for (const c of part) {
    if (c === ESCAPE || c === SEPARATOR) {
      out += ESCAPE;
    }
    out += c;
  }
  return out;
}

function hashParts(parts: readonly string[]): string {
  const h = new Sha256();
  const lenBuf = new Uint8Array(8);
  const view = new DataView(lenBuf.buffer);
  for (const part of parts) {
    const bytes = encoder.encode(part);
    // 8-byte big-endian length prefix, matching Go's binary.BigEndian.PutUint64.
    // A component longer than 2^32 bytes cannot exist in a JS string, so the
    // high word is always zero.
    view.setUint32(0, 0, false);
    view.setUint32(4, bytes.length, false);
    h.update(lenBuf);
    h.update(bytes);
  }
  return HASHED_PREFIX + h.hex();
}

/**
 * Encode `parts` as one key such that no part's content can forge a different
 * split: `join("a:b", "c")` and `join("a", "b:c")` differ, where a naive
 * template literal makes them identical.
 *
 * Above {@link MAX_COMPONENT_BYTES} of total raw input it returns the set's
 * hashed identity instead. An in-bound set whose escaped join would itself
 * begin with the hashed-identity prefix is also hashed, which keeps the raw and
 * hashed output domains disjoint.
 *
 * No parts encodes as the empty string; a single empty part is hashed, so it
 * cannot alias the zero-part encoding.
 */
export function join(...parts: string[]): string {
  if (parts.length === 0) {
    return "";
  }
  if (parts.length === 1 && parts[0] === "") {
    return hashParts(parts);
  }
  let total = 0;
  for (const part of parts) {
    total += utf8Length(part);
  }
  if (total <= MAX_COMPONENT_BYTES) {
    const joined = parts.map(escapePart).join(SEPARATOR);
    if (!joined.startsWith(HASHED_PREFIX)) {
      return joined;
    }
  }
  return hashParts(parts);
}

/**
 * Recover the exact components {@link join} encoded. Its accepted language is
 * exactly `join`'s image: an escape before anything other than a reserved
 * character, or a trailing dangling escape, throws {@link MalformedKeyError}
 * rather than being normalized, so "split accepted it" is a statement about the
 * key's provenance. A hashed identity throws {@link HashedKeyError}; any other
 * `sha256:`-prefixed key throws {@link MalformedKeyError}, since join emits no
 * raw key with that prefix.
 *
 * The empty key splits to no components, mirroring `join()`.
 */
export function split(key: string): string[] {
  if (key === "") {
    return [];
  }
  if (key.startsWith(HASHED_PREFIX)) {
    if (isHashedDigest(key.slice(HASHED_PREFIX.length))) {
      throw new HashedKeyError();
    }
    throw new MalformedKeyError();
  }
  const parts: string[] = [];
  let cur = "";
  let escaped = false;
  // Both reserved characters are ASCII, so iterating code points rather than
  // code units cannot split one, and reassembling by concatenation is exact.
  for (const c of key) {
    if (escaped) {
      if (c !== ESCAPE && c !== SEPARATOR) {
        throw new MalformedKeyError();
      }
      cur += c;
      escaped = false;
    } else if (c === ESCAPE) {
      escaped = true;
    } else if (c === SEPARATOR) {
      parts.push(cur);
      cur = "";
    } else {
      cur += c;
    }
  }
  if (escaped) {
    throw new MalformedKeyError();
  }
  parts.push(cur);
  return parts;
}

/**
 * Whether `key` is a hashed identity, i.e. whether {@link join} reduced an
 * oversized component set rather than encoding it. Call it before
 * {@link split} at a site that must parse its keys back.
 */
export function isHashed(key: string): boolean {
  return key.startsWith(HASHED_PREFIX) && isHashedDigest(key.slice(HASHED_PREFIX.length));
}

/**
 * Whether `rest` is a digest {@link hashParts} could have emitted: exactly
 * HASHED_HEX_LEN lowercase hex characters. join emits lowercase hex, so an
 * uppercase digest never came from join.
 */
function isHashedDigest(rest: string): boolean {
  return rest.length === HASHED_HEX_LEN && /^[0-9a-f]+$/.test(rest);
}
