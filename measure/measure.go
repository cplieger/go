// Package measure is a standard-library-only Prometheus exposition library;
// Handler serves text format 0.0.4.
//
// Construction never panics on an invalid name, label or bucket set: the
// error is captured into the metric and surfaces at registration, where
// Register returns it and MustRegister panics. Such a metric records nothing
// (one warning on the first dropped record) and its Write* function emits
// nothing. A label-arity mismatch on a record path and a negative Counter.Add
// panic.
//
// Finish registration before serving a custom handler built on the Write*
// functions, which read a metric's name without the registry lock. Registry
// handlers and record paths are safe concurrently with registration. What is
// left out by design is listed in the README.
package measure

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// helpEscaper escapes backslashes and newlines in HELP text per Prometheus exposition format.
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// Process metric family names, defined once so the reservation list
// (processFamilyNames) and the process-metric IR builder (processFamilies)
// reference a single source and cannot drift.
const (
	pmGoroutines     = "go_goroutines"
	pmHeapAllocBytes = "go_memstats_heap_alloc_bytes"
	pmGCPauseTotal   = "process_gc_pause_seconds_total"
	pmUptime         = "process_uptime_seconds"
	pmStartTime      = "process_start_time_seconds"
	pmCPUTotal       = "process_cpu_seconds_total"
	pmResidentBytes  = "process_resident_memory_bytes"
	pmOpenFDs        = "process_open_fds"
	pmMaxFDs         = "process_max_fds"
)

// processFamilyNames are the family names processFamilies emits. Reserved at
// creation so a user metric colliding with one fails fast like any other
// duplicate instead of silently producing a duplicate "# TYPE" line that
// breaks the scrape.
var processFamilyNames = []string{
	pmGoroutines, pmHeapAllocBytes,
	pmGCPauseTotal,
	pmUptime, pmStartTime,
	pmCPUTotal,
	pmResidentBytes, pmOpenFDs, pmMaxFDs,
}

// Process metric HELP text, single-sourced so the reservation list and the
// writers cannot expose divergent descriptions for the same family.
const (
	helpGoroutines = "Number of goroutines that currently exist."
	helpHeapAlloc  = "Number of heap bytes allocated and currently in use."
	helpGCPause    = "Total GC pause time"
	helpUptime     = "Process uptime"
	helpStartTime  = "Start time of the process since unix epoch in seconds"
	helpCPU        = "Total user and system CPU time spent in seconds"
	helpResident   = "Resident memory size in bytes"
	helpOpenFDs    = "Number of open file descriptors"
	helpMaxFDs     = "Maximum number of open file descriptors"
)

// Registry holds a collection of metrics to be served.
type Registry struct {
	names map[string]string
	// err is a construction error captured by NewRegistry (an invalid
	// prefix). Every registration reports it, so a registry that cannot
	// produce a valid metric name refuses rather than emitting one.
	err               error
	prefix            string
	counters          []*Counter
	gauges            []*Gauge
	labeledCounters   []*LabeledCounter
	labeledGauges     []*LabeledGauge
	histograms        []*Histogram
	labeledHistograms []*LabeledHistogram
	mu                sync.RWMutex
}

// NewRegistry creates a new metrics registry. Construction through
// NewRegistry is mandatory: the zero Registry has a nil name table and
// panics on the first registration.
//
// An invalid prefix (one that does not match the metric-name grammar) is
// CAPTURED, not panicked, and surfaces at the first [Registry.Register] or
// [Registry.MustRegister] — the same door every other construction error uses.
// A prefix that cannot make a valid metric name makes every metric registered
// through it invalid, so reporting it once at the door beats reporting it at
// construction: it keeps this package to ONE error model with no exception,
// and for a caller registering through MustRegister the failure still lands at
// init, where a package-level registry is built.
func NewRegistry(prefix string) *Registry {
	r := &Registry{
		prefix: prefix,
		names:  make(map[string]string),
	}
	if prefix != "" {
		if err := checkMetricName(prefix); err != nil {
			r.err = fmt.Errorf("measure: invalid registry prefix %q: %w", prefix, err)
		}
	}
	for _, n := range processFamilyNames {
		r.names[n] = "process metric"
	}
	return r
}

