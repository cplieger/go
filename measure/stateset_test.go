package measure

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// familyLines returns the lines of page that belong to the family name: its
// HELP and TYPE lines and its samples.
func familyLines(page, name string) string {
	var out []string
	for line := range strings.SplitSeq(page, "\n") {
		series, _, _ := strings.Cut(line, " ")
		series, _, _ = strings.Cut(series, "{")
		if series == name || strings.HasPrefix(line, "# HELP "+name+" ") || strings.HasPrefix(line, "# TYPE "+name+" ") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// A state set and an info series render as the gauge a v1 program wrote by
// hand: the same lines, in the same order.
func TestStateSetRendersAsTheHandWrittenGauge(t *testing.T) {
	states := []string{"live", "dry_run", "check"}
	set := NewStateSet("provider_health", "1 for the current health.", []string{"provider", "health"}, states,
		WithSeed([]string{"b"}))
	gauge := NewLabeledGauge("provider_health", "1 for the current health.", UnitNone, []string{"provider", "health"})
	withSet, withGauge := NewRegistry("app"), NewRegistry("app")
	withSet.MustRegister(set)
	withGauge.MustRegister(gauge)

	set.Set("dry_run", "a")
	for _, st := range states {
		gauge.Set(0, "a", st)
		gauge.Set(0, "b", st)
	}
	gauge.Set(1, "a", "dry_run")

	got, want := familyLines(body(t, withSet), "app_provider_health"), familyLines(body(t, withGauge), "app_provider_health")
	if got != want || want == "" {
		t.Errorf("state set page:\n%s\nwant the hand-written gauge's:\n%s", got, want)
	}
}

func TestStateSetSetMakesOneStateCurrent(t *testing.T) {
	r := NewRegistry("")
	mode := NewStateSet("config_mode", "h", []string{"mode"}, []string{"live", "dry_run"})
	r.MustRegister(mode)

	read := func(t *testing.T) map[string]float64 {
		t.Helper()
		fams, err := r.GatherOf(t.Context(), mode)
		if err != nil {
			t.Fatalf("GatherOf: %v", err)
		}
		if fams[0].Type != TypeGauge {
			t.Errorf("Type = %q, want gauge", fams[0].Type)
		}
		out := map[string]float64{}
		for _, s := range fams[0].Samples {
			out[s.Labels[0].Value] = s.Value
		}
		return out
	}
	t.Run("everyStateStartsAt0", func(t *testing.T) {
		if got := read(t); len(got) != 2 || got["live"] != 0 || got["dry_run"] != 0 {
			t.Errorf("before Set = %v, want both states at 0", got)
		}
	})
	t.Run("setMovesTheOne", func(t *testing.T) {
		mode.Set("live")
		mode.Set("dry_run")
		if got := read(t); got["live"] != 0 || got["dry_run"] != 1 {
			t.Errorf("after Set(dry_run) = %v, want dry_run 1 and live 0", got)
		}
	})
}

func TestStateSetLabeledSetsAppearAndLeave(t *testing.T) {
	r := NewRegistry("")
	health := NewStateSet("health", "h", []string{"provider", "health"}, []string{"ok", "down"})
	r.MustRegister(health)
	if page := body(t, r); strings.Contains(page, "health{") {
		t.Fatalf("an unseeded labeled state set is on the page before its first Set:\n%s", page)
	}
	health.Set("down", "a")
	page := body(t, r)
	for _, line := range []string{`health{health="down",provider="a"} 1`, `health{health="ok",provider="a"} 0`} {
		if !strings.Contains(page, line+"\n") {
			t.Errorf("page has no line %q:\n%s", line, page)
		}
	}
	health.Delete("a")
	if page := body(t, r); strings.Contains(page, "health{") {
		t.Errorf("Delete left states on the page:\n%s", page)
	}
}

func TestStateSetPanicsOnAMisuse(t *testing.T) {
	health := NewStateSet("health", "h", []string{"provider", "health"}, []string{"ok"})
	mustPanicContaining(t, "does not declare", func() { health.Set("degraded", "a") })
	mustPanicContaining(t, "label arity mismatch", func() { health.Set("ok") })
	mustPanicContaining(t, "label arity mismatch", func() { health.Delete("a", "ok") })
}

// TestStateSet_Concurrent checks the one invariant a hand-written state set
// cannot keep: a scrape never sees two states at 1, nor none once set.
func TestStateSet_Concurrent(t *testing.T) {
	r := NewRegistry("")
	mode := NewStateSet("mode", "h", []string{"mode"}, []string{"a", "b", "c"})
	r.MustRegister(mode)
	mode.Set("a")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				mode.Set([]string{"a", "b", "c"}[i%3])
			}
		}
	})
	for range 200 {
		fams, err := r.GatherOf(t.Context(), mode)
		if err != nil {
			t.Fatalf("GatherOf: %v", err)
		}
		ones := 0.0
		for _, s := range fams[0].Samples {
			ones += s.Value
		}
		if ones != 1 {
			t.Errorf("a read saw %v states at 1, want exactly 1", ones)
		}
	}
	close(stop)
	wg.Wait()
}

