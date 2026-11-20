/** A `List` row's answer, or the list members of an answer that embeds one. */
export interface ListAnswer<T> {
  /** The page's rows, in the list's order. */
  readonly items: readonly T[];
  /** The cursor of the next page, present exactly when rows remain after this one. */
  readonly next_cursor?: string;
  /** The cursor of the previous page, present exactly when `offset` is above 0. */
  readonly prev_cursor?: string;
  /** How many matching rows come before the first item. */
  readonly offset: number;
  /** How many rows match the query. */
  readonly total: number;
}

// The bound view revisions carry, so every total is a safe integer in every browser.
const MAX_TOTAL = 999_999_999_999_999;
const DEFAULT_MAX_PAGES = 1_000;

function count(v: unknown): v is number {
  return Number.isSafeInteger(v) && (v as number) >= 0 && (v as number) <= MAX_TOTAL;
}

// Present means a non-empty string: the server never sends an empty cursor.
function cursor(v: unknown, name: string): boolean {
  if (v === undefined) {
    return false;
  }
  if (typeof v !== "string" || v === "") {
    throw new TypeError(`list answer: ${name} is not a cursor`);
  }
  return true;
}

/**
 * Answers `a` when it holds the rules the server checks before it writes a list page: `offset` and
 * `total` integers from 0 to 999,999,999,999,999, the page inside `total`, `prev_cursor` present
 * exactly when `offset` is above 0 and `next_cursor` exactly when rows remain after the page.
 * Otherwise it throws a TypeError, which a generated decoder reports as a decode failure.
 */
export function checkListAnswer<A extends ListAnswer<unknown>>(a: A): A {
  const o = a as { readonly [k in keyof ListAnswer<unknown>]: unknown };
  if (!Array.isArray(o.items)) {
    throw new TypeError("list answer: items is not a list");
  }
  if (!count(o.total) || !count(o.offset)) {
    throw new TypeError(
      "list answer: offset or total is not an integer from 0 to 999,999,999,999,999",
    );
  }
  const end = o.offset + o.items.length;
  if (end > o.total) {
    throw new TypeError(`list answer: rows ${o.offset} to ${end} pass the total ${o.total}`);
  }
  if (cursor(o.prev_cursor, "prev_cursor") !== o.offset > 0) {
    throw new TypeError("list answer: prev_cursor must be present exactly when offset is above 0");
  }
  if (cursor(o.next_cursor, "next_cursor") !== end < o.total) {
    throw new TypeError(
      "list answer: next_cursor must be present exactly when rows follow the page",
    );
  }
  return a;
}

/**
 * Reads every page of a list: `page` with no cursor, then with each page's `next_cursor`. It yields
 * each page after checkListAnswer, and returns the first failure `page` answers, or undefined after
 * the last page. A page breaking the list rules rejects with a TypeError, and a list with pages
 * left after `maxPages` (1 to any integer, default 1,000) rejects with a RangeError.
 */
export function pages<T, F extends { readonly ok: false }>(
  page: (
    cursor: string | undefined,
    signal: AbortSignal,
  ) => Promise<{ readonly ok: true; readonly data: ListAnswer<T> } | F>,
  o: { readonly signal: AbortSignal; readonly maxPages?: number },
): AsyncGenerator<ListAnswer<T>, F | undefined> {
  const max = o.maxPages ?? DEFAULT_MAX_PAGES;
  if (!Number.isSafeInteger(max) || max < 1) {
    throw new RangeError(`pages: maxPages ${max} is not a positive integer`);
  }
  return (async function* () {
    let next: string | undefined;
    for (let n = 0; n < max; n++) {
      const r = await page(next, o.signal);
      if (!r.ok) {
        return r;
      }
      yield checkListAnswer(r.data);
      next = r.data.next_cursor;
      if (next === undefined) {
        return undefined;
      }
    }
    throw new RangeError(`pages: the list has more than ${max} pages`);
  })();
}
