package capture

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewCapturesInjectedLogger(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("hello", "k", "v")
	logger.Warn("second")

	if rec.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", rec.Len())
	}
	if !rec.Contains("hello") {
		t.Error("Contains(hello) = false, want true")
	}
	if got := rec.Count("second"); got != 1 {
		t.Errorf("Count(second) = %d, want 1", got)
	}
	if msgs := rec.Messages(); len(msgs) != 2 || msgs[0] != "hello" || msgs[1] != "second" {
		t.Errorf("Messages() = %v, want [hello second]", msgs)
	}
}

func TestRecordsReturnsSnapshotCopy(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("one")
	snap := rec.Records()
	logger.Info("two")
	if len(snap) != 1 {
		t.Errorf("snapshot len = %d, want 1 (a later log must not grow an earlier snapshot)", len(snap))
	}
}

func TestRecordsSnapshotGroupsAreIsolated(t *testing.T) {
	t.Parallel()
	// A snapshot's group values must have fresh backing storage: mutating a
	// group slice fetched from one Records() call must not be visible in the
	// stored record or a later snapshot.
	logger, rec := New()
	logger.WithGroup("g").Info("m", "k", "before")

	first := rec.Records()
	grp := recordAttrs(t, first[0])[0].Value.Group()
	grp[0] = slog.String("k", "mutated")

	second := rec.Records()
	got := recordAttrs(t, second[0])[0].Value.Group()
	if len(got) != 1 || got[0].Key != "k" || got[0].Value.String() != "before" {
		t.Errorf("second snapshot group = %v, want [k=before] (snapshot mutation leaked into the recorder)", got)
	}
}

func TestRecordsSnapshotNestedGroupsAreIsolated(t *testing.T) {
	t.Parallel()
	// materialize must rebuild group values RECURSIVELY: mutating an inner
	// (nested) group slice fetched from one Records() call must not be visible
	// in a later snapshot. A shallow one-level copy would pass the flat-group
	// isolation test but alias the nested slice.
	logger, rec := New()
	logger.WithGroup("outer").WithGroup("inner").Info("m", "k", "before")

	first := rec.Records()
	outer := recordAttrs(t, first[0])[0].Value.Group()
	innerGrp := outer[0].Value.Group()
	innerGrp[0] = slog.String("k", "mutated")

	second := rec.Records()
	got := recordAttrs(t, second[0])[0].Value.Group()[0].Value.Group()
	if len(got) != 1 || got[0].Key != "k" || got[0].Value.String() != "before" {
		t.Errorf("second snapshot nested group = %v, want [k=before] (nested-group snapshot mutation leaked into the recorder)", got)
	}
}

func TestRecordsSnapshotPreservesRecordFields(t *testing.T) {
	t.Parallel()
	// materialize rebuilds each record via slog.NewRecord; the snapshot must
	// carry the original Time, Level, and PC through that rebuild.
	logger, rec := New()
	before := time.Now()
	logger.Error("boom", "k", "v")
	after := time.Now()

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("Len = %d, want 1", len(records))
	}
	r := records[0]
	if r.Level != slog.LevelError {
		t.Errorf("snapshot Level = %v, want Error", r.Level)
	}
	if r.Time.Before(before) || r.Time.After(after) {
		t.Errorf("snapshot Time = %v, want within [%v, %v]", r.Time, before, after)
	}
	if r.PC == 0 {
		t.Error("snapshot PC = 0, want the original call-site PC preserved")
	}
	if r.Message != "boom" {
		t.Errorf("snapshot Message = %q, want %q", r.Message, "boom")
	}
	if n := r.NumAttrs(); n != 1 {
		t.Errorf("snapshot NumAttrs = %d, want 1", n)
	}
}

func TestStoredRecordsDoNotAliasCallerGroupSlices(t *testing.T) {
	t.Parallel()
	// A real handler fixes rendered content at Handle time; mutating a group
	// slice the caller retained after logging must not change what was captured.
	logger, rec := New()
	kids := []slog.Attr{slog.String("k", "before")}
	logger.LogAttrs(t.Context(), slog.LevelInfo, "m",
		slog.Attr{Key: "g", Value: slog.GroupValue(kids...)})

	kids[0] = slog.String("k", "after")

	got := recordAttrs(t, rec.Records()[0])[0].Value.Group()
	if len(got) != 1 || got[0].Value.String() != "before" {
		t.Errorf("captured group = %v, want [k=before] (stored record aliased the caller's slice)", got)
	}
}

func TestDerivedRecordsDoNotAliasCallerGroupSlices(t *testing.T) {
	t.Parallel()

	t.Run("inherited attrs are fixed at derivation time", func(t *testing.T) {
		logger, rec := New()
		children := []slog.Attr{slog.String("k", "before")}
		derived := logger.With(slog.Attr{Key: "g", Value: slog.GroupValue(children...)})

		children[0] = slog.String("k", "after")
		derived.Info("m")

		got := recordAttrs(t, rec.Records()[0])[0].Value.Group()
		if len(got) != 1 || got[0].Value.String() != "before" {
			t.Errorf("captured inherited group = %v, want [k=before] (derived attrs aliased the caller's slice)", got)
		}
	})

	t.Run("call-site attrs are fixed at handle time", func(t *testing.T) {
		logger, rec := New()
		derived := logger.With("base", 1)
		children := []slog.Attr{slog.String("k", "before")}

		derived.LogAttrs(t.Context(), slog.LevelInfo, "m",
			slog.Attr{Key: "g", Value: slog.GroupValue(children...)})
		children[0] = slog.String("k", "after")

		got := recordAttrs(t, rec.Records()[0])[1].Value.Group()
		if len(got) != 1 || got[0].Value.String() != "before" {
			t.Errorf("captured call-site group = %v, want [k=before] (derived Handle aliased the caller's slice)", got)
		}
	})
}

