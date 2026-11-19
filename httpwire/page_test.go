package httpwire_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/sealed"
)

type (
	entryKind  string
	entryQuery struct {
		httpwire.Page
		Kind entryKind `json:"kind,omitempty"`
	}
	entry struct {
		Key uint64 `json:"key"`
	}
	archivedPage struct {
		httpwire.List[entry]
		State string `json:"state"`
	}
	sealedHandle struct {
		Value string `json:"value"`
	}
	listCall   = httpwire.Call[noP, entryQuery, noB, noH]
	handleCall = httpwire.Call[noP, noQ, noB, noH]
)

func (entryKind) MaxBytes() int { return 16 }

func pageDoc(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/page.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pageTable holds a session List row, one whose answer embeds List, a
// Public List row, and a session and a Public row that seal handles.
func pageTable(t testing.TB) *httpwire.Table {
	t.Helper()
	listRow := func(name, path, summary string, access httpwire.Access, resp reflect.Type) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: summary + ".",
			Query: reflect.TypeFor[entryQuery](), Response: resp, Access: access, List: true,
		}
	}
	handleRow := func(name, path, summary string, access httpwire.Access) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: summary + ".",
			Response: reflect.TypeFor[sealedHandle](), Access: access,
		}
	}
	return &httpwire.Table{
		Prefix: "/api", Catalog: fixtureCatalog(t),
		Endpoints: []httpwire.Endpoint{
			listRow("items.list", "/items", "List items", session, reflect.TypeFor[httpwire.List[entry]]()),
			listRow("items.archived", "/items/archived", "List archived items", session, reflect.TypeFor[archivedPage]()),
			listRow("items.public", "/public/items", "List public items", public, reflect.TypeFor[httpwire.List[entry]]()),
			handleRow("handles.seal", "/handles", "Seal a handle", session),
			handleRow("handles.public", "/public/handles", "Seal a public handle", public),
		},
	}
}

// principalOf reads the fixture's Principal header, "session <account>
// <id>", "key <account> <id>" or "socket", into the credential and the
// cursor owner access's gate gives each.
func principalOf(r *http.Request) (credential, owner string) {
	f := strings.Fields(r.Header.Get("Principal"))
	switch {
	case len(f) == 3 && f[0] == "session":
		return "s:" + f[2], "u:" + f[1]
	case len(f) == 3 && f[0] == "key":
		return "k:" + f[2], "k:" + f[2]
	case len(f) == 1 && f[0] == "socket":
		return "o:admin-socket", "o:admin-socket"
	}
	return "", ""
}

const (
	sessionA = "session acct1 a"
	sessionB = "session acct1 b"
	otherAcc = "session acct2 c"
	keyOne   = "key acct1 k1"
	keyTwo   = "key acct1 k2"
	socket   = "socket"
)

// pageSealer is the app's sealer, which every pageStore build shares, as
// a key file's Sealer outlives a restart.
var pageSealer = sealed.New([32]byte{1, 2, 3})

// pageStore is a set of keys listed in ascending order, its List handlers
// paging it both ways with the reference keyset scan.
type pageStore struct {
	api    *httpwire.API
	keys   map[uint64]bool
	answer func(listCall) (httpwire.List[entry], error)
	handle func(context.Context, handleCall) (sealedHandle, error)
	calls  []listCall
	mu     sync.Mutex
}

// newPageStore holds keys 1 to n under an API with pageSealer.
func newPageStore(t testing.TB, n int, opts ...httpwire.Option) *pageStore {
	t.Helper()
	s := &pageStore{keys: map[uint64]bool{}}
	for k := range n {
		s.keys[uint64(k+1)] = true
	}
	s.api = s.build(t, append([]httpwire.Option{httpwire.WithSealer(pageSealer)}, opts...)...)
	return s
}

