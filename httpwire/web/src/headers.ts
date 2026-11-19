export const WIRE_VERSION_HEADER = "Wire-Version";
export const IDEMPOTENCY_HEADER = "Idempotency-Key";
export const REQUEST_ID_HEADER = "X-Request-Id";

const MAX_KEY = 255;
const PRINTABLE = /^[\x20-\x7e]+$/;
const TAG = /^[0-9a-f]{16}\.(0|[1-9][0-9]*)$/;
const MAX_REV = 2n ** 64n - 1n;

/**
 * A new idempotency key: a version 4 UUID (RFC 9562) from 16 `crypto.getRandomValues` bytes.
 * `crypto.randomUUID` needs a secure context, which a page served over plain HTTP is not.
 */
export function newIdempotencyKey(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = ((b[6] ?? 0) & 0x0f) | 0x40;
  b[8] = ((b[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/**
 * The `Idempotency-Key` value for `key`, an RFC 9651 String; undefined unless `key` is 1 to 255
 * printable ASCII characters, the only keys the server takes.
 */
export function idempotencyKeyHeader(key: string): string | undefined {
  if (key.length > MAX_KEY || !PRINTABLE.test(key)) {
    return undefined;
  }
  return `"${key.replace(/[\\"]/g, "\\$&")}"`;
}

/**
 * The `If-Match` value for a tag a read answered: the tag inside double quotes, one strong entity
 * tag. Undefined unless `tag` is 16 lowercase hex characters, a dot and a decimal with no leading
 * zero of at most 2^64 - 1.
 */
export function ifMatchHeader(tag: string): string | undefined {
  const m = TAG.exec(tag);
  if (m === null || BigInt(m[1] ?? "") > MAX_REV) {
    return undefined;
  }
  return `"${tag}"`;
}
