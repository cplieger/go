package sealed

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// FuzzParseKeyFile_canonical checks docs/format.md's rule that one key state
// has one spelling: the parser never panics; each refusal is
// ErrKeyFileFormat and holds no hex run a key line would leave; and each
// file it accepts holds one or two distinct keys, each id the first 16
// bytes of HKDF-SHA256 (RFC 5869 §2) of its key under "sealed/key-id", and
// encodes back to exactly its bytes.
func FuzzParseKeyFile_canonical(f *testing.F) {
	v := loadVectors(f)
	for _, kf := range v.KeyFiles {
		f.Add([]byte(kf.Text))
	}
	for _, c := range keyFileRefusals(f, v) {
		f.Add([]byte(c.text))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := parseKeyFile(testKeyPath, data)
		if err != nil {
			if r != nil || !errors.Is(err, ErrKeyFileFormat) || hexRun.MatchString(err.Error()) {
				t.Fatalf("refused with a ring %v and %q; want no ring, ErrKeyFileFormat and no hex run", r != nil, err)
			}
			return
		}
		if r.n < 1 || r.n > 2 {
			t.Fatalf("accepted %d keys", r.n)
		}
		for i := range r.n {
			if want := hex.EncodeToString(refHKDF(r.keys[i].bytes(), nil, "sealed/key-id", idLen)); r.keys[i].id != want {
				t.Errorf("%s id %s, want %s", keyWords[i], r.keys[i].id, want)
			}
		}
		if r.n == 2 && r.keys[0].id == r.keys[1].id {
			t.Errorf("accepted two equal keys")
		}
		if enc := encodeKeyFile(r); !bytes.Equal(enc, data) {
			t.Errorf("accepted %q, which encodes to %q", data, enc)
		}
	})
}
