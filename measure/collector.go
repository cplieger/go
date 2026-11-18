package measure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Desc declares one family a [Collector] emits: its name, HELP text, kind,
// unit and label names. Build it with [NewGaugeDesc], [NewCounterDesc],
// [NewHistogramDesc], [NewStateSetDesc] or [NewInfoDesc] and hand it to
// exactly one [NewCollector]. Its source is [SourceRead], so it is never
// seeded: it serves what the collector emits at each read. An
// [ExportChartdata] Desc may instead be registered on its own: it reserves
// its name, serves nothing, and [Registry.GatherOf] reads it with no samples.
type Desc struct {
	err    error
	owner  atomic.Pointer[Collector] // claimed by NewCollector; nil until then
	name   string                    // prefixed by the registry at registration
	help   string
	typ    Type
	labels []string
	bounds []float64 // a histogram's bucket bounds; nil otherwise
	meta   declMeta
}

// NewGaugeDesc declares a gauge family for a [Collector], its values in
// unit. Unlike the instruments, a collector family takes any number of
// labels. An invalid declaration is captured and surfaces at
// [Registry.Register].
func NewGaugeDesc(name, help string, unit Unit, labels []string, opts ...Option) *Desc {
	return newDesc(KindGauge, name, help, unit, labels, opts)
}

// NewCounterDesc declares a counter family for a [Collector], its values in
// unit. Its values are float64, because a collector reports a total it reads
// elsewhere. Per Prometheus convention the name should end in `_total`.
func NewCounterDesc(name, help string, unit Unit, labels []string, opts ...Option) *Desc {
	return newDesc(KindCounter, name, help, unit, labels, opts)
}

// NewHistogramDesc declares a histogram family for a [Collector], its
// observations in unit, with the finite, strictly increasing bucket bounds
// its samples count against; the le="+Inf" bucket is implicit, and nil
// bounds leave only it. Emit its samples with [Emitter.EmitHistogram]. A bad
// bound, or the label name "le", which the buckets use, is captured and
// surfaces at [Registry.Register].
func NewHistogramDesc(name, help string, unit Unit, labels []string, bounds []float64, opts ...Option) *Desc {
	d := newDesc(KindHistogram, name, help, unit, labels, opts)
	if d.err == nil && slices.Contains(d.labels, "le") {
		d.err = fmt.Errorf(`measure: label name "le" for metric %q is reserved for the histogram bucket bound`, name)
	}
	if err := checkBuckets(name, bounds); d.err == nil && err != nil {
		d.err = err
	}
	d.bounds = slices.Clone(bounds)
	d.meta.buckets = d.bounds
	return d
}

// NewStateSetDesc declares a state set family for a [Collector], as
// [NewStateSet] declares one: the last label is the state label, whose
// values are states. Emit its samples with [Emitter.EmitState]. The errors
// NewStateSet captures, other than seeding, are captured here too.
func NewStateSetDesc(name, help string, labels, states []string, opts ...Option) *Desc {
	d := newDesc(KindStateSet, name, help, UnitNone, labels, opts)
	d.meta.states = slices.Clone(states)
	if err := checkStates(name, d.labels, d.meta.states); d.err == nil && err != nil {
		d.err = err
	}
	return d
}

// NewInfoDesc declares an info family for a [Collector], as [NewInfo]
// declares one. Emit each of its label sets with value 1.
func NewInfoDesc(name, help string, labels []string, opts ...Option) *Desc {
	return newDesc(KindInfo, name, help, UnitNone, labels, opts)
}

func newDesc(kind Kind, name, help string, unit Unit, labels []string, opts []Option) *Desc {
	o := applyOptions(opts)
	help = sanitizeHelp(name, help)
	owned, lerr := checkLabelNames(name, labels)
	typ := TypeGauge
	switch kind {
	case KindCounter:
		typ = TypeCounter
	case KindHistogram:
		typ = TypeHistogram
	}
	d := &Desc{name: name, help: help, typ: typ, labels: owned}
	d.meta.fill(name, help, kind, unit, owned, &o)
	d.meta.source = SourceRead
	d.err = firstErr(checkMetricName(name), lerr, checkOptionFits(name, &o, false, -1),
		checkDeclaration(name, kind, unit, owned, &o))
	return d
}

