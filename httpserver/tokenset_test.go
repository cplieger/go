package httpserver_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

func TestNewTokenSet_refuses(t *testing.T) {
	secret := "s3cr3t-token-value"
	cases := []struct {
		name      string
		in        []string
		want      error
		wantIndex int
		wantFirst int
	}{
		{"none", nil, httpserver.ErrNoTokens, -1, -1},
		{"an empty token", []string{secret, ""}, httpserver.ErrTokenEmpty, 1, -1},
		{"a token one byte over the bound", []string{secret + strings.Repeat("x", httpserver.MaxTokenBytes+1-len(secret))}, httpserver.ErrTokenTooLong, 0, -1},
		{"a repeated token", []string{secret, "second-token", secret}, httpserver.ErrTokenRepeated, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := httpserver.NewTokenSet(tc.in)
			var te *httpserver.TokenSetError
			if !errors.As(err, &te) || !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want a *TokenSetError matching %v", err, tc.want)
			}
			if te.Index != tc.wantIndex || te.First != tc.wantFirst {
				t.Errorf("Index, First = %d, %d, want %d, %d", te.Index, te.First, tc.wantIndex, tc.wantFirst)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("error %q quotes the secret", err)
			}
		})
	}
}

func TestTokenSet_Match(t *testing.T) {
	set, err := httpserver.NewTokenSet([]string{"alpha-token", "bravo-token", strings.Repeat("c", httpserver.MaxTokenBytes)})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		presented string
		wantIndex int
		wantOK    bool
	}{
		{"the first", "alpha-token", 0, true},
		{"the second", "bravo-token", 1, true},
		{"one at the bound", strings.Repeat("c", httpserver.MaxTokenBytes), 2, true},
		{"no match", "charlie-token", -1, false},
		{"a prefix of one", "alpha", -1, false},
		{"empty", "", -1, false},
		{"one byte over the bound", strings.Repeat("c", httpserver.MaxTokenBytes+1), -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index, ok := set.Match(tc.presented)
			if index != tc.wantIndex || ok != tc.wantOK {
				t.Errorf("Match = (%d, %v), want (%d, %v)", index, ok, tc.wantIndex, tc.wantOK)
			}
		})
	}
}

func TestTokenSet_zeroMatchesNothing(t *testing.T) {
	var set httpserver.TokenSet
	if index, ok := set.Match(""); ok || index != -1 {
		t.Errorf("zero Match(\"\") = (%d, %v), want (-1, false)", index, ok)
	}
	if set.Len() != 0 {
		t.Errorf("zero Len = %d, want 0", set.Len())
	}
}

func TestTokenSet_comparesEveryDigest(t *testing.T) {
	set, err := httpserver.NewTokenSet([]string{"alpha-token", "bravo-token", "charlie-token", "delta-token"})
	if err != nil {
		t.Fatal(err)
	}
	calls, restore := httpserver.CountDigestCompares()
	t.Cleanup(restore)
	for _, presented := range []string{"alpha-token", "delta-token", "no-such-token"} {
		*calls = 0
		set.Match(presented)
		if *calls != set.Len() {
			t.Errorf("Match(%q) compared %d digests, want all %d", presented, *calls, set.Len())
		}
	}
	*calls = 0
	set.Match(strings.Repeat("x", httpserver.MaxTokenBytes+1))
	if *calls != 0 {
		t.Errorf("an over-long value was compared %d times, want 0", *calls)
	}
}

func FuzzTokenSet_matchesOnlyItsOwn(f *testing.F) {
	for _, seed := range []string{"", "alpha-token", "alpha-tokem", "bravo", strings.Repeat("z", httpserver.MaxTokenBytes+1)} {
		f.Add(seed)
	}
	configured := []string{"alpha-token", "bravo-token"}
	set, err := httpserver.NewTokenSet(configured)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, presented string) {
		index, ok := set.Match(presented)
		want := -1
		for i, c := range configured {
			if c == presented {
				want = i
			}
		}
		if ok != (want >= 0) || index != want {
			t.Fatalf("Match(%q) = (%d, %v), want index %d", presented, index, ok, want)
		}
	})
}
