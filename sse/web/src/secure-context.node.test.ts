import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  publishedSources,
  readSource,
  scanSecureContext,
} from "./test-helpers/secure-context-scan.js";

// A page served over plain HTTP from a LAN address is not a secure context, so every
// [SecureContext] member is undefined there. This package calls none of them, so the files allowed
// to reach clipboard, subtle or locks behind a non-secure arm are none.
const ALLOWED: ReadonlySet<string> = new Set();

const ROOT = join(import.meta.dirname, "..");

const members = (text: string, allowed: ReadonlySet<string> = ALLOWED): string[] =>
  scanSecureContext("src/x.ts", text, allowed).map((f) => f.member);

describe("secure-context release check", () => {
  it("release check refuses crypto.randomUUID in a non-test source", () => {
    expect(members("export const id = crypto.randomUUID();")).toEqual(["randomUUID"]);
    expect(members("const id = globalThis.crypto?.randomUUID();")).toEqual(["randomUUID"]);
    expect(members('const f = crypto["randomUUID"];')).toEqual(["randomUUID"]);
    expect(members("const { randomUUID } = crypto;")).toEqual(["randomUUID"]);
    expect(members('const ok = "randomUUID" in crypto;')).toEqual(["randomUUID"]);
    expect(members("const id = crypto.randomUUID();", new Set(["src/x.ts"]))).toEqual([
      "randomUUID",
    ]);
  });

  it("refuses navigator.clipboard, crypto.subtle and navigator.locks outside the allow-list", () => {
    const text = [
      "await navigator.clipboard.writeText(s);",
      "const { subtle } = crypto;",
      'await navigator["locks"].request("x", f);',
    ].join("\n");
    expect(scanSecureContext("src/x.ts", text, ALLOWED)).toEqual([
      { path: "src/x.ts", line: 1, member: "clipboard" },
      { path: "src/x.ts", line: 2, member: "subtle" },
      { path: "src/x.ts", line: 3, member: "locks" },
    ]);
    expect(scanSecureContext("src/x.ts", text, new Set(["src/x.ts"]))).toEqual([]);
  });

  it("does not count a member named only in a comment", () => {
    expect(members("// never crypto.randomUUID or navigator.clipboard\nconst a = 1;")).toEqual([]);
  });

  it("the package sources pass", () => {
    const files = publishedSources(ROOT);
    expect(files).toEqual(expect.arrayContaining(["src/tab.ts", "src/feed/retain.ts"]));
    expect(files.filter((f) => f.endsWith(".test.ts") || f.includes("test-helpers/"))).toEqual([]);
    const found = files.flatMap((f) => scanSecureContext(f, readSource(ROOT, f), ALLOWED));
    expect(found).toEqual([]);
  });
});
