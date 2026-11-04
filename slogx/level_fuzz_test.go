package slogx

import (
	"log/slog"
	"strings"
	"testing"
)

// FuzzParseLevel asserts the invariants every caller relies on against arbitrary
// LOG_LEVEL values: it never panics, a rejected value returns the default
// unchanged, a blank value is always the default with ok=true, and the
// long-form warning alias means exactly what the warn spelling means.
func FuzzParseLevel(f *testing.F) {
	for _, seed := range []string{
		"", "  ", "debug", "info", "warn", "warning", "WARNING",
		"error", "warn+1", "debug-2", "banana", "warn ", "12", "info+99",
		"warning+1", "warning-2", "warningfoo", "warning+", "+1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		level, ok := ParseLevel(raw, slog.LevelInfo)
		if !ok && level != slog.LevelInfo {
			t.Fatalf("ParseLevel(%q) not ok but level=%v, want the default LevelInfo", raw, level)
		}
		if strings.TrimSpace(raw) == "" && (!ok || level != slog.LevelInfo) {
			t.Fatalf("ParseLevel(%q) = (%v, %v), want (Info, true) for a blank value", raw, level, ok)
		}
		// A recognized value must round-trip: re-parsing its canonical string
		// yields the same level (idempotence), independent of the default given.
		if ok && strings.TrimSpace(raw) != "" {
			if reparsed, reok := ParseLevel(level.String(), slog.LevelError); !reok || reparsed != level {
				t.Fatalf("ParseLevel(%q)=%v did not round-trip: ParseLevel(%q, Error)=(%v, %v)", raw, level, level.String(), reparsed, reok)
			}
		}
		// Alias consistency: whenever a warning-spelled value is accepted, it
		// means exactly what the warn-spelled form means. One-directional on
		// purpose: "warn"+X can itself spell the bare alias (X = "ing") with
		// no accepted warning-spelled twin.
		if aliasLevel, aliasOK := ParseLevel("warning"+raw, slog.LevelInfo); aliasOK {
			if warnLevel, warnOK := ParseLevel("warn"+raw, slog.LevelInfo); !warnOK || warnLevel != aliasLevel {
				t.Fatalf("alias divergence: ParseLevel(%q)=(%v, %v) but ParseLevel(%q)=(%v, %v)",
					"warning"+raw, aliasLevel, aliasOK, "warn"+raw, warnLevel, warnOK)
			}
		}
	})
}
