package sealed

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

// versionLine is the first line of every key file this release reads and
// writes.
const versionLine = "sealed-key/v1"

// keyWords name a key file's key lines, in their order: the current key,
// then the previous one.
var keyWords = [2]string{"current", "previous"}

var lf = []byte{'\n'}

// encodeKeyFile returns the key file of r, in docs/format.md's grammar. It
// holds r's keys in hex, so the caller keeps it no longer than its write.
func encodeKeyFile(r *ring) []byte {
	b := append([]byte(versionLine), '\n')
	for i, k := range r.keys[:r.n] {
		b = append(b, keyWords[i]...)
		b = append(b, ' ')
		b = append(b, k.id...)
		b = append(b, ' ')
		b = hex.AppendEncode(b, k.bytes())
		b = append(b, '\n')
	}
	return b
}

// parseKeyFile returns the ring of the key file data read from path, or
// ErrKeyFileFormat naming path and the line it refused, never that line's
// text, which can hold a key.
func parseKeyFile(path string, data []byte) (*ring, error) {
	refuse := func(line int, reason string) error {
		return fmt.Errorf("%w: %s: line %d: %s", ErrKeyFileFormat, path, line, reason)
	}
	lines := bytes.Split(data, lf)
	if !bytes.Equal(lines[0], []byte(versionLine)) {
		return nil, refuse(1, versionReason(lines[0]))
	}
	if last := lines[len(lines)-1]; len(last) != 0 {
		return nil, refuse(len(lines), "no line feed at its end")
	}
	keyLines := lines[1 : len(lines)-1]
	switch {
	case len(keyLines) == 0:
		return nil, refuse(2, "no current key")
	case len(keyLines) > len(keyWords):
		return nil, refuse(2+len(keyWords), "more than two keys")
	}
	r := &ring{n: len(keyLines)}
	for i, line := range keyLines {
		key, id, reason := splitKeyLine(line, keyWords[i])
		if reason != "" {
			return nil, refuse(i+2, reason)
		}
		rk, err := newRingKey(key)
		if err != nil {
			return nil, fmt.Errorf("sealed: %w", err)
		}
		if rk.id != hex.EncodeToString(id[:]) {
			return nil, refuse(i+2, "id does not match its key")
		}
		if i == 1 && rk.id == r.keys[0].id {
			return nil, refuse(3, "the same key as line 2")
		}
		r.keys[i] = rk
	}
	// One key state has one spelling. Of what the checks above pass, only
	// uppercase hex differs from its encoding; comparing the whole file also
	// refuses whatever a later change to those checks would let through.
	if enc := encodeKeyFile(r); !bytes.Equal(enc, data) {
		return nil, refuse(1+bytes.Count(data[:commonPrefixLen(data, enc)], lf), "hex digits not lowercase")
	}
	return r, nil
}

// commonPrefixLen returns the length of the longest prefix a and b share.
func commonPrefixLen(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// versionReason says why line is not the version line, naming the version
// of a newer key file: a version of at most nine digits cannot hold a key.
func versionReason(line []byte) string {
	v, ok := bytes.CutPrefix(line, []byte("sealed-key/v"))
	if !ok || len(v) == 0 || len(v) > 9 || bytes.ContainsFunc(v, func(c rune) bool { return c < '0' || c > '9' }) {
		return "not a key file version line"
	}
	return fmt.Sprintf("sealed-key/v%s, this release reads %s", v, versionLine)
}

// splitKeyLine returns the key and id of a key line that starts with word,
// or the reason it is not one. It accepts hex digits of either case.
func splitKeyLine(line []byte, word string) (key [keyLen]byte, id [idLen]byte, reason string) {
	rest, ok := bytes.CutPrefix(line, []byte(word+" "))
	if !ok {
		return key, id, fmt.Sprintf("not a %s key line", word)
	}
	idHex, keyHex, _ := bytes.Cut(rest, []byte{' '})
	if len(idHex) != 2*idLen {
		return key, id, "id not 32 hex digits"
	}
	if _, err := hex.Decode(id[:], idHex); err != nil {
		return key, id, "id not 32 hex digits"
	}
	if len(keyHex) != 2*keyLen {
		return key, id, "key not 64 hex digits"
	}
	if _, err := hex.Decode(key[:], keyHex); err != nil {
		return key, id, "key not 64 hex digits"
	}
	return key, id, ""
}