func TestWithAttrsAndGroupStillCapture(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	// WithAttrs/WithGroup derive handles over the same record buffer; the
	// record lands on the root Recorder.
	logger.With("base", 1).WithGroup("grp").Info("nested")
	if !rec.Contains("nested") {
		t.Error("WithAttrs/WithGroup dropped the record")
	}
}

// recordAttrs flattens a record's top-level attrs for structural assertions.
func recordAttrs(t *testing.T, r slog.Record) []slog.Attr {
	t.Helper()
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return attrs
}

func TestWithAttrsCapturedAtTopLevel(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.With("base", 1).Info("m", "direct", "v")

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("Len = %d, want 1", len(records))
	}
	attrs := recordAttrs(t, records[0])
	if len(attrs) != 2 {
		t.Fatalf("attrs = %v, want [base=1 direct=v]", attrs)
	}
	if attrs[0].Key != "base" || attrs[0].Value.String() != "1" {
		t.Errorf("inherited attr = %v, want base=1 first (inherited attrs precede call-site attrs)", attrs[0])
	}
	if attrs[1].Key != "direct" || attrs[1].Value.String() != "v" {
		t.Errorf("call-site attr = %v, want direct=v", attrs[1])
	}
}

func TestWithGroupNestingMatchesRealHandler(t *testing.T) {
	t.Parallel()
	// logger.With(a).WithGroup(g).With(b) logging (m, c) must capture
	// a=1 at the top level and b=2, c=3 inside group g — the same shape a
	// stdlib handler renders as a=1 g.b=2 g.c=3.
	logger, rec := New()
	logger.With("a", 1).WithGroup("g").With("b", 2).Info("m", "c", 3)

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("Len = %d, want 1", len(records))
	}
	attrs := recordAttrs(t, records[0])
	if len(attrs) != 2 {
		t.Fatalf("top-level attrs = %v, want [a g]", attrs)
	}
	if attrs[0].Key != "a" || attrs[0].Value.String() != "1" {
		t.Errorf("attrs[0] = %v, want a=1", attrs[0])
	}
	if attrs[1].Key != "g" || attrs[1].Value.Kind() != slog.KindGroup {
		t.Fatalf("attrs[1] = %v, want group g", attrs[1])
	}
	grp := attrs[1].Value.Group()
	if len(grp) != 2 || grp[0].Key != "b" || grp[0].Value.String() != "2" || grp[1].Key != "c" || grp[1].Value.String() != "3" {
		t.Errorf("group g = %v, want [b=2 c=3]", grp)
	}
}

func TestNestedGroupsCaptured(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.WithGroup("g").WithGroup("h").Info("m", "k", 1)

	attrs := recordAttrs(t, rec.Records()[0])
	if len(attrs) != 1 || attrs[0].Key != "g" || attrs[0].Value.Kind() != slog.KindGroup {
		t.Fatalf("top-level attrs = %v, want a single group g", attrs)
	}
	inner := attrs[0].Value.Group()
	if len(inner) != 1 || inner[0].Key != "h" || inner[0].Value.Kind() != slog.KindGroup {
		t.Fatalf("g = %v, want a single nested group h", inner)
	}
	leaf := inner[0].Value.Group()
	if len(leaf) != 1 || leaf[0].Key != "k" || leaf[0].Value.String() != "1" {
		t.Errorf("g.h = %v, want [k=1]", leaf)
	}
}

func TestEmptyGroupElided(t *testing.T) {
	t.Parallel()
	// A group with no attrs inside it is elided, matching stdlib handler output.
	logger, rec := New()
	logger.WithGroup("g").Info("bare")

	r := rec.Records()[0]
	if n := r.NumAttrs(); n != 0 {
		t.Errorf("NumAttrs = %d, want 0 (empty group must be elided)", n)
	}
}

func TestDerivedHandleContractEdges(t *testing.T) {
	t.Parallel()
	_, rec := New()
	// Contract: an empty name / empty attrs slice returns the receiver.
	if got := rec.WithGroup(""); got != slog.Handler(rec) {
		t.Error("Recorder.WithGroup(\"\") did not return the receiver")
	}
	if got := rec.WithAttrs(nil); got != slog.Handler(rec) {
		t.Error("Recorder.WithAttrs(nil) did not return the receiver")
	}
	d := rec.WithAttrs([]slog.Attr{slog.Int("a", 1)})
	if got := d.WithGroup(""); got != d {
		t.Error("derived.WithGroup(\"\") did not return the receiver")
	}
	if got := d.WithAttrs(nil); got != d {
		t.Error("derived.WithAttrs(nil) did not return the receiver")
	}
}

func TestDerivedSiblingsDoNotAlias(t *testing.T) {
	t.Parallel()
	// Two handles derived from the same parent must not leak steps into each
	// other's prefix (a fresh-backing-array regression guard).
	logger, rec := New()
	parent := logger.With("p", 0)
	parent.With("a", 1).Info("via-a")
	parent.With("b", 2).Info("via-b")

	records := rec.Records()
	if len(records) != 2 {
		t.Fatalf("Len = %d, want 2", len(records))
	}
	attrsA := recordAttrs(t, records[0])
	if len(attrsA) != 2 || attrsA[0].Key != "p" || attrsA[1].Key != "a" {
		t.Errorf("first record attrs = %v, want [p a]", attrsA)
	}
	attrsB := recordAttrs(t, records[1])
	if len(attrsB) != 2 || attrsB[0].Key != "p" || attrsB[1].Key != "b" {
		t.Errorf("second record attrs = %v, want [p b] (sibling must not inherit a)", attrsB)
	}
}

