package httpwire

import (
	"bytes"
	"testing"

	"github.com/cplieger/go/sealed"
)

// TestOpenRefusesAPayloadOffTheLayout seals payloads SealNext and SealPrev
// never write, under the cursor's own purpose, so only the layout check
// can refuse them.
func TestOpenRefusesAPayloadOffTheLayout(t *testing.T) {
	s := sealed.New([32]byte{7})
	p := cursorPurpose("items.list", "hash", "u:acct1")
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
		if _, _, err := openCursor(s, cursorPurpose("items.list", "hash", "u:acct2"), v); err == nil {
			t.Error("a cursor opened for another owner")
		}
	})
}

// TestCursorPurposesNeverCollide holds field lists whose plain joins are
// equal, so only the length before each field keeps them apart.
func TestCursorPurposesNeverCollide(t *testing.T) {
	for name, pair := range map[string][2][3]string{
		"a colon moved across the row":   {{"a:b", "c", "d"}, {"a", "b:c", "d"}},
		"a colon moved across the owner": {{"a", "b", "c:d"}, {"a", "b:c", "d"}},
		"a field moved into the row":     {{"ab", "", ""}, {"a", "b", ""}},
		"a field moved into the owner":   {{"", "", "ab"}, {"", "a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			x, y := pair[0], pair[1]
			if cursorPurpose(x[0], x[1], x[2]) == cursorPurpose(y[0], y[1], y[2]) {
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
	p := cursorPurpose("items.list", "", "")
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
