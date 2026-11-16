//go:build !windows

package ownersock

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const runAsTheServersUser = "docker exec --user <PUID>:<PGID>"

func asError(t *testing.T, err error) *Error {
	t.Helper()
	oerr, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("error %v (%T) is not an *ownersock.Error", err, err)
	}
	return oerr
}

func wantUnavailable(t *testing.T, err error, path string, cause syscall.Errno) {
	t.Helper()
	oerr := asError(t, err)
	if oerr.Code != CodeUnavailable || oerr.Path != path {
		t.Errorf("Code, Path = %q, %q; want %q, %q", oerr.Code, oerr.Path, CodeUnavailable, path)
	}
	for _, want := range []string{path, "running", "in-process"} {
		if !strings.Contains(oerr.Hint, want) {
			t.Errorf("hint %q, want it to carry %q", oerr.Hint, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Errorf("error %v does not match %v", err, cause)
	}
}

func TestClient_reportsNoServer(t *testing.T) {
	t.Run("nothing at the path (ENOENT)", func(t *testing.T) {
		_, path := sockPaths(t)
		_, err := get(t, path)
		wantUnavailable(t, err, path, syscall.ENOENT)
	})
	t.Run("a dead server's socket (ECONNREFUSED)", func(t *testing.T) {
		dir, path := sockPaths(t)
		deadSocket(t, dir, path)
		_, err := get(t, path)
		wantUnavailable(t, err, path, syscall.ECONNREFUSED)
	})
}

func TestClient_reportsARefusedPeer(t *testing.T) {
	withPeerUID(t, func(*net.UnixConn) (int, error) { return otherUID(), nil })
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, nil))
	_, err := get(t, path)
	oerr := asError(t, err)
	if oerr.Code != CodePeerRefused || oerr.Path != path {
		t.Errorf("Code, Path = %q, %q; want %q, %q", oerr.Code, oerr.Path, CodePeerRefused, path)
	}
	denied := asError(t, dialError(path, os.NewSyscallError("connect", syscall.EACCES)))
	if oerr.Hint != denied.Hint || !strings.Contains(oerr.Hint, runAsTheServersUser) {
		t.Errorf("hint %q, want admin_denied's %q", oerr.Hint, denied.Hint)
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%d requests reached the handler, want 0", n)
	}
}

// A body larger than the socket buffers makes net/http's write loop fail
// first, with EPIPE or with the read loop's close; both must still carry the
// refusal's code. Repeated because which loop's error wins is a race.
func TestClient_reportsARefusedPeerSendingABody(t *testing.T) {
	withPeerUID(t, func(*net.UnixConn) (int, error) { return otherUID(), nil })
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, nil))
	for i := range 20 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://admin/peer",
			bytes.NewReader(make([]byte, 8<<20)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := Client(path).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("request %d: a refused peer's POST succeeded", i)
		}
		if oerr, ok := errors.AsType[*Error](err); !ok || oerr.Code != CodePeerRefused {
			t.Errorf("request %d: %v, want %s", i, err, CodePeerRefused)
		}
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%d requests reached the handler, want 0", n)
	}
}

// closedConn is a connection the server closed before any answer: reads end
// at once with zero bytes, and writes fail with the OS's error for it.
type closedConn struct {
	net.Conn
	readErr, writeErr error
}

func (c closedConn) Read([]byte) (int, error)  { return 0, c.readErr }
func (c closedConn) Write([]byte) (int, error) { return 0, c.writeErr }

// net/http may return the write loop's error rather than the read loop's,
// and each OS names a write to a closed peer its own way.
func TestClientConn_aWriteAfterTheRefusalCarriesTheCode(t *testing.T) {
	for name, writeErr := range map[string]error{
		"EPIPE":      syscall.EPIPE,
		"ECONNRESET": syscall.ECONNRESET,
		"ENOTCONN":   syscall.ENOTCONN,
	} {
		t.Run(name, func(t *testing.T) {
			c := &clientConn{
				Conn:    closedConn{readErr: io.EOF, writeErr: os.NewSyscallError("write", writeErr)},
				path:    "/run/app/admin.sock",
				decided: make(chan struct{}),
			}
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("Read of a closed connection succeeded")
			}
			_, err := c.Write([]byte("POST /peer HTTP/1.1\r\n"))
			if oerr, ok := errors.AsType[*Error](err); !ok || oerr.Code != CodePeerRefused {
				t.Errorf("Write: %v, want %s", err, CodePeerRefused)
			}
		})
	}
}

func TestClient_answersOnAServedSocket(t *testing.T) {
	withPeerUID(t, func(*net.UnixConn) (int, error) { return os.Geteuid(), nil })
	_, path := sockPaths(t)
	serve(t, listens(t, path, nil))
	for range 2 {
		if _, err := get(t, path); err != nil {
			t.Errorf("a request to a live server: %v", err)
		}
	}
}

func TestDialError_classifies(t *testing.T) {
	path := "/run/app/admin.sock"
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}
	}
	t.Run("EACCES is admin_denied", func(t *testing.T) {
		err := dialError(path, dial(syscall.EACCES))
		oerr := asError(t, err)
		if oerr.Code != CodeDenied || oerr.Path != path {
			t.Errorf("Code, Path = %q, %q; want %q, %q", oerr.Code, oerr.Path, CodeDenied, path)
		}
		if !strings.Contains(oerr.Hint, runAsTheServersUser) || !strings.Contains(oerr.Hint, path) {
			t.Errorf("hint %q, want it to name the path and %q", oerr.Hint, runAsTheServersUser)
		}
		if !errors.Is(err, syscall.EACCES) {
			t.Errorf("error %v does not match EACCES", err)
		}
	})
	t.Run("ENOENT is admin_unavailable", func(t *testing.T) {
		wantUnavailable(t, dialError(path, dial(syscall.ENOENT)), path, syscall.ENOENT)
	})
	t.Run("ECONNREFUSED is admin_unavailable", func(t *testing.T) {
		wantUnavailable(t, dialError(path, dial(syscall.ECONNREFUSED)), path, syscall.ECONNREFUSED)
	})
	t.Run("any other error carries no code", func(t *testing.T) {
		err := dialError(path, dial(syscall.EMFILE))
		if oerr, ok := errors.AsType[*Error](err); ok {
			t.Errorf("EMFILE got code %q, want none", oerr.Code)
		}
		if !errors.Is(err, syscall.EMFILE) || !strings.Contains(err.Error(), path) {
			t.Errorf("error %v, want EMFILE wrapped with the path", err)
		}
	})
}

func TestClient_aPathTooLongToDialCarriesNoCode(t *testing.T) {
	path := filepath.Join(shortRoot(t), strings.Repeat("p", 200))
	_, err := get(t, path)
	if err == nil {
		t.Fatal("a request on a 200-byte path succeeded")
	}
	if oerr, ok := errors.AsType[*Error](err); ok {
		t.Errorf("got code %q, want none", oerr.Code)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %v, want it to name the path", err)
	}
}
