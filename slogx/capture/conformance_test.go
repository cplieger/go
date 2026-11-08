// Conformance oracles pinning the Recorder against the stdlib itself,
// closing the gap the hand-enumerated cases in capture_test.go leave open:
// if log/slog's documented Handler contract and the Recorder's
// materialization ever disagree, these fail without anyone having had to
// anticipate the divergence.
//
// Two layers:
//
//   - TestSlogtestConformance runs testing/slogtest, the stdlib's own
//     handler-contract battery, against the Recorder.
//   - TestRenderFaithfulDifferential pins the package's headline claim
//     (captured records hold what a stdlib handler would emit) by driving
//     identical scenarios through a real stdlib handler directly AND through
//     a Recorder whose records are then replayed into an identically
//     configured stdlib handler: the two outputs must match byte for byte,
//     for both the Text and JSON handlers.

package capture

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"testing"
	"testing/slogtest"
	"time"
)

// TestSlogtestConformance runs the stdlib's Handler-contract battery against
// the Recorder, projecting each captured record into the nested-map shape the
// battery checks. This is the authoritative oracle for the contract rules
// normalizeAttrs and the derived handles implement (resolve, drop-zero-attr,
// drop-empty-group, inline-empty-key, WithAttrs/WithGroup folding, zero-time
// and zero-PC omission).
func TestSlogtestConformance(t *testing.T) {
	var rec *Recorder
	slogtest.Run(t, func(*testing.T) slog.Handler {
		rec = &Recorder{}
		return rec
	}, func(t *testing.T) map[string]any {
		t.Helper()
		records := rec.Records()
		if len(records) != 1 {
			t.Fatalf("captured %d records, want exactly 1 per battery case", len(records))
		}
		return recordToMap(&records[0])
	})
}

// recordToMap projects a captured record into slogtest's expected result
// shape: built-in keys at the top level (time omitted when zero, source never
// emitted, matching the Recorder's no-rendering stance), attrs by key, and
// each group as its own nested map.
func recordToMap(r *slog.Record) map[string]any {
	m := make(map[string]any)
	if !r.Time.IsZero() {
		m[slog.TimeKey] = r.Time
	}
	m[slog.LevelKey] = r.Level
	m[slog.MessageKey] = r.Message
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = attrValueToAny(a.Value)
		return true
	})
	return m
}

// attrValueToAny converts a captured value for slogtest comparison: groups
// become nested maps, everything else its native Go value.
func attrValueToAny(v slog.Value) any {
	if v.Kind() == slog.KindGroup {
		g := make(map[string]any)
		for _, a := range v.Group() {
			g[a.Key] = attrValueToAny(a.Value)
		}
		return g
	}
	return v.Any()
}

var errBoom = errors.New("boom")

// TestRenderFaithfulDifferential is the differential oracle for the
// package's render-faithfulness claim. Each scenario is driven twice: once
// into a real stdlib handler directly, and once into a Recorder whose stored
// records are then replayed one by one into an identically configured stdlib
// handler. If materialization is faithful, the replayed output is
// byte-identical to the direct output; any divergence between the Recorder's
// normalization and the stdlib's own rules shows up as a diff, with the
// stdlib handler as the authority.
func TestRenderFaithfulDifferential(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 7, 21, 12, 0, 0, 0, time.FixedZone("UTC+5", 5*3600))

	scenarios := []struct {
		name  string
		drive func(l *slog.Logger)
	}{
		{"plain message no attrs", func(l *slog.Logger) {
			l.Info("plain")
		}},
		{"assorted value kinds", func(l *slog.Logger) {
			l.Info("kinds",
				"s", "str", "i", 42, "u", uint64(7), "f", 3.5, "b", true,
				"d", 250*time.Millisecond, "t", fixed, "err", errBoom)
		}},
		{"debug and error levels", func(l *slog.Logger) {
			l.Debug("low level")
			l.Error("high level", "k", "v")
		}},
		{"zero attr dropped", func(l *slog.Logger) {
			l.LogAttrs(t.Context(), slog.LevelInfo, "zero",
				slog.String("keep", "v"), slog.Attr{})
		}},
		{"empty group dropped and empty-keyed group inlined", func(l *slog.Logger) {
			l.Info("groups",
				slog.Group("empty"),
				slog.Group("", slog.String("inlined", "v")),
				slog.Group("degenerate", slog.Attr{}))
		}},
		{"logvaluer resolved at top level and in groups", func(l *slog.Logger) {
			l.Info("resolve", "k", stringValuer{},
				slog.Group("g", slog.Any("nested", stringValuer{})))
		}},
		{"valuer resolving to empty group dropped", func(l *slog.Logger) {
			l.Info("resolve-empty", "keep", "v", slog.Any("dropped", emptyGroupValuer{}))
		}},
		{"WithAttrs prefix", func(l *slog.Logger) {
			l.With("app", "x").Info("with", "k", "v")
		}},
		{"WithGroup nesting", func(l *slog.Logger) {
			l.WithGroup("req").Info("grouped", "path", "/x")
		}},
		{"mixed derivation chain", func(l *slog.Logger) {
			l.With("a", 1).WithGroup("g1").With("b", 2).WithGroup("g2").Info("chain", "c", 3)
		}},
		{"derived groups with no attrs elided", func(l *slog.Logger) {
			l.With("a", "b").WithGroup("G").With("c", "d").WithGroup("H").WithGroup("I").Info("bare")
		}},
		{"empty WithGroup name and empty With are no-ops", func(l *slog.Logger) {
			l.WithGroup("").With().Info("noop", "k", "v")
		}},
		{"duplicate keys kept", func(l *slog.Logger) {
			l.Info("dup", "k", 1, "k", 2)
		}},
		{"deeply nested record groups", func(l *slog.Logger) {
			l.Info("deep", slog.Group("o", slog.Group("i", slog.String("k", "v")), slog.String("s", "t")))
		}},
		{"sibling derived handles interleave in order", func(l *slog.Logger) {
			a, b := l.WithGroup("ga"), l.With("side", "b")
			a.Info("first", "k", 1)
			b.Warn("second")
			a.Info("third", "k", 2)
		}},
	}

	handlers := []struct {
		name string
		mk   func(w io.Writer) slog.Handler
	}{
		{"text", func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, diffOpts()) }},
		{"json", func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, diffOpts()) }},
	}

	for _, h := range handlers {
		for _, sc := range scenarios {
			t.Run(h.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()

				var direct bytes.Buffer
				sc.drive(slog.New(h.mk(&direct)))

				logger, rec := New()
				sc.drive(logger)
				var replayed bytes.Buffer
				replay := h.mk(&replayed)
				records := rec.Records()
				for i := range records {
					if err := replay.Handle(t.Context(), records[i]); err != nil {
						t.Fatalf("replaying captured record %d: %v", i, err)
					}
				}

				if direct.String() != replayed.String() {
					t.Errorf("replayed captured records diverge from direct stdlib output\n--- direct:\n%s--- replayed:\n%s",
						direct.String(), replayed.String())
				}
			})
		}
	}
}

// diffOpts configures the two comparison handlers of a differential case:
// everything is level-enabled (the Recorder captures at all levels), and the
// built-in top-level time attr is dropped because the direct and replayed
// drives run at different walltimes. Time pass-through itself is covered by
// the slogtest zero-time case and the root package's UTC handler tests; user
// attrs named "time" inside groups are NOT dropped.
func diffOpts() *slog.HandlerOptions {
	return &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
}