// Declaration returns the family's metadata.
func (d *Desc) Declaration() Declaration { return d.meta.declaration() }

func (d *Desc) declared() *declMeta {
	if d == nil {
		return nil
	}
	return &d.meta
}

// Collector emits families whose values are read when Prometheus scrapes, not
// recorded as they happen. Build it with [NewCollector] and register it like
// any other [Metric].
type Collector struct {
	err        error
	collect    func(ctx context.Context, e *Emitter)
	active     map[problemKey]bool // problems the last scrape saw; guarded by mu
	logger     *slog.Logger        // the registry's WithLogger; set once at registration
	last       *keptRun            // the latest-started successful run; guarded by mu
	pending    []logEntry          // transitions not logged yet, in order; guarded by mu
	descs      []*Desc
	runs       atomic.Uint64 // runs started, numbering each
	mu         sync.Mutex
	registered atomic.Bool
	logging    bool // a run is draining pending; guarded by mu
}

// keptRun is what a successful run served, private to the collector, which
// a run that calls [Emitter.Keep] serves again.
type keptRun struct {
	problems map[problemKey]int
	fams     []Family
	seq      uint64
}

// NewCollector returns a collector that runs collect, with the request's
// context, on every scrape of [Registry.Handler], every [Registry.Gather] and
// every [Registry.GatherOf] that names one of its descs, and serves what it
// emits for descs. Concurrent reads run collect concurrently, each with its
// own [Emitter]. Construction errors surface at
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
		case d.meta.kind == "":
			return errors.New("measure: desc was not built by a NewXDesc constructor")
		case d.err != nil:
			return d.err
		case seen[d.meta.base]:
			return fmt.Errorf("measure: collector declares %q twice", d.meta.base)
		}
		seen[d.meta.base] = true
	}
	for i, d := range descs {
		if !d.owner.CompareAndSwap(nil, c) {
			for _, claimed := range descs[:i] {
				claimed.owner.Store(nil)
			}
			if d.owner.Load() == standalone {
				return fmt.Errorf("measure: desc %q is registered on its own", d.meta.base)
			}
			return fmt.Errorf("measure: desc %q already belongs to another collector", d.meta.base)
		}
	}
	c.descs = slices.Clone(descs)
	return nil
}

// standalone owns every Desc registered on its own, so no collector can
// adopt it and no registry can register it twice. It is never run.
var standalone = new(Collector)

func (d *Desc) registerInto(r *Registry) error {
	switch {
	case d == nil:
		return errNilMetric
	case r.err != nil:
		return r.err
	case d.meta.kind == "":
		return errors.New("measure: desc was not built by a NewXDesc constructor")
	case d.err != nil:
		return d.err
	case d.meta.export != ExportChartdata:
		return fmt.Errorf("measure: desc %q registered on its own serves nothing, so it must be ExportChartdata; "+
			"a desc served on the page belongs to a NewCollector", d.meta.base)
	}
	if !d.owner.CompareAndSwap(nil, standalone) {
		if d.owner.Load() == standalone {
			return fmt.Errorf("measure: desc %q already registered", d.meta.base)
		}
		return fmt.Errorf("measure: desc %q belongs to a collector; register the collector", d.meta.base)
	}
	name := r.prefixed(d.name)
	reserve := r.reserveName
	if d.typ == TypeHistogram {
		reserve = r.reserveHistogramFamily
	}
	if err := reserve(name, "desc"); err != nil {
		d.owner.Store(nil)
		return err
	}
	d.name = name
	d.meta.final.Store(&name)
	r.members[&d.meta] = struct{}{}
	r.descs = append(r.descs, d)
	return nil
}

