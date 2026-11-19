import { describe, expect, it } from "vitest";
import corpusFile from "../testdata/wirepatterns.json?raw";

// Go's httpwire test gives every case the same verdict through its regexp
// package; the generated decoder checks these patterns with new RegExp(p, "u").
interface Entry {
  pattern: string;
  cases: { input: string; match: boolean }[];
}

const corpus = JSON.parse(corpusFile) as Record<string, Entry>;

describe("the wire-pattern corpus under ECMA-262", () => {
  it("names the six fixed patterns", () => {
    expect(Object.keys(corpus).sort()).toEqual([
      "PatternHeader",
      "PatternJSONBool",
      "PatternJSONInt",
      "PatternJSONNumber",
      "PatternJSONString",
      "PatternJSONUint",
    ]);
  });

  it.each(Object.entries(corpus))("%s gives every case its verdict with the u flag", (_, entry) => {
    const re = new RegExp(entry.pattern, "u");
    const misses = entry.cases.filter((c) => re.test(c.input) !== c.match).map((c) => c.input);
    expect(misses).toEqual([]);
    expect(entry.cases.some((c) => c.match)).toBe(true);
    expect(entry.cases.some((c) => !c.match)).toBe(true);
  });
});
