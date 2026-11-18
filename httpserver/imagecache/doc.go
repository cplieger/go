// Package imagecache serves an upstream's images from the app's own origin,
// checked and kept in a bounded on-disk cache.
//
// The app supplies a [Fetch] that calls its upstream client with its own
// credential, and checks a request's id against its library before calling
// [Cache.Serve]. The package makes no HTTP request and holds no credential.
//
// An image is kept only when its declared type is image/jpeg, image/png,
// image/gif or image/webp, equals what [net/http.DetectContentType] sniffs
// from its bytes, and its size is 1 byte to Options.MaxImage. SVG is never
// kept. Anything else is dropped and answered image_unavailable.
//
// # Eviction
//
// Options.MaxBytes bounds the total size of the cache's files on disk. After
// each image is written, the images fetched longest ago are removed until the
// total fits, so the directory exceeds MaxBytes by at most the one image
// being written. Serving an image does not change its place; refetching it
// does. An image whose file alone is larger than MaxBytes is answered and
// not kept.
//
// # Restart
//
// Each file holds its key, type, digest and the time it was fetched, so
// [New] rebuilds the cache from the directory with no index file. It keeps
// every file that still passes the checks under the current options, and
// removes a 64-hex-digit-named file that does not, along with temp files
// over a minute old left by an interrupted write. Every other name is the
// app's. The fetch time is the one recorded in the file, so a restart
// changes no image's age. If MaxBytes was lowered, the oldest are evicted.
// Use one Cache per directory.
//
// # Orphans
//
// An orphan is a cache file left on disk that the cache does not vouch for:
// one the start or Serve could not read, or one whose removal failed. It is
// never served and is outside MaxBytes. Forget of its key and any
// ForgetSpace remove it, and a new write of its key replaces it. A restart
// reads it again, so a removal error from Forget or ForgetSpace means a
// forgotten image may still be served after a restart: retry the call. The
// error ForgetSpace returns for a space over 128 bytes comes after its
// removals and does not go away on retry.
//
// # Shutdown
//
// [Cache.Shutdown] cancels every fetch in flight, including background
// refetches of stale images, and waits for them. Call it from the server's
// teardown, after the drain and before closing Options.Root. After it, Serve
// still answers kept images, answers a miss image_unavailable, and never
// calls Fetch.
//
// # Fetch budget
//
// A fetch runs under the first caller's deadline, or 30 seconds when its
// request has none. A background refetch always gets 30 seconds. Callers of
// one key share one fetch; the fetch is cancelled when every caller has
// left.
package imagecache
