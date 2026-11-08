package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// ErrShutdownGraceExpired marks a Run return whose origin was the shutdown
// grace period running out, so a caller can tell WHICH DeadlineExceeded it is
// holding.
//
// Run's error is one value with two possible origins: a serve error carrying a
// deadline of the caller's own making, or the graceful sequence outliving the
// single shutdown grace. Both can satisfy
// errors.Is(err, context.DeadlineExceeded), and a caller that assumes the
// second is asserting something the value alone cannot prove. A grace-expiry
// return is wrapped so both hold:
//
//	errors.Is(err, httpserver.ErrShutdownGraceExpired) // the origin: the grace ran out
//	errors.Is(err, context.DeadlineExceeded)        // still true, unchanged
//
// The diagnosis itself stays the caller's: naming the grace constant to raise,
// deciding the log level, and choosing the exit code are app policy.
var ErrShutdownGraceExpired = errors.New("httpserver: shutdown grace period expired")

// Default server and shutdown tunables.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	// defaultMaxHeaderBytes deliberately restates net/http's own 1 MiB default
	// rather than tightening it like the two timeouts above. Shortening a timeout
	// can only end a connection that was already slow or idle, and the symptom is
	// a closed connection; a smaller size cap REJECTS a well-formed request with a
	// 431 the handler never sees, which is both surprising and hard to trace back
	// to a library default. This library's consumers are heterogeneous — some are
	// browser-facing with cookies and large auth headers, some are machine-only
	// APIs — so the right ceiling is per app: assign srv.MaxHeaderBytes.
	defaultMaxHeaderBytes = 1 << 20
	defaultShutdownGrace  = 5 * time.Second
)

// ServerOption configures the *http.Server built by New. Only options that
// add a capability exist; every other field is a plain assignment on the
// returned server (see New).
type ServerOption func(*http.Server)

// WithSlogErrorLog sets http.Server.ErrorLog to a bridge that forwards
// net/http's own connection-level lines into slog at level, so they arrive as
// level-carrying records in an otherwise structured stream instead of as
// unstructured, level-less standard-logger output that no level-based log rule
// can match. The lines it covers are net/http's, above all
// "http: Accept error: ...; retrying" — the trace of an exhausted fd budget,
// which is a whole-service outage no request-scoped log will report.
//
// The LEVEL is the caller's policy and deliberately has no default here,
// because consumers legitimately disagree: an accept failure is fatal to a
// service whose only job is to answer probes (Error) and a degradation to one
// that will retry (Warn). This option only gives that choice a named home
// instead of three hand-written copies of the same slog.NewLogLogger recipe.
//
// It resolves slog.Default() when the option is APPLIED (inside New), so
// install the process logger before building the server; a later
// slog.SetDefault does not retroactively re-target an already-built
// http.Server. Any other *log.Logger is a plain assignment to srv.ErrorLog,
// and the default (net/http's standard logger) is unchanged when neither is
// set.
func WithSlogErrorLog(level slog.Level) ServerOption {
	return func(s *http.Server) {
		s.ErrorLog = slog.NewLogLogger(slog.Default().Handler(), level)
	}
}

