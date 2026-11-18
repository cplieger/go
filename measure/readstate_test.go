package measure

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	allReadStates = []ReadState{ReadComplete, ReadPartial, ReadBlind, ReadUnread, ReadUnsupported, ReadExcluded}
	readT0        = time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
)

func TestReadStatesListsTheSixInOrder(t *testing.T) {
	want := []string{"complete", "partial", "blind", "unread", "unsupported", "excluded"}
	if got := ReadStates(); !slices.Equal(got, want) {
		t.Errorf("ReadStates() = %v, want %v", got, want)
	}
}

func TestZeroReadIsUnread(t *testing.T) {
	if got := FoldParts(Read{}); got.State != ReadUnread {
		t.Errorf("FoldParts(Read{}).State = %q, want unread", got.State)
	}
	if got := (Read{}).Under(Whole(readT0)); got.State != ReadUnread {
		t.Errorf("Read{}.Under(complete).State = %q, want unread", got.State)
	}
}

func TestWholeIsCompleteAtItsTime(t *testing.T) {
	if got, want := Whole(readT0), (Read{State: ReadComplete, AsOf: readT0}); got != want {
		t.Errorf("Whole = %+v, want %+v", got, want)
	}
}

// foldOracle is the rule both folds share, written from the definitions: no
// inputs is unread; all excluded is excluded; of the rest, all unsupported
// is unsupported; otherwise the supported inputs are counted and whole gives
// the state from them. AsOf is the oldest of the inputs counted, zero when
// one has none; worst is the worst counted state, whose reason the fold
// carries.
func foldOracle(in []Read, whole func(counted []ReadState) ReadState) (state, worst ReadState, asOf time.Time) {
	if len(in) == 0 {
		return ReadUnread, ReadUnread, time.Time{}
	}
	var kept, supported []Read
	for _, p := range in {
		if p.State != ReadExcluded {
			kept = append(kept, p)
		}
	}
	for _, p := range kept {
		if p.State != ReadUnsupported {
			supported = append(supported, p)
		}
	}
	counted := supported
	switch {
	case len(kept) == 0:
		counted, state, worst = in, ReadExcluded, ReadExcluded
	case len(supported) == 0:
		counted, state, worst = kept, ReadUnsupported, ReadUnsupported
	default:
		states := make([]ReadState, len(supported))
		for i, p := range supported {
			states[i] = p.State
		}
		state, worst = whole(states), worstOf(states)
	}
	for i, p := range counted {
		if p.AsOf.IsZero() {
			return state, worst, time.Time{}
		}
		if i == 0 || p.AsOf.Before(asOf) {
			asOf = p.AsOf
		}
	}
	return state, worst, asOf
}

// worstOf is the worst of blind, unread, partial and complete.
func worstOf(states []ReadState) ReadState {
	for _, s := range []ReadState{ReadBlind, ReadUnread, ReadPartial} {
		if slices.Contains(states, s) {
			return s
		}
	}
	return ReadComplete
}

// partsWhole is a scope of parts: every part complete is complete; none
// complete or partial is blind when one is blind, else unread; anything
// else is partial.
func partsWhole(states []ReadState) ReadState {
	switch {
	case !slices.ContainsFunc(states, func(s ReadState) bool { return s != ReadComplete }):
		return ReadComplete
	case slices.Contains(states, ReadComplete), slices.Contains(states, ReadPartial):
		return ReadPartial
	case slices.Contains(states, ReadBlind):
		return ReadBlind
	}
	return ReadUnread
}

// partOf gives state s the AsOf at minute m (none for unread, which no whole
// read reached) and a reason naming the state.
func partOf(s ReadState, m int) Read {
	r := Read{State: s, AsOf: readT0.Add(time.Duration(m) * time.Minute)}
	if s == ReadUnread {
		r.AsOf = time.Time{}
	}
	if s != ReadComplete {
		r.Reason = string(s) + "_reason"
	}
	return r
}

func permutations(parts []Read) [][]Read {
	if len(parts) <= 1 {
		return [][]Read{slices.Clone(parts)}
	}
	var out [][]Read
	for i := range parts {
		rest := slices.Concat(parts[:i:i], parts[i+1:])
		for _, p := range permutations(rest) {
			out = append(out, append([]Read{parts[i]}, p...))
		}
	}
	return out
}

