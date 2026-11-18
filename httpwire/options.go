package httpwire

import (
	"log/slog"
	"net/http"

	"github.com/cplieger/go/httpserver/v2"
)

// Option configures New. When an option is given twice, the last one wins.
type Option func(*options)

type options struct {
	guard       Guard
	feed        feedPosition
	keyRate     *httpserver.KeyedRateLimiter
	keyOf       func(*http.Request) (string, bool)
	credential  func(*http.Request) string
	cursorOwner func(*http.Request) string
	outage      func() string
	hold        func(http.Handler) http.Handler
	floor       *ClientFloor
	cursorKey   *[32]byte
	logger      *slog.Logger
	spoolDir    string
	schema      []byte
	idem        IdempotencyOptions
	keyRateSet  bool
	schemaSet   bool
}

type feedPosition interface {
	Position(r *http.Request, kinds []string) string
}

// WithSchema hands New the table's committed OpenAPI document, which every
// request is checked against, httpwiretest.Contract checks answers against,
// and which gives the wire version. New requires it.
func WithSchema(doc []byte) Option {
	return func(o *options) { o.schema, o.schemaSet = doc, true }
}

// WithGuard sets the guard that admits each row's principal; nil is none,
// and New then refuses every row whose class is not Public.
func WithGuard(g Guard) Option {
	return func(o *options) { o.guard = g }
}

// WithCredential names the session or key a request came with, which
// idempotency keys, sealed handles and the held-request cap are scoped to.
// New requires it when a row's class is not Public.
func WithCredential(fn func(*http.Request) string) Option {
	return func(o *options) { o.credential = fn }
}

// WithCursorOwner names the owner a list cursor is bound to: one answer for
// every session of one person. New requires it when a row's class is not
// Public.
func WithCursorOwner(fn func(*http.Request) string) Option {
	return func(o *options) { o.cursorOwner = fn }
}

// WithKeyRate takes one token from l per admitted request whose principal
// key answers as an API key. New requires it when a row's class admits a
// key, and refuses it with a nil l or key.
func WithKeyRate(l *httpserver.KeyedRateLimiter, key func(*http.Request) (string, bool)) Option {
	return func(o *options) { o.keyRate, o.keyOf, o.keyRateSet = l, key, true }
}

// WithHold wraps every Held row's handler and every Keyed row's join waiter,
// counting each against the held-request cap. New requires it for a Held
// or Keyed row.
func WithHold(fn func(next http.Handler) http.Handler) Option {
	return func(o *options) { o.hold = fn }
}

// ClientFloor refuses an installed client below this server's floor, read
// from the table's ClientHeader.
type ClientFloor struct {
	// Below reports whether a client version is under the floor.
	Below func(version string) bool
	// Message is the one line naming this server's version and the client
	// release to install.
	Message func(version string) string
}

// WithClientFloor turns on the client version floor. New requires it
// exactly when the table names a ClientHeader, with both functions set.
func WithClientFloor(f ClientFloor) Option {
	return func(o *options) { o.floor = &f }
}

// WithSpoolDir names the private directory a Keyed Raw row's body is spooled
// in; New requires it for such a row. New removes the regular files in dir
// named httpwire-spool-*, which a crash left, and nothing else; it refuses a
// dir it cannot read.
func WithSpoolDir(dir string) Option {
	return func(o *options) { o.spoolDir = dir }
}

// WithFeed sets the change feed whose position seeds Feed-Position on every
// 2xx of a row with Changes. New requires it for such a row.
func WithFeed(f interface {
	Position(r *http.Request, kinds []string) string
},
) Option {
	return func(o *options) { o.feed = f }
}

// WithLogger names where the binder's lines go. Without it, each line goes
// to slog.Default() as it is when the line is written.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithOutage reports, per request, which file cannot open: "accounts",
// "store" or "" when both are open. While it names one, every row not
// marked DuringOutage answers accounts_unavailable or store_unavailable
// before its guard runs; a mount is refused only while the accounts file is
// out. Any other answer is a wiring bug, answered internal_error. nil is
// no outage.
func WithOutage(fn func() string) Option {
	return func(o *options) { o.outage = fn }
}
