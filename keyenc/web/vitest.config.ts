import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    // Stryker runs in place and parks the originals under .stryker-tmp/backup-*;
    // vitest 5's default include would run those copies too.
    include: ["src/**/*.test.ts"],
  },
});
