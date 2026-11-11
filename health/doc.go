// Package health gives a Go service one health verdict, kept in memory, and
// the two ways a container healthcheck can read it.
//
// The service records its verdict in a Status with Set, and serves it at
// GET /healthz with CheckHandler. A service that always listens checks itself
// by asking that endpoint (package probe). A service that opens no port adds
// WithMarker: every Set then also creates or removes a marker file, and the
// same binary run as `<binary> health` reads it with RunProbe.
//
// If, when NewStatus runs, the marker cannot be written or a symlink is at its
// path, the Status enters degraded mode. Healthy, Fresh and every HTTP
// endpoint keep reporting the in-memory verdict, and RunProbe reports healthy
// while the marker cannot be written, so a working container is not
// restarted. A service that keeps calling Set leaves degraded mode at the
// first Set that can write. One that sets its verdict only at startup makes
// no later Set, so once the cause is fixed it needs a restart.
//
// The package starts no goroutine and takes no context.
package health
