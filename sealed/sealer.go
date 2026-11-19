package sealed

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

// MaxPayload is the longest payload Seal takes. Seal's value for it is
// 4,096 characters, the most a browser keeps for a cookie's name and value
// together, so a cookie's payload must leave room for its name.
const MaxPayload = 3035

// MaxAtRestPayload is the longest payload SealAtRest takes, 64 KiB. An
// at-rest value is kept by a store, never sent in a cookie or a URL, so it
// is not held to MaxPayload; the bound limits what opening an untrusted
// stored value can cost.
const MaxAtRestPayload = 64 << 10

const (
	// version is a value's first byte and the GCM additional data of every
	// value this release seals.
	version          = 0x01
	nonceLen         = 12
	tagLen           = 16
	expiryLen        = 8
	atRestOverhead   = 1 + nonceLen + tagLen
	expiringOverhead = atRestOverhead + expiryLen
)

// additionalData is the GCM additional data: the constant version, never
// the byte a value carries, so a value of another version fails GCM.
var additionalData = []byte{version}

// b64 is the value encoding: unpadded base64url (RFC 4648 §5) whose
// decoder refuses non-zero trailing bits (§3.5).
var b64 = base64.RawURLEncoding.Strict()

// ExpiringLen is the length of every value Seal makes from an n-byte payload:
// unpadded base64url of 37 + n bytes, 50 to 4,096 characters. It returns 0
// for an n outside 0 to MaxPayload, which Seal refuses.
func ExpiringLen(n int) int {
	if n < 0 || n > MaxPayload {
		return 0
	}
	return b64.EncodedLen(expiringOverhead + n)
}

// AtRestLen is the length of every value SealAtRest makes from an n-byte
// payload: unpadded base64url of 29 + n bytes, 39 to 87,420 characters. It
// returns 0 for an n outside 0 to MaxAtRestPayload, which SealAtRest
// refuses.
func AtRestLen(n int) int {
	if n < 0 || n > MaxAtRestPayload {
		return 0
	}
	return b64.EncodedLen(atRestOverhead + n)
}

// Purpose is what a value is bound to: any bytes, compared exactly. A value
// opens only for the purpose it was sealed for, because its key is derived
// from the purpose, and the purpose is not written into the value. Declare
// each purpose once, as a constant of the package that seals it; join
// variable parts with an encoding no two part lists share, such as a length
// before each part.
type Purpose string

// KeyIDs names the keys a Sealer holds. An id is 32 lowercase hex
// characters derived from its key: it names the key and reveals nothing of
// it, so an app may record it beside what it sealed, and an archive may
// carry it. An empty id means no such key.
type KeyIDs struct {
	// Current is the id of the key that seals.
	Current string
	// Previous is the id of the key a pending rotation keeps for opening.
	Previous string
}

// Sealer seals and opens values under a ring of at most two keys: the
// current key seals, and the current and previous keys open. Build one with
// KeyFile.Sealer, New or NewEphemeral. The zero Sealer and a nil *Sealer
// hold no key, so every call that needs one answers ErrNoKey. A Sealer is
// safe for concurrent use, and no printer shows its keys.
type Sealer struct {
	src *keySource
}

// New returns a Sealer over key alone, for a library that receives one key.
// It holds no previous key, so nothing it sealed opens once the caller
// replaces key: it suits values a rotation may invalidate, such as cursors
// and handles, never values a store keeps. New of the all-zero array holds
// no key, so a key option that was never set never seals.
func New(key [32]byte) *Sealer {
	if key == [keyLen]byte{} {
		return &Sealer{}
	}
	return newSealer(key)
}

// NewEphemeral returns a Sealer over one random key no one else holds: what
// it seals stops opening when the process ends. It suits tests, and an app
// that keeps nothing sealed across a restart.
func NewEphemeral() *Sealer { return newSealer(randomKey()) }

// newSealer returns a Sealer whose own source holds key alone.
func newSealer(key [keyLen]byte) *Sealer {
	rk, err := newRingKey(key)
	if err != nil {
		// Unreachable: hkdf.Key fails only for an over-long output or a key under 112 bits.
		return &Sealer{}
	}
	src := new(keySource)
	src.ring.Store(&ring{keys: [2]ringKey{rk}, n: 1})
	return &Sealer{src: src}
}

// load returns s's ring, or nil when s holds no key.
func (s *Sealer) load() *ring {
	if s == nil {
		return nil
	}
	return s.src.load()
}

// Seal returns a value that Open, with purpose, opens to payload until ttl
// from now, rounded down to the whole second, sealed under the current key
// with a fresh random 96-bit nonce. Past 2^32 seals of one purpose under one
// key (NIST SP 800-38D §8.3), a repeated nonce reveals the XOR of two
// payloads and that purpose's authentication key, after which forgeries are
// trivial (RFC 5116 §5.1.1). It refuses a payload over MaxPayload
// (ErrTooLarge) and a ttl under one second (ErrShortTTL), and answers
// ErrNoKey from a Sealer holding none.
func (s *Sealer) Seal(purpose Purpose, payload []byte, ttl time.Duration) (string, error) {
	if len(payload) > MaxPayload {
		return "", ErrTooLarge
	}
	if ttl < time.Second {
		return "", ErrShortTTL
	}
	r := s.load()
	if r == nil {
		return "", ErrNoKey
	}
	plain := make([]byte, expiryLen+len(payload))
	binary.BigEndian.PutUint64(plain, uint64(time.Now().Add(ttl).Unix())) //nolint:gosec // G115: docs/format.md's expiry is two's complement
	copy(plain[expiryLen:], payload)
	return seal(r.keys[0], useExpiring, purpose, plain)
}

