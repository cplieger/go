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
// label names. Build it with [NewGaugeDesc] or [NewCounterDesc] and hand it to
// exactly one [NewCollector].
type Desc struct {
	err    error
	owner  *Collector // set by NewCollector; nil until then
	name   string     // prefixed by the registry at registration
	base   string     // the declared name, never written after construction
	help   string
	typ    Type
	labels []string
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
	descs      []*Desc
	mu         sync.Mutex
	registered atomic.Bool
}

// NewCollector returns a collector that runs collect on every scrape of the
// registry's [Registry.Handler] and on every [Registry.Gather], with that
// request's context, and serves what collect emits for descs. Two concurrent
// scrapes run collect concurrently, each with its own [Emitter].
//
// No descs, a nil desc, a nil collect, two descs with one name, a desc another
// collector already holds, and any desc's construction error are captured and
// surface at [Registry.Register].
func NewCollector(collect func(ctx context.Context, e *Emitter), descs ...*Desc) *Collector {
	c := &Collector{collect: collect, active: make(map[problemKey]bool)}
	c.err = c.adopt(descs)
	return c
}

// adopt claims descs for c and reports the first reason c cannot be registered.
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
		case d.typ != TypeGauge && d.typ != TypeCounter:
			return errors.New("measure: desc was not built with NewGaugeDesc or NewCounterDesc")
		case d.err != nil:
			return d.err
		case seen[d.name]:
			return fmt.Errorf("measure: collector declares %q twice", d.name)
		case d.owner != nil:
			return fmt.Errorf("measure: desc %q already belongs to another collector", d.name)
		}
		seen[d.name] = true
	}
	for _, d := range descs {
		d.owner = c
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

// Emit records value for d with labelValues, one per label name d declares,
// in order. A desc this collector did not declare, the wrong number of label
// values, a label set already emitted in this run (the first stands) and a
// negative or NaN counter value drop the sample: the scrape still answers,
// and [Registry.Gather] reports the drop. An Emit after collect returned
// records nothing. Label values are sanitized to valid UTF-8.
func (e *Emitter) Emit(d *Desc, value float64, labelValues ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	switch {
	case d == nil || d.owner != e.c:
		// Another collector's desc may be renamed by its own registration right
		// now, so only its declared name is read.
		name := "<nil>"
		if d != nil {
			name = d.base
		}
		e.problems[problemKey{name, problemUndeclared}]++
		return
	case len(labelValues) != len(d.labels):
		e.problems[problemKey{d.name, problemArity}]++
		return
	case d.typ == TypeCounter && (value < 0 || math.IsNaN(value)):
		e.problems[problemKey{d.name, problemCounterValue}]++
		return
	}
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
	fam[key] = Sample{Labels: labels, Value: value}
}

// problemKind is one reason a collector sample was dropped or warned about.
type problemKind int

const (
	problemUndeclared problemKind = iota
	problemArity
	problemDuplicate
	problemCounterValue
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

// logTransitions logs a problem once when a scrape first sees it and once
// when a later scrape no longer does, never on every scrape. It logs after
// releasing c.mu, so a log handler that scrapes the registry cannot deadlock.
func (c *Collector) logTransitions(seen map[problemKey]int) {
	var appeared, cleared []problemKey
	c.mu.Lock()
	for k := range seen {
		if !c.active[k] {
			c.active[k] = true
			appeared = append(appeared, k)
		}
	}
	for k := range c.active {
		if _, still := seen[k]; !still {
			delete(c.active, k)
			cleared = append(cleared, k)
		}
	}
	c.mu.Unlock()
	for _, k := range appeared {
		slog.Warn("measure: collector problem", "family", k.family, "problem", k.kind.String(), "count", seen[k])
	}
	for _, k := range cleared {
		slog.Info("measure: collector problem cleared", "family", k.family, "problem", k.kind.String())
	}
}

// compareLabelValues orders samples by their label values, as the labeled
// instruments order their series.
func compareLabelValues(a, b Sample) int {
	return slices.CompareFunc(a.Labels, b.Labels, func(x, y Label) int { return strings.Compare(x.Value, y.Value) })
}

// pageFamily renders a collector family into the exposition IR: labels sorted
// by name and escaped as the instruments' are. ok is false for a family with
// no samples, which the page omits.
func pageFamily(f *Family) (metricFamily, bool) {
	if len(f.Samples) == 0 {
		return metricFamily{}, false
	}
	typ := typeGauge
	if f.Type == TypeCounter {
		typ = typeCounter
	}
	samples := make([]sample, len(f.Samples))
	for i := range f.Samples {
		samples[i] = sample{labels: renderLabels(f.Samples[i].Labels), value: formatValue(f.Samples[i].Value)}
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
