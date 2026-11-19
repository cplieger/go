package sealed

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

// FuzzOpen_onlyOwnValues checks that Open never panics and opens exactly
// the values docs/format.md lays out for its purpose under its ring's two
// keys: one cipher.NewGCM (NIST SP 800-38D §7.2) authenticates under the
// expiring key of the RFC 5869 transcription opens before its expiry, with
// current true under the current key, and is ErrExpired after it; any
// other value is ErrInvalid (RFC 5116 §2.2), every one-bit flip of an
// authentic value included.
func FuzzOpen_onlyOwnValues(f *testing.F) {
	v := loadVectors(f)
	for _, seed := range openSeeds(f, v, 4097) {
		f.Add(seed)
	}
	s, own := ownValuesOf(f, v, "expiring")
	f.Fuzz(func(t *testing.T, value string) { own.check(t, s, value) })
}

// FuzzOpenAtRest_onlyOwnValues checks OpenAtRest as FuzzOpen_onlyOwnValues
// checks Open, under the at-rest key with no expiry, so it never opens a
// Seal value.
func FuzzOpenAtRest_onlyOwnValues(f *testing.F) {
	v := loadVectors(f)
	for _, seed := range openSeeds(f, v, 87421) {
		f.Add(seed)
	}
	s, own := ownValuesOf(f, v, "at-rest")
	f.Fuzz(func(t *testing.T, value string) { own.check(t, s, value) })
}

// FuzzSeal_roundTrip checks that, for any purpose and a payload of n bytes
// within its kind's bound, Seal's value is the unpadded base64url (RFC 4648
// §3.2) of 37 + n bytes, as ExpiringLen says, opens through Open to the
// payload with current true, and opens through cipher.NewGCM (NIST SP
// 800-38D §7.2) under the current key's expiring key for the purpose to an
// expiry an hour on and the payload; that SealAtRest's does the same with
// 29 + n bytes, AtRestLen and the at-rest key; and that a payload over a
// kind's bound is ErrTooLarge.
func FuzzSeal_roundTrip(f *testing.F) {
	f.Add("", []byte{})
	f.Add("example", []byte("hello"))
	f.Add("example\x00nul", []byte{0, 'x', 0})
	f.Add("example/max", bytes.Repeat([]byte{0xff}, MaxPayload))
	f.Add("example/over", bytes.Repeat([]byte{0xff}, MaxPayload+1))
	f.Add("example/max-at-rest", bytes.Repeat([]byte{0xff}, MaxAtRestPayload))
	f.Add("example/over-at-rest", bytes.Repeat([]byte{0xff}, MaxAtRestPayload+1))
	v := loadVectors(f)
	cur := v.key(f, "K2")
	s := ringSealer(f, cur, v.key(f, "K1"))
	expiring := roundTrip{kind: "expiring", open: s.Open, lenOf: ExpiringLen, key: cur, overhead: 37}
	atRest := roundTrip{kind: "at-rest", open: s.OpenAtRest, lenOf: AtRestLen, key: cur, overhead: 29}
	f.Fuzz(func(t *testing.T, purpose string, payload []byte) {
		before := time.Now()
		value, err := s.Seal(Purpose(purpose), payload, time.Hour)
		after := time.Now()
		if len(payload) > MaxPayload {
			checkTooLarge(t, "Seal", len(payload), value, err)
		} else {
			plain := expiring.check(t, purpose, payload, value, err)
			lo, hi := before.Add(time.Hour).Unix(), after.Add(time.Hour).Unix()
			if expiry := int64(binary.BigEndian.Uint64(plain)); expiry < lo || expiry > hi {
				t.Fatalf("expiry %d, want %d to %d", expiry, lo, hi)
			}
			if !bytes.Equal(plain[8:], payload) {
				t.Fatalf("reference payload %q, want %q", plain[8:], payload)
			}
		}
		value, err = s.SealAtRest(Purpose(purpose), payload)
		if len(payload) > MaxAtRestPayload {
			checkTooLarge(t, "SealAtRest", len(payload), value, err)
		} else if plain := atRest.check(t, purpose, payload, value, err); !bytes.Equal(plain, payload) {
			t.Fatalf("reference plaintext %q, want the payload %q", plain, payload)
		}
	})
}

