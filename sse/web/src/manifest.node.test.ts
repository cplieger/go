import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

interface Manifest {
  readonly version: string;
  readonly exports: Record<string, string>;
  readonly dependencies?: Record<string, string>;
}

const root = join(import.meta.dirname, "..");
const readManifest = (name: string): Manifest =>
  JSON.parse(readFileSync(join(root, name), "utf8")) as Manifest;

describe("package manifests", () => {
  it("declares no runtime dependencies", () => {
    expect(readManifest("package.json").dependencies).toBeUndefined();
  });

  it("exports ., ./feed and the feed's worker entry", () => {
    expect(readManifest("package.json").exports).toEqual({
      ".": "./src/index.ts",
      "./feed": "./src/feed/index.ts",
      "./feed/worker": "./src/feed/worker.ts",
    });
    expect(readManifest("jsr.json").exports).toEqual(readManifest("package.json").exports);
  });

  it("package.json and jsr.json agree on the placeholder version", () => {
    expect(readManifest("package.json").version).toBe(readManifest("jsr.json").version);
  });
});
