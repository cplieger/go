import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

/** One use of a `[SecureContext]` member in a source file. */
interface Finding {
  readonly path: string;
  readonly line: number;
  readonly member: string;
}

// randomUUID has getRandomValues as its non-secure arm, so no file may call it; the other three
// need an arm of their own, which only an allow-listed file has.
const NEVER = new Set(["randomUUID"]);
const LISTED = new Set(["clipboard", "subtle", "locks"]);

// Each way a member is reached by name: `a.m`, `a?.m`, `a["m"]`, `{ m } = a`, and a bare string
// (`"m" in a`, `Reflect.get(a, "m")`). A comment never counts.
function memberName(node: ts.Node): string | null {
  if (ts.isPropertyAccessExpression(node)) {
    return node.name.text;
  }
  if (ts.isBindingElement(node) && ts.isObjectBindingPattern(node.parent)) {
    const key = node.propertyName ?? node.name;
    return ts.isIdentifier(key) || ts.isStringLiteral(key) ? key.text : null;
  }
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    return node.text;
  }
  return null;
}

/** The `[SecureContext]` members `text` reaches, outside `allowed` for the listed ones. */
function scanSecureContext(path: string, text: string, allowed: ReadonlySet<string>): Finding[] {
  const file = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
  const out: Finding[] = [];
  const visit = (node: ts.Node): void => {
    const name = memberName(node);
    if (name !== null && (NEVER.has(name) || (LISTED.has(name) && !allowed.has(path)))) {
      const { line } = file.getLineAndCharacterOfPosition(node.getStart(file));
      out.push({ path, line: line + 1, member: name });
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return out;
}

/**
 * The package's published sources under `root/src`, as paths relative to `root`: package.json's
 * `files` ships `src/` without its tests, setup files and test helpers.
 */
function publishedSources(root: string): string[] {
  const dir = join(root, "src");
  return readdirSync(dir, { recursive: true, encoding: "utf8" })
    .map((p) => join(dir, p))
    .filter(
      (p) =>
        p.endsWith(".ts") &&
        !p.endsWith(".test.ts") &&
        !p.endsWith("-setup.ts") &&
        !p.split(/[\\/]/).includes("test-helpers"),
    )
    .map((p) => relative(root, p).split("\\").join("/"))
    .sort();
}

function readSource(root: string, path: string): string {
  return readFileSync(join(root, path), "utf8");
}

// A page served over plain HTTP from a LAN address is not a secure context, so every
// [SecureContext] member is undefined there. This package calls none of them, so the files allowed
// to reach clipboard, subtle or locks behind a non-secure arm are none.
const ALLOWED: ReadonlySet<string> = new Set();

const ROOT = join(import.meta.dirname, "..");

const members = (text: string, allowed: ReadonlySet<string> = ALLOWED): string[] =>
  scanSecureContext("src/x.ts", text, allowed).map((f) => f.member);

describe("secure-context release check", () => {
  it("refuses crypto.randomUUID in a non-test source, allow-listed or not", () => {
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
    expect(files).toEqual(
      expect.arrayContaining(["src/headers.ts", "src/list.ts", "src/testing/index.ts"]),
    );
    expect(files.filter((f) => f.endsWith(".test.ts") || f.includes("test-helpers/"))).toEqual([]);
    const found = files.flatMap((f) => scanSecureContext(f, readSource(ROOT, f), ALLOWED));
    expect(found).toEqual([]);
  });
});
