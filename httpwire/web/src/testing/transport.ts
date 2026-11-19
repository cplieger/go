import type { ErrorCodeSpec, GeneratedEndpoints } from "./fake.js";

/** The request a client sends for a case: its method, path and the transport's own headers. */
export interface TransportRequest {
  readonly method: string;
  readonly path: string;
  /**
   * `Content-Type` (`application/json` with a body), `Wire-Version`, `If-Match` and
   * `Idempotency-Key` as sent; a header not listed is not sent.
   */
  readonly headers: Readonly<Record<string, string>>;
}

/**
 * What the server answers: `answer` and `refuse` go through the decoder-checked fake, and `raw`
 * is an answer that fake would refuse to give (an undeclared status, a body that is no envelope,
 * a code outside the row's union), written as is.
 */
export type TransportAnswer =
  | { readonly kind: "answer"; readonly status: number; readonly data?: unknown }
  | { readonly kind: "refuse"; readonly code: string }
  | {
      readonly kind: "raw";
      readonly status: number;
      readonly headers: Readonly<Record<string, string>>;
      readonly body: string;
    };

/** The result the client gives, in the transport's one failure set. */
export type TransportWant =
  | { readonly ok: true; readonly status: number; readonly data?: unknown }
  | { readonly ok: false; readonly failure: "http"; readonly status: number; readonly code: string }
  | { readonly ok: false; readonly failure: "invalid" | "decode"; readonly status: number };

export interface TransportCase {
  readonly name: string;
  readonly row: string;
  readonly params: Readonly<Record<string, string>>;
  readonly body?: unknown;
  /** The tag a read answered, passed to the row's generated function for `If-Match`. */
  readonly tag?: string;
  /** The raw key passed to a `Keyed` row's generated function. */
  readonly key?: string;
  /** Absent when the client must send nothing. */
  readonly request?: TransportRequest;
  readonly answer?: TransportAnswer;
  readonly want: TransportWant;
}

export interface TransportFixture {
  readonly wireVersion: string;
  readonly errorCodes: Readonly<Record<string, ErrorCodeSpec>>;
  readonly endpoints: GeneratedEndpoints;
  readonly cases: readonly TransportCase[];
}
