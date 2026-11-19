package sealed

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/fips140"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// k1 and k2 are the keys K1 and K2 of testdata/vectors.json; k3 is a key
// neither ring of the tests holds.
var (
	k1 = [keyLen]byte(fromHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	k2 = [keyLen]byte(fromHex("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"))
	k3 = [keyLen]byte(fromHex("404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f"))
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

// refValueKey is the GCM key of docs/format.md's derivation table for a
// value of kind ("expiring" or "at-rest") and purpose, from the RFC 5869
// transcription and the table's info strings written out.
func refValueKey(key [keyLen]byte, kind, purpose string) []byte {
	return refHKDF(key[:], nil, "sealed/v1/"+kind+"\x00"+purpose, keyLen)
}

// refOpen opens the decoded value raw under the AES-256 key gcmKey as
// docs/format.md lays it out (version, 12-byte nonce, ciphertext and
// 16-byte tag, additional data 0x01), with cipher.NewGCM given the nonce
// explicitly (NIST SP 800-38D §7.2). It runs inside WithoutEnforcement,
// since NewGCM is refused in FIPS 140-only mode.
func refOpen(t *testing.T, gcmKey, raw []byte) (plain []byte, err error) {
	t.Helper()
	fips140.WithoutEnforcement(func() {
		block, berr := aes.NewCipher(gcmKey)
		if berr != nil {
			t.Fatalf("aes.NewCipher: %v", berr)
		}
		aead, gerr := cipher.NewGCM(block)
		if gerr != nil {
			t.Fatalf("cipher.NewGCM: %v", gerr)
		}
		plain, err = aead.Open(nil, raw[1:13], raw[13:], []byte{0x01})
	})
	return plain, err
}

// ringSealer returns a Sealer over a ring of keys, the first current and
// the second, if any, previous: the ring a KeyFile installs after Rotate.
func ringSealer(t *testing.T, keys ...[keyLen]byte) *Sealer {
	t.Helper()
	r := &ring{n: len(keys)}
	for i, key := range keys {
		rk, err := newRingKey(key)
		if err != nil {
			t.Fatalf("newRingKey: %v", err)
		}
		r.keys[i] = rk
	}
	src := new(keySource)
	src.ring.Store(r)
	return &Sealer{src: src}
}

// rawOf decodes a value's unpadded base64url (RFC 4648 §5).
func rawOf(t *testing.T, v string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		t.Fatalf("decode %q: %v", v, err)
	}
	return raw
}

// valueOf encodes raw as a value, unpadded base64url (RFC 4648 §5).
func valueOf(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// hexBytes is a byte string written in lowercase hex in testdata/vectors.json.
type hexBytes []byte

func (h *hexBytes) UnmarshalText(text []byte) error {
	b, err := hex.DecodeString(string(text))
	*h = b
	return err
}

// testVectors is testdata/vectors.json: the vectors of docs/format.md, which
// an independent implementation computed from the format's definitions.
// Keys are named by their names in Keys.
type testVectors struct {
	Provenance string `json:"provenance"`
	Keys       []struct {
		Name string   `json:"name"`
		Key  hexBytes `json:"key"`
		ID   string   `json:"id"`
	} `json:"keys"`
	KeyFiles []struct {
		Name     string `json:"name"`
		Current  string `json:"current"`
		Previous string `json:"previous"`
		Text     string `json:"text"`
	} `json:"keyFiles"`
	Values struct {
		Key         string   `json:"key"`
		Purpose     string   `json:"purpose"`
		Nonce       hexBytes `json:"nonce"`
		Payload     string   `json:"payload"`
		Expiry      int64    `json:"expiry"`
		ExpiringOKM hexBytes `json:"expiringOKM"`
		Expiring    string   `json:"expiring"`
		AtRestOKM   hexBytes `json:"atRestOKM"`
		AtRest      string   `json:"atRest"`
	} `json:"values"`
	Subkey struct {
		Key   string   `json:"key"`
		Label string   `json:"label"`
		OKM   hexBytes `json:"okm"`
	} `json:"subkey"`
	MAC struct {
		Key     string   `json:"key"`
		Label   string   `json:"label"`
		Message string   `json:"message"`
		KeyID   string   `json:"keyId"`
		Tag     hexBytes `json:"tag"`
	} `json:"mac"`
}

// loadVectors reads testdata/vectors.json, refusing a field testVectors
// does not name.
func loadVectors(t *testing.T) *testVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatalf("read the vectors: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	v := new(testVectors)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode the vectors: %v", err)
	}
	return v
}

// key returns the key the vectors name name.
func (v *testVectors) key(t *testing.T, name string) [keyLen]byte {
	t.Helper()
	for _, k := range v.Keys {
		if k.Name == name && len(k.Key) == keyLen {
			return [keyLen]byte(k.Key)
		}
	}
	t.Fatalf("the vectors hold no 32-byte key %q", name)
	return [keyLen]byte{}
}
