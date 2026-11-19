package sealed

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// bubbleStart is the time a testing/synctest bubble's clock starts at,
// midnight UTC 2000-01-01 (Go testing/synctest docs, "Time").
var bubbleStart = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// opener is Open or OpenAtRest of one Sealer.
type opener func(Purpose, string) ([]byte, bool, error)

// kind is one kind of value: its seal, with a fixed one-hour ttl for an
// expiring value, and its open.
type kind struct {
	name string
	seal func(*Sealer, Purpose, []byte) (string, error)
	open func(*Sealer) opener
}

var kinds = []kind{
	{
		name: "expiring",
		seal: func(s *Sealer, p Purpose, b []byte) (string, error) { return s.Seal(p, b, time.Hour) },
		open: func(s *Sealer) opener { return s.Open },
	},
	{
		name: "at-rest",
		seal: (*Sealer).SealAtRest,
		open: func(s *Sealer) opener { return s.OpenAtRest },
	},
}

func mustSeal(t *testing.T, k kind, s *Sealer, p Purpose, payload []byte) string {
	t.Helper()
	v, err := k.seal(s, p, payload)
	if err != nil {
		t.Fatalf("seal %s: %v", k.name, err)
	}
	return v
}

// TestSealWireFormat checks a Seal value's bytes against docs/format.md:
// byte 0 is the version 0x01; bytes 1 to 12 are the nonce, which
// NewGCMWithRandomNonce prepends (Go crypto/cipher docs, Overhead 28); the
// rest is AES-256-GCM (RFC 5116 §5.2) under the expiring key of the RFC
// 5869 §2.2 and §2.3 transcription, which cipher.NewGCM opens with that
// nonce and A = 0x01 (NIST SP 800-38D (2007) §7.2) to the expiry,
// floor(now + ttl) as 8 big-endian Unix seconds, then the payload.
func TestSealWireFormat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const purpose Purpose = "example/wire"
		payload := []byte("wire format")
		time.Sleep(400 * time.Millisecond)
		v, err := New(k1).Seal(purpose, payload, 90*time.Minute+700*time.Millisecond)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		raw := rawOf(t, v)
		if want := 1 + 12 + 8 + len(payload) + 16; len(raw) != want {
			t.Fatalf("value is %d bytes, want %d", len(raw), want)
		}
		if raw[0] != 0x01 {
			t.Errorf("version byte = %#x, want 0x01", raw[0])
		}
		plain, err := refOpen(t, refValueKey(k1, "expiring", string(purpose)), raw)
		if err != nil {
			t.Fatalf("reference GCM open: %v", err)
		}
		if len(plain) != 8+len(payload) {
			t.Fatalf("plaintext is %d bytes, want %d", len(plain), 8+len(payload))
		}
		// 00:00:00.4 plus 1h30m0.7s is 01:30:01.1, rounded down to 01:30:01.
		wantExpiry := bubbleStart.Unix() + 90*60 + 1
		if got := int64(binary.BigEndian.Uint64(plain[:8])); got != wantExpiry {
			t.Errorf("expiry = %d, want %d", got, wantExpiry)
		}
		if !bytes.Equal(plain[8:], payload) {
			t.Errorf("payload = %q, want %q", plain[8:], payload)
		}
	})
}

// TestSealAtRestWireFormat checks a SealAtRest value's bytes as
// TestSealWireFormat does (RFC 5116 §5.2; NIST SP 800-38D (2007) §7.2; Go
// crypto/cipher NewGCMWithRandomNonce docs), under the at-rest key: the
// plaintext is the payload alone, with no expiry.
func TestSealAtRestWireFormat(t *testing.T) {
	const purpose Purpose = "example/wire"
	payload := []byte("wire format")
	v, err := New(k1).SealAtRest(purpose, payload)
	if err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	raw := rawOf(t, v)
	if want := 1 + 12 + len(payload) + 16; len(raw) != want {
		t.Fatalf("value is %d bytes, want %d", len(raw), want)
	}
	if raw[0] != 0x01 {
		t.Errorf("version byte = %#x, want 0x01", raw[0])
	}
	plain, err := refOpen(t, refValueKey(k1, "at-rest", string(purpose)), raw)
	if err != nil {
		t.Fatalf("reference GCM open: %v", err)
	}
	if !bytes.Equal(plain, payload) {
		t.Errorf("plaintext = %q, want the payload %q", plain, payload)
	}
}

