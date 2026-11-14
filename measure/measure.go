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
var processSummarySeries = []string{pmGCDuration + suffixSum, pmGCDuration + suffixCount}

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
	members           map[*declMeta]struct{}
	prefix            string
	groups            [groupCount][]instrument
	collectors        []*Collector
	collectorFamilies int // descs across collectors, for slice capacities
	mu                sync.RWMutex
}

// The page's family groups, in page order; each holds its instruments in
// registration order.
const (
	groupLabeledCounters = iota
	groupCounters
	groupLabeledGauges // with the state sets and info series, which render as gauges
	groupGauges
	groupHistograms
	groupLabeledHistograms
	groupCount
)

// instrument is a recorded declaration the registry holds.
type instrument interface {
	Declared
	// family renders the instrument for the page; ok is false when it has
	// no series.
	family() (fam metricFamily, ok bool)
	gather() Family
}

// instrumentCount is the number of instruments registered. Callers hold r.mu.
func (r *Registry) instrumentCount() int {
	n := 0
	for _, g := range r.groups {
		n += len(g)
	}
	return n
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
		prefix:  prefix,
		names:   make(map[string]string),
		members: make(map[*declMeta]struct{}),
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
	families := [4]string{name, name + suffixBucket, name + suffixSum, name + suffixCount}
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
// *Gauge, *LabeledCounter, *LabeledGauge, *StateSet, *Info, *Histogram,
// *LabeledHistogram and *Collector. The interface is sealed (its method is
// unexported): a scrape-time value comes from a function given to
// NewCollector, not from a type implementing an interface.
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
// captured at construction: an invalid metric name; an invalid, reserved or
// duplicate label name; more than 8 labels on an instrument; bad histogram
// buckets; a unit off the list or one the name's suffix contradicts; _total
// on anything but a counter; an unbounded label on a series served on the
// page; or an option the declaration cannot take. It also returns an error
// when the metric is already registered, when its family name collides with
// a registered one, and when m is nil. A collector returns the errors
// [NewCollector] captured, and a Collector not built with NewCollector is
// refused. On error the metric is not attached and the registry is
// unchanged. A metric refused for a name collision stays intact and can be
// registered elsewhere; a construction error is permanent, so such a metric
// can only be rebuilt.
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
// construction error, claim the metric's registered flag, reserve the
// prefixed family name(s) via reserve, and hand the final name to rename,
// which writes it under the metric's own lock where it has one. The metric is
// then appended to its page group. The CAS is the only order between two
// registries registering one value concurrently (their locks are
// independent), so only its winner may write the name; for the same reason
// the loser's already-registered error cannot include the name. On a
// reservation failure the registered flag is rolled back, so a metric refused
// for a name collision stays registrable elsewhere. Callers must hold r.mu.
func (r *Registry) register(m instrument, group int, mErr error, registered *atomic.Bool, kind string,
	reserve func(name, kind string) error, rename func(name string),
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
	meta := m.declared()
	name := r.prefixed(meta.base)
	if err := reserve(name, kind); err != nil {
		registered.Store(false)
		return err
	}
	rename(name)
	meta.final.Store(&name)
	r.members[meta] = struct{}{}
	r.groups[group] = append(r.groups[group], m)
	return nil
}

func (c *Counter) registerInto(r *Registry) error {
	if c == nil {
		return errNilMetric
	}
	return r.register(c, groupCounters, c.err, &c.registered, "counter", r.reserveName,
		func(name string) { c.name = name })
}

func (g *Gauge) registerInto(r *Registry) error {
	if g == nil {
		return errNilMetric
	}
	return r.register(g, groupGauges, g.err, &g.registered, "gauge", r.reserveName,
		func(name string) { g.name = name })
}

func (lc *LabeledCounter) registerInto(r *Registry) error {
	if lc == nil {
		return errNilMetric
	}
	return r.register(lc, groupLabeledCounters, lc.err, &lc.registered, "labeled counter", r.reserveName, lc.rename)
}

func (lg *LabeledGauge) registerInto(r *Registry) error {
	if lg == nil {
		return errNilMetric
	}
	return r.register(lg, groupLabeledGauges, lg.err, &lg.registered, "labeled gauge", r.reserveName, lg.rename)
}

func (s *StateSet) registerInto(r *Registry) error {
	if s == nil {
		return errNilMetric
	}
	return r.register(s, groupLabeledGauges, s.err, &s.registered, "state set", r.reserveName, s.rename)
}

func (i *Info) registerInto(r *Registry) error {
	if i == nil {
		return errNilMetric
	}
	return r.register(i, groupLabeledGauges, i.err, &i.registered, "info series", r.reserveName, i.rename)
}

func (h *Histogram) registerInto(r *Registry) error {
	if h == nil {
		return errNilMetric
	}
	return r.register(h, groupHistograms, h.err, &h.registered, "histogram", r.reserveHistogramFamily,
		func(name string) { h.name = name })
}

func (lh *LabeledHistogram) registerInto(r *Registry) error {
	if lh == nil {
		return errNilMetric
	}
	return r.register(lh, groupLabeledHistograms, lh.err, &lh.registered, "labeled histogram", r.reserveHistogramFamily, lh.rename)
}

// rename writes the registered name under the series lock, which guards
// every read of it on the record paths.
func (s *series) rename(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
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
