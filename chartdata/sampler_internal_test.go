package chartdata

import (
	"context"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/measure/v2"
)

// The cases follow Prometheus's histogram_quantile (promql/quantile.go):
// linear interpolation inside the bucket the rank falls in, the lowest
// bucket from 0 when its bound is above 0, the highest finite bound for a
// rank in the +Inf bucket, NaN for no observations or fewer than two buckets.
func TestQuantileIsHistogramQuantile(t *testing.T) {
	t.Parallel()
	bounds := []float64{1, 2, 4}
	for _, tc := range []struct {
		name   string
		bounds []float64
		cum    []float64
		q      float64
		want   float64
	}{
		{"median", bounds, []float64{1, 3, 4, 4}, 0.5, 1.5},
		{"top", bounds, []float64{1, 3, 4, 4}, 1, 4},
		{"p99", bounds, []float64{1, 3, 4, 4}, 0.99, 3.92},
		{"lowestFromZero", bounds, []float64{4, 4, 4, 4}, 0.25, 0.25},
		{"inTheInfBucket", bounds, []float64{1, 1, 1, 4}, 0.9, 4},
		{"lowestBoundAtZero", []float64{0, 1}, []float64{2, 4, 4}, 0.25, 0},
		{"lowestBoundBelowZero", []float64{-1, 1}, []float64{2, 4, 4}, 0.25, -1},
		{"unsortedCountsMadeMonotonic", bounds, []float64{2, 1, 4, 4}, 0.5, 1},
		{"noObservations", bounds, []float64{0, 0, 0, 0}, 0.5, math.NaN()},
		{"onlyTheInfBucket", nil, []float64{5}, 0.5, math.NaN()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quantile(tc.q, tc.bounds, tc.cum)
			if math.IsNaN(got) != math.IsNaN(tc.want) || math.Abs(got-tc.want) > 1e-12 {
				t.Errorf("quantile(%v, %v, %v) = %v, want %v", tc.q, tc.bounds, tc.cum, got, tc.want)
			}
		})
	}
}

func TestNextEdgeIsTheNextMultipleSinceTheEpoch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		now   time.Time
		every time.Duration
		want  time.Time
	}{
		{time.Unix(90, 0), time.Minute, time.Unix(120, 0)},
		{time.Unix(120, 0), time.Minute, time.Unix(180, 0)},
		{time.Unix(7, 1), 7 * time.Second, time.Unix(14, 0)},
	} {
		if got := nextEdge(tc.now, tc.every); !got.Equal(tc.want) {
			t.Errorf("nextEdge(%v, %v) = %v, want %v", tc.now.Unix(), tc.every, got.Unix(), tc.want.Unix())
		}
	}
}

// A wall clock stepped back across the newest column must not give an edge
// at or before it, or the ring's columns would fall out of order.
func TestSamplerNeverTakesAnEdgeAtOrBeforeTheNewest(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	depth := measure.NewGauge("depth", "h", measure.UnitCount)
	reg.MustRegister(depth)
	track := Sampled(depth)
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: time.Hour}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	if got := s.after(time.Unix(6_030, 0)); !got.Equal(time.Unix(6_060, 0)) {
		t.Errorf("the first edge = %v, want the next minute, 6060", got.Unix())
	}
	for _, edge := range []int64{6_060, 6_120} {
		if _, _, err := s.tick(t.Context(), time.Unix(edge, 0)); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	for _, tc := range []struct {
		now, want int64
	}{
		{6_000, 6_180}, // stepped back past both columns
		{6_120, 6_180}, // on the newest column
		{6_200, 6_240}, // the clock moving on
	} {
		if got := s.after(time.Unix(tc.now, 0)); !got.Equal(time.Unix(tc.want, 0)) {
			t.Errorf("after(%d) = %d, want %d", tc.now, got.Unix(), tc.want)
		}
	}
	if _, err := s.Series(track, time.Hour); err != nil {
		t.Errorf("Series: %v", err)
	}
}

