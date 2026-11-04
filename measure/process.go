package measure

import (
	"encoding/binary"
	"log/slog"
	"math"
	"math/bits"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// processStartTime is captured at package init as the fallback anchor for
// process_start_time_seconds (and, via it, process_uptime_seconds) when the
// kernel-derived start time cannot be read. On Linux the true start time comes
// from /proc (btime + starttime/USER_HZ, see readProcStartTime); on other
// platforms or on any /proc parse failure this package-init instant is used
// instead. Uptime is always reconciled as now - start so the documented
// "start + uptime == now" invariant holds regardless of which source wins.
var processStartTime = time.Now()

// goosLinux is the runtime.GOOS value for Linux, the only platform where
// /proc-based process metrics are expected to succeed.
const goosLinux = "linux"

// defaultUserHZ is the USER_HZ fallback used when the auxiliary vector cannot
// be read: 100 is the fixed kernel-to-userspace ABI value on all modern Linux
// architectures, independent of the kernel's internal CONFIG_HZ.
const defaultUserHZ = 100.0

// auxvClkTckTag is the ELF auxiliary-vector tag for AT_CLKTCK: the value of
// sysconf(_SC_CLK_TCK), i.e. USER_HZ, the unit in which the Linux kernel
// reports per-process CPU time in /proc/self/stat.
const auxvClkTckTag = 17

// userHZ returns the kernel's USER_HZ — the clock-tick unit for utime/stime in
// /proc/self/stat and starttime scaling — read once from the process's ELF
// auxiliary vector (AT_CLKTCK in /proc/self/auxv, pure stdlib) and memoized.
// When the auxiliary vector is absent or unreadable (non-Linux platforms, a
// restricted /proc) it falls back to defaultUserHZ, preserving the historical
// assumption that is correct on every modern Linux architecture.
var userHZ = sync.OnceValue(func() float64 {
	if hz := auxvClkTck("/proc/self/auxv"); hz > 0 {
		return float64(hz)
	}
	return defaultUserHZ
})

// auxvClkTck reads the AT_CLKTCK entry from the ELF auxiliary vector at path.
// Returns -1 when the file is unreadable or carries no usable entry.
func auxvClkTck(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	return parseAuxvClkTck(data, bits.UintSize/8)
}

// parseAuxvClkTck scans an ELF auxiliary vector — a sequence of (tag, value)
// pairs of native-endian unsigned words of wordSize bytes, terminated by an
// AT_NULL (0) tag — for the AT_CLKTCK entry. Returns the clock-tick value, or
// -1 when the entry is absent, zero, or the vector is malformed. wordSize must
// be 4 or 8 (the two Linux ABI word sizes).
func parseAuxvClkTck(data []byte, wordSize int) int64 {
	if wordSize != 4 && wordSize != 8 {
		return -1
	}
	word := func(b []byte) uint64 {
		if wordSize == 8 {
			return binary.NativeEndian.Uint64(b)
		}
		return uint64(binary.NativeEndian.Uint32(b))
	}
	for i := 0; i+2*wordSize <= len(data); i += 2 * wordSize {
		tag, val := word(data[i:]), word(data[i+wordSize:])
		switch tag {
		case 0: // AT_NULL terminates the vector
			return -1
		case auxvClkTckTag:
			if val == 0 || val > math.MaxInt64 {
				return -1
			}
			return int64(val)
		}
	}
	return -1
}

// unlimitedMaxFDs is the sentinel stored in processMetricsData.maxFDs when
// /proc/self/limits reports the soft "Max open files" limit as the literal
// token "unlimited". It is distinct from 0 (the absent/read-failure value) so
// an unlimited limit is treated as a successful reading and exposed as
// float64(math.MaxUint64), matching client_golang (see formatMaxFDs), rather
// than silently dropped.
const unlimitedMaxFDs int64 = -1

// Process-metric ok-mask bits. Each bit is set when the corresponding /proc
// read succeeded, so the mask records WHICH process_* series are currently
// collectable, not just whether any read failed.
const (
	procOKCPU uint32 = 1 << iota
	procOKRSS
	procOKOpenFDs
	procOKMaxFDs

	// procOKAll is the all-ok mask: every /proc read succeeded.
	procOKAll = procOKCPU | procOKRSS | procOKOpenFDs | procOKMaxFDs
)

// procDegraded holds the last stored ok-mask. It starts all-ok so the first
// scrape logs only when a read actually failed (never a spurious "recovered").
var procDegraded = newProcDegradedState()

func newProcDegradedState() *atomic.Uint32 {
	var s atomic.Uint32
	s.Store(procOKAll)
	return &s
}

// processMetricsData holds collected process metrics for shared use.
type processMetricsData struct {
	goroutines int
	heapAlloc  uint64
	gcPause    float64
	startTime  float64
	uptime     float64
	cpuSeconds float64
	rss        int64
	openFDs    int
	maxFDs     int64
}

// Series-presence predicates used by processFamilies when materialising the
// process-metric IR, gating the optional series set in one place.
func (d *processMetricsData) hasCPU() bool     { return d.cpuSeconds >= 0 }
func (d *processMetricsData) hasRSS() bool     { return d.rss > 0 }
func (d *processMetricsData) hasOpenFDs() bool { return d.openFDs >= 0 }
func (d *processMetricsData) hasMaxFDs() bool  { return d.maxFDs > 0 || d.maxFDs == unlimitedMaxFDs }

// procOKMask computes the 4-bit ok-mask for process metric collection from the
// series-presence predicates. On platforms other than Linux, where /proc-based
// metrics are not expected, it returns procOKAll so degradation is never
// reported. Failed reads are signalled by the predicates: cpuSeconds < 0,
// rss <= 0, openFDs < 0, or a non-positive maxFDs (excluding the
// unlimitedMaxFDs sentinel, which is a successful "unlimited" reading).
func procOKMask(goos string, d *processMetricsData) uint32 {
	if goos != goosLinux {
		return procOKAll
	}
	var mask uint32
	if d.hasCPU() {
		mask |= procOKCPU
	}
	if d.hasRSS() {
		mask |= procOKRSS
	}
	if d.hasOpenFDs() {
		mask |= procOKOpenFDs
	}
	if d.hasMaxFDs() {
		mask |= procOKMaxFDs
	}
	return mask
}

// procDegradedTransition stores the new ok-mask in s and reports whether this
// call changed it. Callers log only when it returns true, which preserves the
// one-log-per-transition guarantee while making a widening of the failure set
// and a partial recovery observable, not just the healthy<->degraded edges.
func procDegradedTransition(s *atomic.Uint32, mask uint32) bool {
	return s.Swap(mask) != mask
}

// collectProcessMetrics gathers all process metrics into a struct.
func collectProcessMetrics(d *processMetricsData) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	d.goroutines = runtime.NumGoroutine()
	d.heapAlloc = m.HeapAlloc
	d.gcPause = float64(m.PauseTotalNs) / 1e9
	start := resolvedProcStartTime()
	if start < 0 {
		start = float64(processStartTime.Unix())
	}
	now := float64(time.Now().UnixNano()) / 1e9
	d.startTime = start
	d.uptime = now - start
	d.cpuSeconds = readProcCPUSeconds()
	d.rss = readProcRSS()
	d.openFDs, d.maxFDs = readProcFDs()
	mask := procOKMask(runtime.GOOS, d)
	if procDegradedTransition(procDegraded, mask) {
		if mask != procOKAll {
			slog.Warn("process metric collection partially failed; some process_* metrics will be omitted",
				"cpu_ok", mask&procOKCPU != 0, "rss_ok", mask&procOKRSS != 0,
				"fds_ok", mask&procOKOpenFDs != 0, "max_fds_ok", mask&procOKMaxFDs != 0)
		} else {
			slog.Info("process metric collection recovered; process_* metrics restored")
		}
	}
}