// prefixed joins the registry prefix to a metric name (prefix_name). An empty
// prefix returns the name unchanged.
func (r *Registry) prefixed(name string) string {
	if r.prefix == "" {
		return name
	}
	return r.prefix + "_" + name
}

// reserveName records the exposition family name a metric occupies and
// returns an error if another metric already claims it. The family name is
// the identifier that appears in the "# TYPE" line; every metric type uses
// its registered name verbatim. Family names must be unique across the whole
// registry and across types, because a duplicate "# TYPE" line makes
// Prometheus parsers reject the entire scrape. The collision surfaces through
// the registration doors: Register returns it, MustRegister panics.
// Callers must hold r.mu.
func (r *Registry) reserveName(family, kind string) error {
	if existing, ok := r.names[family]; ok {
		return fmt.Errorf("measure: %s %q collides with already-registered %s; "+
			"metric family names must be unique across the registry", kind, family, existing)
	}
	r.names[family] = kind
	return nil
}

// reserveHistogramFamily reserves the histogram base name plus the derived
// _bucket/_sum/_count series names a histogram emits in both writers. The
// reservation is transactional: on a collision, the names already claimed by
// this call are released, so a failed registration leaves the registry's
// name table exactly as it was. Callers must hold r.mu.
func (r *Registry) reserveHistogramFamily(name, kind string) error {
	families := [4]string{name, name + "_bucket", name + "_sum", name + "_count"}
	for i, f := range families {
		if err := r.reserveName(f, kind); err != nil {
			for _, claimed := range families[:i] {
				delete(r.names, claimed)
			}
			return err
		}
	}
	return nil
}

// Metric is implemented by every metric type in this package: *Counter,
// *Gauge, *LabeledCounter, *LabeledGauge, *Histogram, and *LabeledHistogram.
// The interface is sealed (its method is unexported), so those six are the
// only implementations: third-party collectors in the client_golang style are
// out of scope by design, like the rest of the README's "Unsupported by
// Design" list.
type Metric interface {
	// registerInto validates and attaches the metric to r. Callers hold r.mu.
	registerInto(r *Registry) error
}

// errNilMetric is the error Register returns (and MustRegister panics with)
// when handed a nil Metric — either a nil interface value or a typed-nil
// pointer such as (*Counter)(nil). Guarded explicitly so the error-returning
// door reports the bad argument instead of dereferencing it.
var errNilMetric = errors.New("measure: Register called with nil metric")

