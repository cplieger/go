package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// --- Trigger socket server ---
//
// The daemon listens on an in-container unix socket; each connection is one
// run request (see protocol.go). The socket replaces cross-process /tmp
// coordination (flock lattices, rerun flags, drain latches): mutual exclusion
// is the executor's single goroutine, shutdown reaches waiting clients as
// explicit cancellation results, and completion is an ordinary result
// delivery.

const (
	// requestReadTimeout bounds how long a connected client may take to send
	// its request line, so a silent connection cannot hold a handler
	// goroutine (and shutdown) hostage.
	requestReadTimeout = 30 * time.Second
	// eventWriteTimeout bounds each status write, so a dead client cannot
	// block a handler.
	eventWriteTimeout = 10 * time.Second
	// maxRequestBytes caps one request line. The largest known payload is a
	// forwarded environ, kernel-bounded to ~2 MiB per exec, so 8 MiB is
	// generous headroom; anything larger is a bug or abuse and is rejected as
	// undecodable.
	maxRequestBytes = 8 << 20
)

// errAddrInUse classifies a path bound by a live listener inside Listen's
// one-retry flow. Callers need only the returned error and path: every current
// consumer treats any bind failure as fatal, so exporting this would add an API
// nobody uses and force a minor release for an implementation detail.
var errAddrInUse = errors.New("socket already has a live listener")

// Listen binds the unix socket at path with owner-only permissions. It binds
// FIRST and treats EADDRINUSE as a question, because the kernel already supplies
// the single-instance guard: binding a path a live listener owns fails, and
// closing a listener unlinks its own path. Never unlink before binding — that
// steals a LIVE socket from a running daemon, which then keeps serving an
// unreferenced listener in silence. On EADDRINUSE the path is dialled: an answer
// means a live owner (errAddrInUse), silence means a SIGKILLed predecessor, so
// the file is unlinked and the bind retried once, never in a loop.
func Listen(path string) (net.Listener, error) {
	ln, err := bindOwnerOnly(path)
	if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
		return ln, err
	}
	if c, derr := dialProbe(path); derr == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %s", errAddrInUse, path)
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return nil, rerr
	}
	ln, err = bindOwnerOnly(path)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("%w: %s", errAddrInUse, path)
	}
	return ln, err
}

// probeTimeout bounds the liveness dial in Listen. A connect to a unix socket
// with a listening peer completes in the kernel, so this is not a latency budget
// — it is the guard against a dial that never returns taking boot with it.
const probeTimeout = 2 * time.Second

// dialProbe asks whether anything is listening on path. It exists as a named
// function because the answer decides between reclaiming a dead socket and
// refusing a live one, and because a bare net.Dial would carry no timeout.
//
// context.Background is correct here rather than a caller's context: Listen runs
// at single-threaded boot and takes no context (see its umask note), and the
// bound that matters is this probe's own, not the caller's lifetime.
func dialProbe(path string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// bindOwnerOnly binds path and leaves it readable and writable by its owner only.
func bindOwnerOnly(path string) (net.Listener, error) {
	// Narrow the umask so the socket is born owner-only; the Chmod below is then
	// belt-and-braces rather than closing a world-connectable window.
	//
	// The swap is PROCESS-WIDE, so it is safe only while nothing else in the
	// process creates files: a directory born inside this window is drw-------,
	// and its owner can then neither create entries in it nor unlink from it.
	// Callers satisfy that by calling Listen during single-threaded boot; a
	// parallel test suite does not, and root bypasses the check so it hides.
	oldMask := syscall.Umask(0o177)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	syscall.Umask(oldMask)
	if err != nil {
		return nil, err
	}
	// Owner-only: connecting requires write permission on the socket file,
	// which scopes triggering to the container's own user — the same
	// authority boundary `docker exec` already enforces, failing loudly at
	// connect for a mismatched exec user.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// Server accepts run requests and bridges them onto the queue.
//
// The zero value is not usable; set Queue. The hooks are optional: they exist
// so the app can log acceptance and rejection in its own vocabulary and with
// its own payload attributes (the library never logs payload contents — a
// forwarded environment can carry secrets).
type Server[P any] struct {
	// Queue receives every decoded request as an External job.
	Queue *Queue[P]
	// OnAccepted, when non-nil, runs after a request is queued (the app's
	// "triggered run queued" line).
	OnAccepted func(payload P)
	// OnRejected, when non-nil, runs after a submission is rejected with
	// ErrFull or ErrClosed (the app's rejection warning). Undecodable
	// requests never reach it; the library logs those without the payload.
	OnRejected func(payload P, err error)
	// Logger receives the server's own lines (accept failures, undecodable
	// requests, and the queue lines when OnAccepted or OnRejected is nil).
	// Nil means slog.Default at the time of each line.
	Logger *slog.Logger

	// handlers tracks the per-connection goroutines so Serve can wait for
	// every accepted request to receive its final event. Bounded: every
	// submitted job is guaranteed a result, and a not-yet-submitted
	// connection is bounded by requestReadTimeout.
	handlers sync.WaitGroup
}

// Serve accepts run requests on ln until ctx is done, then closes ln and
// waits: it returns once the accept loop has stopped and every accepted
// request has its final event on the wire. Close the queue when ctx is done
// too, so a waiting job is delivered its cancellation; the wait is bounded by
// that guarantee and by the request read and event write timeouts. Serve
// returns nil after ctx ended, and an error matching net.ErrClosed when ln
// was closed first.
func (s *Server[P]) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	s.serve(ln)
	s.handlers.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("trigger: listener closed before the context ended: %w", net.ErrClosed)
}

// log returns the server's logger, or slog.Default when none was set.
func (s *Server[P]) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// serve accepts connections until the listener is closed.
func (s *Server[P]) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log().Warn("trigger socket accept failed", "error", err)
			// A persistent accept error (e.g. fd exhaustion) must not
			// hot-spin: pace retries so the log gets one warn per second, not
			// a flood. Shutdown still exits promptly: ln.Close() makes the
			// next Accept return net.ErrClosed after at most this pause.
			time.Sleep(time.Second)
			continue
		}
		s.handlers.Go(func() {
			defer func() { _ = conn.Close() }()
			s.handle(conn)
		})
	}
}