func (s *pageStore) build(t testing.TB, opts ...httpwire.Option) *httpwire.API {
	t.Helper()
	api, err := s.newAPI(t, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api
}

func (s *pageStore) newAPI(t testing.TB, opts ...httpwire.Option) (*httpwire.API, error) {
	t.Helper()
	return httpwire.New(pageTable(t), s.bindings(), append([]httpwire.Option{
		httpwire.WithSchema(pageDoc(t)),
		httpwire.WithGuard(fixtureGuard{}),
		httpwire.WithCredential(func(r *http.Request) string { c, _ := principalOf(r); return c }),
		httpwire.WithCursorOwner(func(r *http.Request) string { _, o := principalOf(r); return o }),
	}, opts...)...)
}

func (s *pageStore) bindings() []httpwire.Binding {
	return []httpwire.Binding{
		httpwire.Bind("items.list", s.list),
		httpwire.Bind("items.archived", func(ctx context.Context, c listCall) (archivedPage, error) {
			l, err := s.list(ctx, c)
			return archivedPage{List: l, State: "fresh"}, err
		}),
		httpwire.Bind("items.public", s.list),
		httpwire.Bind("handles.seal", s.sealHandle),
		httpwire.Bind("handles.public", s.sealHandle),
	}
}

func (s *pageStore) list(_ context.Context, c listCall) (httpwire.List[entry], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	if s.answer != nil {
		return s.answer(c)
	}
	return scanPage(s.sortedLocked(), c)
}

func (s *pageStore) sealHandle(ctx context.Context, c handleCall) (sealedHandle, error) {
	if s.handle == nil {
		return sealedHandle{}, errors.New("no handle function")
	}
	return s.handle(ctx, c)
}

func (s *pageStore) sortedLocked() []uint64 {
	keys := make([]uint64, 0, len(s.keys))
	for k := range s.keys {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (s *pageStore) sorted() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

func (s *pageStore) set(present bool, keys ...uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if present {
			s.keys[k] = true
		} else {
			delete(s.keys, k)
		}
	}
}

func (s *pageStore) ran() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *pageStore) last() listCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

func position(k uint64) []byte { return binary.BigEndian.AppendUint64(nil, k) }

// scanPage is the both-ways keyset page over keys, ascending: forward
// after Position, backward the Limit keys before it, and the first page
// when a backward scan reaches the start. Offset counts the keys before
// the page in the same read as Total.
func scanPage(keys []uint64, c listCall) (httpwire.List[entry], error) {
	start := 0
	if c.Position != nil {
		pos := binary.BigEndian.Uint64(c.Position)
		at, found := slices.BinarySearch(keys, pos)
		switch {
		case c.Backward:
			start = max(at-c.Limit, 0)
		case found:
			start = at + 1
		default:
			start = at
		}
	}
	end := min(start+c.Limit, len(keys))
	page := httpwire.List[entry]{Offset: int64(start), Total: int64(len(keys))}
	for _, k := range keys[start:end] {
		page.Items = append(page.Items, entry{Key: k})
	}
	var err error
	switch {
	case start > 0 && start < end:
		page.PrevCursor, err = c.SealPrev(position(keys[start]))
	case start > 0:
		page.PrevCursor, err = c.SealPrev(position(keys[start-1] + 1))
	}
	if err == nil && end < len(keys) {
		page.NextCursor, err = c.SealNext(position(keys[end-1]))
	}
	return page, err
}

func (s *pageStore) get(target, principal string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if principal != "" {
		r.Header.Set("Principal", principal)
	}
	return answer(s.api, r)
}

func withCursor(path, cursor string, query ...string) string {
	v := url.Values{"cursor": {cursor}}
	for i := 0; i+1 < len(query); i += 2 {
		v.Set(query[i], query[i+1])
	}
	return path + "?" + v.Encode()
}

func pageOf(t testing.TB, rec *httptest.ResponseRecorder) httpwire.List[entry] {
	t.Helper()
	var l httpwire.List[entry]
	if err := json.Unmarshal(rec.Body.Bytes(), &l); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("answered %d %q (%v), want 200 with a page", rec.Code, rec.Body.String(), err)
	}
	return l
}

func keysOf(l httpwire.List[entry]) []uint64 {
	var out []uint64
	for _, e := range l.Items {
		out = append(out, e.Key)
	}
	return out
}

func span(from, to uint64) []uint64 {
	var out []uint64
	for k := from; k <= to; k++ {
		out = append(out, k)
	}
	return out
}

func wantPage(t *testing.T, l httpwire.List[entry], keys []uint64, offset, total int64) {
	t.Helper()
	if !slices.Equal(keysOf(l), keys) || l.Offset != offset || l.Total != total {
		t.Errorf("page = %v offset %d of %d, want %v offset %d of %d", keysOf(l), l.Offset, l.Total, keys, offset, total)
	}
}

func wantCursorInvalid(t *testing.T, s *pageStore, rec *httptest.ResponseRecorder, ranBefore int) {
	t.Helper()
	if env := envelopeOf(t, rec); rec.Code != http.StatusBadRequest || env.Code != "cursor_invalid" {
		t.Errorf("answered %d %q, want 400 cursor_invalid", rec.Code, env.Code)
	}
	if n := s.ran(); n != ranBefore {
		t.Errorf("the handler ran %d times, want none", n-ranBefore)
	}
}

