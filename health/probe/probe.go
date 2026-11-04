// Package probe implements an HTTP liveness probe for containers
// without a shell: the HTTP counterpart of the file marker in the
// health package (github.com/cplieger/go/health), for containers that wrap
// a third-party server which cannot cooperate with a Marker but already
// exposes an HTTP endpoint whose reachability IS the health signal.
//
// The ready-made binary around this package lives in cmd/probe; bake it
// into the image and wire it as the Docker HEALTHCHECK.
package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DefaultTimeout is the default total wall-clock budget for one probe
// run (all URLs together). Five seconds sits below Docker's default
// HEALTHCHECK --timeout, so a hung endpoint fails with a written
// reason instead of a SIGKILL from the runtime.
const DefaultTimeout = 5 * time.Second

// URL performs a single liveness GET against url and returns nil when
// the final response (after following redirects) has a 2xx status. Any
// transport error, context expiry, or non-2xx status is an error.
func URL(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err // *url.Error already names the URL and operation
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// Check probes every URL within one shared timeout budget and returns
// 0 when all succeed, 1 otherwise, writing one line per failure to w.
// It probes ALL URLs rather than stopping at the first failure, so a
// multi-surface healthcheck reports every broken surface in one run.
//
// Zero URLs is reported unhealthy: an empty probe answering "healthy"
// would silently mask a misconfigured HEALTHCHECK. A non-positive
// timeout fails immediately for the same reason.
func Check(w io.Writer, timeout time.Duration, urls ...string) int {
	if len(urls) == 0 {
		fmt.Fprintln(w, "unhealthy: no URLs to probe")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	code := 0
	for _, u := range urls {
		if err := URL(ctx, u); err != nil {
			fmt.Fprintf(w, "unhealthy: probe %s: %v\n", u, err)
			code = 1
		}
	}
	return code
}

// Run runs in the probe process (a Docker HEALTHCHECK exec) and exits 0
// when every URL answers 2xx within the shared timeout, 1 otherwise,
// with each failure written to stderr. The HTTP counterpart of the
// parent module's RunProbe; cmd/probe is the ready-made binary around
// it.
func Run(timeout time.Duration, urls ...string) {
	os.Exit(Check(os.Stderr, timeout, urls...))
}
