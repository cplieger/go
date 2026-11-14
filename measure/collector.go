package measure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Desc declares one family a [Collector] emits: its name, HELP text, type and
// label names. Build it with [NewGaugeDesc], [NewCounterDesc] or
// [NewHistogramDesc] and hand it to exactly one [NewCollector].
type Desc struct {
	err    error
	owner  atomic.Pointer[Collector] // claimed by NewCollector; nil until then
	name   string                    // prefixed by the registry at registration
	base   string                    // the declared name, never written after construction
	help   string
	typ    Type
	labels []string
	bounds []float64 // a histogram's bucket bounds; nil otherwise
}

// NewGaugeDesc declares a gauge family for a [Collector]. Unlike the
// instruments, a collector family takes any number of labels. An invalid
// metric name, or an invalid, reserved or duplicate label name, is captured and
// surfaces at [Registry.Register].
func NewGaugeDesc(name, help string, labels []string) *Desc {
	return newDesc(TypeGauge, name, help, labels)
}

// NewCounterDesc declares a counter family for a [Collector]. Its values are
// float64, because a collector reports a total it reads elsewhere. Per
// Prometheus convention the name should end in `_total`.
func NewCounterDesc(name, help string, labels []string) *Desc {
	return newDesc(TypeCounter, name, help, labels)
}

// NewHistogramDesc declares a histogram family for a [Collector], with the
// finite, strictly increasing bucket bounds its samples count against; the
// le="+Inf" bucket is implicit, and nil bounds leave only it. Emit its samples
// with [Emitter.EmitHistogram]. A bad bound, or the label name "le", which the
// buckets use, is captured and surfaces at [Registry.Register].
func NewHistogramDesc(name, help string, labels []string, bounds []float64) *Desc {
	d := newDesc(TypeHistogram, name, help, labels)
	if d.err == nil && slices.Contains(d.labels, "le") {
		d.err = fmt.Errorf(`measure: label name "le" for metric %q is reserved for the histogram bucket bound`, name)
	}
	if err := checkBuckets(name, bounds); d.err == nil && err != nil {
		d.err = err
	}
	d.bounds = slices.Clone(bounds)
	return d
}

func newDesc(typ Type, name, help string, labels []string) *Desc {
	err := checkMetricName(name)
	owned, lerr := checkLabelNames(name, labels)
	if err == nil {
		err = lerr
	}
	return &Desc{err: err, name: name, base: name, help: sanitizeHelp(name, help), typ: typ, labels: owned}
}

// Collector emits families whose values are read when Prometheus scrapes, not
// recorded as they happen. Build it with [NewCollector] and register it like
// any other [Metric].
type Collector struct {
	err        error
	collect    func(ctx context.Context, e *Emitter)
	active     map[problemKey]bool // problems the last scrape saw; guarded by mu
	logger     *slog.Logger        // the registry's WithLogger; set once at registration
	pending    []logEntry          // transitions not logged yet, in order; guarded by mu
	descs      []*Desc
	mu         sync.Mutex
	registered atomic.Bool
	logging    bool // a run is draining pending; guarded by mu
}

// NewCollector returns a collector that runs collect, with the request's
// context, on every scrape of [Registry.Handler] and every [Registry.Gather],
// and serves what it emits for descs. Concurrent scrapes run collect
// concurrently, each with its own [Emitter]. Construction errors surface at
// [Registry.Register]: no descs, a nil desc or collect, two descs with one
// name, a desc another collector holds or not built by a NewXDesc
// constructor, and any desc's own error.
func NewCollector(collect func(ctx context.Context, e *Emitter), descs ...*Desc) *Collector {
	c := &Collector{collect: collect, active: make(map[problemKey]bool)}
	c.err = c.adopt(descs)
	return c
}