func TestFirstPageHasNoPrevCursor(t *testing.T) {
	s := newPageStore(t, 10)
	rec := s.get("/api/items?limit=3", sessionA)
	l := pageOf(t, rec)
	wantPage(t, l, span(1, 3), 0, 10)
	if l.PrevCursor != "" || strings.Contains(rec.Body.String(), "prev_cursor") {
		t.Errorf("the first page carries a prev_cursor: %s", rec.Body.String())
	}
	if l.NextCursor == "" {
		t.Error("the first page of three has no next_cursor")
	}
	if c := s.last(); c.Position != nil || c.Backward || c.Limit != 3 {
		t.Errorf("handler saw Position %v Backward %v Limit %d, want nil, false, 3", c.Position, c.Backward, c.Limit)
	}
	t.Run("absent limit is 50", func(t *testing.T) {
		l := pageOf(t, s.get("/api/items", sessionA))
		wantPage(t, l, span(1, 10), 0, 10)
		if c := s.last(); c.Limit != 50 {
			t.Errorf("Limit = %d, want 50", c.Limit)
		}
		if l.NextCursor != "" || l.PrevCursor != "" {
			t.Errorf("a whole list on one page carries cursors %q %q", l.NextCursor, l.PrevCursor)
		}
	})
	t.Run("an empty list", func(t *testing.T) {
		s := newPageStore(t, 0)
		rec := s.get("/api/items", sessionA)
		wantPage(t, pageOf(t, rec), nil, 0, 0)
		if !strings.Contains(rec.Body.String(), `"items":[]`) {
			t.Errorf("an empty page is %s, want items as []", rec.Body.String())
		}
	})
}

func TestPrevCursorFromPastedLink(t *testing.T) {
	s := newPageStore(t, 20)
	first := pageOf(t, s.get("/api/items?limit=5", sessionA))
	second := pageOf(t, s.get(withCursor("/api/items", first.NextCursor, "limit", "5"), sessionA))
	wantPage(t, second, span(6, 10), 5, 20)
	// A fresh client: another session of the account, holding only the link.
	third := pageOf(t, s.get(withCursor("/api/items", second.NextCursor, "limit", "5"), sessionB))
	wantPage(t, third, span(11, 15), 10, 20)
	if c := s.last(); !slices.Equal(c.Position, position(10)) || c.Backward {
		t.Errorf("handler saw Position %v Backward %v, want key 10 forward", c.Position, c.Backward)
	}
	back := pageOf(t, s.get(withCursor("/api/items", third.PrevCursor, "limit", "5"), sessionB))
	wantPage(t, back, span(6, 10), 5, 20)
	if c := s.last(); !slices.Equal(c.Position, position(11)) || !c.Backward {
		t.Errorf("handler saw Position %v Backward %v, want key 11 backward", c.Position, c.Backward)
	}
	if back.PrevCursor == "" || back.NextCursor == "" {
		t.Errorf("a middle page carries cursors %q %q, want both", back.PrevCursor, back.NextCursor)
	}
}

func TestPrevReachingStartAnswersFirstPage(t *testing.T) {
	s := newPageStore(t, 10)
	first := pageOf(t, s.get("/api/items?limit=3", sessionA))
	second := pageOf(t, s.get(withCursor("/api/items", first.NextCursor, "limit", "3"), sessionA))
	wantPage(t, second, span(4, 6), 3, 10)
	s.set(false, 2)
	back := pageOf(t, s.get(withCursor("/api/items", second.PrevCursor, "limit", "3"), sessionA))
	wantPage(t, back, []uint64{1, 3, 4}, 0, 9)
	if back.PrevCursor != "" || back.NextCursor == "" {
		t.Errorf("the first page reached backward carries cursors %q %q, want only next", back.PrevCursor, back.NextCursor)
	}
}

func TestDeletedRowAtPositionResumesBothWays(t *testing.T) {
	s := newPageStore(t, 12)
	l := pageOf(t, s.get("/api/items?limit=3", sessionA))
	for range 2 {
		l = pageOf(t, s.get(withCursor("/api/items", l.NextCursor, "limit", "3"), sessionA))
	}
	wantPage(t, l, span(7, 9), 6, 12)
	s.set(false, 7, 9)
	next := pageOf(t, s.get(withCursor("/api/items", l.NextCursor, "limit", "3"), sessionA))
	wantPage(t, next, span(10, 12), 7, 10)
	prev := pageOf(t, s.get(withCursor("/api/items", l.PrevCursor, "limit", "3"), sessionA))
	wantPage(t, prev, span(4, 6), 3, 10)
}

// TestOffsetPlusPageNeverPassesTotal walks pages both ways while keys are
// committed and deleted between reads, and holds every page to the keys
// the store held at its read.
func TestOffsetPlusPageNeverPassesTotal(t *testing.T) {
	for seed := range uint64(40) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			s := newPageStore(t, rng.IntN(40))
			limit := strconv.Itoa(1 + rng.IntN(6))
			l := pageOf(t, s.get("/api/items?limit="+limit, sessionA))
			for step := range 40 {
				keys := s.sorted()
				checkPage(t, step, l, keys)
				for range rng.IntN(3) {
					s.set(true, uint64(1+rng.IntN(80)))
				}
				for range rng.IntN(3) {
					if keys := s.sorted(); len(keys) > 0 {
						s.set(false, keys[rng.IntN(len(keys))])
					}
				}
				switch cursor := []string{l.NextCursor, l.PrevCursor, ""}[rng.IntN(3)]; cursor {
				case "":
					l = pageOf(t, s.get("/api/items?limit="+limit, sessionA))
				default:
					l = pageOf(t, s.get(withCursor("/api/items", cursor, "limit", limit), sessionA))
				}
			}
		})
	}
}

