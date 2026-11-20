package masked

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"sync"
)

// tagSize is the length of the HMAC-SHA256 tag that leads every masked
// value; its first aes.BlockSize bytes are the CTR IV.
const tagSize = sha256.Size

// keys holds the process's two masking keys: one tags each text, the other
// encrypts it. They live only here, so no masked value reaches them.
type keys struct {
	block  cipher.Block
	tagKey []byte
}

var processKeys = sync.OnceValue(func() *keys {
	tagKey := make([]byte, 32)
	encKey := make([]byte, 32)
	rand.Read(tagKey) // never returns an error (Go 1.24+); it crashes instead
	rand.Read(encKey)
	// aes.NewCipher errors only on a key length other than 16, 24 or 32.
	block, _ := aes.NewCipher(encKey)
	return &keys{block: block, tagKey: tagKey}
})

// value is tag || AES-256-CTR(text), where tag is the HMAC-SHA256 of text
// and its first 16 bytes are the IV. Equal texts mask to equal bytes within
// the process, which is what makes reflect.DeepEqual and equal agree with
// text equality; the IV depends on the text, so knowing one value's text
// reveals no keystream of another's.
type value struct {
	b []byte
}

// mask returns the masked form of text, or nil for "".
func mask(text string) *value {
	if text == "" {
		return nil
	}
	k := processKeys()
	mac := hmac.New(sha256.New, k.tagKey)
	mac.Write([]byte(text))
	b := make([]byte, tagSize+len(text))
	copy(b, mac.Sum(nil))
	cipher.NewCTR(k.block, b[:aes.BlockSize]).XORKeyStream(b[tagSize:], []byte(text))
	return &value{b: b}
}

// open returns the text v masks, or "" for nil.
func (v *value) open() string {
	if v == nil {
		return ""
	}
	out := make([]byte, len(v.b)-tagSize)
	cipher.NewCTR(processKeys().block, v.b[:aes.BlockSize]).XORKeyStream(out, v.b[tagSize:])
	return string(out)
}

// tag returns v's tag, or 32 zero bytes for nil.
func (v *value) tag() [tagSize]byte {
	var t [tagSize]byte
	if v != nil {
		copy(t[:], v.b[:tagSize])
	}
	return t
}

// equal reports whether a and b mask the same text. It compares the two
// fixed-size tags only, so every pair takes the same work whatever the
// texts' lengths; subtle.ConstantTimeCompare returns at once on unequal
// lengths. Two nils are equal; a nil equals a non-nil value only if an HMAC
// output is 32 zero bytes.
func equal(a, b *value) bool {
	at, bt := a.tag(), b.tag()
	return subtle.ConstantTimeCompare(at[:], bt[:]) == 1
}
