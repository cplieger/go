package sealed

import (
	"bytes"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// TestVectors checks the module against testdata/vectors.json, which
// Python's cryptography over OpenSSL computed from docs/format.md's
// definitions: each key's id, HKDF-SHA256 (RFC 5869 §2) under the info
// "sealed/key-id"; the expiring and at-rest keys of the vector purpose, and
// its values, AES-256-GCM (RFC 5116 §5.2: K_LEN 32, the 16-octet tag
// appended) under the additional data 0x01, which open to the payload, the
// expiring one until its expiry second; the Subkey; and the MAC tag,
// HMAC-SHA256 (RFC 2104 §2), with its key id.
func TestVectors(t *testing.T) {
	v := loadVectors(t)
	t.Run("key ids", func(t *testing.T) {
		for _, k := range v.Keys {
			got, err := keyID(k.Key)
			if err != nil || got != k.ID {
				t.Errorf("%s: keyID = %s, %v; want %s", k.Name, got, err, k.ID)
			}
			if ids := ringSealer(t, v.key(t, k.Name)).KeyIDs(); ids.Current != k.ID {
				t.Errorf("%s: KeyIDs().Current = %s, want %s", k.Name, ids.Current, k.ID)
			}
		}
	})

	vals := v.Values
	key := v.key(t, vals.Key)
	purpose := Purpose(vals.Purpose)
	s := ringSealer(t, key)
	t.Run("value keys", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			u    use
			want []byte
		}{
			{"expiring", useExpiring, vals.ExpiringOKM},
			{"at-rest", useAtRest, vals.AtRestOKM},
		} {
			got, err := derivedKey(key[:], tc.u, vals.Purpose)
			if err != nil || !bytes.Equal(got[:], tc.want) {
				t.Errorf("%s key = %x, %v; want %x", tc.name, got, err, tc.want)
			}
		}
	})
	t.Run("expiring", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			time.Sleep(time.Until(time.Unix(vals.Expiry, 0)) - time.Nanosecond)
			got, current, err := s.Open(purpose, vals.Expiring)
			if err != nil || !current || string(got) != vals.Payload {
				t.Errorf("a nanosecond before the expiry: %q, %v, %v; want %q, true, nil", got, current, err, vals.Payload)
			}
			time.Sleep(time.Nanosecond)
			if got, current, err := s.Open(purpose, vals.Expiring); !errors.Is(err, ErrExpired) {
				t.Errorf("at the expiry: %q, %v, %v; want ErrExpired", got, current, err)
			}
		})
	})
	t.Run("at-rest", func(t *testing.T) {
		got, current, err := s.OpenAtRest(purpose, vals.AtRest)
		if err != nil || !current || string(got) != vals.Payload {
			t.Errorf("OpenAtRest = %q, %v, %v; want %q, true, nil", got, current, err, vals.Payload)
		}
	})
	t.Run("subkey", func(t *testing.T) {
		got, err := ringSealer(t, v.key(t, v.Subkey.Key)).Subkey(Label(v.Subkey.Label))
		if err != nil || !bytes.Equal(got[:], v.Subkey.OKM) {
			t.Errorf("Subkey = %x, %v; want %x", got, err, v.Subkey.OKM)
		}
	})
	t.Run("mac", func(t *testing.T) {
		m := v.MAC
		ms := ringSealer(t, v.key(t, m.Key))
		id, tag, err := ms.MAC(Label(m.Label), []byte(m.Message))
		if err != nil || id != m.KeyID || !bytes.Equal(tag, m.Tag) {
			t.Errorf("MAC = %s %x, %v; want %s %x", id, tag, err, m.KeyID, m.Tag)
		}
		if current, err := ms.VerifyMAC(Label(m.Label), []byte(m.Message), m.KeyID, m.Tag); err != nil || !current {
			t.Errorf("VerifyMAC of the vector = %v, %v; want true, nil", current, err)
		}
	})
}