// checkTooLarge checks that the seal of an n-byte payload over its kind's
// bound answered ErrTooLarge and no value.
func checkTooLarge(t *testing.T, seal string, n int, value string, err error) {
	t.Helper()
	if value != "" || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%s of %d bytes = %q, %v; want ErrTooLarge", seal, n, value, err)
	}
}

// roundTrip is one kind of value as FuzzSeal_roundTrip sees it: its open,
// its length function, the ring's current key, and the bytes a value adds
// to its payload (docs/format.md).
type roundTrip struct {
	open     opener
	lenOf    func(int) int
	kind     string
	key      [keyLen]byte
	overhead int
}

// check checks a value sealed from payload for purpose: no error, the
// length of the unpadded base64url of overhead + n bytes, which lenOf
// answers too, and open's payload with current true. It returns the
// value's plaintext under the reference GCM key of r's kind and purpose.
func (r *roundTrip) check(t *testing.T, purpose string, payload []byte, value string, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatalf("seal %s of %d bytes: %v", r.kind, len(payload), err)
	}
	if want := (4*(r.overhead+len(payload)) + 2) / 3; len(value) != want || r.lenOf(len(payload)) != want {
		t.Fatalf("%s value of %d bytes is %d characters and its length function says %d; want %d", r.kind, len(payload), len(value), r.lenOf(len(payload)), want)
	}
	if got, current, err := r.open(Purpose(purpose), value); err != nil || !current || !bytes.Equal(got, payload) {
		t.Fatalf("open %s = %q, %v, %v; want the payload, true, nil", r.kind, got, current, err)
	}
	plain, err := refOpen(t, refValueKey(r.key, r.kind, purpose), rawOf(t, value))
	if err != nil {
		t.Fatalf("reference GCM open of the %s value: %v", r.kind, err)
	}
	return plain
}

// ownValues is what an open target holds one kind of value to, from
// docs/format.md: the most characters a value takes and the fewest bytes it
// decodes to, whether its plaintext starts with an 8-byte expiry, and its
// GCM key for the purpose under the ring's current, then previous, key.
type ownValues struct {
	open     func(*Sealer) opener
	purpose  Purpose
	keys     [2][]byte
	maxLen   int
	minRaw   int
	expiring bool
}

// ownValuesOf returns the ring of the vectors' K2, current, and K1, and
// what the open of kind ("expiring" or "at-rest") is held to for the
// vectors' purpose.
func ownValuesOf(f *testing.F, v *testVectors, kind string) (*Sealer, *ownValues) {
	cur, prev := v.key(f, "K2"), v.key(f, "K1")
	own := &ownValues{
		purpose: Purpose(v.Values.Purpose),
		keys:    [2][]byte{refValueKey(cur, kind, v.Values.Purpose), refValueKey(prev, kind, v.Values.Purpose)},
	}
	if kind == "expiring" {
		own.open = func(s *Sealer) opener { return s.Open }
		own.maxLen, own.minRaw, own.expiring = 4096, 37, true
	} else {
		own.open = func(s *Sealer) opener { return s.OpenAtRest }
		own.maxLen, own.minRaw = 87420, 29
	}
	return ringSealer(f, cur, prev), own
}

