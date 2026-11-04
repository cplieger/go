// ./eslint.config.base.mjs is a copy of cplieger/ci's canonical ruleset; this file adds the package's own delta.
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
