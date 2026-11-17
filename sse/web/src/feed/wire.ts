import { FEED_VERSION } from "./version.js";

// The browser half of feedwire's frame and header wire. Every check here answers the same verdict
// as the Go decoder on every input; testdata/feed/cases.json holds both halves to it.

/** A feed frame's data bound, in UTF-8 bytes. */
export const MAX_FEED_FRAME_BYTES = 64 * 1024;

/** The largest revision a frame or a Feed-Position value carries: the largest RFC 9651 Integer. */
export const MAX_REV = 999_999_999_999_999;

const MAX_KINDS = 64;

// encoding/json refuses a document nested deeper than this (its scanner's maxNestingDepth), so
// feedwire reads no frozen member from one; refusing it here keeps both halves' verdicts equal.
const MAX_NESTING = 10_000;

export interface ChangedFrame {
  readonly source: string;
  readonly epoch: string;
  readonly rev: number;
  readonly kinds: readonly string[];
}

interface ListeningSource {
  readonly source: string;
  readonly epoch: string;
  readonly floors: ReadonlyMap<string, number>;
}

export interface ListeningFrame {
  /** `null` when the frame carries no `wire` member. */
  readonly wire: string | null;
  readonly sources: readonly ListeningSource[];
}

/** The two members of a listening frame that keep their name, type and place in every format. */
export interface Frozen {
  readonly feed: number;
  readonly wire: string | null;
}

export interface Position {
  readonly source: string;
  readonly epoch: string;
  readonly rev: number;
}

interface Refused {
  readonly ok: false;
  readonly reason: string;
}

type ChangedResult = { readonly ok: true; readonly changed: ChangedFrame } | Refused;
type PeekResult = { readonly ok: true; readonly frozen: Frozen } | Refused;
type PositionResult = { readonly ok: true; readonly positions: readonly Position[] } | Refused;

/**
 * `other_format`: the frozen members read and `feed` is not `FEED_VERSION`, so nothing else was
 * looked at. `invalid` carries the frozen members when they read and the rest did not parse.
 */
type ListeningResult =
  | { readonly kind: "ok"; readonly frozen: Frozen; readonly listening: ListeningFrame }
  | { readonly kind: "other_format"; readonly frozen: Frozen }
  | { readonly kind: "invalid"; readonly frozen: Frozen | null; readonly reason: string };

const SOURCE_RE = /^[a-z][a-z0-9_]{0,31}$/;
const KIND_RE = /^[a-z0-9_]{1,32}$/;
const EPOCH_RE = /^[0-9a-f]{32}$/;
const WIRE_RE = /^[0-9a-f]{16}$/;

/** A lowercase letter, then at most 31 lowercase letters, digits or underscores. */
export const validSource = (name: string): boolean => SOURCE_RE.test(name);
/** 1 to 32 lowercase letters, digits or underscores. */
export const validKind = (name: string): boolean => KIND_RE.test(name);
/** 32 lowercase hex. */
export const validEpoch = (epoch: string): boolean => EPOCH_RE.test(epoch);
/** 16 lowercase hex. */
export const validWire = (wire: string): boolean => WIRE_RE.test(wire);

const refuse = (reason: string): Refused => ({ ok: false, reason });

type JsonObject = Readonly<Record<string, unknown>>;

const isObject = (v: unknown): v is JsonObject =>
  typeof v === "object" && v !== null && !Array.isArray(v);

// JSON.parse defines every member as an own property, so a member named like an Object.prototype
// accessor (__proto__, constructor) reads as data only through an own-property check.
const member = (o: JsonObject, name: string): unknown =>
  Object.hasOwn(o, name) ? o[name] : undefined;

function onlyMembers(
  o: JsonObject,
  required: readonly string[],
  optional: readonly string[] = [],
): boolean {
  return (
    required.every((name) => Object.hasOwn(o, name)) &&
    Object.keys(o).every((name) => required.includes(name) || optional.includes(name))
  );
}

const utf8 = new TextEncoder();