// New builds an *http.Server for handler with streaming-safe defaults:
// ReadHeaderTimeout 10s (a slowloris guard), IdleTimeout 120s, MaxHeaderBytes
// 1 MiB, and ReadTimeout/WriteTimeout left unset (0) so SSE, WebSocket, and
// other long-lived responses work out of the box. Assign a field of the
// returned server to change a default.
//
// Because ReadTimeout and WriteTimeout are unset by default, only header
// reading is time-bounded (by ReadHeaderTimeout); a slow request BODY is not.
// A non-streaming handler should set srv.ReadTimeout to bound slowloris-style
// slow bodies; WriteTimeout must stay unset for a streaming one, because it
// would cut off an in-progress stream. A streaming handler, which cannot use
// a whole-request timeout, should instead apply per-request deadlines via
// http.ResponseController.SetReadDeadline/SetWriteDeadline. Note that
// MaxBytesReader (see LimitBody) bounds body SIZE, not the time taken to send
// it.
//
// # Fields left to the caller, and one that logs nothing
//
// New returns the *http.Server, so every field is reachable with a plain
// assignment and gets no option here — an option that only assigns a field of
// the value the constructor hands back adds surface and no capability, which
// is why WithSlogErrorLog, which builds a bridge, is the one ServerOption.
// Three fields worth knowing about on Go 1.27:
//
//   - Server.MaxHeaderValueCount caps how many header VALUES a request may
//     carry, defaulting to http.DefaultMaxHeaderValueCount (500). Set it
//     directly: srv.MaxHeaderValueCount = 64. Measured on go1.27.0, and the
//     reason it is called out rather than merely left alone: the 431 is
//     answered BELOW the handler, so a request refused by this cap produces NO
//     access-log line from Logging and NO headers from SecurityHeaders — a
//     refusal class an operator watching httpserver's access log cannot see. Cap
//     header count at a proxy, or alert on the server's own error log, if that
//     matters. (MaxHeaderBytes, which New does set, refuses the same way.)
//   - Server.DisableClientPriority = true restores round-robin HTTP/2 stream
//     scheduling in place of Go 1.27's RFC 9218 client-priority handling. This
//     library expresses no opinion: priority is a property of the workload, not
//     of the plumbing, and New leaves Protocols nil so HTTP/2 arrives
//     only over TLS anyway.
//   - Server.Protocols is nil, which is net/http's default set and does NOT
//     include unencrypted HTTP/2. That is what keeps the ReadHeaderTimeout
//     guard above in force: Go 1.27 clears the header deadline after accepting
//     an unencrypted h2 connection, and that path is unreachable unless a
//     caller opts in by setting Protocols with UnencryptedHTTP2.
func New(handler http.Handler, opts ...ServerOption) *http.Server {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}
	for _, o := range opts {
		if o != nil {
			o(srv)
		}
	}
	return srv
}

// runConfig holds resolved Run configuration.
type runConfig struct {
	preDrain      func(ctx context.Context)
	teardown      func(ctx context.Context)
	serveExit     func(ctx context.Context)
	shutdownGrace time.Duration
}

// RunOption configures Run.
type RunOption func(*runConfig)

// WithShutdownGrace sets how long Run allows for graceful shutdown: the window
// for the pre-drain hook to run, for in-flight requests to finish, and for the
// WithTeardown hook to run. Defaults to 5s.
func WithShutdownGrace(d time.Duration) RunOption {
	return func(c *runConfig) { c.shutdownGrace = d }
}

// WithPreDrain registers a hook Run invokes after ctx is cancelled and
// strictly before srv.Shutdown starts draining in-flight requests. It is the
// place for the release-the-streams phase a graceful stop needs ahead of the
// drain: flip a readiness gate so a load balancer stops routing here, cancel
// the server's BaseContext or shut down an SSE hub so long-lived connections
// end (otherwise Shutdown waits its full grace period for them). fn receives
// a context bounded by the SAME shutdown deadline that Shutdown and
// WithTeardown share; whatever budget it spends is no longer available to
// them. A nil fn is ignored.
func WithPreDrain(fn func(ctx context.Context)) RunOption {
	return func(c *runConfig) {
		if fn != nil {
			c.preDrain = fn
		}
	}
}

// WithTeardown registers the hook Run invokes after srv.Shutdown has drained
// in-flight requests, on the graceful path only: the place to close what the
// handlers used (a store, a hub, a background loop). fn receives a context
// bounded by the same shutdown deadline as WithPreDrain and Shutdown. A nil fn
// is ignored.
func WithTeardown(fn func(ctx context.Context)) RunOption {
	return func(c *runConfig) {
		if fn != nil {
			c.teardown = fn
		}
	}
}