// checkFoldIsOrderFree checks fold over every single input and every
// ordered pair and triple of the six states, in every permutation, against
// the oracle with whole.
func checkFoldIsOrderFree(t *testing.T, name string, fold func(...Read) Read, whole func([]ReadState) ReadState) {
	t.Helper()
	var sets [][]Read
	for _, a := range allReadStates {
		sets = append(sets, []Read{partOf(a, 3)})
		for _, b := range allReadStates {
			sets = append(sets, []Read{partOf(a, 3), partOf(b, 1)})
			for _, c := range allReadStates {
				sets = append(sets, []Read{partOf(a, 3), partOf(b, 1), partOf(c, 2)})
			}
		}
	}
	for _, in := range sets {
		wantState, worst, wantAsOf := foldOracle(in, whole)
		wantReason := partOf(worst, 0).Reason
		if wantState == ReadComplete {
			wantReason = ""
		}
		for _, perm := range permutations(in) {
			got := fold(perm...)
			if got.State != wantState || !got.AsOf.Equal(wantAsOf) {
				t.Errorf("%s(%v) = %s at %v, want %s at %v", name, states(perm), got.State, got.AsOf, wantState, wantAsOf)
			}
			if got.Reason != wantReason {
				t.Errorf("%s(%v).Reason = %q, want the worst input's %q", name, states(perm), got.Reason, wantReason)
			}
		}
	}
}

func TestFoldPartsIsOrderFree(t *testing.T) {
	checkFoldIsOrderFree(t, "FoldParts", FoldParts, partsWhole)
}

func TestFoldNeedsIsOrderFree(t *testing.T) {
	checkFoldIsOrderFree(t, "FoldNeeds", FoldNeeds, worstOf)
}

func TestFoldMixedParts(t *testing.T) {
	complete := Whole(readT0)
	blind := Whole(readT0.Add(-time.Hour)).Cut(ReadBlind, "rate_limited")
	unread := Read{State: ReadUnread, Reason: "not_reached"}
	partial := Whole(readT0).Cut(ReadPartial, "page_cap")
	for _, c := range []struct {
		name         string
		in           []Read
		parts, needs ReadState
		reason       string
		asOf         time.Time
	}{
		{"one blind beside complete ones", []Read{complete, blind, complete}, ReadPartial, ReadBlind, "rate_limited", readT0.Add(-time.Hour)},
		{"one unread beside a complete one", []Read{unread, complete}, ReadPartial, ReadUnread, "not_reached", time.Time{}},
		{"one partial beside a complete one", []Read{complete, partial}, ReadPartial, ReadPartial, "page_cap", readT0},
		{"a blind and an unread", []Read{unread, blind}, ReadBlind, ReadBlind, "rate_limited", time.Time{}},
		{"only unread ones", []Read{unread, unread}, ReadUnread, ReadUnread, "not_reached", time.Time{}},
		{"a blind beside a partial one", []Read{partial, blind}, ReadPartial, ReadBlind, "rate_limited", readT0.Add(-time.Hour)},
		{"every one complete", []Read{complete, complete}, ReadComplete, ReadComplete, "", readT0},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, n := FoldParts(c.in...), FoldNeeds(c.in...)
			if p.State != c.parts || n.State != c.needs {
				t.Errorf("FoldParts = %s, FoldNeeds = %s, want %s and %s", p.State, n.State, c.parts, c.needs)
			}
			if p.Reason != c.reason || n.Reason != c.reason {
				t.Errorf("reasons %q and %q, want the worst input's %q in both", p.Reason, n.Reason, c.reason)
			}
			if !p.AsOf.Equal(c.asOf) || !n.AsOf.Equal(c.asOf) {
				t.Errorf("AsOf %v and %v, want the oldest, %v", p.AsOf, n.AsOf, c.asOf)
			}
		})
	}
}

func states(parts []Read) []ReadState {
	out := make([]ReadState, len(parts))
	for i, p := range parts {
		out[i] = p.State
	}
	return out
}

func TestFoldOfNoInputsIsUnread(t *testing.T) {
	if got := FoldParts(); got != (Read{State: ReadUnread}) {
		t.Errorf("FoldParts() = %+v, want unread with no time", got)
	}
	if got := FoldNeeds(); got != (Read{State: ReadUnread}) {
		t.Errorf("FoldNeeds() = %+v, want unread with no time", got)
	}
}

func TestFoldTakesTheFirstWorstReasonAmongEquals(t *testing.T) {
	a := Read{State: ReadBlind, AsOf: readT0, Reason: "rate_limited"}
	b := Read{State: ReadBlind, AsOf: readT0, Reason: "root_unavailable"}
	for name, fold := range map[string]func(...Read) Read{"FoldParts": FoldParts, "FoldNeeds": FoldNeeds} {
		if got := fold(a, b).Reason; got != "rate_limited" {
			t.Errorf("%s(a, b).Reason = %q, want the first, rate_limited", name, got)
		}
		if got := fold(b, a).Reason; got != "root_unavailable" {
			t.Errorf("%s(b, a).Reason = %q, want the first, root_unavailable", name, got)
		}
	}
}

