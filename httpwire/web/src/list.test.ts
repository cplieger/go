import { describe, expect, it } from "vitest";
import { checkListAnswer, type ListAnswer, pages } from "./list.js";

// The bound view revisions carry, which every browser holds as a safe integer.
const MAX_TOTAL = 999_999_999_999_999;

interface Failure {
  readonly ok: false;
  readonly kind: "network";
}
type Answer<T> = { readonly ok: true; readonly data: ListAnswer<T> } | Failure;

const consistent: Record<string, ListAnswer<number>> = {
  first: { items: [1, 2], next_cursor: "n2", offset: 0, total: 5 },
  middle: { items: [3, 4], next_cursor: "n4", prev_cursor: "p3", offset: 2, total: 5 },
  last: { items: [5], prev_cursor: "p5", offset: 4, total: 5 },
  only: { items: [1, 2, 3], offset: 0, total: 3 },
  empty: { items: [], offset: 0, total: 0 },
  emptyPastTheEnd: { items: [], prev_cursor: "p", offset: 7, total: 7 },
  largest: { items: [1], prev_cursor: "p", offset: MAX_TOTAL - 1, total: MAX_TOTAL },
};

const broken: Record<string, unknown> = {
  "total 10^15": { items: [], prev_cursor: "p", offset: 1e15, total: 1e15 },
  "negative total": { items: [], offset: 0, total: -1 },
  "fractional total": { items: [1], next_cursor: "n", offset: 0, total: 2.5 },
  "NaN total": { items: [], offset: 0, total: Number.NaN },
  "infinite total": { items: [1], next_cursor: "n", offset: 0, total: Infinity },
  "total as text": { items: [], offset: 0, total: "0" },
  "negative offset with no prev_cursor": { items: [1, 2], next_cursor: "n", offset: -1, total: 5 },
  "negative offset": { items: [1], next_cursor: "n", prev_cursor: "p", offset: -1, total: 5 },
  "fractional offset": { items: [1], next_cursor: "n", prev_cursor: "p", offset: 1.5, total: 5 },
  "offset past total": { items: [], prev_cursor: "p", offset: 6, total: 5 },
  "page past total": { items: [1, 2, 3], prev_cursor: "p", offset: 3, total: 5 },
  "prev_cursor on the first page": {
    items: [1],
    next_cursor: "n",
    prev_cursor: "p",
    offset: 0,
    total: 5,
  },
  "no prev_cursor past the first page": { items: [2], next_cursor: "n", offset: 1, total: 5 },
  "no next_cursor before the end": { items: [1, 2], offset: 0, total: 5 },
  "next_cursor at the end": { items: [5], next_cursor: "n", prev_cursor: "p", offset: 4, total: 5 },
  "empty next_cursor": { items: [1], next_cursor: "", offset: 0, total: 2 },
  "empty prev_cursor": { items: [2], prev_cursor: "", offset: 1, total: 2 },
  "cursor as a number": { items: [1], next_cursor: 7, offset: 0, total: 2 },
  "items not a list": { items: { 0: 1 }, offset: 0, total: 1 },
};

describe("checkListAnswer", () => {
  it("answers a page that keeps the four rules and the total bound", () => {
    for (const [name, page] of Object.entries(consistent)) {
      expect(checkListAnswer(page), name).toBe(page);
    }
  });

  it("refuses a page that breaks a rule or the total bound with a TypeError", () => {
    for (const [name, page] of Object.entries(broken)) {
      expect(() => checkListAnswer(page as ListAnswer<unknown>), name).toThrow(TypeError);
    }
  });
});

// A list of `total` numbers paged by two through cursors naming the next offset.
function source(total: number, calls: (string | undefined)[], fail?: { at: number; f: Failure }) {
  return (cursor: string | undefined, signal: AbortSignal): Promise<Answer<number>> => {
    calls.push(cursor);
    expect(signal.aborted).toBe(false);
    if (fail !== undefined && calls.length === fail.at) {
      return Promise.resolve(fail.f);
    }
    const offset = cursor === undefined ? 0 : Number(cursor.slice(1));
    const items = Array.from({ length: Math.min(2, total - offset) }, (_, i) => offset + i + 1);
    const end = offset + items.length;
    return Promise.resolve({
      ok: true,
      data: {
        items,
        offset,
        total,
        ...(end < total ? { next_cursor: `n${end}` } : {}),
        ...(offset > 0 ? { prev_cursor: `p${offset}` } : {}),
      },
    });
  };
}

