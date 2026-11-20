package httpwire

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/go/sealed"
)

// Cursor is a page cursor as the query carries it: opaque, at most 1,416
// bytes, absent on the first page. Any cursor that does not open for this
// row, its path values, its query, the headers the row declares and its
// owner answers cursor_invalid.
type Cursor string

// MaxBytes is the length go/sealed gives a direction byte and the longest
// position, sealed.ExpiringLen(1 + 1,024).
func (Cursor) MaxBytes() int { return maxSealed }

// Limit is a page size, 1 to 200; absent is 50.
type Limit int

// Range is the bound the decode holds a limit to and wiregen describes.
func (Limit) Range() (lo, hi int64) { return 1, 200 }

// Page is embedded in a List row's Query type.
type Page struct {
	Cursor Cursor `json:"cursor,omitempty"`
	Limit  Limit  `json:"limit,omitempty"`
}

// List is a List row's Response, or embedded in it beside the app's own
// fields. The binder writes a nil Items as [] and answers internal_error,
// writing nothing, unless 0 <= Offset <= Total <= 999,999,999,999,999,
// Offset+len(Items) <= Total, PrevCursor is absent exactly when Offset is
// 0, and NextCursor is absent exactly when the page ends at Total.
//
//nolint:govet // fieldalignment: the declaration order is the wire's member order.
type List[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	PrevCursor string `json:"prev_cursor,omitempty"`
	// Offset is the count of items before this page's first, read with Total.
	Offset int64 `json:"offset"`
	// Total is the count of items matching the query at this read.
	Total int64 `json:"total"`
}

func (List[T]) listType() reflect.Type { return reflect.TypeFor[List[T]]() }

func (l List[T]) facts() listFacts {
	return listFacts{items: len(l.Items), offset: l.Offset, total: l.Total, next: l.NextCursor != "", prev: l.PrevCursor != ""}
}

const (
	maxPosition = 1024
	// maxSealed is sealed.ExpiringLen(1 + maxPosition), which a function
	// call cannot give a constant; a test holds the two equal.
	maxSealed    = 1416
	defaultLimit = 50
	maxTotal     = 999_999_999_999_999
	cursorAge    = 24 * time.Hour
)

type direction byte

const (
	forward direction = iota
	backward
)

var (
	// ErrPositionTooLong is SealNext's, SealPrev's and Seal's answer for a
	// position or payload over 1,024 bytes; a handler returning it answers
	// internal_error.
	ErrPositionTooLong = errors.New("httpwire: a sealed position or payload is at most 1,024 bytes")
	// ErrSealed is Open's answer for a value that does not open: tampered,
	// sealed under another sealer, purpose or credential, past its ttl, or
	// opened outside a request the binder serves or on a Public row.
	ErrSealed = errors.New("httpwire: the sealed value does not open")

	errNotList    = errors.New("httpwire: SealNext and SealPrev seal only on a List row")
	errNoScope    = errors.New("httpwire: Seal needs the context of a request the binder serves")
	errPublicSeal = errors.New("httpwire: Seal refuses a Public row, where no credential binds the handle")
)

// lengthPrefixed joins fields, each after its length, so no two field
// lists give one byte string.
func lengthPrefixed(fields ...string) string {
	var b []byte
	for _, f := range fields {
		b = binary.AppendUvarint(b, uint64(len(f)))
		b = append(b, f...)
	}
	return string(b)
}

// form is m in one byte string: its names sorted, each followed by its
// count of values and the values in order, every field length-prefixed,
// so no two maps give one string.
func form(m map[string][]string) string {
	var fields []string
	for _, name := range slices.Sorted(maps.Keys(m)) {
		fields = append(fields, name, strconv.Itoa(len(m[name])))
		fields = append(fields, m[name]...)
	}
	return lengthPrefixed(fields...)
}

// cursorPurpose binds a cursor to its row, the hashes of its query (Page
// aside) and declared headers as sent, its owner and the values of the row's path
// wildcards in path order. Both purposes are named under httpwire/, as
// sealed names a purpose by its owning module, so no app purpose on a
// shared Sealer equals one, and they differ from their first distinct
// byte, so a list cursor never opens as a handle.
func cursorPurpose(row, queryHash, headersHash, owner string, path []string) sealed.Purpose {
	return sealed.Purpose("httpwire/cursor:" + lengthPrefixed(append([]string{row, queryHash, headersHash, owner}, path...)...))
}

func formHash(m map[string][]string) string {
	h := sha256.Sum256([]byte(form(m)))
	return string(h[:])
}

func handlePurpose(purpose, credential string) sealed.Purpose {
	return sealed.Purpose("httpwire/seal:" + lengthPrefixed(purpose, credential))
}

// openCursor answers the direction and position of a cursor sealed for p.
// A value longer than any cursor is refused unread.
func openCursor(s *sealed.Sealer, p sealed.Purpose, value string) (direction, []byte, error) {
	if len(value) > maxSealed {
		return 0, nil, sealed.ErrInvalid
	}
	b, _, err := s.Open(p, value)
	if err != nil {
		return 0, nil, err
	}
	if len(b) == 0 || direction(b[0]) > backward {
		return 0, nil, sealed.ErrInvalid
	}
	return direction(b[0]), b[1:], nil
}