// adopt claims descs for c and reports the first reason c cannot be registered.
// It reads only a desc's immutable fields before claiming it, and claims each
// with a compare-and-swap, so two collectors racing for one desc cannot both
// win and neither reads a name the other's registration is writing.
func (c *Collector) adopt(descs []*Desc) error {
	if c.collect == nil {
		return errors.New("measure: NewCollector called with a nil collect function")
	}
	if len(descs) == 0 {
		return errors.New("measure: NewCollector called with no descs")
	}
	seen := make(map[string]bool, len(descs))
	for _, d := range descs {
		switch {
		case d == nil:
			return errors.New("measure: NewCollector called with a nil desc")
		case d.typ != TypeGauge && d.typ != TypeCounter && d.typ != TypeHistogram:
			return errors.New("measure: desc was not built with NewGaugeDesc, NewCounterDesc or NewHistogramDesc")
		case d.err != nil:
			return d.err
		case seen[d.base]:
			return fmt.Errorf("measure: collector declares %q twice", d.base)
		}
		seen[d.base] = true
	}
	for i, d := range descs {
		if !d.owner.CompareAndSwap(nil, c) {
			for _, claimed := range descs[:i] {
				claimed.owner.Store(nil)
			}
			return fmt.Errorf("measure: desc %q already belongs to another collector", d.base)
		}
	}
	c.descs = slices.Clone(descs)
	return nil
}

func (c *Collector) registerInto(r *Registry) error {
	if c == nil {
		return errNilMetric
	}
	if r.err != nil {
		return r.err
	}
	if c.err != nil {
		return c.err
	}
	if c.collect == nil || len(c.descs) == 0 {
		return errors.New("measure: Collector was not built with NewCollector")
	}
	if !c.registered.CompareAndSwap(false, true) {
		return errors.New("measure: collector already registered")
	}
	c.mu.Lock()
	c.logger = r.logger
	c.mu.Unlock()
	names := make([]string, len(c.descs))
	for i, d := range c.descs {
		names[i] = r.prefixed(d.name)
		if err := r.reserveName(names[i], "collector family"); err != nil {
			for _, claimed := range names[:i] {
				delete(r.names, claimed)
			}
			c.registered.Store(false)
			return err
		}
	}
	for i, d := range c.descs {
		d.name = names[i]
	}
	r.collectors = append(r.collectors, c)
	r.collectorFamilies += len(c.descs)
	return nil
}

// Emitter records the samples of one run of a collector's collect function.
type Emitter struct {
	c        *Collector
	samples  map[*Desc]map[string]Sample // keyed by the label values joined with keySep
	problems map[problemKey]int          // dropped or warned samples per family and kind
	mu       sync.Mutex
	done     bool
}

// keySep joins label values into one map key; it cannot occur in valid UTF-8.
const keySep = "\xff"

// Emit records value for d, a gauge or counter family, with labelValues, one
// per label name d declares, in order. A desc this collector did not declare,
// a histogram desc, the wrong number of label values, a label set already
// emitted in this run (the first stands) and a negative or NaN counter value
// drop the sample: the scrape still answers, and [Registry.Gather] reports
// the drop. An Emit after collect returned records nothing. Label values are
// sanitized to valid UTF-8.
func (e *Emitter) Emit(d *Desc, value float64, labelValues ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepts(d, len(labelValues)) {
		return
	}
	switch {
	case d.typ == TypeHistogram:
		e.problems[problemKey{d.name, problemShape}]++
		return
	case d.typ == TypeCounter && (value < 0 || math.IsNaN(value)):
		e.problems[problemKey{d.name, problemCounterValue}]++
		return
	}
	e.record(d, Sample{Value: value}, labelValues)
}

// EmitHistogram records one series of d, a histogram family: buckets holds
// the cumulative count of observations at or below each of d's bounds, in
// order, count the total (the le="+Inf" bucket) and sum their sum. Buckets of
// the wrong length or falling, a count below the last bucket, a NaN sum and a
// desc that is not a histogram drop the sample, as do the problems [Emitter.Emit]
// drops for.
func (e *Emitter) EmitHistogram(d *Desc, buckets []uint64, count uint64, sum float64, labelValues ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepts(d, len(labelValues)) {
		return
	}
	if d.typ != TypeHistogram || !cumulative(buckets, len(d.bounds), count) || math.IsNaN(sum) {
		e.problems[problemKey{d.name, problemShape}]++
		return
	}
	v := &HistogramValue{Buckets: make([]Bucket, len(buckets)), Count: count, Sum: sum}
	for i, n := range buckets {
		v.Buckets[i] = Bucket{UpperBound: d.bounds[i], Count: n}
	}
	e.record(d, Sample{Histogram: v}, labelValues)
}

