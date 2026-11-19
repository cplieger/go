package sealed

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
)

// Label names a key derived for one use outside this package's values, by
// Subkey or MAC: any bytes, compared exactly. Declare each label once, in
// the package that uses it, under that package's path.
type Label string

// use is one row of the derivation table in docs/format.md.
type use uint8

const (
	useKeyID use = iota
	useExpiring
	useAtRest
	useSubkey
	useMAC
)

// infoWords are the fixed words of each use's HKDF info. No word holds a
// NUL and no two are equal, so info is injective, which the separation of
// RFC 5869 §3.2 needs. The v1 words change only with the value format; the
// other three never, because an app records what they derive.
var infoWords = [...]string{
	useKeyID:    "sealed/key-id",
	useExpiring: "sealed/v1/expiring",
	useAtRest:   "sealed/v1/at-rest",
	useSubkey:   "sealed/subkey",
	useMAC:      "sealed/mac",
}

const (
	// keyLen is the length of a ring key and of every key derived from one:
	// AES-256's K_LEN (RFC 5116 §5.2), a Subkey and an HMAC-SHA256 key.
	keyLen = 32
	// idLen is the length of a key id before hex.
	idLen = 16
)

// info returns the HKDF info of u for the caller's purpose or label s: the
// key id's word alone, and every other use's word, a NUL, then s, so the
// text before the first NUL names the use and the rest is exactly s.
func info(u use, s string) string {
	if u == useKeyID {
		return infoWords[useKeyID]
	}
	return infoWords[u] + "\x00" + s
}

// derive returns n bytes of HKDF-SHA256 (RFC 5869 §2) of key under info,
// with the empty salt, which §2.2 reads as HashLen zero bytes.
func derive(key []byte, info string, n int) ([]byte, error) {
	return hkdf.Key(sha256.New, key, nil, info, n)
}

// derivedKey returns the key of use u for the purpose or label s.
func derivedKey(key []byte, u use, s string) ([keyLen]byte, error) {
	b, err := derive(key, info(u, s), keyLen)
	if err != nil {
		return [keyLen]byte{}, err
	}
	return [keyLen]byte(b), nil
}

// keyID returns key's id, its idLen key-id bytes in lowercase hex.
func keyID(key []byte) (string, error) {
	b, err := derive(key, info(useKeyID, ""), idLen)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