// processFamilies materialises Go runtime and standard process metrics into the
// neutral IR. go_goroutines, go_memstats_heap_alloc_bytes,
// process_gc_pause_seconds_total, process_uptime_seconds and
// process_start_time_seconds are emitted on every platform.
// process_cpu_seconds_total, process_resident_memory_bytes,
// process_open_fds and process_max_fds are sourced from /proc and are
// Linux-only; on other platforms they are silently omitted. CPU time is
// divided by USER_HZ (see userHZ).
func processFamilies() []metricFamily {
	var d processMetricsData
	collectProcessMetrics(&d)

	gauge := func(name, help, value string) metricFamily {
		return metricFamily{name: name, typ: typeGauge, help: help, samples: []sample{{value: value}}}
	}
	counter := func(name, help, value string) metricFamily {
		return metricFamily{name: name, typ: typeCounter, help: help, samples: []sample{{value: value}}}
	}

	fams := make([]metricFamily, 0, len(processFamilyNames))
	fams = append(fams,
		gauge(pmGoroutines, helpGoroutines, strconv.Itoa(d.goroutines)),
		gauge(pmHeapAllocBytes, helpHeapAlloc, strconv.FormatUint(d.heapAlloc, 10)),
		counter(pmGCPauseTotal, helpGCPause, formatValue(d.gcPause)),
		gauge(pmUptime, helpUptime, formatValue(d.uptime)),
		gauge(pmStartTime, helpStartTime, formatValue(d.startTime)),
	)
	if d.hasCPU() {
		fams = append(fams, counter(pmCPUTotal, helpCPU, formatValue(d.cpuSeconds)))
	}
	if d.hasRSS() {
		fams = append(fams, gauge(pmResidentBytes, helpResident, strconv.FormatInt(d.rss, 10)))
	}
	if d.hasOpenFDs() {
		fams = append(fams, gauge(pmOpenFDs, helpOpenFDs, strconv.Itoa(d.openFDs)))
		if d.hasMaxFDs() {
			fams = append(fams, gauge(pmMaxFDs, helpMaxFDs, formatMaxFDs(d.maxFDs)))
		}
	}
	return fams
}

