//go:build !windows

package ownersock

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortRoot is a fresh directory under the system temporary root, short
// enough that a socket below it fits sun_path (unix(7)) wherever t.TempDir's
// long test names would not.
func shortRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "os")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// sockPaths is a socket directory, not yet created, and the socket in it.
func sockPaths(t *testing.T) (dir, path string) {
	t.Helper()
	dir = filepath.Join(shortRoot(t), "d")
	return dir, filepath.Join(dir, "s.sock")
}

func listens(t *testing.T, path string, logger *slog.Logger) net.Listener {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ln, err := Listen(Config{Path: path, Logger: logger})
	if err != nil {
		t.Fatalf("Listen(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// dials proves something accepts connections at path.
func dials(t *testing.T, path string) {
	t.Helper()
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	_ = c.Close()
}

// deadSocket leaves at path the socket file of a server that has exited.
func deadSocket(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = old.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the dead server's socket is not there: %v", err)
	}
}

// served is an HTTP server on an ownersock listener whose handler answers
// the request's Peer mark and counts the requests that reached it.
type served struct {
	hits atomic.Int64
}

func serve(t *testing.T, ln net.Listener) *served {
	t.Helper()
	s := &served{}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.hits.Add(1)
			uid, ok := Peer(r.Context())
			_, _ = io.WriteString(w, "uid="+strconv.Itoa(uid)+" ok="+strconv.FormatBool(ok))
		}),
		ConnContext:       ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return s
}

// get calls path through Client and returns the answer's body.
func get(t *testing.T, path string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://any.host.invalid/peer", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Client(path).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
