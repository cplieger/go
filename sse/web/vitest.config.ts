import { existsSync } from "node:fs";
import { playwright } from "@vitest/browser-playwright";
import { chromium } from "playwright";
import { defineConfig } from "vitest/config";
import { fixtureOrigin } from "./src/test-helpers/fixture-port.js";

const browserBinary = chromium.executablePath();
const browserAvailable = existsSync(browserBinary);
if (!browserAvailable) {
  console.warn(
    `[vitest] browser project skipped: no Playwright Chromium at ${browserBinary}; run npx --no-install playwright install chromium`,
  );
}

// The engines the change feed's browser suites run in, which CI sets from package.json's
// cplieger.browsers; Chromium alone when it is unset.
const ENGINES = (process.env["CPLIEGER_BROWSERS"] ?? "chromium").split(",") as (
  "chromium" | "firefox" | "webkit"
)[];

// Chromium delivers animation frames at ~60Hz, so each frame a test awaits costs it ~16.7ms;
// the flag removes the cap.
const chromiumProvider = playwright({
  launchOptions: { channel: "chromium", args: ["--disable-frame-rate-limit"] },
});

function browserMode(
  instances: { browser: (typeof ENGINES)[number]; provider?: typeof chromiumProvider }[],
) {
  return {
    enabled: true,
    headless: true,
    provider: chromiumProvider,
    instances,
    viewport: { width: 1280, height: 720 },
    screenshotFailures: false,
  };
}

// `extends: true` is what makes a project inherit the root `test` block; without it
// every strictness option below is silently dropped for that project.
const projects = [
  {
    extends: true,
    test: {
      name: "node",
      environment: "node",
      include: ["src/**/*.node.test.ts"],
      sequence: { groupOrder: 0 },
    },
  },
  ...(browserAvailable
    ? [
        {
          extends: true,
          test: {
            name: "browser",
            include: ["src/**/*.test.ts"],
            exclude: ["src/**/*.node.test.ts", "src/feed/**", "node_modules/**"],
            sequence: { groupOrder: 1 },
            browser: browserMode([{ browser: "chromium" }]),
          },
        },
        {
          extends: true,
          test: {
            name: "engines",
            include: ["src/feed/**/*.test.ts"],
            exclude: ["src/**/*.node.test.ts", "node_modules/**"],
            sequence: { groupOrder: 2 },
            browser: browserMode(
              ENGINES.map((browser) =>
                browser === "chromium"
                  ? { browser }
                  : { browser, provider: playwright({ launchOptions: {} }) },
              ),
            ),
          },
        },
      ]
    : []),
];

// The fixture answers no CORS preflight, so the browser projects reach it through the dev
// server: a request under /__fixture is forwarded to the port the globalSetup binds it to.
const FIXTURE_PREFIX = "/__fixture";

export default defineConfig({
  server: {
    proxy: {
      [FIXTURE_PREFIX]: {
        target: fixtureOrigin(),
        changeOrigin: true,
        rewrite: (path) => path.slice(FIXTURE_PREFIX.length),
      },
    },
  },
  test: {
    projects,
    env: { SSE_FIXTURE: process.env["SSE_FIXTURE"] ?? "" },
    globalSetup: ["./src/test-helpers/fixture-setup.ts"],
    // The fixture-driven suites share one fixture whose control flags are global state, so
    // files run one at a time and the projects one after the other (groupOrder above).
    fileParallelism: false,
    setupFiles: ["./src/fc-strict-setup.ts"],
    // Stryker runs `inPlace`, so it mirrors every mutated file into
    // .stryker-tmp/backup-*; without this the whole suite is collected a second
    // time from that copy, where the repo-root fixtures no longer resolve.
    exclude: ["node_modules/**", "**/.stryker-tmp/**"],
    passWithNoTests: false,
    allowOnly: false,
    globals: false,
    expect: {
      requireAssertions: true,
    },
    clearMocks: true,
    mockReset: true,
    restoreMocks: true,
    unstubEnvs: true,
    unstubGlobals: true,
    bail: process.env["CI"] ? 1 : 0,
    testTimeout: 5000,
    hookTimeout: 10000,
    slowTestThreshold: 300,
    sequence: {
      shuffle: { files: false, tests: false },
      concurrent: false,
      hooks: "stack",
    },
    printConsoleTrace: true,
    coverage: {
      provider: "v8",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.test.ts", "src/**/*.d.ts", "src/**/*-setup.ts", "src/**/test-helpers/**"],
    },
  },
});