// WithServeExit registers an opt-in teardown hook Run invokes when srv.Serve
// returns on its own — before ctx is ever cancelled — in place of the graceful
// sequence. That covers a fatal serve error (the listener or the accept loop is
// gone) and a Shutdown or Close the caller drove itself outside Run. Exactly
// one of the two paths runs per Run call: with the hook registered, either
// pre-drain -> Shutdown -> teardown ran, or fn did. Without it, Run keeps
// today's behavior of returning the serve error with NEITHER hook invoked, so
// an existing caller sees no change. A nil fn is ignored.
//
// fn receives a context bounded by the shutdown grace period, and gets the
// whole budget: there was no drain to share it with. Run does NOT call
// srv.Shutdown on this path — Serve has already returned and closed the
// listener, so a drain would only spend fn's budget waiting on connections
// whose accept loop is already dead.
//
// WithPreDrain and WithTeardown are NOT reused here, because both are documented
// against a graceful stop that this path is not: ctx is still LIVE (nothing
// cancelled it), and there is no drain for a pre-drain phase to precede. That
// matters for what fn must do — a teardown that waits on a goroutine keyed to
// ctx (a background loop stopped by the same signal context) has to cancel it
// inside fn, by calling the signal context's stop function, or it waits out the
// whole grace for a goroutine nothing has asked to stop. A caller whose
// teardown is cancellation-independent can pass the same function to both:
// Run(ctx, srv, ln, WithTeardown(teardown), WithServeExit(teardown)).
func WithServeExit(fn func(ctx context.Context)) RunOption {
	return func(c *runConfig) {
		if fn != nil {
			c.serveExit = fn
		}
	}
}

// Run serves srv on ln until ctx is cancelled, then shuts down within one
// grace period (WithShutdownGrace): the WithPreDrain hook, srv.Shutdown, then
// the WithTeardown hook, all bounded by the same deadline. When Serve returns
// before ctx is cancelled, only the WithServeExit hook runs. Run returns a
// serve error first, else the shutdown error (see ErrShutdownGraceExpired), and
// treats http.ErrServerClosed as a clean stop. It sets srv.ConnState, calling
// any hook already set first, and closes connections that have sent no request
// once srv.Shutdown starts. Bind ln first so a port-in-use error precedes Run.
// srv and ln must be non-nil; Run panics on either before serving.
func Run(ctx context.Context, srv *http.Server, ln net.Listener, opts ...RunOption) error {
	if srv == nil || ln == nil {
		// Checked here so the panic is the caller's to recover: a nil ln would
		// otherwise panic inside Serve's goroutine and kill the process.
		panic("httpserver: Run needs a non-nil srv and ln")
	}
	c := &runConfig{shutdownGrace: defaultShutdownGrace}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}

	silent := &silentConns{conns: make(map[net.Conn]struct{})}
	callerHook := srv.ConnState
	srv.ConnState = func(conn net.Conn, state http.ConnState) {
		if callerHook != nil {
			callerHook(conn, state)
		}
		silent.track(conn, state)
	}
	srv.RegisterOnShutdown(silent.closeAll)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// Serve returned before shutdown was requested (a fatal serve error;
		// ErrServerClosed was already normalized to nil). The graceful sequence
		// does not run here: ctx is still live and the listener is already
		// gone, so neither the pre-drain phase nor a drain has any meaning.
		// Only the opt-in serve-exit hook gets a turn, on its own full grace
		// budget (see WithServeExit).
		if c.serveExit != nil {
			exitCtx, exitCancel := context.WithTimeout(context.Background(), c.shutdownGrace)
			c.serveExit(exitCtx)
			exitCancel()
		}
		return err
	case <-ctx.Done():
	}

	deadline := time.Now().Add(c.shutdownGrace)
	if c.preDrain != nil {
		preCtx, preCancel := context.WithDeadline(context.Background(), deadline)
		c.preDrain(preCtx)
		preCancel()
	}
	shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)

	if c.teardown != nil {
		teardownCtx, teardownCancel := context.WithDeadline(context.Background(), deadline)
		defer teardownCancel()
		c.teardown(teardownCtx)
	}

	// Serve returns ErrServerClosed (normalized to nil) once Shutdown runs. A
	// real serve error takes precedence over the shutdown error.
	if err := <-serveErr; err != nil {
		return err
	}
	return graceExpiryError(shutdownErr)
}

