package measure

import (
	"encoding/binary"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// processStartTime is captured at package init as the fallback anchor for
// process_start_time_seconds (and, via it, process_uptime_seconds) when the
// kernel-derived start time cannot be read. On Linux the true start time comes
// from /proc (see readKernelStartTime). Elsewhere, where reading it needs a
// sysctl the standard library does not wrap, and on any /proc failure, this
// package-init instant is used instead. Uptime is always reconciled as now - start so the documented
// "start + uptime == now" invariant holds regardless of which source wins.
var processStartTime = time.Now()

// auxvClkTckTag is the ELF auxiliary-vector tag for AT_CLKTCK: the value of
// sysconf(_SC_CLK_TCK), i.e. USER_HZ, the unit in which the Linux kernel
// reports per-process CPU time in /proc/self/stat.
const auxvClkTckTag = 17

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

// Process-metric ok-mask bits. Each bit is set when the corresponding read
// succeeded, so the mask records WHICH process_* series are currently
// collectable, not just whether any read failed.
const (
	procOKCPU uint32 = 1 << iota
	procOKRSS
	procOKOpenFDs
	procOKMaxFDs
	procOKVirtual
	procOKMaxVirtual
	procOKNetwork

	// procOKAll is the all-ok mask: every read succeeded.
	procOKAll = procOKCPU | procOKRSS | procOKOpenFDs | procOKMaxFDs |
		procOKVirtual | procOKMaxVirtual | procOKNetwork

	// procOKDarwin is the set macOS gives a program without cgo, as in
	// client_golang: resident and virtual memory need task_info, and no
	// per-process traffic counter exists.
	procOKDarwin = procOKCPU | procOKOpenFDs | procOKMaxFDs | procOKMaxVirtual
)

// procDegraded holds the last stored ok-mask. It starts all-ok so the first
// scrape logs only when a read actually failed (never a spurious "recovered").
var procDegraded = newProcDegradedState()

func newProcDegradedState() *atomic.Uint32 {
	var s atomic.Uint32
	s.Store(procOKAll)
	return &s
}

// processMetricsData holds one scrape's process readings and start time. A
// failed or unsupported read leaves the sentinel its predicate rejects.
type processMetricsData struct {
	startTime  float64
	uptime     float64
	cpuSeconds float64 // -1 when the read failed
	vsize      float64 // -1 when the read failed
	netRx      float64 // -1 when the read failed, as netTx
	netTx      float64
	maxFDs     float64 // -1 when the read failed
	maxVsize   float64 // -1 when the read failed
	rss        int64   // 0 when the read failed
	openFDs    int     // -1 when the read failed
}

// Series-presence predicates used by processFamiliesOf, gating the optional
// series set in one place.
func (d *processMetricsData) hasCPU() bool        { return d.cpuSeconds >= 0 }
func (d *processMetricsData) hasRSS() bool        { return d.rss > 0 }
func (d *processMetricsData) hasOpenFDs() bool    { return d.openFDs >= 0 }
func (d *processMetricsData) hasMaxFDs() bool     { return d.maxFDs >= 0 }
func (d *processMetricsData) hasVirtual() bool    { return d.vsize >= 0 }
func (d *processMetricsData) hasMaxVirtual() bool { return d.maxVsize >= 0 }
func (d *processMetricsData) hasNetwork() bool    { return d.netRx >= 0 && d.netTx >= 0 }

// procOKMask computes the ok-mask for process metric collection from the
// series-presence predicates. A bit outside expected, the set the platform
// gives (procExpected), is always set, so a family the platform lacks is never
// reported as degraded.
func procOKMask(expected uint32, d *processMetricsData) uint32 {
	mask := procOKAll &^ expected
	for _, c := range [...]struct {
		bit uint32
		ok  bool
	}{
		{procOKCPU, d.hasCPU()},
		{procOKRSS, d.hasRSS()},
		{procOKOpenFDs, d.hasOpenFDs()},
		{procOKMaxFDs, d.hasMaxFDs()},
		{procOKVirtual, d.hasVirtual()},
		{procOKMaxVirtual, d.hasMaxVirtual()},
		{procOKNetwork, d.hasNetwork()},
	} {
		if c.ok {
			mask |= c.bit
		}
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

// collectProcessMetrics gathers the start time and the readings the platform
// gives (readPlatformProcess); every other reading keeps its failure sentinel.
func collectProcessMetrics(d *processMetricsData) {
	start := resolvedProcStartTime()
	if start < 0 {
		start = float64(processStartTime.Unix())
	}
	now := float64(time.Now().UnixNano()) / 1e9
	d.startTime = start
	d.uptime = now - start
	unreadProcess(d)
	readPlatformProcess(d)
	mask := procOKMask(procExpected, d)
	if procDegradedTransition(procDegraded, mask) {
		if mask != procOKAll {
			slog.Warn("process metric collection partially failed; some process_* metrics will be omitted",
				"cpu_ok", mask&procOKCPU != 0, "rss_ok", mask&procOKRSS != 0,
				"fds_ok", mask&procOKOpenFDs != 0, "max_fds_ok", mask&procOKMaxFDs != 0,
				"vsize_ok", mask&procOKVirtual != 0, "max_vsize_ok", mask&procOKMaxVirtual != 0,
				"network_ok", mask&procOKNetwork != 0)
		} else {
			slog.Info("process metric collection recovered; process_* metrics restored")
		}
	}
}

// unreadProcess sets every reading of d to its failure sentinel.
func unreadProcess(d *processMetricsData) {
	d.cpuSeconds, d.vsize, d.netRx, d.netTx = -1, -1, -1, -1
	d.maxFDs, d.maxVsize, d.rss, d.openFDs = -1, -1, 0, -1
}

// processFamilies renders processGather's families for the page: the Go
// families on every platform, and the process families the platform gives
// when their reads succeed. CPU time is divided by USER_HZ (see userHZ).
func processFamilies() []metricFamily {
	gathered := processGather()
	fams := make([]metricFamily, 0, len(gathered))
	for i := range gathered {
		if f, ok := pageFamily(&gathered[i]); ok {
			fams = append(fams, f)
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

// procStatFields returns the /proc/self/stat fields after the comm, so index 0
// is field 3 (state). The comm is parenthesized and may itself contain spaces
// and parens, so the fields start after the LAST ')'. It returns nil when no
// ')' is present.
func procStatFields(data []byte) []string {
	_, afterComm, ok := strings.CutLast(string(data), ")")
	if !ok {
		return nil
	}
	return strings.Fields(afterComm)
}

// parseProcStatCPU parses /proc/self/stat content for utime+stime (fields 14
// and 15) in seconds, scaled by the given USER_HZ. Returns -1 on failure.
func parseProcStatCPU(data []byte, hz float64) float64 {
	fields := procStatFields(data)
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

// parseProcStatVsize parses /proc/self/stat content for vsize (field 23), the
// virtual memory size in bytes. Returns -1 on failure.
func parseProcStatVsize(data []byte) float64 {
	const vsizeIdx = 23 - 3
	fields := procStatFields(data)
	if len(fields) <= vsizeIdx {
		return -1
	}
	n, err := strconv.ParseUint(fields[vsizeIdx], 10, 64)
	if err != nil {
		return -1
	}
	return float64(n)
}

// resolvedProcStartTime memoizes the kernel-derived process start time. Because
// the start time is immutable for the process lifetime, it is resolved once via
// readKernelStartTime and reused on every subsequent scrape, avoiding the
// redundant per-scrape reads of /proc/self/stat and /proc/stat. The result
// (including a -1 read failure) is captured on the first call; the caller
// applies the package-init fallback when it is negative.
var (
	procStartTimeOnce sync.Once
	procStartTimeVal  float64
)

func resolvedProcStartTime() float64 {
	procStartTimeOnce.Do(func() {
		procStartTimeVal = readKernelStartTime()
	})
	return procStartTimeVal
}

// parseProcStatStartTime parses /proc/self/stat content for the process
// starttime (field 22), in clock ticks since boot. Returns -1 on failure.
func parseProcStatStartTime(data []byte) int64 {
	const startTimeIdx = 22 - 3
	fields := procStatFields(data)
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

// readOpenFDs counts the process's open file descriptors by listing dir,
// /proc/self/fd on Linux and /dev/fd on macOS. Readdirnames calls no stat,
// which a kqueue descriptor fails on macOS. Returns -1 on failure.
func readOpenFDs(dir string) int {
	f, err := os.Open(dir)
	if err != nil {
		slog.Debug("measure: opening "+dir+" failed", "error", err)
		return -1
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		slog.Debug("measure: reading "+dir+" entries failed", "error", err)
		return -1
	}
	return openFDCount(names)
}

// parseProcLimit parses /proc/self/limits content for the soft value of the
// named limit ("Max open files", "Max address space"), which the kernel prints
// unsigned. The literal "unlimited" reads as float64(math.MaxUint64)
// (rendered "1.8446744073709552e+19"), what client_golang serves. Returns -1
// when the line is absent, carries no value, or holds a malformed number.
func parseProcLimit(data []byte, name string) float64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, name)
		if !ok {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) == 0 {
			return -1
		}
		if strings.EqualFold(fields[0], "unlimited") {
			return float64(math.MaxUint64)
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return -1
		}
		return float64(n)
	}
	return -1
}

// parseProcNetstatOctets parses /proc/self/net/netstat content for the IpExt
// InOctets and OutOctets counters. They count the traffic of the process's
// network namespace, as client_golang's same-named families do. Returns -1, -1
// when either counter is missing or malformed.
func parseProcNetstatOctets(data []byte) (rx, tx float64) {
	keys, vals, ok := procNetstatSection(data, "IpExt:")
	if !ok {
		return -1, -1
	}
	rx, tx = -1, -1
	for j, k := range keys {
		if k != "InOctets" && k != "OutOctets" {
			continue
		}
		n, err := strconv.ParseUint(vals[j], 10, 64)
		if err != nil {
			return -1, -1
		}
		if k == "InOctets" {
			rx = float64(n)
		} else {
			tx = float64(n)
		}
	}
	if rx < 0 || tx < 0 {
		return -1, -1
	}
	return rx, tx
}

// procNetstatSection finds a netstat section: a line of field names, then a
// line of as many values, both starting with prefix. ok is false when the
// section is absent or its value line is missing or does not match.
func procNetstatSection(data []byte, prefix string) (keys, vals []string, ok bool) {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		names, found := strings.CutPrefix(line, prefix)
		if !found {
			continue
		}
		if i+1 == len(lines) {
			return nil, nil, false
		}
		values, found := strings.CutPrefix(lines[i+1], prefix)
		keys, vals = strings.Fields(names), strings.Fields(values)
		return keys, vals, found && len(keys) == len(vals)
	}
	return nil, nil, false
}

// openFDCount converts the raw descriptor listing into the number of
// descriptors actually open by the process. The directory handle opened to
// read the listing appears in it, so it is subtracted; the result is floored
// at 0.
func openFDCount(names []string) int {
	return max(len(names)-1, 0)
}