// family is a Desc registered on its own as Gather reads it: no samples.
func (d *Desc) family() Family {
	return Family{Name: d.name, Help: d.help, Type: d.typ, Samples: []Sample{}}
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
		reserve := r.reserveName
		if d.typ == TypeHistogram {
			reserve = r.reserveHistogramFamily
		}
		if err := reserve(names[i], "collector family"); err != nil {
			for j, claimed := range names[:i] {
				r.releaseFamily(claimed, c.descs[j].typ == TypeHistogram)
			}
			c.registered.Store(false)
			return err
		}
	}
	for i, d := range c.descs {
		d.name = names[i]
		d.meta.final.Store(&names[i])
		r.members[&d.meta] = struct{}{}
	}
	r.collectors = append(r.collectors, c)
	r.collectorFamilies += len(c.descs)
	return nil
}

// Emitter records the samples of one run of a collector's collect function.
type Emitter struct {
	c        *Collector
	keptErr  error                       // the first Keep's cause
	samples  map[*Desc]map[string]Sample // keyed by the label values joined with keySep
	problems map[problemKey]int          // dropped or warned samples per family and kind
	mu       sync.Mutex
	done     bool
	kept     bool
}

// keySep joins label values into one map key; it cannot occur in valid UTF-8.
const keySep = "\xff"

// Emit records value for d, a gauge, counter or info family, with
// labelValues, one per label name d declares, in order. A desc this
// collector did not declare, a histogram or state set desc, the wrong number
// of label values, a label set already emitted in this run (the first
// stands), a negative or NaN counter value and an info value other than 1
// drop the sample: the scrape still answers, and [Registry.Gather] reports
// the drop. An Emit after collect returned records nothing. Label values are
// sanitized to valid UTF-8.
func (e *Emitter) Emit(d *Desc, value float64, labelValues ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepts(d, len(labelValues)) {
		return
	}
	switch kind := d.meta.kind; {
	case kind == KindHistogram || kind == KindStateSet || (kind == KindInfo && value != 1):
		e.problems[problemKey{d.name, problemShape}]++
		return
	case kind == KindCounter && (value < 0 || math.IsNaN(value)):
		e.problems[problemKey{d.name, problemCounterValue}]++
		return
	}
	e.record(d, Sample{Value: value}, labelValues)
}

// Keep is what a collect function whose own read failed (its store read, its
// snapshot) calls instead of emitting, so that a scrape never withholds a
// series an alert reads. The run serves the samples of the collector's
// latest successful run again, and nothing before its first, discarding
// what it emitted and anything emitted after; [Registry.Gather] reports no
// error for it, and the failure, with err, is logged once when it starts
// and once when a run succeeds again. A Keep after collect returned does
// nothing.
func (e *Emitter) Keep(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done || e.kept {
		return
	}
	e.kept, e.keptErr = true, err
}

// EmitState records the current state of one state set of d, a state set
// family: a sample of 1 for state and of 0 for each other state, with
// labelValues, one per label before the state label, in order. A state d
// does not declare, a desc that is not a state set and the problems
// [Emitter.Emit] drops for drop every sample of the set.
func (e *Emitter) EmitState(d *Desc, state string, labelValues ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepts(d, len(labelValues)+1) {
		return
	}
	if d.meta.kind != KindStateSet || !slices.Contains(d.meta.states, state) {
		e.problems[problemKey{d.name, problemShape}]++
		return
	}
	vals := append(slices.Clone(labelValues), state)
	if _, dup := e.samples[d][sanitizedKey(vals)]; dup {
		e.problems[problemKey{d.name, problemDuplicate}]++
		return
	}
	for _, st := range d.meta.states {
		value := 0.0
		if st == state {
			value = 1
		}
		vals[len(vals)-1] = st
		e.record(d, Sample{Value: value}, vals)
	}
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
			name = d.meta.base
		}
		e.problems[problemKey{name, problemUndeclared}]++
		return false
	case n != len(d.labels):
		e.problems[problemKey{d.name, problemArity}]++
		return false
	}
	return true
}

