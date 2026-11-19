package sealed

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
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
// for the docs/format.md vectors, and against the RFC transcription; and
// that KeyIDs answers those ids for each kind of Sealer.
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
	t.Run("KeyIDs", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			s    *Sealer
			want KeyIDs
		}{
			{"New", New(k1), KeyIDs{Current: "70603bd36c8291d779397aa614e66b28"}},
			{"two-key ring", ringSealer(t, k2, k1), KeyIDs{Current: "0681d8a548d4df142f5e9a3ab863e9ba", Previous: "70603bd36c8291d779397aa614e66b28"}},
			{"zero", &Sealer{}, KeyIDs{}},
			{"nil", nil, KeyIDs{}},
			{"New of zero", New([32]byte{}), KeyIDs{}},
		} {
			if got := tc.s.KeyIDs(); got != tc.want {
				t.Errorf("%s: KeyIDs = %+v, want %+v", tc.name, got, tc.want)
			}
		}
		a, b := NewEphemeral().KeyIDs(), NewEphemeral().KeyIDs()
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a.Current) || a.Previous != "" {
			t.Errorf("NewEphemeral: KeyIDs = %+v, want one id of 32 lowercase hex", a)
		}
		if a.Current == b.Current {
			t.Errorf("two NewEphemeral share the id %s", a.Current)
		}
	})
}

// TestSubkey checks Subkey against the RFC 5869 §2 transcription under
// docs/format.md's info "sealed/subkey" NUL label, with the docs/format.md
// vector OpenSSL computed: one key per label, derived from the current key,
// the same at every call.
func TestSubkey(t *testing.T) {
	t.Run("vector", func(t *testing.T) {
		got, err := New(k1).Subkey("example/cursor")
		if err != nil {
			t.Fatalf("Subkey: %v", err)
		}
		if want := fromHex("06a613c5bc49e3e6efd70c3b5f8bf403c9fb981197d71bfaca44446225db35f8"); !bytes.Equal(got[:], want) {
			t.Errorf("Subkey = %x, want %x", got, want)
		}
	})
	r := ringSealer(t, k2, k1)
	seen := map[[32]byte]Label{}
	for _, label := range []Label{"", "a", "a\x00", "b", "httpwire/cursor"} {
		t.Run(fmt.Sprintf("%q", label), func(t *testing.T) {
			got, err := r.Subkey(label)
			if err != nil {
				t.Fatalf("Subkey: %v", err)
			}
			if want := refHKDF(k2[:], nil, "sealed/subkey\x00"+string(label), keyLen); !bytes.Equal(got[:], want) {
				t.Errorf("Subkey = %x, want the current key's %x", got, want)
			}
			if again, err := r.Subkey(label); err != nil || again != got {
				t.Errorf("second Subkey = %x, %v; want %x", again, err, got)
			}
			if other, ok := seen[got]; ok {
				t.Errorf("same Subkey as label %q", other)
			}
			seen[got] = label
		})
	}
}

// TestMAC checks MAC as HMAC-SHA256 (RFC 2104 §2) under the key of
// docs/format.md's info "sealed/mac" NUL label (the RFC 5869
// transcription), with the docs/format.md vector OpenSSL computed, and
// VerifyMAC's answers: nil with current for its own tag, the previous
// key's tag with current false, and ErrInvalid with current false for
// every changed input (tags compared as crypto/hmac.Equal's docs say).
func TestMAC(t *testing.T) {
	s := New(k1)
	t.Run("vector", func(t *testing.T) {
		id, tag, err := s.MAC("example/manifest", []byte("example manifest"))
		if err != nil {
			t.Fatalf("MAC: %v", err)
		}
		if id != "70603bd36c8291d779397aa614e66b28" {
			t.Errorf("key id = %s, want K1's", id)
		}
		if want := fromHex("90c989e51170bc63130b9888d84f37a7e962ede9ba867393c5b177351f10fa09"); !bytes.Equal(tag, want) {
			t.Errorf("tag = %x, want %x", tag, want)
		}
	})
	t.Run("transcription", func(t *testing.T) {
		for _, label := range []Label{"", "x", "a\x00b", "snapshot/manifest"} {
			_, tag, err := s.MAC(label, []byte("a manifest"))
			if err != nil {
				t.Fatalf("MAC: %v", err)
			}
			h := hmac.New(sha256.New, refHKDF(k1[:], nil, "sealed/mac\x00"+string(label), keyLen))
			h.Write([]byte("a manifest"))
			if want := h.Sum(nil); !bytes.Equal(tag, want) {
				t.Errorf("label %q: tag = %x, want %x", label, tag, want)
			}
		}
	})
	const label Label = "example/manifest"
	msg := []byte("a manifest")
	id, tag, err := s.MAC(label, msg)
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	t.Run("verifies", func(t *testing.T) {
		if current, err := s.VerifyMAC(label, msg, id, tag); err != nil || !current {
			t.Errorf("VerifyMAC = %v, %v; want true, nil", current, err)
		}
	})
	t.Run("previous key", func(t *testing.T) {
		r := ringSealer(t, k2, k1)
		if current, err := r.VerifyMAC(label, msg, id, tag); err != nil || current {
			t.Errorf("previous key's tag: %v, %v; want false, nil", current, err)
		}
		gotID, gotTag, err := r.MAC(label, msg)
		if err != nil {
			t.Fatalf("MAC: %v", err)
		}
		wantID, wantTag, err := New(k2).MAC(label, msg)
		if err != nil {
			t.Fatalf("MAC: %v", err)
		}
		if gotID != wantID || !bytes.Equal(gotTag, wantTag) {
			t.Errorf("ring MAC = %s %x, want the current key's %s %x", gotID, gotTag, wantID, wantTag)
		}
	})
	flipped := bytes.Clone(tag)
	flipped[31] ^= 0x80
	otherID, otherTag, err := New(k2).MAC(label, msg)
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	for _, tc := range []struct {
		name  string
		label Label
		msg   []byte
		id    string
		tag   []byte
	}{
		{"changed message", label, []byte("a manifesT"), id, tag},
		{"other label", "example/other", msg, id, tag},
		{"changed tag", label, msg, id, flipped},
		{"31-byte tag", label, msg, id, tag[:31]},
		{"33-byte tag", label, msg, id, append(bytes.Clone(tag), 0)},
		{"empty tag", label, msg, id, nil},
		{"unknown id", label, msg, otherID, tag},
		{"unknown key's own tag", label, msg, otherID, otherTag},
		{"empty id", label, msg, "", tag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if current, err := s.VerifyMAC(tc.label, tc.msg, tc.id, tc.tag); !errors.Is(err, ErrInvalid) || current {
				t.Errorf("VerifyMAC = %v, %v; want false, ErrInvalid", current, err)
			}
		})
	}
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
