package httpwire

import (
	"net/http"
	"reflect"
	"time"
)

// Endpoint is one route of an app's JSON API. The binder, the guard, the
// client generator and the change feed all read it; none adds a field of its
// own beside it.
type Endpoint struct {
	Params, Query, Request, Response reflect.Type // nil is none
	// Headers is a struct of the request headers the row takes, each field
	// naming its header in a header tag; nil is none.
	Headers reflect.Type

	// Reasons maps a code of the row's union to the reasons it may carry.
	Reasons map[string]ReasonSet

	// Name is "<area>.<verb>", unique in the table: the operation id and the
	// generated client function's name.
	Name string
	// Method is GET, POST, PUT, PATCH or DELETE; a GET row also answers HEAD.
	Method string
	// Path is relative to the table's Prefix, which New prepends: "/" then
	// literal segments and whole-segment {name} wildcards.
	Path        string
	Summary     string
	Description string
	// ContentType is a Raw row's 2xx media type, required there and only there.
	ContentType string
	// Result names the read row that returns a Keyed row's result, for an
	// answer too large to replay.
	Result string

	// Successes are the success statuses the row answers besides Status. A
	// JSON row's are a 2xx other than 205 with a Response or a 204 with none;
	// a Raw or Stream row's only a 204 with none; a Redirect row's other
	// redirect statuses with none.
	Successes []Success
	// Codes are the codes the handler answers itself.
	Codes []string
	// Owners are the owner code sets mapError tries, in order.
	Owners []*Owner
	// Changes names the feed kinds this read serves; empty on a write.
	Changes []string
	// Events are a Stream row's events.
	Events []Event
	// PersonActions are the person-only actions decided inside the row.
	PersonActions []string
	// Errors is the row's error union, filled by Derive; a value set by hand
	// is replaced.
	Errors []string

	Access Access
	// Status is the primary success status. Zero is 200, 204 for a JSON row
	// with no Response, and 302 for a Redirect.
	Status int
	// MaxBody is the request body cap when not the table's; zero is the
	// table's.
	MaxBody int
	// Window is a Keyed row's idempotency window, 1 minute to 7 days; zero
	// is the table's.
	Window time.Duration
	// MaxKept is a Keyed row's answers kept per credential; zero is no count
	// bound.
	MaxKept int
	Kind    Kind

	Keyed   bool // takes an Idempotency-Key
	IfMatch bool // takes If-Match naming the Tag it was read at
	List    bool // a list read: takes Page, answers List[T]
	// Personal marks a read whose rows differ per principal within one shape;
	// it names a kind only in a OnePerson table.
	Personal bool
	// Held marks a JSON row that holds the request open, counted by the
	// held-request cap.
	Held bool
	// ShownOnce marks an answer carrying a secret shown once; such a row is
	// never Keyed.
	ShownOnce bool
	// DuringOutage keeps the row served while a store file cannot open.
	DuringOutage bool
}

// ReasonSet is the reasons one code may carry on a row. Open marks a set the
// row cannot close (a field name or an upstream code rides in the reason):
// Listed holds the known ones, and a client must accept any other.
type ReasonSet struct {
	Listed []string
	Open   bool
}

// Success is one success status a row answers besides its primary one.
type Success struct {
	Response reflect.Type // nil is none (a 204)
	Status   int
}

// Event is one event a Stream row sends.
type Event struct {
	Data reflect.Type // the JSON data type
	// Name is the SSE event name, in the code grammar.
	Name string
	// Readers, on an app event the feed publishes, says who receives it;
	// zero on the feed's own events.
	Readers Access
}

// Access says who a row admits. Its classes are the guard's.
type Access struct {
	// Scope is the key scope: required on Scoped, optional on Key and on a
	// PersonOnly Session row, refused on every other class.
	Scope string
	Class Class
	// RecentAuth asks a session to have authenticated recently (a step-up).
	RecentAuth bool
	// PersonOnly refuses every API key whatever its scopes; valid only on a
	// Session row.
	PersonOnly bool
	HTTPS      bool // admit only a secure request
	Passive    bool // not session activity
}

// Class is a row's admission class. The zero Class is refused.
type Class uint8

// The admission classes.
const (
	Public Class = iota + 1
	Session
	Key
	Scoped
)

// Kind is what a row answers on success.
type Kind uint8

// The row kinds.
const (
	// JSON answers Response as JSON, or nothing with no Response.
	JSON Kind = iota
	// Raw answers a body of the row's ContentType the handler writes itself.
	Raw
	// Stream answers text/event-stream.
	Stream
	// Redirect answers a 3xx whose Location the handler builds from server
	// values only.
	Redirect
)

// Table is an app's API: its rows, owners, feed kinds and mounts.
type Table struct {
	Catalog *Catalog
	// Prefix starts every row's full path, Prefix then Path: "/api", "/v1".
	Prefix string
	// ClientHeader is the header a client version floor reads; "" is no
	// floor.
	ClientHeader string
	Endpoints    []Endpoint
	// Sources are the change feed's sources.
	Sources []FeedSource
	// Kinds are every change kind with its source; empty with no feed.
	Kinds []FeedKind
	// Mounts are rows served and guarded by a handler outside the binder.
	Mounts []Mount
	// MaxBody is the request body cap; zero is 64 KiB.
	MaxBody int
	// Window is how long a Keyed row with no Window of its own keeps an
	// answer: 10 minutes when zero, else 1 minute to 7 days. Derive copies
	// it onto each such row, so the document publishes every row's window.
	Window time.Duration
	// OnePerson says only one person can sign in, so a Personal read may
	// name kinds.
	OnePerson bool
}

// Mount is a handler serving its own rows: they are in the table for the
// client generator, the document and the unions, and the handler decodes and
// guards them itself.
type Mount struct {
	Handler http.Handler
	Rows    []Endpoint
}

// FeedSource is one source of the change feed: a store with its own epoch
// and revisions, or an in-memory set of parts.
type FeedSource struct {
	// Name is ^[a-z][a-z0-9_]{0,31}$, a key of the Feed-Position dictionary.
	Name string
	// Log says a store log is behind it, so a reader can catch up.
	Log bool
}

// FeedKind is one change kind and the source it belongs to.
type FeedKind struct {
	// Name is in the code grammar, at most 32 bytes, unique across sources.
	Name   string
	Source string
}

// The person-only actions: reachable by no API key, only by a person's
// session or the admin-socket principal. The first three are a whole row's
// purpose, so their row is PersonOnly; the last three are decided inside a
// row through RequirePerson.
const (
	PersonSignInMethods   = "sign_in_methods"
	PersonTrustedProxies  = "trusted_proxies"
	PersonKeyRotate       = "key_rotate"
	PersonAdminAccount    = "admin_account"
	PersonSecretRetarget  = "secret_retarget"
	PersonAccountsArchive = "accounts_archive"
)