// cumulative reports whether buckets has n entries that never fall and end at
// or below count.
func cumulative(buckets []uint64, n int, count uint64) bool {
	if len(buckets) != n {
		return false
	}
	for i, b := range buckets {
		if (i > 0 && b < buckets[i-1]) || b > count {
			return false
		}
	}
	return true
}

// accepts reports whether a sample for d with n label values may be recorded
// in this run, counting the problem when not. e.mu is held.
func (e *Emitter) accepts(d *Desc, n int) bool {
	switch {
	case e.done:
		return false
	case d == nil || d.owner.Load() != e.c:
		// Another collector's desc may be renamed by its own registration right
		// now, so only its declared name is read.
		name := "<nil>"
		if d != nil {
			name = d.base
		}
		e.problems[problemKey{name, problemUndeclared}]++
		return false
	case n != len(d.labels):
		e.problems[problemKey{d.name, problemArity}]++
		return false
	}
	return true
}

// record stores s for d under labelValues, sanitized, unless this run already
// emitted that label set. e.mu is held.
func (e *Emitter) record(d *Desc, s Sample, labelValues []string) {
	labels := make([]Label, len(d.labels))
	vals := make([]string, len(d.labels))
	for i, v := range labelValues {
		san, changed := sanitizeUTF8(v)
		if changed {
			e.problems[problemKey{d.name, problemSanitized}]++
		}
		vals[i] = san
		labels[i] = Label{Name: d.labels[i], Value: san}
	}
	key := strings.Join(vals, keySep)
	fam := e.samples[d]
	if fam == nil {
		fam = make(map[string]Sample)
		e.samples[d] = fam
	}
	if _, dup := fam[key]; dup {
		e.problems[problemKey{d.name, problemDuplicate}]++
		return
	}
	s.Labels = labels
	fam[key] = s
}

// problemKind is one reason a collector sample was dropped or warned about.
type problemKind int

const (
	problemUndeclared problemKind = iota
	problemArity
	problemDuplicate
	problemCounterValue
	problemShape       // a sample that does not fit its family's type or buckets
	problemCardinality // a warning only: nothing is dropped
	problemSanitized   // a warning only: the value was kept with U+FFFD
)

// dropped reports whether the problem drops samples, which Gather returns as
// an error, or only warns.
func (k problemKind) dropped() bool { return k < problemCardinality }

func (k problemKind) String() string {
	switch k {
	case problemUndeclared:
		return "a desc this collector does not declare"
	case problemArity:
		return "the wrong number of label values"
	case problemDuplicate:
		return "a label set already emitted in this scrape"
	case problemCounterValue:
		return "a negative or NaN counter value"
	case problemShape:
		return "values that do not fit the family's type or buckets"
	case problemCardinality:
		return fmt.Sprintf("%d or more series", cardinalityWarnThreshold)
	case problemSanitized:
		return "label values with invalid UTF-8, replaced with U+FFFD"
	}
	return "unknown"
}

type problemKey struct {
	family string
	kind   problemKind
}