// silentConns holds the connections still in http.StateNew, which
// http.Server.Shutdown counts as active for 5 s
// (https://go.dev/issue/22682), so that shutdown can close them at once.
type silentConns struct {
	conns    map[net.Conn]struct{}
	mu       sync.Mutex
	shutdown bool
}

func (s *silentConns) track(conn net.Conn, state http.ConnState) {
	s.mu.Lock()
	// After closeAll has swept the set, Serve can still report a connection it
	// accepted just before its listener closed.
	lateNew := state == http.StateNew && s.shutdown
	switch {
	case lateNew:
	case state == http.StateNew:
		s.conns[conn] = struct{}{}
	default:
		delete(s.conns, conn)
	}
	s.mu.Unlock()
	if lateNew {
		_ = conn.Close()
	}
}

func (s *silentConns) closeAll() {
	s.mu.Lock()
	s.shutdown = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for conn := range conns {
		_ = conn.Close()
	}
}

// graceExpiryError marks a shutdown error that IS the grace period running out,
// so Run's caller can identify that origin with errors.Is instead of inferring
// it from a bare context.DeadlineExceeded (see ErrShutdownGraceExpired).
//
// It is a pure addition to the return contract: the original error stays in the
// chain as the second %w, so every errors.Is / errors.As check a caller already
// performs against it (context.DeadlineExceeded above all) keeps holding, and a
// shutdown error of any other kind — or none — passes through untouched.
func graceExpiryError(shutdownErr error) error {
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return shutdownErr
	}
	return fmt.Errorf("%w: %w", ErrShutdownGraceExpired, shutdownErr)
}

// AwaitDone blocks until done is closed or ctx expires, and reports whether
// done closed in time. It is the bounded WAIT a teardown needs, not the
// teardown itself: shutdown teardown bodies stay app-owned, and so does every
// policy around the result — what to log, at which level, whether to name the
// grace constant, whether to count it.
//
// It deliberately creates NO timeout of its own and logs nothing. Run hands
// the WithTeardown hook a context carrying whatever remains of the ONE
// shutdown grace after the pre-drain phase and the HTTP drain have spent
// their share, so a fresh deadline invented here would hand the teardown a
// budget the shutdown sequence does not actually have.
//
// The post-expiry recheck is the reason this is worth sharing. A drain that
// consumed the whole grace hands the teardown an ALREADY-EXPIRED context, and a
// select whose cases are both ready picks pseudo-randomly — so the naive
// two-case select reports a goroutine that DID finish as still running a
// fraction of the time, turning a clean drain into a spurious warning. After
// ctx fires, AwaitDone re-checks done and only then reports false.
//
// A nil done channel never becomes ready, so the wait is then bounded by ctx
// alone.
func AwaitDone(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-ctx.Done():
	}
	// ctx has expired, but both cases above may have become ready together, in
	// which case the select's choice was arbitrary. Completion wins: a teardown
	// that finished exactly as the budget ran out finished.
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// CausedByCancellation reports whether err is the observable form of THIS
// context's cancellation, so a boundary can tell a routine stop apart from a
// fault that merely happened at the same moment.
//
// It PROVES the match rather than assuming it. A cancelled context alone is not
// evidence: a listener bind that genuinely failed while a signal was arriving
// must still read as a bind failure, not as a clean stop — the classification
// mistake this predicate exists to prevent. So err must carry either the stable
// ctx.Err() or the cancellation cause.
//
// Matching context.Cause(ctx) as well as ctx.Err() is what makes it usable at a
// net/http boundary: a cause passed to context.WithCancelCause need not wrap
// context.Canceled, and net/http reports a cancelled operation as the cause
// verbatim, so a ctx.Err()-only check misses it. A nil ctx, a nil err, or a
// context that is not cancelled all report false.
//
// What to DO with a true answer stays the caller's policy — the log level, the
// message, the exit code, and whether the operation is retried are none of the
// library's business.
func CausedByCancellation(ctx context.Context, err error) bool {
	if ctx == nil || err == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, ctx.Err()) || errors.Is(err, context.Cause(ctx))
}
