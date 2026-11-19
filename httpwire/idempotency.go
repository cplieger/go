package httpwire

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// IdempotencyKey is the slot a kept answer is held under: the key one
// credential sent on one row.
type IdempotencyKey struct {
	Credential string
	Row        string
	Key        string
}

// KeptAnswer is a Keyed row's settled answer, which a resend with the same key
// and fingerprint is answered with until Expires.
type KeptAnswer struct {
	Expires     time.Time
	ContentType string
	// Body is nil when Oversize.
	Body []byte
	// RetryAfter is a kept refusal's; zero on a success.
	RetryAfter time.Duration
	// Status is the status the first run answered, a declared success or
	// a refusal.
	Status      int
	Fingerprint [32]byte
	// Oversize marks an answer kept without its body: its resend answers
	// idempotency_answer_too_large and never runs the handler.
	Oversize bool
}

// IdempotencyStore keeps settled answers; a request in flight is joined in
// memory. A store that writes the key in the work's transaction reads it
// with Idempotency in the handler, answers it from Load, and makes Save a
// no-op. A Load error answers internal_error. A Save error is logged and
// the answer kept in this process's memory until Expires. That memory
// admits a new key by the default bounds over the answers it holds, not
// the runs in flight, so runs whose saves all fail can pass those bounds by
// their number; no kept answer is dropped to hold them.
type IdempotencyStore interface {
	Load(ctx context.Context, k IdempotencyKey) (KeptAnswer, bool, error)
	Save(ctx context.Context, k IdempotencyKey, a KeptAnswer) error
}

// IdempotencyOutcome is what a keyed request met instead of a first run.
type IdempotencyOutcome string

// The outcomes an Observer is told of.
const (
	OutcomeReplayed IdempotencyOutcome = "replayed" // a kept answer was written again
	OutcomeJoined   IdempotencyOutcome = "joined"   // the request waited for the run in flight and wrote its answer
	OutcomeReused   IdempotencyOutcome = "reused"   // the key was held for another request
	OutcomeOversize IdempotencyOutcome = "oversize" // the kept answer had no body: idempotency_answer_too_large
)

// IdempotencyEvent is one outcome on one row, for the app's counters.
type IdempotencyEvent struct {
	Row     string
	Outcome IdempotencyOutcome
}

// IdempotencyOptions configures every Keyed row. Each zero value is its
// default; New refuses a value outside its bound.
type IdempotencyOptions struct {
	// Store keeps settled answers; nil is the in-memory store.
	Store IdempotencyStore
	// Observer is told of each outcome other than a first run.
	Observer func(IdempotencyEvent)
	// MaxAnswer is the largest body kept: 64 KiB when zero, else 1 KiB to
	// 1 MiB. A larger answer is sent whole and kept as Oversize.
	MaxAnswer int
	// MaxBytes bounds the in-memory store's kept bodies: 64 MiB when zero,
	// else 1 MiB to 1 GiB. A body that would pass it is kept as Oversize.
	MaxBytes int
	// MaxBytesPerCredential bounds one credential's kept bodies in memory:
	// MaxBytes / 8 when zero, raised to MaxAnswer, else MaxAnswer to
	// MaxBytes. A credential at it gets idempotency_store_full.
	MaxBytesPerCredential int
	// MaxPerCredential bounds one credential's kept answers and runs in
	// flight in memory: 1,000 when zero, else 1 to 10,000.
	MaxPerCredential int
}

// WithIdempotency configures the Keyed rows; without it each default holds.
func WithIdempotency(o IdempotencyOptions) Option {
	return func(opts *options) { opts.idem = o }
}

// Idempotency answers, inside a Keyed row's handler, the slot its answer
// is kept under and the request's fingerprint, so a store can write the key
// in the work's own transaction. ok is false anywhere else.
func Idempotency(ctx context.Context) (k IdempotencyKey, fingerprint [32]byte, ok bool) {
	c, ok := ctx.Value(idemCallKey{}).(*idemCall)
	if !ok {
		return IdempotencyKey{}, [32]byte{}, false
	}
	return c.key, c.fp, true
}

// IdempotencyPolicy is a Keyed row's published policy, its OpenAPI
// operation's x-idempotency member.
type IdempotencyPolicy struct {
	Scope      string `json:"scope"`
	Concurrent string `json:"concurrent"`
	Kept       string `json:"kept"`
	Result     string `json:"result,omitempty"`
	// WindowS is the row's Window in whole seconds, rounded down.
	WindowS int64 `json:"window_s"`
	MaxKept int   `json:"max_kept,omitempty"`
}

// PolicyOf answers a Keyed row's published policy, read from the row as
// Derive leaves it (its Window set); ok is false for a row that is not
// Keyed.
func PolicyOf(ep *Endpoint) (p IdempotencyPolicy, ok bool) {
	if !ep.Keyed {
		return IdempotencyPolicy{}, false
	}
	return IdempotencyPolicy{
		WindowS:    int64(ep.Window / time.Second),
		Scope:      "credential,operation",
		Concurrent: "join",
		Kept:       "success_and_effectful_refusals",
		MaxKept:    ep.MaxKept,
		Result:     ep.Result,
	}, true
}