func TestConcurrentDerivedHandlesAreRaceFree(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	var wg sync.WaitGroup
	for i := range 25 {
		wg.Go(func() { logger.With("worker", i).Info("derived-concurrent") })
		wg.Go(func() { logger.Info("root-concurrent") })
	}
	wg.Wait()
	if got := rec.Count("derived-concurrent"); got != 25 {
		t.Errorf("Count(derived-concurrent) = %d, want 25", got)
	}
	if got := rec.Count("root-concurrent"); got != 25 {
		t.Errorf("Count(root-concurrent) = %d, want 25", got)
	}
}

func TestDefaultCapturesGlobalAndRestores(t *testing.T) {
	// Not parallel: mutates the global slog default.
	before := slog.Default()
	t.Run("captures", func(t *testing.T) {
		rec := Default(t)
		slog.Info("through-default")
		if !rec.Contains("through-default") {
			t.Error("Default did not capture a slog.Default() log")
		}
	})
	if slog.Default() != before {
		t.Error("Default did not restore slog.Default() after the subtest ended")
	}
}

func TestDefaultRestoresTheLogPackage(t *testing.T) {
	// Not parallel: mutates the global slog default and the log package.
	prevWriter, prevFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	// Stand in for a caller that owns the log package's state, so the assertions
	// below read what Default's cleanup left behind rather than the ambient
	// process defaults.
	var sink bytes.Buffer
	log.SetOutput(&sink)
	log.SetFlags(log.Ldate | log.Ltime)

	t.Run("captures", func(t *testing.T) {
		rec := Default(t)
		slog.Info("through-default")
		if !rec.Contains("through-default") {
			t.Fatal("Default did not capture a slog.Default() log")
		}
	})

	if got, want := log.Flags(), log.Ldate|log.Ltime; got != want {
		t.Errorf("log.Flags() = %d, want %d (slog.SetDefault zeroes them, so the cleanup must put them back)", got, want)
	}

	// slog's own default handler writes through the log package, so a log
	// writer left pointing at the recorder takes every later slog call in the
	// process down with it.
	sink.Reset()
	slog.Warn("after-cleanup-slog")
	log.Print("after-cleanup-log")
	if got := sink.String(); !strings.Contains(got, "after-cleanup-slog") || !strings.Contains(got, "after-cleanup-log") {
		t.Errorf("caller's log sink = %q, want both after-cleanup lines (the cleanup must point log's output back)", got)
	}
}

func TestDefaultRestoreOrderSurvivesANonDefaultPrevious(t *testing.T) {
	// Not parallel: mutates the global slog default and the log package.
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	// A caller whose slog default is a real handler rather than slog's own: now
	// reinstalling it re-runs the log redirect and re-zeroes the flags, so the
	// cleanup only lands if it puts slog back BEFORE the log package.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	log.SetFlags(log.Ldate)

	t.Run("captures", func(t *testing.T) {
		rec := Default(t)
		slog.Info("through-default")
		if !rec.Contains("through-default") {
			t.Fatal("Default did not capture a slog.Default() log")
		}
	})

	if got, want := log.Flags(), log.Ldate; got != want {
		t.Errorf("log.Flags() = %d, want %d (reinstalling a non-default previous logger re-zeroes them, so the log restore must come second)", got, want)
	}
}

func TestSwapDefaultRestoresPrevious(t *testing.T) {
	// Not parallel: mutates the global slog default and the log package.
	prevWriter, prevFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	// Pin a log baseline slog.SetDefault never writes, so the flags assertion
	// fails without the restore even when an earlier test left them at 0.
	var sink bytes.Buffer
	log.SetOutput(&sink)
	log.SetFlags(log.Ldate | log.Ltime)

	before, beforeWriter, beforeFlags := slog.Default(), log.Writer(), log.Flags()
	outer, _ := New()
	inner, _ := New()

	t.Run("outer", func(t *testing.T) {
		SwapDefault(t, outer)
		if slog.Default() != outer {
			t.Fatal("SwapDefault did not make the given logger the default")
		}
		t.Run("inner", func(t *testing.T) {
			SwapDefault(t, inner)
			if slog.Default() != inner {
				t.Error("nested SwapDefault did not make the given logger the default")
			}
		})
		if slog.Default() != outer {
			t.Error("after the nested swap's cleanup, the default is not the outer logger")
		}
	})

	if slog.Default() != before {
		t.Error("after both cleanups, the default is not the logger from before the swaps")
	}
	if log.Writer() != beforeWriter {
		t.Error("after both cleanups, log.Writer() is not the writer from before the swaps")
	}
	if log.Flags() != beforeFlags {
		t.Errorf("after both cleanups, log.Flags() = %d, want %d", log.Flags(), beforeFlags)
	}
}

func TestConcurrentHandleIsRaceFree(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { logger.Info("concurrent") })
	}
	wg.Wait()
	if got := rec.Count("concurrent"); got != 50 {
		t.Errorf("Count(concurrent) = %d, want 50", got)
	}
}

func TestRecorderNegativeLookups(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("present")

	if rec.Contains("absent") {
		t.Error("Contains(absent) = true, want false for a substring no record holds")
	}
	if got := rec.Count("absent"); got != 0 {
		t.Errorf("Count(absent) = %d, want 0", got)
	}
}

