package probe

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// FuzzLocalURL takes both arguments as untrusted, since a service reads its
// listen address from the environment. A URL it returns always parses, keeps
// the scheme, path and port, and names the host the listener answers on.
func FuzzLocalURL(f *testing.F) {
	for _, s := range [][2]string{
		{":8080", "/healthz"},
		{"0.0.0.0:8080", "/healthz"},
		{"[::]:8080", "/healthz"},
		{"[::ffff:0.0.0.0]:8080", "/healthz"},
		{"127.0.0.1:9190", "/healthz"},
		{"[fe80::1%eth0]:80", "/healthz"},
		{"localhost:8080", "/a b?c#d"},
		{"a b:8080", "/healthz"},
		{":+80", "/healthz"},
		{"8080", "/healthz"},
		{"/healthz", ":8080"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, listenAddr, path string) {
		got, err := LocalURL(listenAddr, path)
		if err != nil {
			if got != "" {
				t.Fatalf("LocalURL(%q, %q) = %q beside error %v", listenAddr, path, got, err)
			}
			return
		}
		host, port, err := net.SplitHostPort(listenAddr)
		if err != nil {
			t.Fatalf("LocalURL(%q, %q) succeeded on an address SplitHostPort refuses: %v", listenAddr, path, err)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("LocalURL(%q, %q) = %q, which does not parse: %v", listenAddr, path, got, err)
		}
		if u.Scheme != "http" || u.Path != path || u.Port() != port {
			t.Fatalf("LocalURL(%q, %q) = %q: scheme %q, path %q, port %q", listenAddr, path, got, u.Scheme, u.Path, u.Port())
		}
		if unspecified(host) {
			if u.Hostname() != "127.0.0.1" {
				t.Fatalf("LocalURL(%q, %q) = %q, want host 127.0.0.1 for an unspecified bind", listenAddr, path, got)
			}
			return
		}
		want, wantErr := netip.ParseAddr(host)
		gotAddr, gotErr := netip.ParseAddr(u.Hostname())
		switch {
		case wantErr == nil && (gotErr != nil || gotAddr != want):
			t.Fatalf("LocalURL(%q, %q) = %q, want host %v", listenAddr, path, got, want)
		case wantErr != nil && u.Hostname() != host:
			t.Fatalf("LocalURL(%q, %q) = %q, want host %q", listenAddr, path, got, host)
		}
	})
}

// unspecified is the oracle for the hosts a wildcard listener binds: empty,
// or an address net.IP calls unspecified, whatever its zone.
func unspecified(host string) bool {
	if host == "" {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}
