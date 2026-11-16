package ownersock

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/textsafe"
)

// maxPathLen is the longest socket path that binds: unix(7) gives sun_path
// 108 bytes, and a pathname socket's path is NUL-terminated inside it.
const maxPathLen = 107

// probeTimeout bounds the dial that asks whether a socket left at the path
// still has a server behind it.
const probeTimeout = 2 * time.Second

// warnEvery is how long a refused uid's WARN line silences the next ones.
const warnEvery = time.Minute

// Config describes the admin socket [Listen] binds.
type Config struct {
	// Logger receives the refusal warnings; nil is slog.Default().
	Logger *slog.Logger
	// Path is the socket's absolute, clean path, at most 107 bytes, in a
	// directory of its own that filesafe.EnsurePrivateDir accepts.
	Path string
}

// Test seams, read once by each Listen.
var (
	peerUID = socketPeerUID
	now     = time.Now
)

// Listen creates c.Path's directory at 0700 if missing or proves an existing
// one private, binds the socket there at mode 0600 and returns a listener
// whose Accept peer-checks every connection.
// A path over 107 bytes, or one that is not absolute and clean, is refused
// before the directory is touched. A socket a server answers on refuses
// start, a socket nothing answers is replaced, and any other file at the
// path is refused and left as it is. Directory refusals match filesafe's
// sentinels with errors.Is. Closing the listener removes the socket.
func Listen(c Config) (net.Listener, error) {
	path := c.Path
	if err := checkPathLen(path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || dir == path {
		return nil, fmt.Errorf("ownersock: socket path %q is not an absolute, clean file path",
			textsafe.SanitizeSingleLine(path))
	}
	if _, err := filesafe.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("ownersock: socket directory: %w", err)
	}
	ul, err := bind(path)
	if err != nil {
		return nil, err
	}
	// The socket is born with the umask's mode; the window before this chmod
	// reaches no other uid because the directory is already 0700 and ours.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("ownersock: chmod socket: %w", err), ul.Close())
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &listener{
		ul:      ul,
		uid:     os.Geteuid(),
		peerUID: peerUID,
		now:     now,
		log:     logger,
		warned:  map[int]time.Time{},
	}, nil
}

func checkPathLen(path string) error {
	if len(path) <= maxPathLen {
		return nil
	}
	return fmt.Errorf("ownersock: socket path %q is %d bytes, over the %d a unix socket path holds",
		textsafe.SanitizeSingleLine(path), len(path), maxPathLen)
}

// bind binds first so the kernel is the single-instance guard: nothing is
// removed while the path is free, and two starts racing for it cannot both
// win. EADDRINUSE asks what is at the path, and a dead socket's removal is
// followed by one more bind, never a loop.
func bind(path string) (*net.UnixListener, error) {
	ul, err := listenUnix(path)
	if !errors.Is(err, syscall.EADDRINUSE) {
		return ul, err
	}
	if rmErr := removeDeadSocket(path); rmErr != nil {
		return nil, rmErr
	}
	ul, err = listenUnix(path)
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, liveServer(path)
	}
	return ul, err
}

func listenUnix(path string) (*net.UnixListener, error) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, fmt.Errorf("ownersock: %w", err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("ownersock: listening on %q returned a %T", textsafe.SanitizeSingleLine(path), ln)
	}
	return ul, nil
}

// removeDeadSocket removes the socket at path when a dial finds no server
// behind it. A live socket, a file that is not a socket (a symlink
// included) and a probe that fails any other way are refused and nothing
// is removed.
func removeDeadSocket(path string) error {
	name := textsafe.SanitizeSingleLine(path)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ownersock: %w", err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("ownersock: %q is a %s, not a socket; refusing to replace it", name, fileKind(fi.Mode()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return liveServer(path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("ownersock: probing the socket at %q: %w", name, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ownersock: removing a dead socket: %w", err)
	}
	return nil
}

func liveServer(path string) error {
	return fmt.Errorf("ownersock: a server is already listening on %q", textsafe.SanitizeSingleLine(path))
}

func fileKind(m fs.FileMode) string {
	switch m.Type() {
	case 0:
		return "regular file"
	case fs.ModeDir:
		return "directory"
	case fs.ModeSymlink:
		return "symlink"
	default:
		return "special file"
	}
}

// listener holds the *net.UnixListener unexported so no caller reaches its
// AcceptUnix, which would skip the peer check.
type listener struct {
	ul      *net.UnixListener
	peerUID func(*net.UnixConn) (int, error)
	now     func() time.Time
	log     *slog.Logger
	warned  map[int]time.Time // guarded by mu
	uid     int
	mu      sync.Mutex
}

// Accept returns the next connection whose peer is the server's uid. A
// refused connection is closed before any byte is read or written.
func (l *listener) Accept() (net.Conn, error) {
	for {
		c, err := l.ul.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.peerUID(c)
		if err == nil && uid == l.uid {
			return &conn{UnixConn: c, uid: uid}, nil
		}
		if err != nil {
			uid = -1
		}
		// Logged before the close, so the line exists once the peer sees it.
		l.warn(uid, err)
		_ = c.Close()
	}
}

// Close stops the listener and removes the socket file.
func (l *listener) Close() error { return l.ul.Close() }

// Addr is the socket's address.
func (l *listener) Addr() net.Addr { return l.ul.Addr() }

// warn logs a refusal unless the same uid was logged within warnEvery.
// Entries older than that are dropped here, so the map holds only the uids
// refused in the last minute.
func (l *listener) warn(uid int, cause error) {
	t := l.now()
	l.mu.Lock()
	for u, at := range l.warned {
		if t.Sub(at) >= warnEvery {
			delete(l.warned, u)
		}
	}
	_, seen := l.warned[uid]
	if !seen {
		l.warned[uid] = t
	}
	l.mu.Unlock()
	if seen {
		return
	}
	args := []any{"uid", uid, "server_uid", l.uid, "path", l.ul.Addr().String()}
	if cause != nil {
		args = append(args, "err", cause)
	}
	l.log.Warn("ownersock: refused a connection from another user", args...)
}

// conn is a connection Accept peer-checked; ConnContext recognises it.
type conn struct {
	*net.UnixConn
	uid int
}

type peerKey struct{}

// ConnContext is an http.Server ConnContext: it marks a connection this
// package's listener accepted and peer-checked, so [Peer] reports it on
// each request. Any other connection leaves ctx unmarked.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if pc, ok := c.(*conn); ok {
		return context.WithValue(ctx, peerKey{}, pc.uid)
	}
	return ctx
}

// Peer returns the peer's uid and true when ctx carries [ConnContext]'s
// mark, and -1 and false otherwise.
func Peer(ctx context.Context) (uid int, ok bool) {
	uid, ok = ctx.Value(peerKey{}).(int)
	if !ok {
		return -1, false
	}
	return uid, true
}
