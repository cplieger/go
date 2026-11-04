// Package-local ESLint config. The shared, org-authored ruleset is
// ./eslint.config.base.mjs; this file imports it and adds the one repo-specific
// delta below. Do not re-inline the ruleset here — this file WAS a full copy
// until 2026-09-16, which meant every canonical improvement bypassed the linter
// that actually runs, and knip reported the base as an unused file.
//
// The base is a HAND-MAINTAINED copy of cplieger/ci's canonical config, not a
// synced one: sync.yaml writes to <repo>/eslint.config.base.mjs, the ROOT, which
// nothing reads (the package-dir dest was tried in ci #371 and reverted by #372).
// So a canonical improvement lands in the unread root file and does NOT reach
// this one — when the root copy changes, copy it here too. Nothing detects drift.
import baseConfig from "./eslint.config.base.mjs";

export default [
  ...baseConfig,

  // The SHA-256 digest: fixed-size typed-array hot loops under
  // noUncheckedIndexedAccess. Every index is provably in range (the arrays are
  // allocated at their exact loop bounds), so the assertions carry no
  // information and the alternative is ~40 inline disables in one file.
  {
    files: ["src/sha256.ts"],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
    },
  },
];
