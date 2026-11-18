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
	if got := FoldRead(Read{}); got.State != ReadUnread {
		t.Errorf("FoldRead(Read{}).State = %q, want unread", got.State)
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

// foldOracle is section 4.1's rule as written: no parts is unread; all
// excluded is excluded; of the rest, all unsupported is unsupported;
// otherwise the worst of blind, unread, partial, complete among what is
// left. AsOf is the oldest of the parts counted, zero when one has none.
func foldOracle(parts []Read) (ReadState, time.Time) {
	if len(parts) == 0 {
		return ReadUnread, time.Time{}
	}
	var kept, supported []Read
	for _, p := range parts {
		if p.State != ReadExcluded {
			kept = append(kept, p)
		}
	}
	for _, p := range kept {
		if p.State != ReadUnsupported {
			supported = append(supported, p)
		}
	}
	counted, state := supported, ReadComplete
	switch {
	case len(kept) == 0:
		counted, state = parts, ReadExcluded
	case len(supported) == 0:
		counted, state = kept, ReadUnsupported
	default:
		rank := map[ReadState]int{ReadComplete: 0, ReadPartial: 1, ReadUnread: 2, ReadBlind: 3}
		for _, p := range supported {
			if rank[p.State] > rank[state] {
				state = p.State
			}
		}
	}
	var asOf time.Time
	for i, p := range counted {
		if p.AsOf.IsZero() {
			return state, time.Time{}
		}
		if i == 0 || p.AsOf.Before(asOf) {
			asOf = p.AsOf
		}
	}
	return state, asOf
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

func TestFoldReadIsOrderFree(t *testing.T) {
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
	for _, parts := range sets {
		wantState, wantAsOf := foldOracle(parts)
		for _, perm := range permutations(parts) {
			got := FoldRead(perm...)
			if got.State != wantState || !got.AsOf.Equal(wantAsOf) {
				t.Errorf("FoldRead(%v) = %s at %v, want %s at %v", states(perm), got.State, got.AsOf, wantState, wantAsOf)
			}
			if want := partOf(wantState, 0).Reason; wantState != ReadComplete && got.Reason != want {
				t.Errorf("FoldRead(%v).Reason = %q, want the worst part's %q", states(perm), got.Reason, want)
			}
			if wantState == ReadComplete && got.Reason != "" {
				t.Errorf("FoldRead(%v).Reason = %q, want none for a complete fold", states(perm), got.Reason)
			}
		}
	}
}

func states(parts []Read) []ReadState {
	out := make([]ReadState, len(parts))
	for i, p := range parts {
		out[i] = p.State
	}
	return out
}

func TestFoldReadOfNoPartsIsUnread(t *testing.T) {
	if got := FoldRead(); got != (Read{State: ReadUnread}) {
		t.Errorf("FoldRead() = %+v, want unread with no time", got)
	}
}

func TestFoldReadTakesTheFirstWorstReasonAmongEquals(t *testing.T) {
	a := Read{State: ReadBlind, AsOf: readT0, Reason: "rate_limited"}
	b := Read{State: ReadBlind, AsOf: readT0, Reason: "root_unavailable"}
	if got := FoldRead(a, b).Reason; got != "rate_limited" {
		t.Errorf("FoldRead(a, b).Reason = %q, want the first, rate_limited", got)
	}
	if got := FoldRead(b, a).Reason; got != "root_unavailable" {
		t.Errorf("FoldRead(b, a).Reason = %q, want the first, root_unavailable", got)
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
	k.set(7, nil)
	gaugeValue(t, r, "g")
	k.set(0, errors.New("store read failed"))
	fams, _ := r.Gather(t.Context())
	f := findFamily(t, fams, "g")
	for i := range f.Samples {
		f.Samples[i].Value = 99
		f.Samples[i].Labels[0].Value = "edited"
	}
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
