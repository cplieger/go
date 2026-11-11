package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// The metrics listener's timeouts. Both of its routes answer in one short
// response, so the whole request is bounded, unlike New's streaming-safe
// defaults.
const (
	metricsReadTimeout  = 10 * time.Second
	metricsWriteTimeout = 30 * time.Second
)

// MetricsServer is the opt-in listener of an app that opens no port of its
// own: it serves exactly GET /metrics and GET /healthz on one address, with
// both paths logged at Debug (WithProbeLogLevel) and panics recovered. Build
// it with ListenMetrics, which binds before the app starts work, then call
// Serve.
type MetricsServer struct {
	srv *http.Server
	ln  net.Listener
}

// ListenMetrics binds addr, a net.Listen "tcp" address, at once and returns a
// MetricsServer for metrics at GET /metrics and healthz at GET /healthz. A
// bind failure is returned naming addr before anything is served, so the
// caller can refuse to start. An empty addr opens nothing: the returned
// server's Serve returns nil at once and its Addr is nil, so the caller needs
// no branch for a listener that is off. ListenMetrics reads no environment;
// the app owns its setting's name, parsing and trimming.
//
// metrics and healthz must be non-nil, typically a measure Registry's Handler
// and a health CheckHandler; ListenMetrics panics on either, whatever addr is,
// so the mistake shows with the listener off too.
func ListenMetrics(ctx context.Context, addr string, metrics, healthz http.Handler) (*MetricsServer, error) {
	if metrics == nil || healthz == nil {
		panic("httpserver: ListenMetrics needs non-nil metrics and healthz handlers")
	}
	if addr == "" {
		return &MetricsServer{}, nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("httpserver: listen on %q: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.Handle("GET /healthz", healthz)
	srv := New(Chain(mux, Logging(WithProbeLogLevel("/metrics", "/healthz")), Recoverer()))
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
// and returns Run's error. When the listener is off it returns nil at once.
// Call it once.
func (m *MetricsServer) Serve(ctx context.Context, opts ...RunOption) error {
	if m.srv == nil {
		return nil
	}
	return Run(ctx, m.srv, m.ln, opts...)
}
