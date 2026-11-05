package measure

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file defines the metric-family intermediate representation (IR) and the
// Prometheus text-format encoder that materialises a scrape.
//
// The scrape pipeline is: collect the registry's metrics into a flat
// []metricFamily once (Registry.collect), then hand that slice to
// encodePrometheus (text format 0.0.4). Every value and label string in the IR
// is pre-rendered in its canonical form (formatValue for floats, %d for
// integers, buildLabelString for labels), so the encoder only assembles lines.

// Metric type discriminators used in the "# TYPE" line.
const (
	typeCounter   = "counter"
	typeGauge     = "gauge"
	typeHistogram = "histogram"
)

// metricFamily is one "# TYPE"/"# HELP" block: a metric name, its type, its
// HELP text (raw, escaped by the encoder), and the samples it emits.
type metricFamily struct {
	name    string
	typ     string
	help    string
	samples []sample
}

// sample is a single exposition line within a family. nameSuffix is appended to
// the family's series base name ("" for counters and gauges; "_bucket"/"_sum"/
// "_count" for histograms). labels is the pre-rendered, spec-escaped label
// content WITHOUT the surrounding braces — for histogram bucket samples it
// already includes the implicit le label ("" when the sample has no labels).
// value is the pre-rendered value token (formatValue output or a base-10
// integer).
type sample struct {
	nameSuffix string
	labels     string
	value      string
}

// family materialises an unlabeled counter into the IR. An unlabeled counter is
// always emitted.
func (c *Counter) family() metricFamily {
	return metricFamily{
		name:    c.name,
		typ:     typeCounter,
		help:    c.help,
		samples: []sample{{value: strconv.FormatInt(c.val.Load(), 10)}},
	}
}

// family materialises a labeled counter. ok is false when no label combination
// has been observed, matching the writers' "no keys, no output" behavior (no
// "# TYPE"/"# HELP" block is emitted for an empty labeled metric).
func (lc *LabeledCounter) family() (fam metricFamily, ok bool) {
	keys := lc.sortedLabelKeys(lc.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys))
	for i := range keys {
		key := &keys[i]
		lc.mu.RLock()
		v := lc.vals[*key]
		lc.mu.RUnlock()
		if v == nil {
			continue
		}
		samples = append(samples, sample{
			labels: buildLabelString(lc.labels, key),
			value:  strconv.FormatInt(v.Load(), 10),
		})
	}
	return metricFamily{name: lc.name, typ: typeCounter, help: lc.help, samples: samples}, len(samples) > 0
}

// family materialises an unlabeled gauge. An unlabeled gauge is always emitted.
func (g *Gauge) family() metricFamily {
	return metricFamily{
		name:    g.name,
		typ:     typeGauge,
		help:    g.help,
		samples: []sample{{value: formatValue(g.Get())}},
	}
}

// family materialises a labeled gauge. ok is false when no label combination
// has been set.
func (lg *LabeledGauge) family() (fam metricFamily, ok bool) {
	keys := lg.sortedLabelKeys(lg.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys))
	for i := range keys {
		key := &keys[i]
		lg.mu.RLock()
		ptr := lg.vals[*key]
		lg.mu.RUnlock()
		if ptr == nil {
			continue
		}
		samples = append(samples, sample{
			labels: buildLabelString(lg.labels, key),
			value:  formatValue(math.Float64frombits(ptr.Load())),
		})
	}
	return metricFamily{name: lg.name, typ: typeGauge, help: lg.help, samples: samples}, len(samples) > 0
}

// family materialises an unlabeled histogram. An unlabeled histogram is always
// emitted.
func (h *Histogram) family() metricFamily {
	return metricFamily{
		name:    h.name,
		typ:     typeHistogram,
		help:    h.help,
		samples: histogramSamples(h, ""),
	}
}

// family materialises a labeled histogram. ok is false when no label
// combination has been observed.
func (lh *LabeledHistogram) family() (fam metricFamily, ok bool) {
	keys := lh.sortedLabelKeys(lh.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys)*(len(lh.bounds)+3))
	for i := range keys {
		key := &keys[i]
		lh.mu.RLock()
		h := lh.vals[*key]
		lh.mu.RUnlock()
		if h == nil {
			continue
		}
		samples = append(samples, histogramSamples(h, buildLabelString(lh.labels, key))...)
	}
	return metricFamily{name: lh.name, typ: typeHistogram, help: lh.help, samples: samples}, len(samples) > 0
}