// scope is what the binder knows of a request's principal, carried in its
// context for CredentialOf, CursorOwnerOf, Seal, Open and RequirePerson.
type scope struct {
	api               *API
	ep                *Endpoint
	credential, owner string
	public            bool
}

type scopeKey struct{}

func scopeOf(ctx context.Context) *scope {
	s, _ := ctx.Value(scopeKey{}).(*scope)
	return s
}

// CredentialOf answers WithCredential's answer for the request ctx belongs
// to; "" on a Public row and outside a request the binder serves.
func CredentialOf(ctx context.Context) string {
	if s := scopeOf(ctx); s != nil {
		return s.credential
	}
	return ""
}

// CursorOwnerOf answers WithCursorOwner's answer for the request ctx
// belongs to; "" on a Public row and outside a request the binder serves.
func CursorOwnerOf(ctx context.Context) string {
	if s := scopeOf(ctx); s != nil {
		return s.owner
	}
	return ""
}

// Seal seals payload, at most 1,024 bytes, under WithSealer's sealer until
// ttl from now, bound to purpose and CredentialOf(ctx), so only that
// credential opens it. It refuses a Public row, where a handle would come
// back from anyone, and wraps sealed's refusals (sealed.ErrShortTTL,
// sealed.ErrNoKey).
func Seal(ctx context.Context, purpose string, payload []byte, ttl time.Duration) (string, error) {
	s := scopeOf(ctx)
	switch {
	case s == nil:
		return "", errNoScope
	case s.public:
		return "", errPublicSeal
	case len(payload) > maxPosition:
		return "", ErrPositionTooLong
	}
	v, err := s.api.sealer.Seal(handlePurpose(purpose, s.credential), payload, ttl)
	if err != nil {
		return "", fmt.Errorf("httpwire: Seal: %w", err)
	}
	return v, nil
}

// Open answers the payload Seal sealed for purpose and this request's
// credential, before its ttl, else ErrSealed. A sealer holding no key
// answers an error wrapping sealed.ErrNoKey instead, which is the server's
// fault and not the client's.
func Open(ctx context.Context, purpose, value string) ([]byte, error) {
	s := scopeOf(ctx)
	if s == nil {
		return nil, ErrSealed
	}
	// No handle opens on a Public row: Seal refuses one, so none is sealed
	// to its empty credential.
	payload, _, err := s.api.sealer.Open(handlePurpose(purpose, s.credential), value)
	switch {
	case errors.Is(err, sealed.ErrNoKey):
		return nil, fmt.Errorf("httpwire: Open: %w", err)
	case err != nil:
		return nil, ErrSealed
	}
	return payload, nil
}

// WithSealer sets the sealer list cursors and Seal's handles are sealed
// under, such as a sealed.KeyFile's Sealer, so they outlive a restart.
// Without it, New uses sealed.NewEphemeral, so cursors stop opening at a
// restart. New refuses a nil Sealer and one holding no key: an app whose
// key file is lost omits the option until it has one. A Sealer whose
// KeyFile is closed answers internal_error, so close the API first.
func WithSealer(s *sealed.Sealer) Option {
	return func(o *options) { o.sealer, o.sealerSet = s, true }
}

// listCursor seals a List request's cursors under cursorPurpose's
// bindings.
type listCursor struct {
	sealer  *sealed.Sealer
	purpose sealed.Purpose
}

func (l *listCursor) seal(dir direction, pos []byte) (string, error) {
	switch {
	case l == nil:
		return "", errNotList
	case len(pos) > maxPosition:
		return "", ErrPositionTooLong
	}
	v, err := l.sealer.Seal(l.purpose, append([]byte{byte(dir)}, pos...), cursorAge)
	if err != nil {
		return "", fmt.Errorf("httpwire: sealing a cursor: %w", err)
	}
	return v, nil
}

// SealNext answers the next_cursor resuming after pos, the page's last
// item: a request with it has Position pos and Backward false. It is bound
// to this row, WithCursorOwner's owner, and the path values, query (Page
// aside) and declared headers as the request sent them, and answers
// ErrPositionTooLong for a pos over 1,024 bytes.
func (c Call[P, Q, B, H]) SealNext(pos []byte) (string, error) { return c.list.seal(forward, pos) }

// SealPrev answers the prev_cursor ending before pos, the page's first
// item: a request with it has Position pos and Backward true, and answers
// the Limit rows before pos in list order, or the first page when fewer
// precede it. It is bound as SealNext's is.
func (c Call[P, Q, B, H]) SealPrev(pos []byte) (string, error) { return c.list.seal(backward, pos) }