func checkPage(t *testing.T, step int, l httpwire.List[entry], keys []uint64) {
	t.Helper()
	end := l.Offset + int64(len(l.Items))
	switch {
	case l.Total != int64(len(keys)):
		t.Fatalf("step %d: total %d, want the %d keys held", step, l.Total, len(keys))
	case l.Offset < 0 || end > l.Total:
		t.Fatalf("step %d: offset %d plus %d items passes total %d", step, l.Offset, len(l.Items), l.Total)
	case !slices.Equal(keysOf(l), keys[l.Offset:end]):
		t.Fatalf("step %d: items %v at offset %d, want %v", step, keysOf(l), l.Offset, keys[l.Offset:end])
	case (l.PrevCursor == "") != (l.Offset == 0):
		t.Fatalf("step %d: prev_cursor %q at offset %d", step, l.PrevCursor, l.Offset)
	case (l.NextCursor == "") != (end == l.Total):
		t.Fatalf("step %d: next_cursor %q with %d of %d shown", step, l.NextCursor, end, l.Total)
	}
}

func TestListAnswerChecksRefuseInconsistentPage(t *testing.T) {
	two := []entry{{Key: 1}, {Key: 2}}
	for name, page := range map[string]httpwire.List[entry]{
		"a negative offset":                 {Items: two[:1], Offset: -1, Total: 5, PrevCursor: "p", NextCursor: "n"},
		"an offset past the total":          {Offset: 6, Total: 5, PrevCursor: "p", NextCursor: "n"},
		"offset plus page past the total":   {Items: two, Offset: 4, Total: 5, PrevCursor: "p", NextCursor: "n"},
		"a prev cursor on the first page":   {Items: two, Offset: 0, Total: 5, PrevCursor: "p", NextCursor: "n"},
		"no prev cursor past the first":     {Items: two, Offset: 2, Total: 5, NextCursor: "n"},
		"a next cursor at the end":          {Items: two, Offset: 3, Total: 5, PrevCursor: "p", NextCursor: "n"},
		"no next cursor before the end":     {Items: two, Offset: 0, Total: 5},
		"a negative total":                  {Offset: 0, Total: -1},
		"an empty first page with a cursor": {Offset: 0, Total: 0, NextCursor: "n"},
	} {
		for _, path := range []string{"/api/items", "/api/items/archived"} {
			t.Run(name+" on "+path, func(t *testing.T) {
				sink := &logSink{}
				s := newPageStore(t, 0, httpwire.WithLogger(slog.New(sink)))
				s.answer = func(listCall) (httpwire.List[entry], error) { return page, nil }
				rec := s.get(path, sessionA)
				if env := envelopeOf(t, rec); rec.Code != http.StatusInternalServerError || env.Code != "internal_error" {
					t.Errorf("answered %d %q, want 500 internal_error", rec.Code, env.Code)
				}
				if strings.Contains(rec.Body.String(), "items") {
					t.Errorf("the page was written: %s", rec.Body.String())
				}
				if !sink.has("ERROR", "row=items.") {
					t.Errorf("no ERROR line naming the row: %q", sink.all())
				}
			})
		}
	}
	t.Run("a consistent page", func(t *testing.T) {
		s := newPageStore(t, 0)
		s.answer = func(listCall) (httpwire.List[entry], error) {
			return httpwire.List[entry]{Items: two, Offset: 3, Total: 5, PrevCursor: "p"}, nil
		}
		rec := s.get("/api/items/archived", sessionA)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"fresh"`) {
			t.Errorf("answered %d %q, want 200 with the embedding type's own field", rec.Code, rec.Body.String())
		}
	})
}

func TestListTotalOverSafeBoundIsInternalError(t *testing.T) {
	sink := &logSink{}
	s := newPageStore(t, 0, httpwire.WithLogger(slog.New(sink)))
	s.answer = func(listCall) (httpwire.List[entry], error) {
		return httpwire.List[entry]{Offset: 0, Total: 1_000_000_000_000_000, NextCursor: "n"}, nil
	}
	rec := s.get("/api/items", sessionA)
	if env := envelopeOf(t, rec); rec.Code != http.StatusInternalServerError || env.Code != "internal_error" {
		t.Errorf("a total of 10^15 answered %d %q, want 500 internal_error", rec.Code, env.Code)
	}
	if !sink.has("ERROR", "row=items.list") {
		t.Errorf("no ERROR line naming the row: %q", sink.all())
	}
	s.answer = func(listCall) (httpwire.List[entry], error) {
		return httpwire.List[entry]{Items: []entry{{Key: 1}}, Offset: 999_999_999_999_998, Total: 999_999_999_999_999, PrevCursor: "p"}, nil
	}
	rec = s.get("/api/items", sessionA)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":999999999999999`) {
		t.Errorf("a total at the bound answered %d %q, want it served", rec.Code, rec.Body.String())
	}
	negative := &logSink{}
	s = newPageStore(t, 0, httpwire.WithLogger(slog.New(negative)))
	s.answer = func(listCall) (httpwire.List[entry], error) { return httpwire.List[entry]{Total: -1}, nil }
	if rec = s.get("/api/items", sessionA); rec.Code != http.StatusInternalServerError || !negative.has("ERROR", "row=items.list", "total is outside") {
		t.Errorf("a total of -1 answered %d, log %q: want the total named", rec.Code, negative.all())
	}
}

func TestCursorOpensInAnotherSessionOfOneAccount(t *testing.T) {
	s := newPageStore(t, 10)
	first := pageOf(t, s.get("/api/items?limit=4", sessionA))
	second := pageOf(t, s.get(withCursor("/api/items", first.NextCursor, "limit", "4"), sessionB))
	wantPage(t, second, span(5, 8), 4, 10)
}

func TestCursorRefusesAnotherOwner(t *testing.T) {
	s := newPageStore(t, 10)
	cursorOf := func(principal string) string {
		return pageOf(t, s.get("/api/items?limit=4", principal)).NextCursor
	}
	for name, tc := range map[string]struct{ from, to string }{
		"a session of another account":   {sessionA, otherAcc},
		"a key of the same account":      {sessionA, keyOne},
		"a second key":                   {keyOne, keyTwo},
		"a socket cursor over TCP":       {socket, sessionA},
		"a session cursor on the socket": {sessionA, socket},
	} {
		t.Run(name, func(t *testing.T) {
			c := cursorOf(tc.from)
			n := s.ran()
			wantCursorInvalid(t, s, s.get(withCursor("/api/items", c, "limit", "4"), tc.to), n)
		})
	}
	for _, p := range []string{keyOne, socket} {
		t.Run("opens for its own "+p, func(t *testing.T) {
			wantPage(t, pageOf(t, s.get(withCursor("/api/items", cursorOf(p), "limit", "4"), p)), span(5, 8), 4, 10)
		})
	}
}

func TestCursorRefusals(t *testing.T) {
	s := newPageStore(t, 10)
	good := pageOf(t, s.get("/api/items?limit=3", sessionA)).NextCursor
	mid := len(good) / 2
	flipped := []byte(good)
	flipped[mid] = map[bool]byte{true: 'B', false: 'A'}[flipped[mid] == 'A']
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if len(good) != 62 {
		t.Fatalf("an 8-byte position sealed to %d characters, want 62", len(good))
	}
	last := strings.IndexByte(alphabet, good[len(good)-1])
	// The cursor's 46 bytes leave four unused bits in its last character.
	respelled := good[:len(good)-1] + string(alphabet[last&^0xF|(last+1)&0xF])
	for name, target := range map[string]string{
		"another spelling":  withCursor("/api/items", respelled, "limit", "3"),
		"another row":       withCursor("/api/items/archived", good, "limit", "3"),
		"another query":     withCursor("/api/items", good, "limit", "3", "kind", "book"),
		"a tampered byte":   withCursor("/api/items", string(flipped), "limit", "3"),
		"one byte cut":      withCursor("/api/items", good[:len(good)-1], "limit", "3"),
		"over 1,416 bytes":  withCursor("/api/items", strings.Repeat("A", 1417), "limit", "3"),
		"not base64url":     withCursor("/api/items", "not!base64", "limit", "3"),
		"padded":            withCursor("/api/items", good+"==", "limit", "3"),
		"not UTF-8":         "/api/items?limit=3&cursor=%FF%FE",
		"too short to open": withCursor("/api/items", "AAAA", "limit", "3"),
	} {
		t.Run(name, func(t *testing.T) {
			n := s.ran()
			wantCursorInvalid(t, s, s.get(target, sessionA), n)
		})
	}
	t.Run("another sealer", func(t *testing.T) {
		s.api = s.build(t, httpwire.WithSealer(sealed.New([32]byte{9})))
		n := s.ran()
		wantCursorInvalid(t, s, s.get(withCursor("/api/items", good, "limit", "3"), sessionA), n)
	})
	t.Run("a key drawn at each start", func(t *testing.T) {
		s.api = s.build(t)
		c := pageOf(t, s.get("/api/items?limit=3", sessionA)).NextCursor
		wantPage(t, pageOf(t, s.get(withCursor("/api/items", c, "limit", "3"), sessionA)), span(4, 6), 3, 10)
		s.api = s.build(t)
		n := s.ran()
		wantCursorInvalid(t, s, s.get(withCursor("/api/items", c, "limit", "3"), sessionA), n)
	})
	t.Run("the same sealer after a restart, a changed limit", func(t *testing.T) {
		s.api = s.build(t, httpwire.WithSealer(pageSealer))
		wantPage(t, pageOf(t, s.get(withCursor("/api/items", good, "limit", "2"), sessionA)), span(4, 5), 3, 10)
	})
}

func TestCursorRefusedPastADay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newPageStore(t, 10)
		c := pageOf(t, s.get("/api/items?limit=3", sessionA)).NextCursor
		time.Sleep(24*time.Hour - time.Second)
		wantPage(t, pageOf(t, s.get(withCursor("/api/items", c, "limit", "3"), sessionA)), span(4, 6), 3, 10)
		time.Sleep(time.Second)
		n := s.ran()
		wantCursorInvalid(t, s, s.get(withCursor("/api/items", c, "limit", "3"), sessionA), n)
	})
}

