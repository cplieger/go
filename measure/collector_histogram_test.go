package measure

import (
	"context"
	"math"
	"strings"
	"testing"
)

// The expected page follows the text exposition format: each finite bucket,
// then le="+Inf" carrying the count, then _sum and _count, with the user
// labels before le.
func TestCollectorServesAHistogramFamily(t *testing.T) {
	r := NewRegistry("app")
	d := NewHistogramDesc("start_seconds", "Start latency", []string{"host", "call"}, []float64{0.5, 2})
	r.MustRegister(staticCollector(func(e *Emitter) {
		e.EmitHistogram(d, []uint64{1, 3}, 4, 7.25, "nas", "start")
	}, d))

	want := "# HELP app_start_seconds Start latency\n# TYPE app_start_seconds histogram\n" +
		"app_start_seconds_bucket{call=\"start\",host=\"nas\",le=\"0.5\"} 1\n" +
		"app_start_seconds_bucket{call=\"start\",host=\"nas\",le=\"2\"} 3\n" +
		"app_start_seconds_bucket{call=\"start\",host=\"nas\",le=\"+Inf\"} 4\n" +
		"app_start_seconds_sum{call=\"start\",host=\"nas\"} 7.25\n" +
		"app_start_seconds_count{call=\"start\",host=\"nas\"} 4\n"
	if out := body(t, r); !strings.Contains(out, want) {
		t.Errorf("page lacks the histogram family:\n%s\nwant:\n%s", out, want)
	}

	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantFam := Family{Name: "app_start_seconds", Help: "Start latency", Type: TypeHistogram, Samples: []Sample{{
		Labels:    []Label{{"host", "nas"}, {"call", "start"}},
		Histogram: &HistogramValue{Buckets: []Bucket{{0.5, 1}, {2, 3}}, Count: 4, Sum: 7.25},
	}}}
	if got := findFamily(t, fams, "app_start_seconds"); !familyEqual(got, wantFam) {
		t.Errorf("Gather = %s, want %s", describeFamily(got), describeFamily(wantFam))
	}
}

func TestCollectorHistogramWithNoBoundsHasOnlyTheInfBucket(t *testing.T) {
	r := NewRegistry("")
	d := NewHistogramDesc("h", "h", nil, nil)
	r.MustRegister(staticCollector(func(e *Emitter) { e.EmitHistogram(d, nil, 2, 1.5) }, d))
	want := "h_bucket{le=\"+Inf\"} 2\nh_sum 1.5\nh_count 2\n"
	if out := body(t, r); !strings.Contains(out, want) {
		t.Errorf("page:\n%s\nwant:\n%s", out, want)
	}
}

func TestHistogramDescConstructionErrorsSurfaceAtRegister(t *testing.T) {
	for name, tc := range map[string]struct {
		d    *Desc
		want string
	}{
		"le label":            {NewHistogramDesc("h", "h", []string{"le"}, nil), `"le"`},
		"unsorted bounds":     {NewHistogramDesc("h", "h", nil, []float64{2, 1}), "strictly increasing"},
		"infinite bound":      {NewHistogramDesc("h", "h", nil, []float64{1, math.Inf(1)}), "finite"},
		"invalid metric name": {NewHistogramDesc("1h", "h", nil, nil), "1h"},
		"reserved label":      {NewHistogramDesc("h", "h", []string{"__x"}, nil), "reserved"},
	} {
		t.Run(name, func(t *testing.T) {
			err := NewRegistry("").Register(NewCollector(func(context.Context, *Emitter) {}, tc.d))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Register = %v, want an error naming %s", err, tc.want)
			}
		})
	}
}

// A caller's slice changed after the call cannot change the family's bounds.
func TestHistogramDescOwnsItsBounds(t *testing.T) {
	bounds := []float64{1, 2}
	d := NewHistogramDesc("h", "h", nil, bounds)
	bounds[0] = 5
	r := NewRegistry("")
	r.MustRegister(staticCollector(func(e *Emitter) { e.EmitHistogram(d, []uint64{1, 1}, 1, 1) }, d))
	if out := body(t, r); !strings.Contains(out, "h_bucket{le=\"1\"} 1\n") {
		t.Errorf("page:\n%s\nwant the bound declared at the call", out)
	}
}

func TestCollectorDropsMalformedHistogramSamples(t *testing.T) {
	for name, tc := range map[string]struct {
		emit func(e *Emitter, h, g *Desc)
		want string
	}{
		"bucket count":         {func(e *Emitter, h, _ *Desc) { e.EmitHistogram(h, []uint64{1}, 1, 1, "a") }, "do not fit"},
		"falling buckets":      {func(e *Emitter, h, _ *Desc) { e.EmitHistogram(h, []uint64{3, 2}, 3, 1, "a") }, "do not fit"},
		"count below a bucket": {func(e *Emitter, h, _ *Desc) { e.EmitHistogram(h, []uint64{1, 3}, 2, 1, "a") }, "do not fit"},
		"NaN sum":              {func(e *Emitter, h, _ *Desc) { e.EmitHistogram(h, []uint64{1, 1}, 1, math.NaN(), "a") }, "do not fit"},
		"value for histogram":  {func(e *Emitter, h, _ *Desc) { e.Emit(h, 1, "a") }, "do not fit"},
		"histogram for gauge":  {func(e *Emitter, _, g *Desc) { e.EmitHistogram(g, nil, 1, 1) }, "do not fit"},
		"label count":          {func(e *Emitter, h, _ *Desc) { e.EmitHistogram(h, []uint64{1, 1}, 1, 1) }, "wrong number of label values"},
		"duplicate labels": {func(e *Emitter, h, _ *Desc) {
			e.EmitHistogram(h, []uint64{1, 1}, 1, 1, "a")
			e.EmitHistogram(h, []uint64{2, 2}, 2, 2, "a")
		}, "already emitted"},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewHistogramDesc("h", "h", []string{"k"}, []float64{1, 2})
			g := NewGaugeDesc("g", "h", nil)
			r := NewRegistry("")
			r.MustRegister(staticCollector(func(e *Emitter) { tc.emit(e, h, g) }, h, g))
			fams, err := r.Gather(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Gather error = %v, want one naming %q", err, tc.want)
			}
			f := findFamily(t, fams, "h")
			if name == "duplicate labels" {
				if len(f.Samples) != 1 || f.Samples[0].Histogram.Count != 1 {
					t.Errorf("h = %s, want the first sample to stand", describeFamily(f))
				}
				return
			}
			if len(f.Samples) != 0 || len(findFamily(t, fams, "g").Samples) != 0 {
				t.Errorf("h = %s, want the malformed sample dropped", describeFamily(f))
			}
		})
	}
}

// Equal bucket counts are an empty bucket, which the format allows.
func TestCollectorKeepsEmptyBucketsAndANegativeSum(t *testing.T) {
	r := NewRegistry("")
	d := NewHistogramDesc("h", "h", nil, []float64{1, 2})
	r.MustRegister(staticCollector(func(e *Emitter) { e.EmitHistogram(d, []uint64{0, 0}, 0, -0.5) }, d))
	fams, err := r.Gather(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f := findFamily(t, fams, "h"); len(f.Samples) != 1 || f.Samples[0].Histogram.Sum != -0.5 {
		t.Errorf("h = %s, want one sample with sum -0.5", describeFamily(f))
	}
}