// TestOpenRefusesTampering checks that every changed value is ErrInvalid:
// GCM answers FAIL when the tag does not match (NIST SP 800-38D (2007) §7.2
// step 8; RFC 5116 §2.2, a single FAIL output), and a version byte other
// than 0x01 is refused before GCM runs (docs/format.md, the open's step 3),
// since the additional data is the constant 0x01 and does not cover it.
func TestOpenRefusesTampering(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/tamper"
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			open := k.open(s)
			v := mustSeal(t, k, s, purpose, []byte("tamper"))
			if _, _, err := open(purpose, v); err != nil {
				t.Fatalf("untampered value: %v", err)
			}
			raw := rawOf(t, v)
			refused := func(t *testing.T, what, tampered string) {
				t.Helper()
				if p, cur, err := open(purpose, tampered); !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: open = %q, %v, %v; want ErrInvalid", what, p, cur, err)
				}
			}
			t.Run("bit flips", func(t *testing.T) {
				for i := range len(raw) * 8 {
					b := bytes.Clone(raw)
					b[i/8] ^= 1 << (i % 8)
					refused(t, fmt.Sprintf("bit %d", i), valueOf(b))
				}
			})
			t.Run("version bytes", func(t *testing.T) {
				for ver := range 256 {
					if ver == 0x01 {
						continue
					}
					b := bytes.Clone(raw)
					b[0] = byte(ver)
					refused(t, fmt.Sprintf("version %#x", ver), valueOf(b))
				}
			})
			t.Run("truncated", func(t *testing.T) {
				refused(t, "one byte short", valueOf(raw[:len(raw)-1]))
			})
			t.Run("extended", func(t *testing.T) {
				refused(t, "one byte more", valueOf(append(bytes.Clone(raw), 0)))
				refused(t, "one character more", v+"A")
			})
		})
	}
}

// TestNoncePerSeal checks that seals under one key draw distinct nonces:
// RFC 5116 §2.1 requires each nonce to be distinct and permits any method
// that meets that, and NIST SP 800-38D (2007) §8.2.2's construction draws a
// 96-bit random field. Two Sealers over one key stand in for one key across
// a restart, where a counter that starts again repeats.
func TestNoncePerSeal(t *testing.T) {
	const seals = 10000
	sealers := []*Sealer{New(k1), New(k1)}
	payload := []byte("one payload")
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			seen := make(map[[12]byte]int, seals)
			for i := range seals {
				raw := rawOf(t, mustSeal(t, k, sealers[i%2], "example/nonce", payload))
				nonce := [12]byte(raw[1:13])
				if j, ok := seen[nonce]; ok {
					t.Fatalf("seal %d repeats the nonce %x of seal %d", i, nonce, j)
				}
				seen[nonce] = i
			}
		})
	}
}

// TestPurposeBinding checks that a value opens for its own purpose only:
// GCM answers FAIL unless the key, nonce, additional data and ciphertext
// all match (RFC 5116 §2.2), and the key is derived from the purpose
// (docs/format.md's derivation table).
func TestPurposeBinding(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/token"
	others := []Purpose{
		"example/toke", "xample/token", "example/token/", "example/tokens", "",
		"example/token\x00", "\x00example/token", "example/t\x00ken", "EXAMPLE/TOKEN",
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			open := k.open(s)
			v := mustSeal(t, k, s, purpose, []byte("bound"))
			if got, _, err := open(purpose, v); err != nil || string(got) != "bound" {
				t.Fatalf("own purpose: %q, %v", got, err)
			}
			for _, other := range others {
				if p, cur, err := open(other, v); !errors.Is(err, ErrInvalid) {
					t.Errorf("purpose %q: open = %q, %v, %v; want ErrInvalid", other, p, cur, err)
				}
			}
		})
	}
	t.Run("NUL bytes", func(t *testing.T) {
		v, err := s.SealAtRest("a", []byte("bound"))
		if err != nil {
			t.Fatalf("SealAtRest: %v", err)
		}
		if _, _, err := s.OpenAtRest("a\x00", v); !errors.Is(err, ErrInvalid) {
			t.Errorf(`"a" value opened for "a\x00": %v, want ErrInvalid`, err)
		}
	})
}