func TestUnderTable(t *testing.T) {
	// want[own][parent], rows and columns in allReadStates order: a read
	// whose own state is neither complete nor partial is kept; under a
	// complete or excluded parent it is kept; under blind it is blind;
	// under any other parent it is partial.
	want := [6][6]ReadState{
		{ReadComplete, ReadPartial, ReadBlind, ReadPartial, ReadPartial, ReadComplete},
		{ReadPartial, ReadPartial, ReadBlind, ReadPartial, ReadPartial, ReadPartial},
		{ReadBlind, ReadBlind, ReadBlind, ReadBlind, ReadBlind, ReadBlind},
		{ReadUnread, ReadUnread, ReadUnread, ReadUnread, ReadUnread, ReadUnread},
		{ReadUnsupported, ReadUnsupported, ReadUnsupported, ReadUnsupported, ReadUnsupported, ReadUnsupported},
		{ReadExcluded, ReadExcluded, ReadExcluded, ReadExcluded, ReadExcluded, ReadExcluded},
	}
	for i, own := range allReadStates {
		for j, parent := range allReadStates {
			r, p := partOf(own, 5), partOf(parent, 2)
			got := r.Under(p)
			if got.State != want[i][j] {
				t.Errorf("%s under %s = %s, want %s", own, parent, got.State, want[i][j])
			}
			wantAsOf := r.AsOf
			if (own == ReadComplete || own == ReadPartial) && parent != ReadExcluded && (p.AsOf.IsZero() || p.AsOf.Before(r.AsOf)) {
				wantAsOf = p.AsOf
			}
			if !got.AsOf.Equal(wantAsOf) {
				t.Errorf("%s under %s: AsOf = %v, want the older, %v", own, parent, got.AsOf, wantAsOf)
			}
		}
	}
}

func TestUnderNamesWhyARead(t *testing.T) {
	root := Read{State: ReadBlind, AsOf: readT0, Reason: "root_unavailable"}
	if got := Whole(readT0.Add(time.Hour)).Under(root); got.Reason != "root_unavailable" {
		t.Errorf("complete under a blind root: Reason = %q, want the root's", got.Reason)
	}
	own := Whole(readT0).Cut(ReadPartial, "page_cap")
	if got := own.Under(root.Cut(ReadPartial, "rate_limited")); got.Reason != "page_cap" {
		t.Errorf("partial under a partial parent: Reason = %q, want its own page_cap", got.Reason)
	}
	if got := Whole(readT0).Under(Whole(readT0)); got.Reason != "" {
		t.Errorf("complete under complete: Reason = %q, want none", got.Reason)
	}
}

func TestCutNeverMovesAsOf(t *testing.T) {
	for _, prev := range []Read{{}, Whole(readT0), Whole(readT0).Cut(ReadPartial, "rate_limited")} {
		for _, s := range allReadStates {
			got := prev.Cut(s, "store_full")
			if !got.AsOf.Equal(prev.AsOf) || got.State != s || got.Reason != "store_full" {
				t.Errorf("%+v.Cut(%s, store_full) = %+v, want state %s, that reason and AsOf %v", prev, s, got, s, prev.AsOf)
			}
		}
	}
}

func TestNewReadDescsNamesBothSeriesFromTheStem(t *testing.T) {
	d := NewReadDescs("signal", "Each signal's read", []string{"connection", "signal"})
	st, at := d.State.Declaration(), d.Complete.Declaration()
	if st.Name != "signal_state" || st.Kind != KindStateSet || !slices.Equal(st.Labels, []string{"connection", "signal", "state"}) ||
		!slices.Equal(st.States, ReadStates()) {
		t.Errorf("State = %+v, want signal_state, a state set over connection, signal and state with the six states", st)
	}
	if at.Name != "signal_complete_timestamp_seconds" || at.Kind != KindGauge || at.Unit != UnitSeconds ||
		!slices.Equal(at.Labels, []string{"connection", "signal"}) {
		t.Errorf("Complete = %+v, want signal_complete_timestamp_seconds, a gauge in seconds over connection and signal", at)
	}
	r := NewRegistry("app")
	if err := r.Register(NewCollector(func(context.Context, *Emitter) {}, d.State, d.Complete)); err != nil {
		t.Errorf("Register = %v, want nil", err)
	}
}