func TestCursorSealBounds(t *testing.T) {
	sink := &logSink{}
	s := newPageStore(t, 0, httpwire.WithLogger(slog.New(sink)))
	var longest int
	var tooLong []error
	s.answer = func(c listCall) (httpwire.List[entry], error) {
		for _, seal := range []func([]byte) (string, error){c.SealNext, c.SealPrev} {
			v, err := seal(make([]byte, 1024))
			if err != nil {
				t.Fatalf("a 1,024-byte position: %v", err)
			}
			longest = max(longest, len(v))
			_, err = seal(make([]byte, 1025))
			tooLong = append(tooLong, err)
		}
		return httpwire.List[entry]{}, nil
	}
	rec := s.get("/api/items", sessionA)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %q", rec.Code, rec.Body.String())
	}
	if longest != 1416 {
		t.Errorf("a 1,024-byte position sealed to %d bytes, want 1,416", longest)
	}
	for _, err := range tooLong {
		if !errors.Is(err, httpwire.ErrPositionTooLong) {
			t.Errorf("a 1,025-byte position answered %v, want ErrPositionTooLong", err)
		}
	}
	t.Run("returned by the handler", func(t *testing.T) {
		s.answer = func(c listCall) (httpwire.List[entry], error) {
			_, err := c.SealNext(make([]byte, 1025))
			return httpwire.List[entry]{}, err
		}
		rec := s.get("/api/items", sessionA)
		if env := envelopeOf(t, rec); rec.Code != http.StatusInternalServerError || env.Code != "internal_error" {
			t.Errorf("answered %d %q, want 500 internal_error", rec.Code, env.Code)
		}
		if !sink.has("ERROR", "row=items.list") {
			t.Errorf("no ERROR line naming the row: %q", sink.all())
		}
	})
	t.Run("off a List row", func(t *testing.T) {
		var errs []error
		s.handle = func(_ context.Context, c handleCall) (sealedHandle, error) {
			_, e1 := c.SealNext(position(1))
			_, e2 := c.SealPrev(position(1))
			errs = append(errs, e1, e2)
			return sealedHandle{}, nil
		}
		s.get("/api/handles", sessionA)
		if len(errs) != 2 || errs[0] == nil || errs[1] == nil {
			t.Errorf("SealNext and SealPrev on a row that is not List answered %v, want errors", errs)
		}
	})
}