// A timer that fires late, as in a process paused across several edges,
// reads at the latest edge it passed, not the one it was set for: the
// edges between are gaps, and a counter's increase over the pause is in no
// column, since when inside the pause it happened is unknown.
func TestSamplerStampsALateReadAtTheEdgeItIsTakenAt(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	runs := measure.NewCounter("runs_total", "h", measure.UnitCount)
	reg.MustRegister(runs)
	track := Sampled(runs)
	var events []FrameEvent
	s, err := NewSampler(Sampling{
		Registry: reg, Every: time.Minute, Keep: 10 * time.Minute,
		Emit: func(e FrameEvent) { events = append(events, e) },
	}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	minute := func(m int64) time.Time { return time.Unix(m*60, 0) }
	wake := func(next, now time.Time) {
		t.Helper()
		if err := s.wake(t.Context(), next, now); err != nil {
			t.Fatalf("wake: %v", err)
		}
	}
	runs.Add(1)
	wake(minute(101), minute(101)) // the first column, its reset and its append
	runs.Add(1)
	wake(minute(102), minute(102).Add(time.Second))
	next := s.after(minute(102).Add(30 * time.Second))
	if !next.Equal(minute(103)) {
		t.Fatalf("the timer is set for %d, want 103", next.Unix()/60)
	}
	runs.Add(120)
	events = nil
	wake(next, minute(108).Add(30*time.Second))
	f, err := s.Series(track, 10*time.Minute)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	var x []int64
	for _, v := range f.X {
		x = append(x, int64(v)/60_000)
	}
	if want := []int64{101, 102, 103, 104, 105, 106, 107, 108}; !slices.Equal(x, want) {
		t.Errorf("columns = %v, want %v", x, want)
	}
	got := plainValues(f.Series[0].Values)
	if want := []float64{math.NaN(), 1, math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()}; !slices.EqualFunc(got, want, sameFloat) {
		t.Errorf("increases = %v, want %v: the pause's 120 in no column", got, want)
	}
	if len(events) != 6 {
		t.Errorf("the late wake sent %d events, want an append for each of 103 to 108, 6", len(events))
	}
	runs.Add(4)
	wake(minute(109), minute(109))
	f, err = s.Series(track, time.Minute)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if got := plainValues(f.Series[0].Values); len(got) != 1 || got[0] != 4 {
		t.Errorf("the column after the pause = %v, want [4]", got)
	}
}

// A counter whose first reading after missed edges is unusable keeps no
// baseline, since an increase from the one before the edges would cover
// several steps: its next usable reading is a first appearance.
func TestSamplerKeepsNoBaselinePastMissedEdges(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	jobs := measure.NewCounterDesc("jobs_total", "h", measure.UnitCount, nil)
	total := 0.0
	reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		e.Emit(jobs, total)
	}, jobs))
	track := Sampled(jobs)
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 10 * time.Minute}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	for _, step := range []struct {
		minute int64
		total  float64
	}{
		{1, 10}, {2, 11}, {5, math.Inf(1)}, {6, 20}, {7, 21}, // 3 and 4 missed
	} {
		total = step.total
		if _, _, err := s.tick(t.Context(), time.Unix(step.minute*60, 0)); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	f, err := s.Series(track, 10*time.Minute)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	nan := math.NaN()
	if got, want := plainValues(f.Series[0].Values), []float64{nan, 1, nan, nan, nan, nan, 1}; !slices.EqualFunc(got, want, sameFloat) {
		t.Errorf("increases of 10, 11, +Inf after two missed edges, 20, 21 = %v, want %v", got, want)
	}
}

// plainValues returns wire values as plain ones, NaN for a gap.
func plainValues(values []*float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = math.NaN()
		if v != nil {
			out[i] = *v
		}
	}
	return out
}