func TestCaptureRecordsBelowDefaultLevel(t *testing.T) {
	t.Parallel()
	// Recorder.Enabled reports true for every level, so a Debug record (below
	// slog's default Info threshold) is still captured.
	logger, rec := New()
	logger.Debug("debug-detail")
	if !rec.Contains("debug-detail") {
		t.Error("Debug record not captured; Recorder must record at any level")
	}
}

func TestCountExact(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("cycle complete")
	logger.Warn("cycle complete")
	logger.Error("cycle completed with errors")
	logger.Info("prefix cycle complete")

	// The exact-match count sees only the two exact messages; the substring
	// Count sees all four — the false-pass window CountExact exists to close.
	if got := rec.CountExact("cycle complete"); got != 2 {
		t.Errorf("CountExact(cycle complete) = %d, want 2", got)
	}
	if got := rec.Count("cycle complete"); got != 4 {
		t.Errorf("Count(cycle complete) = %d, want 4 (substring semantics)", got)
	}
	if got := rec.CountExact("cycle"); got != 0 {
		t.Errorf("CountExact(cycle) = %d, want 0 for a partial message", got)
	}
	if got := rec.CountExact("absent"); got != 0 {
		t.Errorf("CountExact(absent) = %d, want 0", got)
	}
	if got := rec.CountExact(""); got != 0 {
		t.Errorf("CountExact(\"\") = %d, want 0 when no record has an empty message", got)
	}
}

func TestDerivedHandlePreservesRecordFields(t *testing.T) {
	t.Parallel()
	// derived.Handle rebuilds the record via slog.NewRecord to materialize the
	// derivation prefix; the rebuilt record must carry the original Time,
	// Level, PC, and Message, exactly as the root path does. Debug also pins
	// derived.Enabled's record-everything contract below the default level.
	logger, rec := New()
	before := time.Now()
	logger.With("base", 1).Debug("boom", "k", "v")
	after := time.Now()

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("Len = %d, want 1 (derived handles must capture below the default Info level)", len(records))
	}
	r := records[0]
	if r.Level != slog.LevelDebug {
		t.Errorf("derived record Level = %v, want Debug", r.Level)
	}
	if r.Time.Before(before) || r.Time.After(after) {
		t.Errorf("derived record Time = %v, want within [%v, %v]", r.Time, before, after)
	}
	if r.PC == 0 {
		t.Error("derived record PC = 0, want the original call-site PC preserved")
	}
	if r.Message != "boom" {
		t.Errorf("derived record Message = %q, want %q", r.Message, "boom")
	}
	if n := r.NumAttrs(); n != 2 {
		t.Errorf("derived record NumAttrs = %d, want 2 (base + call-site)", n)
	}
}

