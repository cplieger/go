package httpwire

import (
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"
)

// Cursor is a page cursor as the query carries it: opaque, at most 1,416
// bytes, absent on the first page. Any cursor that does not open for this
// row, query and owner answers cursor_invalid.
type Cursor string

// MaxBytes is base64url of the 12-byte nonce, the format, direction and
// issue-time header, the longest position and the 16-byte tag:
// 12 + 1 + 1 + 8 + 1,024 + 16 bytes.
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
	maxPosition  = 1024
	maxSealed    = 1416
	defaultLimit = 50
	maxTotal     = 999_999_999_999_999
	cursorAge    = 24 * time.Hour
	sealFormat   = 1
	sealHeader   = 10 // format, direction, issue time
)

// The first field of the additional data keeps a list cursor and a purpose
// handle apart whatever purpose an app names.
const (
	cursorDomain = "cursor"
	handleDomain = "seal"
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
	// sealed under another key, purpose or credential, past its age, or
	// opened outside a request the binder serves or on a Public row.
	ErrSealed = errors.New("httpwire: the sealed value does not open")

	errNotList    = errors.New("httpwire: SealNext and SealPrev seal only on a List row")
	errNoScope    = errors.New("httpwire: Seal needs the context of a request the binder serves")
	errPublicSeal = errors.New("httpwire: Seal refuses a Public row, where no credential binds the handle")
)

// sealedText is strict so one sealed value has one spelling.
var sealedText = base64.RawURLEncoding.Strict()

// sealer seals with AES-256-GCM under a random 12-byte nonce, prepended.
type sealer struct{ aead cipher.AEAD }

func newSealer(key [32]byte) (*sealer, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(dir direction, payload, aad []byte) (string, error) {
	if len(payload) > maxPosition {
		return "", ErrPositionTooLong
	}
	pt := make([]byte, 0, sealHeader+len(payload))
	pt = append(pt, sealFormat, byte(dir))
	pt = binary.BigEndian.AppendUint64(pt, uint64(max(time.Now().Unix(), 0)))
	pt = append(pt, payload...)
	return sealedText.EncodeToString(s.aead.Seal(nil, nil, pt, aad)), nil //nolint:gosec // NewGCMWithRandomNonce draws the nonce; its Seal takes none (https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)
}

// open answers the direction and payload of a value seal made with aad no
// longer than maxAge ago. A value longer than any seal makes is refused
// unread.
func (s *sealer) open(sealed string, aad []byte, maxAge time.Duration) (dir direction, payload []byte, ok bool) {
	if len(sealed) > maxSealed {
		return 0, nil, false
	}
	ct, err := sealedText.DecodeString(sealed)
	if err != nil {
		return 0, nil, false
	}
	pt, err := s.aead.Open(nil, nil, ct, aad)
	if err != nil || len(pt) < sealHeader || pt[0] != sealFormat || direction(pt[1]) > backward {
		return 0, nil, false
	}
	// Only seal makes a plaintext that opens, and it writes a time past 1970.
	secs := int64(binary.BigEndian.Uint64(pt[2:sealHeader])) //nolint:gosec // G115: see above
	if time.Since(time.Unix(secs, 0)) > maxAge {
		return 0, nil, false
	}
	return direction(pt[1]), pt[sealHeader:], true
}

// additionalData joins fields, each after its length, so no two field
// lists give one byte string.
func additionalData(fields ...string) []byte {
	var b []byte
	for _, f := range fields {
		b = binary.AppendUvarint(b, uint64(len(f)))
		b = append(b, f...)
	}
	return b
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

// Seal seals payload, at most 1,024 bytes, under the API's cursor key,
// bound to purpose and CredentialOf(ctx), so only that credential opens it.
// It refuses a Public row, where a handle would come back from anyone.
func Seal(ctx context.Context, purpose string, payload []byte) (string, error) {
	s := scopeOf(ctx)
	switch {
	case s == nil:
		return "", errNoScope
	case s.public:
		return "", errPublicSeal
	}
	return s.api.sealer.seal(forward, payload, additionalData(handleDomain, purpose, s.credential))
}

// Open answers the payload Seal sealed for purpose and this request's
// credential no longer than maxAge ago, else ErrSealed.
func Open(ctx context.Context, purpose, sealed string, maxAge time.Duration) ([]byte, error) {
	s := scopeOf(ctx)
	if s == nil {
		return nil, ErrSealed
	}
	// No handle opens on a Public row: Seal refuses one, so none is sealed
	// to its empty credential. Only Seal seals under handleDomain, forward.
	_, payload, ok := s.api.sealer.open(sealed, additionalData(handleDomain, purpose, s.credential), maxAge)
	if !ok {
		return nil, ErrSealed
	}
	return payload, nil
}

// WithCursorKey sets the key list cursors and Seal's handles are sealed
// under. Without it, New draws one at random, so cursors stop opening at a
// restart and a client reads the list again from its first page.
func WithCursorKey(key [32]byte) Option {
	return func(o *options) { o.cursorKey = &key }
}

// listCursor seals a List request's cursors, bound to its row, its query
// without Page, and its owner.
type listCursor struct {
	sealer *sealer
	aad    []byte
}

func (l *listCursor) seal(dir direction, pos []byte) (string, error) {
	if l == nil {
		return "", errNotList
	}
	return l.sealer.seal(dir, pos, l.aad)
}

// SealNext answers the next_cursor resuming after pos, the page's last
// item: a request with it has Position pos and Backward false. It is bound
// to this row, this query (Page aside) and WithCursorOwner's owner, and
// answers ErrPositionTooLong for a pos over 1,024 bytes.
func (c Call[P, Q, B, H]) SealNext(pos []byte) (string, error) { return c.list.seal(forward, pos) }

// SealPrev answers the prev_cursor ending before pos, the page's first
// item: a request with it has Position pos and Backward true, and answers
// the Limit rows before pos in list order, or the first page when fewer
// precede it. It is bound as SealNext's is.
func (c Call[P, Q, B, H]) SealPrev(pos []byte) (string, error) { return c.list.seal(backward, pos) }

var (
	pageType   = reflect.TypeFor[Page]()
	cursorType = reflect.TypeFor[Cursor]()
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
// cursor, bound to the row, the query with Page zeroed, and the owner.
func (a *API) openPage(r *http.Request, ep *Endpoint, d *decoded) *Error {
	idx, _ := pageIndex(ep.Query)
	q := reflect.New(ep.Query).Elem()
	q.Set(reflect.ValueOf(d.query))
	p, _ := reflect.TypeAssert[Page](q.FieldByIndex(idx))
	q.FieldByIndex(idx).SetZero()
	canonical, err := json.Marshal(q.Interface())
	if err != nil {
		a.log().ErrorContext(r.Context(), "httpwire: a List row's query does not encode as JSON", "row", ep.Name, "error", err)
		return &Error{Code: codeInternal}
	}
	hash := sha256.Sum256(canonical)
	d.list = &listCursor{sealer: a.sealer, aad: additionalData(cursorDomain, ep.Name, string(hash[:]), CursorOwnerOf(r.Context()))}
	d.limit = cmp.Or(int(p.Limit), defaultLimit)
	if p.Cursor == "" {
		return nil
	}
	dir, pos, ok := a.sealer.open(string(p.Cursor), d.list.aad, cursorAge)
	if !ok {
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