// A series given no value at a step, such as the first after missed edges,
// keeps its ring while the ring holds a usable point, so the page keeps
// getting appends, and loses it once the ring holds none, however often it
// is read.
func TestSamplerKeepsASeriesWhileItsRingHoldsAValue(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	runs := measure.NewCounter("runs_total", "h", measure.UnitCount)
	wait := measure.NewHistogram("wait_seconds", "h", measure.UnitSeconds)
	reg.MustRegister(runs, wait)
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 3 * time.Minute},
		Sampled(runs), Sampled(wait, AsQuantile(0.5)))
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	tick := func(minute int64, observe bool) []FrameEvent {
		t.Helper()
		if observe {
			runs.Add(1)
			wait.Observe(0.1)
		}
		events, _, err := s.tick(t.Context(), time.Unix(minute*60, 0))
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		return events
	}
	tick(1, true)
	tick(2, true) // both series take their first value
	// 3 is missed, so each ring is 2's value and two gaps.
	for _, e := range tick(4, true) {
		if e.Kind != KindAppend || len(e.Values) != 1 {
			t.Errorf("minute 4: %s event of %s with %d values, want appends of one series", e.Kind, e.Key, len(e.Values))
		}
	}
	// The quantile reads no observation, so its ring is all gaps; the
	// counter's increase of 0 is a value.
	events := tick(5, false)
	if len(events) != 2 {
		t.Fatalf("minute 5 sent %d events, want one per track", len(events))
	}
	if e := events[0]; e.Kind != KindAppend || len(e.Values) != 1 || e.Values[0] == nil || *e.Values[0] != 0 {
		t.Errorf("the counter's event = %s of %d values, want an append of 0", e.Kind, len(e.Values))
	}
	if e := events[1]; e.Kind != KindSet || e.Series == nil || len(e.Series.Series) != 0 {
		t.Errorf("the quantile's event = %s, want a set of no series", e.Kind)
	}
}

func sameFloat(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }

// A histogram label set read before its first observation keeps a baseline
// of zeros, so its first observation counts, and a track keeps at most
// MaxBaselines of them: one forgotten past the cap gives no point for its
// first observation, a first appearance.
func TestSamplerKeepsAtMostMaxBaselinesOfUnobservedLabelSets(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	wait := measure.NewHistogramDesc("wait_seconds", "h", measure.UnitSeconds, []string{"agent"}, []float64{1, 2, 4},
		measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
	agents := make([]string, MaxBaselines+5)
	for i := range agents {
		agents[i] = strconv.Itoa(10_000 + i)
	}
	observed := map[string]uint64{}
	reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		for _, a := range agents {
			n := observed[a]
			e.EmitHistogram(wait, []uint64{n, n, n}, n, float64(n)/2, a) // n observations of 0.5
		}
	}, wait))
	track := Sampled(wait, AsQuantile(0.5), By("agent"))
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 10 * time.Minute, Logger: slog.New(slog.DiscardHandler)}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	tick := func(minute int64) {
		t.Helper()
		if _, _, err := s.tick(t.Context(), time.Unix(minute*60, 0)); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	tick(1)
	if got := len(s.tracks[track].bases); got != MaxBaselines {
		t.Errorf("baselines of %d unobserved label sets = %d, want %d", len(agents), got, MaxBaselines)
	}
	// All changed at the first column and hold no place, so the first in
	// label-set order are forgotten.
	forgotten, kept := agents[0], agents[len(agents)-1]
	observed[forgotten], observed[kept] = 1, 1
	tick(2)
	f, err := s.Series(track, 10*time.Minute)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(f.Series) != 1 || f.Series[0].Key != kept {
		t.Fatalf("series = %d, want only %s's: %s's baseline was forgotten", len(f.Series), kept, forgotten)
	}
	if got, want := plainValues(f.Series[0].Values), []float64{math.NaN(), 0.5}; !slices.EqualFunc(got, want, sameFloat) {
		t.Errorf("%s = %v, want %v: its first observation counts from zero", kept, got, want)
	}
}

