package urlform

import (
	"encoding/json"
	"os"
	"testing"
)

// fixtureCase is one row of testdata/whatwg-fixtures.json: an input plus the
// expected urlform facts and the browser truth (wpt) printed on failure. The
// rows' source and note keys are provenance for whoever re-derives a row and
// are not decoded. Expectations are hand-vetted against the WHATWG reading,
// never regenerated from the implementation.
type fixtureCase struct {
	Name              string `json:"name"`
	Input             string `json:"input"`
	Class             string `json:"class"`
	Host              string `json:"host"`
	Scheme            string `json:"scheme"`
	Port              string `json:"port"`
	WPT               string `json:"wpt"`
	HasUserInfo       bool   `json:"hasUserInfo"`
	HasBackslash      bool   `json:"hasBackslash"`
	HasTabOrNewline   bool   `json:"hasTabOrNewline"`
	HostUnrecoverable bool   `json:"hostUnrecoverable"`
}

// fixtureClasses maps the fixture file's class vocabulary onto the enum.
var fixtureClasses = map[string]Class{
	"empty":             ClassEmpty,
	"malformed":         ClassMalformed,
	"absolute":          ClassAbsolute,
	"hidden_host":       ClassHiddenHost,
	"protocol_relative": ClassProtocolRelative,
	"schemeless_host":   ClassSchemelessHost,
	"relative":          ClassRelative,
}

// TestClassifyWHATWGFixtures checks Classify against the curated conformance
// corpus, the external oracle the fuzz and property tests cannot provide. The
// corpus is a snapshot: re-check the WHATWG URL Standard
// (https://url.spec.whatwg.org) and WPT's url/resources/urltestdata.json
// (https://github.com/web-platform-tests/wpt) against the commit the corpus
// provenance field records, and re-derive any changed expectation from the
// spec, never from the implementation.
func TestClassifyWHATWGFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/whatwg-fixtures.json")
	if err != nil {
		t.Fatalf("read fixture corpus: %v", err)
	}
	var corpus struct {
		Provenance string        `json:"provenance"`
		Cases      []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode fixture corpus: %v", err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("fixture corpus is empty")
	}
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			wantClass, ok := fixtureClasses[tc.Class]
			if !ok {
				t.Fatalf("fixture class %q is not in the enum vocabulary", tc.Class)
			}
			f := Classify(tc.Input)
			if f.Class != wantClass {
				t.Errorf("Class = %v, want %v (wpt: %s)", f.Class, wantClass, tc.WPT)
			}
			if f.Host != tc.Host {
				t.Errorf("Host = %q, want %q (wpt: %s)", f.Host, tc.Host, tc.WPT)
			}
			if f.Scheme != tc.Scheme {
				t.Errorf("Scheme = %q, want %q", f.Scheme, tc.Scheme)
			}
			if f.Port != tc.Port {
				t.Errorf("Port = %q, want %q", f.Port, tc.Port)
			}
			if f.HasUserInfo != tc.HasUserInfo {
				t.Errorf("HasUserInfo = %v, want %v", f.HasUserInfo, tc.HasUserInfo)
			}
			if f.HasBackslash != tc.HasBackslash {
				t.Errorf("HasBackslash = %v, want %v", f.HasBackslash, tc.HasBackslash)
			}
			if f.HasTabOrNewline != tc.HasTabOrNewline {
				t.Errorf("HasTabOrNewline = %v, want %v", f.HasTabOrNewline, tc.HasTabOrNewline)
			}
			if f.HostUnrecoverable != tc.HostUnrecoverable {
				t.Errorf("HostUnrecoverable = %v, want %v", f.HostUnrecoverable, tc.HostUnrecoverable)
			}
		})
	}
}
