import type { ErrorCodeSpec, GeneratedEndpoints } from "./fake.js";

/** The request a client sends for a case: its method, path and the transport's own headers. */
export interface TransportRequest {
  /** The request's method. */
  readonly method: string;
  /** The request's path, its parameters filled in. */
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

/**
 * One case: the row and arguments the client's generated function is called with, the request it
 * must send, the server's answer and the result the client must give.
 */
export interface TransportCase {
  /** The case's name, unique in the fixture. */
  readonly name: string;
  /** The row whose generated function the case calls. */
  readonly row: string;
  /** The path parameters passed to it. */
  readonly params: Readonly<Record<string, string>>;
  /** The request body passed to it. */
  readonly body?: unknown;
  /** The tag a read answered, passed to the row's generated function for `If-Match`. */
  readonly tag?: string;
  /** The raw key passed to a `Keyed` row's generated function. */
  readonly key?: string;
  /** Absent when the client must send nothing. */
  readonly request?: TransportRequest;
  /** What the server answers; absent when the client must send nothing. */
  readonly answer?: TransportAnswer;
  /** The result the client must give. */
  readonly want: TransportWant;
}

/** The rows, catalog and wire version a transport's cases run against, and the cases. */
export interface TransportFixture {
  /** The `Wire-Version` writes send and answers carry. */
  readonly wireVersion: string;
  /** Each code's catalog status and remediation. */
  readonly errorCodes: Readonly<Record<string, ErrorCodeSpec>>;
  /** The rows, as a generated client's `ENDPOINTS`. */
  readonly endpoints: GeneratedEndpoints;
  /** The cases, each named once. */
  readonly cases: readonly TransportCase[];
}
