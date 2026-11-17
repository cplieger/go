import { playwright } from "@vitest/browser-playwright";
import { defineConfig } from "vitest/config";

// CI sets the engines from package.json's cplieger.browsers; Chromium alone when it is unset.
const ENGINES = (process.env["CPLIEGER_BROWSERS"] ?? "chromium").split(",") as (
  "chromium" | "firefox" | "webkit"
)[];

// Chromium caps animation frames at ~60Hz, and throttles them further late in a long
// session; the flag removes the cap.
const chromiumProvider = playwright({
  launchOptions: { channel: "chromium", args: ["--disable-frame-rate-limit"] },
});

// `extends: true` is what makes a project inherit the root `test` block; without it
// every strictness option below is silently dropped for that project.
const projects = [
  {
    extends: true,
    test: {
      name: "node",
      environment: "node",
      include: ["src/**/*.node.test.ts"],
    },
  },
  {
    extends: true,
    test: {
      name: "engines",
      include: ["src/**/*.test.ts"],
      exclude: ["src/**/*.node.test.ts", "node_modules/**", "**/.stryker-tmp/**"],
      browser: {
        enabled: true,
        headless: true,
        provider: chromiumProvider,
        instances: ENGINES.map((browser) =>
          browser === "chromium"
            ? { browser }
            : { browser, provider: playwright({ launchOptions: {} }) },
        ),
        screenshotFailures: false,
      },
    },
  },
];

export default defineConfig({
  test: {
    projects,
    isolate: true,
    // Stryker runs `inPlace` and parks the originals under .stryker-tmp/backup-*;
    // without this the suite is collected a second time from that copy.
    exclude: ["node_modules/**", "**/.stryker-tmp/**"],
    passWithNoTests: false,
    allowOnly: false,
    globals: false,
    expect: { requireAssertions: true },
    clearMocks: true,
    mockReset: true,
    restoreMocks: true,
    unstubEnvs: true,
    unstubGlobals: true,
    bail: process.env["CI"] ? 1 : 0,
    testTimeout: 5000,
    hookTimeout: 10000,
    sequence: { shuffle: { files: false, tests: false }, concurrent: false, hooks: "stack" },
    printConsoleTrace: true,
    coverage: {
      provider: "v8",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.test.ts", "src/**/*.d.ts", "src/**/*-setup.ts", "src/**/test-helpers/**"],
    },
  },
});
