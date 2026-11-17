package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/cplieger/go/textsafe"
)

// The ops listener's fixed tunables. Its two routes each answer one short
// response, so the whole request is bounded, unlike New's streaming-safe
// defaults.
const (
	opsReadTimeout  = 10 * time.Second
	opsWriteTimeout = 30 * time.Second
	// opsShutdownGrace starts only once every product listener has drained,
	// so it adds to their grace and stays short.
	opsShutdownGrace = time.Second
)

// opsBindRetry is how ListenOps retries EADDRINUSE: a restarted container's
// previous process can still hold the port for a moment.
var opsBindRetry = struct{ interval, window time.Duration }{250 * time.Millisecond, 5 * time.Second}

// maxErrorValue bounds a configured value quoted in an error.
const maxErrorValue = 64

// OpsAddr is the ops address from OPS_LISTEN_ADDR's raw value: edge spaces and
// invisible runes cut, and blank, the variable's default, is def, the app's
// documented ops address. A def of "" makes the listener opt-in.
func OpsAddr(value, def string) string {
	if v := strings.TrimFunc(value, edgeRune); v != "" {
		return v
	}
	return def
}

// edgeRune reports a rune a pasted value can carry at its edges that
// net.Listen cannot resolve; strings.TrimSpace keeps zero-width runes.
func edgeRune(r rune) bool { return unicode.IsSpace(r) || invisibleRune(r) }

// OpsConfig configures ListenOps.
type OpsConfig struct {
	// Metrics answers GET /metrics; required.
	Metrics http.Handler
	// Healthz answers GET /healthz; required.
	Healthz http.Handler
	// Errors writes every refusal; nil is WriteError.
	Errors ErrorResponder
	// Logger takes the access lines, refusals and recovered panics; nil is
	// slog.Default().
	Logger *slog.Logger
	// Setting names the setting Addr came from ("OPS_LISTEN_ADDR") in errors.
	Setting string
	// Addr is OpsAddr's answer; "" opens nothing.
	Addr string
	// Hosts are the host names answered besides loopback's.
	Hosts []string
}

// Ops is a bound ops listener. Serve it with RunWithOps.
type Ops struct {
	srv     *http.Server
	ln      net.Listener
	logger  *slog.Logger
	release func() bool
	cause   func() error
	setting string
	claimed atomic.Bool
}

