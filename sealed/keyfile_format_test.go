package sealed

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// testKeyPath is the path the tests' parser refusals name.
const testKeyPath = "/keys/sealing.key"

// hexRun matches what a refusal must never hold: a hex run as long as a
// key id, which a key line's id or key would leave in the message.
var hexRun = regexp.MustCompile(`[0-9A-Fa-f]{32,}`)

// keyFileRefusal is a file docs/format.md's key file grammar or rules
// refuse, the line its refusal names, and a phrase of the reason.
type keyFileRefusal struct {
	name string
	text string
	line int
	says string
}

// keyFileRefusals returns the refusals of docs/format.md's key file
// grammar (ABNF, RFC 5234, with RFC 7405's case-sensitive strings) and
// rules, built from the vectors' keys and ids: K2 current and K1 previous.
func keyFileRefusals(tb testing.TB, v *testVectors) []keyFileRefusal {
	tb.Helper()
	ids := map[string]string{}
	for _, k := range v.Keys {
		ids[k.Name] = k.ID
	}
	key1, key2 := v.key(tb, "K1"), v.key(tb, "K2")
	hex1, hex2 := hex.EncodeToString(key1[:]), hex.EncodeToString(key2[:])
	id1, id2 := ids["K1"], ids["K2"]
	line := func(word, id, key string) string { return word + " " + id + " " + key + "\n" }
	ver := "sealed-key/v1\n"
	cur := line("current", id2, hex2)
	prev := line("previous", id1, hex1)
	last := byte('0')
	if hex2[63] == '0' {
		last = '1'
	}
	changed := hex2[:63] + string(last)
	return []keyFileRefusal{
		{"another first line", "age-encryption.org/v1\n" + cur, 1, "version line"},
		{"no version line", cur + prev, 1, "version line"},
		{"empty", "", 1, "version line"},
		{"byte order mark", "\ufeff" + ver + cur, 1, "version line"},
		{"newer version", "sealed-key/v2\n" + cur, 1, "sealed-key/v2"},
		{"CRLF", strings.ReplaceAll(ver+cur, "\n", "\r\n"), 1, "version line"},
		{"CR on a key line", ver + strings.TrimSuffix(cur, "\n") + "\r\n", 2, "key not 64 hex digits"},
		{"version line only", ver, 2, "no current key"},
		{"no final line feed", ver + strings.TrimSuffix(cur, "\n"), 2, "line feed"},
		{"text after the last line feed", ver + cur + prev + "x", 4, "line feed"},
		{"blank line", ver + "\n" + cur, 2, "not a current key line"},
		{"blank line at the end", ver + cur + "\n", 3, "not a previous key line"},
		{"leading space", ver + " " + cur, 2, "not a current key line"},
		{"tab", ver + "current\t" + id2 + " " + hex2 + "\n", 2, "not a current key line"},
		{"two spaces", ver + "current  " + id2 + " " + hex2 + "\n", 2, "id not 32 hex digits"},
		{"no id or key", ver + "current \n", 2, "id not 32 hex digits"},
		{"trailing space", ver + strings.TrimSuffix(cur, "\n") + " \n", 2, "key not 64 hex digits"},
		{"31-character id", ver + line("current", id2[1:], hex2), 2, "id not 32 hex digits"},
		{"non-hex id", ver + line("current", "g"+id2[1:], hex2), 2, "id not 32 hex digits"},
		{"63-character key", ver + line("current", id2, hex2[1:]), 2, "key not 64 hex digits"},
		{"65-character key", ver + line("current", id2, hex2+"0"), 2, "key not 64 hex digits"},
		{"non-hex key", ver + line("current", id2, "g"+hex2[1:]), 2, "key not 64 hex digits"},
		{"uppercase key", ver + line("current", id2, strings.ToUpper(hex2)), 2, "not lowercase"},
		{"uppercase id", ver + line("current", strings.ToUpper(id2), hex2), 2, "not lowercase"},
		{"uppercase previous key", ver + cur + line("previous", id1, strings.ToUpper(hex1)), 3, "not lowercase"},
		{"id of another key", ver + line("current", id1, hex2), 2, "id does not match its key"},
		{"key with a changed digit", ver + line("current", id2, changed), 2, "id does not match its key"},
		{"previous before current", ver + prev + cur, 2, "not a current key line"},
		{"previous only", ver + prev, 2, "not a current key line"},
		{"two current lines", ver + cur + line("current", id1, hex1), 3, "not a previous key line"},
		{"equal ids", ver + cur + line("previous", id2, hex2), 3, "same key as line 2"},
		{"a third key", ver + cur + prev + line("previous", id1, hex1), 4, "more than two keys"},
	}
}

// TestKeyFileGrammar checks the key file parser and encoder against
// docs/format.md: the vectors' key files, which Python's cryptography
// computed, parse to their keys and ids and are what the encoder writes;
// every file the grammar (ABNF, RFC 5234, with RFC 7405's case-sensitive
// strings) or its rules refuse is ErrKeyFileFormat, naming the path and
// the line, and holding no hex run a key line's id or key would leave.
func TestKeyFileGrammar(t *testing.T) {
	v := loadVectors(t)
	ids := map[string]string{}
	for _, k := range v.Keys {
		ids[k.Name] = k.ID
	}
	for _, kf := range v.KeyFiles {
		names := []string{kf.Current}
		if kf.Previous != "" {
			names = append(names, kf.Previous)
		}
		keys := make([][keyLen]byte, len(names))
		for i, name := range names {
			keys[i] = v.key(t, name)
		}
		t.Run(kf.Name+" parse", func(t *testing.T) {
			r, err := parseKeyFile(testKeyPath, []byte(kf.Text))
			if err != nil {
				t.Fatalf("parseKeyFile: %v", err)
			}
			if r.n != len(keys) {
				t.Fatalf("%d keys, want %d", r.n, len(keys))
			}
			for i, key := range keys {
				if got := r.keys[i].bytes(); !bytes.Equal(got, key[:]) {
					t.Errorf("%s key = %x, want %s's", keyWords[i], got, names[i])
				}
				if got := r.keys[i].id; got != ids[names[i]] {
					t.Errorf("%s id = %s, want %s", keyWords[i], got, ids[names[i]])
				}
			}
		})
		t.Run(kf.Name+" encode", func(t *testing.T) {
			if got := encodeKeyFile(testRing(t, keys...)); string(got) != kf.Text {
				t.Errorf("encodeKeyFile = %q, want %q", got, kf.Text)
			}
		})
	}
	for _, c := range keyFileRefusals(t, v) {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseKeyFile(testKeyPath, []byte(c.text))
			if r != nil || !errors.Is(err, ErrKeyFileFormat) {
				t.Fatalf("parseKeyFile = a ring %v, %v; want no ring, ErrKeyFileFormat", r != nil, err)
			}
			msg := err.Error()
			if want := fmt.Sprintf("%s: line %d: ", testKeyPath, c.line); !strings.Contains(msg, want) {
				t.Errorf("%q does not name %q", msg, want)
			}
			if !strings.Contains(msg, c.says) {
				t.Errorf("%q does not say %q", msg, c.says)
			}
			if hexRun.MatchString(msg) {
				t.Errorf("%q holds a hex run of 32 or more", msg)
			}
		})
	}
}
