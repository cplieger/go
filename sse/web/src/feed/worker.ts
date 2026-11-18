import { createWorkerHost } from "../worker.js";
import { feedHostOptions } from "./host-options.js";

interface SharedScope {
  onconnect: ((event: MessageEvent) => void) | null;
  readonly location: { readonly href: string };
}

const scope = globalThis as unknown as SharedScope;
const host = createWorkerHost(feedHostOptions(scope.location.href));

scope.onconnect = (event: MessageEvent) => {
  const port = event.ports[0];
  if (port !== undefined) {
    host.attach(port);
  }
};