func TestEmitReadServesTheStateAndTheLastWholeTime(t *testing.T) {
	d := NewReadDescs("signal", "Each signal's read", []string{"signal"})
	at := time.Unix(1_763_474_700, 500_000_000)
	r := NewRegistry("app")
	r.MustRegister(staticCollector(func(e *Emitter) {
		e.EmitRead(d, Whole(at).Cut(ReadPartial, "rate_limited"), "checks")
		e.EmitRead(d, Read{}, "reviews")
	}, d.State, d.Complete))
	out := body(t, r)
	for _, line := range []string{
		`app_signal_state{signal="checks",state="partial"} 1`,
		`app_signal_state{signal="checks",state="complete"} 0`,
		`app_signal_complete_timestamp_seconds{signal="checks"} 1.7634747005e+09`,
		`app_signal_state{signal="reviews",state="unread"} 1`,
		`app_signal_complete_timestamp_seconds{signal="reviews"} 0`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("page lacks %q:\n%s", line, out)
		}
	}
	if _, err := r.Gather(t.Context()); err != nil {
		t.Errorf("Gather = %v, want nil", err)
	}
}

// keepCollector emits value on each run whose err is nil and calls Keep with
// err otherwise.
type keepCollector struct {
	err   error
	g     *Desc
	mu    sync.Mutex
	value float64
}

func (k *keepCollector) set(value float64, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.value, k.err = value, err
}

func (k *keepCollector) collect(_ context.Context, e *Emitter) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		e.Emit(k.g, -1, "a")
		e.Keep(k.err)
		return
	}
	e.Emit(k.g, k.value, "a")
}

func gaugeValue(t *testing.T, r *Registry, name string) (float64, bool) {
	t.Helper()
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Errorf("Gather = %v, want nil", err)
	}
	f := findFamily(t, fams, name)
	s, ok := f.Sample("a")
	return s.Value, ok
}

func TestKeepServesLastSamples(t *testing.T) {
	logs := captureDebugLogs(t)
	k := &keepCollector{g: NewGaugeDesc("g", "h", UnitNone, []string{"k"})}
	r := NewRegistry("")
	r.MustRegister(NewCollector(k.collect, k.g))

	k.set(7, nil)
	if v, ok := gaugeValue(t, r, "g"); !ok || v != 7 {
		t.Fatalf("first run serves %v (present %v), want 7", v, ok)
	}
	k.set(0, errors.New("store read failed"))
	for range 3 {
		if v, ok := gaugeValue(t, r, "g"); !ok || v != 7 {
			t.Errorf("a kept run serves %v (present %v), want the last success's 7", v, ok)
		}
		if out := body(t, r); !strings.Contains(out, "g{k=\"a\"} 7\n") {
			t.Errorf("a kept scrape's page lacks the last sample:\n%s", out)
		}
	}
	k.set(9, nil)
	if v, ok := gaugeValue(t, r, "g"); !ok || v != 9 {
		t.Errorf("after recovery serves %v (present %v), want 9", v, ok)
	}
	body(t, r)
	warns := strings.Count(logs.String(), "level=WARN")
	clears := strings.Count(logs.String(), "level=INFO")
	if warns != 1 || clears != 1 || !strings.Contains(logs.String(), "store read failed") {
		t.Errorf("got %d warnings and %d clearings naming the cause, want 1 and 1:\n%s", warns, clears, logs)
	}
}

func TestKeepBeforeFirstSuccessServesNothing(t *testing.T) {
	captureDebugLogs(t)
	k := &keepCollector{g: NewGaugeDesc("g", "h", UnitNone, []string{"k"})}
	r := NewRegistry("")
	r.MustRegister(NewCollector(k.collect, k.g))
	k.set(0, errors.New("store read failed"))
	if _, ok := gaugeValue(t, r, "g"); ok {
		t.Errorf("a collector that never succeeded serves a sample, want none")
	}
	if out := body(t, r); strings.Contains(out, "g{") {
		t.Errorf("page serves a sample for a collector that never succeeded:\n%s", out)
	}
}