func TestEmptyGroupRecordAttrElidedWithEnclosingGroup(t *testing.T) {
	t.Parallel()
	// stdlib parity: an empty group renders nothing, so a derivation group
	// whose only content was that attr is elided too — a stdlib handler emits
	// just msg=m. The second case only becomes empty after inner normalization
	// (its sole child is a zero Attr); it used to defeat the enclosing-group
	// elision.
	for _, tc := range []struct {
		name string
		attr slog.Attr
	}{
		{name: "empty group", attr: slog.Group("empty")},
		{name: "group holding only a zero attr", attr: slog.Group("empty", slog.Attr{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger, rec := New()
			logger.WithGroup("g").LogAttrs(t.Context(), slog.LevelInfo, "m", tc.attr)

			records := rec.Records()
			if len(records) != 1 {
				t.Fatalf("Len = %d, want 1", len(records))
			}
			if n := records[0].NumAttrs(); n != 0 {
				t.Errorf("NumAttrs = %d, want 0 (a degenerate record attr must not defeat enclosing-group elision)", n)
			}
		})
	}
}

func TestZeroAttrDropped(t *testing.T) {
	t.Parallel()
	// "If an Attr's key and value are both the zero value, ignore the Attr" —
	// a stdlib handler renders nothing for slog.Attr{}; capture must match.
	logger, rec := New()
	logger.LogAttrs(t.Context(), slog.LevelInfo, "m", slog.Attr{}, slog.String("k", "v"))

	attrs := recordAttrs(t, rec.Records()[0])
	if len(attrs) != 1 || attrs[0].Key != "k" || attrs[0].Value.String() != "v" {
		t.Errorf("attrs = %v, want [k=v] (the zero Attr must be dropped)", attrs)
	}
}

func TestWithEmptyGroupDerivationElided(t *testing.T) {
	t.Parallel()
	// Logger.With hands attrs to WithAttrs unfiltered; stdlib handlers
	// preformat them there, and an empty group adds nothing. The whole
	// derivation op is elided: attrs that normalize to empty return the
	// receiver, exactly like the empty-slice guard.
	logger, rec := New()
	logger.With(slog.Group("empty")).Info("m", "k", "v")

	attrs := recordAttrs(t, rec.Records()[0])
	if len(attrs) != 1 || attrs[0].Key != "k" {
		t.Errorf("attrs = %v, want [k=v] (an empty-group With op must be elided)", attrs)
	}

	degenerate := []slog.Attr{slog.Group("empty"), {}}
	if got := rec.WithAttrs(degenerate); got != slog.Handler(rec) {
		t.Error("Recorder.WithAttrs(all-degenerate attrs) did not return the receiver")
	}
	d := rec.WithAttrs([]slog.Attr{slog.Int("a", 1)})
	if got := d.WithAttrs(degenerate); got != d {
		t.Error("derived.WithAttrs(all-degenerate attrs) did not return the receiver")
	}
}

func TestEmptyKeyedGroupInlined(t *testing.T) {
	t.Parallel()
	// "If a group's key is empty, inline the group's Attrs" — stdlib handlers
	// render slog.Group("", a, b) exactly as if a and b were passed directly,
	// at the top level and nested inside a named group alike.
	logger, rec := New()
	logger.Info("top", slog.Group("", slog.String("a", "1"), slog.Int("b", 2)))
	logger.Info("nested", slog.Group("outer", slog.Group("", slog.String("x", "1"))))

	records := rec.Records()
	top := recordAttrs(t, records[0])
	if len(top) != 2 || top[0].Key != "a" || top[0].Value.String() != "1" ||
		top[1].Key != "b" || top[1].Value.String() != "2" {
		t.Errorf("top-level attrs = %v, want inlined [a=1 b=2]", top)
	}
	nested := recordAttrs(t, records[1])
	if len(nested) != 1 || nested[0].Key != "outer" || nested[0].Value.Kind() != slog.KindGroup {
		t.Fatalf("nested attrs = %v, want a single group outer", nested)
	}
	grp := nested[0].Value.Group()
	if len(grp) != 1 || grp[0].Key != "x" || grp[0].Value.String() != "1" {
		t.Errorf("group outer = %v, want inlined [x=1]", grp)
	}
}

// stringValuer is a slog.LogValuer resolving to a fixed string, for asserting
// that captured values are stored resolved rather than as the raw valuer.
type stringValuer struct{}

func (stringValuer) LogValue() slog.Value { return slog.StringValue("resolved") }

// emptyGroupValuer is a slog.LogValuer resolving to an empty group, for
// asserting that resolution happens before the group output rules apply.
type emptyGroupValuer struct{}

func (emptyGroupValuer) LogValue() slog.Value { return slog.GroupValue() }

var (
	_ slog.LogValuer = stringValuer{}
	_ slog.LogValuer = emptyGroupValuer{}
)

func TestLogValuerStoredResolved(t *testing.T) {
	t.Parallel()
	// "Attr's values should be resolved" — a stdlib handler renders the
	// LogValue result; the captured record must hold it, not the raw valuer.

	t.Run("record attr", func(t *testing.T) {
		t.Parallel()
		logger, rec := New()
		logger.Info("m", "k", stringValuer{})

		attrs := recordAttrs(t, rec.Records()[0])
		if len(attrs) != 1 || attrs[0].Value.Kind() != slog.KindString {
			t.Fatalf("attrs = %v, want one resolved string attr", attrs)
		}
		if got := attrs[0].Value.String(); got != "resolved" {
			t.Errorf("captured value = %q, want %q (value must be stored resolved)", got, "resolved")
		}
	})

	t.Run("derivation attr", func(t *testing.T) {
		t.Parallel()
		logger, rec := New()
		logger.With("k", stringValuer{}).Info("m")

		attrs := recordAttrs(t, rec.Records()[0])
		if len(attrs) != 1 || attrs[0].Value.Kind() != slog.KindString || attrs[0].Value.String() != "resolved" {
			t.Errorf("attrs = %v, want [k=resolved] (WithAttrs must resolve at derivation time)", attrs)
		}
	})

	t.Run("valuer resolving to an empty group is dropped", func(t *testing.T) {
		t.Parallel()
		// Resolution happens before the group rules: a valuer whose LogValue
		// is an empty group reaches the handler unelided (it is not a group
		// until resolved) and must then drop like any other empty group.
		logger, rec := New()
		logger.Info("m", "g", emptyGroupValuer{})

		if n := rec.Records()[0].NumAttrs(); n != 0 {
			t.Errorf("NumAttrs = %d, want 0 (a valuer resolving to an empty group must drop)", n)
		}
	})
}

func TestGroupOfOnlyDegenerateAttrsDropsEntirely(t *testing.T) {
	t.Parallel()
	// Emptiness is judged after inner normalization: a group whose children
	// all normalize away — a zero Attr, an empty inner group — is itself empty
	// and drops entirely, as a stdlib handler backs out a group it never wrote
	// anything into.
	logger, rec := New()
	logger.Info("m", slog.Group("outer", slog.Attr{}, slog.Group("inner")))

	if n := rec.Records()[0].NumAttrs(); n != 0 {
		t.Errorf("NumAttrs = %d, want 0 (a group of only degenerate attrs must drop entirely)", n)
	}
}

func TestGroupNormalizationRecursesThroughValuers(t *testing.T) {
	t.Parallel()
	// Resolution recurses into group children: a LogValuer nested inside a
	// group is stored resolved, and a child valuer resolving to an empty
	// group empties its enclosing group, which then drops entirely -- the
	// resolve rule feeding the group-emptiness rule, as a stdlib handler
	// renders it.
	logger, rec := New()
	logger.Info("m", slog.Group("outer", slog.Any("k", stringValuer{})))
	logger.Info("n", slog.Group("outer", slog.Any("g", emptyGroupValuer{})))

	records := rec.Records()
	got := recordAttrs(t, records[0])
	if len(got) != 1 || got[0].Key != "outer" || got[0].Value.Kind() != slog.KindGroup {
		t.Fatalf("attrs = %v, want a single group outer", got)
	}
	grp := got[0].Value.Group()
	if len(grp) != 1 || grp[0].Key != "k" || grp[0].Value.Kind() != slog.KindString || grp[0].Value.String() != "resolved" {
		t.Errorf("group outer = %v, want [k=resolved] (a valuer nested in a group must be stored resolved)", grp)
	}
	if n := records[1].NumAttrs(); n != 0 {
		t.Errorf("NumAttrs = %d, want 0 (a group whose only child resolves to an empty group must drop entirely)", n)
	}
}

func TestEmptyKeyedGroupInlinedAtDerivation(t *testing.T) {
	t.Parallel()
	// WithAttrs normalizes at derivation time: an empty-keyed group handed to
	// Logger.With is inlined into the derivation prefix, so its attrs land at
	// the top level ahead of call-site attrs -- the same shape a stdlib
	// handler renders for With(slog.Group("", ...)).
	logger, rec := New()
	logger.With(slog.Group("", slog.String("a", "1"))).Info("m", "b", 2)

	attrs := recordAttrs(t, rec.Records()[0])
	if len(attrs) != 2 || attrs[0].Key != "a" || attrs[0].Value.String() != "1" ||
		attrs[1].Key != "b" || attrs[1].Value.String() != "2" {
		t.Errorf("attrs = %v, want inlined [a=1 b=2]", attrs)
	}
}

func TestEmptyGroupElisionPreservesEarlierAttrs(t *testing.T) {
	t.Parallel()
	// An elided empty group must not swallow attrs inherited BEFORE it:
	// logger.With(a).WithGroup(g) logging a bare message renders a=1 and no
	// group on a stdlib handler, so the capture must too. Guards the backward
	// materialization loop in derived.Handle: eliding g skips only that op,
	// never the remaining derivation steps.
	logger, rec := New()
	logger.With("a", 1).WithGroup("g").Info("bare")

	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("Len = %d, want 1", len(records))
	}
	attrs := recordAttrs(t, records[0])
	if len(attrs) != 1 || attrs[0].Key != "a" || attrs[0].Value.String() != "1" {
		t.Errorf("attrs = %v, want [a=1] (an elided empty group must not swallow earlier inherited attrs)", attrs)
	}
}

