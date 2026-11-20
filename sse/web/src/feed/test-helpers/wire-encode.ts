import { FEED_VERSION } from "../version.js";
import type { ChangedFrame, ListeningFrame, Position } from "../wire.js";

// The byte form feedwire's encoders write (EncodeChanged, EncodeListening, FormatPosition). The
// fixture suite holds this mirror to the Go output: every canonical case's bytes must equal it.

const byCodeUnit = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

export function encodeChanged(c: ChangedFrame): string {
  const kinds = c.kinds.map((k) => `"${k}"`).join(",");
  return `{"source":"${c.source}","epoch":"${c.epoch}","rev":${String(c.rev)},"kinds":[${kinds}]}`;
}

export function encodeListening(l: ListeningFrame, feed: number = FEED_VERSION): string {
  const wire = l.wire === null ? "" : `,"wire":"${l.wire}"`;
  const sources = l.sources.map((s) => {
    const floors = [...s.floors.keys()]
      .sort(byCodeUnit)
      .map((k) => `"${k}":${String(s.floors.get(k))}`)
      .join(",");
    return `{"source":"${s.source}","epoch":"${s.epoch}","floors":{${floors}}}`;
  });
  return `{"feed":${String(feed)}${wire},"sources":[${sources.join(",")}]}`;
}

export function formatPosition(ps: readonly Position[]): string {
  return ps.map((p) => `${p.source}=${String(p.rev)};epoch="${p.epoch}"`).join(", ");
}
