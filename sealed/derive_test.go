package sealed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// TestHKDFEmptySalt checks derive against RFC 5869 Appendix A.3 (SHA-256,
// zero-length salt and info): a nil salt is the RFC's absent salt, which
// §2.2 reads as HashLen zero bytes, and §2.3 makes a shorter OKM the first
// L octets of T.
func TestHKDFEmptySalt(t *testing.T) {
	ikm := bytes.Repeat([]byte{0x0b}, 22)
	okm := fromHex("8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8")
	for _, l := range []int{42, keyLen, idLen} {
		t.Run(fmt.Sprint("L=", l), func(t *testing.T) {
			got, err := derive(ikm, "", l)
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if !bytes.Equal(got, okm[:l]) {
				t.Errorf("derive = %x, want RFC 5869 A.3 OKM[:%d] %x", got, l, okm[:l])
			}
		})
	}
}

// TestKeyID checks that a key's id is the first 16 bytes of HKDF-SHA256 of
// the key under the empty salt and info "sealed/key-id" (RFC 5869 §2.3),
// in lowercase hex: against the ids an independent implementation computed
// for the docs/format.md vectors, and against the RFC transcription.
func TestKeyID(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  [keyLen]byte
		want string
	}{
		{"K1", k1, "70603bd36c8291d779397aa614e66b28"},
		{"K2", k2, "0681d8a548d4df142f5e9a3ab863e9ba"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keyID(tc.key[:])
			if err != nil {
				t.Fatalf("keyID: %v", err)
			}
			if got != tc.want {
				t.Errorf("keyID = %s, want %s", got, tc.want)
			}
			rk, err := newRingKey(tc.key)
			if err != nil {
				t.Fatalf("newRingKey: %v", err)
			}
			if rk.id != tc.want {
				t.Errorf("newRingKey id = %s, want %s", rk.id, tc.want)
			}
		})
	}
	t.Run("transcription", func(t *testing.T) {
		for i := range 16 {
			key := sha256.Sum256([]byte{byte(i)})
			want := hex.EncodeToString(refHKDF(key[:], nil, "sealed/key-id", 16))
			got, err := keyID(key[:])
			if err != nil {
				t.Fatalf("keyID: %v", err)
			}
			if got != want {
				t.Errorf("keyID(%x) = %s, want %s", key, got, want)
			}
		}
	})
}

// TestDerivationSeparation checks the info strings of docs/format.md's
// derivation table, and that no two uses or callers' bytes share a key:
// RFC 5869 §3.2 separates derived keys only by distinct info, and RFC 5116
// §3.3 asks the same injective encoding of what a key or value binds.
func TestDerivationSeparation(t *testing.T) {
	cases := []struct {
		name string
		u    use
		s    string
		info string
	}{
		{"key id", useKeyID, "", "sealed/key-id"},
		{"expiring p", useExpiring, "p", "sealed/v1/expiring\x00p"},
		{"at-rest p", useAtRest, "p", "sealed/v1/at-rest\x00p"},
		{"expiring a", useExpiring, "a", "sealed/v1/expiring\x00a"},
		{"expiring a NUL", useExpiring, "a\x00", "sealed/v1/expiring\x00a\x00"},
		{"at-rest a", useAtRest, "a", "sealed/v1/at-rest\x00a"},
		{"at-rest a NUL", useAtRest, "a\x00", "sealed/v1/at-rest\x00a\x00"},
		{"expiring empty", useExpiring, "", "sealed/v1/expiring\x00"},
		{"at-rest empty", useAtRest, "", "sealed/v1/at-rest\x00"},
		{"expiring x", useExpiring, "x", "sealed/v1/expiring\x00x"},
		{"at-rest x", useAtRest, "x", "sealed/v1/at-rest\x00x"},
		{"subkey x", useSubkey, "x", "sealed/subkey\x00x"},
		{"MAC x", useMAC, "x", "sealed/mac\x00x"},
		{"subkey empty", useSubkey, "", "sealed/subkey\x00"},
		{"MAC empty", useMAC, "", "sealed/mac\x00"},
		// Labels that spell another use's info, whole or after its prefix.
		{"subkey spelling expiring", useSubkey, "v1/expiring\x00p", "sealed/subkey\x00v1/expiring\x00p"},
		{"subkey spelling whole expiring", useSubkey, "sealed/v1/expiring\x00p", "sealed/subkey\x00sealed/v1/expiring\x00p"},
		{"MAC spelling at-rest", useMAC, "v1/at-rest\x00p", "sealed/mac\x00v1/at-rest\x00p"},
		{"subkey spelling key id", useSubkey, "key-id", "sealed/subkey\x00key-id"},
		{"MAC spelling whole key id", useMAC, "sealed/key-id", "sealed/mac\x00sealed/key-id"},
		{"subkey spelling MAC", useSubkey, "mac\x00x", "sealed/subkey\x00mac\x00x"},
		{"MAC spelling subkey", useMAC, "subkey\x00x", "sealed/mac\x00subkey\x00x"},
	}
	type derived struct {
		name string
		key  [keyLen]byte
	}
	var seen []derived
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := info(tc.u, tc.s); got != tc.info {
				t.Errorf("info = %q, want %q", got, tc.info)
			}
			key, err := derivedKey(k1[:], tc.u, tc.s)
			if err != nil {
				t.Fatalf("derivedKey: %v", err)
			}
			if want := refHKDF(k1[:], nil, tc.info, keyLen); !bytes.Equal(key[:], want) {
				t.Errorf("derivedKey = %x, want HKDF under info %q, %x", key, tc.info, want)
			}
			for _, o := range seen {
				if o.key == key {
					t.Errorf("same key as %q", o.name)
				}
			}
			seen = append(seen, derived{tc.name, key})
		})
	}
}
