import { decodeEnvelope, type ErrorEnvelope, type FieldError } from "../envelope.js";
import { REQUEST_ID_HEADER, WIRE_VERSION_HEADER } from "../headers.js";

/** One row of a generated client's `ENDPOINTS`. */
export interface GeneratedEndpoint<T> {
  readonly name: string;
  readonly method: string;
  /** The row's pattern: literal segments, `{name}` for one segment, `{name...}` last for the rest. */
  readonly path: string;
  /** The row's generated decoder; a throw is a refusal. */
  readonly decode: (v: unknown, status: number) => T;
  /** The row's error union. */
  readonly codes: readonly string[];
  /** The row's success statuses, the primary first. */
  readonly statuses: readonly number[];
}

/** A generated client's `ENDPOINTS`, keyed by row name. */
export type GeneratedEndpoints = Readonly<Record<string, GeneratedEndpoint<unknown>>>;

/** A catalog row as the document's `x-error-codes` publishes it. */
export interface ErrorCodeSpec {
  readonly status: number;
  readonly remediation: string;
}

/** The rows a fake server answers for, and the wire version every answer carries. */
export interface FakeServerOptions {
  readonly endpoints: GeneratedEndpoints;
  readonly wireVersion: string;
  /** Each code's status and remediation; `refuse` of a code missing here needs its own status. */
  readonly errorCodes?: Readonly<Record<string, ErrorCodeSpec>>;
}

/** One request the fake routed to a row. */
export interface FakeCall {
  readonly name: string;
  readonly url: string;
  /** The request body, parsed as JSON when it is JSON; absent when the request sent none. */
  readonly body?: unknown;
}

/** The `events.stream` row's connections. */
export interface FakeStream {
  /**
   * Sends one frame to every open connection. A `changed` or `listening` frame is held to the
   * feed's frame rules first, and an `sse:` name is the hub's own; each throws, as does a push
   * with no connection open.
   */
  push(event: string, data: unknown): void;
  /** Ends every open connection. */
  close(): void;
  readonly open: number;
}

/** An answer's status (the row's primary when absent) and extra headers. */
export interface AnswerOptions {
  readonly status?: number;
  readonly headers?: Readonly<Record<string, string>>;
  /** Answer only a request whose path parameters hold these values. */
  readonly params?: Readonly<Record<string, string>>;
}

/**
 * A refusal's status (the catalog's when absent), its envelope's `reason` and `fields`, and the
 * path parameters it is limited to.
 */
export interface RefuseOptions {
  readonly status?: number;
  readonly reason?: string;
  readonly fields?: readonly FieldError[];
  readonly params?: Readonly<Record<string, string>>;
}

/**
 * A fake of the server a generated client calls. Among a row's answers and refusals whose `params`
 * hold, the newest registered answers; `calls` lists each routed request in order.
 */
export interface FakeServer {
  readonly fetch: typeof fetch;
  answer<T>(ep: GeneratedEndpoint<T>, data: T, o?: AnswerOptions): void;
  refuse(ep: GeneratedEndpoint<unknown>, code: string, o?: RefuseOptions): void;
  stream(): FakeStream;
  readonly calls: readonly FakeCall[];
}

// The stream row changefeed's Endpoints names.
const STREAM_ROW = "events.stream";
const FEED_VERSION = 1;
const MAX_REV = 999_999_999_999_999;
const MAX_FRAME = 64 * 1024;
const MAX_KINDS = 64;
const SOURCE = /^[a-z][a-z0-9_]{0,31}$/;
const KIND = /^[a-z0-9_]{1,32}$/;
const EPOCH = /^[0-9a-f]{32}$/;
const WIRE = /^[0-9a-f]{16}$/;
// Statuses the Fetch standard answers with a null body.
const BODILESS = new Set([204, 205, 304]);

type Params = Readonly<Record<string, string>>;
type Matcher = (pathname: string) => Record<string, string> | undefined;

interface Route {
  readonly ep: GeneratedEndpoint<unknown>;
  readonly match: Matcher;
}

interface Registered {
  readonly params: Params | undefined;
  readonly respond: (requestId: string) => Response;
}

function segment(s: string): string | undefined {
  try {
    return decodeURIComponent(s);
  } catch {
    return undefined;
  }
}