// WriteProcess writes Go runtime and standard process metrics in
// Prometheus text format. It is a thin shim over the neutral IR
// (processFamilies) and the Prometheus encoder, preserved as part of the
// package's exported surface.
func WriteProcess(b *strings.Builder) {
	appendPrometheus(b, processFamilies())
}

// readProcCPUSeconds reads /proc/self/stat for utime+stime in seconds. Returns -1 on failure.
func readProcCPUSeconds() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/self/stat failed", "error", err)
		}
		return -1
	}
	return parseProcStatCPU(data, userHZ())
}

// parseProcStatCPU parses /proc/self/stat content for utime+stime in seconds,
// scaled by the given USER_HZ. Returns -1 on failure.
func parseProcStatCPU(data []byte, hz float64) float64 {
	// The comm field is parenthesized and may itself contain spaces and
	// parens, so the fields after it start after the LAST ')'.
	_, afterComm, ok := strings.CutLast(string(data), ")")
	if !ok {
		return -1
	}
	fields := strings.Fields(afterComm)
	// utime is field index 11, stime is 12 (0-indexed from after comm)
	if len(fields) < 13 {
		return -1
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return -1
	}
	return float64(utime+stime) / hz
}

// resolvedProcStartTime memoizes the kernel-derived process start time. Because
// the start time is immutable for the process lifetime, it is resolved once via
// readProcStartTime and reused on every subsequent scrape, avoiding the
// redundant per-scrape reads of /proc/self/stat and /proc/stat. The result
// (including a -1 read failure) is captured on the first call; the caller
// applies the package-init fallback when it is negative.
var (
	procStartTimeOnce sync.Once
	procStartTimeVal  float64
)

func resolvedProcStartTime() float64 {
	procStartTimeOnce.Do(func() {
		procStartTimeVal = readProcStartTime()
	})
	return procStartTimeVal
}

// readProcStartTime returns the true process start time in seconds since the
// Unix epoch, derived from the kernel: the system boot time (btime, from
// /proc/stat) plus the process starttime (field 22 of /proc/self/stat, in clock
// ticks after boot) divided by USER_HZ. This matches how client_golang and
// procfs compute process_start_time_seconds. Returns -1 on any read or parse
// failure (including non-Linux platforms, where the /proc files are absent) so
// the caller can fall back to the package-init anchor.
func readProcStartTime() float64 {
	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/self/stat for start time failed", "error", err)
		}
		return -1
	}
	ticks := parseProcStatStartTime(statData)
	if ticks < 0 {
		return -1
	}
	btimeData, err := os.ReadFile("/proc/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/stat for btime failed", "error", err)
		}
		return -1
	}
	btime := parseProcStatBtime(btimeData)
	if btime < 0 {
		return -1
	}
	return float64(btime) + float64(ticks)/userHZ()
}