function nestingWithin(data: string): boolean {
  let depth = 0;
  let inString = false;
  for (let i = 0; i < data.length; i++) {
    const c = data.charCodeAt(i);
    if (inString) {
      if (c === 0x5c) {
        i++;
      } else if (c === 0x22) {
        inString = false;
      }
    } else if (c === 0x22) {
      inString = true;
    } else if (c === 0x7b || c === 0x5b) {
      if (++depth > MAX_NESTING) {
        return false;
      }
    } else if (c === 0x7d || c === 0x5d) {
      depth--;
    }
  }
  return true;
}

type Parsed = { readonly ok: true; readonly obj: JsonObject } | Refused;

function frameObject(data: string): Parsed {
  // A string's UTF-8 length is at least its UTF-16 length, so the first test skips the encode.
  if (data.length > MAX_FEED_FRAME_BYTES || utf8.encode(data).byteLength > MAX_FEED_FRAME_BYTES) {
    return refuse("frame data above 64 KiB");
  }
  if (!nestingWithin(data)) {
    return refuse("frame data nested deeper than 10,000");
  }
  let v: unknown;
  try {
    v = JSON.parse(data);
  } catch {
    return refuse("frame data is not JSON");
  }
  return isObject(v) ? { ok: true, obj: v } : refuse("frame data is not a JSON object");
}

// Every JSON number reads as an IEEE double, as encoding/json's float parse does in feedwire, so
// 1.0 and 1e3 are integers in both halves.
const rev = (v: unknown, low: number): v is number =>
  typeof v === "number" && Number.isInteger(v) && v >= low && v <= MAX_REV;

/** Parses a `changed` frame's data strictly: the four members and no other, each in bounds. */
export function parseChanged(data: string): ChangedResult {
  const parsed = frameObject(data);
  if (!parsed.ok) {
    return parsed;
  }
  const o = parsed.obj;
  if (!onlyMembers(o, ["source", "epoch", "rev", "kinds"])) {
    return refuse("changed frame members are not source, epoch, rev and kinds");
  }
  const source = member(o, "source");
  const epoch = member(o, "epoch");
  const r = member(o, "rev");
  const kinds = member(o, "kinds");
  if (typeof source !== "string" || !validSource(source)) {
    return refuse("changed frame source is not a source name");
  }
  if (typeof epoch !== "string" || !validEpoch(epoch)) {
    return refuse("changed frame epoch is not 32 lowercase hex");
  }
  if (!rev(r, 1)) {
    return refuse("changed frame rev is not an integer from 1 to MAX_REV");
  }
  if (!Array.isArray(kinds) || kinds.length === 0 || kinds.length > MAX_KINDS) {
    return refuse("changed frame kinds are not 1 to 64 names");
  }
  const names: string[] = [];
  for (const k of kinds as readonly unknown[]) {
    const prev = names.at(-1);
    if (typeof k !== "string" || !validKind(k) || (prev !== undefined && prev >= k)) {
      return refuse("changed frame kinds are not sorted, distinct kind names");
    }
    names.push(k);
  }
  return { ok: true, changed: { source, epoch, rev: r, kinds: names } };
}

function peekObject(o: JsonObject): PeekResult {
  const feed = member(o, "feed");
  if (typeof feed !== "number" || !Number.isSafeInteger(feed)) {
    return refuse("listening frame has no integer feed member");
  }
  let wire: string | null = null;
  if (Object.hasOwn(o, "wire")) {
    const w = member(o, "wire");
    if (typeof w !== "string" || !validWire(w)) {
      return refuse("listening frame wire is not 16 lowercase hex");
    }
    wire = w;
  }
  // + 0 turns a -0 into 0, which is the integer feedwire reads.
  return { ok: true, frozen: { feed: feed + 0, wire } };
}

