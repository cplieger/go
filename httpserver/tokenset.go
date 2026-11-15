package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
)

// MaxTokenBytes bounds a token TokenSet holds or matches. A presented value
// longer than this never matches and is refused before it is hashed.
const MaxTokenBytes = 512

// compareDigest is the comparison Match runs per configured digest; tests
// count its calls to pin that Match never exits early.
var compareDigest = subtle.ConstantTimeCompare

// TokenSet matches a presented credential against several configured secrets
// in constant time and reports which one matched. It is the multi-secret
// form of StaticTokenVerifier, for an endpoint whose callers each hold their
// own secret. Build it with NewTokenSet. The zero value matches nothing. It
// is safe for concurrent use.
type TokenSet struct {
	digests [][sha256.Size]byte
}

// The reasons a TokenSetError carries, matched with errors.Is.
var (
	ErrNoTokens      = errors.New("no tokens configured")
	ErrTokenEmpty    = errors.New("token is empty")
	ErrTokenTooLong  = errors.New("token is longer than MaxTokenBytes")
	ErrTokenRepeated = errors.New("token repeats an earlier token")
)

// TokenSetError is NewTokenSet's refusal. It names positions only, never a
// byte of any secret, so a caller can map Index to its own configuration
// line without the message quoting a token.
type TokenSetError struct {
	// Err is one of ErrNoTokens, ErrTokenEmpty, ErrTokenTooLong and
	// ErrTokenRepeated.
	Err error
	// Index is the refused token's position; -1 for ErrNoTokens.
	Index int
	// First is the earlier position a repeated token equals; -1 otherwise.
	First int
}

func (e *TokenSetError) Error() string {
	switch {
	case e.Index < 0:
		return "httpserver: " + e.Err.Error()
	case e.First >= 0:
		return fmt.Sprintf("httpserver: token %d: %v (token %d)", e.Index, e.Err, e.First)
	}
	return fmt.Sprintf("httpserver: token %d: %v", e.Index, e.Err)
}

func (e *TokenSetError) Unwrap() error { return e.Err }

// NewTokenSet keeps the SHA-256 digest of each configured secret, in order,
// so Match's index is the secret's position in configured. It refuses no
// secret at all, an empty secret, one longer than MaxTokenBytes, and one
// given twice, which would make the index ambiguous, with a *TokenSetError.
func NewTokenSet(configured []string) (TokenSet, error) {
	if len(configured) == 0 {
		return TokenSet{}, &TokenSetError{Err: ErrNoTokens, Index: -1, First: -1}
	}
	digests := make([][sha256.Size]byte, 0, len(configured))
	seen := make(map[[sha256.Size]byte]int, len(configured))
	for i, secret := range configured {
		switch {
		case secret == "":
			return TokenSet{}, &TokenSetError{Err: ErrTokenEmpty, Index: i, First: -1}
		case len(secret) > MaxTokenBytes:
			return TokenSet{}, &TokenSetError{Err: ErrTokenTooLong, Index: i, First: -1}
		}
		d := sha256.Sum256([]byte(secret))
		if first, dup := seen[d]; dup {
			return TokenSet{}, &TokenSetError{Err: ErrTokenRepeated, Index: i, First: first}
		}
		seen[d] = i
		digests = append(digests, d)
	}
	return TokenSet{digests: digests}, nil
}

// Len reports how many secrets the set holds.
func (s TokenSet) Len() int { return len(s.digests) }

// Match reports the position of the configured secret presented equals. A
// value longer than MaxTokenBytes answers (-1, false) without being hashed;
// any other is hashed once and compared with every configured digest, with no
// early exit, so the time taken depends only on the number of secrets. No
// match, and the zero TokenSet, answer (-1, false).
func (s TokenSet) Match(presented string) (index int, ok bool) {
	if len(presented) > MaxTokenBytes || len(s.digests) == 0 {
		return -1, false
	}
	p := sha256.Sum256([]byte(presented))
	found, at := 0, 0
	for i := range s.digests {
		eq := compareDigest(s.digests[i][:], p[:])
		at = subtle.ConstantTimeSelect(eq, i, at)
		found |= eq
	}
	if found == 0 {
		return -1, false
	}
	return at, true
}