// TestAttrHelpers covers the attribute-level assertion surface: rendered
// (kind-agnostic) value matching, the "" wildcards on both scoping
// parameters, message-substring scoping, first-match ordering for AttrValue,
// and that WithAttrs-derived attributes are visible (the helpers scan the
// materialized records, not just per-call attrs).
func TestAttrHelpers(t *testing.T) {
	log, rec := New()
	log.Info("mapping refreshed", "records", 42, "path", "/config/overrides.json")
	log.Warn("mapping degraded", "stale_reason", "refresh failed", "records", 7)
	log.With("component", "indexer").Error("upstream failed", "records", 42)

	if v, ok := rec.AttrValue("mapping refreshed", "records"); !ok || v != "42" {
		t.Errorf(`AttrValue("mapping refreshed", "records") = %q, %v; want "42", true (Int64 renders "42")`, v, ok)
	}
	if v, ok := rec.AttrValue("", "records"); !ok || v != "42" {
		t.Errorf(`AttrValue("", "records") = %q, %v; want the FIRST record's "42", true`, v, ok)
	}
	if _, ok := rec.AttrValue("mapping refreshed", "absent"); ok {
		t.Error(`AttrValue(..., "absent") found a value, want ok=false`)
	}
	if _, ok := rec.AttrValue("no such message", "records"); ok {
		t.Error("AttrValue with an unmatched message scope found a value, want ok=false")
	}

	if !rec.HasAttr("mapping degraded", "stale_reason", "refresh failed") {
		t.Error("HasAttr missed a message-scoped exact rendered value")
	}
	if rec.HasAttr("mapping degraded", "stale_reason", "refresh") {
		t.Error("HasAttr matched a substring, want exact rendered equality")
	}
	if !rec.HasAttr("", "records", "7") {
		t.Error(`HasAttr("", "records", "7") missed the unscoped scan`)
	}

	if !rec.AttrContains("", "stale_reason", "refresh") {
		t.Error("AttrContains missed a keyed substring")
	}
	if !rec.AttrContains("", "", "overrides.json") {
		t.Error(`AttrContains with key "" (any attribute) missed the value substring`)
	}
	if rec.AttrContains("", "", "no-such-substring") {
		t.Error("AttrContains matched a substring no attribute carries")
	}

	// WithAttrs-derived attributes are folded into the stored records.
	if !rec.HasAttr("upstream failed", "component", "indexer") {
		t.Error("HasAttr missed a WithAttrs-derived attribute")
	}
}

