package measure

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

// Unit is the unit a declaration's values are in, from a closed list.
// Values are in the base unit: seconds, never milliseconds.
type Unit string

// The units a declaration can take.
const (
	UnitSeconds Unit = "seconds"
	UnitBytes   Unit = "bytes"
	// UnitRatio is a share from 0 to 1.
	UnitRatio Unit = "ratio"
	UnitCount Unit = "count"
	// UnitNone is a plain number: a 0/1 condition, a state set, an info series
	// or a value in a unit the list does not hold.
	UnitNone Unit = "none"
)

// Kind is what a declaration's series hold.
type Kind string

// The kinds of declaration. A state set and an info series render as
// gauges, since text format 0.0.4 has no type for either.
const (
	KindCounter   Kind = "counter"
	KindGauge     Kind = "gauge"
	KindHistogram Kind = "histogram"
	// KindStateSet is one label set to 1 for the current state and 0 for the
	// others.
	KindStateSet Kind = "stateset"
	// KindInfo is a series whose labels carry the information, set to 1.
	KindInfo Kind = "info"
)

// Export says where a declaration's series are served.
type Export int

// The places a declaration's series can be served. The zero value is
// ExportMetric.
const (
	// ExportMetric serves the series on the /metrics page only.
	ExportMetric Export = iota
	// ExportChartdata never serves the series on the page; [Registry.Gather]
	// and [Registry.GatherOf] still read it, for charts.
	ExportChartdata
	// ExportBoth serves the series on the page and for charts.
	ExportBoth
)

// String returns "metric", "chartdata" or "both".
func (e Export) String() string {
	switch e {
	case ExportMetric:
		return "metric"
	case ExportChartdata:
		return "chartdata"
	case ExportBoth:
		return "both"
	}
	return fmt.Sprintf("Export(%d)", int(e))
}

// onPage reports whether the /metrics page serves the series.
func (e Export) onPage() bool { return e != ExportChartdata }

// Source says where a declaration's values come from.
type Source int

// The sources of a declaration's values.
const (
	// SourceRecorded is an instrument the program records into.
	SourceRecorded Source = iota
	// SourceRead is a [Desc] whose values a [Collector]'s function reads.
	SourceRead
)

// String returns "recorded" or "read".
func (s Source) String() string {
	if s == SourceRead {
		return "read"
	}
	return "recorded"
}

// Declaration is the metadata of one declared series, as
// [Declared.Declaration] returns it. Its slices are fresh copies.
type Declaration struct {
	// Name is the registered name, registry prefix included, or the declared
	// name while the declaration is not registered.
	Name string
	Help string
	Kind Kind
	Unit Unit
	// Labels are the label names in declared order. A state set's last
	// label is its state label.
	Labels []string
	// Unbounded are the labels declared with [WithUnbounded].
	Unbounded []string
	// Buckets are a histogram's finite bucket bounds, without +Inf.
	Buckets []float64
	// States are a state set's states, in declared order.
	States []string
	Export Export
	Source Source
}

// Declared is a declaration: one of this package's instruments or a
// collector's [Desc]. The interface is sealed.
type Declared interface {
	// Declaration returns the declaration's metadata.
	Declaration() Declaration
	declared() *declMeta
}

// declMeta is the part of a declaration every kind shares. Every field but
// final is fixed at construction.
type declMeta struct {
	final     atomic.Pointer[string] // the registered name; nil until registration
	base      string
	help      string
	kind      Kind
	unit      Unit
	labels    []string
	unbounded []string
	buckets   []float64
	states    []string
	export    Export
	source    Source
}

// fill records a recorded declaration's fixed fields; labels must be the
// caller's owned copy.
func (m *declMeta) fill(name, help string, kind Kind, unit Unit, labels []string, o *options) {
	m.base, m.help, m.kind, m.unit, m.labels = name, help, kind, unit, labels
	m.unbounded = slices.Clone(o.unbounded)
	m.export = o.export
}

// firstErr returns the first non-nil error, the one a construction captures.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *declMeta) declaration() Declaration {
	name := m.base
	if p := m.final.Load(); p != nil {
		name = *p
	}
	return Declaration{
		Name:      name,
		Help:      m.help,
		Kind:      m.kind,
		Unit:      m.unit,
		Labels:    slices.Clone(m.labels),
		Unbounded: slices.Clone(m.unbounded),
		Buckets:   slices.Clone(m.buckets),
		States:    slices.Clone(m.states),
		Export:    m.export,
		Source:    m.source,
	}
}

// Option configures a declaration. An option a declaration cannot take is a
// construction error, which surfaces at [Registry.Register].
type Option func(*options)

type options struct {
	buckets      []float64
	unbounded    []string
	seeds        [][]string
	export       Export
	bucketsSet   bool
	unboundedSet bool
	seedsSet     bool
}

