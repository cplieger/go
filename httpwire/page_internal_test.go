package httpwire

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/go/sealed"
)

// TestOpenRefusesAPayloadOffTheLayout seals payloads SealNext and SealPrev
// never write, under the cursor's own purpose, so only the layout check
// can refuse them.
func TestOpenRefusesAPayloadOffTheLayout(t *testing.T) {
	s := sealed.New([32]byte{7})
	p := cursorPurpose("items.list", "hash", "u:acct1", nil)
	for name, tc := range map[string]struct {
		payload []byte
		dir     direction
		pos     []byte
		opens   bool
	}{
		"forward":                {[]byte{0, 'p'}, forward, []byte("p"), true},
		"backward, no position":  {[]byte{1}, backward, []byte{}, true},
		"an unknown direction":   {[]byte{2, 'p'}, 0, nil, false},
		"no direction byte":      {nil, 0, nil, false},
		"the largest direction":  {[]byte{0xFF}, 0, nil, false},
		"a position of 1 KiB":    {append([]byte{0}, make([]byte, maxPosition)...), forward, make([]byte, maxPosition), true},
		"a backward 1 KiB value": {append([]byte{1}, make([]byte, maxPosition)...), backward, make([]byte, maxPosition), true},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := s.Seal(p, tc.payload, cursorAge)
			if err != nil {
				t.Fatal(err)
			}
			dir, pos, err := openCursor(s, p, v)
			ok := err == nil
			if ok != tc.opens || dir != tc.dir || !bytes.Equal(pos, tc.pos) {
				t.Errorf("openCursor = %v, %q, %v, want %v, %q, %v", dir, pos, ok, tc.dir, tc.pos, tc.opens)
			}
		})
	}
	t.Run("another purpose", func(t *testing.T) {
		v, err := s.Seal(p, []byte{0, 'p'}, cursorAge)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := openCursor(s, cursorPurpose("items.list", "hash", "u:acct2", nil), v); err == nil {
			t.Error("a cursor opened for another owner")
		}
	})
}

// TestCursorPurposesNeverCollide holds field lists whose plain joins are
// equal, so only the length before each field keeps them apart.
func TestCursorPurposesNeverCollide(t *testing.T) {
	type parts struct {
		row, hash, owner string
		path             []string
	}
	for name, pair := range map[string][2]parts{
		"a colon moved across the row":    {{"a:b", "c", "d", nil}, {"a", "b:c", "d", nil}},
		"a colon moved across the owner":  {{"a", "b", "c:d", nil}, {"a", "b:c", "d", nil}},
		"a field moved into the row":      {{"ab", "", "", nil}, {"a", "b", "", nil}},
		"a field moved into the owner":    {{"", "", "ab", nil}, {"", "a", "b", nil}},
		"two path values joined":          {{"r", "h", "o", []string{"a", "b"}}, {"r", "h", "o", []string{"ab"}}},
		"an empty path value":             {{"r", "h", "o", []string{""}}, {"r", "h", "o", nil}},
		"the owner moved into the path":   {{"r", "h", "ox", nil}, {"r", "h", "o", []string{"x"}}},
		"a path value moved across a cut": {{"r", "h", "o", []string{"a/", "b"}}, {"r", "h", "o", []string{"a", "/b"}}},
	} {
		t.Run(name, func(t *testing.T) {
			x, y := pair[0], pair[1]
			if cursorPurpose(x.row, x.hash, x.owner, x.path) == cursorPurpose(y.row, y.hash, y.owner, y.path) {
				t.Errorf("%q and %q give one purpose", x, y)
			}
		})
	}
}

// TestOpenRefusesAValuePastTheBoundUnread measures allocations because a
// longer value fails to open either way: refused unread, it costs no
// decode buffer.
func TestOpenRefusesAValuePastTheBoundUnread(t *testing.T) {
	if want := sealed.ExpiringLen(1 + maxPosition); maxSealed != want || maxSealed != 1416 {
		t.Fatalf("maxSealed = %d, want sealed.ExpiringLen(1 + %d) = %d, which the URI-length sentence of docs/wire.md reads as 1,416", maxSealed, maxPosition, want)
	}
	s := sealed.New([32]byte{7})
	p := cursorPurpose("items.list", "", "", nil)
	longest, err := s.Seal(p, append([]byte{0}, make([]byte, maxPosition)...), cursorAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(longest) != maxSealed {
		t.Fatalf("a %d-byte position sealed to %d bytes, want %d", maxPosition, len(longest), maxSealed)
	}
	if _, _, err := openCursor(s, p, longest); err != nil {
		t.Error("the longest sealed value does not open")
	}
	over := longest + "A"
	if _, _, err := openCursor(s, p, over); err == nil {
		t.Error("a value a byte past the bound opens")
	}
	if n := testing.AllocsPerRun(10, func() { openCursor(s, p, over) }); n != 0 {
		t.Errorf("opening a value a byte past the bound allocated %v times, want 0", n)
	}
}

// TestPurposesAreHttpwiresOwn holds both purposes under httpwire's module
// name, as sealed names a purpose by its owner, so an app purpose on the
// shared Sealer named under its own module never equals one.
func TestPurposesAreHttpwiresOwn(t *testing.T) {
	for name, p := range map[string]sealed.Purpose{
		"a cursor": cursorPurpose("items.list", "hash", "u:acct1", nil),
		"a handle": handlePurpose("state", "s:a"),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.HasPrefix(string(p), "httpwire/") {
				t.Errorf("purpose %q is not under httpwire/", p)
			}
		})
	}
	if c, h := cursorPurpose("", "", "", nil), handlePurpose("", ""); strings.HasPrefix(string(h), string(c)) || strings.HasPrefix(string(c), string(h)) {
		t.Errorf("cursor purpose %q and handle purpose %q share a prefix", c, h)
	}
}