func TestKeptSamplesSurviveAReaderEditingThem(t *testing.T) {
	captureDebugLogs(t)
	k := &keepCollector{g: NewGaugeDesc("g", "h", UnitNone, []string{"k"})}
	r := NewRegistry("")
	r.MustRegister(NewCollector(k.collect, k.g))
	edit := func() {
		fams, _ := r.Gather(t.Context())
		f := findFamily(t, fams, "g")
		for i := range f.Samples {
			f.Samples[i].Value = 99
			f.Samples[i].Labels[0].Value = "edited"
		}
	}
	k.set(7, nil)
	edit()
	k.set(0, errors.New("store read failed"))
	if v, ok := gaugeValue(t, r, "g"); !ok || v != 7 {
		t.Errorf("after a caller edited the successful Gather, a kept run serves %v (present %v), want 7", v, ok)
	}
	edit()
	if v, ok := gaugeValue(t, r, "g"); !ok || v != 7 {
		t.Errorf("after a caller edited a kept Gather, serves %v (present %v), want 7", v, ok)
	}
}

func TestKeepUnderConcurrentScrapesNeverServesAFailedRun(t *testing.T) {
	captureDebugLogs(t)
	k := &keepCollector{g: NewGaugeDesc("g", "h", UnitNone, []string{"k"})}
	r := NewRegistry("")
	r.MustRegister(NewCollector(k.collect, k.g))
	k.set(0, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var served []float64
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				var err error
				if (i+j)%3 == 0 {
					err = fmt.Errorf("read %d failed", j)
				}
				k.set(float64(j), err)
				fams, _ := r.Gather(t.Context())
				f := findFamily(t, fams, "g")
				if s, ok := f.Sample("a"); ok {
					mu.Lock()
					served = append(served, s.Value)
					mu.Unlock()
				}
				_ = body(t, r)
			}
		})
	}
	wg.Wait()
	if i := slices.Index(served, -1); i >= 0 || len(served) == 0 {
		t.Errorf("served %d samples, the failed run's -1 at %d; want only successful runs' values", len(served), i)
	}
}

func TestKeepServesTheLatestStartedSuccess(t *testing.T) {
	captureDebugLogs(t)
	g := NewGaugeDesc("g", "h", UnitNone, []string{"k"})
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	step := 0
	r := NewRegistry("")
	r.MustRegister(NewCollector(func(_ context.Context, e *Emitter) {
		mu.Lock()
		step++
		n := step
		mu.Unlock()
		switch n {
		case 1:
			close(started)
			<-release
			e.Emit(g, 1, "a")
		case 2:
			e.Emit(g, 2, "a")
		default:
			e.Keep(errors.New("store read failed"))
		}
	}, g))
	slow := make(chan struct{})
	go func() {
		defer close(slow)
		_, _ = r.Gather(t.Context())
	}()
	<-started
	gaugeValue(t, r, "g")
	close(release)
	<-slow
	if v, ok := gaugeValue(t, r, "g"); !ok || v != 2 {
		t.Errorf("a kept run serves %v (present %v), want 2, the later-started run's, not the later-finished 1", v, ok)
	}
}

func TestUnknownReadStateReadsBlind(t *testing.T) {
	odd := Read{State: "stale", AsOf: readT0, Reason: "odd"}
	for name, fold := range map[string]func(...Read) Read{"FoldParts": FoldParts, "FoldNeeds": FoldNeeds} {
		if got := fold(odd, Whole(readT0).Cut(ReadUnread, "late")); got.State != ReadBlind || got.Reason != "odd" {
			t.Errorf("%s with an unknown state beside an unread one = %+v, want blind with its reason", name, got)
		}
	}
	if got := FoldParts(Whole(readT0), odd); got.State != ReadPartial || got.Reason != "odd" {
		t.Errorf("FoldParts with an unknown state beside a complete one = %+v, want partial with its reason", got)
	}
	if got := odd.Under(Whole(readT0)); got.State != ReadBlind {
		t.Errorf("unknown under complete = %s, want blind", got.State)
	}
	if got := Whole(readT0).Under(odd); got.State != ReadBlind || got.Reason != "odd" {
		t.Errorf("complete under an unknown parent = %+v, want blind with the parent's reason", got)
	}
}

func TestEmitReadDropsAnUnknownState(t *testing.T) {
	captureDebugLogs(t)
	d := NewReadDescs("signal", "Each signal's read", []string{"signal"})
	r := NewRegistry("app")
	r.MustRegister(staticCollector(func(e *Emitter) {
		e.EmitRead(d, Read{State: "stale", AsOf: readT0}, "checks")
	}, d.State, d.Complete))
	fams, err := r.Gather(t.Context())
	if err == nil || !strings.Contains(err.Error(), "app_signal_state") {
		t.Errorf("Gather = %v, want the dropped state set named", err)
	}
	if f := findFamily(t, fams, "app_signal_state"); len(f.Samples) != 0 {
		t.Errorf("state set serves %d samples for an unknown state, want none", len(f.Samples))
	}
}
