package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
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
	// every accepted request to receive its final event. Bounded once the
	// executor finishes every submitted job, and a not-yet-submitted
	// connection by requestReadTimeout.
	handlers sync.WaitGroup
}

// Serve accepts run requests on ln until ctx is done, then closes ln and waits
// for every connection it accepted; it blocks, so start it in its own goroutine.
// It trusts every connection ln accepts: bind ln with ownersock.Listen, which
// admits only the server's own user. A connection ends after its job's final
// event, written best-effort, or at the request read timeout. Only the executor
// finishes jobs, so close the queue and let Execute return before waiting on
// Serve. Serve returns nil when ctx ended, and an error matching net.ErrClosed
// when ln was closed before ctx ended. Call Serve once per Server.
func (s *Server[P]) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	s.serve(ln)
	// Read before the drain, so a ctx that ends while handlers finish does
	// not turn a listener closed first into a clean return.
	ctxEnded := ctx.Err() != nil
	s.handlers.Wait()
	if ctxEnded {
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
		// A bare io.EOF means nothing was sent: a second start's liveness probe
		// (ownersock.Listen dials a contested path) and a client killed before
		// its write both connect and close without writing. Nothing was lost, so
		// it is not a rejection. A request cut short reports
		// io.ErrUnexpectedEOF, so it keeps the warning and the answer.
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