// TestAttrReturnsTypedValues covers the contract Attr exists for and the
// rendered helpers cannot express: that a value was logged as a particular
// slog.Kind. The two attrs below render IDENTICALLY, so AttrValue cannot tell
// them apart; the test asserts that fact first, so a future change that made
// them render differently would surface here rather than quietly removing
// Attr's reason to exist.
func TestAttrReturnsTypedValues(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 7, 29, 14, 7, 0, 0, time.UTC)
	log, rec := New()
	log.Info("typed", "at", ts)
	log.Info("stringly", "at", ts.String())

	typed, ok := rec.Attr("typed", "at")
	if !ok {
		t.Fatal(`Attr("typed", "at") found nothing, want the Time value`)
	}
	stringly, ok := rec.Attr("stringly", "at")
	if !ok {
		t.Fatal(`Attr("stringly", "at") found nothing, want the String value`)
	}

	if typed.String() != stringly.String() {
		t.Fatalf("the two attrs render differently (%q vs %q); Attr's premise is that a rendered comparison cannot separate them", typed.String(), stringly.String())
	}
	if typed.Kind() != slog.KindTime {
		t.Errorf("Attr(...).Kind() = %v, want %v for slog.Time", typed.Kind(), slog.KindTime)
	}
	if stringly.Kind() != slog.KindString {
		t.Errorf("Attr(...).Kind() = %v, want %v for a pre-rendered string", stringly.Kind(), slog.KindString)
	}
	if !typed.Time().Equal(ts) {
		t.Errorf("Attr(...).Time() = %v, want %v", typed.Time(), ts)
	}

	// Scoping and the not-found result come from the shared walk; pin that Attr
	// is wired into it rather than re-testing the walk itself.
	if _, ok := rec.Attr("typed", "absent"); ok {
		t.Error(`Attr(..., "absent") found a value, want ok=false`)
	}
	if v, ok := rec.Attr("no such message", "at"); ok || !v.Equal(slog.Value{}) {
		t.Errorf("Attr with an unmatched message scope = %v, %v; want the zero Value, false", v, ok)
	}
	if v, ok := rec.Attr("", "at"); !ok || v.Kind() != slog.KindTime {
		t.Errorf(`Attr("", "at") = %v (%v), %v; want the FIRST record's Time value`, v, v.Kind(), ok)
	}
}

// TestAttrGroupValuesAreIsolated is the Records()-snapshot isolation contract
// for the one value Attr lets escape the lock. A KindGroup value carries a
// []Attr that aliases the stored record, so returning it raw would let a
// caller mutate the recorder's own buffer.
func TestAttrGroupValuesAreIsolated(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("m", slog.Group("g", slog.String("k", "before")))

	got, ok := rec.Attr("m", "g")
	if !ok {
		t.Fatal(`Attr("m", "g") found nothing, want the group value`)
	}
	got.Group()[0] = slog.String("k", "mutated")

	again, _ := rec.Attr("m", "g")
	if grp := again.Group(); len(grp) != 1 || grp[0].Value.String() != "before" {
		t.Errorf("second Attr group = %v, want [k=before] (a mutation through the returned value leaked into the recorder)", grp)
	}
}

// TestAttrNestedGroupValuesAreIsolated pins that the detach is RECURSIVE. A
// shallow one-level copy passes the flat-group test above while still aliasing
// the inner slice.
func TestAttrNestedGroupValuesAreIsolated(t *testing.T) {
	t.Parallel()
	logger, rec := New()
	logger.Info("m", slog.Group("outer", slog.Group("inner", slog.String("k", "before"))))

	got, ok := rec.Attr("m", "outer")
	if !ok {
		t.Fatal(`Attr("m", "outer") found nothing, want the group value`)
	}
	got.Group()[0].Value.Group()[0] = slog.String("k", "mutated")

	again, _ := rec.Attr("m", "outer")
	if grp := again.Group()[0].Value.Group(); len(grp) != 1 || grp[0].Value.String() != "before" {
		t.Errorf("second Attr nested group = %v, want [k=before] (a nested mutation leaked into the recorder)", grp)
	}
}

// TestAttrValueRendersAttr pins that the rendered helper is derived from the
// typed one, so the two can never disagree about what exists or what it says.
func TestAttrValueRendersAttr(t *testing.T) {
	t.Parallel()
	log, rec := New()
	log.Info("m", "count", 42, "when", time.Date(2026, 7, 29, 14, 7, 0, 0, time.UTC))
	log.Info("m", slog.Group("g", slog.String("k", "v")))

	for _, key := range []string{"count", "when", "g", "absent"} {
		typed, tok := rec.Attr("m", key)
		rendered, rok := rec.AttrValue("m", key)
		if tok != rok {
			t.Errorf("Attr(%q) ok=%v but AttrValue(%q) ok=%v; the two must agree on existence", key, tok, key, rok)
			continue
		}
		if want := typed.String(); tok && rendered != want {
			t.Errorf("AttrValue(%q) = %q, want %q (Attr's rendered form)", key, rendered, want)
		}
		if !tok && rendered != "" {
			t.Errorf("AttrValue(%q) = %q on a miss, want the empty string", key, rendered)
		}
	}
}

// exactMessageRecorder seeds the fixture the exact-message attr accessors are
// tested against. The SUPERSTRING record is captured FIRST on purpose: it is
// what the substring helpers answer from, so the exact accessors skipping it
// is a real discrimination rather than an artifact of capture order. The last
// record arrives through a derived handle, so the fixture also spans the
// WithAttrs fold.
func exactMessageRecorder() *Recorder {
	log, rec := New()
	log.Info("cycle completed with errors", "files", 99)
	log.Info("cycle complete", "files", 3)
	log.Warn("cycle complete", "files", 7)
	log.Info("cycle complete", "path", "/tmp/a")
	log.With("component", "indexer").Info("cycle complete", "files", 11)
	return rec
}

