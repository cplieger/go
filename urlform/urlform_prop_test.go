package urlform

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestClassifyBackslashCanonicalizationProperty is the every-PR
// property net over the Classify untrusted-URL boundary (the generated
// fuzz exploration runs only in the weekly bounded job): a browser-style
// backslash authority must classify to the same public semantic facts as its
// slash-canonical form, pinning the backslash canonicalization that keeps
// host evidence recoverable. The generator stays inside the canonicalized
// scope on purpose - a schemeless authority (ambient special scheme) with
// every backslash ahead of any query/fragment - since outside that scope
// (non-special schemes) the WHATWG reading keeps backslashes ordinary and
// the law deliberately does not hold.
func TestClassifyBackslashCanonicalizationProperty(t *testing.T) {
	type semanticFacts struct {
		Host              string
		Scheme            string
		Port              string
		Class             Class
		HasUserInfo       bool
		HostUnrecoverable bool
	}
	suffix := rapid.StringMatching(`[A-Za-z0-9._~/?&=%+-]{0,64}`)

	rapid.Check(t, func(t *rapid.T) {
		raw := `\\animebytes.tv/` + suffix.Draw(t, "suffix")
		canonical := strings.ReplaceAll(raw, `\`, "/")

		rawForm := Classify(raw)
		got := semanticFacts{rawForm.Host, rawForm.Scheme, rawForm.Port, rawForm.Class, rawForm.HasUserInfo, rawForm.HostUnrecoverable}
		slashForm := Classify(canonical)
		want := semanticFacts{slashForm.Host, slashForm.Scheme, slashForm.Port, slashForm.Class, slashForm.HasUserInfo, slashForm.HostUnrecoverable}
		if got != want {
			t.Errorf("Classify(%q) semantic facts = %+v, want canonical-slash facts %+v", raw, got, want)
		}
	})
}

// TestClassifyTabNewlineInsensitivityProperty pins the WHATWG preprocessing
// as a spec-derived metamorphic law: inserting ASCII tab/newline bytes at
// ANY positions in a URL string never changes the classified facts (a
// browser deletes them wherever they appear, so neither may the
// classification) - only the HasTabOrNewline smuggling flag and only when
// an insertion lands interior may differ. This is the property that makes
// string-level gates over the facts smuggling-proof.
func TestClassifyTabNewlineInsensitivityProperty(t *testing.T) {
	type semanticFacts struct {
		Trimmed           string
		Host              string
		Scheme            string
		Port              string
		Class             Class
		HasUserInfo       bool
		HasBackslash      bool
		HostUnrecoverable bool
	}
	facts := func(f Form) semanticFacts {
		return semanticFacts{f.Trimmed, f.Host, f.Scheme, f.Port, f.Class, f.HasUserInfo, f.HasBackslash, f.HostUnrecoverable}
	}
	base := rapid.SampledFrom([]string{
		"https://animebytes.tv/torrents.php?id=1&torrentid=2",
		"https:/animebytes.tv/x",
		"HTTPS://user@NYAA.si:8080/view/1#frag",
		"animebytes.tv/torrents.php?id=1",
		"//animebytes.tv/x",
		"/torrents.php?id=1",
		`\\animebytes.tv/x`,
		"javascript:alert(1)",
	})
	smuggle := rapid.SliceOfN(rapid.SampledFrom([]rune{'\t', '\n', '\r'}), 1, 8)

	rapid.Check(t, func(t *rapid.T) {
		orig := base.Draw(t, "base")
		mutated := orig
		for _, r := range smuggle.Draw(t, "smuggle") {
			at := rapid.IntRange(0, len(mutated)).Draw(t, "at")
			mutated = mutated[:at] + string(r) + mutated[at:]
		}

		got := facts(Classify(mutated))
		want := facts(Classify(orig))
		if got != want {
			t.Errorf("Classify(%q) facts = %+v, want the smuggle-free facts %+v", mutated, got, want)
		}
	})
}