// Register adds a metric to the registry. It returns the error the metric
// captured at construction (invalid metric name, invalid/reserved/duplicate
// label name, more than 8 labels, bad histogram buckets), an error when the
// metric is already registered, an error when the metric's exposition
// family name collides with an already-registered one, or an error when m is
// nil. On error the metric is not attached and the registry is unchanged.
// After a family-name collision the metric itself is left unregistered and
// intact, so it can still be registered with a different registry (or with
// this one once the colliding name is freed); a construction error is
// immutable, so such a metric cannot be repaired, only rebuilt with a valid
// name, label set, or buckets. MustRegister is the panicking sibling for
// callers that cannot handle an error.
func (r *Registry) Register(m Metric) error {
	if m == nil {
		return errNilMetric
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return m.registerInto(r)
}

// MustRegister registers each metric in turn and panics on the first error
// (the client_golang shape). It is the door for package-level var metric
// sets, whose init-time registration has no caller to hand an error to.
func (r *Registry) MustRegister(ms ...Metric) {
	for _, m := range ms {
		if err := r.Register(m); err != nil {
			panic(err)
		}
	}
}

// register runs the shared registration flow: reject a metric that captured a
// construction error, claim the metric's registered flag, resolve the
// metric's name via base, reserve the prefixed family name(s) via reserve,
// and hand the final name to attach, which links the metric into the
// registry. base runs only AFTER the CAS is won: the winner's attach renames
// the metric, and nothing but the CAS orders a concurrent cross-registry
// registration of the same value (the registries' locks are independent), so
// only the CAS winner may read or write the name field. For the same reason
// the loser's already-registered error cannot include the metric name. On a
// reservation failure the registered flag is rolled back, so a metric refused
// for a name collision stays registrable elsewhere. Callers must hold r.mu.
func (r *Registry) register(mErr error, registered *atomic.Bool, kind string, base func() string,
	reserve func(name, kind string) error, attach func(name string),
) error {
	if r.err != nil {
		return r.err
	}
	if mErr != nil {
		return mErr
	}
	if !registered.CompareAndSwap(false, true) {
		return errors.New("measure: " + kind + " already registered")
	}
	name := r.prefixed(base())
	if err := reserve(name, kind); err != nil {
		registered.Store(false)
		return err
	}
	attach(name)
	return nil
}

func (c *Counter) registerInto(r *Registry) error {
	if c == nil {
		return errNilMetric
	}
	return r.register(c.err, &c.registered, "counter", func() string { return c.name }, r.reserveName, func(name string) {
		c.name = name
		r.counters = append(r.counters, c)
	})
}

func (g *Gauge) registerInto(r *Registry) error {
	if g == nil {
		return errNilMetric
	}
	return r.register(g.err, &g.registered, "gauge", func() string { return g.name }, r.reserveName, func(name string) {
		g.name = name
		r.gauges = append(r.gauges, g)
	})
}

func (lc *LabeledCounter) registerInto(r *Registry) error {
	if lc == nil {
		return errNilMetric
	}
	return r.register(lc.err, &lc.registered, "labeled counter", func() string { return lc.name }, r.reserveName, func(name string) {
		lc.mu.Lock()
		lc.name = name
		lc.mu.Unlock()
		r.labeledCounters = append(r.labeledCounters, lc)
	})
}

func (lg *LabeledGauge) registerInto(r *Registry) error {
	if lg == nil {
		return errNilMetric
	}
	return r.register(lg.err, &lg.registered, "labeled gauge", func() string { return lg.name }, r.reserveName, func(name string) {
		lg.mu.Lock()
		lg.name = name
		lg.mu.Unlock()
		r.labeledGauges = append(r.labeledGauges, lg)
	})
}

func (h *Histogram) registerInto(r *Registry) error {
	if h == nil {
		return errNilMetric
	}
	return r.register(h.err, &h.registered, "histogram", func() string { return h.name }, r.reserveHistogramFamily, func(name string) {
		h.name = name
		r.histograms = append(r.histograms, h)
	})
}

func (lh *LabeledHistogram) registerInto(r *Registry) error {
	if lh == nil {
		return errNilMetric
	}
	return r.register(lh.err, &lh.registered, "labeled histogram", func() string { return lh.name }, r.reserveHistogramFamily, func(name string) {
		lh.mu.Lock()
		lh.name = name
		lh.mu.Unlock()
		r.labeledHistograms = append(r.labeledHistograms, lh)
	})
}

// Handler returns an HTTP handler serving Prometheus text format.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.WriteString(w, encodePrometheus(r.collect())); err != nil {
			slog.Debug("measure: writing prometheus exposition failed", "error", err)
		}
	}
}

// formatValue renders a float64 metric value in its canonical exposition form.
// Non-finite values use the spec tokens "+Inf"/"-Inf"/"NaN". A finite value
// that is exactly integral and within the int64-exact range renders as a bare
// integer (e.g. "42", "1718193600"). Everything else uses the shortest
// round-trippable form (strconv 'g'), which preserves full precision and never
// floors a small magnitude to zero the way a fixed-precision %.6f would.
func formatValue(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	if v >= -1e15 && v <= 1e15 && v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
