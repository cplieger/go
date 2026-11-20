package httpwire_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/go/httpwire"
)

var schemaFieldCodes = []string{"malformed", "required", "unknown", "enum", "union", "range", "pattern"}

// checkEntries holds the shape every CheckBody answer keeps: field codes from
// the one map, one entry per path, a message on each.
func checkEntries(t *testing.T, entries []httpwire.FieldError) {
	t.Helper()
	seen := map[string]bool{}
	for _, e := range entries {
		if !slices.Contains(schemaFieldCodes, e.Code) {
			t.Errorf("entry %q has code %q, outside the field-code map", e.Path, e.Code)
		}
		if seen[e.Path] {
			t.Errorf("two entries at path %q", e.Path)
		}
		seen[e.Path] = true
		if e.Message == "" {
			t.Errorf("entry %q has no message", e.Path)
		}
	}
}

func FuzzCheckBody_verdictIgnoresLayout(f *testing.F) {
	for _, seed := range []string{
		item(""),
		item(`"color":"blue","tags":["a",2],"labels":{"x":1},"quoted":"5x"`),
		`{"name":"ééééé","count":9007199254740993,"big":18446744073709551616,"ratio":1e400}`,
		`{"kind":"circle","radius":1,"side":2}`,
		`[{"kind":"square","radius":1}]`,
		`null`, ``, `{"name":`, `"\ud800"`, `{"name":"a","count":1,"name":2}`,
	} {
		f.Add(seed, 0)
		f.Add(seed, 201)
	}
	b, err := readFixtureDoc()
	if err != nil {
		f.Fatal(err)
	}
	s, err := httpwire.ParseSchema(b)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string, status int) {
		for _, op := range []string{"createItem", "putShape"} {
			got := s.CheckBody(op, status, []byte(body))
			checkEntries(t, got)
			if again := s.CheckBody(op, status, []byte(body)); !slices.Equal(got, again) {
				t.Fatalf("CheckBody is not deterministic: %v then %v", got, again)
			}
			if !json.Valid([]byte(body)) {
				continue
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if other := s.CheckBody(op, status, compact.Bytes()); !slices.Equal(got, other) {
				t.Fatalf("CheckBody(%q) = %v, but %v on its compact form", body, got, other)
			}
		}
	})
}

func FuzzParseSchema_acceptedDocumentsCheck(f *testing.F) {
	b, err := readFixtureDoc()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(b), item(""))
	f.Add(schemaDoc(`{"Root":{"$ref":"#/components/schemas/B"},"B":{"$ref":"#/components/schemas/Root"}}`), `{}`)
	f.Add(schemaDoc(`{"Root":{"type":"object","properties":{"a":{"anyOf":[{"$ref":"#/components/schemas/Root"},{"type":"null"}]}}}}`), `{"a":{"a":null}}`)
	f.Add(schemaDoc(`{"Root":{"oneOf":[{"$ref":"#/components/schemas/A"}],"discriminator":{"propertyName":"k","mapping":{"a":"#/components/schemas/A"}}},"A":{"type":"object","properties":{"k":{"const":"a"}}}}`), `{"k":"a"}`)
	f.Fuzz(func(t *testing.T, doc, body string) {
		s, err := httpwire.ParseSchema([]byte(doc))
		if err != nil {
			if s != nil {
				t.Fatal("ParseSchema returned a Schema with its error")
			}
			return
		}
		for _, op := range []string{"op", "createItem", "putShape"} {
			for _, status := range []int{0, 200, 201} {
				checkEntries(t, s.CheckBody(op, status, []byte(body)))
			}
		}
	})
}
