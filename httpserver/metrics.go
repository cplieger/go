package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// The metrics listener's timeouts. Both of its routes answer in one short
// response, so the whole request is bounded, unlike New's streaming-safe
// defaults.
const (
	metricsReadTimeout  = 10 * time.Second
	metricsWriteTimeout = 30 * time.Second
)

// errAlreadyServedOrClosed is Serve's answer once the server was served or
// closed.
var errAlreadyServedOrClosed = errors.New("httpserver: MetricsServer already served or closed")

// errCloseAfterServe is Close's answer once Serve took the listener; only
// cancelling Serve's context stops a running Serve.
var errCloseAfterServe = errors.New("httpserver: MetricsServer.Close after Serve; cancel Serve's context to stop it")

// MetricsHandlers names the two handlers the metrics listener serves, so they
// cannot be passed in each other's place.
type MetricsHandlers struct {
	// Metrics answers GET /metrics.
	Metrics http.Handler
	// Healthz answers GET /healthz.
	Healthz http.Handler
}

// MetricsServer is the opt-in listener of an app that opens no port of its own:
// it routes only /metrics and /healthz, for GET and the HEAD a GET pattern
// matches, both under WithProbeLogLevel, with panics recovered. Build it with
// ListenMetrics, which binds before the app starts work, then call Serve.
type MetricsServer struct {
	srv *http.Server
	ln  net.Listener
	// state is which call claimed the listener: stateIdle, stateServed or
	// stateClosed.
	state atomic.Uint32
}

// The MetricsServer states.
const (
	stateIdle uint32 = iota
	stateServed
	stateClosed
)

// ListenMetrics binds addr, a net.Listen "tcp" address, before it returns and
// serves h once Serve runs. A bind failure is returned naming addr. An empty
// addr opens nothing: Serve then returns nil at once and Addr is nil.
// ListenMetrics reads no environment, and opts reach New. It panics on a nil
// handler, whatever addr is, so the mistake shows with the listener off too.
func ListenMetrics(ctx context.Context, addr string, h MetricsHandlers, opts ...ServerOption) (*MetricsServer, error) {
	if h.Metrics == nil {
		panic("httpserver: ListenMetrics needs a non-nil MetricsHandlers.Metrics")
	}
	if h.Healthz == nil {
		panic("httpserver: ListenMetrics needs a non-nil MetricsHandlers.Healthz")
	}
	if addr == "" {
		return &MetricsServer{}, nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("httpserver: listen on %q: %w", addr, err)
	}
	// A GET pattern also matches HEAD; any other method gets 405.
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h.Metrics)
	mux.Handle("GET /healthz", h.Healthz)
	srv := New(Chain(mux, Logging(WithProbeLogLevel("/metrics", "/healthz")), Recoverer()), opts...)
	srv.ReadTimeout = metricsReadTimeout
	srv.WriteTimeout = metricsWriteTimeout
	return &MetricsServer{srv: srv, ln: ln}, nil
}

// Addr returns the bound address, which names the port the kernel chose for
// a ":0" address, or nil when the listener is off.
func (m *MetricsServer) Addr() net.Addr {
	if m.ln == nil {
		return nil
	}
	return m.ln.Addr()
}

// Serve serves until ctx is cancelled, then shuts down as Run does with opts,
// and returns Run's error. When the listener is off it returns nil at once. A
// call after Serve or Close, on a listener that is on, returns an error and
// serves nothing.
func (m *MetricsServer) Serve(ctx context.Context, opts ...RunOption) error {
	if m.srv == nil {
		return nil
	}
	if !m.state.CompareAndSwap(stateIdle, stateServed) {
		return errAlreadyServedOrClosed
	}
	return Run(ctx, m.srv, m.ln, opts...)
}

// Close releases a listener Serve has not started, for a caller that gives up
// before serving, such as a startup that fails after ListenMetrics. Once
// Serve has started it does nothing and returns an error: cancel Serve's
// context to stop the server. It returns nil when the listener is off, and
// for a repeated Close.
func (m *MetricsServer) Close() error {
	if m.ln == nil {
		return nil
	}
	if m.state.CompareAndSwap(stateIdle, stateClosed) {
		return m.ln.Close()
	}
	if m.state.Load() == stateClosed {
		return nil
	}
	return errCloseAfterServe
}