// The ServeMux pattern forms a table row may hold: `{$}` matches only the trailing slash.
function compile(path: string): { match: Matcher; literals: number } {
  const parts = path.split("/").map((p) => (p === "{$}" ? "" : p));
  const literals = parts.filter((p) => !p.startsWith("{")).length;
  const match: Matcher = (pathname) => {
    const got = pathname.split("/");
    const params: Record<string, string> = {};
    for (const [i, p] of parts.entries()) {
      if (p.startsWith("{") && p.endsWith("...}")) {
        const rest = segment(got.slice(i).join("/"));
        if (rest === undefined || i !== parts.length - 1) {
          return undefined;
        }
        params[p.slice(1, -4)] = rest;
        return params;
      }
      const g = got[i];
      if (g === undefined) {
        return undefined;
      }
      if (p.startsWith("{")) {
        const v = segment(g);
        if (v === undefined || v === "") {
          return undefined;
        }
        params[p.slice(1, -1)] = v;
      } else if (p !== g) {
        return undefined;
      }
    }
    return got.length === parts.length ? params : undefined;
  };
  return { match, literals };
}

function holds(want: Params | undefined, got: Params): boolean {
  return want === undefined || Object.entries(want).every(([k, v]) => got[k] === v);
}

function isObject(v: unknown): v is Readonly<Record<string, unknown>> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function rev(v: unknown, lo: number): boolean {
  return typeof v === "number" && Number.isInteger(v) && v >= lo && v <= MAX_REV;
}

function onlyMembers(o: Readonly<Record<string, unknown>>, names: readonly string[]): boolean {
  return Object.keys(o).every((k) => names.includes(k));
}

function checkChanged(d: unknown): string | undefined {
  if (!isObject(d) || !onlyMembers(d, ["source", "epoch", "rev", "kinds"])) {
    return "not an object of source, epoch, rev and kinds";
  }
  if (typeof d["source"] !== "string" || !SOURCE.test(d["source"])) {
    return "source outside the source grammar";
  }
  if (typeof d["epoch"] !== "string" || !EPOCH.test(d["epoch"])) {
    return "epoch not 32 lowercase hex";
  }
  if (!rev(d["rev"], 1)) {
    return `rev not an integer from 1 to ${MAX_REV}`;
  }
  const kinds = d["kinds"];
  if (!Array.isArray(kinds) || kinds.length === 0 || kinds.length > MAX_KINDS) {
    return `kinds not 1 to ${MAX_KINDS} names`;
  }
  for (const [i, k] of (kinds as readonly unknown[]).entries()) {
    if (typeof k !== "string" || !KIND.test(k)) {
      return "a kind outside the kind grammar";
    }
    const prev: unknown = kinds[i - 1];
    if (typeof prev === "string" && prev >= k) {
      return "kinds not sorted without repeats";
    }
  }
  return undefined;
}

function checkListeningSource(s: unknown, seen: Set<string>): number | string {
  if (!isObject(s) || !onlyMembers(s, ["source", "epoch", "floors"])) {
    return "a source not an object of source, epoch and floors";
  }
  if (typeof s["source"] !== "string" || !SOURCE.test(s["source"]) || seen.has(s["source"])) {
    return "a source name outside the grammar or repeated";
  }
  seen.add(s["source"]);
  if (typeof s["epoch"] !== "string" || !EPOCH.test(s["epoch"])) {
    return "an epoch not 32 lowercase hex";
  }
  const floors = s["floors"];
  if (!isObject(floors)) {
    return "floors not an object";
  }
  for (const [k, f] of Object.entries(floors)) {
    if (!KIND.test(k) || !rev(f, 1)) {
      return `a floor outside the kind grammar or 1 to ${MAX_REV}`;
    }
  }
  return Object.keys(floors).length;
}

function checkListening(d: unknown): string | undefined {
  if (!isObject(d) || !onlyMembers(d, ["feed", "wire", "sources"])) {
    return "not an object of feed, wire and sources";
  }
  if (d["feed"] !== FEED_VERSION) {
    return `feed not ${FEED_VERSION}`;
  }
  if (Object.hasOwn(d, "wire") && (typeof d["wire"] !== "string" || !WIRE.test(d["wire"]))) {
    return "wire not 16 lowercase hex";
  }
  const sources = d["sources"];
  if (!Array.isArray(sources)) {
    return "sources not a list";
  }
  const seen = new Set<string>();
  let kinds = 0;
  for (const s of sources as readonly unknown[]) {
    const n = checkListeningSource(s, seen);
    if (typeof n === "string") {
      return n;
    }
    kinds += n;
  }
  return kinds > MAX_KINDS ? `more than ${MAX_KINDS} kinds` : undefined;
}

