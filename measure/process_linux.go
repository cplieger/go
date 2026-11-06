//go:build linux

package measure

import (
	"log/slog"
	"math/bits"
	"os"
	"sync"
)

// procExpected is the set of process families Linux gives: all of them.
const procExpected = procOKAll

// defaultUserHZ is the USER_HZ fallback used when the auxiliary vector cannot
// be read: 100 is the fixed kernel-to-userspace ABI value on all modern Linux
// architectures, independent of the kernel's internal CONFIG_HZ.
const defaultUserHZ = 100.0

// userHZ returns the kernel's USER_HZ — the clock-tick unit for utime/stime in
// /proc/self/stat and starttime scaling — read once from the process's ELF
// auxiliary vector (AT_CLKTCK in /proc/self/auxv, pure stdlib) and memoized.
// When the auxiliary vector is absent or unreadable (a restricted /proc) it
// falls back to defaultUserHZ, preserving the historical
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

// readPlatformProcess fills d from /proc: each scrape reads /proc/self/stat,
// status, limits and net/netstat once and lists /proc/self/fd.
func readPlatformProcess(d *processMetricsData) {
	stat := readProcFile("/proc/self/stat")
	d.cpuSeconds = parseProcStatCPU(stat, userHZ())
	d.vsize = parseProcStatVsize(stat)
	d.rss = parseProcStatusRSS(readProcFile("/proc/self/status"))
	d.openFDs = readOpenFDs("/proc/self/fd")
	limits := readProcFile("/proc/self/limits")
	d.maxFDs = parseProcLimit(limits, "Max open files")
	d.maxVsize = parseProcLimit(limits, "Max address space")
	d.netRx, d.netTx = parseProcNetstatOctets(readProcFile("/proc/self/net/netstat"))
}

// readProcFile reads one /proc file, or returns nil. A failure is logged at
// Debug.
func readProcFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("measure: reading "+path+" failed", "error", err)
		return nil
	}
	return data
}

// readKernelStartTime returns the true process start time in seconds since the
// Unix epoch, derived from the kernel: the system boot time (btime, from
// /proc/stat) plus the process starttime (field 22 of /proc/self/stat, in clock
// ticks after boot) divided by USER_HZ. This matches how client_golang and
// procfs compute process_start_time_seconds. Returns -1 on any read or parse
// failure so the caller can fall back to the package-init anchor.
func readKernelStartTime() float64 {
	ticks := parseProcStatStartTime(readProcFile("/proc/self/stat"))
	if ticks < 0 {
		return -1
	}
	btime := parseProcStatBtime(readProcFile("/proc/stat"))
	if btime < 0 {
		return -1
	}
	return float64(btime) + float64(ticks)/userHZ()
}
