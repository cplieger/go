package measure

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// processStartTime is captured at package init as the fallback anchor for
// process_start_time_seconds (and, via it, process_uptime_seconds) when the
// kernel-derived start time cannot be read. On Linux the start time comes from
// /proc (see readKernelStartTime); on every other platform, and when that read
// fails, this package-init instant stands in. Uptime is always reconciled as
// now - start so the documented "start + uptime == now" invariant holds
// regardless of which source wins.
var processStartTime = time.Now()

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
		logProcTransition(mask)
	}
}

// logProcTransition logs a new ok-mask: a Warn naming each expected family's
// state, or one Info line once every read has recovered.
func logProcTransition(mask uint32) {
	if mask == procOKAll {
		slog.Info("process metric collection recovered; process_* metrics restored")
		return
	}
	slog.Warn("process metric collection partially failed; some process_* metrics will be omitted",
		procOKAttrs(procExpected, mask)...)
}

// procOKKeys names each ok-mask bit in the degradation Warn line.
var procOKKeys = [...]struct {
	key string
	bit uint32
}{
	{"cpu_ok", procOKCPU},
	{"rss_ok", procOKRSS},
	{"fds_ok", procOKOpenFDs},
	{"max_fds_ok", procOKMaxFDs},
	{"vsize_ok", procOKVirtual},
	{"max_vsize_ok", procOKMaxVirtual},
	{"network_ok", procOKNetwork},
}

// procOKAttrs returns the Warn line's key-value pairs for the families in
// expected only, so a family the platform never serves is not reported healthy.
func procOKAttrs(expected, mask uint32) []any {
	attrs := make([]any, 0, 2*len(procOKKeys))
	for _, k := range procOKKeys {
		if k.bit&expected != 0 {
			attrs = append(attrs, k.key, mask&k.bit != 0)
		}
	}
	return attrs
}

// unreadProcess sets every reading of d to its failure sentinel.
func unreadProcess(d *processMetricsData) {
	d.cpuSeconds, d.vsize, d.netRx, d.netTx = -1, -1, -1, -1
	d.maxFDs, d.maxVsize, d.rss, d.openFDs = -1, -1, 0, -1
}

// processFamilies renders processGather's families for the page: the Go
// families on every platform, and the process families the platform gives
// when their reads succeed.
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

// readOpenFDs counts the process's open file descriptors by listing dir
// (fdDir), less the handles the listing itself holds (fdListingHandles).
// Readdirnames calls no stat, which a kqueue descriptor fails on macOS.
// Returns -1 on failure.
func readOpenFDs(dir string, handles int) int {
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
	return openFDCount(names, handles)
}

// openFDCount converts the raw descriptor listing into the number of
// descriptors the process holds apart from the listing's own handles, which
// appear in it; the result is floored at 0.
func openFDCount(names []string, handles int) int {
	return max(len(names)-handles, 0)
}
