package measure

import (
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// cardinalityWarnThreshold is the per-metric series count at which loadOrStore
// emits its one-time label-cardinality warning. Log-only: no cap or rejection
// is imposed and recording behavior is unchanged; the warning turns silent
// unbounded series growth (e.g. a caller using raw request paths as a label
// value) into an actionable, alertable signal before memory is exhausted.
const cardinalityWarnThreshold = 1000

// warnInertOnce emits the one-time warning for a record dropped on a metric
// carrying a construction error, naming the metric and the error; the CAS on
// warned keeps every later drop a silent no-op (mirroring the one-time
// cardinality warning). Registration is the designed reporting door, but a
// metric constructed and never registered would otherwise fail with no
// diagnostic at all, and recording is the operation such code actually
// performs. Called only inside a record path's err != nil branch, so the
// valid-metric fast path stays a single nil check; name is read via pointer
// there, which is race-free because a metric carrying a construction error is
// unregistrable and thus never concurrently renamed by a registration.
func warnInertOnce(err error, warned *atomic.Bool, name *string) {
	if warned.CompareAndSwap(false, true) {
		slog.Warn("measure: record dropped, metric carries a construction error",
			"metric", *name, "error", err)
	}
}

// Counter is a monotonically increasing counter.
type Counter struct {
	err        error // construction-time validation error; surfaces at registration
	name       string
	help       string
	val        atomic.Int64
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewCounter creates a named counter. Per Prometheus convention the name
// should end in `_total` (e.g. "http_requests_total"). An invalid name is
// captured into the counter rather than panicking: the counter records
// nothing, WriteCounter emits nothing for it, and the error surfaces at
// registration.
func NewCounter(name, help string) *Counter {
	help = sanitizeHelp(name, help)
	return &Counter{name: name, help: help, err: checkMetricName(name)}
}

// Inc increments the counter by 1. The counter saturates at math.MaxInt64
// instead of wrapping negative.
func (c *Counter) Inc() { c.Add(1) }

// Add increments the counter by n. Panics if n < 0. The counter saturates at
// math.MaxInt64 instead of wrapping negative. A counter carrying a
// construction error records nothing (one warning on the first drop).
func (c *Counter) Add(n int64) {
	if c.err != nil {
		warnInertOnce(c.err, &c.warned, &c.name)
		return
	}
	if n < 0 {
		panic("measure: Counter.Add called with negative value")
	}
	addSaturating(&c.val, n)
}

// addSaturating adds n (>= 0) to v, clamping at math.MaxInt64 on overflow.
// Counters are monotonic and start at zero, so a negative result after adding
// a non-negative n can only mean two's-complement wraparound; the value is
// pinned to MaxInt64 so the exposed series never violates the monotonic
// contract. The correction is a plain Store: every competing writer that
// observes the wrap stores the same MaxInt64, and once pinned there the
// counter stays pinned (MaxInt64 + n wraps negative again and is re-pinned).
func addSaturating(v *atomic.Int64, n int64) {
	if v.Add(n) < 0 {
		v.Store(math.MaxInt64)
	}
}

// maxLabels is the per-metric label-name cap. labelKey is a fixed-size array
// so series lookups stay allocation-free and the map key stays comparable;
// the cap is a documented product limit (Prometheus best practice keeps label
// counts far below it).
const maxLabels = 8

// series carries the state every per-label-combination operation needs: the
// lock guarding the series map and the metric name a warning reports. The three
// labeled metric types embed it, so those operations are METHODS on the state
// they mutate instead of package-level functions taking a mutex and a name
// pointer.
//
// It is deliberately NOT generic: the map's value type arrives on each method
// instead (Go 1.27 generic methods). A generic carrier — series[V] holding vals
// — would drop one more argument per call, and it was measured and rejected,
// twice over. Embedded, `go doc` renders `series[*atomic.Int64]` inside the
// public body of LabeledCounter (an embedded unexported GENERIC instantiation
// is printed, where an embedded plain unexported type and a named unexported
// field are both folded into "Has unexported fields"), so all three exported
// types would advertise a type no caller can name. Held as a named field
// instead, godoc stays clean but 84 lc.name/lc.mu/lc.vals references gain a
// level of indirection to save one argument at 9 call sites.
type series struct {
	name       string
	mu         sync.RWMutex
	cardWarned bool // the one-time cardinality warning has fired; guarded by mu
}

// labelKey is a fixed-size struct key for labeled metrics.
type labelKey [maxLabels]string

// labelKeyFor validates arity and packs label values into a fixed-size key.
func labelKeyFor(labels, labelVals []string) labelKey {
	if len(labelVals) != len(labels) {
		panic("measure: label arity mismatch")
	}
	var key labelKey
	copy(key[:], labelVals)
	return key
}

// LabeledCounter tracks counts per label combination.
type LabeledCounter struct {
	vals   map[labelKey]*atomic.Int64
	help   string
	err    error // construction-time validation error; surfaces at registration
	labels []string
	series
	registered atomic.Bool
	warned     atomic.Bool // one-time inert-record warning emitted
}

// NewLabeledCounter creates a labeled counter with the given label names. As
// with NewCounter, the name should end in `_total` per Prometheus convention.
// Construction through NewLabeledCounter is mandatory: the zero
// LabeledCounter has a nil series map and panics on the first record. An
// invalid metric name, an invalid/reserved/duplicate label name, or more than
// 8 labels is captured into the counter rather than panicking: the counter
// records nothing and the error surfaces at registration.
func NewLabeledCounter(name, help string, labels []string) *LabeledCounter {
	help = sanitizeHelp(name, help)
	owned, err := checkNameAndLabels("LabeledCounter", name, labels)
	return &LabeledCounter{
		name:   name,
		help:   help,
		err:    err,
		labels: owned,
		vals:   make(map[labelKey]*atomic.Int64),
	}
}

// Inc increments the counter for the given label values.
func (lc *LabeledCounter) Inc(labelVals ...string) {
	lc.Add(1, labelVals...)
}

// Add increments the counter for the given label values by n. Panics if n < 0
// or on a label-arity mismatch. Each series saturates at math.MaxInt64
// instead of wrapping negative. A counter carrying a construction error
// records nothing, with one warning on the first drop (and skips both panics:
// its label set is not trustworthy enough to judge arity against).
func (lc *LabeledCounter) Add(n int64, labelVals ...string) {
	if lc.err != nil {
		warnInertOnce(lc.err, &lc.warned, &lc.name)
		return
	}
	if n < 0 {
		panic("measure: LabeledCounter.Add called with negative value")
	}
	key := labelKeyFor(lc.labels, labelVals)
	if v, loaded := lc.loadOrStore(lc.vals, &key,
		func() *atomic.Int64 { a := &atomic.Int64{}; a.Store(n); return a }); loaded {
		addSaturating(v, n)
	}
}

// Reset removes all label combinations from the counter.
func (lc *LabeledCounter) Reset() {
	lc.mu.Lock()
	clear(lc.vals)
	lc.mu.Unlock()
}

// Delete removes a single label combination from the counter.
// It panics if the number of label values does not match the label count.
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created. A counter carrying a construction error has no series,
// so Delete is a no-op.
func (lc *LabeledCounter) Delete(labelVals ...string) {
	if lc.err != nil {
		return
	}
	lc.deleteSeries(lc.vals, lc.labels, labelVals)
}

// WriteCounter writes a counter in Prometheus text format. It is a thin shim
// over the neutral IR (Counter.family) and the Prometheus encoder, retained as
// part of the package's exported surface. A counter carrying a construction
// error writes nothing: an invalid metric must never reach the exposition.
func WriteCounter(b *strings.Builder, c *Counter) {
	if c.err != nil {
		return
	}
	appendPrometheus(b, []metricFamily{c.family()})
}

// loadOrStore returns the entry for key, creating it with makeV under the
// write lock when absent. loaded reports whether the entry already existed.
// The hot loaded-path never reads s.name. Label values carrying invalid UTF-8
// are sanitized with U+FFFD on the series-creation path (see storeNewSeries),
// and when storing a new series pushes the map exactly to
// cardinalityWarnThreshold, a one-time warning names the metric so a
// label-cardinality explosion is observable before it exhausts memory. All
// warning captures (the metric name, the representative sanitized value)
// happen under the lock in storeNewSeries, so exactly one inserter observes
// each event and the name read is synchronized with the mutex-guarded rename
// in Register*; the warnings themselves are emitted AFTER the write lock is
// released so arbitrary slog handler code never runs under the metric lock.
func (s *series) loadOrStore[V any](m map[labelKey]V, key *labelKey, makeV func() V) (v V, loaded bool) {
	s.mu.RLock()
	v, loaded = m[*key]
	s.mu.RUnlock()
	if loaded {
		return v, true
	}
	var w seriesWarnings
	v, loaded, w = s.storeNewSeries(m, key, makeV)
	if w.san {
		slog.Warn("measure: label value contained invalid UTF-8; sanitized with U+FFFD",
			"metric", w.name, "value", w.sanValue)
	}
	if w.card {
		slog.Warn("measure: labeled metric series count crossed threshold; possible label-cardinality explosion",
			"metric", w.name, "series", cardinalityWarnThreshold)
	}
	return v, loaded
}

// seriesWarnings carries the warning captures storeNewSeries takes under the
// write lock so loadOrStore can emit the corresponding slog warnings after the
// lock is released.
type seriesWarnings struct {
	name     string // metric name, captured under the lock when any warning fires
	sanValue string // representative sanitized label value, truncated for logging
	san      bool   // sanitization created a new series
	card     bool   // this insert was the metric's first crossing of cardinalityWarnThreshold
}

// sanitizeLabelKey runs sanitizeUTF8 over each of key's values, rewriting them
// in place. It returns one representative sanitized value (truncated via
// maxLogValueLen for logging) and whether any value changed.
func sanitizeLabelKey(key *labelKey) (rep string, changed bool) {
	for i, v := range key {
		san, ch := sanitizeUTF8(v)
		if !ch {
			continue
		}
		key[i] = san
		if !changed {
			rep = truncateForLog(san)
		}
		changed = true
	}
	return rep, changed
}

// storeNewSeries sanitizes key's label values to valid UTF-8 (in place, so the
// caller's key holds the sanitized values afterwards) and inserts the sanitized
// key under the write lock (double-checked). A raw invalid-UTF-8 key is never
// stored, so records carrying invalid UTF-8 always miss loadOrStore's RLock
// fast path and re-take this slow path, landing on the same sanitized series —
// deliberate: degraded input gets the slow path. Distinct raw values may merge
// into one sanitized series — also deliberate. The returned seriesWarnings is
// captured under the lock (the metric name reads are synchronized with the
// mutex-guarded rename in Register*): w.san fires only when a sanitization
// actually CREATED a new series (the double-check-found path never warns), and
// w.card fires the first time an insert pushes the map to
// cardinalityWarnThreshold; the latch on the carrier keeps a later dip (a
// Delete or Reset) and re-crossing from re-emitting it, so the warning is
// one-time per metric. The defer keeps the unlock on a makeV panic path.
func (s *series) storeNewSeries[V any](m map[labelKey]V, key *labelKey, makeV func() V) (v V, loaded bool, w seriesWarnings) {
	sanValue, sanitized := sanitizeLabelKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, loaded = m[*key]; loaded {
		return v, true, seriesWarnings{}
	}
	v = makeV()
	m[*key] = v
	if sanitized {
		w.san = true
		w.sanValue = sanValue
	}
	if len(m) == cardinalityWarnThreshold && !s.cardWarned {
		s.cardWarned = true
		w.card = true
	}
	if w.san || w.card {
		w.name = s.name
	}
	return v, false, w
}

// deleteSeries removes the series for labelVals from m under the write lock.
// The lookup key is sanitized to valid UTF-8 exactly as recording sanitizes it
// (sanitizeLabelKey), so Delete called with the original raw values removes
// the series recording created. Shared by the three labeled Delete methods.
func (s *series) deleteSeries[V any](m map[labelKey]V, labels, labelVals []string) {
	key := labelKeyFor(labels, labelVals)
	sanitizeLabelKey(&key)
	s.mu.Lock()
	delete(m, key)
	s.mu.Unlock()
}

// sortLabelKeys sorts label keys lexicographically.
func sortLabelKeys(keys []labelKey) {
	slices.SortFunc(keys, func(a, b labelKey) int { return slices.Compare(a[:], b[:]) })
}

// sortedLabelKeys snapshots the keys of vals under the series read lock and
// returns them sorted lexicographically. Callers hold no lock on entry.
func (s *series) sortedLabelKeys[V any](vals map[labelKey]V) []labelKey {
	s.mu.RLock()
	keys := make([]labelKey, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	sortLabelKeys(keys)
	return keys
}

// buildLabelString builds a sorted, spec-escaped label string from labels and key.
func buildLabelString(labels []string, key *labelKey) string {
	return renderLabels(labelsFor(labels, key))
}

// WriteLabeledCounter writes a labeled counter in Prometheus text format (IR
// shim). A counter carrying a construction error writes nothing.
func WriteLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	if lc.err != nil {
		return
	}
	if f, ok := lc.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}