// ListenOps binds the ops listener before it returns: GET and HEAD on /healthz
// and /metrics only, under WithProbeLogLevel and Recoverer, with a 10 s read
// and 30 s write timeout, refusals through Errors, and a Host outside Hosts
// refused unless LoopbackRequest holds. EADDRINUSE is retried every 250 ms for
// up to 5 s; another bind error fails at once, naming Setting and the cleaned
// address. A nil handler is an error even with the listener off. The listener
// closes when ctx ends before RunWithOps takes it. docs/server.md has the rest.
func ListenOps(ctx context.Context, c OpsConfig) (*Ops, error) { //nolint:gocritic // hugeParam: read once at construction, and a value keeps the caller's config unshared
	if c.Metrics == nil || c.Healthz == nil {
		return nil, errors.New("httpserver: ListenOps needs non-nil Metrics and Healthz handlers")
	}
	if c.Addr == "" {
		return &Ops{}, nil
	}
	errs := c.Errors
	if errs == nil {
		errs = WriteError
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hosts, err := opsHostPolicy(c.Hosts, errs, logger)
	if err != nil {
		return nil, fmt.Errorf("httpserver: %s: %w", c.Setting, err)
	}
	ln, err := bindOps(ctx, c.Setting, c.Addr)
	if err != nil {
		return nil, err
	}
	srv := New(Chain(opsRoutes(c.Metrics, c.Healthz, errs),
		Logging(WithLogger(logger), WithProbeLogLevel("/healthz", "/metrics")),
		Recoverer(WithRecoverLogger(logger), WithRecoverResponder(errs)),
		hosts.Middleware()))
	srv.ReadTimeout = opsReadTimeout
	srv.WriteTimeout = opsWriteTimeout
	return &Ops{
		srv:     srv,
		ln:      ln,
		logger:  logger,
		setting: c.Setting,
		release: context.AfterFunc(ctx, func() { _ = ln.Close() }),
		cause:   func() error { return context.Cause(ctx) },
	}, nil
}

// Addr returns the bound address, which names the port the kernel chose for a
// ":0" address, or nil when the listener is off.
func (o *Ops) Addr() net.Addr {
	if o == nil || o.ln == nil {
		return nil
	}
	return o.ln.Addr()
}

// opsHostPolicy is the ops listener's host check. It is active even over no
// hosts, so an empty list admits loopback alone rather than every name.
func opsHostPolicy(hosts []string, errs ErrorResponder, logger *slog.Logger) (*HostPolicy, error) {
	p, invalid := ParseHostList(hosts, WithLoopbackExempt(true), WithHostResponder(errs), WithHostAllowlistLogger(logger))
	if len(invalid) > 0 {
		for i, v := range invalid {
			invalid[i] = textsafe.SanitizeSingleLineBounded(v, maxErrorValue)
		}
		return nil, fmt.Errorf("hosts no Host can match: %q", invalid)
	}
	p.active = true
	return p, nil
}

func opsRoutes(metrics, healthz http.Handler, errs ErrorResponder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var h http.Handler
		switch r.URL.Path {
		case "/metrics":
			h = metrics
		case "/healthz":
			h = healthz
		default:
			errs(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			errs(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func bindOps(ctx context.Context, setting, addr string) (net.Listener, error) {
	deadline := time.Now().Add(opsBindRetry.window)
	var lc net.ListenConfig
	for {
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, listenError(setting, addr, err)
		}
		t := time.NewTimer(opsBindRetry.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, listenError(setting, addr, context.Cause(ctx))
		case <-t.C:
		}
	}
}

func listenError(setting, addr string, err error) error {
	return fmt.Errorf("httpserver: %s %q: %w", setting, textsafe.SanitizeSingleLineBounded(addr, maxErrorValue), cleanError{err})
}

// cleanError carries err with its text cleaned: a resolver's error quotes the
// raw address, which may hold a control character.
type cleanError struct{ err error }

func (e cleanError) Error() string { return textsafe.SanitizeSingleLineBounded(e.err.Error(), 256) }
func (e cleanError) Unwrap() error { return e.err }

// Served is a product listener RunWithOps serves with Run and Options.
type Served struct {
	Server   *http.Server
	Listener net.Listener
	Options  []RunOption
}

// RunWithOps serves every Served with Run, and ops beside them, until ctx ends
// or one fails, which stops the rest; every error is joined. The ops listener
// stops only after every product Run has returned, so /healthz answers through
// their drain, then within its own 1 s grace, a request cut there logged at
// WARN, not returned. Either side may be empty; with neither it returns nil.
// It owns every listener given, closing them when ListenOps's context ended
// first. It panics on a Served with a nil Server or Listener.
func RunWithOps(ctx context.Context, ops *Ops, served ...Served) error {
	for i, s := range served {
		if s.Server == nil || s.Listener == nil {
			panic(fmt.Sprintf("httpserver: RunWithOps needs a non-nil Server and Listener in served[%d]", i))
		}
	}
	on := ops != nil && ops.ln != nil
	if on {
		if !ops.claimed.CompareAndSwap(false, true) {
			closeServed(served)
			return errors.New("httpserver: RunWithOps already served this Ops")
		}
		if !ops.release() {
			// AfterFunc closes ln in its own goroutine, so close it here to have it closed on return.
			_ = ops.ln.Close()
			closeServed(served)
			return fmt.Errorf("httpserver: %s closed before serving: %w", ops.setting, ops.cause())
		}
	} else if len(served) == 0 {
		return nil
	}

	productCtx, stopProducts := context.WithCancel(ctx)
	defer stopProducts()
	opsCtx, stopOps := context.WithCancel(context.WithoutCancel(ctx))
	defer stopOps()
	opsDone := make(chan error, 1)
	if on {
		go func() {
			opsDone <- Run(opsCtx, ops.srv, ops.ln, WithShutdownGrace(opsShutdownGrace),
				WithServeExit(func(context.Context) { stopProducts() }))
		}()
	}
	errs := make([]error, len(served), len(served)+1)
	var wg sync.WaitGroup
	for i, s := range served {
		wg.Go(func() {
			errs[i] = Run(productCtx, s.Server, s.Listener, s.Options...)
			stopProducts()
		})
	}
	if len(served) == 0 {
		<-productCtx.Done()
	}
	wg.Wait()
	if on {
		stopOps()
		errs = append(errs, ops.stopped(<-opsDone))
	}
	return errors.Join(errs...)
}

func closeServed(served []Served) {
	for _, s := range served {
		_ = s.Listener.Close()
	}
}

// stopped turns the ops Run's result into RunWithOps's: a grace expiry closes
// the connections it left and is warned about, never returned.
func (o *Ops) stopped(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrShutdownGraceExpired):
		_ = o.srv.Close()
		o.logger.Warn("httpserver: ops requests still in flight at the end of the ops listener's grace were cut off",
			"setting", o.setting, "addr", o.ln.Addr().String(), "grace", opsShutdownGrace.String())
		return nil
	}
	return fmt.Errorf("httpserver: %s on %s stopped: %w", o.setting, o.ln.Addr(), err)
}
