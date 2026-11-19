/** One path and the server's answer for it: a page with its params, a redirect, or neither (no page). */
export interface PageCase {
  readonly path: string;
  readonly name?: string;
  readonly params?: Readonly<Record<string, string>>;
  readonly redirect?: string;
  /** A dot segment, which a browser's URL parser removes before a router reads the path. */
  readonly serverOnly?: true;
}

/** A fixed page table, keyed by page name, and httpserver.PageMatcher's answers over it. */
export interface PageFixture {
  readonly name: string;
  readonly pages: Readonly<Record<string, string>>;
  readonly apiPrefix: string;
  readonly cases: readonly PageCase[];
}
