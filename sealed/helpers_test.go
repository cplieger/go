package sealed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// k1 and k2 are the keys of the vectors in docs/format.md.
var (
	k1 = [keyLen]byte(fromHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	k2 = [keyLen]byte(fromHex("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"))
)

func fromHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// refHKDF is HKDF-SHA256 transcribed from RFC 5869 with crypto/hmac, the
// oracle the tests hold every derivation to: §2.2, PRK = HMAC-Hash(salt,
// IKM), an absent salt being HashLen zeros; §2.3, T(i) = HMAC-Hash(PRK,
// T(i-1) | info | i), and OKM the first l octets of T.
func refHKDF(ikm, salt []byte, info string, l int) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	extract := hmac.New(sha256.New, salt)
	extract.Write(ikm)
	prk := extract.Sum(nil)
	var okm, t []byte
	for i := byte(1); len(okm) < l; i++ {
		expand := hmac.New(sha256.New, prk)
		expand.Write(t)
		expand.Write([]byte(info))
		expand.Write([]byte{i})
		t = expand.Sum(nil)
		okm = append(okm, t...)
	}
	return okm[:l]
}