function parseSource(raw: unknown): ListeningSource | string {
  if (!isObject(raw) || !onlyMembers(raw, ["source", "epoch", "floors"])) {
    return "listening source members are not source, epoch and floors";
  }
  const source = member(raw, "source");
  const epoch = member(raw, "epoch");
  const floors = member(raw, "floors");
  if (typeof source !== "string" || !validSource(source)) {
    return "listening source is not a source name";
  }
  if (typeof epoch !== "string" || !validEpoch(epoch)) {
    return "listening source epoch is not 32 lowercase hex";
  }
  if (!isObject(floors)) {
    return "listening source floors is not an object";
  }
  const out = new Map<string, number>();
  for (const k of Object.keys(floors)) {
    const f = member(floors, k);
    if (!validKind(k) || !rev(f, 1)) {
      return "listening floor is not a kind name with an integer from 1 to MAX_REV";
    }
    out.set(k, f);
  }
  return { source, epoch, floors: out };
}

/**
 * Parses a listening frame's data: the frozen members first, then, when `feed` is
 * `FEED_VERSION`, the rest strictly (`feed`, `sources` and an optional `wire`, no other member).
 */
export function parseListening(data: string): ListeningResult {
  const parsed = frameObject(data);
  if (!parsed.ok) {
    return { kind: "invalid", frozen: null, reason: parsed.reason };
  }
  const o = parsed.obj;
  const peeked = peekObject(o);
  if (!peeked.ok) {
    return { kind: "invalid", frozen: null, reason: peeked.reason };
  }
  const { frozen } = peeked;
  if (frozen.feed !== FEED_VERSION) {
    return { kind: "other_format", frozen };
  }
  const invalid = (reason: string): ListeningResult => ({ kind: "invalid", frozen, reason });
  const raws = member(o, "sources");
  if (!onlyMembers(o, ["feed", "sources"], ["wire"]) || !Array.isArray(raws)) {
    return invalid("listening frame members are not feed, wire and sources");
  }
  const sources: ListeningSource[] = [];
  let kinds = 0;
  for (const raw of raws as readonly unknown[]) {
    const s = parseSource(raw);
    if (typeof s === "string") {
      return invalid(s);
    }
    if (sources.some((p) => p.source === s.source)) {
      return invalid("listening frame names a source twice");
    }
    kinds += s.floors.size;
    if (kinds > MAX_KINDS) {
      return invalid("listening frame holds more than 64 kinds");
    }
    sources.push(s);
  }
  return { kind: "ok", frozen, listening: { wire: frozen.wire, sources } };
}

/**
 * Parses a Feed-Position value as an RFC 9651 dictionary (a repeated key overwrites its value in
 * place) into its members in order. Each member is an Integer from 0 to `MAX_REV` under a
 * `validSource` key, with an `epoch` parameter that is a String of 32 lowercase hex; other
 * parameters are ignored and any other member refuses the whole value. An empty value is an
 * empty dictionary: no position.
 */
export function parsePosition(value: string): PositionResult {
  const members = parseDictionary(value);
  if (members === null) {
    return refuse("Feed-Position is not an RFC 9651 dictionary");
  }
  const positions: Position[] = [];
  for (const m of members) {
    if (!validSource(m.key)) {
      return refuse("Feed-Position key is not a source name");
    }
    if (m.inner || m.item.kind !== "integer" || m.item.num < 0 || m.item.num > MAX_REV) {
      return refuse("Feed-Position member is not an Integer from 0 to MAX_REV");
    }
    if (m.epoch.kind !== "string" || !validEpoch(m.epoch.str)) {
      return refuse("Feed-Position member has no epoch parameter of 32 lowercase hex");
    }
    positions.push({ source: m.key, epoch: m.epoch.str, rev: m.item.num + 0 });
  }
  return { ok: true, positions };
}

type SfItem =
  | { readonly kind: "integer"; readonly num: number }
  | { readonly kind: "string"; readonly str: string }
  | { readonly kind: "none" | "other" };

interface SfMember {
  readonly key: string;
  readonly item: SfItem;
  readonly epoch: SfItem;
  readonly inner: boolean;
}

const OTHER: SfItem = { kind: "other" };
const NONE: SfItem = { kind: "none" };

// Thrown inside the RFC 9651 parser only; parseDictionary turns it into a refusal.
class SfSyntax extends Error {}