// parseProcStatStartTime parses /proc/self/stat content for the process
// starttime (field 22, 1-indexed), in clock ticks since boot. Returns -1 on
// failure. Like parseProcStatCPU it locates the fields after the comm by
// cutting around the final ')', since the comm may itself contain spaces and
// parens; counting from field 3 (state) at slice index 0, starttime sits at
// index 22-3 = 19.
func parseProcStatStartTime(data []byte) int64 {
	_, afterComm, ok := strings.CutLast(string(data), ")")
	if !ok {
		return -1
	}
	fields := strings.Fields(afterComm)
	const startTimeIdx = 19
	if len(fields) <= startTimeIdx {
		return -1
	}
	ticks, err := strconv.ParseInt(fields[startTimeIdx], 10, 64)
	if err != nil || ticks < 0 {
		return -1
	}
	return ticks
}

// parseProcStatBtime parses /proc/stat content for the "btime <epoch>" line,
// the system boot time in seconds since the Unix epoch. Returns -1 when the
// line is absent or carries a malformed value.
func parseProcStatBtime(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		btime, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64)
		if err != nil || btime < 0 {
			return -1
		}
		return btime
	}
	return -1
}

// readProcRSS reads /proc/self/status for VmRSS in bytes. Returns 0 on failure.
func readProcRSS() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/self/status failed", "error", err)
		}
		return 0
	}
	return parseProcStatusRSS(data)
}

// parseProcStatusRSS parses /proc/self/status content for VmRSS in bytes. Returns 0 on failure.
func parseProcStatusRSS(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		val := strings.TrimSpace(after)
		val, _ = strings.CutSuffix(val, " kB")
		val = strings.TrimSpace(val)
		kb, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// readProcFDs returns (open, maxFDs) file descriptors. Returns (-1, 0) on failure.
func readProcFDs() (open int, maxFDs int64) {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: opening /proc/self/fd failed", "error", err)
		}
		return -1, 0
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/self/fd entries failed", "error", err)
		}
		return -1, 0
	}
	// Subtract the fd held by the open directory handle itself, which
	// appears in its own /proc/self/fd listing.
	open = openFDCount(names)
	// Read max from /proc/self/limits
	data, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("measure: reading /proc/self/limits failed", "error", err)
		}
		return open, maxFDs
	}
	return open, parseProcLimitsMaxFDs(data)
}

// parseProcLimitsMaxFDs parses /proc/self/limits content for the soft "Max open
// files" limit in descriptors. Returns the unlimitedMaxFDs sentinel for the
// literal "unlimited" token (a successful reading), and 0 when the line is
// absent, carries no value, or holds a malformed or negative number (a
// negative token must never collide with the unlimitedMaxFDs sentinel). Like parseProcStatCPU
// and parseProcStatusRSS, this is the pure-parse counterpart to its I/O reader
// (readProcFDs), so the limits parsing is unit testable in isolation.
func parseProcLimitsMaxFDs(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "Max open files")
		if !ok {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) >= 1 {
			if strings.EqualFold(fields[0], "unlimited") {
				return unlimitedMaxFDs
			}
			n, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil || n < 0 {
				return 0
			}
			return n
		}
		return 0
	}
	return 0
}

// formatMaxFDs renders the soft "Max open files" limit as a Prometheus gauge
// value. The unlimitedMaxFDs sentinel renders as
// float64(math.MaxUint64) (the token "1.8446744073709552e+19"), matching what
// client_golang emits for an unlimited limit; every other value is its decimal
// form.
func formatMaxFDs(maxFDs int64) string {
	if maxFDs == unlimitedMaxFDs {
		return formatValue(float64(math.MaxUint64))
	}
	return strconv.FormatInt(maxFDs, 10)
}

// openFDCount converts the raw /proc/self/fd entry list into the number of
// descriptors actually open by the process. The directory handle opened to
// read /proc/self/fd appears in its own listing, so it is subtracted; the
// result is floored at 0.
func openFDCount(names []string) int {
	return max(len(names)-1, 0)
}
