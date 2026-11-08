// Package measure is a standard-library-only Prometheus exposition library;
// Handler serves text format 0.0.4.
//
// Construction never panics on an invalid name, label or bucket set: the
// error is captured into the metric and surfaces at registration, where
// Register returns it and MustRegister panics. Such a metric records nothing
// (one warning on the first dropped record) and Handler emits nothing for it.
// A label-arity mismatch on a record path and a negative Counter.Add panic.
//
// Registry handlers and record paths are safe concurrently with
// registration. What is left out by design is listed in the README.
package measure

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// helpEscaper escapes backslashes and newlines in HELP text per Prometheus exposition format.
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// Go and process family names, defined once so the reservation list
// (processFamilyNames) and the builders (processFamiliesOf, runtimeFamilies)
// cannot drift. The go_memstats_* names other than the heap allocation one
// live only in runtimeFamilies.
const (
	pmGCDuration      = "go_gc_duration_seconds"
	pmGOGC            = "go_gc_gogc_percent"
	pmGOMEMLIMIT      = "go_gc_gomemlimit_bytes"
	pmGoroutines      = "go_goroutines"
	pmGoInfo          = "go_info"
	pmHeapAllocBytes  = "go_memstats_heap_alloc_bytes"
	pmLastGC          = "go_memstats_last_gc_time_seconds"
	pmGOMAXPROCS      = "go_sched_gomaxprocs_threads"
	pmThreads         = "go_threads"
	pmGCPauseTotal    = "process_gc_pause_seconds_total"
	pmUptime          = "process_uptime_seconds"
	pmStartTime       = "process_start_time_seconds"
	pmCPUTotal        = "process_cpu_seconds_total"
	pmResidentBytes   = "process_resident_memory_bytes"
	pmOpenFDs         = "process_open_fds"
	pmMaxFDs          = "process_max_fds"
	pmVirtualBytes    = "process_virtual_memory_bytes"
	pmVirtualMaxBytes = "process_virtual_memory_max_bytes"
	pmNetReceive      = "process_network_receive_bytes_total"
	pmNetTransmit     = "process_network_transmit_bytes_total"
)

// processFamilyNames are the family names processGather can emit, sorted.
// NewRegistry reserves them, and processSummarySeries, so a user metric
// colliding with one fails at Register instead of producing a duplicate
// "# TYPE" line or series that breaks the scrape.
var processFamilyNames = func() []string {
	names := []string{
		pmGCDuration, pmGoroutines, pmGoInfo, pmLastGC, pmThreads,
		pmGCPauseTotal, pmUptime, pmStartTime, pmCPUTotal, pmResidentBytes,
		pmOpenFDs, pmMaxFDs, pmVirtualBytes, pmVirtualMaxBytes, pmNetReceive, pmNetTransmit,
	}
	names = slices.Grow(names, len(runtimeFamilies))
	for i := range runtimeFamilies {
		names = append(names, runtimeFamilies[i].name)
	}
	slices.Sort(names)
	return names
}()

// processSummarySeries are the series names the go_gc_duration_seconds
// summary writes beside its quantiles.
var processSummarySeries = []string{pmGCDuration + "_sum", pmGCDuration + "_count"}

// Go and process HELP text for the families built outside runtimeFamilies.
const (
	helpGCDuration = "Stop-the-world pause durations of recent garbage collections in seconds."
	helpGoroutines = "Number of goroutines that currently exist."
	helpGoInfo     = "Information about the Go environment, with the Go version as a label."
	helpHeapAlloc  = "Number of heap bytes allocated and currently in use."
	helpLastGC     = "Time of the last garbage collection in seconds since the Unix epoch."
	helpThreads    = "Number of operating system threads the Go runtime has created."
	helpGCPause    = "Total GC pause time"
	helpUptime     = "Process uptime"
	helpStartTime  = "Start time of the process since unix epoch in seconds"
	helpCPU        = "Total user and system CPU time spent in seconds"
	helpResident   = "Resident memory size in bytes"
	helpOpenFDs    = "Number of open file descriptors"
	helpMaxFDs     = "Maximum number of open file descriptors"
	helpVirtual    = "Virtual memory size in bytes."
	helpVirtualMax = "Soft limit on the process's virtual memory size in bytes."
	helpNetRx      = "Bytes received over the network in the process's network namespace."
	helpNetTx      = "Bytes sent over the network in the process's network namespace."
)

// Registry holds a collection of metrics to be served.
type Registry struct {
	names map[string]string
	// err is a construction error captured by NewRegistry (an invalid
	// prefix). Every registration reports it, so a registry that cannot
	// produce a valid metric name refuses rather than emitting one.
	err               error
	logger            *slog.Logger
	prefix            string
	counters          []*Counter
	gauges            []*Gauge
	labeledCounters   []*LabeledCounter
	labeledGauges     []*LabeledGauge
	histograms        []*Histogram
	labeledHistograms []*LabeledHistogram
	collectors        []*Collector
	collectorFamilies int // descs across collectors, for slice capacities
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
func NewRegistry(prefix string, opts ...RegistryOption) *Registry {
	r := &Registry{
		prefix: prefix,
		names:  make(map[string]string),
	}
	for _, o := range opts {
		if o != nil {
			o(r)
		}
	}
	if prefix != "" {
		if err := checkMetricName(prefix); err != nil {
			r.err = fmt.Errorf("measure: invalid registry prefix %q: %w", prefix, err)
		}
	}
	for _, n := range slices.Concat(processFamilyNames, processSummarySeries) {
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
// *Gauge, *LabeledCounter, *LabeledGauge, *Histogram, *LabeledHistogram and
// *Collector. The interface is sealed (its method is unexported), so those
// seven are the only implementations: a scrape-time value comes from a
// function given to NewCollector, not from a type implementing an interface.
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
// label name, more than 8 labels on a labeled instrument, bad histogram buckets), an error when the
// metric is already registered, an error when the metric's exposition
// family name collides with an already-registered one, or an error when m is
// nil. A collector also returns the errors NewCollector captured (no descs, a
// nil desc, a nil collect, a repeated family name, a desc another collector
// holds, a desc not built with NewGaugeDesc or NewCounterDesc, or a desc's
// invalid name or label), and a Collector not built with NewCollector is
// refused. On error the metric is not attached and the registry is unchanged.
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

// Handler returns an HTTP handler serving Prometheus text format. Each request
// runs every registered collector with the request's context.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.WriteString(w, encodePrometheus(r.collect(req.Context()))); err != nil {
			orDefault(r.logger).Debug("measure: writing prometheus exposition failed", "error", err)
		}
	}
}

// RegistryOption configures NewRegistry.
type RegistryOption func(*Registry)

// WithLogger sets the logger for the lines the registry writes while it
// serves or gathers: a failed write of the exposition and a registered
// collector's problems. A nil logger, like none, means slog.Default at the
// time of each line. Lines a metric writes when it is recorded, and the
// process-metric lines, which every registry in the process shares, go to
// slog.Default.
func WithLogger(l *slog.Logger) RegistryOption {
	return func(r *Registry) { r.logger = l }
}

// orDefault is l, or slog.Default at the moment of the call when l is nil.
func orDefault(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
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
