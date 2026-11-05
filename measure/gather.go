package measure

import (
	"context"
	"errors"
	"math"
)

// Type is a family's metric type, as the "# TYPE" line names it.
type Type string

// The metric types a [Family] can carry.
const (
	TypeCounter   Type = "counter"
	TypeGauge     Type = "gauge"
	TypeHistogram Type = "histogram"
	// TypeSummary is reserved for runtime summaries the registry adds itself;
	// no instrument or collector produces one yet.
	TypeSummary Type = "summary"
)

// Family is one metric family as [Registry.Gather] reads it: structured
// values, never rendered exposition text.
type Family struct {
	Name, Help string
	Type       Type
	// Samples are sorted by label values. A declared family that has no
	// series yet has none.
	Samples []Sample
}

// Sample is one series of a [Family].
type Sample struct {
	// Histogram is set for a histogram family only.
	Histogram *HistogramValue
	// Summary is set for a summary family only.
	Summary *SummaryValue
	// Labels are in the order the family declares them.
	Labels []Label
	// Value is a counter's or gauge's value.
	Value float64
}

// Label is one label name and value.
type Label struct{ Name, Value string }

// HistogramValue is one histogram series.
type HistogramValue struct {
	// Buckets are cumulative and ascending, without the +Inf bucket, whose
	// count is Count.
	Buckets []Bucket
	Count   uint64
	Sum     float64
}

// Bucket is one cumulative histogram bucket.
type Bucket struct {
	UpperBound float64
	Count      uint64
}

// SummaryValue is one summary series.
type SummaryValue struct {
	Quantiles []Quantile
	Count     uint64
	Sum       float64
}

// Quantile is one quantile of a summary series.
type Quantile struct{ Quantile, Value float64 }

