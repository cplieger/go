package httpwire_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/cplieger/go/httpwire"
)

var wireVersionForm = regexp.MustCompile(`^[0-9a-f]{16}$`)

func FuzzWireVersion_ignoresLayout(f *testing.F) {
	f.Add(wireFixture(f))
	for _, seed := range []string{
		`{}`, `{"info":{"description":1}}`, schemaAt(`{"items":[{"description":"d"}]}`),
		`{"paths":{"/a":{"get":{"responses":{"404":{"x-error-codes":{"c":{"remediation":"r"}}}}}}}}`,
		`{"a":1,"a":2}`, "{\"\xff\":\"\xfe\"}", `[]`, `{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		v := httpwire.WireVersion(doc)
		if v == "" {
			trimmed := bytes.TrimSpace(doc)
			if json.Valid(doc) && len(trimmed) > 0 && trimmed[0] == '{' {
				t.Fatalf("WireVersion is empty for the JSON object %q", doc)
			}
			return
		}
		if !wireVersionForm.MatchString(v) {
			t.Fatalf("WireVersion = %q, not 16 lowercase hex", v)
		}
		for name, layout := range map[string]func(*bytes.Buffer, []byte) error{
			"compact": json.Compact,
			"indent":  func(b *bytes.Buffer, src []byte) error { return json.Indent(b, src, "", "\t") },
		} {
			var b bytes.Buffer
			if err := layout(&b, doc); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := httpwire.WireVersion(b.Bytes()); got != v {
				t.Fatalf("%s layout moved the version: %q then %q", name, v, got)
			}
		}
	})
}
