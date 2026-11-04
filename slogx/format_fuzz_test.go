package slogx

import (
	"strings"
	"testing"
)

// FuzzParseFormat asserts the invariants every caller relies on against
// arbitrary log-format config values: it never panics, the result is always
// one of the two valid formats, a rejected value returns the default
// unchanged, and a blank value is always the default with ok=true.
func FuzzParseFormat(f *testing.F) {
	for _, seed := range []string{
		"", "  ", "text", "json", "JSON", "Text", " json ", "logfmt",
		"jsonl", "jso", "yaml", "0", "text\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		format, ok := ParseFormat(raw, JSON)
		if format != Text && format != JSON {
			t.Fatalf("ParseFormat(%q) = %v, not a valid Format", raw, format)
		}
		if !ok && format != JSON {
			t.Fatalf("ParseFormat(%q) not ok but format=%v, want the default JSON", raw, format)
		}
		if strings.TrimSpace(raw) == "" && (!ok || format != JSON) {
			t.Fatalf("ParseFormat(%q) = (%v, %v), want (JSON, true) for a blank value", raw, format, ok)
		}
		// A recognized non-blank value must be default-independent: the same
		// input parses to the same format whatever default is supplied.
		if ok && strings.TrimSpace(raw) != "" {
			if other, otherOK := ParseFormat(raw, Text); !otherOK || other != format {
				t.Fatalf("ParseFormat(%q) default-dependent: (JSON def)=%v, (Text def)=(%v, %v)", raw, format, other, otherOK)
			}
		}
	})
}