// WithBuckets sets a histogram instrument's bucket bounds, a strictly
// increasing list of finite values; le="+Inf" is implicit. An empty list
// leaves only the +Inf bucket. Without it, a histogram uses
// [DefaultBuckets]. It is a construction error on any other declaration.
func WithBuckets(bounds []float64) Option {
	return func(o *options) {
		o.buckets = bounds
		o.bucketsSet = true
	}
}

// WithExport sets where the declaration's series are served; the default is
// [ExportMetric].
func WithExport(e Export) Option {
	return func(o *options) { o.export = e }
}

// WithUnbounded declares labels whose values have no bound the program
// controls, such as a name a user types. Only an [ExportChartdata]
// declaration may carry one: on a declaration served on the page, and for a
// label the declaration does not have, it is a construction error.
func WithUnbounded(labels ...string) Option {
	return func(o *options) {
		o.unbounded = slices.Clone(labels)
		o.unboundedSet = true
	}
}

// WithSeed creates each label set at 0 when the instrument is built, so an
// increase() rule sees a series before its first event. Each set holds one
// value per label name in declared order; a state set's sets leave out its
// state label and start with every state at 0. Seed the values known at
// start, from configuration or an enum; a value that appears later is
// recorded by the code that adds it. It is a construction error on an
// unlabeled instrument, an info series and a collector's [Desc], whose
// series a read function produces.
func WithSeed(labelSets ...[]string) Option {
	return func(o *options) {
		o.seeds = make([][]string, len(labelSets))
		for i, s := range labelSets {
			o.seeds[i] = slices.Clone(s)
		}
		o.seedsSet = true
	}
}

func applyOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// unitSuffixes maps each base-unit name suffix to the unit it states.
var unitSuffixes = [...]struct {
	suffix string
	unit   Unit
}{
	{"_seconds", UnitSeconds},
	{"_bytes", UnitBytes},
	{"_ratio", UnitRatio},
}

// checkDeclaration refuses a declaration that contradicts itself: a unit off
// the list, a name suffix that disagrees with the kind or unit, an export off
// the list, or an unbounded label that is undeclared or served on the page.
func checkDeclaration(name string, kind Kind, unit Unit, labels []string, o *options) error {
	switch unit {
	case UnitSeconds, UnitBytes, UnitRatio, UnitCount, UnitNone:
	case "":
		return fmt.Errorf("measure: metric %q declares no unit", name)
	default:
		return fmt.Errorf("measure: metric %q declares unit %q, which is not seconds, bytes, ratio, count or none", name, unit)
	}
	if err := checkSuffix(name, kind, unit); err != nil {
		return err
	}
	if o.export < ExportMetric || o.export > ExportBoth {
		return fmt.Errorf("measure: metric %q declares export %v, which is not metric, chartdata or both", name, o.export)
	}
	for _, l := range o.unbounded {
		if !slices.Contains(labels, l) {
			return fmt.Errorf("measure: metric %q declares unbounded label %q, which is not one of its labels", name, l)
		}
		if o.export.onPage() {
			return fmt.Errorf("measure: metric %q declares unbounded label %q and is served on /metrics (export %v); "+
				"only an ExportChartdata series may carry an unbounded label", name, l, o.export)
		}
	}
	return nil
}

// checkSuffix refuses a name whose suffix contradicts the declaration, per
// https://prometheus.io/docs/practices/naming/: _total names a counter, and a
// base-unit suffix (before any _total) names its unit. A name with no such
// suffix contradicts nothing.
func checkSuffix(name string, kind Kind, unit Unit) error {
	base, total := strings.CutSuffix(name, "_total")
	if total && kind != KindCounter {
		return fmt.Errorf(`measure: metric %q ends in "_total" but declares kind %q; only a counter may`, name, kind)
	}
	for _, s := range unitSuffixes {
		if !strings.HasSuffix(base, s.suffix) || unit == s.unit {
			continue
		}
		if strings.HasSuffix(base, "_timestamp_seconds") {
			return fmt.Errorf(`measure: metric %q ends in "_timestamp_seconds" but declares unit %q; a timestamp is in seconds`, name, unit)
		}
		return fmt.Errorf("measure: metric %q ends in %q but declares unit %q", name, s.suffix, unit)
	}
	return nil
}

// checkOptionFits refuses the options a declaration of this shape cannot
// take. seedArity is the number of values a seed set holds, or -1 when the
// declaration takes no seed.
func checkOptionFits(name string, o *options, histogram bool, seedArity int) error {
	switch {
	case o.bucketsSet && !histogram:
		return fmt.Errorf("measure: WithBuckets on metric %q, which is not a histogram instrument", name)
	case o.seedsSet && seedArity < 0:
		return fmt.Errorf("measure: WithSeed on metric %q, which takes no seed", name)
	}
	for _, s := range o.seeds {
		if len(s) != seedArity {
			return fmt.Errorf("measure: WithSeed on metric %q gives %d label values, want %d", name, len(s), seedArity)
		}
	}
	return nil
}

// errNilDeclared is returned for a nil declaration handed to GatherOf.
var errNilDeclared = errors.New("measure: GatherOf called with a nil declaration")