var (
	pageType   = reflect.TypeFor[Page]()
	cursorType = reflect.TypeFor[Cursor]()
	// pageNames are Page's query parameters, which move between the pages
	// one cursor reads, so its purpose leaves them out.
	pageNames = func() []string {
		fields, _ := textFields(pageType)
		names := make([]string, len(fields))
		for i, f := range fields {
			names[i] = f.name
		}
		return names
	}()
)

// pageIndex answers the index of struct t's embedded Page.
func pageIndex(t reflect.Type) ([]int, bool) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, false
	}
	for f := range t.Fields() {
		if f.Anonymous && f.Type == pageType {
			return f.Index, true
		}
	}
	return nil, false
}

type pageAnswer interface {
	listType() reflect.Type
	facts() listFacts
}

// listIndex answers the index of the List[T] that t is or embeds, empty
// when t is one.
func listIndex(t reflect.Type) ([]int, bool) {
	return answerIndex(t, func(t reflect.Type) bool {
		p, ok := methodOf[pageAnswer](t)
		return ok && p.listType() == t
	})
}

// answerIndex answers the index of the field of t that is, by is, the
// answer type t is or embeds, empty when t is one.
func answerIndex(t reflect.Type, is func(reflect.Type) bool) ([]int, bool) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, false
	}
	if is(t) {
		return []int{}, true
	}
	for f := range t.Fields() {
		if f.Anonymous && is(f.Type) {
			return f.Index, true
		}
	}
	return nil, false
}

type listFacts struct {
	offset, total int64
	items         int
	next, prev    bool
}

// problem names the first page rule f breaks, or "".
func (f listFacts) problem() string {
	switch {
	case f.total < 0 || f.total > maxTotal:
		return "total is outside 0 to 999,999,999,999,999"
	case f.offset < 0 || f.offset > f.total:
		return "offset is outside 0 to total"
	case f.offset+int64(f.items) > f.total:
		return "offset plus the page's items passes total"
	case f.prev == (f.offset == 0):
		return "prev_cursor is not present exactly when offset is past 0"
	case f.next == (f.offset+int64(f.items) == f.total):
		return "next_cursor is not present exactly when the page ends before total"
	}
	return ""
}

// checkedPage answers a List row's answer with a nil Items as [], or the
// page rule it breaks.
func checkedPage(body any) (page any, problem string) {
	v := reflect.ValueOf(body)
	idx, ok := listIndex(v.Type())
	if !ok {
		return body, "the answer holds no httpwire.List"
	}
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	l := out.FieldByIndex(idx)
	p, _ := reflect.TypeAssert[pageAnswer](l)
	if problem = p.facts().problem(); problem != "" {
		return nil, problem
	}
	if items := l.FieldByName("Items"); items.IsNil() {
		items.Set(reflect.MakeSlice(items.Type(), 0, 0))
	}
	return out.Interface(), ""
}

// openPage reads a List request's Page from d's query and opens its
// cursor for cursorPurpose's bindings.
func (a *API) openPage(r *http.Request, ep *Endpoint, d *decoded) *Error {
	idx, _ := pageIndex(ep.Query)
	p, _ := reflect.TypeAssert[Page](reflect.ValueOf(d.query).FieldByIndex(idx))
	query := maps.Clone(d.sent.query)
	for _, name := range pageNames {
		delete(query, name)
	}
	d.list = &listCursor{sealer: a.sealer, purpose: cursorPurpose(ep.Name, formHash(query), formHash(d.sent.headers), CursorOwnerOf(r.Context()), d.sent.path)}
	d.limit = cmp.Or(int(p.Limit), defaultLimit)
	if p.Cursor == "" {
		return nil
	}
	dir, pos, err := openCursor(a.sealer, d.list.purpose, string(p.Cursor))
	switch {
	case errors.Is(err, sealed.ErrNoKey):
		a.log().ErrorContext(r.Context(), "httpwire: the sealer holds no key", "row", ep.Name, "error", err)
		return &Error{Code: codeInternal}
	case err != nil:
		return &Error{Code: codeCursor, Message: "the cursor does not open for this list"}
	}
	d.position, d.backward = pos, dir == backward
	return nil
}

// listProblems names how ep misuses Page or List; every list read is a
// List row, and only a List row takes Page or answers List[T].
func listProblems(ep *Endpoint) []string {
	_, hasPage := pageIndex(ep.Query)
	_, isList := listIndex(bodyOf(ep))
	if !ep.List {
		var p []string
		if hasPage {
			p = append(p, "the Query embeds httpwire.Page, which only a List row takes")
		}
		if isList {
			p = append(p, "the Response is an httpwire.List[T], which only a List row answers")
		}
		return p
	}
	var p []string
	if !hasPage {
		p = append(p, "a List row's Query embeds httpwire.Page")
	}
	if !isList {
		p = append(p, "a List row's Response is, or embeds, an httpwire.List[T]")
	}
	if ep.Method != http.MethodGet || ep.Kind != JSON {
		p = append(p, fmt.Sprintf("a List row is a JSON GET, not a %s %s", kindNames[ep.Kind], ep.Method))
	}
	return p
}
