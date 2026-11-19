package sealed

import (
	"sync/atomic"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/masked"
)

// ring is the keys a Sealer holds: keys[0] is the current key and, when n
// is 2, keys[1] the previous one. A stored ring is never changed: a write
// builds a new one and stores it, so each call reads one consistent ring.
type ring struct {
	keys [2]ringKey
	n    int
}

// ringKey is one key of a ring, masked so no printer shows it, and its id.
type ringKey struct {
	key masked.Secret
	id  string
}

// keySource is the ring a KeyFile and every Sealer it hands out share, so a
// Sealer sees each install at its next call. lease is the KeyFile's, read
// and set under its mu. Held here, it stays reachable while any Sealer is,
// so the lock file's finalizer cannot close it and drop the lease early.
type keySource struct {
	lease *filesafe.Lease
	ring  atomic.Pointer[ring]
}

func newRingKey(key [keyLen]byte) (ringKey, error) {
	id, err := keyID(key[:])
	if err != nil {
		return ringKey{}, err
	}
	return ringKey{key: masked.NewSecret(string(key[:])), id: id}, nil
}

// bytes returns the key in a new slice, which the caller keeps no longer
// than its call.
func (k ringKey) bytes() []byte { return []byte(k.key.Reveal()) }

// load returns s's ring, or nil when s holds no key.
func (s *keySource) load() *ring {
	if s == nil {
		return nil
	}
	return s.ring.Load()
}

// byID returns the key whose id is id, and whether it is the current key.
func (r *ring) byID(id string) (k ringKey, current, ok bool) {
	for i := range r.n {
		if r.keys[i].id == id {
			return r.keys[i], i == 0, true
		}
	}
	return ringKey{}, false, false
}