// At the baseline cap, a label set holding a place keeps its baseline
// however long ago it changed: only those holding no place are forgotten.
func TestSamplerNeverForgetsTheBaselineOfALabelSetHoldingAPlace(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("", measure.WithLogger(slog.New(slog.DiscardHandler)))
	runs := measure.NewCounterDesc("runs_total", "h", measure.UnitCount, []string{"agent"},
		measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("agent"))
	newer := 0
	reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
		for i := range MaxLabelSets {
			e.Emit(runs, 1, "p"+strconv.Itoa(10_000+i)) // idle: an increase of 0 at each step
		}
		for i := range newer {
			e.Emit(runs, 1, "q"+strconv.Itoa(10_000+i))
		}
	}, runs))
	track := Sampled(runs, By("agent"))
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 10 * time.Minute, Logger: slog.New(slog.DiscardHandler)}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	ts := s.tracks[track]
	for minute, n := range []int{0, MaxBaselines} {
		newer = n
		if _, _, err := s.tick(t.Context(), time.Unix(int64(minute+1)*60, 0)); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if len(ts.places) != MaxLabelSets || len(ts.bases) != MaxBaselines {
		t.Fatalf("places = %d, baselines = %d, want %d and %d", len(ts.places), len(ts.bases), MaxLabelSets, MaxBaselines)
	}
	for set := range ts.places {
		if _, ok := ts.bases[set]; !ok {
			t.Errorf("the placed label set %q lost its baseline", set)
		}
	}
}

