package chartdata

import (
	"log/slog"
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
		if _, err := s.tick(t.Context(), time.Unix(edge, 0)); err != nil {
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

// When a set event is refused, the page holds the series of the last set it
// got, so the sampler must send no append, whose values follow the ring's
// series, until a set goes out.
func TestSamplerSendsNoAppendAfterARefusedSet(t *testing.T) {
	t.Parallel()
	reg := measure.NewRegistry("")
	up := measure.NewLabeledGauge("up", "h", measure.UnitNone, []string{"host"})
	reg.MustRegister(up)
	s, err := NewSampler(Sampling{Registry: reg, Every: time.Minute, Keep: 3 * time.Minute, Logger: slog.New(slog.DiscardHandler)},
		Sampled(up, By("host")))
	if err != nil {
		t.Fatalf("NewSampler: %v", err)
	}
	ts := s.order[0]
	minute := int64(0)
	tick := func() []FrameEvent {
		t.Helper()
		minute++
		events, err := s.tick(t.Context(), time.Unix(minute*60, 0))
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		return events
	}
	kinds := func(events []FrameEvent) []FrameEventKind {
		var out []FrameEventKind
		for _, e := range events {
			if e.Kind != KindReset {
				out = append(out, e.Kind)
			}
		}
		return out
	}
	up.Set(1, "a")
	tick()
	// A value no constructor takes, which the sampler never stores itself,
	// makes every set refused while it is in the ring.
	ts.values["a"].buf[0] = math.Inf(1)
	up.Set(1, "b")
	for i, want := range [][]FrameEventKind{
		nil,          // b is new: the set is refused
		nil,          // same series, but the page lacks the last set
		{KindSet},    // the bad column has left the 3-minute ring
		{KindAppend}, // the page has the set
	} {
		if got := kinds(tick()); !slices.Equal(got, want) {
			t.Errorf("step %d's events = %v, want %v", i+2, got, want)
		}
	}
}