// TestKindSeparation checks that neither kind opens the other's values for
// the same purpose, both ways (docs/format.md's derivation table: one key
// per kind), with at-rest payloads of 8 bytes and more, whose values are as
// long as an expiring value's.
func TestKindSeparation(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/kind"
	for _, payload := range [][]byte{nil, []byte("abcdefgh"), bytes.Repeat([]byte("abcdefgh"), 5)} {
		t.Run(fmt.Sprint(len(payload), " bytes"), func(t *testing.T) {
			atRest, err := s.SealAtRest(purpose, payload)
			if err != nil {
				t.Fatalf("SealAtRest: %v", err)
			}
			if p, cur, err := s.Open(purpose, atRest); !errors.Is(err, ErrInvalid) {
				t.Errorf("Open(at-rest value) = %q, %v, %v; want ErrInvalid", p, cur, err)
			}
			expiring, err := s.Seal(purpose, payload, time.Hour)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if p, cur, err := s.OpenAtRest(purpose, expiring); !errors.Is(err, ErrInvalid) {
				t.Errorf("OpenAtRest(expiring value) = %q, %v, %v; want ErrInvalid", p, cur, err)
			}
		})
	}
}

// TestExpiry checks the clock rule of docs/format.md: the expiry is
// floor(seal time + ttl) and a value opens while the time is before it,
// and a value that does not authenticate is ErrInvalid whatever its age.
// The bubble's fake clock (Go testing/synctest docs, "Time") moves only by
// the test's sleeps.
func TestExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const purpose Purpose = "example/expiry"
		s := New(k1)
		time.Sleep(700 * time.Millisecond)
		v, err := s.Seal(purpose, []byte("x"), 10*time.Minute)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		time.Sleep(time.Until(bubbleStart.Add(600*time.Second - time.Nanosecond)))
		if _, cur, err := s.Open(purpose, v); err != nil || !cur {
			t.Errorf("1 ns before the expiry: current %v, %v; want true, nil", cur, err)
		}
		time.Sleep(time.Nanosecond)
		if p, cur, err := s.Open(purpose, v); !errors.Is(err, ErrExpired) {
			t.Errorf("at the expiry: %q, %v, %v; want ErrExpired", p, cur, err)
		}
		foreign, err := New(k2).Seal(purpose, []byte("x"), time.Second)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		otherPurpose, err := s.Seal("example/other", []byte("x"), time.Second)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		time.Sleep(time.Hour)
		if p, cur, err := s.Open(purpose, foreign); !errors.Is(err, ErrInvalid) {
			t.Errorf("another key's expired value: %q, %v, %v; want ErrInvalid", p, cur, err)
		}
		if p, cur, err := s.Open(purpose, otherPurpose); !errors.Is(err, ErrInvalid) {
			t.Errorf("another purpose's expired value: %q, %v, %v; want ErrInvalid", p, cur, err)
		}
	})
}

// TestShortTTL checks that Seal refuses a ttl under one second and takes
// one second (docs/format.md: a value never starts expired).
func TestShortTTL(t *testing.T) {
	s := New(k1)
	for _, ttl := range []time.Duration{999 * time.Millisecond, 0, -time.Second, time.Duration(-1 << 63)} {
		if v, err := s.Seal("example/ttl", []byte("x"), ttl); !errors.Is(err, ErrShortTTL) || v != "" {
			t.Errorf("ttl %v: %q, %v; want ErrShortTTL", ttl, v, err)
		}
	}
	v, err := s.Seal("example/ttl", []byte("x"), time.Second)
	if err != nil {
		t.Fatalf("ttl 1s: %v", err)
	}
	if _, _, err := s.Open("example/ttl", v); err != nil {
		t.Errorf("ttl 1s value: %v", err)
	}
}

// b64Len is the length of n bytes in unpadded base64 (RFC 4648 §5 with §3.2's
// padding left out): every 6 bits a character, the last one partial.
func b64Len(n int) int { return (8*n + 5) / 6 }

// TestExpiringBound checks MaxPayload, ExpiringLen and Open's length bound:
// a user agent ignores a cookie whose name and value pass 4,096 octets
// (RFC 6265bis, draft-ietf-httpbis-rfc6265bis-22 §5.6 step 5), so MaxPayload
// is the longest payload whose value fits 4,096 characters; httpwire's
// cursor bound is 1,416 characters; lengths are base64url (RFC 4648 §5).
func TestExpiringBound(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/bound"
	t.Run("MaxPayload is the longest that fits a cookie", func(t *testing.T) {
		if got := b64Len(37 + MaxPayload); got > 4096 {
			t.Errorf("a MaxPayload value is %d characters, over 4,096", got)
		}
		if got := b64Len(37 + MaxPayload + 1); got <= 4096 {
			t.Errorf("a MaxPayload+1 value is %d characters, which fits 4,096", got)
		}
	})
	t.Run("MaxPayload seals and opens", func(t *testing.T) {
		payload := bytes.Repeat([]byte{0xa5}, MaxPayload)
		v, err := s.Seal(purpose, payload, time.Hour)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(v) != 4096 {
			t.Errorf("value is %d characters, want 4,096", len(v))
		}
		if got, _, err := s.Open(purpose, v); err != nil || !bytes.Equal(got, payload) {
			t.Errorf("Open: %d bytes, %v; want the payload", len(got), err)
		}
	})
	t.Run("over MaxPayload", func(t *testing.T) {
		payload := make([]byte, MaxPayload+1)
		for name, sealer := range map[string]*Sealer{"keyed": s, "zero": {}, "nil": nil} {
			if v, err := sealer.Seal(purpose, payload, time.Hour); !errors.Is(err, ErrTooLarge) || v != "" {
				t.Errorf("%s Sealer: %q, %v; want ErrTooLarge", name, v, err)
			}
		}
	})
	t.Run("lengths", func(t *testing.T) {
		for _, n := range []int{0, 1, 2, 3, 1024, 1025, 3035} {
			v, err := s.Seal(purpose, make([]byte, n), time.Hour)
			if err != nil {
				t.Fatalf("Seal(%d): %v", n, err)
			}
			if len(v) != b64Len(37+n) || ExpiringLen(n) != b64Len(37+n) {
				t.Errorf("n %d: value %d, ExpiringLen %d; want %d", n, len(v), ExpiringLen(n), b64Len(37+n))
			}
		}
	})
	t.Run("docs/format.md lengths", func(t *testing.T) {
		for n, want := range map[int]int{-1: 0, 0: 50, 1024: 1415, 1025: 1416, 3035: 4096, 3036: 0} {
			if got := ExpiringLen(n); got != want {
				t.Errorf("ExpiringLen(%d) = %d, want %d", n, got, want)
			}
		}
	})
	t.Run("oversize value", func(t *testing.T) {
		if p, cur, err := s.Open(purpose, strings.Repeat("A", 4097)); !errors.Is(err, ErrInvalid) {
			t.Errorf("4,097 characters: %q, %v, %v; want ErrInvalid", p, cur, err)
		}
	})
}

// TestAtRestBound checks MaxAtRestPayload, AtRestLen and OpenAtRest's
// length bound: 64 KiB stays under GCM's plaintext limit, len(P) ≤ 2^39 −
// 256 bits (NIST SP 800-38D (2007) §5.2.1.1, which RFC 5116 §5.1 and §5.2
// print as P_MAX, off by one octet per erratum 5219; no assertion uses
// the RFC's figure), and lengths are base64url (RFC 4648 §5).
func TestAtRestBound(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/bound"
	t.Run("under GCM's limit", func(t *testing.T) {
		if MaxAtRestPayload > (1<<39-256)/8 {
			t.Errorf("MaxAtRestPayload %d is over SP 800-38D's %d octets", MaxAtRestPayload, int64((1<<39-256)/8))
		}
	})
	t.Run("MaxAtRestPayload seals and opens", func(t *testing.T) {
		payload := bytes.Repeat([]byte{0x5a}, MaxAtRestPayload)
		v, err := s.SealAtRest(purpose, payload)
		if err != nil {
			t.Fatalf("SealAtRest: %v", err)
		}
		if len(v) != 87420 {
			t.Errorf("value is %d characters, want 87,420", len(v))
		}
		if got, _, err := s.OpenAtRest(purpose, v); err != nil || !bytes.Equal(got, payload) {
			t.Errorf("OpenAtRest: %d bytes, %v; want the payload", len(got), err)
		}
	})
	t.Run("over MaxAtRestPayload", func(t *testing.T) {
		payload := make([]byte, MaxAtRestPayload+1)
		for name, sealer := range map[string]*Sealer{"keyed": s, "zero": {}, "nil": nil} {
			if v, err := sealer.SealAtRest(purpose, payload); !errors.Is(err, ErrTooLarge) || v != "" {
				t.Errorf("%s Sealer: %q, %v; want ErrTooLarge", name, v, err)
			}
		}
	})
	t.Run("over MaxPayload", func(t *testing.T) {
		payload := bytes.Repeat([]byte{0x3c}, MaxPayload+1)
		v, err := s.SealAtRest(purpose, payload)
		if err != nil {
			t.Fatalf("SealAtRest of MaxPayload+1 bytes: %v", err)
		}
		if got, _, err := s.OpenAtRest(purpose, v); err != nil || !bytes.Equal(got, payload) {
			t.Errorf("OpenAtRest: %d bytes, %v; want the payload", len(got), err)
		}
	})
	t.Run("lengths", func(t *testing.T) {
		for _, n := range []int{0, 1, 2, 3, 3035, 3036, 65536} {
			v, err := s.SealAtRest(purpose, make([]byte, n))
			if err != nil {
				t.Fatalf("SealAtRest(%d): %v", n, err)
			}
			if len(v) != b64Len(29+n) || AtRestLen(n) != b64Len(29+n) {
				t.Errorf("n %d: value %d, AtRestLen %d; want %d", n, len(v), AtRestLen(n), b64Len(29+n))
			}
		}
	})
	t.Run("docs/format.md lengths", func(t *testing.T) {
		for n, want := range map[int]int{-1: 0, 0: 39, 3035: 4086, 65536: 87420, 65537: 0} {
			if got := AtRestLen(n); got != want {
				t.Errorf("AtRestLen(%d) = %d, want %d", n, got, want)
			}
		}
	})
	t.Run("oversize value", func(t *testing.T) {
		if p, cur, err := s.OpenAtRest(purpose, strings.Repeat("A", 87421)); !errors.Is(err, ErrInvalid) {
			t.Errorf("87,421 characters: %d bytes, %v, %v; want ErrInvalid", len(p), cur, err)
		}
	})
}

// TestOpenOversizeAllocatesNothing checks the open's step 1 in
// docs/format.md: a value over its kind's longest is refused before it is
// decoded, so it allocates nothing.
func TestOpenOversizeAllocatesNothing(t *testing.T) {
	s := New(k1)
	for _, tc := range []struct {
		name  string
		value string
		open  opener
	}{
		{"Open", strings.Repeat("A", 4097), s.Open},
		{"OpenAtRest", strings.Repeat("A", 87421), s.OpenAtRest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			allocs := testing.AllocsPerRun(1, func() { _, _, err = tc.open("example/bound", tc.value) })
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
			if allocs != 0 {
				t.Errorf("%v allocations, want 0", allocs)
			}
		})
	}
}

// TestStrictEncoding checks that a value is read only in its one spelling,
// unpadded base64url: padding (RFC 4648 §3.2), the standard alphabet's
// characters (§4, against §5's), a CR or LF, which a decoder may skip
// (§3.3), and non-zero trailing bits in the last character (§3.5) are each
// refused. A 4-byte expiring and a 5-byte at-rest payload end their values
// in a partial group, so each has padding to add and trailing bits to set.
func TestStrictEncoding(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/encoding"
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, k := range []struct {
		kind
		payload string
	}{
		{kinds[0], "hell"},
		{kinds[1], "hello"},
	} {
		t.Run(k.name, func(t *testing.T) {
			open := k.open(s)
			payload := []byte(k.payload)
			v := mustSeal(t, k.kind, s, purpose, payload)
			for tries := 0; !strings.ContainsAny(v, "-_"); tries++ {
				if tries == 1000 {
					t.Fatal("no value holding - or _ in 1,000 seals")
				}
				v = mustSeal(t, k.kind, s, purpose, payload)
			}
			if len(v)%4 == 0 {
				t.Fatalf("value of %d characters ends a whole group", len(v))
			}
			if got, _, err := open(purpose, v); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("canonical value: %q, %v", got, err)
			}
			last := strings.IndexByte(alphabet, v[len(v)-1])
			for name, variant := range map[string]string{
				"padding":           v + strings.Repeat("=", 4-len(v)%4),
				"standard alphabet": base64.RawStdEncoding.EncodeToString(rawOf(t, v)),
				"LF inside":         v[:10] + "\n" + v[10:],
				"CR inside":         v[:10] + "\r" + v[10:],
				"CRLF at the end":   v + "\r\n",
				"trailing bits":     v[:len(v)-1] + string(alphabet[last|1]),
			} {
				if p, cur, err := open(purpose, variant); !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: %q, %v, %v; want ErrInvalid", name, p, cur, err)
				}
			}
		})
	}
}

