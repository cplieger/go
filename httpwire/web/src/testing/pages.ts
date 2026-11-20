/** One path and the server's answer for it: a page with its params, a redirect, or neither (no page). */
export interface PageCase {
  /** The request path. */
  readonly path: string;
  /** The page served for it. */
  readonly name?: string;
  /** The path parameters the page is served with. */
  readonly params?: Readonly<Record<string, string>>;
  /** Where the server redirects the path instead. */
  readonly redirect?: string;
  /** A dot segment, which a browser's URL parser removes before a router reads the path. */
  readonly serverOnly?: true;
}

/** A fixed page table, keyed by page name, and httpserver.PageMatcher's answers over it. */
export interface PageFixture {
  /** The fixture's name. */
  readonly name: string;
  /** Each page's path pattern, keyed by page name. */
  readonly pages: Readonly<Record<string, string>>;
  /** The API's prefix, which no page answers under. */
  readonly apiPrefix: string;
  /** Each path and the answer for it. */
  readonly cases: readonly PageCase[];
}
