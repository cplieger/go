package ownersock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// The codes an [*Error] from [Client] carries: stable strings a
// command-line tool may match and map to its own sentences.
const (
	// CodePeerRefused: the server closed the connection before any answer,
	// because the caller's uid is not the server's.
	CodePeerRefused = "admin_peer_refused"
	// CodeUnavailable: no server listens at the path (ENOENT, ECONNREFUSED).
	CodeUnavailable = "admin_unavailable"
	// CodeDenied: the socket's directory or file refused the caller (EACCES).
	CodeDenied = "admin_denied"
)

// Error is a [Client] failure with a stable code and a hint a command-line
// tool can print as it is. Err is the cause and matches with errors.Is.
type Error struct {
	Err  error
	Code string
	Hint string
	Path string
}

func (e *Error) Error() string {
	if e.Err == nil {
		return "ownersock: " + e.Code + ": " + e.Hint
	}
	return "ownersock: " + e.Code + ": " + e.Err.Error() + ": " + e.Hint
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

func deniedHint(path string) string {
	return "only the server's user can use the admin socket at " + path +
		"; run the command as that user (docker exec --user <PUID>:<PGID>)"
}

func unavailableHint(path string) string {
	return "no server is listening on " + path + "; is the app running? " +
		"A command runs in-process only when the app cannot start"
}

// Client returns an *http.Client that dials the socket at path whatever a
// request's host, with keep-alives off so each request has its own
// connection. Its errors match [*Error] with errors.As when the server
// refused the caller's uid ([CodePeerRefused]), no server listens
// ([CodeUnavailable]) or the caller may not reach the socket ([CodeDenied]);
// any other failure is returned wrapped, with no code. It sets no timeout:
// the caller bounds a call with its request's context or Client's Timeout.
func Client(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if err := checkPathLen(path); err != nil {
				return nil, err
			}
			c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
			if err != nil {
				return nil, dialError(path, err)
			}
			return &clientConn{Conn: c, path: path, decided: make(chan struct{})}, nil
		},
		DisableKeepAlives: true,
	}}
}

func dialError(path string, err error) error {
	switch {
	case errors.Is(err, syscall.EACCES):
		return &Error{Code: CodeDenied, Hint: deniedHint(path), Path: path, Err: err}
	case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ECONNREFUSED):
		return &Error{Code: CodeUnavailable, Hint: unavailableHint(path), Path: path, Err: err}
	}
	return fmt.Errorf("ownersock: dial %s: %w", path, err)
}

// verdictWait bounds how long a failed write waits for the first read's
// verdict. net/http's read loop reads from the moment the connection is
// dialed, and a peer that closed makes that read return at once.
const verdictWait = time.Second

// clientConn turns the server's refusal into a [CodePeerRefused] error on
// every read and write of the refused connection, so the code survives
// whichever error net/http returns: with a large body the write loop's error
// wins, often a net.ErrClosed after the read loop closed the connection.
// The first read that ends decides: zero bytes and the peer's close is a
// refusal, and any byte read is an answer.
type clientConn struct {
	net.Conn
	refusal error
	decided chan struct{}
	path    string
	once    sync.Once
}

func (c *clientConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 || err != nil {
		c.once.Do(func() {
			if n == 0 && closedByPeer(err) {
				c.refusal = &Error{Code: CodePeerRefused, Hint: deniedHint(c.path), Path: c.path, Err: err}
			}
			close(c.decided)
		})
	}
	if err != nil && c.refusal != nil {
		return n, c.refusal
	}
	return n, err
}

func (c *clientConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil || !closedByPeer(err) && !errors.Is(err, net.ErrClosed) {
		return n, err
	}
	t := time.NewTimer(verdictWait)
	defer t.Stop()
	select {
	case <-c.decided:
		if c.refusal != nil {
			return n, c.refusal
		}
	case <-t.C:
	}
	return n, err
}

// closedByPeer includes ENOTCONN, which darwin's write(2) returns on a unix
// socket whose peer has closed.
func closedByPeer(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENOTCONN)
}