// handle serves one connection: decode the request, submit it, stream events.
func (s *Server[P]) handle(conn net.Conn) {
	var payload P
	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&payload); err != nil {
		// A bare io.EOF means nothing was sent: Listen's own live-owner probe
		// connects and closes without writing, and so does a client killed
		// before its write. Nothing was requested and nothing was lost, so it
		// is not a rejection. A request whose bytes arrived and were cut short
		// reports io.ErrUnexpectedEOF, so it keeps the warning and the answer.
		if errors.Is(err, io.EOF) {
			s.log().Debug("trigger connection closed without sending a request")
			return
		}
		s.log().Warn("trigger request rejected: undecodable", "error", err)
		writeEvent(s.log(), conn, Event{Kind: EventDone, OK: false, Reason: "undecodable request"})
		return
	}
	j := NewJob(External, payload)
	if err := s.Queue.Submit(j); err != nil {
		if s.OnRejected != nil {
			s.OnRejected(payload, err)
		} else {
			s.log().Warn("trigger request rejected", "reason", err)
		}
		writeEvent(s.log(), conn, Event{Kind: EventDone, OK: false, Reason: err.Error()})
		return
	}
	if s.OnAccepted != nil {
		s.OnAccepted(payload)
	} else {
		s.log().Info("triggered run queued")
	}
	writeEvent(s.log(), conn, Event{Kind: EventQueued})

	relayEvents(s.log(), conn, j)
}

// relayEvents streams the job's lifecycle to the client: a started event if
// the run begins (a job cancelled before starting delivers its result without
// ever starting, so it waits on both), then exactly one final done event.
func relayEvents[P any](l *slog.Logger, conn net.Conn, j *Job[P]) {
	started := j.Started()
	for {
		select {
		case <-started:
			writeEvent(l, conn, Event{Kind: EventStarted})
			started = nil // block this case from now on; wait for the result
		case out := <-j.Result():
			if started != nil {
				// Both channels can be ready in one select round (a fast
				// run), so drain the start signal first: the wire order the
				// protocol documents (queued -> started -> done) must hold
				// for a run that actually started.
				select {
				case <-started:
					writeEvent(l, conn, Event{Kind: EventStarted})
				default:
				}
			}
			writeEvent(l, conn, Event{
				Kind:       EventDone,
				OK:         out.OK,
				DurationMs: out.Duration.Milliseconds(),
				Reason:     out.Reason,
			})
			return
		}
	}
}

// writeEvent sends one status line, best-effort: a departed client only
// forfeits its own visibility (the run itself is daemon-owned and its result
// delivery never blocks on the connection).
func writeEvent(l *slog.Logger, conn net.Conn, ev Event) {
	_ = conn.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
	if err := json.NewEncoder(conn).Encode(ev); err != nil {
		l.Debug("trigger event write failed (client gone?)", "event", ev.Kind, "error", err)
	}
}
