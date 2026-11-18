import { createVersionMap } from "../versions.js";
import type { WorkerHostOptions } from "../worker.js";
import { retainListening } from "./retain.js";
import { rootPath } from "./stream-path.js";

/**
 * The feed worker's host options for the worker script at `href`: one stream at the
 * root-relative path in its `stream` query parameter. Throws a RangeError naming the parameter
 * when it is missing or names no root-relative path, so no stream opens on a default.
 */
export function feedHostOptions(href: string): WorkerHostOptions {
  const path = new URL(href).searchParams.get("stream");
  if (path === null) {
    throw new RangeError('feed worker: the worker URL has no "stream" query parameter');
  }
  if (!rootPath(path)) {
    throw new RangeError(`feed worker: stream "${path}" is not a root-relative path`);
  }
  // The feed takes no revalidation: each connection's listening frame carries what a reconnect
  // needs, and the host posts its retained frame to every tab that attaches.
  return {
    url: path,
    versions: createVersionMap(),
    revalidate: () => Promise.resolve(),
    retain: retainListening,
  };
}