function checkFrame(event: string, data: unknown): string {
  if (event === "" || /[\r\n]/.test(event) || event.startsWith("sse:")) {
    throw new TypeError(`fake stream: ${JSON.stringify(event)} is not an app or feed event name`);
  }
  const text = JSON.stringify(data) as string | undefined;
  if (text === undefined) {
    throw new TypeError(`fake stream: ${event} data is not JSON`);
  }
  if (event !== "changed" && event !== "listening") {
    return text;
  }
  const problem =
    new TextEncoder().encode(text).length > MAX_FRAME
      ? "over 64 KiB"
      : event === "changed"
        ? checkChanged(data)
        : checkListening(data);
  if (problem !== undefined) {
    throw new TypeError(`fake stream: ${event} frame breaks the feed's rules: ${problem}`);
  }
  return text;
}

interface Connection {
  readonly send: (frame: string) => void;
  readonly end: () => void;
}

function hexEpoch(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
}

class Stream implements FakeStream {
  readonly #conns = new Set<Connection>();
  readonly #epoch = hexEpoch();
  #head = 0;

  get open(): number {
    return this.#conns.size;
  }

  push(event: string, data: unknown): void {
    const text = checkFrame(event, data);
    if (this.#conns.size === 0) {
      throw new Error(`fake stream: ${event} pushed with no connection open`);
    }
    // A listening frame comes from the connect hook, whose frames carry no id.
    let id = "";
    if (event !== "listening") {
      this.#head++;
      id = `id: ${this.#epoch}:${String(this.#head)}\n`;
    }
    for (const c of this.#conns) {
      c.send(`${id}event: ${event}\ndata: ${text}\n\n`);
    }
  }

  close(): void {
    for (const c of [...this.#conns]) {
      c.end();
    }
  }

  // The fake keeps no frames to replay, so only a cursor at the head resumes.
  #verdict(cursor: string | null): string {
    if (cursor === null) {
      return "fresh";
    }
    const m = /^([0-9a-f]{16}):(0|[1-9][0-9]{0,15})$/.exec(cursor);
    if (m === null) {
      return "cursor_invalid";
    }
    if (m[1] !== this.#epoch) {
      return "epoch_changed";
    }
    const offset = Number(m[2]);
    return offset > this.#head ? "gap_ahead" : offset < this.#head ? "gap_floor" : "resumed";
  }