// sanitizedKey is the map key of labelValues once sanitized, as record
// stores it.
func sanitizedKey(labelValues []string) string {
	vals := make([]string, len(labelValues))
	for i, v := range labelValues {
		vals[i], _ = sanitizeUTF8(v)
	}
	return strings.Join(vals, keySep)
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
	problemKept        // a warning only: a run called Keep, keyed on the first desc
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
	case problemKept:
		return "a failed read, so the collector serves its last successful run's samples, none before its first"
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
	seq := c.runs.Add(1)
	e := &Emitter{c: c, samples: make(map[*Desc]map[string]Sample), problems: make(map[problemKey]int)}
	c.collect(ctx, e)
	e.mu.Lock()
	e.done = true
	kept, cause := e.kept, e.keptErr
	e.mu.Unlock()
	if kept {
		return c.serveKept(cause)
	}

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
	c.mu.Lock()
	if c.last == nil || seq > c.last.seq {
		c.last = &keptRun{seq: seq, fams: cloneFamilies(fams), problems: maps.Clone(e.problems)}
	}
	c.mu.Unlock()
	c.logTransitions(e.problems, nil)
	return fams, droppedErr(e.problems)
}

// serveKept answers a run that called Keep: a copy of the latest successful
// run's families and its drops, or every family empty before the first.
func (c *Collector) serveKept(cause error) ([]Family, error) {
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	problems := map[problemKey]int{}
	var fams []Family
	if last != nil {
		problems = maps.Clone(last.problems)
		fams = cloneFamilies(last.fams)
	} else {
		fams = make([]Family, len(c.descs))
		for i, d := range c.descs {
			fams[i] = Family{Name: d.name, Help: d.help, Type: d.typ, Samples: []Sample{}}
		}
	}
	problems[problemKey{c.descs[0].name, problemKept}] = 1
	c.logTransitions(problems, cause)
	return fams, droppedErr(problems)
}

// droppedErr joins one error per family and kind of problem that dropped
// samples, sorted.
func droppedErr(problems map[problemKey]int) error {
	var errs []error
	for k, n := range problems {
		if k.kind.dropped() {
			errs = append(errs, fmt.Errorf("measure: collector family %q: %d sample(s) dropped for %s", k.family, n, k.kind))
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(errs...)
}

// cloneFamilies copies fams deeply, so neither a caller nor the page's
// in-place label sort reaches the collector's kept copy.
func cloneFamilies(fams []Family) []Family {
	out := slices.Clone(fams)
	for i := range out {
		out[i].Samples = slices.Clone(out[i].Samples)
		for j := range out[i].Samples {
			s := &out[i].Samples[j]
			s.Labels = slices.Clone(s.Labels)
			if s.Histogram != nil {
				h := *s.Histogram
				h.Buckets = slices.Clone(h.Buckets)
				s.Histogram = &h
			}
		}
	}
	return out
}

// logEntry is one transition logTransitions owes the log.
type logEntry struct {
	cause   error // a kept run's Keep error
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
	if e.key.kind == problemKept {
		l.Warn("measure: collector problem", "family", e.key.family, "problem", e.key.kind.String(), "error", e.cause)
		return
	}
	l.Warn("measure: collector problem", "family", e.key.family, "problem", e.key.kind.String(), "count", e.count)
}

// logTransitions logs a problem once when a scrape first sees it and once
// when a later scrape no longer does, never on every scrape; a kept run's
// entry carries cause. Transitions queue
// under c.mu in the order they happen, and one run at a time drains the queue
// with c.mu released: concurrent scrapes log in state order, and a log handler
// that scrapes the registry only queues, so it cannot deadlock.
func (c *Collector) logTransitions(seen map[problemKey]int, cause error) {
	c.mu.Lock()
	for k, n := range seen {
		if !c.active[k] {
			c.active[k] = true
			c.pending = append(c.pending, logEntry{key: k, count: n, cause: cause})
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