const isDigit = (c: string): boolean => c >= "0" && c <= "9";
const isLcalpha = (c: string): boolean => c >= "a" && c <= "z";
const isAlpha = (c: string): boolean => isLcalpha(c) || (c >= "A" && c <= "Z");
const TCHAR_EXTRA = "!#$%&'*+-.^_`|~";
const isTchar = (c: string): boolean =>
  c !== "" && (isAlpha(c) || isDigit(c) || TCHAR_EXTRA.includes(c));
const isBase64 = (c: string): boolean =>
  c !== "" && (isAlpha(c) || isDigit(c) || c === "+" || c === "/" || c === "=");
const isLowerHex = (c: string): boolean => isDigit(c) || (c >= "a" && c <= "f");

/** The parsing algorithms of RFC 9651 section 4.2, keeping only what a Feed-Position reads. */
class SfParser {
  #i = 0;
  readonly #s: string;

  constructor(s: string) {
    this.#s = s;
  }

  #peek(): string {
    return this.#s.charAt(this.#i);
  }

  #done(): boolean {
    return this.#i >= this.#s.length;
  }

  #skipSP(): void {
    while (this.#peek() === " ") {
      this.#i++;
    }
  }

  #skipOWS(): void {
    while (this.#peek() === " " || this.#peek() === "\t") {
      this.#i++;
    }
  }

  // RFC 9651 section 4.2.2: a repeated key keeps its first place with its last value.
  dictionary(): SfMember[] {
    this.#skipSP();
    const members: SfMember[] = [];
    while (!this.#done()) {
      const m = this.#member();
      const j = members.findIndex((o) => o.key === m.key);
      if (j >= 0) {
        members[j] = m;
      } else {
        members.push(m);
      }
      this.#skipOWS();
      if (this.#done()) {
        break;
      }
      if (this.#peek() !== ",") {
        throw new SfSyntax();
      }
      this.#i++;
      this.#skipOWS();
      if (this.#done()) {
        throw new SfSyntax();
      }
    }
    return members;
  }

  #member(): SfMember {
    const key = this.#key();
    let item = OTHER;
    let inner = false;
    if (this.#peek() === "=") {
      this.#i++;
      if (this.#peek() === "(") {
        inner = true;
        this.#innerList();
      } else {
        item = this.#bareItem();
      }
    }
    return { key, item, inner, epoch: this.#parameters() };
  }

  // RFC 9651 section 4.2.3.3.
  #key(): string {
    const start = this.#i;
    const first = this.#peek();
    if (!isLcalpha(first) && first !== "*") {
      throw new SfSyntax();
    }
    for (;;) {
      const c = this.#peek();
      if (!isLcalpha(c) && !isDigit(c) && c !== "_" && c !== "-" && c !== "." && c !== "*") {
        return this.#s.slice(start, this.#i);
      }
      this.#i++;
    }
  }

  // RFC 9651 section 4.2.3.2; answers the last epoch parameter's value.
  #parameters(): SfItem {
    let epoch = NONE;
    while (this.#peek() === ";") {
      this.#i++;
      this.#skipSP();
      const key = this.#key();
      let value = OTHER;
      if (this.#peek() === "=") {
        this.#i++;
        value = this.#bareItem();
      }
      if (key === "epoch") {
        epoch = value;
      }
    }
    return epoch;
  }

  // RFC 9651 section 4.2.1.2, read for its syntax only.
  #innerList(): void {
    this.#i++;
    while (!this.#done()) {
      this.#skipSP();
      if (this.#peek() === ")") {
        this.#i++;
        return;
      }
      this.#bareItem();
      this.#parameters();
      const c = this.#peek();
      if (c !== " " && c !== ")") {
        throw new SfSyntax();
      }
    }
    throw new SfSyntax();
  }

  // RFC 9651 section 4.2.3.1: only Integers and Strings keep their value.
  #bareItem(): SfItem {
    const c = this.#peek();
    if (c === "-" || isDigit(c)) {
      return this.#number();
    }
    if (c === '"') {
      return this.#string();
    }
    if (c === "*" || isAlpha(c)) {
      this.#token();
    } else if (c === ":") {
      this.#byteSequence();
    } else if (c === "?") {
      this.#boolean();
    } else if (c === "@") {
      this.#i++;
      if (this.#number().kind !== "integer") {
        throw new SfSyntax();
      }
    } else if (c === "%") {
      this.#displayString();
    } else {
      throw new SfSyntax();
    }
    return OTHER;
  }

  // RFC 9651 section 4.2.4: an Integer has at most 15 digits; a Decimal at most 12 before its
  // point and 1 to 3 after it.
  #number(): SfItem {
    const start = this.#i;
    if (this.#peek() === "-") {
      this.#i++;
    }
    if (!isDigit(this.#peek())) {
      throw new SfSyntax();
    }
    let digits = 0;
    let dot = -1;
    for (;;) {
      const c = this.#peek();
      if (isDigit(c)) {
        digits++;
      } else if (c === "." && dot < 0) {
        if (digits > 12) {
          throw new SfSyntax();
        }
        dot = digits;
      } else {
        break;
      }
      this.#i++;
      if (digits > 15) {
        throw new SfSyntax();
      }
    }
    if (dot >= 0) {
      const frac = digits - dot;
      if (frac < 1 || frac > 3) {
        throw new SfSyntax();
      }
      return OTHER;
    }
    return { kind: "integer", num: Number(this.#s.slice(start, this.#i)) };
  }

  // RFC 9651 section 4.2.5.
  #string(): SfItem {
    this.#i++;
    let out = "";
    while (!this.#done()) {
      const c = this.#s.charAt(this.#i++);
      if (c === "\\") {
        const e = this.#peek();
        if (e !== '"' && e !== "\\") {
          throw new SfSyntax();
        }
        out += e;
        this.#i++;
      } else if (c === '"') {
        return { kind: "string", str: out };
      } else if (c < " " || c > "~") {
        throw new SfSyntax();
      } else {
        out += c;
      }
    }
    throw new SfSyntax();
  }

  // RFC 9651 section 4.2.6: tchar, ":" and "/" after the first character.
  #token(): void {
    this.#i++;
    for (let c = this.#peek(); c === ":" || c === "/" || isTchar(c); c = this.#peek()) {
      this.#i++;
    }
  }

  // RFC 9651 section 4.2.7, checked for its alphabet only: the value is never decoded.
  #byteSequence(): void {
    this.#i++;
    while (!this.#done()) {
      const c = this.#s.charAt(this.#i++);
      if (c === ":") {
        return;
      }
      if (!isBase64(c)) {
        throw new SfSyntax();
      }
    }
    throw new SfSyntax();
  }

  // RFC 9651 section 4.2.8.
  #boolean(): void {
    this.#i++;
    const c = this.#peek();
    if (c !== "0" && c !== "1") {
      throw new SfSyntax();
    }
    this.#i++;
  }

  // RFC 9651 section 4.2.10.
  #displayString(): void {
    this.#i++;
    if (this.#peek() !== '"') {
      throw new SfSyntax();
    }
    this.#i++;
    const bytes: number[] = [];
    while (!this.#done()) {
      const c = this.#s.charAt(this.#i++);
      if (c < " " || c > "~") {
        throw new SfSyntax();
      }
      if (c === "%") {
        const hex = this.#s.slice(this.#i, this.#i + 2);
        if (hex.length !== 2 || !isLowerHex(hex.charAt(0)) || !isLowerHex(hex.charAt(1))) {
          throw new SfSyntax();
        }
        bytes.push(Number.parseInt(hex, 16));
        this.#i += 2;
      } else if (c === '"') {
        try {
          new TextDecoder("utf-8", { fatal: true }).decode(new Uint8Array(bytes));
        } catch {
          throw new SfSyntax();
        }
        return;
      } else {
        bytes.push(c.charCodeAt(0));
      }
    }
    throw new SfSyntax();
  }
}

// RFC 9651 section 4.2: the input must be ASCII.
function parseDictionary(value: string): SfMember[] | null {
  for (let i = 0; i < value.length; i++) {
    if (value.charCodeAt(i) > 0x7f) {
      return null;
    }
  }
  try {
    return new SfParser(value).dictionary();
  } catch (e) {
    if (e instanceof SfSyntax) {
      return null;
    }
    throw e;
  }
}