// A step whose sum for a series is a gap gives that series' ring no point,
// so none of its label sets takes or renews a place at that step: a new
// label set whose quantile is past 1e300 takes none, held label sets whose
// gauges sum past 1e300 lose their places once their points leave the ring,
// however often they are read, and the places they free go to new label sets
// of held series before new series.
func TestSamplerGivesNoPlaceForAStepWhoseSumIsAGap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		kind  measure.Kind
		track []TrackOption
		// steps holds a histogram's cumulative observations per bucket by
		// label set, "key/host", +Inf last.
		steps []map[string][]uint64
		// gauges holds a gauge's value by label set.
		gauges     []map[string]float64
		newest     map[string]float64 // each series' newest column, by key
		want, gone []string           // the label sets holding a place, and not
		mostSets   int
	}{
		{
			name:  "newLabelSetOfAHeldSeries",
			kind:  measure.KindHistogram,
			track: []TrackOption{AsQuantile(0.5)},
			steps: []map[string][]uint64{
				{"k/h1": {0, 0, 0}},
				{"k/h1": {1, 1, 1}},                    // 0.5: the series' first point
				{"k/h1": {1, 1, 1}, "k/h2": {0, 0, 0}}, // h2's baseline
				{"k/h1": {1, 1, 1}, "k/h2": {0, 1, 1}}, // a median of 5e300, a gap
			},
			newest: map[string]float64{"wait_seconds": math.NaN()},
			want:   []string{"k/h1"}, gone: []string{"k/h2"},
		},
		{
			name: "heldLabelSetsPastTheirPoints",
			kind: measure.KindGauge,
			gauges: []map[string]float64{
				{"k/h1": 1, "k/h2": 1},
				{"k/h0": 1},
				{"k/h1": 6e299, "k/h2": 6e299}, // a sum of 1.2e300, a gap
				{"k/h1": 6e299, "k/h2": 6e299}, // h1's and h2's points have left the ring
			},
			newest: map[string]float64{"load": math.NaN()},
			want:   []string{"k/h0"}, gone: []string{"k/h1", "k/h2"},
		},
		{
			name:  "freedPlacesAtTheCap",
			kind:  measure.KindGauge,
			track: []TrackOption{By("key")},
			gauges: []map[string]float64{
				{"a/h1": 1, "a/h2": 1, "b/h1": 10}, // the three places
				{"b/h1": 10},
				{"b/h1": 10},
				// a's points have left the ring and a sums past 1e300: its two
				// places go to b's new label set, then to the new series c.
				{"a/h1": 6e299, "a/h2": 6e299, "b/h1": 10, "b/h2": 7, "c/h1": 3},
			},
			newest:   map[string]float64{"b": 17, "c": 3},
			want:     []string{"b/h1", "b/h2", "c/h1"},
			gone:     []string{"a/h1", "a/h2"},
			mostSets: 3,
		},
		{
			name:  "aGapAfterTakingAFreedPlace",
			kind:  measure.KindGauge,
			track: []TrackOption{By("key")},
			gauges: []map[string]float64{
				{"a/h1": 1, "a/h2": 1, "b/h1": 1},
				{"b/h1": 1},
				{"b/h1": 1},
				// a sums past 1e300; b/h2 takes a freed place and b sums past
				// 1e300 too, so neither takes a place and c takes one.
				{"a/h1": 6e299, "a/h2": 6e299, "b/h1": 6e299, "b/h2": 6e299, "c/h1": 3},
			},
			newest:   map[string]float64{"b": math.NaN(), "c": 3},
			want:     []string{"b/h1", "c/h1"},
			gone:     []string{"a/h1", "a/h2", "b/h2"},
			mostSets: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := []measure.Option{measure.WithExport(measure.ExportChartdata), measure.WithUnbounded("key", "host")}
			labels := []string{"key", "host"}
			var desc *measure.Desc
			if tc.kind == measure.KindHistogram {
				desc = measure.NewHistogramDesc("wait_seconds", "h", measure.UnitSeconds, labels, []float64{1, 1e301}, opts...)
			} else {
				desc = measure.NewGaugeDesc("load", "h", measure.UnitCount, labels, opts...)
			}
			at := 0
			reg := measure.NewRegistry("")
			reg.MustRegister(measure.NewCollector(func(_ context.Context, e *measure.Emitter) {
				if tc.kind == measure.KindHistogram {
					for _, set := range slices.Sorted(maps.Keys(tc.steps[at])) {
						key, host, _ := strings.Cut(set, "/")
						cum := tc.steps[at][set]
						e.EmitHistogram(desc, cum[:len(cum)-1], cum[len(cum)-1], 0, key, host)
					}
					return
				}
				for _, set := range slices.Sorted(maps.Keys(tc.gauges[at])) {
					key, host, _ := strings.Cut(set, "/")
					e.Emit(desc, tc.gauges[at][set], key, host)
				}
			}, desc))
			track := Sampled(desc, tc.track...)
			s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 3 * time.Minute}, track)
			if err != nil {
				t.Fatalf("NewSampler: %v", err)
			}
			if tc.mostSets > 0 {
				s.tracks[track].mostSets = tc.mostSets
			}
			for at = range max(len(tc.steps), len(tc.gauges)) {
				if _, _, err := s.tick(t.Context(), time.Unix(int64(at+1)*60, 0)); err != nil {
					t.Fatalf("tick: %v", err)
				}
			}
			f, err := s.Series(track, 3*time.Minute)
			if err != nil {
				t.Fatalf("Series: %v", err)
			}
			newest := map[string]float64{}
			for _, sr := range f.Series {
				newest[sr.Key] = plainValues(sr.Values)[len(sr.Values)-1]
			}
			if !maps.EqualFunc(newest, tc.newest, sameFloat) {
				t.Errorf("newest columns %v, want %v", newest, tc.newest)
			}
			places := s.tracks[track].places
			placed := func(set string) bool {
				key, host, _ := strings.Cut(set, "/")
				_, ok := places[labelSet([]measure.Label{{Name: "key", Value: key}, {Name: "host", Value: host}})]
				return ok
			}
			for _, set := range tc.want {
				if !placed(set) {
					t.Errorf("%s holds no place, want one", set)
				}
			}
			for _, set := range tc.gone {
				if placed(set) {
					t.Errorf("%s holds a place, want none", set)
				}
			}
		})
	}
}
