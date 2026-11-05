// Package health implements the file-marker healthcheck pattern for
// distroless containers with no shell: the running process touches a
// marker file at lifecycle points via Set, and a probe process (the
// same binary re-invoked with a `health` subcommand) stats it via
// RunProbe. Package probe (github.com/cplieger/go/health/probe) is the
// HTTP-based counterpart for containers wrapping a third-party server.
//
// If the marker directory is not writable (e.g. compose's
// `read_only: true` without a `tmpfs: /tmp` mount), the constructor
// enters degraded mode: Set and Cleanup become no-ops, and the probe
// reports healthy rather than restart-looping a container whose only
// broken piece is the signaling channel.
//
// The probe checks existence only by default; an opt-in freshness
// deadline is available via WithMaxAge.
//
// Logging goes through slog.Default(); configure it via slog.SetDefault
// in main before constructing a Marker.
//
// Thread-safe; Set may be called from any goroutine.
package health

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Signal is the interface satisfied by *Marker. Consumers (e.g.
// HTTP handlers) can depend on this interface without importing the
// concrete type.
type Signal interface {
	Healthy() bool
}

var _ Signal = (*Marker)(nil)

// DefaultPath is the default marker location. Docker healthchecks
// stat this path; the app creates and removes it at lifecycle points.
const DefaultPath = "/tmp/.healthy"

// Marker implements the file-based distroless healthcheck pattern.
// Use NewMarker to construct it; call Set(bool) at lifecycle points;
// defer Cleanup on shutdown; call RunProbe from main when os.Args[1] is
// "health".
type Marker struct {
	path           string
	loggedFailSigs []string
	mu             sync.Mutex
	failed         bool
	degraded       bool
}

// NewMarker constructs a marker for path and probes the parent
// directory for writability. On failure it logs a single Warn with a
// fix hint and returns a marker in degraded mode; callers need not
// branch on the result.
func NewMarker(path string) *Marker {
	m := &Marker{path: path}
	if err := probeHealthDir(path); err != nil {
		m.degraded = true
		slog.Warn("health marker directory not writable, "+
			"container will report healthy in degraded mode",
			"dir", filepath.Dir(path),
			"error", err,
			"hint", "compose.yaml with read_only:true requires "+
				"`tmpfs: [\"/tmp:size=1m,mode=1777,noexec,nosuid,nodev\"]`")
	}
	return m
}

// Set touches or removes the marker for ok, from any goroutine. It logs a
// change from the file as it stands at the call (no file is unhealthy): Info
// when healthy, Warn when not, nothing when the file already shows ok. A
// filesystem failure is logged and swallowed (see SetChecked), and the next
// successful write logs one Info. In degraded mode Set is a no-op.
func (m *Marker) Set(ok bool) { _ = m.SetChecked(ok) }

// SetChecked is Set with the filesystem outcome reported: it returns nil
// when the marker now reflects ok, and the underlying error when reading,
// touching or removing it failed (the failure Set logs and swallows, so no
// extra log line is emitted). It exists for callers whose own success
// contract includes the marker write — e.g. a one-shot scan subcommand
// whose exit code an external scheduler alerts on, where a silently lost
// heartbeat should fail the invocation loudly instead. In degraded mode
// it returns nil, matching Set's no-op contract.
func (m *Marker) SetChecked(ok bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degraded {
		return nil
	}

	// Read per call: a separate process (a docker exec subcommand) may write
	// the same file, so a cached state would miss its changes.
	was, statErr := markerPresent(m.path)
	if statErr != nil {
		m.warnFailure("failed to stat health marker", statErr)
		return statErr
	}
	if msg, err := m.applyState(ok); err != nil {
		m.warnFailure(msg, err)
		return err
	}
	if was != ok {
		logHealthState(ok)
	}
	if m.recordSuccess() {
		slog.Info("health marker write recovered", "path", m.path, "healthy", ok)
	}
	return nil
}

// markerPresent reads the marker as the existence probe does: present is
// healthy and absent unhealthy. Any other stat error is returned, never read as absence.
func markerPresent(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// applyState performs the branch-specific filesystem operation for Set:
// touch the marker when ok, remove it (tolerating an already-absent file)
// otherwise. Returns the warn message and error on failure, or ("", nil)
// on success. Caller holds m.mu.
func (m *Marker) applyState(ok bool) (string, error) {
	if ok {
		if err := writeMarker(m.path); err != nil {
			return "failed to create health marker", err
		}
		return "", nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "failed to remove health marker", err
	}
	return "", nil
}

// logHealthState logs a health-state transition at the level matching the
// new state: Info when healthy, Warn when not.
func logHealthState(ok bool) {
	if ok {
		slog.Info("health state changed", "healthy", true)
		return
	}
	slog.Warn("health state changed", "healthy", false)
}

// Cleanup removes the marker. Typically called via defer at shutdown.
// In degraded mode Cleanup is a no-op.
func (m *Marker) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degraded {
		return
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("failed to remove health marker on cleanup",
			"path", m.path, "error", err)
	}
}

// CheckHealthy reports whether the marker file currently exists via a
// strict os.Stat, one call per invocation. Unlike RunProbe/ProbeCheck,
// it reports false in degraded mode whatever file is there, because Set
// cannot change it: an HTTP consumer via Handler deserves an honest signal
// rather than the restart-loop avoidance the probe needs.
func (m *Marker) CheckHealthy() bool {
	if m == nil || m.degraded {
		return false
	}
	_, err := os.Stat(m.path)
	return err == nil
}

// Healthy satisfies the Signal interface by delegating to
// [Marker.CheckHealthy]. It inherits CheckHealthy's cost: one os.Stat
// per call, not a field read.
func (m *Marker) Healthy() bool { return m.CheckHealthy() }

// Within returns a Signal for a marker os.Stat can read: healthy when d is
// non-positive or the marker is no older than d, the comparison [WithMaxAge]
// gives the probe, so a future modification time is fresh. Any stat error is
// unhealthy. Pass the same [Lease.Duration] to both, so a marker in a writable
// directory gets one verdict from the probe and the handler. In degraded mode
// (an unwritable directory) it is unhealthy whatever file is there, as
// CheckHealthy is, while the probe reports healthy. It costs one os.Stat per
// call.
func (m *Marker) Within(d time.Duration) Signal {
	if m == nil || m.degraded {
		return markerWithin{}
	}
	return markerWithin{path: m.path, d: d}
}

// markerWithin is the Signal Within returns; a nil or degraded marker leaves
// path empty, which never stats.
type markerWithin struct {
	path string
	d    time.Duration
}

func (w markerWithin) Healthy() bool {
	info, err := os.Stat(w.path)
	return err == nil && !stale(time.Since(info.ModTime()), w.d)
}

// stale is the lease rule the probe and Within share: a positive maxAge
// that the marker's age exceeds.
func stale(age, maxAge time.Duration) bool { return maxAge > 0 && age > maxAge }

// ProbeOption configures the probe-side health decision (RunProbe and
// ProbeCheck). Without options the probe checks marker existence only.
type ProbeOption func(*probeConfig)

// probeConfig holds the probe-side knobs collected from ProbeOptions.
type probeConfig struct {
	maxAge time.Duration
}

// WithMaxAge arms an opt-in freshness deadline: a marker older than d
// is unhealthy (exit 1), turning the signal from a level ("the app
// last reported healthy") into a lease ("the app recently proved
// progress"). Every Set(true) refreshes the marker's mtime, so an app
// that already calls Set(true) once per work cycle gets heartbeat
// semantics by passing this option to RunProbe in its health subcommand.
//
// Arm it only where the resident process runs its own bounded work
// cycle at a known cadence, so a stale marker means a wedged loop that
// a restart fixes. Do NOT arm it for externally-triggered apps (a
// separate docker exec writes the marker): an idle resident between
// triggers is healthy, and restarting it cannot fix a trigger that
// stopped firing. Marker.Healthy (and therefore Handler) stays
// existence-based regardless of this option.
//
// A non-positive d disables the deadline (same as omitting the option), so
// derive d with [Lease]: multiplying an operator-supplied interval inline can
// wrap to a negative d, which this option cannot tell from that disable.
func WithMaxAge(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.maxAge = d }
}

