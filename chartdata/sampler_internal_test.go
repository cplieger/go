package chartdata

import (
	"math"
	"slices"
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
// column, since it covers several steps.
func TestSamplerStampsALateReadAtTheEdgeItIsTakenAt(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	runs := measure.NewCounter("runs_total", "h", measure.UnitCount)
	reg.MustRegister(runs)
	track := Sampled(runs)
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 10 * time.Minute}, track)
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	minute := func(m int64) time.Time { return time.Unix(m*60, 0) }
	take := func(now time.Time) []FrameEvent {
		t.Helper()
		next := s.after(now)
		events, _, err := s.tick(t.Context(), s.due(next, next))
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		return events
	}
	runs.Add(1)
	take(minute(100).Add(30 * time.Second)) // the column at 101
	runs.Add(1)
	take(minute(101).Add(30 * time.Second)) // 102
	next := s.after(minute(102).Add(30 * time.Second))
	if !next.Equal(minute(103)) {
		t.Fatalf("the timer is set for %d, want 103", next.Unix()/60)
	}
	runs.Add(120)
	woke := minute(108).Add(30 * time.Second)
	if got := s.due(next, woke); !got.Equal(minute(108)) {
		t.Errorf("due(103, 108:30) = %d, want the latest edge passed, 108", got.Unix()/60)
	}
	if got := s.due(next, next.Add(time.Second)); !got.Equal(next) {
		t.Errorf("due on time = %d, want 103", got.Unix()/60)
	}
	events, _, err := s.tick(t.Context(), s.due(next, woke))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
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
		t.Errorf("the late tick sent %d events, want an append for each of 103 to 108, 6", len(events))
	}
	runs.Add(4)
	take(minute(108).Add(30 * time.Second))
	f, err = s.Series(track, time.Minute)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if got := plainValues(f.Series[0].Values); len(got) != 1 || got[0] != 4 {
		t.Errorf("the column after the pause = %v, want [4]", got)
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

// A series read at a step that gives it no value, such as the first after
// missed edges, keeps its ring even when the ring then holds only gaps, so
// the page keeps getting appends instead of a set without it.
func TestSamplerKeepsASeriesReadWithNoValue(t *testing.T) {
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
	tick := func(minute int64) []FrameEvent {
		t.Helper()
		runs.Add(1)
		wait.Observe(0.1)
		events, _, err := s.tick(t.Context(), time.Unix(minute*60, 0))
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		return events
	}
	tick(1)
	tick(2) // both series take their first value
	for _, minute := range []int64{10, 11} {
		for _, e := range tick(minute) {
			if e.Kind != KindAppend || len(e.Values) != 1 {
				t.Errorf("minute %d: %s event of %s with %d values, want appends of one series", minute, e.Kind, e.Key, len(e.Values))
			}
		}
	}
}

func sameFloat(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }
