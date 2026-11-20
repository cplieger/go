//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package ownersock

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// withPeerUID makes the listeners the test opens read peers through f.
// It swaps a package var, so its tests run serially.
func withPeerUID(t *testing.T, f func(*net.UnixConn) (int, error)) {
	t.Helper()
	old := peerUID
	peerUID = f
	t.Cleanup(func() { peerUID = old })
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func withClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	old := now
	now = c.now
	t.Cleanup(func() { now = old })
	return c
}

func otherUID() int { return os.Geteuid() + 1 }

// refusedBeforeAnyByte sends a request over a raw connection and requires
// the server to close it having written nothing.
func refusedBeforeAnyByte(t *testing.T, path string) {
	t.Helper()
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET /peer HTTP/1.1\r\nHost: admin\r\n\r\n")
	got, err := io.ReadAll(c)
	if len(got) != 0 {
		t.Errorf("the server wrote %q to a refused peer, want nothing", got)
	}
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("reading a refused connection: %v, want EOF or a reset", err)
	}
}

func warnLines(logs, uid string) int {
	n := 0
	for line := range strings.Lines(logs) {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, " uid="+uid) {
			n++
		}
	}
	return n
}

func TestAccept_refusesAnotherUser(t *testing.T) {
	var calls atomic.Int64
	withPeerUID(t, func(*net.UnixConn) (int, error) {
		if calls.Add(1) == 1 {
			return otherUID(), nil
		}
		return os.Geteuid(), nil
	})
	logs := &syncBuffer{}
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, slog.New(slog.NewTextHandler(logs, nil))))

	refusedBeforeAnyByte(t, path)
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%d requests from another user reached the handler, want 0", n)
	}
	if n := warnLines(logs.String(), strconv.Itoa(otherUID())); n != 1 {
		t.Errorf("%d WARN lines for the refused uid, want 1; logs: %s", n, logs)
	}

	body, err := get(t, path)
	if err != nil {
		t.Fatalf("the connection after a refusal: %v, want it accepted", err)
	}
	if want := "uid=" + strconv.Itoa(os.Geteuid()) + " ok=true"; body != want {
		t.Errorf("the next request saw %q, want %q", body, want)
	}
}

func TestAccept_refusesAPeerItCannotRead(t *testing.T) {
	withPeerUID(t, func(*net.UnixConn) (int, error) { return 0, errors.New("getsockopt failed") })
	logs := &syncBuffer{}
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, slog.New(slog.NewTextHandler(logs, nil))))

	refusedBeforeAnyByte(t, path)
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%d requests reached the handler, want 0", n)
	}
	if n := warnLines(logs.String(), "-1"); n != 1 || !strings.Contains(logs.String(), "getsockopt failed") {
		t.Errorf("want one WARN line under uid -1 naming the cause; logs: %s", logs)
	}
}

func TestAccept_warnsOncePerUIDAMinute(t *testing.T) {
	clock := withClock(t)
	var uid atomic.Int64
	uid.Store(int64(otherUID()))
	withPeerUID(t, func(*net.UnixConn) (int, error) { return int(uid.Load()), nil })
	logs := &syncBuffer{}
	_, path := sockPaths(t)
	serve(t, listens(t, path, slog.New(slog.NewTextHandler(logs, nil))))
	first, second := strconv.Itoa(otherUID()), strconv.Itoa(otherUID()+1)

	for range 3 {
		refusedBeforeAnyByte(t, path)
	}
	if n := warnLines(logs.String(), first); n != 1 {
		t.Errorf("after three refusals at once: %d WARN lines, want 1", n)
	}
	clock.advance(59 * time.Second)
	refusedBeforeAnyByte(t, path)
	if n := warnLines(logs.String(), first); n != 1 {
		t.Errorf("59 seconds later: %d WARN lines, want still 1", n)
	}
	uid.Store(int64(otherUID() + 1))
	refusedBeforeAnyByte(t, path)
	if n := warnLines(logs.String(), second); n != 1 {
		t.Errorf("a second uid: %d WARN lines for it, want its own 1", n)
	}
	uid.Store(int64(otherUID()))
	clock.advance(2 * time.Second)
	refusedBeforeAnyByte(t, path)
	if n := warnLines(logs.String(), first); n != 2 {
		t.Errorf("61 seconds after the first: %d WARN lines, want 2", n)
	}
}

func TestAccept_nilLoggerWarnsOnTheDefault(t *testing.T) {
	withPeerUID(t, func(*net.UnixConn) (int, error) { return otherUID(), nil })
	logs := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	_, path := sockPaths(t)
	ln, err := Listen(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	serve(t, ln)

	refusedBeforeAnyByte(t, path)
	if n := warnLines(logs.String(), strconv.Itoa(otherUID())); n != 1 {
		t.Errorf("%d WARN lines on slog.Default, want 1; logs: %s", n, logs)
	}
}

func TestPeer_unmarked(t *testing.T) {
	t.Run("a context no connection marked", func(t *testing.T) {
		if uid, ok := Peer(t.Context()); ok || uid != -1 {
			t.Errorf("Peer = %d, %v; want -1, false", uid, ok)
		}
	})
	t.Run("a connection another listener accepted", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		if uid, ok := Peer(ConnContext(t.Context(), a)); ok || uid != -1 {
			t.Errorf("Peer = %d, %v; want -1, false", uid, ok)
		}
	})
}