const maxDuration = time.Duration(math.MaxInt64)

// Lease derives a probe-side freshness deadline for [WithMaxAge] from an app's
// own cadence: Cycles refresh intervals plus Attempts work timeouts, never
// shorter than Floor. The zero value is a disabled lease reporting 0, so an
// externally-triggered or one-shot app passes a zero Lease and stays
// level-based. Interval alone decides that; every other field contributes
// nothing when non-positive. [Lease.Duration] saturates at the largest
// time.Duration rather than wrapping, because a wrapped negative deadline is
// indistinguishable from that deliberate disable.
type Lease struct {
	// Interval is the cadence at which the running process refreshes the
	// marker. A non-positive Interval disables the lease outright, whatever
	// the other fields hold: an app with no cadence has no wedge to detect.
	Interval time.Duration
	// Timeout is the longest one unit of work may legitimately take.
	Timeout time.Duration
	// Floor is the shortest deadline to arm. It applies only once Interval is
	// positive, so it can never arm a lease the Interval disabled.
	Floor time.Duration
	// Cycles is how many missed refreshes to tolerate. Non-positive counts as none.
	Cycles int
	// Attempts is how many Timeout budgets one refresh may spend. Non-positive
	// counts as none.
	Attempts int
}

// Duration reports the deadline to pass to [WithMaxAge]: 0 when the lease is
// disabled, otherwise a positive duration that saturates at the largest
// time.Duration rather than wrapping through zero.
func (l Lease) Duration() time.Duration {
	if l.Interval <= 0 {
		return 0
	}
	d := addSaturating(scale(l.Interval, l.Cycles), scale(l.Timeout, l.Attempts))
	return max(d, l.Floor)
}