// Open returns the payload of a value Seal made for purpose, before its
// expiry, and current, false when the previous key sealed it, so the caller
// seals it again. It answers ErrExpired for an authentic value past its
// expiry, ErrInvalid for any other value it cannot authenticate, one longer
// than ExpiringLen(MaxPayload) refused before decoding, and ErrNoKey from a
// Sealer holding none; current is false whenever err is non-nil. The
// payload is a new slice.
func (s *Sealer) Open(purpose Purpose, value string) (payload []byte, current bool, err error) {
	r := s.load()
	if r == nil {
		return nil, false, ErrNoKey
	}
	if len(value) > ExpiringLen(MaxPayload) {
		return nil, false, ErrInvalid
	}
	plain, current, err := r.open(useExpiring, purpose, value, expiringOverhead)
	if err != nil {
		return nil, false, err
	}
	expiry := time.Unix(int64(binary.BigEndian.Uint64(plain)), 0) //nolint:gosec // G115: docs/format.md's expiry is two's complement
	if !time.Now().Before(expiry) {
		return nil, false, ErrExpired
	}
	return plain[expiryLen:], current, nil
}

// SealAtRest returns a value that OpenAtRest, with purpose, opens to
// payload, with no expiry: a value a store keeps. Open never opens it, and
// OpenAtRest never opens a Seal value. Its nonce, its 2^32 limit and what a
// breach of it costs are Seal's. It refuses a payload over
// MaxAtRestPayload (ErrTooLarge), and answers ErrNoKey from a Sealer
// holding none.
func (s *Sealer) SealAtRest(purpose Purpose, payload []byte) (string, error) {
	if len(payload) > MaxAtRestPayload {
		return "", ErrTooLarge
	}
	r := s.load()
	if r == nil {
		return "", ErrNoKey
	}
	return seal(r.keys[0], useAtRest, purpose, payload)
}

// OpenAtRest returns the payload of a value SealAtRest made for purpose, and
// current, false when the previous key sealed it, so the caller seals it
// again. It answers ErrInvalid for any value it cannot authenticate, one
// longer than AtRestLen(MaxAtRestPayload) refused before decoding, and
// ErrNoKey from a Sealer holding none; current is false whenever err is
// non-nil. The payload is a new slice.
func (s *Sealer) OpenAtRest(purpose Purpose, value string) (payload []byte, current bool, err error) {
	r := s.load()
	if r == nil {
		return nil, false, ErrNoKey
	}
	if len(value) > AtRestLen(MaxAtRestPayload) {
		return nil, false, ErrInvalid
	}
	return r.open(useAtRest, purpose, value, atRestOverhead)
}

// KeyIDs returns the ids of the keys s holds, read together. Both are empty
// when s holds no key.
func (s *Sealer) KeyIDs() KeyIDs { return s.load().ids() }

// seal returns the value of plain under k's key for u and purpose, in the
// layout of docs/format.md: version, nonce, ciphertext and tag.
func seal(k ringKey, u use, purpose Purpose, plain []byte) (string, error) {
	aead, err := newAEAD(k, u, purpose)
	if err != nil {
		return "", err
	}
	dst := make([]byte, 1, 1+aead.Overhead()+len(plain))
	dst[0] = version
	return b64.EncodeToString(aead.Seal(dst, nil, plain, additionalData)), nil //nolint:gosec // G407: this AEAD draws the nonce; the argument must be nil
}

// open returns the plaintext of value under the first key of r that
// authenticates it for u and purpose, and whether that key is the current
// one. Every refusal is ErrInvalid.
func (r *ring) open(u use, purpose Purpose, value string, minLen int) (plain []byte, current bool, err error) {
	raw, err := b64.DecodeString(value)
	// The decoder skips \r and \n (RFC 4648 §3.3): a value holding one
	// re-encodes shorter than itself, which refuses it.
	if err != nil || b64.EncodedLen(len(raw)) != len(value) || len(raw) < minLen || raw[0] != version {
		return nil, false, ErrInvalid
	}
	for i := range r.n {
		aead, err := newAEAD(r.keys[i], u, purpose)
		if err != nil {
			return nil, false, err
		}
		// The nonce argument stays empty: this AEAD reads the nonce from the
		// front of its input and panics on a non-empty one.
		if plain, err := aead.Open(nil, nil, raw[1:], additionalData); err == nil {
			return plain, i == 0, nil
		}
	}
	return nil, false, ErrInvalid
}

// newAEAD returns AES-256-GCM with random nonces under k's key for u and
// purpose.
func newAEAD(k ringKey, u use, purpose Purpose) (cipher.AEAD, error) {
	key, err := derivedKey(k.bytes(), u, string(purpose))
	if err != nil {
		return nil, fmt.Errorf("sealed: %w", err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("sealed: %w", err)
	}
	// It refuses a block that is not crypto/aes's own, which only a
	// GOEXPERIMENT=boringcrypto build gives.
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("sealed: %w", err)
	}
	return aead, nil
}