  serve(req: Request, headers: Headers): Response {
    const verdict = this.#verdict(req.headers.get("Last-Event-ID"));
    const hello = JSON.stringify({
      wire: 1,
      epoch: this.#epoch,
      floor: "0",
      head: String(this.#head),
      resumed: verdict === "resumed",
      verdict,
      keepalive_ms: 15000,
      keepalive_event: "sse:keepalive",
    });
    const utf8 = new TextEncoder();
    let conn: Connection | undefined;
    const body = new ReadableStream<Uint8Array>({
      start: (ctl) => {
        const c: Connection = {
          send: (frame) => {
            ctl.enqueue(utf8.encode(frame));
          },
          end: () => {
            if (this.#conns.delete(c)) {
              ctl.close();
            }
          },
        };
        conn = c;
        this.#conns.add(c);
        ctl.enqueue(utf8.encode(`retry: 1500\n\nevent: sse:hello\ndata: ${hello}\n\n`));
        req.signal.addEventListener("abort", c.end);
      },
      cancel: () => {
        if (conn !== undefined) {
          this.#conns.delete(conn);
        }
      },
    });
    headers.set("Content-Type", "text/event-stream");
    headers.set("Cache-Control", "no-cache, no-transform");
    return new Response(body, { status: 200, headers });
  }
}

function readBody(text: string): unknown {
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

/**
 * A `fetch` that answers only what the given rows can answer: `answer` runs the row's decoder on
 * the data at the answer's status, `refuse` writes an envelope `decodeEnvelope` accepts for a code
 * of the row's union, and a request to a row with no answer, or to no row, rejects and is reported
 * through `reportError`, so a caller that swallows the rejection still fails the test.
 */
export function createFakeServer(o: FakeServerOptions): FakeServer {
  const routes: Route[] = Object.values(o.endpoints)
    .map((ep) => ({ ep, ...compile(ep.path) }))
    .sort((a, b) => b.literals - a.literals);
  const registered = new Map<string, Registered[]>();
  const calls: FakeCall[] = [];
  let requests = 0;
  let stream: Stream | undefined;

  const own = (ep: GeneratedEndpoint<unknown>): void => {
    if (o.endpoints[ep.name] !== ep) {
      throw new Error(`fake server: ${ep.name} is not one of the fake's endpoints`);
    }
  };
  const register = (name: string, r: Registered): void => {
    registered.set(name, [r, ...(registered.get(name) ?? [])]);
  };
  const headers = (requestId: string, extra?: Readonly<Record<string, string>>): Headers => {
    const h = new Headers({ [WIRE_VERSION_HEADER]: o.wireVersion, [REQUEST_ID_HEADER]: requestId });
    for (const [k, v] of Object.entries(extra ?? {})) {
      h.set(k, v);
    }
    return h;
  };
  const fail = (message: string): Promise<never> => {
    const err = new Error(`fake server: ${message}`);
    (globalThis as { readonly reportError?: (e: unknown) => void }).reportError?.(err);
    return Promise.reject(err);
  };

  const fakeFetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const req = new Request(input, init);
    const url = new URL(req.url);
    let route: Route | undefined;
    let params: Record<string, string> | undefined;
    for (const r of routes) {
      const method = req.method === "HEAD" ? "GET" : req.method;
      params = r.ep.method === method ? r.match(url.pathname) : undefined;
      if (params !== undefined) {
        route = r;
        break;
      }
    }
    if (route === undefined || params === undefined) {
      return fail(`${req.method} ${url.pathname} matches no row`);
    }
    const name = route.ep.name;
    const text = await req.text();
    calls.push(text === "" ? { name, url: req.url } : { name, url: req.url, body: readBody(text) });
    requests++;
    const requestId = `fake-${String(requests)}`;
    const got = params;
    const hit = registered.get(name)?.find((r) => holds(r.params, got));
    if (hit !== undefined) {
      const res = hit.respond(requestId);
      return req.method === "HEAD"
        ? new Response(null, { status: res.status, headers: res.headers })
        : res;
    }
    if (name === STREAM_ROW && stream !== undefined) {
      return stream.serve(req, headers(requestId));
    }
    return fail(`${name} has no answer for ${req.method} ${url.pathname}`);
  };

  return {
    fetch: fakeFetch,
    calls,
    answer<T>(ep: GeneratedEndpoint<T>, data: T, a: AnswerOptions = {}): void {
      own(ep);
      const status = a.status ?? ep.statuses[0];
      if (status === undefined || !ep.statuses.includes(status)) {
        throw new Error(`fake server: ${ep.name} declares no status ${String(status)}`);
      }
      const bodiless = BODILESS.has(status);
      if (bodiless && data !== undefined) {
        throw new Error(`fake server: ${ep.name} answers ${String(status)} with no body`);
      }
      const text = bodiless ? undefined : (JSON.stringify(data) as string | undefined);
      try {
        ep.decode(text === undefined ? undefined : JSON.parse(text), status);
      } catch (err) {
        throw new Error(`fake server: ${ep.name} cannot answer this data at ${String(status)}`, {
          cause: err,
        });
      }
      register(ep.name, {
        params: a.params,
        respond: (id) => {
          const h = headers(id, a.headers);
          if (text === undefined) {
            return new Response(null, { status, headers: h });
          }
          if (!h.has("Content-Type")) {
            h.set("Content-Type", "application/json");
          }
          return new Response(text, { status, headers: h });
        },
      });
    },
    refuse(ep: GeneratedEndpoint<unknown>, code: string, r: RefuseOptions = {}): void {
      own(ep);
      if (!ep.codes.includes(code)) {
        throw new Error(`fake server: ${code} is not in ${ep.name}'s union`);
      }
      const spec = o.errorCodes?.[code];
      const status = r.status ?? spec?.status;
      if (status === undefined || status < 400 || status > 599) {
        throw new Error(`fake server: ${code} needs a status from 400 to 599`);
      }
      if (spec !== undefined && spec.status !== status) {
        throw new Error(`fake server: the catalog answers ${code} as ${String(spec.status)}`);
      }
      const base = { code, message: code, remediation: spec?.remediation ?? "" };
      const extra = {
        ...(r.reason === undefined ? {} : { reason: r.reason }),
        ...(r.fields === undefined ? {} : { fields: r.fields }),
      };
      if (decodeEnvelope({ ...base, ...extra, request_id: "fake-0" }) === undefined) {
        throw new Error(`fake server: ${ep.name}'s ${code} refusal is not a valid envelope`);
      }
      register(ep.name, {
        params: r.params,
        respond: (id) => {
          const env: ErrorEnvelope = { ...base, request_id: id, ...extra };
          const h = headers(id, { "Content-Type": "application/json" });
          return new Response(JSON.stringify(env), { status, headers: h });
        },
      });
    },
    stream(): FakeStream {
      if (o.endpoints[STREAM_ROW] === undefined) {
        throw new Error(`fake server: no ${STREAM_ROW} row to stream on`);
      }
      stream ??= new Stream();
      return stream;
    },
  };
}
