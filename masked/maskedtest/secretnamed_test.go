package maskedtest_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/go/masked/maskedtest"
)

// state is a string enum: its values are a fixed set, not free text.
type state string

type releaseItem struct {
	ReleaseKey string `json:"release_key"`
	Title      string `json:"title"`
}

type feedKeyCreated struct {
	Key string `json:"key"`
}

type inner struct {
	Passkey string `json:"ab_passkey"`
	Token   state  `json:"token_state"`
	KeyID   uint64 `json:"key_id"`
}

type outer struct {
	Keys  map[string]inner `json:"keys"`
	Items []inner          `json:"items"`
}

type base struct {
	AccessToken string `json:"access_token"`
}

type shapes struct {
	Keyring  map[state]string `json:"keyring"`
	States   map[state]state  `json:"token_states"`
	Password *string          `json:"pw_password,omitempty"`
	APIKey   string
	Secret   string `json:"-"`
	token    string
	Upper    string   `json:"X_API_TOKEN"`
	Tokens   []string `json:"refresh_tokens"`
	Raw      []byte   `json:"session_secret"`
	base
	KeyCount int      `json:"key_count"`
	Digest   [32]byte `json:"key_digest"`
}

// chain refers to itself through a slice and a map.
type chain []map[string]chain

type wireNode struct {
	Next  *wireNode `json:"next"`
	Token string    `json:"token"`
	Chain chain     `json:"token_chain"`
}

// The literals write every field: deadset keeps a field reflection reads
// only where the program writes it
// (github.com/cplieger/deadset-go docs/encoding-reflection.md).
var (
	release = releaseItem{ReleaseKey: "nyaa:1", Title: "t"}
	created = feedKeyCreated{Key: "k"}
	nested  = outer{
		Keys:  map[string]inner{"a": {Passkey: "p", Token: "set", KeyID: 1}},
		Items: []inner{{Passkey: "p", Token: "set", KeyID: 1}},
	}
)

func TestSecretNamed(t *testing.T) {
	t.Parallel()
	enums := []reflect.Type{reflect.TypeFor[state]()}
	t.Run("credential_named_field_fails", func(t *testing.T) {
		t.Parallel()
		got := maskedtest.SecretNamed(maskedtest.NameAllow{Enums: enums}, reflect.TypeOf(nested))
		want := []string{
			`maskedtest_test.inner.Passkey: json name "ab_passkey" holds text (string)`,
			`maskedtest_test.outer.Keys: json name "keys" holds text (map[string]maskedtest_test.inner)`,
		}
		if !slices.Equal(got, want) {
			t.Errorf("SecretNamed() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("allowed_with_reason", func(t *testing.T) {
		t.Parallel()
		allow := maskedtest.NameAllow{
			Fields: map[string]string{
				"maskedtest_test.releaseItem.ReleaseKey": "a release's identity: its tracker and torrent id",
				"maskedtest_test.feedKeyCreated.Key":     "a new feed key, shown once to the owner who created it",
			},
		}
		got := maskedtest.SecretNamed(allow, reflect.TypeOf(release), reflect.TypeOf(created), nil)
		if len(got) > 0 {
			t.Errorf("SecretNamed() = %q, want none", got)
		}
	})
	t.Run("shapes", func(t *testing.T) {
		t.Parallel()
		pw := "p"
		fixture := shapes{
			Keyring:     map[state]string{"set": "v"},
			States:      map[state]state{"set": "set"},
			Password:    &pw,
			APIKey:      "k",
			Secret:      "s",
			token:       "t",
			Upper:       "u",
			Tokens:      []string{"t"},
			Raw:         []byte("r"),
			AccessToken: "a",
			KeyCount:    1,
			Digest:      [32]byte{1},
		}
		got := maskedtest.SecretNamed(maskedtest.NameAllow{Enums: enums}, reflect.TypeFor[*shapes]())
		want := []string{
			`maskedtest_test.base.AccessToken: json name "access_token" holds text (string)`,
			`maskedtest_test.shapes.APIKey: json name "APIKey" holds text (string)`,
			`maskedtest_test.shapes.Keyring: json name "keyring" holds text (map[maskedtest_test.state]string)`,
			`maskedtest_test.shapes.Password: json name "pw_password" holds text (*string)`,
			`maskedtest_test.shapes.Raw: json name "session_secret" holds text ([]uint8)`,
			`maskedtest_test.shapes.Tokens: json name "refresh_tokens" holds text ([]string)`,
			`maskedtest_test.shapes.Upper: json name "X_API_TOKEN" holds text (string)`,
		}
		if !slices.Equal(got, want) {
			t.Errorf("SecretNamed(%T) =\n%q\nwant\n%q", fixture, got, want)
		}
	})
	t.Run("stale_allow", func(t *testing.T) {
		t.Parallel()
		allow := maskedtest.NameAllow{
			Fields: map[string]string{
				"maskedtest_test.releaseItem.Gone":       "a field that was removed",
				"maskedtest_test.releaseItem.Title":      "a field whose name is no credential's",
				"maskedtest_test.feedKeyCreated.Key":     "",
				"maskedtest_test.releaseItem.ReleaseKey": "a release's identity",
			},
			Enums: []reflect.Type{reflect.TypeFor[state](), reflect.TypeFor[time.Month]()},
		}
		got := maskedtest.SecretNamed(allow, reflect.TypeOf(release), reflect.TypeOf(created))
		want := []string{
			"allow entry maskedtest_test.feedKeyCreated.Key: has no reason",
			"allow entry maskedtest_test.releaseItem.Gone: names no credential-named text field a root reaches",
			"allow entry maskedtest_test.releaseItem.Title: names no credential-named text field a root reaches",
			"allow entry maskedtest_test.state: reached by no root",
			"allow entry time.Month: reached by no root",
		}
		if !slices.Equal(got, want) {
			t.Errorf("SecretNamed() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("cycles", func(t *testing.T) {
		t.Parallel()
		loop := &wireNode{Token: "t", Chain: chain{}}
		loop.Next = loop
		done := make(chan []string, 1)
		go func() { done <- maskedtest.SecretNamed(maskedtest.NameAllow{}, reflect.TypeOf(loop)) }()
		select {
		case got := <-done:
			want := []string{
				`maskedtest_test.wireNode.Chain: json name "token_chain" holds text (maskedtest_test.chain)`,
				`maskedtest_test.wireNode.Token: json name "token" holds text (string)`,
			}
			if !slices.Equal(got, want) {
				t.Errorf("SecretNamed() = %q, want %q", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("SecretNamed did not return within 10s on a cyclic type")
		}
	})
}