type idemCallKey struct{}

type idemCall struct {
	key IdempotencyKey
	fp  [32]byte
}

const (
	headerIdempotencyKey = "Idempotency-Key"
	maxIdempotencyKey    = 255
	// maxEntries is the in-memory store's full mark for kept answers and
	// runs; past it each credential gets a share, and twice it is the ceiling.
	maxEntries = 10_000
)

// idemLimits are IdempotencyOptions with each default filled in.
type idemLimits struct {
	maxAnswer    int
	maxBytes     int
	perCredBytes int
	perCred      int
}

// limits checks o against each bound, answering every refusal.
func (o IdempotencyOptions) limits() (l idemLimits, problems []string) {
	l = idemLimits{
		maxAnswer: cmp.Or(o.MaxAnswer, 64<<10),
		maxBytes:  cmp.Or(o.MaxBytes, 64<<20),
		perCred:   cmp.Or(o.MaxPerCredential, 1000),
	}
	l.perCredBytes = cmp.Or(o.MaxBytesPerCredential, max(l.maxBytes/8, l.maxAnswer))
	bound := func(name string, set bool, v, lo, hi int64, unit string) {
		if set && (v < lo || v > hi) {
			problems = append(problems, fmt.Sprintf("%s %d is outside %d to %d%s", name, v, lo, hi, unit))
		}
	}
	bound("MaxAnswer", o.MaxAnswer != 0, int64(o.MaxAnswer), 1<<10, 1<<20, " bytes")
	bound("MaxBytes", o.MaxBytes != 0, int64(o.MaxBytes), 1<<20, 1<<30, " bytes")
	bound("MaxBytesPerCredential", o.MaxBytesPerCredential != 0, int64(o.MaxBytesPerCredential), int64(l.maxAnswer), int64(l.maxBytes), " bytes (MaxAnswer to MaxBytes)")
	bound("MaxPerCredential", o.MaxPerCredential != 0, int64(o.MaxPerCredential), 1, maxEntries, "")
	if o.Store != nil {
		for _, f := range []struct {
			name string
			set  bool
		}{{"MaxBytes", o.MaxBytes != 0}, {"MaxBytesPerCredential", o.MaxBytesPerCredential != 0}, {"MaxPerCredential", o.MaxPerCredential != 0}} {
			if f.set {
				problems = append(problems, f.name+" bounds only the in-memory store, and Store is set")
			}
		}
	}
	return l, problems
}

// idempotencyKeyOf reads r's Idempotency-Key: required on a Keyed row, as
// one RFC 9651 sf-string (draft-ietf-httpapi-idempotency-key-header-08
// section 2.1), and refused on every other row.
func idempotencyKeyOf(r *http.Request, ep *Endpoint) (string, *Error) {
	vs, sent := r.Header[headerIdempotencyKey]
	switch {
	case !ep.Keyed && sent:
		return "", &Error{Code: "idempotency_key_unsupported", Message: "this endpoint takes no Idempotency-Key"}
	case !ep.Keyed:
		return "", nil
	case !sent:
		return "", &Error{Code: codeKeyRequired, Message: "this endpoint needs an Idempotency-Key"}
	}
	if len(vs) == 1 {
		if key, ok := parseSFString(strings.Trim(vs[0], " \t")); ok && key != "" && len(key) <= maxIdempotencyKey {
			return key, nil
		}
	}
	return "", &Error{Code: codeKeyInvalid, Message: "the Idempotency-Key is not one quoted string of 1 to 255 printable ASCII characters"}
}

// parseSFString unquotes an RFC 9651 section 3.3.3 String: printable ASCII
// between quotes, in which a backslash escapes only a quote or a backslash.
// Nothing may follow the closing quote, parameters included.
func parseSFString(v string) (string, bool) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(v)-1; i++ {
		switch c := v[i]; {
		case c == '\\':
			i++
			if i == len(v)-1 || v[i] != '"' && v[i] != '\\' {
				return "", false
			}
			b.WriteByte(v[i])
		case c == '"', c < 0x20, c > 0x7e:
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// fingerprint is SHA-256 over the row name and the json.Marshal of the
// decoded request, each after its 4-byte big-endian length, so a body's
// spelling changes nothing. Each part is marshalled through a pointer, so
// a pointer-receiver MarshalJSON or MarshalText is used.
func fingerprint(row string, d *decoded) ([32]byte, error) {
	part, err := json.Marshal(struct {
		Params, Query, Body any
		Headers             any `json:",omitempty"`
		IfMatch             string
		RawBody             []byte `json:",omitempty"`
	}{addressed(d.params), addressed(d.query), addressed(d.body), addressed(d.headers), d.ifMatch.String(), d.rawBody})
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	for _, f := range [][]byte{[]byte(row), part} {
		_, _ = h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(f)))) //nolint:gosec // a row name and a decoded request are far below 4 GiB.
		_, _ = h.Write(f)
	}
	return [32]byte(h.Sum(nil)), nil
}

func addressed(v any) any {
	if v == nil {
		return nil
	}
	p := reflect.New(reflect.TypeOf(v))
	p.Elem().Set(reflect.ValueOf(v))
	return p.Interface()
}
