package probe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLocalURL(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		path       string
		want       string
	}{
		{"empty host", ":8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"unspecified IPv4", "0.0.0.0:8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"unspecified IPv6", "[::]:8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"unspecified IPv4 mapped", "[::ffff:0.0.0.0]:8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"unspecified IPv6 with a zone", "[::%eth0]:8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"loopback IPv4", "127.0.0.1:9190", "/healthz", "http://127.0.0.1:9190/healthz"},
		{"bound IPv4", "192.0.2.10:9190", "/healthz", "http://192.0.2.10:9190/healthz"},
		{"loopback IPv6", "[::1]:8080", "/healthz", "http://[::1]:8080/healthz"},
		{"bound IPv6", "[2001:db8::1]:8080", "/healthz", "http://[2001:db8::1]:8080/healthz"},
		{"IPv6 with a zone", "[fe80::1%eth0]:80", "/healthz", "http://[fe80::1%25eth0]:80/healthz"},
		{"hostname", "localhost:8080", "/healthz", "http://localhost:8080/healthz"},
		{"path escaped", ":8080", "/a b", "http://127.0.0.1:8080/a%20b"},
		{"highest port", ":65535", "/healthz", "http://127.0.0.1:65535/healthz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LocalURL(tc.listenAddr, tc.path)
			if err != nil {
				t.Fatalf("LocalURL(%q, %q) error = %v", tc.listenAddr, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("LocalURL(%q, %q) = %q, want %q", tc.listenAddr, tc.path, got, tc.want)
			}
			if _, err := url.Parse(got); err != nil {
				t.Errorf("url.Parse(%q) = %v", got, err)
			}
			if _, err := http.NewRequest(http.MethodGet, got, http.NoBody); err != nil {
				t.Errorf("http.NewRequest(%q) = %v", got, err)
			}
		})
	}
}

func TestLocalURLRefuses(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		path       string
	}{
		{"missing port", "8080", "/healthz"},
		{"empty port", "host:", "/healthz"},
		{"ephemeral port", ":0", "/healthz"},
		{"service name", ":http", "/healthz"},
		{"out of range", ":65536", "/healthz"},
		{"six digits", ":000080", "/healthz"},
		{"signed port", ":+80", "/healthz"},
		{"host gives no valid URL", "a b:8080", "/healthz"},
		{"empty path", ":8080", ""},
		{"relative path", ":8080", "healthz"},
		{"swapped arguments", "/healthz", ":8080"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LocalURL(tc.listenAddr, tc.path)
			if err == nil {
				t.Errorf("LocalURL(%q, %q) = %q, want an error", tc.listenAddr, tc.path, got)
			}
			if got != "" {
				t.Errorf("LocalURL(%q, %q) returned %q beside its error, want empty", tc.listenAddr, tc.path, got)
			}
		})
	}
}

// The caller wraps the error with its own setting name, so a part named
// twice here would print twice in the caller's message.
func TestLocalURLErrorsNameTheAddressOnce(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		path       string
		want       string
		absent     []string
	}{
		{"missing port", "8080", "/healthz", "8080", nil},
		{"bad port", "myhost:http", "/healthz", `"http"`, []string{"myhost"}},
		{"signed port", "myhost:+80", "/healthz", `"+80"`, []string{"myhost"}},
		{"bad path", "myhost:8080", "healthz", `"healthz"`, []string{"myhost", "8080"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LocalURL(tc.listenAddr, tc.path)
			if err == nil {
				t.Fatalf("LocalURL(%q, %q) error = nil, want one", tc.listenAddr, tc.path)
			}
			msg := err.Error()
			if n := strings.Count(msg, tc.want); n != 1 {
				t.Errorf("error %q holds %q %d times, want once", msg, tc.want, n)
			}
			for _, a := range tc.absent {
				if strings.Contains(msg, a) {
					t.Errorf("error %q names %q, which did not fail", msg, a)
				}
			}
		})
	}
}

func TestLocalURLReachesAListener(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, err := LocalURL(srv.Listener.Addr().String(), "/healthz")
	if err != nil {
		t.Fatalf("LocalURL(%q) error = %v", srv.Listener.Addr(), err)
	}
	if code := Check(io.Discard, DefaultTimeout, u); code != 0 {
		t.Errorf("Check(%q) = %d, want 0", u, code)
	}
}
