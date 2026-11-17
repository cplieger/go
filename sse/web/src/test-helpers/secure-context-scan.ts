import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";

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
export function scanSecureContext(
  path: string,
  text: string,
  allowed: ReadonlySet<string>,
): Finding[] {
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
export function publishedSources(root: string): string[] {
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

export function readSource(root: string, path: string): string {
  return readFileSync(join(root, path), "utf8");
}
