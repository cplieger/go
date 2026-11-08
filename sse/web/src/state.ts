import type { ClientState } from "./reducer.js";

/**
 * A stream's connection state as an application reads it: the kind, the
 * generation, and the readings each kind acts on, including when a backoff
 * ends. It leaves out the timers the stream keeps for itself (when it opened,
 * its last byte, when it went hidden), so those can change without a breaking
 * release.
 */
export type StreamState =
  | { readonly kind: "stopped"; readonly generation: number }
  | {
      readonly kind: "connecting";
      readonly generation: number;
      readonly attempt: number;
      readonly visible: boolean;
      readonly online: boolean;
    }
  | {
      readonly kind: "open";
      readonly generation: number;
      readonly attempt: number;
      /** The hello's keepalive_ms. */
      readonly keepaliveMs: number;
      readonly visible: boolean;
      readonly online: boolean;
    }
  | {
      readonly kind: "backoff";
      readonly generation: number;
      readonly attempt: number;
      /** When the next attempt starts, in milliseconds on the stream's clock. */
      readonly until: number;
      readonly visible: boolean;
      readonly online: boolean;
    }
  | {
      readonly kind: "offline";
      readonly generation: number;
      readonly attempt: number;
      readonly visible: boolean;
    }
  | {
      readonly kind: "hidden_closed";
      readonly generation: number;
      readonly attempt: number;
      readonly online: boolean;
    };

/** The kinds a {@link StreamState} can take, as lifecycle `state` records name them. */
export type StreamStateKind = StreamState["kind"];

/** The application's view of the reducer's state: the same kind with its timers left out. */
export function toStreamState(s: ClientState): StreamState {
  switch (s.kind) {
    case "stopped":
      return { kind: s.kind, generation: s.generation };
    case "connecting":
      return {
        kind: s.kind,
        generation: s.generation,
        attempt: s.attempt,
        visible: s.visible,
        online: s.online,
      };
    case "backoff":
      return {
        kind: s.kind,
        generation: s.generation,
        attempt: s.attempt,
        until: s.until,
        visible: s.visible,
        online: s.online,
      };
    case "open":
      return {
        kind: s.kind,
        generation: s.generation,
        attempt: s.attempt,
        keepaliveMs: s.keepaliveMs,
        visible: s.visible,
        online: s.online,
      };
    case "offline":
      return { kind: s.kind, generation: s.generation, attempt: s.attempt, visible: s.visible };
    case "hidden_closed":
      return { kind: s.kind, generation: s.generation, attempt: s.attempt, online: s.online };
  }
}