// histogramSamples expands one histogram into its cumulative bucket, sum, and
// count samples. labelStr is the pre-rendered user-label content (empty for an
// unlabeled histogram); each bucket sample's labels carry the implicit le label
// rendered with formatValue (a bare integer bound stays le="1"). The ordering —
// every finite bucket, then the +Inf bucket, then _sum, then _count — matches
// the writers and the exposition format.
func histogramSamples(h *Histogram, labelStr string) []sample {
	sum, count, bucketVals := h.snapshot()
	samples := make([]sample, 0, len(h.bounds)+3)
	for i, bound := range h.bounds {
		samples = append(samples, sample{
			nameSuffix: "_bucket",
			labels:     leLabels(labelStr, formatValue(bound)),
			value:      strconv.FormatInt(bucketVals[i], 10),
		})
	}
	samples = append(samples,
		sample{nameSuffix: "_bucket", labels: leLabels(labelStr, "+Inf"), value: strconv.FormatInt(bucketVals[len(h.bounds)], 10)},
		sample{nameSuffix: "_sum", labels: labelStr, value: formatValue(sum)},
		sample{nameSuffix: "_count", labels: labelStr, value: strconv.FormatInt(count, 10)},
	)
	return samples
}

// leLabels joins the user labels with the implicit le bucket label. With no
// user labels it yields just le="<bound>"; otherwise the le label follows the
// user labels, matching the writers' `{labels,le="..."}` rendering.
func leLabels(labelStr, le string) string {
	if labelStr == "" {
		return `le="` + le + `"`
	}
	return labelStr + `,le="` + le + `"`
}

// collect materialises the whole registry into a flat []metricFamily in the
// canonical scrape order (labeled counters, counters, labeled gauges, gauges,
// histograms, labeled histograms, collectors, then process metrics). Registry
// metrics are read under the registry read lock; collectors and process
// metrics run afterwards, outside the lock, because they call app code and
// perform /proc and runtime reads that must not block registration.
func (r *Registry) collect(ctx context.Context) []metricFamily {
	r.mu.RLock()
	fams := make([]metricFamily, 0,
		len(r.labeledCounters)+len(r.counters)+len(r.labeledGauges)+
			len(r.gauges)+len(r.histograms)+len(r.labeledHistograms)+len(processFamilyNames))
	for _, lc := range r.labeledCounters {
		if f, ok := lc.family(); ok {
			fams = append(fams, f)
		}
	}
	for _, c := range r.counters {
		fams = append(fams, c.family())
	}
	for _, lg := range r.labeledGauges {
		if f, ok := lg.family(); ok {
			fams = append(fams, f)
		}
	}
	for _, g := range r.gauges {
		fams = append(fams, g.family())
	}
	for _, h := range r.histograms {
		fams = append(fams, h.family())
	}
	for _, lh := range r.labeledHistograms {
		if f, ok := lh.family(); ok {
			fams = append(fams, f)
		}
	}
	collectors := r.collectors
	r.mu.RUnlock()
	fams = appendCollectorFamilies(ctx, fams, collectors)
	return append(fams, processFamilies()...)
}

// appendCollectorFamilies runs each collector and appends its non-empty
// families. The page serves what survived; run logs the drops.
func appendCollectorFamilies(ctx context.Context, fams []metricFamily, collectors []*Collector) []metricFamily {
	for _, c := range collectors {
		cf, _ := c.run(ctx)
		for i := range cf {
			if f, ok := pageFamily(&cf[i]); ok {
				fams = append(fams, f)
			}
		}
	}
	return fams
}

// encodePrometheus renders the IR in Prometheus text format 0.0.4: HELP before
// TYPE, HELP escaped without the double-quote, and the registered name used
// verbatim for both the family lines and the sample series.
func encodePrometheus(fams []metricFamily) string {
	var b strings.Builder
	appendPrometheus(&b, fams)
	return b.String()
}

func appendPrometheus(b *strings.Builder, fams []metricFamily) {
	for i := range fams {
		f := &fams[i]
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", f.name, helpEscaper.Replace(f.help), f.name, f.typ)
		for j := range f.samples {
			s := &f.samples[j]
			writeSample(b, f.name+s.nameSuffix, s.labels, s.value)
		}
	}
}

// writeSample writes one exposition line, omitting the brace group when the
// sample carries no labels.
func writeSample(b *strings.Builder, name, labels, value string) {
	if labels == "" {
		fmt.Fprintf(b, "%s %s\n", name, value)
		return
	}
	fmt.Fprintf(b, "%s{%s} %s\n", name, labels, value)
}