func TestInfoHoldsOneLabelSet(t *testing.T) {
	r := NewRegistry("app")
	build := NewInfo("build_info", "Always 1, labelled with the running version.", []string{"version"})
	r.MustRegister(build)
	if page := body(t, r); strings.Contains(page, "app_build_info") {
		t.Fatalf("info series on the page before its first Set:\n%s", page)
	}

	t.Run("rendersAsTheHandWrittenGauge", func(t *testing.T) {
		build.Set("1.2.0")
		gauge := NewLabeledGauge("build_info", "Always 1, labelled with the running version.", UnitNone, []string{"version"})
		other := NewRegistry("app")
		other.MustRegister(gauge)
		gauge.Set(1, "1.2.0")
		got, want := familyLines(body(t, r), "app_build_info"), familyLines(body(t, other), "app_build_info")
		if got != want || want == "" {
			t.Errorf("info page:\n%s\nwant the hand-written gauge's:\n%s", got, want)
		}
	})
	t.Run("setReplaces", func(t *testing.T) {
		build.Set("1.3.0")
		fams, err := r.GatherOf(t.Context(), build)
		if err != nil {
			t.Fatalf("GatherOf: %v", err)
		}
		if s := fams[0].Samples; len(s) != 1 || s[0].Labels[0].Value != "1.3.0" || s[0].Value != 1 {
			t.Errorf("after a second Set, samples = %+v, want only version 1.3.0 at 1", s)
		}
	})
	t.Run("deleteTakesItOff", func(t *testing.T) {
		build.Delete()
		if page := body(t, r); strings.Contains(page, "app_build_info") {
			t.Errorf("Delete left the info series on the page:\n%s", page)
		}
	})
	t.Run("arity", func(t *testing.T) {
		mustPanicContaining(t, "label arity mismatch", func() { build.Set() })
	})
}

func TestInfoWarnsOncePerSanitizedChange(t *testing.T) {
	logs := captureDebugLogs(t)
	build := NewInfo("build_info", "h", []string{"version"})
	NewRegistry("").MustRegister(build)
	build.Set("1.\xff")
	build.Set("1.\xff")
	if n := strings.Count(logs.String(), "invalid UTF-8"); n != 1 {
		t.Errorf("logged %d invalid UTF-8 warnings for one sanitized value set twice, want 1:\n%s", n, logs)
	}
}

func TestCollectorEmitsStateSetsAndInfo(t *testing.T) {
	_ = captureDebugLogs(t)
	health := NewStateSetDesc("health", "h", []string{"component", "health"}, []string{"ok", "down"})
	store := NewInfoDesc("store_info", "h", []string{"engine"})
	var emit func(e *Emitter)
	r := NewRegistry("")
	r.MustRegister(NewCollector(func(_ context.Context, e *Emitter) { emit(e) }, health, store))

	t.Run("served", func(t *testing.T) {
		emit = func(e *Emitter) {
			e.EmitState(health, "down", "db")
			e.Emit(store, 1, "bolt")
		}
		page := body(t, r)
		for _, line := range []string{
			"# TYPE health gauge", `health{component="db",health="ok"} 0`, `health{component="db",health="down"} 1`,
			"# TYPE store_info gauge", `store_info{engine="bolt"} 1`,
		} {
			if !strings.Contains(page, line+"\n") {
				t.Errorf("page has no line %q:\n%s", line, page)
			}
		}
	})
	for _, tc := range []struct {
		name string
		emit func(e *Emitter)
		want string
	}{
		{"unknownState", func(e *Emitter) { e.EmitState(health, "degraded", "db") }, `"health": 1 sample(s) dropped for values that do not fit`},
		{"repeatedSet", func(e *Emitter) { e.EmitState(health, "ok", "db"); e.EmitState(health, "down", "db") }, `"health": 1 sample(s) dropped for a label set already emitted`},
		{"emitOnAStateSet", func(e *Emitter) { e.Emit(health, 1, "db", "ok") }, `"health": 1 sample(s) dropped for values that do not fit`},
		{"emitStateOnInfo", func(e *Emitter) { e.EmitState(store, "ok") }, `"store_info": 1 sample(s) dropped for values that do not fit`},
		{"infoNotOne", func(e *Emitter) { e.Emit(store, 0, "bolt") }, `"store_info": 1 sample(s) dropped for values that do not fit`},
		{"stateArity", func(e *Emitter) { e.EmitState(health, "ok") }, `"health": 1 sample(s) dropped for the wrong number of label values`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emit = tc.emit
			_, err := r.Gather(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Gather error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	t.Run("aDroppedStateLeavesTheSetOff", func(t *testing.T) {
		emit = func(e *Emitter) { e.EmitState(health, "degraded", "db") }
		if page := body(t, r); strings.Contains(page, "health{") {
			t.Errorf("an unknown state put samples on the page:\n%s", page)
		}
	})
}
