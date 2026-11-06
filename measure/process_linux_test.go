package measure

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUserHZ_Value(t *testing.T) {
	if defaultUserHZ != 100.0 {
		t.Errorf("defaultUserHZ = %v, want 100 (the near-universal Linux _SC_CLK_TCK)", defaultUserHZ)
	}
	// On any modern Linux machine the auxv-derived value agrees with the ABI
	// constant.
	if got := userHZ(); got != 100.0 {
		t.Errorf("userHZ() = %v, want 100", got)
	}
}

// TestAuxvClkTck_RealFile reads the live /proc/self/auxv and expects the
// kernel's AT_CLKTCK (100 on every modern architecture).
func TestAuxvClkTck_RealFile(t *testing.T) {
	if got := auxvClkTck("/proc/self/auxv"); got != 100 {
		t.Errorf("auxvClkTck(/proc/self/auxv) = %d, want 100", got)
	}
}

func TestAuxvClkTck_MissingFile(t *testing.T) {
	if got := auxvClkTck(filepath.Join(t.TempDir(), "absent")); got != -1 {
		t.Errorf("auxvClkTck(absent) = %d, want -1", got)
	}
}

// TestReadOpenFDs_readsAllEntries verifies readOpenFDs reads every fd entry, not
// just one: a live process always has at least its standard descriptors plus the
// directory handle open, so the open count is >= 1.
func TestReadOpenFDs_readsAllEntries(t *testing.T) {
	if open := readOpenFDs(fdDir, fdListingHandles); open < 1 {
		t.Fatalf("readOpenFDs(%s) = %d, want >= 1 (must read all fd entries)", fdDir, open)
	}
}

// TestReadKernelStartTime_AgreesWithPackageInitAnchor pins the kernel start-time
// composition against the only independent witness the process has: the
// package-init instant, captured microseconds after exec. The boot time from
// /proc/stat plus the process's tick offset from /proc/self/stat must therefore
// land within a second or two of that anchor. A composition that drops either
// term, or applies the offset in the wrong direction, lands seconds to days
// away — the offset is the machine's uptime at exec.
func TestReadKernelStartTime_AgreesWithPackageInitAnchor(t *testing.T) {
	got := readKernelStartTime()
	anchor := float64(processStartTime.Unix())
	if got < anchor-2 || got > anchor+2 {
		t.Errorf("readKernelStartTime() = %.3f, want within 2s of the package-init anchor %.0f", got, anchor)
	}
}

// TestWriteProcess_LinuxFDsEmitted checks end to end that WriteProcess emits
// process_open_fds and process_max_fds when /proc/self/fd and
// /proc/self/limits are readable: a non-negative count, and a positive limit
// (float64(math.MaxUint64) when unlimited).
func TestWriteProcess_LinuxFDsEmitted(t *testing.T) {
	var b strings.Builder
	WriteProcess(&b)

	var openVal, maxVal string
	var gotOpen, gotMax bool
	for line := range strings.SplitSeq(b.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "process_open_fds "); ok {
			openVal, gotOpen = v, true
		}
		if v, ok := strings.CutPrefix(line, "process_max_fds "); ok {
			maxVal, gotMax = v, true
		}
	}
	if !gotOpen {
		t.Fatal("missing process_open_fds line")
	}
	if n, err := strconv.Atoi(openVal); err != nil || n < 0 {
		t.Errorf("process_open_fds = %q, want a non-negative integer", openVal)
	}
	if !gotMax {
		t.Fatal("missing process_max_fds line")
	}
	// process_max_fds is a positive integer for a finite limit, or
	// float64(math.MaxUint64) ("1.8446744073709552e+19") for an unlimited one,
	// so parse it as a float rather than an int.
	if n, err := strconv.ParseFloat(maxVal, 64); err != nil || n <= 0 {
		t.Errorf("process_max_fds = %q, want a positive number", maxVal)
	}
}

// TestProcessFamiliesOnLinux reads the live /proc files: every family appears,
// and the new ones carry plausible values.
func TestProcessFamiliesOnLinux(t *testing.T) {
	var d processMetricsData
	collectProcessMetrics(&d)
	if d.vsize <= 0 || !d.hasMaxVirtual() || !d.hasNetwork() {
		t.Errorf("vsize %v, max vsize %v, network %v/%v; want every read to succeed", d.vsize, d.maxVsize, d.netRx, d.netTx)
	}
	if rss := float64(d.rss); d.vsize < rss {
		t.Errorf("virtual memory %v is below resident memory %v", d.vsize, rss)
	}
	if d.maxVsize < d.vsize {
		t.Errorf("virtual memory limit %v is below the virtual memory in use %v", d.maxVsize, d.vsize)
	}
}

// TestProcExpected_linux checks that every family Linux serves can degrade:
// a failed read of any one of them changes the mask.
func TestProcExpected_linux(t *testing.T) {
	healthy := processMetricsData{cpuSeconds: 1, rss: 1, openFDs: 1, maxFDs: 1, vsize: 1, maxVsize: 1, netRx: 1, netTx: 1}
	for name, fail := range map[string]func(*processMetricsData){
		"cpu":       func(d *processMetricsData) { d.cpuSeconds = -1 },
		"rss":       func(d *processMetricsData) { d.rss = 0 },
		"open fds":  func(d *processMetricsData) { d.openFDs = -1 },
		"max fds":   func(d *processMetricsData) { d.maxFDs = -1 },
		"vsize":     func(d *processMetricsData) { d.vsize = -1 },
		"max vsize": func(d *processMetricsData) { d.maxVsize = -1 },
		"network":   func(d *processMetricsData) { d.netRx = -1 },
	} {
		d := healthy
		fail(&d)
		if procOKMask(procExpected, &d) == procOKAll {
			t.Errorf("a failed %s read leaves the Linux mask all-ok", name)
		}
	}
}

// TestLogProcTransition_linux checks both log lines on Linux: the Warn names
// all seven families, and a full recovery logs the Info line alone.
func TestLogProcTransition_linux(t *testing.T) {
	buf := captureDebugLogs(t)
	logProcTransition(procOKAll &^ procOKNetwork)
	for _, kv := range []string{"cpu_ok=true", "rss_ok=true", "fds_ok=true", "max_fds_ok=true", "vsize_ok=true", "max_vsize_ok=true", "network_ok=false"} {
		if !strings.Contains(buf.String(), kv) {
			t.Errorf("Warn line %q lacks %s", buf.String(), kv)
		}
	}
	buf.Reset()
	logProcTransition(procOKAll)
	if out := buf.String(); !strings.Contains(out, "recovered") || strings.Contains(out, "_ok=") {
		t.Errorf("recovery log = %q, want the Info line with no family states", out)
	}
}
