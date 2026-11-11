// Package probe is an HTTP liveness probe for containers with no shell. A
// service that serves GET /healthz uses it in its own `health` subcommand,
// with LocalURL deriving the address from its listen address. An image that
// wraps a server it did not write uses the ready-made binary in cmd/probe
// against that server's own endpoint.
package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"time"
)

// DefaultTimeout is the default total budget for one probe run, all URLs
// together. Five seconds sits below Docker's default HEALTHCHECK --timeout,
// so a hung endpoint fails with a written reason instead of a kill.
const DefaultTimeout = 5 * time.Second

// client sends every probe GET. Its own transport has no proxy, so a probe of
// a listener in the same container never leaves it, and keeps no idle
// connection, so every probe dials the listener afresh. Its zero Timeout
// leaves the deadline to the context.
var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// get returns nil when the final response, after redirects, is 2xx.
func get(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err // *url.Error already names the URL and operation
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// Check probes every URL within one shared timeout and returns 0 when all
// answer 2xx, 1 otherwise, writing one line per failure to w. It probes every
// URL even after one fails, so one run names every broken surface. Zero URLs
// or a non-positive timeout is unhealthy. It never sends through a proxy,
// whatever HTTP_PROXY says, and opens a new connection for every URL.
func Check(w io.Writer, timeout time.Duration, urls ...string) int {
	if len(urls) == 0 {
		fmt.Fprintln(w, "unhealthy: no URLs to probe")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	code := 0
	for _, u := range urls {
		if err := get(ctx, u); err != nil {
			fmt.Fprintf(w, "unhealthy: probe %s: %v\n", u, err)
			code = 1
		}
	}
	return code
}

// Run is Check writing to stderr, as a probe process's last call: it exits
// with Check's result.
func Run(timeout time.Duration, urls ...string) {
	os.Exit(Check(os.Stderr, timeout, urls...))
}

// LocalURL returns the http URL at which a probe in the same container reaches
// path on a listener bound to listenAddr, a net.Listen address such as ":8080".
// An empty or unspecified host (0.0.0.0, ::) becomes 127.0.0.1, which a
// listener on network tcp or tcp4 answers on; for a tcp6 listener, pass
// [::1]:port. Any other host is kept, because a listener bound to one address
// answers on no other. An IPv6 host is bracketed. It returns an error when
// listenAddr has no port of digits from 1 to 65535, its host makes no valid
// URL, or path does not start with a slash.
func LocalURL(listenAddr, path string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", err // *net.AddrError names the address; the caller names its setting
	}
	if !validPort(port) {
		return "", fmt.Errorf("port %q is not a number from 1 to 65535", port)
	}
	if path == "" || path[0] != '/' {
		return "", fmt.Errorf("path %q does not start with a slash", path)
	}
	if wildcard(host) {
		host = "127.0.0.1"
	}
	s := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: path}).String()
	// SplitHostPort passes hosts url.Parse refuses ("a b", a control byte).
	if _, err := url.Parse(s); err != nil {
		return "", fmt.Errorf("listen address %q gives no valid URL: %w", listenAddr, err)
	}
	return s, nil
}

// validPort checks the digits first because strconv.Atoi accepts a sign,
// which url.Parse refuses in a port.
func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, c := range []byte(port) {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, _ := strconv.Atoi(port)
	return n >= 1 && n <= 65535
}

// wildcard reports a host net.Listen binds on every address. Like net.Listen,
// it ignores a zone and reads ::ffff:0.0.0.0 as unspecified.
func wildcard(host string) bool {
	if host == "" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.WithZone("").Unmap().IsUnspecified()
}
