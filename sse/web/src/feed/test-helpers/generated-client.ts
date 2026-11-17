// The shape wiregen writes for an app's generated client, built against the next feed format, so
// the version test can pass a mismatched FEED_VERSION the way a stale generated client would.

export const FEED_VERSION = 2;

export const KINDS = {
  "2fa": "store",
  budgets: "store",
  runs: "store",
  queue: "live",
  settings: "settings",
} as const;

export const LOGGED_SOURCES = ["settings", "store"] as const;