// TestRing checks the ring the way Tink's keysets define it ("Keysets":
// the primary key encrypts, and decryption tries every key in the
// keyset): the current key seals, a current-key value opens with current
// true, a previous-key value with current false, and a value of a key the
// ring does not hold is ErrInvalid.
func TestRing(t *testing.T) {
	r := ringSealer(t, k2, k1)
	cur, prev, third := New(k2), New(k1), New(k3)
	const purpose Purpose = "example/ring"
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			open := k.open(r)
			if got, current, err := open(purpose, mustSeal(t, k, cur, purpose, []byte("c"))); err != nil || !current || string(got) != "c" {
				t.Errorf("current key's value: %q, current %v, %v; want c, true", got, current, err)
			}
			got, current, err := open(purpose, mustSeal(t, k, prev, purpose, []byte("p")))
			if err != nil || current || string(got) != "p" {
				t.Errorf("previous key's value: %q, current %v, %v; want p, false", got, current, err)
			}
			if p, current, err := open(purpose, mustSeal(t, k, third, purpose, []byte("x"))); !errors.Is(err, ErrInvalid) {
				t.Errorf("third key's value: %q, current %v, %v; want ErrInvalid", p, current, err)
			}
			resealed := mustSeal(t, k, r, purpose, got)
			if got, current, err := open(purpose, resealed); err != nil || !current || string(got) != "p" {
				t.Errorf("re-sealed value: %q, current %v, %v; want p, true", got, current, err)
			}
			if _, current, err := k.open(cur)(purpose, resealed); err != nil || !current {
				t.Errorf("re-sealed value under the current key alone: current %v, %v", current, err)
			}
			if _, _, err := k.open(prev)(purpose, resealed); !errors.Is(err, ErrInvalid) {
				t.Errorf("re-sealed value under the previous key alone: %v, want ErrInvalid", err)
			}
		})
	}
}

// TestCurrentFalseOnError checks docs/format.md's rule that current is
// false whenever an open answers an error, under a two-key ring, for each
// kind of refusal, values under the current key that fail a later check
// included.
func TestCurrentFalseOnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := ringSealer(t, k2, k1)
		const purpose Purpose = "example/current"
		expiring, err := r.Seal(purpose, []byte("x"), time.Minute)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		atRest, err := r.SealAtRest(purpose, []byte("x"))
		if err != nil {
			t.Fatalf("SealAtRest: %v", err)
		}
		id, tag, err := r.MAC("example/mac", []byte("msg"))
		if err != nil {
			t.Fatalf("MAC: %v", err)
		}
		foreignID, foreignTag, err := New(k3).MAC("example/mac", []byte("msg"))
		if err != nil {
			t.Fatalf("MAC: %v", err)
		}
		foreignExpiring, err := New(k3).Seal(purpose, []byte("x"), time.Hour)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		foreignAtRest, err := New(k3).SealAtRest(purpose, []byte("x"))
		if err != nil {
			t.Fatalf("SealAtRest: %v", err)
		}
		fresh, err := r.Seal(purpose, []byte("x"), time.Hour)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		time.Sleep(2 * time.Minute)
		flip := func(v string) string {
			raw := rawOf(t, v)
			raw[len(raw)-1] ^= 1
			return valueOf(raw)
		}
		flipped := bytes.Clone(tag)
		flipped[0] ^= 1
		keyless := &Sealer{}
		for name, call := range map[string]func() (bool, error){
			"Open bit flip":            func() (bool, error) { _, c, err := r.Open(purpose, flip(fresh)); return c, err },
			"Open foreign key":         func() (bool, error) { _, c, err := r.Open(purpose, foreignExpiring); return c, err },
			"Open expired":             func() (bool, error) { _, c, err := r.Open(purpose, expiring); return c, err },
			"Open keyless":             func() (bool, error) { _, c, err := keyless.Open(purpose, fresh); return c, err },
			"Open wrong length":        func() (bool, error) { _, c, err := r.Open(purpose, fresh[:len(fresh)-4]); return c, err },
			"OpenAtRest bit flip":      func() (bool, error) { _, c, err := r.OpenAtRest(purpose, flip(atRest)); return c, err },
			"OpenAtRest foreign key":   func() (bool, error) { _, c, err := r.OpenAtRest(purpose, foreignAtRest); return c, err },
			"OpenAtRest keyless":       func() (bool, error) { _, c, err := keyless.OpenAtRest(purpose, atRest); return c, err },
			"OpenAtRest wrong length":  func() (bool, error) { _, c, err := r.OpenAtRest(purpose, atRest[:len(atRest)-4]); return c, err },
			"VerifyMAC bit flip":       func() (bool, error) { return r.VerifyMAC("example/mac", []byte("msg"), id, flipped) },
			"VerifyMAC changed msg":    func() (bool, error) { return r.VerifyMAC("example/mac", []byte("msh"), id, tag) },
			"VerifyMAC foreign key":    func() (bool, error) { return r.VerifyMAC("example/mac", []byte("msg"), foreignID, foreignTag) },
			"VerifyMAC keyless":        func() (bool, error) { return keyless.VerifyMAC("example/mac", []byte("msg"), id, tag) },
			"VerifyMAC wrong length":   func() (bool, error) { return r.VerifyMAC("example/mac", []byte("msg"), id, tag[:31]) },
			"VerifyMAC foreign key id": func() (bool, error) { return r.VerifyMAC("example/mac", []byte("msg"), foreignID, tag) },
		} {
			current, err := call()
			if err == nil {
				t.Errorf("%s: no error", name)
			}
			if current {
				t.Errorf("%s: current true beside %v", name, err)
			}
		}
	})
}

