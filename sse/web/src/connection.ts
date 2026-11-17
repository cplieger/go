import type { StreamState } from "./state.js";

/** Whether the stream is open, and since when it is not: the facts a connection indicator shows. */
export interface Connection {
  readonly state: "live" | "lost";
  /** When the stream left `open`; null while it is open and before it was ever open. */
  readonly since: number | null;
}

/**
 * Reads a stream state as a connection: `live` while it is `open`, `lost` in every other kind,
 * the first connect included. `lostAt` is the caller's: the time of the lifecycle `state` record
 * whose `from` is `open`, or null before the stream was ever open. It applies no delay; a grace
 * before a drop shows, and an offline delay, are the indicator's.
 */
export function connectionOf(state: StreamState, lostAt: number | null): Connection {
  return state.kind === "open" ? { state: "live", since: null } : { state: "lost", since: lostAt };
}