// scale multiplies d by n, saturating at maxDuration instead of wrapping. A
// non-positive d or n contributes nothing.
func scale(d time.Duration, n int) time.Duration {
	if d <= 0 || n <= 0 {
		return 0
	}
	if d > maxDuration/time.Duration(n) {
		return maxDuration
	}
	return d * time.Duration(n)
}

// addSaturating returns a+b for non-negative a and b, saturating at maxDuration.
func addSaturating(a, b time.Duration) time.Duration {
	if a > maxDuration-b {
		return maxDuration
	}
	return a + b
}

// MarkerState is what a single look at a health marker found: STALE
// means the writing loop is wedged and a restart may clear it; ABSENT
// means nothing has written it yet (cold start, wiped volume) and a
// restart changes nothing. The 0/1 probe exit code cannot express that
// difference; Inspect's Freshness can.
type MarkerState int

const (
	// MarkerFresh is a marker present and, when a deadline is armed, inside it.
	MarkerFresh MarkerState = iota
	// MarkerStale is a marker present but older than the armed deadline, in a
	// directory the app can write. Only reachable with WithMaxAge; without it a
	// present marker is always fresh.
	MarkerStale
	// MarkerAbsent is a marker that does not exist in a usable directory.
	MarkerAbsent
	// MarkerUnreadable is a marker whose stat failed for a reason other than
	// absence (permission, symlink loop, I/O) in a usable directory.
	MarkerUnreadable
	// MarkerDirUnavailable is degraded mode: the marker's directory cannot be
	// written, so the app has no way to signal through the filesystem at all:
	// the marker is absent, unreadable or past a deadline nothing can refresh.
	// The probe treats this as healthy; see RunProbe.
	MarkerDirUnavailable
)

// String renders the state for a log attribute.
func (s MarkerState) String() string {
	switch s {
	case MarkerFresh:
		return "fresh"
	case MarkerStale:
		return "stale"
	case MarkerAbsent:
		return "absent"
	case MarkerUnreadable:
		return "unreadable"
	case MarkerDirUnavailable:
		return "dir-unavailable"
	}
	return "unknown"
}

// Freshness is one look at a health marker: what state it is in, how old it is,
// and why the stat failed when it did. A caller acting on marker freshness
// in-process (a resident daemon watching its own work loop for a wedge) needs
// the age and needs stale distinguished from absent to avoid calling a cold
// start a wedge.
type Freshness struct {
	// Err is the underlying stat or directory-probe error for MarkerUnreadable
	// and MarkerDirUnavailable, nil otherwise.
	Err error
	// Age is how long ago the marker was last written: set for MarkerFresh,
	// MarkerStale, and MarkerDirUnavailable when a stale marker was found;
	// zero for every state with no marker to age.
	Age time.Duration
	// MaxAge is the armed deadline, or 0 when none was armed.
	MaxAge time.Duration
	State  MarkerState
}

// Healthy reports whether this reading is the probe's healthy verdict: a
// fresh marker, or a directory the app cannot signal through at all.
func (f Freshness) Healthy() bool {
	return f.State == MarkerFresh || f.State == MarkerDirUnavailable
}

// Reason is the operator-facing diagnostic for an unhealthy reading, and the
// empty string for a healthy one. It is the text RunProbe writes to stderr.
func (f Freshness) Reason() string {
	switch f.State {
	case MarkerStale:
		return fmt.Sprintf("unhealthy: marker stale: %s old exceeds max-age %s",
			f.Age.Truncate(time.Second), f.MaxAge)
	case MarkerAbsent:
		return "unhealthy: marker absent"
	case MarkerUnreadable:
		return "unhealthy: marker stat failed: " + f.Err.Error()
	case MarkerFresh, MarkerDirUnavailable:
		return ""
	}
	return ""
}

