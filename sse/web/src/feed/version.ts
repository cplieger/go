/**
 * The feed frame format this package reads, equal to `feedwire.Version` at the same go-repo tag.
 * The app passes its generated `FEED_VERSION` as `FeedOptions.feedVersion`.
 */
export const FEED_VERSION = 1;

/**
 * Thrown by `createFeed` when the app's generated `FEED_VERSION` differs from this package's: the
 * app's server writes frames in a format this parser cannot read. Move `@cplieger/sse` and the Go
 * `sse/v2` module together, then regenerate the client.
 */
export class FeedVersionMismatch extends Error {
  override readonly name = "FeedVersionMismatch";
  /** The app's generated `FEED_VERSION`. */
  readonly generated: number;
  /** This package's `FEED_VERSION`. */
  readonly library: number;

  constructor(generated: number, library: number) {
    super(
      `feed format mismatch: the generated client's FEED_VERSION is ${String(generated)} and @cplieger/sse/feed reads ${String(library)}`,
    );
    this.generated = generated;
    this.library = library;
  }
}