// run calls collect once and returns every declared family in declaration
// order, samples sorted by label values, with the error Gather reports.
func (c *Collector) run(ctx context.Context) ([]Family, error) {
	e := &Emitter{c: c, samples: make(map[*Desc]map[string]Sample), problems: make(map[problemKey]int)}
	c.collect(ctx, e)
	e.mu.Lock()
	e.done = true
	e.mu.Unlock()

	fams := make([]Family, 0, len(c.descs))
	for _, d := range c.descs {
		byKey := e.samples[d]
		if len(byKey) >= cardinalityWarnThreshold {
			e.problems[problemKey{d.name, problemCardinality}] = len(byKey)
		}
		samples := make([]Sample, 0, len(byKey))
		for _, s := range byKey {
			samples = append(samples, s)
		}
		slices.SortFunc(samples, compareLabelValues)
		fams = append(fams, Family{Name: d.name, Help: d.help, Type: d.typ, Samples: samples})
	}
	c.logTransitions(e.problems)

	var errs []error
	for k, n := range e.problems {
		if k.kind.dropped() {
			errs = append(errs, fmt.Errorf("measure: collector family %q: %d sample(s) dropped for %s", k.family, n, k.kind))
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return fams, errors.Join(errs...)
}

// logEntry is one transition logTransitions owes the log.
type logEntry struct {
	key     problemKey
	count   int
	cleared bool
}

func (e logEntry) log(l *slog.Logger) {
	l = orDefault(l)
	if e.cleared {
		l.Info("measure: collector problem cleared", "family", e.key.family, "problem", e.key.kind.String())
		return
	}
	l.Warn("measure: collector problem", "family", e.key.family, "problem", e.key.kind.String(), "count", e.count)
}

// logTransitions logs a problem once when a scrape first sees it and once
// when a later scrape no longer does, never on every scrape. Transitions queue
// under c.mu in the order they happen, and one run at a time drains the queue
// with c.mu released: concurrent scrapes log in state order, and a log handler
// that scrapes the registry only queues, so it cannot deadlock.
func (c *Collector) logTransitions(seen map[problemKey]int) {
	c.mu.Lock()
	for k, n := range seen {
		if !c.active[k] {
			c.active[k] = true
			c.pending = append(c.pending, logEntry{key: k, count: n})
		}
	}
	for k := range c.active {
		if _, still := seen[k]; !still {
			delete(c.active, k)
			c.pending = append(c.pending, logEntry{key: k, cleared: true})
		}
	}
	if c.logging || len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	c.logging = true
	logger := c.logger
	c.mu.Unlock()
	drained := false
	var rest []logEntry
	defer func() { // a panicking handler loses only its own entry and frees the queue
		if !drained {
			c.mu.Lock()
			c.pending = slices.Concat(rest, c.pending)
			c.logging = false
			c.mu.Unlock()
		}
	}()
	for {
		c.mu.Lock()
		batch := c.pending
		c.pending = nil
		if len(batch) == 0 {
			c.logging = false
			drained = true
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		for i, e := range batch {
			rest = batch[i+1:]
			e.log(logger)
		}
		rest = nil
	}
}

// compareLabelValues orders samples by their label values, as the labeled
// instruments order their series.
func compareLabelValues(a, b Sample) int {
	return slices.CompareFunc(a.Labels, b.Labels, func(x, y Label) int { return strings.Compare(x.Value, y.Value) })
}

// pageFamily renders a collector or runtime family into the exposition IR:
// labels sorted by name and escaped as the instruments' are. A summary or
// histogram series expands into its quantile or bucket lines, then _sum and
// _count. ok is false for a family with no samples, which the page omits.
func pageFamily(f *Family) (metricFamily, bool) {
	if len(f.Samples) == 0 {
		return metricFamily{}, false
	}
	typ := typeGauge
	switch f.Type {
	case TypeCounter:
		typ = typeCounter
	case TypeSummary:
		typ = typeSummary
	case TypeHistogram:
		typ = typeHistogram
	}
	samples := make([]sample, 0, len(f.Samples))
	for i := range f.Samples {
		s := &f.Samples[i]
		labels := renderLabels(s.Labels)
		switch f.Type {
		case TypeSummary:
			samples = summarySamples(samples, labels, s.Summary)
			continue
		case TypeHistogram:
			samples = histogramValueSamples(samples, labels, s.Histogram)
			continue
		}
		samples = append(samples, sample{labels: labels, value: formatValue(s.Value)})
	}
	return metricFamily{name: f.Name, typ: typ, help: f.Help, samples: samples}, true
}

// renderLabels builds the spec-escaped label content of one sample, sorted by
// label name. It sorts labels in place.
func renderLabels(labels []Label) string {
	slices.SortFunc(labels, func(a, b Label) int { return strings.Compare(a.Name, b.Name) })
	var sb strings.Builder
	for i, l := range labels {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(l.Name)
		sb.WriteString(`="`)
		_, _ = labelEscaper.WriteString(&sb, l.Value)
		sb.WriteByte('"')
	}
	return sb.String()
}
