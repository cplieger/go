package slogx

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		raw       string
		def       slog.Level
		wantLevel slog.Level
		wantOK    bool
	}{
		{name: "empty uses default and is not an error", raw: "", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: true},
		{name: "empty honors a non-info default", raw: "", def: slog.LevelWarn, wantLevel: slog.LevelWarn, wantOK: true},
		{name: "whitespace only is treated as empty", raw: "   ", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: true},
		{name: "debug", raw: "debug", def: slog.LevelInfo, wantLevel: slog.LevelDebug, wantOK: true},
		{name: "info overrides a warn default", raw: "info", def: slog.LevelWarn, wantLevel: slog.LevelInfo, wantOK: true},
		{name: "warn", raw: "warn", def: slog.LevelInfo, wantLevel: slog.LevelWarn, wantOK: true},
		{name: "error", raw: "error", def: slog.LevelInfo, wantLevel: slog.LevelError, wantOK: true},
		{name: "uppercase", raw: "DEBUG", def: slog.LevelInfo, wantLevel: slog.LevelDebug, wantOK: true},
		{name: "mixed case", raw: "Warn", def: slog.LevelInfo, wantLevel: slog.LevelWarn, wantOK: true},
		{name: "surrounding space is trimmed", raw: "  error  ", def: slog.LevelInfo, wantLevel: slog.LevelError, wantOK: true},
		{name: "long-form warning alias", raw: "warning", def: slog.LevelInfo, wantLevel: slog.LevelWarn, wantOK: true},
		{name: "long-form warning alias uppercase", raw: "WARNING", def: slog.LevelInfo, wantLevel: slog.LevelWarn, wantOK: true},
		{name: "warning alias with positive offset", raw: "warning+1", def: slog.LevelInfo, wantLevel: slog.LevelWarn + 1, wantOK: true},
		{name: "warning alias with negative offset", raw: "warning-2", def: slog.LevelInfo, wantLevel: slog.LevelWarn - 2, wantOK: true},
		{name: "warning with trailing junk is rejected", raw: "warningfoo", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: false},
		{name: "warning alias with sign but no digits is rejected", raw: "warning+", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: false},
		{name: "warning alias with malformed offset is rejected", raw: "warning+1x", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: false},
		{name: "bare offset without a level name is rejected", raw: "+4", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: false},
		{name: "positive offset", raw: "warn+1", def: slog.LevelInfo, wantLevel: slog.LevelWarn + 1, wantOK: true},
		{name: "negative offset", raw: "error-2", def: slog.LevelInfo, wantLevel: slog.LevelError - 2, wantOK: true},
		{name: "unparseable falls back and flags", raw: "banana", def: slog.LevelInfo, wantLevel: slog.LevelInfo, wantOK: false},
		{name: "unparseable honors the default", raw: "loud", def: slog.LevelError, wantLevel: slog.LevelError, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotLevel, gotOK := ParseLevel(tc.raw, tc.def)
			if gotLevel != tc.wantLevel {
				t.Errorf("ParseLevel(%q, %v) level = %v, want %v", tc.raw, tc.def, gotLevel, tc.wantLevel)
			}
			if gotOK != tc.wantOK {
				t.Errorf("ParseLevel(%q, %v) ok = %v, want %v", tc.raw, tc.def, gotOK, tc.wantOK)
			}
		})
	}
}