// Gather reads the whole registry as structured families: the instruments,
// then the collectors' families, then the process families, in the order the
// page shows them. Unlike the page, it lists a declared family that has no
// series yet, with no samples. It runs every collector with ctx, and returns
// every sample a collector dropped as one error naming the family; the
// families it returns hold what survived. A counter instrument's int64 value
// becomes a float64, exact up to 2^53.
func (r *Registry) Gather(ctx context.Context) ([]Family, error) {
	r.mu.RLock()
	fams := make([]Family, 0,
		len(r.labeledCounters)+len(r.counters)+len(r.labeledGauges)+
			len(r.gauges)+len(r.histograms)+len(r.labeledHistograms)+len(processFamilyNames))
	for _, lc := range r.labeledCounters {
		fams = append(fams, lc.gather())
	}
	for _, c := range r.counters {
		fams = append(fams, scalarFamily(c.name, c.help, TypeCounter, float64(c.val.Load())))
	}
	for _, lg := range r.labeledGauges {
		fams = append(fams, lg.gather())
	}
	for _, g := range r.gauges {
		fams = append(fams, scalarFamily(g.name, g.help, TypeGauge, g.Get()))
	}
	for _, h := range r.histograms {
		fams = append(fams, Family{
			Name: h.name, Help: h.help, Type: TypeHistogram,
			Samples: []Sample{{Labels: []Label{}, Histogram: histogramValue(h)}},
		})
	}
	for _, lh := range r.labeledHistograms {
		fams = append(fams, lh.gather())
	}
	collectors := r.collectors
	r.mu.RUnlock()

	var errs []error
	for _, c := range collectors {
		cf, err := c.run(ctx)
		fams = append(fams, cf...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	fams = append(fams, processGather()...)
	return fams, errors.Join(errs...)
}

func scalarFamily(name, help string, typ Type, v float64) Family {
	return Family{Name: name, Help: help, Type: typ, Samples: []Sample{{Labels: []Label{}, Value: v}}}
}

// labelsFor pairs declared label names with one series key, in declared order.
func labelsFor(names []string, key *labelKey) []Label {
	labels := make([]Label, len(names))
	for i, n := range names {
		labels[i] = Label{Name: n, Value: key[i]}
	}
	return labels
}

func (lc *LabeledCounter) gather() Family {
	keys := lc.sortedLabelKeys(lc.vals)
	samples := make([]Sample, 0, len(keys))
	for i := range keys {
		lc.mu.RLock()
		v := lc.vals[keys[i]]
		lc.mu.RUnlock()
		if v != nil {
			samples = append(samples, Sample{Labels: labelsFor(lc.labels, &keys[i]), Value: float64(v.Load())})
		}
	}
	return Family{Name: lc.name, Help: lc.help, Type: TypeCounter, Samples: samples}
}

func (lg *LabeledGauge) gather() Family {
	keys := lg.sortedLabelKeys(lg.vals)
	samples := make([]Sample, 0, len(keys))
	for i := range keys {
		lg.mu.RLock()
		p := lg.vals[keys[i]]
		lg.mu.RUnlock()
		if p != nil {
			samples = append(samples, Sample{Labels: labelsFor(lg.labels, &keys[i]), Value: math.Float64frombits(p.Load())})
		}
	}
	return Family{Name: lg.name, Help: lg.help, Type: TypeGauge, Samples: samples}
}

func (lh *LabeledHistogram) gather() Family {
	keys := lh.sortedLabelKeys(lh.vals)
	samples := make([]Sample, 0, len(keys))
	for i := range keys {
		lh.mu.RLock()
		h := lh.vals[keys[i]]
		lh.mu.RUnlock()
		if h != nil {
			samples = append(samples, Sample{Labels: labelsFor(lh.labels, &keys[i]), Histogram: histogramValue(h)})
		}
	}
	return Family{Name: lh.name, Help: lh.help, Type: TypeHistogram, Samples: samples}
}

// histogramValue reads one histogram series as cumulative buckets.
func histogramValue(h *Histogram) *HistogramValue {
	sum, count, bucketVals := h.snapshot()
	buckets := make([]Bucket, len(h.bounds))
	for i, bound := range h.bounds {
		buckets[i] = Bucket{UpperBound: bound, Count: uint64(max(bucketVals[i], 0))}
	}
	return &HistogramValue{Buckets: buckets, Count: uint64(max(count, 0)), Sum: sum}
}

// processGather reads the process families as structured values. It lists the
// same families, under the same conditions, as processFamilies.
func processGather() []Family {
	var d processMetricsData
	collectProcessMetrics(&d)
	fams := make([]Family, 0, len(processFamilyNames))
	fams = append(fams,
		scalarFamily(pmGoroutines, helpGoroutines, TypeGauge, float64(d.goroutines)),
		scalarFamily(pmHeapAllocBytes, helpHeapAlloc, TypeGauge, float64(d.heapAlloc)),
		scalarFamily(pmGCPauseTotal, helpGCPause, TypeCounter, d.gcPause),
		scalarFamily(pmUptime, helpUptime, TypeGauge, d.uptime),
		scalarFamily(pmStartTime, helpStartTime, TypeGauge, d.startTime),
	)
	if d.hasCPU() {
		fams = append(fams, scalarFamily(pmCPUTotal, helpCPU, TypeCounter, d.cpuSeconds))
	}
	if d.hasRSS() {
		fams = append(fams, scalarFamily(pmResidentBytes, helpResident, TypeGauge, float64(d.rss)))
	}
	if d.hasOpenFDs() {
		fams = append(fams, scalarFamily(pmOpenFDs, helpOpenFDs, TypeGauge, float64(d.openFDs)))
		if d.hasMaxFDs() {
			maxFDs := float64(d.maxFDs)
			if d.maxFDs == unlimitedMaxFDs {
				maxFDs = float64(math.MaxUint64)
			}
			fams = append(fams, scalarFamily(pmMaxFDs, helpMaxFDs, TypeGauge, maxFDs))
		}
	}
	return fams
}