// TestAttrValueExact covers the exact-message single-value getter: that it
// discriminates against a substring superset (the false-pass window it exists
// to close), that it takes the first match in capture order, and its misses —
// a partial message, a missing attribute, a missing message, and the empty
// message that is NOT a wildcard here.
func TestAttrValueExact(t *testing.T) {
	t.Parallel()
	rec := exactMessageRecorder()

	// The premise: the substring getter answers from the superstring record,
	// captured first, and reports success while doing it.
	if v, ok := rec.AttrValue("cycle complete", "files"); !ok || v != "99" {
		t.Fatalf(`AttrValue("cycle complete", "files") = %q, %v; want "99", true (the superstring record is what substring scoping finds first — AttrValueExact's whole reason to exist)`, v, ok)
	}

	for _, tc := range []struct {
		name   string
		msg    string
		key    string
		want   string
		wantOK bool
	}{
		{name: "exact_match_beats_substring_superset", msg: "cycle complete", key: "files", want: "3", wantOK: true},
		{name: "superstring_message_is_reachable_in_its_own_right", msg: "cycle completed with errors", key: "files", want: "99", wantOK: true},
		{name: "key_wildcard_takes_the_first_attribute", msg: "cycle complete", key: "", want: "3", wantOK: true},
		{name: "partial_message_matches_nothing", msg: "cycle", key: "files"},
		{name: "missing_attribute", msg: "cycle complete", key: "absent"},
		{name: "missing_message", msg: "no such message", key: "files"},
		{name: "empty_message_is_not_a_wildcard", msg: "", key: "files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := rec.AttrValueExact(tc.msg, tc.key)
			if ok != tc.wantOK {
				t.Errorf("AttrValueExact(%q, %q) ok = %v, want %v", tc.msg, tc.key, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("AttrValueExact(%q, %q) = %q, want %q", tc.msg, tc.key, got, tc.want)
			}
		})
	}
}

// TestAttrValuesExact covers the exact-message collector: every matching
// record's value in capture order (including across a derived handle), the
// key wildcard collecting a whole record's top-level values, and the misses,
// which return nil rather than an empty slice.
func TestAttrValuesExact(t *testing.T) {
	t.Parallel()
	rec := exactMessageRecorder()

	for _, tc := range []struct {
		name string
		msg  string
		key  string
		want []string
	}{
		{
			name: "collects_every_matching_record_in_order",
			msg:  "cycle complete", key: "files",
			// 99 belongs to the superstring message and 7 sits on a WARN record:
			// the collector is level-blind and message-exact, and 11 arrives
			// through a derived handle, so the fold is included.
			want: []string{"3", "7", "11"},
		},
		{
			name: "key_wildcard_collects_every_top_level_value",
			msg:  "cycle complete", key: "",
			// Derived attrs render before the call site's own, as a handler
			// would emit them.
			want: []string{"3", "7", "/tmp/a", "indexer", "11"},
		},
		{name: "single_match_still_collects", msg: "cycle completed with errors", key: "files", want: []string{"99"}},
		{name: "missing_attribute_collects_nothing", msg: "cycle complete", key: "absent"},
		{name: "missing_message_collects_nothing", msg: "no such message", key: "files"},
		{name: "partial_message_collects_nothing", msg: "cycle", key: "files"},
		{name: "empty_message_is_not_a_wildcard", msg: "", key: "files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := rec.AttrValuesExact(tc.msg, tc.key)
			if !slices.Equal(got, tc.want) {
				t.Errorf("AttrValuesExact(%q, %q) = %q, want %q", tc.msg, tc.key, got, tc.want)
			}
			if tc.want == nil && got != nil {
				t.Errorf("AttrValuesExact(%q, %q) = %#v on a miss, want nil", tc.msg, tc.key, got)
			}
		})
	}
}

// TestCountLevel covers the level-scoped counter: exact level discrimination
// (the escalation-contract use: one ERROR, zero WARN of the same message),
// substring matching (the same vocabulary as Count and the attr helpers),
// and the "" wildcard counting every record at the level.
func TestCountLevel(t *testing.T) {
	log, rec := New()
	log.Warn("library walk shrank; keeping previous snapshot")
	log.Warn("library walk shrank; keeping previous snapshot")
	log.Error("library walk shrank repeatedly; inspect the arr")
	log.Warn("unrelated warning")

	if got := rec.CountLevel(slog.LevelWarn, "library walk shrank"); got != 2 {
		t.Errorf("CountLevel(Warn, sub) = %d, want 2", got)
	}
	if got := rec.CountLevel(slog.LevelError, "library walk shrank"); got != 1 {
		t.Errorf("CountLevel(Error, sub) = %d, want 1", got)
	}
	if got := rec.CountLevel(slog.LevelWarn, "keeping previous"); got != 2 {
		t.Errorf("CountLevel(Warn, mid-message sub) = %d, want 2 (substring, not prefix)", got)
	}
	if got := rec.CountLevel(slog.LevelWarn, ""); got != 3 {
		t.Errorf(`CountLevel(Warn, "") = %d, want every WARN record (3)`, got)
	}
	if got := rec.CountLevel(slog.LevelInfo, "library walk shrank"); got != 0 {
		t.Errorf("CountLevel(Info, sub) = %d, want 0 (exact level discrimination)", got)
	}
}

// TestRecorderZeroValueReady pins the zero-value contract stated on the type:
// &Recorder{} is a working handler without going through New or Default (a
// consumer embeds it directly and hands it to slog.New), so the zero value is
// part of the compatibility surface, not an accident of implementation.
func TestRecorderZeroValueReady(t *testing.T) {
	t.Parallel()
	rec := &Recorder{}
	slog.New(rec).With("app", "x").WithGroup("req").Info("hello", "k", "v")

	if rec.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", rec.Len())
	}
	if v, ok := rec.AttrValue("hello", "app"); !ok || v != "x" {
		t.Errorf(`AttrValue("hello", "app") = %q, %v; want "x", true`, v, ok)
	}
	if !rec.HasAttr("hello", "req", "[k=v]") {
		t.Error(`HasAttr("hello", "req", "[k=v]") = false; want the WithGroup-nested attrs rendered under the group key`)
	}
}
