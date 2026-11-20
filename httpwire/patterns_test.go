package httpwire_test

import (
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// The TS half's pattern test reads the same corpus and matches each case
// with new RegExp(pattern, "u"), so both engines give one verdict.
const wirePatternsFile = "web/testdata/wirepatterns.json"

var wirePatternConstants = map[string]string{
	"PatternJSONString": httpwire.PatternJSONString,
	"PatternJSONNumber": httpwire.PatternJSONNumber,
	"PatternJSONInt":    httpwire.PatternJSONInt,
	"PatternJSONUint":   httpwire.PatternJSONUint,
	"PatternJSONBool":   httpwire.PatternJSONBool,
	"PatternHeader":     httpwire.PatternHeader,
}

type wirePatternCase struct {
	Input string `json:"input"`
	Match bool   `json:"match"`
}

type wirePatternEntry struct {
	Pattern string            `json:"pattern"`
	Cases   []wirePatternCase `json:"cases"`
}

func readWirePatterns(t *testing.T) map[string]wirePatternEntry {
	t.Helper()
	b, err := os.ReadFile(wirePatternsFile)
	if err != nil {
		t.Fatal(err)
	}
	var corpus map[string]wirePatternEntry
	if err := json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func TestWirePatternCorpus(t *testing.T) {
	corpus := readWirePatterns(t)
	if got, want := slices.Sorted(maps.Keys(corpus)), slices.Sorted(maps.Keys(wirePatternConstants)); !slices.Equal(got, want) {
		t.Fatalf("corpus names %v, want the six constants %v", got, want)
	}
	for name, entry := range corpus {
		t.Run(name, func(t *testing.T) {
			if entry.Pattern != wirePatternConstants[name] {
				t.Fatalf("corpus pattern %q, want the constant %q", entry.Pattern, wirePatternConstants[name])
			}
			var matches, misses int
			re := regexp.MustCompile(entry.Pattern)
			for _, c := range entry.Cases {
				if got := re.MatchString(c.Input); got != c.Match {
					t.Errorf("MatchString(%q) = %v, want %v", c.Input, got, c.Match)
				}
				if c.Match {
					matches++
				} else {
					misses++
				}
			}
			if matches == 0 || misses == 0 {
				t.Errorf("corpus has %d matching and %d refused cases, want both", matches, misses)
			}
		})
	}
}

func TestIsWirePattern(t *testing.T) {
	for name, p := range wirePatternConstants {
		if !httpwire.IsWirePattern(p) {
			t.Errorf("IsWirePattern(%s) = false", name)
		}
	}
	for _, p := range []string{
		"",
		`^[a-z]+$`,
		`^(0|[1-9][0-9]*)$`,
		`^-?(0|[1-9][0-9]*)$`,
		httpwire.PatternJSONInt + " ",
		httpwire.PatternJSONInt[1:],
		`^(?:True|false)$`,
	} {
		if httpwire.IsWirePattern(p) {
			t.Errorf("IsWirePattern(%q) = true", p)
		}
	}
}