// TestNoKey checks that every Sealer holding no key answers ErrNoKey, with
// zero values, from every call that needs a key, and panics on none: the
// zero Sealer, a nil *Sealer and New of the all-zero array, as the Sealer
// and New godoc say, so each zero value says it does not work.
func TestNoKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *Sealer
	}{
		{"zero Sealer", &Sealer{}},
		{"nil Sealer", nil},
		{"New of zero", New([32]byte{})},
	} {
		t.Run(tc.name, func(t *testing.T) { checkNoKey(t, tc.s) })
	}
}

// TestEphemeral checks that each NewEphemeral holds a key of its own
// (its godoc: one random key no one else holds).
func TestEphemeral(t *testing.T) {
	a, b := NewEphemeral(), NewEphemeral()
	const purpose Purpose = "example/ephemeral"
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			for _, pair := range [][2]*Sealer{{a, b}, {b, a}} {
				v := mustSeal(t, k, pair[0], purpose, []byte("own"))
				if got, cur, err := k.open(pair[0])(purpose, v); err != nil || !cur || string(got) != "own" {
					t.Errorf("own value: %q, %v, %v", got, cur, err)
				}
				if p, cur, err := k.open(pair[1])(purpose, v); !errors.Is(err, ErrInvalid) {
					t.Errorf("other's value: %q, %v, %v; want ErrInvalid", p, cur, err)
				}
			}
		})
	}
}

// TestNew checks that New uses its key as the ring key: New(K1) opens the
// values of testdata/vectors.json, which an independent implementation
// computed from docs/format.md's definitions (the expiring one inside a
// bubble, before its 2100 expiry), and what it seals opens under the RFC
// 5869 transcription's key.
func TestNew(t *testing.T) {
	vec := loadVectors(t)
	vals := vec.Values
	key := vec.key(t, vals.Key)
	s := New(key)
	synctest.Test(t, func(t *testing.T) {
		got, cur, err := s.Open(Purpose(vals.Purpose), vals.Expiring)
		if err != nil || !cur || string(got) != vals.Payload {
			t.Errorf("expiring vector: %q, %v, %v; want %q", got, cur, err, vals.Payload)
		}
	})
	got, cur, err := s.OpenAtRest(Purpose(vals.Purpose), vals.AtRest)
	if err != nil || !cur || string(got) != vals.Payload {
		t.Errorf("at-rest vector: %q, %v, %v; want %q", got, cur, err, vals.Payload)
	}
	for _, tc := range []struct {
		kind  string
		value func() (string, error)
		frame int
	}{
		{"expiring", func() (string, error) { return s.Seal("example/new", []byte("new"), time.Hour) }, 8},
		{"at-rest", func() (string, error) { return s.SealAtRest("example/new", []byte("new")) }, 0},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			v, err := tc.value()
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			plain, err := refOpen(t, refValueKey(key, tc.kind, "example/new"), rawOf(t, v))
			if err != nil || len(plain) != tc.frame+3 || string(plain[tc.frame:]) != "new" {
				t.Errorf("reference open: %q, %v; want new after %d bytes", plain, err, tc.frame)
			}
		})
	}
}