// authentic returns the plaintext of value and the index of the ring key
// that authenticates it, when it is a value of o's kind: at most maxLen
// characters, the one unpadded base64url spelling of its bytes (RFC 4648
// §3.5), at least minRaw bytes, version 0x01, and a GCM tag that verifies.
func (o *ownValues) authentic(t *testing.T, value string) (plain []byte, keyIndex int, ok bool) {
	if len(value) > o.maxLen {
		return nil, 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value || len(raw) < o.minRaw || raw[0] != 0x01 {
		return nil, 0, false
	}
	for i, key := range o.keys {
		if plain, err := refOpen(t, key, raw); err == nil {
			return plain, i, true
		}
	}
	return nil, 0, false
}

// check holds s's open of value to what authentic finds, reading the
// clock before and after the open, since the open reads it between.
func (o *ownValues) check(t *testing.T, s *Sealer, value string) {
	open := o.open(s)
	before := time.Now()
	payload, current, err := open(o.purpose, value)
	after := time.Now()
	plain, keyIndex, ok := o.authentic(t, value)
	if !ok {
		if payload != nil || current || !errors.Is(err, ErrInvalid) {
			t.Fatalf("a value made under no ring key for the purpose opened to %q, %v, %v; want ErrInvalid", payload, current, err)
		}
		return
	}
	var expiry time.Time
	if o.expiring {
		expiry = time.Unix(int64(binary.BigEndian.Uint64(plain)), 0)
		plain = plain[8:]
	}
	switch {
	case err == nil:
		if o.expiring && !before.Before(expiry) {
			t.Fatalf("opened a value that expired at %v", expiry)
		}
		if !bytes.Equal(payload, plain) || current != (keyIndex == 0) {
			t.Fatalf("open = %q, %v; want %q, %v", payload, current, plain, keyIndex == 0)
		}
	case errors.Is(err, ErrExpired):
		if !o.expiring || after.Before(expiry) || payload != nil || current {
			t.Fatalf("open = %q, %v, %v of a value expiring at %v", payload, current, err, expiry)
		}
	default:
		t.Fatalf("an authentic value: open = %q, %v, %v", payload, current, err)
	}
	raw := rawOf(t, value)
	for bit := range len(raw) * 8 {
		b := bytes.Clone(raw)
		b[bit/8] ^= 1 << (bit % 8)
		if got, cur, err := open(o.purpose, valueOf(b)); got != nil || cur || !errors.Is(err, ErrInvalid) {
			t.Fatalf("bit %d flipped: open = %q, %v, %v; want ErrInvalid", bit, got, cur, err)
		}
	}
}

// openSeeds returns the open targets' seeds: the vectors' two values, built
// with their fixed nonce through refSeal, which the builder must rebuild
// exactly; the same under K2, the ring's current key, with expiries in the
// future, the past and before 1970; values for another purpose and under a
// key the ring lacks; an empty string, AAAA, a run of tooLong A characters,
// and the vectors' values with a line feed inside.
func openSeeds(f *testing.F, v *testVectors, tooLong int) []string {
	vals := v.Values
	expiring := func(key [keyLen]byte, purpose string, expiry int64) string {
		plain := binary.BigEndian.AppendUint64(nil, uint64(expiry))
		return refSeal(f, refValueKey(key, "expiring", purpose), vals.Nonce, append(plain, vals.Payload...))
	}
	atRest := func(key [keyLen]byte, purpose string) string {
		return refSeal(f, refValueKey(key, "at-rest", purpose), vals.Nonce, []byte(vals.Payload))
	}
	cur, prev := v.key(f, "K2"), v.key(f, "K1")
	ve, va := expiring(prev, vals.Purpose, vals.Expiry), atRest(prev, vals.Purpose)
	if ve != vals.Expiring || va != vals.AtRest {
		f.Fatalf("the seed builder made %s and %s, not the vectors' values", ve, va)
	}
	return []string{
		ve, va,
		expiring(cur, vals.Purpose, vals.Expiry), atRest(cur, vals.Purpose),
		expiring(cur, vals.Purpose, bubbleStart.Unix()), expiring(cur, vals.Purpose, -1),
		expiring(cur, "example/other", vals.Expiry), atRest(cur, "example/other"),
		expiring(k3, vals.Purpose, vals.Expiry), atRest(k3, vals.Purpose),
		"", "AAAA", strings.Repeat("A", tooLong),
		ve[:20] + "\n" + ve[20:], va[:20] + "\n" + va[20:],
	}
}
