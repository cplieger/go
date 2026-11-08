//go:build darwin && !ios

package measure

import (
	"strings"
	"syscall"
	"testing"
)

// TestReadPlatformProcess_darwin reads the live process: the four families
// macOS gives without cgo carry plausible values, and the others stay on
// their sentinels.
func TestReadPlatformProcess_darwin(t *testing.T) {
	var d processMetricsData
	unreadProcess(&d)
	readPlatformProcess(&d)
	if d.cpuSeconds < 0 {
		t.Errorf("cpu seconds = %v, want a getrusage reading", d.cpuSeconds)
	}
	if d.openFDs < 1 {
		t.Errorf("open fds = %d, want >= 1 (the standard descriptors)", d.openFDs)
	}
	if d.maxFDs < float64(d.openFDs) {
		t.Errorf("max fds = %v, want at least the %d open", d.maxFDs, d.openFDs)
	}
	if d.maxVsize <= 0 {
		t.Errorf("max vsize = %v, want a getrlimit reading", d.maxVsize)
	}
	if d.hasRSS() || d.hasVirtual() || d.hasNetwork() {
		t.Errorf("rss %d, vsize %v, network %v/%v: want every one unread on macOS", d.rss, d.vsize, d.netRx, d.netTx)
	}
	if got := procOKMask(procExpected, &d); got != procOKAll {
		t.Errorf("procOKMask = %#09b, want procOKAll on a healthy macOS read", got)
	}
}

// TestSoftLimit_darwin checks softLimit against getrlimit's own answer.
func TestSoftLimit_darwin(t *testing.T) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	if got := softLimit(syscall.RLIMIT_NOFILE); got != float64(lim.Cur) {
		t.Errorf("softLimit(RLIMIT_NOFILE) = %v, want %v", got, float64(lim.Cur))
	}
	if got := softLimit(-1); got != -1 {
		t.Errorf("softLimit(-1) = %v, want -1 (a failed call)", got)
	}
}

// TestWriteProcess_darwin checks the page: the four macOS families appear and
// resident memory does not.
func TestWriteProcess_darwin(t *testing.T) {
	var b strings.Builder
	writeProcess(&b)
	out := b.String()
	for _, name := range []string{pmCPUTotal, pmOpenFDs, pmMaxFDs, pmVirtualMaxBytes, pmStartTime} {
		if !strings.Contains(out, "\n"+name+" ") {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{pmResidentBytes, pmVirtualBytes, pmNetReceive, pmNetTransmit} {
		if strings.Contains(out, "\n"+name+" ") {
			t.Errorf("%s is served, want it absent on macOS", name)
		}
	}
}

// TestProcExpected_darwin checks that each family macOS serves can degrade: a
// failed read of any one of them changes the mask.
func TestProcExpected_darwin(t *testing.T) {
	healthy := processMetricsData{cpuSeconds: 1, openFDs: 1, maxFDs: 1, maxVsize: 1, vsize: -1, netRx: -1, netTx: -1}
	for name, fail := range map[string]func(*processMetricsData){
		"cpu":       func(d *processMetricsData) { d.cpuSeconds = -1 },
		"open fds":  func(d *processMetricsData) { d.openFDs = -1 },
		"max fds":   func(d *processMetricsData) { d.maxFDs = -1 },
		"max vsize": func(d *processMetricsData) { d.maxVsize = -1 },
	} {
		d := healthy
		if procOKMask(procExpected, &d) != procOKAll {
			t.Fatalf("the healthy macOS readings %+v do not give procOKAll", d)
		}
		fail(&d)
		if procOKMask(procExpected, &d) == procOKAll {
			t.Errorf("a failed %s read leaves the macOS mask all-ok", name)
		}
	}
}

// TestLogProcTransition_darwin checks that the degradation Warn names only the
// families macOS serves, so it never reports resident memory as healthy.
func TestLogProcTransition_darwin(t *testing.T) {
	buf := captureDebugLogs(t)
	logProcTransition(procOKAll &^ procOKCPU)
	out := buf.String()
	if !strings.Contains(out, "cpu_ok=false") || !strings.Contains(out, "max_vsize_ok=true") {
		t.Errorf("Warn line %q does not name the macOS families' states", out)
	}
	for _, key := range []string{" rss_ok=", " vsize_ok=", " network_ok="} {
		if strings.Contains(out, key) {
			t.Errorf("Warn line %q names %s, a family macOS does not serve", out, key)
		}
	}
}