// TestPayloadCopies checks that a Sealer keeps no caller slice (the opens'
// godoc: the payload is a new slice): changing a payload after its seal, or
// a payload an open returned, changes no later open.
func TestPayloadCopies(t *testing.T) {
	s := New(k1)
	const purpose Purpose = "example/copies"
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			payload := []byte("original")
			v := mustSeal(t, k, s, purpose, payload)
			payload[0] = 'X'
			got, _, err := k.open(s)(purpose, v)
			if err != nil || string(got) != "original" {
				t.Fatalf("after changing the sealed payload: %q, %v", got, err)
			}
			got[0] = 'Y'
			again, _, err := k.open(s)(purpose, v)
			if err != nil || string(again) != "original" {
				t.Errorf("after changing an opened payload: %q, %v", again, err)
			}
		})
	}
}

// The format fixture: values the production path sealed under K1 when the
// format was first released, which every later release must open.
const (
	fixturePath    = "testdata/v1-fixture.txt"
	fixtureRegen   = "UPDATE_GOLDEN=1 go test -run '^TestFormatFixture$' ."
	fixturePurpose = Purpose("example/fixture")
	fixturePayload = "sealed by the first release"
	// fixtureTTL keeps the fixture's expiring value open for a century.
	fixtureTTL = 100 * 365 * 24 * time.Hour
)

// TestFormatFixture checks that the values of testdata/v1-fixture.txt still
// open to their payload with current (docs/format.md, "Versioning": a
// release opens every version it ever sealed), the expiring one until the
// expiry second its frame holds, read as docs/format.md defines it (8
// big-endian Unix seconds before the payload) by the reference opener. The
// fixture is the production path's own output, so TestVectors pairs it
// with independent values: a fixture rewritten from a broken format still
// fails there.
func TestFormatFixture(t *testing.T) {
	s := New(k1)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeFixture(t, s)
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read the fixture: %v; write it with %s", err, fixtureRegen)
	}
	values := map[string]string{}
	for line := range strings.Lines(string(data)) {
		name, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		if !ok {
			t.Fatalf("fixture line %q is not a kind, a space and a value; rewrite it with %s", line, fixtureRegen)
		}
		values[name] = value
	}
	value := func(t *testing.T, kind string) string {
		t.Helper()
		v, ok := values[kind]
		if !ok {
			t.Fatalf("the fixture holds no %s value; rewrite it with %s", kind, fixtureRegen)
		}
		return v
	}
	check := func(t *testing.T, kind string, got []byte, current bool, err error) {
		t.Helper()
		if err != nil || !current || string(got) != fixturePayload {
			t.Errorf("the fixture's %s value opens to %q, %v, %v; want %q, true, nil. "+
				"The format changed, and a release must open what an earlier one sealed: "+
				"rewrite the fixture with %s only for a format change that is meant",
				kind, got, current, err, fixturePayload, fixtureRegen)
		}
	}
	t.Run("expiring", func(t *testing.T) {
		v := value(t, "expiring")
		plain, err := refOpen(t, refValueKey(k1, "expiring", string(fixturePurpose)), rawOf(t, v))
		if err != nil || len(plain) < 8 {
			t.Fatalf("the fixture's expiring value does not open as docs/format.md defines it: %d bytes, %v", len(plain), err)
		}
		expiry := time.Unix(int64(binary.BigEndian.Uint64(plain)), 0)
		synctest.Test(t, func(t *testing.T) {
			time.Sleep(time.Until(expiry) - time.Nanosecond)
			got, current, err := s.Open(fixturePurpose, v)
			check(t, "expiring", got, current, err)
			time.Sleep(time.Nanosecond)
			if got, current, err := s.Open(fixturePurpose, v); !errors.Is(err, ErrExpired) {
				t.Errorf("at the fixture's expiry %v: %q, %v, %v; want ErrExpired", expiry.UTC(), got, current, err)
			}
		})
	})
	t.Run("at-rest", func(t *testing.T) {
		got, current, err := s.OpenAtRest(fixturePurpose, value(t, "at-rest"))
		check(t, "at-rest", got, current, err)
	})
}

// writeFixture rewrites the format fixture with values s seals now.
func writeFixture(t *testing.T, s *Sealer) {
	t.Helper()
	expiring, err := s.Seal(fixturePurpose, []byte(fixturePayload), fixtureTTL)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	atRest, err := s.SealAtRest(fixturePurpose, []byte(fixturePayload))
	if err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	data := "expiring " + expiring + "\nat-rest " + atRest + "\n"
	if err := os.WriteFile(fixturePath, []byte(data), 0o644); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
}
