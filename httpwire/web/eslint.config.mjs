// ./eslint.config.base.mjs is a copy of cplieger/ci's canonical ruleset; this file adds the package's ignores.
import baseConfig from "./eslint.config.base.mjs";

// ECMA-262 specifies a date string's parse only for its own three-digit form, so a wire
// instant goes through parseInstant: no Date.parse, and no new Date from anything but a
// number literal or parseInstant's answer.
const instantRule =
  "read a wire instant with parseInstant, never with Date.parse or new Date(text)";

export default [
  ...baseConfig,
  {
    ignores: ["**/reports/**", "**/.stryker-tmp/**", "**/coverage/**"],
  },
  {
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "MemberExpression[object.name='Date'][property.name='parse']",
          message: instantRule,
        },
        {
          selector: "MemberExpression[object.name='Date'][property.value='parse']",
          message: instantRule,
        },
        {
          selector:
            "NewExpression:matches([callee.name='Date'], [callee.property.name='Date']) > .arguments:not(Literal[value=type(number)], CallExpression[callee.name='parseInstant'])",
          message: instantRule,
        },
      ],
    },
  },
];