func TestPublicListPagesWithoutAnOwner(t *testing.T) {
	s := newPageStore(t, 10)
	c := pageOf(t, s.get("/api/public/items?limit=3", "")).NextCursor
	for _, p := range []string{"", otherAcc, keyOne} {
		t.Run("page two as "+cmpOr(p, "anonymous"), func(t *testing.T) {
			wantPage(t, pageOf(t, s.get(withCursor("/api/public/items", c, "limit", "3"), p)), span(4, 6), 3, 10)
			if got := s.last(); got.Position == nil {
				t.Error("the handler saw no position")
			}
		})
	}
	t.Run("an authenticated row's cursor", func(t *testing.T) {
		auth := pageOf(t, s.get("/api/items?limit=3", sessionA)).NextCursor
		n := s.ran()
		wantCursorInvalid(t, s, s.get(withCursor("/api/public/items", auth, "limit", "3"), sessionA), n)
	})
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// sealOn runs f in a handles row's handler as principal and answers what
// it returned.
func sealOn(t *testing.T, s *pageStore, path, principal string, f func(ctx context.Context) string) string {
	t.Helper()
	s.handle = func(ctx context.Context, _ handleCall) (sealedHandle, error) {
		return sealedHandle{Value: f(ctx)}, nil
	}
	rec := s.get(path, principal)
	var h sealedHandle
	if err := json.Unmarshal(rec.Body.Bytes(), &h); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("answered %d %q (%v)", rec.Code, rec.Body.String(), err)
	}
	return h.Value
}

func describe(b []byte, err error) string {
	if err != nil {
		if errors.Is(err, httpwire.ErrSealed) {
			return "ErrSealed"
		}
		return "error"
	}
	return "opened " + string(b)
}

