package measure

import (
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBuckets returns the default histogram bucket boundaries (HTTP
// latency). Each call returns a fresh slice, so callers cannot mutate the
// defaults process-wide.
func DefaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}
}

// APIBuckets returns coarse latency buckets (seconds) for outbound API calls
// and slow collect/scan cycles, where DefaultBuckets (max 1.0s) saturates at
// +Inf. Each call returns a fresh slice, so callers cannot mutate the
// boundaries process-wide.
func APIBuckets() []float64 {
	return []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
}

// checkBuckets requires finite, strictly increasing bounds, since le="+Inf"
// is implicit and a duplicate or falling bound makes a parser reject the
// whole scrape. The error names metric, so a MustRegister of many metrics
// says which one. An empty set is valid: only the +Inf bucket.
func checkBuckets(metric string, bounds []float64) error {
	for i, b := range bounds {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			return fmt.Errorf("measure: histogram bucket bound for metric %q must be finite, got %v", metric, b)
		}
		if i > 0 && b <= bounds[i-1] {
			return fmt.Errorf("measure: histogram bucket bounds for metric %q must be strictly increasing, got %v after %v",
				metric, b, bounds[i-1])
		}
	}
	return nil
}

// resolveBuckets returns an owned copy of the bounds WithBuckets set, or
// DefaultBuckets without it. On a bucket-contract violation it returns nil
// bounds and the error (naming metric) for the constructor to capture.
func resolveBuckets(metric string, o *options) ([]float64, error) {
	bounds := o.buckets
	if !o.bucketsSet {
		bounds = DefaultBuckets()
	}
	if err := checkBuckets(metric, bounds); err != nil {
		return nil, err
	}
	return slices.Clone(bounds), nil
}

// Histogram tracks a distribution using cumulative buckets and atomic CAS for sum.
type Histogram struct {
	name       string
	help       string
	err        error // construction-time validation error; surfaces at registration
	bounds     []float64
	buckets    []atomic.Int64
	meta       declMeta
	sumBits    atomic.Uint64
	count      atomic.Int64
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
	// mu is used with inverted RWMutex semantics: Observe holds RLock (writers
	// mutate sum/count/buckets via atomics, so they run concurrently), while
	// snapshot holds the exclusive Lock to read a consistent view with no
	// Observe in flight. Do not swap these: a Lock in Observe serializes the
	// hot path; an RLock in snapshot reintroduces torn count/bucket reads.
	mu sync.RWMutex
}

// NewHistogram creates a histogram whose observations are in unit. By
// default it uses DefaultBuckets; use WithBuckets to override. Construction
// through NewHistogram is mandatory: the zero Histogram has no bucket slots,
// so Observe on it panics with an index out of range. An invalid declaration
// or a bucket-contract violation (non-finite, duplicate, or out-of-order
// bounds) is captured into the histogram rather than panicking: the
// histogram records nothing and the error surfaces at registration.
func NewHistogram(name, help string, unit Unit, opts ...Option) *Histogram {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	bounds, berr := resolveBuckets(name, &o)
	h := &Histogram{
		name:    name,
		help:    help,
		bounds:  bounds,
		buckets: make([]atomic.Int64, len(bounds)+1),
	}
	h.meta.fill(name, help, KindHistogram, unit, nil, &o)
	h.meta.buckets = bounds
	h.err = firstErr(checkMetricName(name), berr, checkOptionFits(name, &o, true, -1),
		checkDeclaration(name, KindHistogram, unit, nil, &o))
	return h
}

// Declaration returns the histogram's metadata.
func (h *Histogram) Declaration() Declaration { return h.meta.declaration() }

func (h *Histogram) declared() *declMeta {
	if h == nil {
		return nil
	}
	return &h.meta
}

// Observe records a value, in the histogram's unit. A histogram carrying a
// construction error records nothing (one warning on the first drop).
func (h *Histogram) Observe(v float64) {
	if h.err != nil {
		warnInertOnce(h.err, &h.warned, &h.name)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	addFloatBits(&h.sumBits, v)
	h.count.Add(1)
	for i, bound := range h.bounds {
		if v <= bound {
			for j := i; j < len(h.bounds); j++ {
				h.buckets[j].Add(1)
			}
			break
		}
	}
	h.buckets[len(h.bounds)].Add(1) // +Inf
}

// snapshot atomically reads sum, count, and per-bucket counts under the
// histogram lock. Shared by the histogram family builders and writers.
func (h *Histogram) snapshot() (sum float64, count int64, bucketVals []int64) {
	h.mu.Lock()
	sum = math.Float64frombits(h.sumBits.Load())
	count = h.count.Load()
	bucketVals = make([]int64, len(h.buckets))
	for i := range h.buckets {
		bucketVals[i] = h.buckets[i].Load()
	}
	h.mu.Unlock()
	return sum, count, bucketVals
}

// LabeledHistogram tracks histograms per label combination.
type LabeledHistogram struct {
	vals   map[labelKey]*Histogram
	help   string
	err    error // construction-time validation error; surfaces at registration
	bounds []float64
	labels []string
	meta   declMeta
	series
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewLabeledHistogram creates a labeled histogram with the given label
// names, its observations in unit. It uses DefaultBuckets unless WithBuckets
// sets others, and [WithSeed] creates label sets with no observations.
// Construction through it is mandatory: the zero LabeledHistogram panics on
// the first record. An invalid declaration, including the reserved "le"
// label and bad buckets, is captured and surfaces at registration.
func NewLabeledHistogram(name, help string, unit Unit, labels []string, opts ...Option) *LabeledHistogram {
	help = sanitizeHelp(name, help)
	o := applyOptions(opts)
	owned, err := checkNameAndLabels("LabeledHistogram", name, labels)
	if err == nil && slices.Contains(owned, "le") {
		err = fmt.Errorf(`measure: label name "le" for metric %q is reserved for the histogram bucket bound`, name)
	}
	bounds, berr := resolveBuckets(name, &o)
	lh := &LabeledHistogram{
		help:   help,
		labels: owned,
		bounds: bounds,
		vals:   make(map[labelKey]*Histogram),
	}
	lh.name = name
	lh.meta.fill(name, help, KindHistogram, unit, owned, &o)
	lh.meta.buckets = bounds
	lh.err = firstErr(err, berr, checkOptionFits(name, &o, true, len(owned)),
		checkDeclaration(name, KindHistogram, unit, owned, &o))
	if lh.err == nil {
		for _, set := range o.seeds {
			key := labelKeyFor(lh.labels, set)
			lh.loadOrStore(lh.vals, &key, lh.newSeries)
		}
	}
	return lh
}

// Declaration returns the histogram's metadata.
func (lh *LabeledHistogram) Declaration() Declaration { return lh.meta.declaration() }

func (lh *LabeledHistogram) declared() *declMeta {
	if lh == nil {
		return nil
	}
	return &lh.meta
}

func (lh *LabeledHistogram) newSeries() *Histogram {
	return &Histogram{bounds: lh.bounds, buckets: make([]atomic.Int64, len(lh.bounds)+1)}
}

// Observe records a value, in the histogram's unit, for the given label
// values. It panics on a label-arity mismatch. A histogram carrying a
// construction error records nothing (one warning on the first drop).
func (lh *LabeledHistogram) Observe(v float64, labelVals ...string) {
	if lh.err != nil {
		warnInertOnce(lh.err, &lh.warned, &lh.name)
		return
	}
	key := labelKeyFor(lh.labels, labelVals)
	h, _ := lh.loadOrStore(lh.vals, &key, lh.newSeries)
	h.Observe(v)
}

// Reset removes all label combinations from the histogram.
func (lh *LabeledHistogram) Reset() {
	lh.mu.Lock()
	clear(lh.vals)
	lh.mu.Unlock()
}

// Delete removes a single label combination from the histogram.
// It panics if the number of label values does not match the label count.
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created. A histogram carrying a construction error has no series,
// so Delete is a no-op.
func (lh *LabeledHistogram) Delete(labelVals ...string) {
	if lh.err != nil {
		return
	}
	lh.deleteSeries(lh.vals, lh.labels, labelVals)
}

// Timer measures elapsed time and reports to a Histogram. Build one with
// NewTimer or LabeledHistogram.NewTimer; the zero Timer is not usable.
type Timer struct {
	start   time.Time
	observe func(float64)
}

// NewTimer starts a timer that will observe into the given histogram, which
// must not be nil.
func NewTimer(h *Histogram) *Timer {
	return &Timer{start: time.Now(), observe: h.Observe}
}

// ObserveDuration records the elapsed time since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	t.observe(d.Seconds())
	return d
}

// NewTimer returns a Timer that, on ObserveDuration, records the elapsed time
// into this labeled histogram with the given label values. This lets the
// common per-label latency case use Timer's defer-ObserveDuration ergonomics
// (plain NewTimer only composes with an unlabeled Histogram).
func (lh *LabeledHistogram) NewTimer(labelVals ...string) *Timer {
	// Fail fast on arity mismatch at construction (labelKeyFor panics), so a
	// wrong-arity timer surfaces where it is created rather than inside a
	// deferred ObserveDuration. A histogram carrying a construction error
	// skips the check (its label set is not trustworthy enough to judge arity
	// against); the timer's Observe is then a no-op anyway.
	if lh.err == nil {
		_ = labelKeyFor(lh.labels, labelVals)
	}
	vals := slices.Clone(labelVals)
	return &Timer{start: time.Now(), observe: func(s float64) { lh.Observe(s, vals...) }}
}
