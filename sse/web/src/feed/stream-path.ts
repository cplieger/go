const BASES = ["https://feed.invalid", "https://second.feed.invalid"];

/**
 * Whether `path` is a root-relative URL, which resolves on the page's own origin. One base cannot
 * tell, since a path naming that base's host keeps its origin. The URL parser strips tabs and
 * newlines and reads `\` as `/` (https://url.spec.whatwg.org/#concept-basic-url-parser), so it
 * decides rather than a prefix check.
 */
export function rootPath(path: string): boolean {
  if (!path.startsWith("/")) {
    return false;
  }
  try {
    return BASES.every((base) => new URL(path, base).origin === base);
  } catch {
    return false;
  }
}
