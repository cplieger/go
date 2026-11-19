package httpwire

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// TestOpenRefusesAHeaderOffTheLayout seals plaintexts the sealer never
// writes, under its own key and additional data, so only the header
// checks can refuse them.
func TestOpenRefusesAHeaderOffTheLayout(t *testing.T) {
	s, err := newSealer([32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	aad := additionalData(cursorDomain, "items.list")
	now := uint64(time.Now().Unix())
	header := func(format, dir byte, secs uint64) []byte {
		return binary.BigEndian.AppendUint64([]byte{format, dir}, secs)
	}
	for name, tc := range map[string]struct {
		plaintext []byte
		opens     bool
	}{
		"the layout, forward":        {append(header(1, 0, now), 'p'), true},
		"the layout, backward":       {header(1, 1, now), true},
		"an unknown format":          {append(header(2, 0, now), 'p'), false},
		"format zero":                {header(0, 0, now), false},
		"an unknown direction":       {header(1, 2, now), false},
		"a header cut short":         {header(1, 0, now)[:9], false},
		"an issue time past int64":   {header(1, 0, math.MaxInt64+1), false},
		"an issue time past the age": {header(1, 0, now-uint64(cursorAge/time.Second)-2), false},
	} {
		t.Run(name, func(t *testing.T) {
			sealed := sealedText.EncodeToString(s.aead.Seal(nil, nil, tc.plaintext, aad))
			if _, _, ok := s.open(sealed, aad, cursorAge); ok != tc.opens {
				t.Errorf("open = %v, want %v", ok, tc.opens)
			}
		})
	}
}

// TestOpenRefusesAValuePastTheBoundUnread measures allocations because a
// longer value fails to open either way: refused unread, it costs no
// decode buffer.
func TestOpenRefusesAValuePastTheBoundUnread(t *testing.T) {
	s, err := newSealer([32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	aad := additionalData(cursorDomain, "items.list")
	longest, err := s.seal(forward, make([]byte, maxPosition), aad)
	if err != nil {
		t.Fatal(err)
	}
	if len(longest) != maxSealed {
		t.Fatalf("a %d-byte position sealed to %d bytes, want %d", maxPosition, len(longest), maxSealed)
	}
	if _, _, ok := s.open(longest, aad, cursorAge); !ok {
		t.Error("the longest sealed value does not open")
	}
	over := longest + "A"
	if _, _, ok := s.open(over, aad, cursorAge); ok {
		t.Error("a value a byte past the bound opens")
	}
	if n := testing.AllocsPerRun(10, func() { s.open(over, aad, cursorAge) }); n != 0 {
		t.Errorf("opening a value a byte past the bound allocated %v times, want 0", n)
	}
}