func TestSealBindsAHandleToItsCredential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newPageStore(t, 0)
		handle := sealOn(t, s, "/api/handles", sessionA, func(ctx context.Context) string {
			v, err := httpwire.Seal(ctx, "state", []byte("payload"), time.Hour)
			if err != nil {
				return "error " + err.Error()
			}
			return v
		})
		openOn := func(s *pageStore, principal, path, purpose string) string {
			return sealOn(t, s, path, principal, func(ctx context.Context) string {
				return describe(httpwire.Open(ctx, purpose, handle))
			})
		}
		for name, tc := range map[string]struct {
			principal, path, purpose, want string
		}{
			"its own credential":             {sessionA, "/api/handles", "state", "opened payload"},
			"another session of the account": {sessionB, "/api/handles", "state", "ErrSealed"},
			"another purpose":                {sessionA, "/api/handles", "other", "ErrSealed"},
			"a Public row":                   {sessionA, "/api/public/handles", "state", "ErrSealed"},
		} {
			if got := openOn(s, tc.principal, tc.path, tc.purpose); got != tc.want {
				t.Errorf("%s: Open = %s, want %s", name, got, tc.want)
			}
		}
		if got := openOn(newPageStore(t, 0, httpwire.WithSealer(sealed.New([32]byte{9}))), sessionA, "/api/handles", "state"); got != "ErrSealed" {
			t.Errorf("Open under another sealer = %s, want ErrSealed", got)
		}
		if got := openOn(newPageStore(t, 0), sessionA, "/api/handles", "state"); got != "opened payload" {
			t.Errorf("Open under the same sealer after a restart = %s, want it opened", got)
		}
		time.Sleep(time.Hour - time.Second)
		if got := openOn(s, sessionA, "/api/handles", "state"); got != "opened payload" {
			t.Errorf("Open a second before its ttl = %s, want it opened", got)
		}
		time.Sleep(time.Second)
		if got := openOn(s, sessionA, "/api/handles", "state"); got != "ErrSealed" {
			t.Errorf("Open at its ttl = %s, want ErrSealed", got)
		}
		l := newPageStore(t, 10)
		handle = pageOf(t, l.get("/api/items?limit=3", sessionA)).NextCursor
		if got := openOn(s, sessionA, "/api/handles", "cursor:items.list"); got != "ErrSealed" {
			t.Errorf("Open of a list cursor = %s, want ErrSealed", got)
		}
	})
	sealAs := func(t *testing.T, path string, payload []byte, ttl time.Duration, want func(string, error) bool) {
		t.Helper()
		s := newPageStore(t, 0)
		got := sealOn(t, s, path, sessionA, func(ctx context.Context) string {
			v, err := httpwire.Seal(ctx, "state", payload, ttl)
			return strconv.FormatBool(want(v, err))
		})
		if got != "true" {
			t.Error("Seal did not answer the error")
		}
	}
	t.Run("a payload over 1,024 bytes", func(t *testing.T) {
		sealAs(t, "/api/handles", make([]byte, 1025), time.Hour, func(_ string, err error) bool {
			return errors.Is(err, httpwire.ErrPositionTooLong)
		})
	})
	t.Run("a ttl under one second", func(t *testing.T) {
		sealAs(t, "/api/handles", []byte("p"), time.Second-1, func(_ string, err error) bool {
			return errors.Is(err, sealed.ErrShortTTL)
		})
	})
	t.Run("Seal refuses a Public row", func(t *testing.T) {
		sealAs(t, "/api/public/handles", []byte("p"), time.Hour, func(v string, err error) bool {
			return err != nil && v == ""
		})
	})
	t.Run("outside a request", func(t *testing.T) {
		if v, err := httpwire.Seal(t.Context(), "state", []byte("p"), time.Hour); err == nil || v != "" {
			t.Errorf("Seal outside a request = %q, %v", v, err)
		}
		if _, err := httpwire.Open(t.Context(), "state", "AAAA"); !errors.Is(err, httpwire.ErrSealed) {
			t.Errorf("Open outside a request = %v, want ErrSealed", err)
		}
	})
}

func TestNewRefusesASealerWithNoKey(t *testing.T) {
	s := &pageStore{keys: map[uint64]bool{}}
	for name, sl := range map[string]*sealed.Sealer{"nil": nil, "the zero Sealer": {}} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.newAPI(t, httpwire.WithSealer(sl)); err == nil || !strings.Contains(err.Error(), "WithSealer") {
				t.Errorf("New = %v, want an error naming WithSealer", err)
			}
		})
	}
}

func TestCredentialAndOwnerOfARequest(t *testing.T) {
	s := newPageStore(t, 0)
	read := func(ctx context.Context) string {
		return httpwire.CredentialOf(ctx) + " " + httpwire.CursorOwnerOf(ctx)
	}
	for name, tc := range map[string]struct{ path, principal, want string }{
		"a session":             {"/api/handles", sessionA, "s:a u:acct1"},
		"a key":                 {"/api/handles", keyOne, "k:k1 k:k1"},
		"the socket":            {"/api/handles", socket, "o:admin-socket o:admin-socket"},
		"a session, Public row": {"/api/public/handles", sessionA, " "},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sealOn(t, s, tc.path, tc.principal, read); got != tc.want {
				t.Errorf("CredentialOf and CursorOwnerOf = %q, want %q", got, tc.want)
			}
		})
	}
	if got := httpwire.CredentialOf(t.Context()) + httpwire.CursorOwnerOf(t.Context()); got != "" {
		t.Errorf("outside a request = %q, want empty", got)
	}
}

