package slogx

import "testing"

func TestParseFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		raw        string
		def        Format
		wantFormat Format
		wantOK     bool
	}{
		{name: "empty uses default and is not an error", raw: "", def: Text, wantFormat: Text, wantOK: true},
		{name: "empty honors a json default", raw: "", def: JSON, wantFormat: JSON, wantOK: true},
		{name: "whitespace only is treated as empty", raw: "   ", def: JSON, wantFormat: JSON, wantOK: true},
		{name: "text", raw: "text", def: JSON, wantFormat: Text, wantOK: true},
		{name: "json", raw: "json", def: Text, wantFormat: JSON, wantOK: true},
		{name: "uppercase", raw: "JSON", def: Text, wantFormat: JSON, wantOK: true},
		{name: "mixed case", raw: "Text", def: JSON, wantFormat: Text, wantOK: true},
		{name: "surrounding space is trimmed", raw: "  json  ", def: Text, wantFormat: JSON, wantOK: true},
		{name: "unrecognized falls back and flags", raw: "logfmt", def: JSON, wantFormat: JSON, wantOK: false},
		{name: "unrecognized honors a text default", raw: "yaml", def: Text, wantFormat: Text, wantOK: false},
		{name: "partial match is not accepted", raw: "jso", def: Text, wantFormat: Text, wantOK: false},
		{name: "superstring is not accepted", raw: "jsonl", def: Text, wantFormat: Text, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotFormat, gotOK := ParseFormat(tc.raw, tc.def)
			if gotFormat != tc.wantFormat {
				t.Errorf("ParseFormat(%q, %v) format = %v, want %v", tc.raw, tc.def, gotFormat, tc.wantFormat)
			}
			if gotOK != tc.wantOK {
				t.Errorf("ParseFormat(%q, %v) ok = %v, want %v", tc.raw, tc.def, gotOK, tc.wantOK)
			}
		})
	}
}
