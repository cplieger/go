package measure

import (
	"math"
	"sync/atomic"
)

// Gauge is a value that can go up and down (float64).
type Gauge struct {
	err        error // construction-time validation error; surfaces at registration
	name       string
	help       string
	meta       declMeta
	bits       atomic.Uint64
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewGauge creates a named gauge whose value is in unit. An invalid
// declaration is captured into the gauge rather than panicking: the gauge
// records nothing, Handler never emits it, and the error surfaces at
// registration. A gauge that must leave the page is a [LabeledGauge] with
// no labels, whose Delete removes its one series.
func NewGauge(name, help string, unit Unit, opts ...Option) *Gauge {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	g := &Gauge{name: name, help: help}
	g.meta.fill(name, help, KindGauge, unit, nil, &o)
	g.err = firstErr(checkMetricName(name), checkOptionFits(name, &o, false, -1),
		checkDeclaration(name, KindGauge, unit, nil, &o))
	return g
}

// Declaration returns the gauge's metadata.
func (g *Gauge) Declaration() Declaration { return g.meta.declaration() }

func (g *Gauge) declared() *declMeta {
	if g == nil {
		return nil
	}
	return &g.meta
}

// Set sets the gauge to an arbitrary float64 value. A gauge carrying a
// construction error records nothing (one warning on the first drop).
func (g *Gauge) Set(v float64) {
	if g.err != nil {
		warnInertOnce(g.err, &g.warned, &g.name)
		return
	}
	g.bits.Store(math.Float64bits(v))
}

// addFloatBits atomically adds delta to the float64 stored as IEEE-754 bits
// in u via a compare-and-swap loop -- the package's canonical lock-free float
// accumulate, shared by Gauge.Add and Histogram.Observe's sum update.
func addFloatBits(u *atomic.Uint64, delta float64) {
	for {
		old := u.Load()
		if u.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+delta)) {
			return
		}
	}
}

// Add adds a float64 delta to the gauge. A gauge carrying a construction
// error records nothing (one warning on the first drop).
func (g *Gauge) Add(delta float64) {
	if g.err != nil {
		warnInertOnce(g.err, &g.warned, &g.name)
		return
	}
	addFloatBits(&g.bits, delta)
}

// Sub subtracts a float64 delta from the gauge.
func (g *Gauge) Sub(delta float64) { g.Add(-delta) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Value returns the current gauge value.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

// LabeledGauge tracks gauges per label combination.
type LabeledGauge struct {
	vals   map[labelKey]*atomic.Uint64
	help   string
	err    error // construction-time validation error; surfaces at registration
	labels []string
	meta   declMeta
	series
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewLabeledGauge creates a labeled gauge whose values are in unit.
// [WithSeed] creates label sets at 0. With no labels it holds at most one
// series, which Delete takes off the page. Construction through
// NewLabeledGauge is mandatory: the zero LabeledGauge has a nil series map
// and panics on the first record. An invalid declaration, including more
// than 8 labels, is captured into the gauge rather than panicking: the gauge
// records nothing and the error surfaces at registration.
func NewLabeledGauge(name, help string, unit Unit, labels []string, opts ...Option) *LabeledGauge {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	owned, err := checkNameAndLabels("LabeledGauge", name, labels)
	lg := &LabeledGauge{
		help:   help,
		labels: owned,
		vals:   make(map[labelKey]*atomic.Uint64),
	}
	lg.name = name
	lg.meta.fill(name, help, KindGauge, unit, owned, &o)
	lg.err = firstErr(err, checkOptionFits(name, &o, false, len(owned)),
		checkDeclaration(name, KindGauge, unit, owned, &o))
	if lg.err == nil {
		for _, set := range o.seeds {
			lg.Set(0, set...)
		}
	}
	return lg
}

// Declaration returns the gauge's metadata.
func (lg *LabeledGauge) Declaration() Declaration { return lg.meta.declaration() }

func (lg *LabeledGauge) declared() *declMeta {
	if lg == nil {
		return nil
	}
	return &lg.meta
}

// Set sets the gauge for the given label values. It panics on a label-arity
// mismatch. A gauge carrying a construction error records nothing (one
// warning on the first drop).
func (lg *LabeledGauge) Set(v float64, labelVals ...string) {
	if lg.err != nil {
		warnInertOnce(lg.err, &lg.warned, &lg.name)
		return
	}
	key := labelKeyFor(lg.labels, labelVals)
	if ptr, loaded := lg.loadOrStore(lg.vals, &key,
		func() *atomic.Uint64 { u := &atomic.Uint64{}; u.Store(math.Float64bits(v)); return u }); loaded {
		ptr.Store(math.Float64bits(v))
	}
}

// Reset removes all label combinations from the gauge.
func (lg *LabeledGauge) Reset() {
	lg.mu.Lock()
	clear(lg.vals)
	lg.mu.Unlock()
}

// Delete removes a single label combination from the gauge.
// It panics if the number of label values does not match the label count.
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created. A gauge carrying a construction error has no series, so
// Delete is a no-op.
func (lg *LabeledGauge) Delete(labelVals ...string) {
	if lg.err != nil {
		return
	}
	lg.deleteSeries(lg.vals, lg.labels, labelVals)
}