func TestDeriveRefusesListMisuse(t *testing.T) {
	type pageByPointer struct {
		*httpwire.Page
	}
	listRow := func(tb *httpwire.Table) *httpwire.Endpoint { return row(tb, "items.list") }
	handles := func(tb *httpwire.Table) *httpwire.Endpoint { return row(tb, "handles.seal") }
	for name, tc := range map[string]struct {
		edit func(tb *httpwire.Table)
		want []string
	}{
		"a List row with no Query":          {func(tb *httpwire.Table) { listRow(tb).Query = nil }, []string{`row "items.list"`, "Page"}},
		"a List row whose Query lacks Page": {func(tb *httpwire.Table) { listRow(tb).Query = reflect.TypeFor[jobQuery]() }, []string{`row "items.list"`, "Page"}},
		"a List row whose Query holds Page as a field": {func(tb *httpwire.Table) {
			listRow(tb).Query = reflect.TypeFor[struct {
				P httpwire.Page `json:"p"`
			}]()
		}, []string{`row "items.list"`, "Query embeds httpwire.Page"}},
		"a List row whose Response is a slice": {func(tb *httpwire.Table) { listRow(tb).Response = reflect.TypeFor[[]entry]() }, []string{`row "items.list"`, "List[T]"}},
		"a List row whose Response points to a List": {func(tb *httpwire.Table) {
			listRow(tb).Response = reflect.TypeFor[*httpwire.List[entry]]()
		}, []string{`row "items.list"`, "List[T]"}},
		"a List row whose Response holds List as a field": {func(tb *httpwire.Table) {
			listRow(tb).Response = reflect.TypeFor[struct {
				L httpwire.List[entry] `json:"l"`
			}]()
		}, []string{`row "items.list"`, "Response is, or embeds"}},
		"a List row whose Response embeds a pointer to List": {func(tb *httpwire.Table) {
			listRow(tb).Response = reflect.TypeFor[struct {
				*httpwire.List[entry]
			}]()
		}, []string{`row "items.list"`, "Response is, or embeds"}},
		"a List row whose Response embeds List two levels down": {func(tb *httpwire.Table) {
			listRow(tb).Response = reflect.TypeFor[struct{ archivedPage }]()
		}, []string{`row "items.list"`, "Response is, or embeds"}},
		"a List row with no Response":    {func(tb *httpwire.Table) { listRow(tb).Response = nil }, []string{`row "items.list"`, "List[T]"}},
		"Page on a row that is not List": {func(tb *httpwire.Table) { handles(tb).Query = reflect.TypeFor[entryQuery]() }, []string{`row "handles.seal"`, "Page"}},
		"a List answer on a row that is not List": {func(tb *httpwire.Table) {
			handles(tb).Response = reflect.TypeFor[archivedPage]()
		}, []string{`row "handles.seal"`, "List[T]"}},
		"a List write": {func(tb *httpwire.Table) { listRow(tb).Method = http.MethodPost }, []string{`row "items.list"`, "GET"}},
		"a Raw List row": {func(tb *httpwire.Table) {
			ep := listRow(tb)
			ep.Kind, ep.ContentTypes, ep.Response = httpwire.Raw, []string{"text/csv"}, nil
		}, []string{`row "items.list"`, "JSON"}},
		"a mount List row with no Page": {func(tb *httpwire.Table) {
			tb.Mounts = []httpwire.Mount{{Handler: http.NotFoundHandler(), Rows: []httpwire.Endpoint{{
				Name: "auth.keys", Method: http.MethodGet, Path: "/auth/keys", Summary: "List keys", Description: "Lists keys.",
				Response: reflect.TypeFor[httpwire.List[entry]](), Access: session, List: true, Codes: []httpserver.ErrorCode{"auth_required"},
			}}}}
		}, []string{`row "auth.keys"`, "Page"}},
		"a pointer to Page": {func(tb *httpwire.Table) { listRow(tb).Query = reflect.TypeFor[pageByPointer]() }, []string{`row "items.list"`, "Page"}},
	} {
		t.Run(name, func(t *testing.T) {
			tb := pageTable(t)
			tc.edit(tb)
			_, derr := httpwire.Derive(tb, guardCodes)
			_, nerr := httpwire.New(tb, (&pageStore{}).bindings(), httpwire.WithSchema(pageDoc(t)), httpwire.WithGuard(fixtureGuard{}))
			for _, err := range []error{derr, nerr} {
				if err == nil {
					t.Fatal("accepted the table")
				}
				for _, w := range tc.want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q does not name %q", err, w)
					}
				}
			}
		})
	}
	t.Run("the fixture table", func(t *testing.T) {
		if _, err := httpwire.Derive(pageTable(t), guardCodes); err != nil {
			t.Errorf("Derive = %v", err)
		}
	})
}
