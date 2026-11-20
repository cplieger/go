/** One refused field: its path in the request, its code and a sentence for the user. */
export interface FieldError {
  /** The field's path: a body's dotted members and `[i]` items (empty for the whole body), or a query key or header name. */
  path: string;
  /** Why the field was refused: a `SCHEMA_FIELD_CODES` code or one the app's validators write. */
  code: string;
  /** A sentence for the user, in the server's words. */
  message: string;
}

/**
 * The codes the Go server's decode and schema check write into `FieldError.code`. A field's own
 * refusal code and an app's validators write other codes into the same list, so `code` stays `string`.
 */
export const SCHEMA_FIELD_CODES = [
  "malformed",
  "required",
  "unknown",
  "enum",
  "union",
  "range",
  "pattern",
] as const;
Object.freeze(SCHEMA_FIELD_CODES);

/** A refusal's body, as `decodeEnvelope` answers it. */
export interface ErrorEnvelope {
  /** The refusal's code, one of the row's error union. */
  code: string;
  /** A sentence for the user, in the server's words. */
  message: string;
  /** What the user can do next, the catalog's text for `code`. */
  remediation: string;
  /** The id of the request that wrote this refusal, which a replay keeps from the first run. */
  request_id: string;
  /** A finer machine reason under `code`, in the code grammar. */
  reason?: string;
  /** Each refused field, when the refusal names fields. */
  fields?: readonly FieldError[];
  /** How long to wait before a retry, in milliseconds. */
  retry_after_ms?: number;
  /** What another service said. Untrusted: show it only as quoted text under the app's own sentence. */
  upstream?: { service: string; status?: number; message: string };
}

// The bounds the Go writer and DecodeEnvelope hold too; string lengths are UTF-8 bytes.
/** @internal The bound on a free-text member, such as a message or remediation. */
export const MAX_STRING = 4096;
const MAX_REASON = 64;
const MAX_FIELDS = 100;
const MAX_UPSTREAM_MESSAGE = 512;

const CODE = /^[a-z0-9_]+$/;
const utf8 = new TextEncoder();

type Members = Readonly<Record<string, unknown>>;

/** @internal Reports whether v is a JSON object, not null or an array. */
export function isObject(v: unknown): v is Members {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// A lone surrogate encodes as U+FFFD's three bytes, as Go's decoder reads the same escape.
function str(o: Members, name: string, limit: number): string | undefined {
  if (!Object.hasOwn(o, name)) {
    return undefined;
  }
  const v = o[name];
  return typeof v === "string" && utf8.encode(v).length <= limit ? v : undefined;
}

/** @internal Answers o's member name when it is a code of at most limit bytes, else undefined. */
export function code(o: Members, name: string, limit: number): string | undefined {
  const v = str(o, name, limit);
  return v !== undefined && CODE.test(v) ? v : undefined;
}

function integer(v: unknown, lo: number, hi: number): number | undefined {
  if (typeof v !== "number" || !Number.isInteger(v) || v < lo || v > hi) {
    return undefined;
  }
  // -0 answers 0, as Go's integer conversion does.
  return v === 0 ? 0 : v;
}

function decodeFields(v: unknown): FieldError[] | undefined {
  if (!Array.isArray(v) || v.length > MAX_FIELDS) {
    return undefined;
  }
  const out: FieldError[] = [];
  for (const item of v as readonly unknown[]) {
    if (!isObject(item)) {
      return undefined;
    }
    const path = str(item, "path", MAX_STRING);
    const c = str(item, "code", MAX_STRING);
    const message = str(item, "message", MAX_STRING);
    if (path === undefined || c === undefined || message === undefined) {
      return undefined;
    }
    out.push({ path, code: c, message });
  }
  return out;
}

/** @internal Answers v as an envelope's upstream member, or undefined when it breaks the member's rules. */
export function decodeUpstream(v: unknown): ErrorEnvelope["upstream"] {
  if (!isObject(v)) {
    return undefined;
  }
  const service = code(v, "service", MAX_STRING);
  const message = str(v, "message", MAX_UPSTREAM_MESSAGE);
  if (service === undefined || message === undefined) {
    return undefined;
  }
  if (!Object.hasOwn(v, "status")) {
    return { service, message };
  }
  const status = integer(v["status"], 100, 599);
  return status === undefined ? undefined : { service, status, message };
}

/**
 * Decodes an error body by the rules the Go DecodeEnvelope holds: code, message,
 * remediation and request_id present and strings; code, reason and upstream.service in
 * the code grammar; reason at most 64 bytes, fields at most 100 entries, upstream.message
 * at most 512 bytes and every other string at most 4 KiB; retry_after_ms an integer from
 * 0 to 2^53 - 1 and upstream.status one from 100 to 599. A member present with any other
 * value, null and undefined included, answers undefined; an unknown member is dropped.
 */
export function decodeEnvelope(v: unknown): ErrorEnvelope | undefined {
  if (!isObject(v)) {
    return undefined;
  }
  const c = code(v, "code", MAX_STRING);
  const message = str(v, "message", MAX_STRING);
  const remediation = str(v, "remediation", MAX_STRING);
  const requestId = str(v, "request_id", MAX_STRING);
  if (
    c === undefined ||
    message === undefined ||
    remediation === undefined ||
    requestId === undefined
  ) {
    return undefined;
  }
  const env: ErrorEnvelope = { code: c, message, remediation, request_id: requestId };
  if (Object.hasOwn(v, "reason")) {
    const reason = code(v, "reason", MAX_REASON);
    if (reason === undefined) {
      return undefined;
    }
    env.reason = reason;
  }
  if (Object.hasOwn(v, "fields")) {
    const fields = decodeFields(v["fields"]);
    if (fields === undefined) {
      return undefined;
    }
    env.fields = fields;
  }
  if (Object.hasOwn(v, "retry_after_ms")) {
    const ms = integer(v["retry_after_ms"], 0, Number.MAX_SAFE_INTEGER);
    if (ms === undefined) {
      return undefined;
    }
    env.retry_after_ms = ms;
  }
  if (Object.hasOwn(v, "upstream")) {
    const upstream = decodeUpstream(v["upstream"]);
    if (upstream === undefined) {
      return undefined;
    }
    env.upstream = upstream;
  }
  return env;
}
