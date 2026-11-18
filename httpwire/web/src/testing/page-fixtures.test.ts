import { describe, expect, it } from "vitest";
import { PAGE_FIXTURES } from "./index.js";

const cases = PAGE_FIXTURES.flatMap((f) => f.cases.map((c) => ({ table: f.name, ...c })));

describe("PAGE_FIXTURES", () => {
  it("holds the fixed tables", () => {
    expect(PAGE_FIXTURES.map((f) => f.name)).toEqual(["catchall", "library", "precedence"]);
    for (const f of PAGE_FIXTURES) {
      expect(f.cases.length).toBeGreaterThan(0);
      expect(Object.keys(f.pages).length).toBeGreaterThan(0);
    }
  });

  // The origin is prepended rather than passed as a base, since new URL("//library", base)
  // reads "library" as a host.
  it.each(cases)(
    "$table $path is serverOnly exactly when this engine's URL parser rewrites it",
    (c) => {
      const u = new URL(`https://x.invalid${c.path}`);
      expect(c.serverOnly === true).toBe(u.pathname + u.search !== c.path);
    },
  );

  it.each(cases)("$table $path answers a page, a redirect or no page", (c) => {
    expect(c.params !== undefined).toBe(c.name !== undefined);
    expect(c.name !== undefined && c.redirect !== undefined).toBe(false);
  });

  it("names only pages of its table", () => {
    for (const f of PAGE_FIXTURES) {
      const unknown = f.cases.filter((c) => c.name !== undefined && !(c.name in f.pages));
      expect(unknown).toEqual([]);
    }
  });

  it("never answers a page at or under the API prefix", () => {
    for (const f of PAGE_FIXTURES) {
      const under = f.cases.filter(
        (c) =>
          (c.path === f.apiPrefix || c.path.startsWith(`${f.apiPrefix}/`)) && c.name !== undefined,
      );
      expect(under).toEqual([]);
    }
  });
});
