import type { Frame } from "../stream.js";
import { FEED_VERSION } from "./version.js";
import { MAX_FEED_FRAME_BYTES, parseChanged, parseListening } from "./wire.js";

interface Held {
  readonly epoch: string;
  readonly floors: ReadonlyMap<string, number>;
}

// Another format holds only its frozen members; a lost frame holds no source, since the tab
// reads it as lost track whatever it carries.
interface Retained {
  readonly feed: number;
  readonly wire: string | null;
  readonly sources: ReadonlyMap<string, Held>;
  readonly lost: boolean;
}

const MAX_KINDS = 64;
const NONE: ReadonlyMap<string, Held> = new Map();

const byCodeUnit = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

// The retained frame is the only state, so it is read back on every fold. A frame that does not
// parse is one this function marked lost, or one it never wrote: a tab reads both as lost track.
function decode(f: Frame | null): Retained | null {
  if (f === null) {
    return null;
  }
  const p = parseListening(f.data);
  switch (p.kind) {
    case "ok":
      return {
        feed: FEED_VERSION,
        wire: p.listening.wire,
        sources: new Map(p.listening.sources.map((s) => [s.source, s])),
        lost: false,
      };
    case "other_format":
      return { feed: p.frozen.feed, wire: p.frozen.wire, sources: NONE, lost: false };
    case "invalid":
      return { feed: FEED_VERSION, wire: p.frozen?.wire ?? null, sources: NONE, lost: true };
  }
}

const lostFrame = (wire: string | null): Frame =>
  encode({ feed: FEED_VERSION, wire, sources: NONE, lost: true });

// feedwire's EncodeListening byte form; every name was validated by the parse it came from, so
// none needs escaping. `lost` is a member no format declares, so the tab's strict parse refuses it.
function encode(r: Retained): Frame {
  const wire = r.wire === null ? "" : `,"wire":"${r.wire}"`;
  let data: string;
  if (r.feed === FEED_VERSION) {
    const sources = [...r.sources].map(([source, h]) => {
      const floors = [...h.floors.keys()]
        .sort(byCodeUnit)
        .map((k) => `"${k}":${String(h.floors.get(k))}`)
        .join(",");
      return `{"source":"${source}","epoch":"${h.epoch}","floors":{${floors}}}`;
    });
    const lost = r.lost ? ',"lost":true' : "";
    data = `{"feed":${String(r.feed)}${wire},"sources":[${sources.join(",")}]${lost}}`;
  } else {
    data = `{"feed":${String(r.feed)}${wire}}`;
  }
  return { type: "listening", data, id: null };
}

// A fold the tab's parse would refuse is a server bug, so it is lost track like a bad frame. The
// data is ASCII, so its length is its UTF-8 byte length.
function bounded(r: Retained): Frame {
  let kinds = 0;
  for (const h of r.sources.values()) {
    kinds += h.floors.size;
  }
  const f = encode(r);
  return kinds > MAX_KINDS || f.data.length > MAX_FEED_FRAME_BYTES ? lostFrame(r.wire) : f;
}

function foldListening(r: Retained | null, retained: Frame | null, data: string): Frame | null {
  const p = parseListening(data);
  if (p.kind === "other_format") {
    const wire = p.frozen.wire ?? r?.wire ?? null;
    return encode({ feed: p.frozen.feed, wire, sources: NONE, lost: false });
  }
  if (p.kind === "invalid") {
    // Under another format a frame whose frozen members do not read is dropped, as the tab drops it.
    if (p.frozen === null && r !== null && r.feed !== FEED_VERSION) {
      return retained;
    }
    return lostFrame(p.frozen?.wire ?? r?.wire ?? null);
  }
  const wire = p.listening.wire ?? r?.wire ?? null;
  if (r?.lost === true) {
    return lostFrame(wire);
  }
  // Another format's retained frame holds no source, so this frame replaces it.
  const sources = new Map(r?.sources ?? NONE);
  for (const s of p.listening.sources) {
    const prev = sources.get(s.source);
    if (prev?.epoch !== s.epoch) {
      sources.set(s.source, s);
      continue;
    }
    const floors = new Map(prev.floors);
    for (const [kind, floor] of s.floors) {
      floors.set(kind, Math.max(floor, floors.get(kind) ?? 0));
    }
    sources.set(s.source, { epoch: s.epoch, floors });
  }
  return bounded({ feed: FEED_VERSION, wire, sources, lost: false });
}

function foldChanged(r: Retained | null, retained: Frame | null, data: string): Frame | null {
  if (r !== null && r.feed !== FEED_VERSION) {
    return retained;
  }
  const p = parseChanged(data);
  if (!p.ok) {
    return lostFrame(r?.wire ?? null);
  }
  const { source, epoch, rev, kinds } = p.changed;
  const prev = r?.sources.get(source);
  if (r === null || prev?.epoch !== epoch) {
    return retained;
  }
  if (kinds.every((k) => (prev.floors.get(k) ?? 0) >= rev)) {
    return retained;
  }
  const floors = new Map(prev.floors);
  for (const k of kinds) {
    floors.set(k, Math.max(rev, floors.get(k) ?? 0));
  }
  return bounded({ ...r, sources: new Map(r.sources).set(source, { epoch, floors }) });
}

/**
 * The worker host's `retain` for the change feed: folds `listening` and `changed` frames into one
 * `listening` frame that an attaching tab applies as its connection's. Per source a new epoch
 * replaces the entry, else each kind keeps its higher floor; `wire` is the newest one seen. Another
 * feed format is kept as its frozen members only. A feed frame that does not parse marks it lost
 * (the tab's lost track) until the host clears it. Undeclared names are kept for the tab to drop.
 */
export function retainListening(retained: Frame | null, frame: Frame): Frame | null {
  switch (frame.type) {
    case "listening":
      return foldListening(decode(retained), retained, frame.data);
    case "changed":
      return foldChanged(decode(retained), retained, frame.data);
    default:
      return retained;
  }
}
