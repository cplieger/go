package httpwire_test

import (
	"net/http"
	"testing"
)

// FuzzCursor_onlyASealedCursorOpens: whatever text a client sends as the
// cursor, only the one the API sealed opens; every other answers
// cursor_invalid and runs no handler. Each fuzz process draws its own key,
// so a seed sealed in another process is one more text that must not open.
func FuzzCursor_onlyASealedCursorOpens(f *testing.F) {
	s := newPageStore(f, 10)
	s.api = s.build(f)
	sealed := pageOf(f, s.get("/api/items?limit=3", sessionA)).NextCursor
	f.Add(sealed)
	f.Add(sealed[:len(sealed)-1])
	f.Add(sealed + "A")
	f.Add("A" + sealed[1:])
	f.Add("")
	f.Add("AAAA")
	f.Add("not base64")
	f.Add(string(make([]byte, 1417)))
	f.Fuzz(func(t *testing.T, cursor string) {
		n := s.ran()
		rec := s.get(withCursor("/api/items", cursor, "limit", "3"), sessionA)
		switch cursor {
		case sealed, "":
			if rec.Code != http.StatusOK {
				t.Fatalf("a cursor the API sealed answered %d %q", rec.Code, rec.Body.String())
			}
		default:
			if env := envelopeOf(t, rec); rec.Code != http.StatusBadRequest || env.Code != "cursor_invalid" || s.ran() != n {
				t.Fatalf("cursor %q answered %d %q", cursor, rec.Code, env.Code)
			}
		}
	})
}
