package masked

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

const redacted = "[redacted]"

// Secret holds a plaintext credential. Every printing route renders
// "[redacted]", masked bytes or an address, or "" for the zero value. The
// plaintext is never held in clear, so a printer walking any holder's
// fields by reflection (%p of a struct value, go-cmp, go-spew) finds only
// masked bytes. == does not compile on a Secret: compare with [Secret.Equal]
// and key an in-memory map on [Secret.Key].
type Secret struct {
	_ [0]func()
	p *value
}

// NewSecret wraps plaintext. NewSecret("") is the zero Secret.
func NewSecret(plaintext string) Secret { return Secret{p: mask(plaintext)} }

// Reveal returns the plaintext, or "" for the zero value.
func (s Secret) Reveal() string { return s.p.open() }

// IsZero reports whether s holds no plaintext.
func (s Secret) IsZero() bool { return s.p == nil }

// Equal reports, in constant time for any two values, whether s and o hold
// the same text: it compares two fixed-size 32-byte tags, never the texts.
// Two zero values are equal, and a zero value equals no other.
func (s Secret) Equal(o Secret) bool { return equal(s.p, o.p) }

// Key returns s's tag, an HMAC-SHA256 of the text under a key drawn at
// process start, or 32 zero bytes for the zero Secret. Equal texts give
// equal keys within one process, so it is a valid map key, and == on it is
// a fixed-size compare. The key changes every start, so it is an in-process
// map key only: never store or send it. A durable identifier is derived
// where the value is hashed or sealed, never by storing Reveal().
func (s Secret) Key() [32]byte { return s.p.tag() }

func (s Secret) render() string {
	if s.IsZero() {
		return ""
	}
	return redacted
}

// Format writes "[redacted]", or nothing for the zero value, for every verb
// fmt routes to it.
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, s.render())
}

// LogValue returns the redacted rendering as a string value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.render()) }

// MarshalJSON encodes the redacted rendering as a JSON string, which
// nothing decodes.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.render()) }
