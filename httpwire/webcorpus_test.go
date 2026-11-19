package httpwire_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// The TS half's tests read the same three files, so a code, a tag rule or a
// key rule changed on one side fails both in one change.
const (
	wireCodesFile = "web/testdata/wire-codes.json"
	tagsFile      = "web/testdata/tags.json"
	keysFile      = "web/testdata/idempotency-keys.json"
)

func readCorpus(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestWireCodesFile(t *testing.T) {
	want := httpwire.CatalogCodes(httpwire.NewCatalog())
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		b, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wireCodesFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []httpserver.ErrorCode
	readCorpus(t, wireCodesFile, &got)
	if !slices.Equal(got, want) {
		t.Fatalf("%s lists %q, want NewCatalog's codes %q; regenerate with UPDATE_GOLDEN=1 go test . -run TestWireCodesFile in httpwire", wireCodesFile, got, want)
	}
}

type tagCase struct {
	Tag   string `json:"tag"`
	Valid bool   `json:"valid"`
}

func TestTagsFile(t *testing.T) {
	var cases []tagCase
	readCorpus(t, tagsFile, &cases)
	if len(cases) < 20 {
		t.Fatalf("%s holds %d cases, want the full corpus", tagsFile, len(cases))
	}
	valid := 0
	for _, c := range cases {
		var tag httpwire.Tag
		err := tag.UnmarshalText([]byte(c.Tag))
		if (err == nil) != c.Valid {
			t.Errorf("UnmarshalText(%q) error %v, corpus says valid %v", c.Tag, err, c.Valid)
		}
		if err == nil && tag.String() != c.Tag {
			t.Errorf("UnmarshalText(%q) then String answers %q", c.Tag, tag.String())
		}
		if _, ok := httpwire.ParseIfMatch(`"` + c.Tag + `"`); ok != c.Valid {
			t.Errorf("If-Match %q accepted %v, corpus says valid %v", `"`+c.Tag+`"`, ok, c.Valid)
		}
		if c.Valid {
			valid++
		}
	}
	if valid == 0 || valid == len(cases) {
		t.Fatalf("%s holds %d valid of %d, want both kinds", tagsFile, valid, len(cases))
	}
}

type keyCase struct {
	Key    string  `json:"key"`
	Header *string `json:"header"`
}

// naiveQuote is what a client that skips the bound check would send.
func naiveQuote(key string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(key) + `"`
}

func TestIdempotencyKeysFile(t *testing.T) {
	var cases []keyCase
	readCorpus(t, keysFile, &cases)
	if len(cases) < 10 {
		t.Fatalf("%s holds %d cases, want the full corpus", keysFile, len(cases))
	}
	refused := 0
	for _, c := range cases {
		if c.Header == nil {
			refused++
			if key, ok := httpwire.ParseIdempotencyKey(naiveQuote(c.Key)); ok {
				t.Errorf("key %q is refused in the corpus, but its quoted form parses as %q", c.Key, key)
			}
			continue
		}
		key, ok := httpwire.ParseIdempotencyKey(*c.Header)
		if !ok || key != c.Key {
			t.Errorf("Idempotency-Key %s parses as %q, %v; want %q", *c.Header, key, ok, c.Key)
		}
	}
	if refused == 0 || refused == len(cases) {
		t.Fatalf("%s holds %d refused of %d, want both kinds", keysFile, refused, len(cases))
	}
}