async function drain<T, R>(g: AsyncGenerator<T, R>): Promise<{ yielded: T[]; returned: R }> {
  const yielded: T[] = [];
  for (;;) {
    const r = await g.next();
    if (r.done === true) {
      return { yielded, returned: r.value };
    }
    yielded.push(r.value);
  }
}

describe("pages", () => {
  it("yields every page in order, following each next_cursor, then returns undefined", async () => {
    const calls: (string | undefined)[] = [];
    const signal = new AbortController().signal;
    const seen: AbortSignal[] = [];
    const page = source(5, calls);
    const { yielded, returned } = await drain(
      pages((c, s) => (seen.push(s), page(c, s)), { signal }),
    );
    expect(yielded.map((p) => p.items)).toEqual([[1, 2], [3, 4], [5]]);
    expect(calls).toEqual([undefined, "n2", "n4"]);
    expect(seen.every((s) => s === signal)).toBe(true);
    expect(returned).toBeUndefined();
  });

  it("yields an empty list's one page", async () => {
    const calls: (string | undefined)[] = [];
    const { yielded, returned } = await drain(
      pages(source(0, calls), { signal: new AbortController().signal }),
    );
    expect(yielded).toEqual([{ items: [], offset: 0, total: 0 }]);
    expect(calls).toEqual([undefined]);
    expect(returned).toBeUndefined();
  });

  it("returns the first failure and asks for no page after it", async () => {
    const calls: (string | undefined)[] = [];
    const f: Failure = { ok: false, kind: "network" };
    const { yielded, returned } = await drain(
      pages(source(9, calls, { at: 2, f }), { signal: new AbortController().signal }),
    );
    expect(yielded.map((p) => p.offset)).toEqual([0]);
    expect(returned).toBe(f);
    expect(calls).toEqual([undefined, "n2"]);
  });

  it("rejects with a TypeError at a page that breaks a rule", async () => {
    const answers: Answer<number>[] = [
      { ok: true, data: { items: [1], next_cursor: "n1", offset: 0, total: 3 } },
      { ok: true, data: { items: [2], next_cursor: "n2", offset: 1, total: 3 } },
    ];
    const g = pages(() => Promise.resolve(answers.shift() as Answer<number>), {
      signal: new AbortController().signal,
    });
    expect((await g.next()).value).toMatchObject({ offset: 0 });
    await expect(g.next()).rejects.toThrow(TypeError);
  });

  it("asks for nothing more once the caller stops", async () => {
    const calls: (string | undefined)[] = [];
    for await (const p of pages(source(9, calls), { signal: new AbortController().signal })) {
      expect(p.offset).toBe(0);
      break;
    }
    expect(calls).toEqual([undefined]);
  });

  it("reads at most maxPages pages, failing with a RangeError when more remain", async () => {
    const signal = new AbortController().signal;
    const exact: (string | undefined)[] = [];
    expect(
      (await drain(pages(source(6, exact), { signal, maxPages: 3 }))).returned,
    ).toBeUndefined();
    expect(exact).toHaveLength(3);

    const over: (string | undefined)[] = [];
    await expect(drain(pages(source(7, over), { signal, maxPages: 3 }))).rejects.toThrow(
      RangeError,
    );
    expect(over).toHaveLength(3);
  });

  it("reads at most 1,000 pages by default", async () => {
    const calls: (string | undefined)[] = [];
    const signal = new AbortController().signal;
    await expect(drain(pages(source(2_002, calls), { signal }))).rejects.toThrow(RangeError);
    expect(calls).toHaveLength(1_000);

    const all: (string | undefined)[] = [];
    expect((await drain(pages(source(2_000, all), { signal }))).yielded).toHaveLength(1_000);
  });

  it("refuses a maxPages that is not a positive integer before any request", () => {
    const calls: (string | undefined)[] = [];
    const signal = new AbortController().signal;
    for (const maxPages of [0, -1, 1.5, Number.NaN, Infinity]) {
      expect(() => pages(source(5, calls), { signal, maxPages }), String(maxPages)).toThrow(
        RangeError,
      );
    }
    expect(calls).toEqual([]);
  });
});
