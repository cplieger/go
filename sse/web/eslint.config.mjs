// ./eslint.config.base.mjs is a copy of cplieger/ci's canonical ruleset; this file adds the package's ignores.
import baseConfig from "./eslint.config.base.mjs";

export default [
  ...baseConfig,
  {
    ignores: ["**/reports/**", "**/.stryker-tmp/**", "**/coverage/**"],
  },
];
