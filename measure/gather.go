package measure

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Type is a family's metric type, as the "# TYPE" line names it.
type Type string

// The metric types a [Family] can carry.
const (
	TypeCounter   Type = "counter"
	TypeGauge     Type = "gauge"
	TypeHistogram Type = "histogram"
	// TypeSummary is the type of go_gc_duration_seconds, the one runtime
	// summary every registry adds; no instrument or collector produces one.
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
// then the collectors' families, then the Go and process families sorted by
// name, in the order the page shows them. Unlike the page, it lists a
// declared family that has no series yet, with no samples, and every
// [ExportChartdata] family. It runs every collector with ctx, and returns
// every sample a collector dropped as one error naming the family; the
// families it returns hold what survived. A counter instrument's int64 value
// becomes a float64, exact up to 2^53.
func (r *Registry) Gather(ctx context.Context) ([]Family, error) {
	r.mu.RLock()
	fams := make([]Family, 0, r.instrumentCount()+r.collectorFamilies+len(processFamilyNames))
	for _, group := range r.groups {
		for _, m := range group {
			fams = append(fams, m.gather())
		}
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

// GatherOf reads only decls, each registered in r, as one [Family] each, in
// the order given and as [Registry.Gather] reads them, whatever their
// export: an instrument from its current values, a collector's [Desc] by
// running its collector with ctx, once for all the decls it serves. It runs
// no other collector and reads no Go or process family. It returns an error
// and no families for a nil declaration or one not registered in r, and
// otherwise the drops of the collectors it ran, as Gather does.
func (r *Registry) GatherOf(ctx context.Context, decls ...Declared) ([]Family, error) {
	fams, runs, err := r.gatherInstruments(decls)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, c := range runs {
		cf, err := c.run(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		for i, d := range decls {
			if desc, ok := d.(*Desc); ok && desc.owner.Load() == c {
				fams[i] = cf[slices.Index(c.descs, desc)]
			}
		}
	}
	return fams, errors.Join(errs...)
}

// gatherInstruments reads the instruments among decls into their places and
// returns the collectors the rest need, each once, after checking that r
// holds every declaration.
func (r *Registry) gatherInstruments(decls []Declared) ([]Family, []*Collector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fams := make([]Family, len(decls))
	var runs []*Collector
	for i, d := range decls {
		var meta *declMeta
		if d != nil {
			meta = d.declared()
		}
		if meta == nil {
			return nil, nil, errNilDeclared
		}
		if _, ok := r.members[meta]; !ok {
			return nil, nil, fmt.Errorf("measure: GatherOf: %q is not registered in this registry", meta.base)
		}
		switch m := d.(type) {
		case instrument:
			fams[i] = m.gather()
		case *Desc:
			if c := m.owner.Load(); !slices.Contains(runs, c) {
				runs = append(runs, c)
			}
		}
	}
	return fams, runs, nil
}

// Find returns the family named name in fams, as [Registry.Gather] or
// [Registry.GatherOf] returned them.
func Find(fams []Family, name string) (Family, bool) {
	for i := range fams {
		if fams[i].Name == name {
			return fams[i], true
		}
	}
	return Family{}, false
}

// Sample returns the series of f whose label values are labelValues, in the
// order f declares its labels.
func (f *Family) Sample(labelValues ...string) (Sample, bool) {
	for _, s := range f.Samples {
		if slices.EqualFunc(s.Labels, labelValues, func(l Label, v string) bool { return l.Value == v }) {
			return s, true
		}
	}
	return Sample{}, false
}

func (c *Counter) gather() Family {
	return scalarFamily(c.name, c.help, TypeCounter, float64(c.val.Load()))
}

func (g *Gauge) gather() Family {
	return scalarFamily(g.name, g.help, TypeGauge, g.Value())
}

func (h *Histogram) gather() Family {
	return Family{
		Name: h.name, Help: h.help, Type: TypeHistogram,
		Samples: []Sample{{Labels: []Label{}, Histogram: histogramValue(h)}},
	}
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

// processGather reads the Go and process families as structured values,
// sorted by name as client_golang's registry sorts them; the page renders
// this same list (processFamilies).
func processGather() []Family {
	var d processMetricsData
	collectProcessMetrics(&d)
	return processFamiliesOf(readGoRuntime(), &d)
}

// processFamiliesOf builds processGather's families from one read of the Go
// runtime and one of the platform's process readings; a family whose reading
// failed, or that the platform lacks, is left out.
func processFamiliesOf(g *goRuntime, d *processMetricsData) []Family {
	fams := g.families(make([]Family, 0, len(processFamilyNames)))
	fams = append(fams,
		scalarFamily(pmGCPauseTotal, helpGCPause, TypeCounter, g.gc.PauseTotal.Seconds()),
		scalarFamily(pmUptime, helpUptime, TypeGauge, d.uptime),
		scalarFamily(pmStartTime, helpStartTime, TypeGauge, d.startTime),
	)
	for _, o := range [...]struct {
		name, help string
		typ        Type
		ok         bool
		value      float64
	}{
		{pmCPUTotal, helpCPU, TypeCounter, d.hasCPU(), d.cpuSeconds},
		{pmResidentBytes, helpResident, TypeGauge, d.hasRSS(), float64(d.rss)},
		{pmOpenFDs, helpOpenFDs, TypeGauge, d.hasOpenFDs(), float64(d.openFDs)},
		{pmMaxFDs, helpMaxFDs, TypeGauge, d.hasMaxFDs(), d.maxFDs},
		{pmVirtualBytes, helpVirtual, TypeGauge, d.hasVirtual(), d.vsize},
		{pmVirtualMaxBytes, helpVirtualMax, TypeGauge, d.hasMaxVirtual(), d.maxVsize},
		{pmNetReceive, helpNetRx, TypeCounter, d.hasNetwork(), d.netRx},
		{pmNetTransmit, helpNetTx, TypeCounter, d.hasNetwork(), d.netTx},
	} {
		if o.ok {
			fams = append(fams, scalarFamily(o.name, o.help, o.typ, o.value))
		}
	}
	slices.SortFunc(fams, func(a, b Family) int { return strings.Compare(a.Name, b.Name) })
	return fams
}
