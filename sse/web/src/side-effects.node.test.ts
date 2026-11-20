import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

import { build } from "esbuild";
import { rolldown } from "rolldown";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const PACKAGE_ROOT = resolve(import.meta.dirname, "..");

// An app imports these for their exports, so an effect-only import of one bundles to nothing.
const PURE = ["", "/feed"];
// The SharedWorker script an app lists as a bundle entry: its import installs `onconnect`.
const EFFECTFUL = ["/feed/worker"];

type Bundle = (entry: string) => Promise<string>;

const bundlers: Record<string, Bundle> = {
  esbuild: async (entry) => {
    const out = await build({
      entryPoints: [entry],
      bundle: true,
      write: false,
      format: "esm",
      platform: "browser",
      logLevel: "silent",
    });
    return out.outputFiles.map((f) => f.text).join("");
  },
  rolldown: async (entry) => {
    const b = await rolldown({ input: entry, logLevel: "silent", platform: "browser" });
    try {
      const { output } = await b.generate({ format: "esm" });
      return output.map((c) => ("code" in c ? c.code : "")).join("");
    } finally {
      await b.close();
    }
  },
};

let dir = "";

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "sse-side-effects-"));
  await mkdir(join(dir, "node_modules", "@cplieger"), { recursive: true });
  await symlink(PACKAGE_ROOT, join(dir, "node_modules", "@cplieger", "sse"), "dir");
});

afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

/** Bundles an entry that imports `@cplieger/sse<subpath>` only for its effect. */
async function bundleEffectOnly(bundle: Bundle, subpath: string): Promise<string> {
  const entry = join(dir, `entry${subpath.replaceAll("/", "-")}.js`);
  await writeFile(entry, `import "@cplieger/sse${subpath}";\n`);
  const code = await bundle(entry);
  return code.replaceAll(/\/\/.*$/gm, "").trim();
}

it("covers every exported subpath", async () => {
  const pkg = JSON.parse(await readFile(join(PACKAGE_ROOT, "package.json"), "utf8")) as {
    exports: Record<string, unknown>;
  };
  const exported = Object.keys(pkg.exports).map((key) => key.slice(1));
  expect(exported.toSorted()).toEqual([...PURE, ...EFFECTFUL].toSorted());
});

describe.each(Object.entries(bundlers))("sideEffects under %s", (_name, bundle) => {
  it.each(PURE)("an effect-only import of @cplieger/sse%s bundles to no code", async (subpath) => {
    expect(await bundleEffectOnly(bundle, subpath)).toBe("");
  });

  it.each(EFFECTFUL)(
    "an effect-only import of @cplieger/sse%s keeps the code installing onconnect",
    async (subpath) => {
      expect(await bundleEffectOnly(bundle, subpath)).toMatch(/\.onconnect\s*=/);
    },
  );
});