// Inspect reads a health marker and reports what it found, without deciding
// anything or exiting. RunProbe and ProbeCheck are presentations of it, so a
// caller acting on Inspect's result and a container healthcheck reading the
// exit code cannot diverge.
//
// Pass WithMaxAge to arm the lease, and read MarkerStale to detect a wedged
// loop. Without it a present marker is always MarkerFresh.
func Inspect(path string, opts ...ProbeOption) Freshness {
	var cfg probeConfig
	for _, o := range opts {
		o(&cfg)
	}
	info, statErr := os.Stat(path) // #nosec G703 -- trusted caller-supplied marker path, existence check only
	if statErr == nil {
		age := time.Since(info.ModTime())
		if !stale(age, cfg.maxAge) {
			return Freshness{State: MarkerFresh, Age: age, MaxAge: cfg.maxAge}
		}
		// A stale marker nothing can refresh, in an unwritable directory, is
		// degraded mode: failing it would restart-loop a container a restart
		// cannot fix.
		if dirErr := probeHealthDir(path); dirErr != nil {
			return Freshness{State: MarkerDirUnavailable, Age: age, MaxAge: cfg.maxAge, Err: dirErr}
		}
		return Freshness{State: MarkerStale, Age: age, MaxAge: cfg.maxAge}
	}
	// A directory nothing can write to explains absence without implicating
	// app health, so the dir probe runs before classifying the stat error.
	if dirErr := probeHealthDir(path); dirErr != nil {
		return Freshness{State: MarkerDirUnavailable, MaxAge: cfg.maxAge, Err: dirErr}
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return Freshness{State: MarkerAbsent, MaxAge: cfg.maxAge}
	}
	return Freshness{State: MarkerUnreadable, MaxAge: cfg.maxAge, Err: statErr}
}

// RunProbe runs in the separate `health` subcommand process. It exits
// 0 if the marker is present (and fresh, when WithMaxAge is armed) or
// the marker directory is unwritable (degraded mode: the long-running
// process cannot signal through the filesystem, so the probe falls
// back to "alive"). It exits 1 when the marker, in a writable
// directory, is absent or stale past an armed deadline; the stderr
// diagnostic names the underlying stat failure when the cause is
// something other than absence.
func RunProbe(path string, opts ...ProbeOption) {
	code, reason := probeCheck(path, opts...)
	if code != 0 {
		fmt.Fprintln(os.Stderr, reason)
	}
	os.Exit(code)
}

// ProbeCheck implements the health-probe decision without calling
// os.Exit, so it can be unit-tested. Returns 0 for healthy or
// degraded, 1 for unhealthy.
func ProbeCheck(path string, opts ...ProbeOption) int {
	code, _ := probeCheck(path, opts...)
	return code
}

// probeCheck carries the shared probe decision plus the operator-facing
// diagnostic for the unhealthy exit: "marker absent" for the common
// ENOENT case, a stale-age line when an armed WithMaxAge deadline is
// exceeded, and the underlying stat error otherwise (permission,
// symlink loop, I/O), so RunProbe does not mislabel those as absence.
//
// It is a presentation of Inspect, not a second reading: one
// implementation decides, and the exit code and the structured result
// are two views of it (pinned by TestInspectAndProbeCheckCannotDisagree).
func probeCheck(path string, opts ...ProbeOption) (code int, reason string) {
	f := Inspect(path, opts...)
	if f.Healthy() {
		return 0, ""
	}
	return 1, f.Reason()
}

// warnFailure logs a filesystem-op failure once per distinct (message,
// error) signature per streak, keying on both the static message AND the
// underlying error. A repeated identical failure stays silent, while a
// new message OR a new underlying error arising mid-streak still
// surfaces exactly once. Then it marks the marker failed. Caller holds m.mu.
func (m *Marker) warnFailure(msg string, err error) {
	if !m.failed {
		m.loggedFailSigs = m.loggedFailSigs[:0]
	}
	sig := msg + "\x00" + err.Error()
	if !slices.Contains(m.loggedFailSigs, sig) {
		slog.Warn(msg, "path", m.path, "error", err)
		m.loggedFailSigs = append(m.loggedFailSigs, sig)
	}
	m.failed = true
}

// recordSuccess ends a failure streak after a successful write, returning
// whether one was open. Caller holds m.mu.
func (m *Marker) recordSuccess() bool {
	recovered := m.failed
	m.failed = false
	m.loggedFailSigs = m.loggedFailSigs[:0]
	return recovered
}

// writeMarker atomically touches the marker file. A fresh os.Create is
// sufficient: the file is empty, and O_TRUNC on an existing file
// refreshes its mtime, which is the contract an armed WithMaxAge
// deadline reads. TestHealthMarker_SetTrue_refreshesMtime pins it.
func writeMarker(path string) error {
	f, err := os.Create(path) // #nosec G304 -- caller-supplied trusted path
	if err != nil {
		return err
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}
	return nil
}

// probeHealthDir verifies the marker's parent directory is writable by
// creating and deleting a temp file. Returns the underlying error on
// failure so callers can log with context.
func probeHealthDir(path string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".health-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(name) // #nosec G703 -- name generated by os.CreateTemp above, not external input
		}
	}()

	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close probe: %w", closeErr)
	}
	if rmErr := os.Remove(name); rmErr != nil { // #nosec G703 -- name generated by os.CreateTemp above, not external input
		return fmt.Errorf("remove probe: %w", rmErr)
	}
	removed = true
	return nil
}
