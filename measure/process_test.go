package measure

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseProcStatCPU(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{
			name: "comm with spaces and parens",
			// pid (comm with (nested) spaces) state ppid pgrp ... fields[11]=utime fields[12]=stime
			in:   "1234 (weird (proc) name) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0",
			want: 3.0, // (200+100)/100
		},
		{
			// The comm itself ENDS in ')', so the true terminator is the last of
			// two adjacent ones: a split around the first, or around the
			// second-to-last, reads the state field as utime.
			name: "comm ending in a paren",
			in:   "1234 (proc)) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0",
			want: 3.0,
		},
		{
			name: "simple comm",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 50 50 0 0",
			want: 1.0, // (50+50)/100
		},
		{
			name: "too few fields",
			in:   "1234 (cat) S 1 1 1 0 0",
			want: -1,
		},
		{
			name: "no closing paren",
			in:   "1234 cat S 1 1",
			want: -1,
		},
		{
			name: "non-numeric utime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 abc 50 0 0",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatCPU([]byte(tc.in), 100)
			if got != tc.want {
				t.Errorf("parseProcStatCPU(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatStartTime(t *testing.T) {
	// A well-formed /proc/self/stat line: field 22 (starttime) sits at index 19
	// of the after-comm slice. The lines below place 8000000 in that position,
	// among realistic noise for the surrounding fields.
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "simple comm",
			//     pid  comm  st ppid pgrp sess tty tpgid flags min cmin maj cmaj ut st cut cst pri ni thr itreal START
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 8000000",
			want: 8000000,
		},
		{
			name: "comm with spaces and parens",
			in:   "1234 (weird (proc) name) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 42",
			want: 42,
		},
		{
			// The comm itself ends in ')': only a split around the LAST
			// separator lands starttime at index 19.
			name: "comm ending in a paren",
			in:   "1234 (proc)) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 42",
			want: 42,
		},
		{
			name: "too few fields",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100",
			want: -1,
		},
		{
			// Exactly 19 fields after the comm: starttime sits at index 19, so
			// the field set is one short and reading it would run off the end.
			name: "one field short of starttime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0",
			want: -1,
		},
		{
			// A starttime of zero ticks is a reading, not a failure: the guard
			// rejects only negatives, so zero is reported as collected.
			name: "starttime zero",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 0",
			want: 0,
		},
		{
			// The kernel never reports a negative starttime; a file that does is
			// malformed, and must not become a negative tick count downstream.
			name: "negative starttime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 -5",
			want: -1,
		},
		{
			name: "no closing paren",
			in:   "1234 cat S 1 1",
			want: -1,
		},
		{
			name: "trailing paren guarded",
			in:   "1234 (cat)",
			want: -1,
		},
		{
			name: "non-numeric starttime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 abc",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcStatStartTime([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcStatStartTime(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatBtime(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "btime among noise",
			in:   "cpu  100 0 50 900 0 0 0\ncpu0 50 0 25 450\nintr 12345\nbtime 1700000000\nprocesses 4242\n",
			want: 1700000000,
		},
		{
			name: "btime first line",
			in:   "btime 1600000000\n",
			want: 1600000000,
		},
		{
			name: "no btime line",
			in:   "cpu  100 0 50 900\nprocesses 4242\n",
			want: -1,
		},
		{
			name: "malformed value",
			in:   "btime notanumber\n",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcStatBtime([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcStatBtime(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatusRSS(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "VmRSS with kB suffix",
			in:   "Name:\tcat\nVmRSS:\t  1024 kB\nThreads:\t1\n",
			want: 1024 * 1024,
		},
		{
			name: "VmRSS without suffix",
			in:   "VmRSS:\t2048\n",
			want: 2048 * 1024,
		},
		{
			name: "no VmRSS line",
			in:   "Name:\tcat\nThreads:\t1\n",
			want: 0,
		},
		{
			name: "malformed value",
			in:   "VmRSS:\tnotanumber kB\n",
			want: 0,
		},
		{
			name: "empty input",
			in:   "",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatusRSS([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseProcStatusRSS(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestProcOKMask(t *testing.T) {
	healthy := processMetricsData{cpuSeconds: 1.5, rss: 4096, openFDs: 12, maxFDs: 1024, vsize: 8192, maxVsize: float64(math.MaxUint64), netRx: 10, netTx: 20}
	failed := processMetricsData{cpuSeconds: -1, rss: 0, openFDs: -1, maxFDs: -1, vsize: -1, maxVsize: -1, netRx: -1, netTx: -1}
	tests := []struct {
		name string
		goos string
		edit func(*processMetricsData)
		want uint32
	}{
		{"linux all healthy", "linux", func(*processMetricsData) {}, procOKAll},
		{"linux cpu read failed", "linux", func(d *processMetricsData) { d.cpuSeconds = -1 }, procOKAll &^ procOKCPU},
		// cpuSeconds == 0, openFDs == 0, vsize == 0 and zero traffic are
		// HEALTHY readings: the failure guards are strict (< 0), so a <= 0
		// mutation would wrongly degrade them.
		{"linux cpu zero healthy", "linux", func(d *processMetricsData) { d.cpuSeconds = 0 }, procOKAll},
		{"linux rss zero", "linux", func(d *processMetricsData) { d.rss = 0 }, procOKAll &^ procOKRSS},
		{"linux rss negative", "linux", func(d *processMetricsData) { d.rss = -1 }, procOKAll &^ procOKRSS},
		{"linux fds read failed", "linux", func(d *processMetricsData) { d.openFDs = -1 }, procOKAll &^ procOKOpenFDs},
		{"linux fds zero healthy", "linux", func(d *processMetricsData) { d.openFDs = 0 }, procOKAll},
		// A limit of zero is a reading; only -1 is a failed limits read.
		{"linux max fds zero healthy", "linux", func(d *processMetricsData) { d.maxFDs = 0 }, procOKAll},
		{"linux max fds read failed", "linux", func(d *processMetricsData) { d.maxFDs = -1 }, procOKAll &^ procOKMaxFDs},
		{"linux vsize read failed", "linux", func(d *processMetricsData) { d.vsize = -1 }, procOKAll &^ procOKVirtual},
		{"linux vsize zero healthy", "linux", func(d *processMetricsData) { d.vsize = 0 }, procOKAll},
		{"linux max vsize finite healthy", "linux", func(d *processMetricsData) { d.maxVsize = 1 << 30 }, procOKAll},
		{"linux max vsize read failed", "linux", func(d *processMetricsData) { d.maxVsize = -1 }, procOKAll &^ procOKMaxVirtual},
		{"linux network read failed", "linux", func(d *processMetricsData) { d.netRx, d.netTx = -1, -1 }, procOKAll &^ procOKNetwork},
		{"linux network one side failed", "linux", func(d *processMetricsData) { d.netTx = -1 }, procOKAll &^ procOKNetwork},
		{"linux network zero healthy", "linux", func(d *processMetricsData) { d.netRx, d.netTx = 0, 0 }, procOKAll},
		{"linux all failed", "linux", func(d *processMetricsData) { *d = failed }, 0},
		{"non-linux all failed stays quiet", "darwin", func(d *processMetricsData) { *d = failed }, procOKAll},
		{"non-linux healthy", "windows", func(*processMetricsData) {}, procOKAll},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := healthy
			tc.edit(&d)
			if got := procOKMask(tc.goos, &d); got != tc.want {
				t.Errorf("procOKMask(%q, %+v) = %#09b, want %#09b", tc.goos, d, got, tc.want)
			}
		})
	}
}

func TestOpenFDCount(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  int
	}{
		{"empty listing", nil, 0},
		{"only the dir handle", []string{"3"}, 0},
		{"three real fds plus handle", []string{"0", "1", "2", "3"}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openFDCount(tc.names); got != tc.want {
				t.Errorf("openFDCount(%v) = %d, want %d", tc.names, got, tc.want)
			}
		})
	}
}

func TestProcDegradedTransition(t *testing.T) {
	cpuFailed := procOKAll &^ procOKCPU
	cpuRSSFailed := procOKAll &^ (procOKCPU | procOKRSS)
	tests := []struct {
		name    string
		initial uint32
		mask    uint32
		want    bool
	}{
		{"all-ok -> all-ok (no change)", procOKAll, procOKAll, false},
		{"all-ok -> cpu failed (degraded edge)", procOKAll, cpuFailed, true},
		{"cpu failed -> cpu failed (no change)", cpuFailed, cpuFailed, false},
		{"cpu failed -> cpu+rss failed (failure-set widening)", cpuFailed, cpuRSSFailed, true},
		{"cpu+rss failed -> rss failed (partial recovery)", cpuRSSFailed, procOKAll &^ procOKRSS, true},
		{"cpu failed -> all-ok (recovered edge)", cpuFailed, procOKAll, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s atomic.Uint32
			s.Store(tt.initial)
			if got := procDegradedTransition(&s, tt.mask); got != tt.want {
				t.Errorf("procDegradedTransition(%#05b, %#05b) = %v, want %v",
					tt.initial, tt.mask, got, tt.want)
			}
			if got := s.Load(); got != tt.mask {
				t.Errorf("state after transition = %#05b, want %#05b", got, tt.mask)
			}
		})
	}
}

// TestProcPresencePredicates_Boundaries pins each series-presence predicate at
// the value where its `>`/`>=` comparison diverges: a zero CPU, open-FD or
// limit reading is present, a zero RSS reading is absent.
func TestProcPresencePredicates_Boundaries(t *testing.T) {
	if !(&processMetricsData{cpuSeconds: 0}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{cpuSeconds: -1}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=-1) = true, want false")
	}

	if (&processMetricsData{rss: 0}).hasRSS() {
		t.Error("hasRSS(rss=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{rss: 1}).hasRSS() {
		t.Error("hasRSS(rss=1) = false, want true")
	}

	if !(&processMetricsData{openFDs: 0}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{openFDs: -1}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=-1) = true, want false")
	}

	if !(&processMetricsData{maxFDs: 0}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{maxFDs: -1}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=-1) = true, want false")
	}
	if !(&processMetricsData{maxVsize: 0}).hasMaxVirtual() {
		t.Error("hasMaxVirtual(maxVsize=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{maxVsize: -1}).hasMaxVirtual() {
		t.Error("hasMaxVirtual(maxVsize=-1) = true, want false")
	}
}

// A stat line with no comm field starts at the separator, so CutLast's `before`
// is empty while `found` is still true. The fields after it must parse.
func TestParseProcStatCPU_NoCommField(t *testing.T) {
	in := []byte(") 0 0 0 0 0 0 0 0 0 0 0 200 100")
	if got := parseProcStatCPU(in, 100); got != 3.0 {
		t.Errorf("parseProcStatCPU(%q) = %v, want 3.0 (an empty comm must still parse)", in, got)
	}
}

// A line ending at the ')' leaves nothing after the comm, so the field-count
// guard must return -1 rather than indexing an empty slice.
func TestParseProcStatCPU_NothingAfterComm(t *testing.T) {
	in := []byte("1234 (cat)")
	if got := parseProcStatCPU(in, 100); got != -1 {
		t.Errorf("parseProcStatCPU(%q) = %v, want -1 (no fields after the comm)", in, got)
	}
}

func TestUserHZ_Value(t *testing.T) {
	if defaultUserHZ != 100.0 {
		t.Errorf("defaultUserHZ = %v, want 100 (the near-universal Linux _SC_CLK_TCK)", defaultUserHZ)
	}
	// On any modern Linux CI/dev machine the auxv-derived value must agree
	// with the ABI constant; elsewhere the fallback yields the same 100.
	if got := userHZ(); got != 100.0 {
		t.Errorf("userHZ() = %v, want 100", got)
	}
}

// buildAuxv assembles a synthetic ELF auxiliary vector from (tag, value) pairs
// in native endianness at the given word size.
func buildAuxv(wordSize int, pairs ...[2]uint64) []byte {
	buf := make([]byte, 0, len(pairs)*2*wordSize)
	for _, p := range pairs {
		for _, w := range p {
			if wordSize == 8 {
				buf = binary.NativeEndian.AppendUint64(buf, w)
			} else {
				buf = binary.NativeEndian.AppendUint32(buf, uint32(w))
			}
		}
	}
	return buf
}

func TestParseAuxvClkTck(t *testing.T) {
	for _, ws := range []int{4, 8} {
		t.Run(fmt.Sprintf("wordsize_%d", ws), func(t *testing.T) {
			tests := []struct {
				name string
				data []byte
				want int64
			}{
				{"clktck_present", buildAuxv(ws, [2]uint64{6, 4096}, [2]uint64{auxvClkTckTag, 100}, [2]uint64{0, 0}), 100},
				{"clktck_first", buildAuxv(ws, [2]uint64{auxvClkTckTag, 250}, [2]uint64{0, 0}), 250},
				// A vector that ends on a pair boundary carries no AT_NULL, so
				// the final pair is only read if the scan admits the pair that
				// ends exactly at len(data).
				{"clktck_in_final_pair_unterminated", buildAuxv(ws, [2]uint64{6, 4096}, [2]uint64{auxvClkTckTag, 250}), 250},
				{"terminated_before_clktck", buildAuxv(ws, [2]uint64{6, 4096}, [2]uint64{0, 0}, [2]uint64{auxvClkTckTag, 100}), -1},
				{"absent", buildAuxv(ws, [2]uint64{6, 4096}, [2]uint64{0, 0}), -1},
				{"zero_value_rejected", buildAuxv(ws, [2]uint64{auxvClkTckTag, 0}, [2]uint64{0, 0}), -1},
				{"empty", nil, -1},
				{"truncated_pair", buildAuxv(ws, [2]uint64{6, 4096})[:ws+1], -1},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					if got := parseAuxvClkTck(tc.data, ws); got != tc.want {
						t.Errorf("parseAuxvClkTck(%s, %d) = %d, want %d", tc.name, ws, got, tc.want)
					}
				})
			}
		})
	}
	if got := parseAuxvClkTck(buildAuxv(8, [2]uint64{auxvClkTckTag, 100}), 5); got != -1 {
		t.Errorf("parseAuxvClkTck with unsupported word size = %d, want -1", got)
	}
}

// TestParseAuxvClkTck_ValueRangeBoundary pins the int64 conversion boundary: a
// tick value that is exactly representable as an int64 is reported, and only a
// value ABOVE that is rejected as unusable. Both discriminating values need a
// 64-bit word, so they sit outside the word-size loop above (a 4-byte word
// cannot carry either).
func TestParseAuxvClkTck_ValueRangeBoundary(t *testing.T) {
	tests := []struct {
		name string
		val  uint64
		want int64
	}{
		{"max_int64_reported", math.MaxInt64, math.MaxInt64},
		{"one_above_max_int64_rejected", math.MaxInt64 + 1, -1},
		{"max_uint64_rejected", math.MaxUint64, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := buildAuxv(8, [2]uint64{auxvClkTckTag, tc.val}, [2]uint64{0, 0})
			if got := parseAuxvClkTck(data, 8); got != tc.want {
				t.Errorf("parseAuxvClkTck(AT_CLKTCK=%d, 8) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

// TestAuxvClkTck_RealFile reads the live /proc/self/auxv on Linux and expects
// the kernel's AT_CLKTCK (100 on every modern architecture); elsewhere the
// reader must return -1.
func TestAuxvClkTck_RealFile(t *testing.T) {
	got := auxvClkTck("/proc/self/auxv")
	if runtime.GOOS == goosLinux {
		if got != 100 {
			t.Errorf("auxvClkTck(/proc/self/auxv) = %d, want 100", got)
		}
	} else if got != -1 {
		t.Errorf("auxvClkTck on non-Linux = %d, want -1", got)
	}
}

func TestAuxvClkTck_MissingFile(t *testing.T) {
	if got := auxvClkTck(filepath.Join(t.TempDir(), "absent")); got != -1 {
		t.Errorf("auxvClkTck(absent) = %d, want -1", got)
	}
}

// TestParseProcStatCPU_DivisorIsUserHZ verifies parseProcStatCPU scales
// utime+stime by the supplied USER_HZ: the result equals (utime+stime)/hz
// exactly.
func TestParseProcStatCPU_DivisorIsUserHZ(t *testing.T) {
	const utime, stime = 200, 100
	stat := []byte("1 (test proc) S 0 0 0 0 0 0 0 0 0 0 200 100")

	got := parseProcStatCPU(stat, 100)
	want := float64(utime+stime) / 100
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want (utime+stime)/hz = %v", got, want)
	}
	if math.Abs(got-3.0) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want 3.0 (300 ticks / 100)", got)
	}
	if got := parseProcStatCPU(stat, 250); math.Abs(got-1.2) > 1e-12 {
		t.Errorf("parseProcStatCPU at hz=250 = %v, want 1.2 (300 ticks / 250)", got)
	}
}

// TestParseProcLimit pins the soft-limit parser, including the "single field"
// boundary: with exactly one field after the label, the parser reports it.
func TestParseProcLimit(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		// Real kernel shape: soft, hard, then the "files" unit. fields[0] is the
		// soft limit this parser reports.
		{"soft hard unit", "Max open files            1024                 4096                 files\n", 1024},
		{"single field", "Max open files 4096\n", 4096},
		{"zero is a reading", "Max open files 0 4096 files\n", 0},
		// The kernel prints the limit unsigned, so a finite limit above 2^63 is
		// a reading, as procfs parses it.
		{"above int64", "Max open files 9223372036854775808 unlimited files\n", 9223372036854775808},
		{"largest finite", "Max open files 18446744073709551614 unlimited files\n", 18446744073709551614},
		{"label only", "Max open files\n", -1},
		{"absent", "Max locked memory         0                    0                    bytes\n", -1},
		// "unlimited" is a valid, successful reading, served as client_golang
		// serves it ("1.8446744073709552e+19").
		{"unlimited soft", "Max open files            unlimited            unlimited            files\n", float64(math.MaxUint64)},
		{"unlimited mixed case", "Max open files Unlimited\n", float64(math.MaxUint64)},
		{"malformed value", "Max open files notanumber\n", -1},
		{"negative value", "Max open files  -1  4096  files\n", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcLimit([]byte(tc.in), "Max open files"); got != tc.want {
				t.Errorf("parseProcLimit(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
	if got := formatValue(float64(math.MaxUint64)); got != "1.8446744073709552e+19" {
		t.Errorf("an unlimited limit renders %q, want client_golang's 1.8446744073709552e+19", got)
	}
}

// TestReadProcOpenFDs_readsAllEntries verifies readProcOpenFDs reads every fd entry, not
// just one: a live process always has at least its standard descriptors plus the
// directory handle open, so the open count is >= 1.
func TestReadProcOpenFDs_readsAllEntries(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd)")
	}
	if open := readProcOpenFDs(); open < 1 {
		t.Fatalf("readProcOpenFDs() = %d, want >= 1 (must read all fd entries)", open)
	}
}

func TestProcGCPauseSeconds_ScaledToSeconds(t *testing.T) {
	runtime.GC()
	runtime.GC() // guarantee PauseTotalNs > 0

	var b strings.Builder
	WriteProcess(&b)

	var val float64
	var found bool
	for line := range strings.SplitSeq(b.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "process_gc_pause_seconds_total "); ok {
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("process_gc_pause_seconds_total value %q not a float: %v", v, err)
			}
			val, found = parsed, true
		}
	}
	if !found {
		t.Fatal("missing process_gc_pause_seconds_total line")
	}
	// Cumulative GC pause for a test process is far below a second; a value
	// scaled in nanoseconds (no /1e9) would be >= 1e9. Any threshold between
	// the two distinguishes the seconds-scaled value from an unscaled one.
	if val < 0 || val > 1e6 {
		t.Errorf("process_gc_pause_seconds_total = %v, want a small non-negative seconds value", val)
	}
}

// TestReadProcStartTime_AgreesWithPackageInitAnchor pins the kernel start-time
// composition against the only independent witness the process has: the
// package-init instant, captured microseconds after exec. The boot time from
// /proc/stat plus the process's tick offset from /proc/self/stat must therefore
// land within a second or two of that anchor. A composition that drops either
// term, or applies the offset in the wrong direction, lands seconds to days
// away — the offset is the machine's uptime at exec.
func TestReadProcStartTime_AgreesWithPackageInitAnchor(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("kernel-derived start time is Linux-only (/proc/self/stat, /proc/stat)")
	}
	got := readProcStartTime()
	anchor := float64(processStartTime.Unix())
	if got < anchor-2 || got > anchor+2 {
		t.Errorf("readProcStartTime() = %.3f, want within 2s of the package-init anchor %.0f", got, anchor)
	}
}

func TestWriteProcess(t *testing.T) {
	var b strings.Builder
	WriteProcess(&b)
	out := b.String()
	for _, want := range []string{
		"go_goroutines",
		"go_memstats_heap_alloc_bytes",
		"process_gc_pause_seconds_total",
		"process_uptime_seconds",
		"process_start_time_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteProcess missing %q", want)
		}
	}
}

func TestWriteProcess_uptimeAndStartTimeReconcile(t *testing.T) {
	// process_uptime_seconds is derived as now - process_start_time_seconds (the
	// start time coming from the kernel on Linux, or the package-init fallback
	// otherwise), so start + uptime must reconcile with now regardless of source.
	var b strings.Builder
	WriteProcess(&b)
	out := b.String()

	var uptime, start float64
	var gotUptime, gotStart bool
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "process_uptime_seconds "); ok {
			uptime, _ = strconv.ParseFloat(v, 64)
			gotUptime = true
		}
		if v, ok := strings.CutPrefix(line, "process_start_time_seconds "); ok {
			start, _ = strconv.ParseFloat(v, 64)
			gotStart = true
		}
	}
	if !gotUptime || !gotStart {
		t.Fatal("missing process_uptime_seconds or process_start_time_seconds")
	}
	if uptime < 0 {
		t.Errorf("uptime = %.3f, want >= 0", uptime)
	}
	now := float64(time.Now().Unix())
	if diff := now - (start + uptime); diff < -2 || diff > 2 {
		t.Errorf("start(%.0f) + uptime(%.3f) = %.3f, want ~= now(%.0f); diff=%.3f",
			start, uptime, start+uptime, now, diff)
	}
}

// TestWriteProcess_LinuxFDsEmitted checks end to end that WriteProcess emits
// process_open_fds and process_max_fds when /proc/self/fd and
// /proc/self/limits are readable: a non-negative count, and a positive limit
// (float64(math.MaxUint64) when unlimited).
func TestWriteProcess_LinuxFDsEmitted(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd, /proc/self/limits)")
	}
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

// TestCollectProcessMetrics_LogsRecoveryTransition pins the recovery half of
// the one-log-per-transition contract end-to-end: a scrape whose ok-mask
// returns to all-ok after a degraded scrape logs the Info recovery line
// exactly once, and a subsequent healthy scrape stays silent. Serial: it
// captures slog.Default and mutates the package-level procDegraded state.
func TestCollectProcessMetrics_LogsRecoveryTransition(t *testing.T) {
	// Settle the environment's real mask first; the test needs an all-ok
	// environment so the injected degraded mask transitions back to healthy.
	var d processMetricsData
	collectProcessMetrics(&d)
	if procDegraded.Load() != procOKAll {
		t.Skip("process metric collection degraded in this environment")
	}

	buf := captureDebugLogs(t)
	t.Cleanup(func() { procDegraded.Store(procOKAll) })
	procDegraded.Store(procOKAll &^ procOKCPU) // simulate a prior degraded scrape

	collectProcessMetrics(&d)

	if logs := buf.String(); !strings.Contains(logs, "process metric collection recovered") {
		t.Fatalf("logs after degraded->healthy scrape = %q, want the recovery Info line", logs)
	}
	if got := procDegraded.Load(); got != procOKAll {
		t.Errorf("procDegraded after recovery = %#05b, want procOKAll", got)
	}

	// One log per transition: a second healthy scrape must not re-log.
	collectProcessMetrics(&d)
	if got := strings.Count(buf.String(), "process metric collection recovered"); got != 1 {
		t.Errorf("recovery logged %d times across two healthy scrapes, want exactly 1", got)
	}
}

// TestCollectProcessMetrics_StartTimeFallbackToPackageInit forces the memoized
// kernel start time to its -1 failure sentinel and verifies collectProcessMetrics
// falls back to the package-init anchor while preserving the documented
// "start + uptime == now" reconciliation. Serial: it mutates the package-level
// memoized procStartTimeVal.
func TestCollectProcessMetrics_StartTimeFallbackToPackageInit(t *testing.T) {
	resolvedProcStartTime() // ensure the sync.Once has fired before overriding
	prev := procStartTimeVal
	t.Cleanup(func() { procStartTimeVal = prev })
	procStartTimeVal = -1

	var d processMetricsData
	collectProcessMetrics(&d)

	want := float64(processStartTime.Unix())
	if d.startTime != want {
		t.Errorf("startTime = %v, want package-init fallback %v", d.startTime, want)
	}
	now := float64(time.Now().Unix())
	if diff := now - (d.startTime + d.uptime); diff < -2 || diff > 2 {
		t.Errorf("start(%v) + uptime(%v) = %v, want ~= now(%v); diff=%v",
			d.startTime, d.uptime, d.startTime+d.uptime, now, diff)
	}
}

// TestCollectProcessMetrics_ZeroKernelStartTimeIsNotAFailure pins the polarity
// of the fallback guard: only the negative read-failure sentinel sends
// collectProcessMetrics to the package-init anchor, so a memoized kernel start
// time of exactly zero is used as collected. Serial: it mutates the
// package-level memoized procStartTimeVal.
func TestCollectProcessMetrics_ZeroKernelStartTimeIsNotAFailure(t *testing.T) {
	resolvedProcStartTime() // ensure the sync.Once has fired before overriding
	prev := procStartTimeVal
	t.Cleanup(func() { procStartTimeVal = prev })
	procStartTimeVal = 0

	var d processMetricsData
	collectProcessMetrics(&d)

	if d.startTime != 0 {
		t.Errorf("startTime = %v, want 0 (a zero kernel start time is a reading, not the -1 failure sentinel)", d.startTime)
	}
}

// statLine builds a /proc/self/stat line with the given comm and fields 3 to
// 23, field 23 (vsize) last.
func statLine(comm, vsize string) string {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = strconv.Itoa(i + 3)
	}
	return "1234 (" + comm + ") " + strings.Join(fields, " ") + " " + vsize + " 77 0"
}

func TestParseProcStatVsize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{"simple comm", statLine("cat", "1683972096"), 1683972096},
		// The comm may hold spaces and parens; fields count from the last ')'.
		{"comm with spaces and parens", statLine("weird (proc) name)", "4096"), 4096},
		{"zero is a reading", statLine("cat", "0"), 0},
		{"above int64", statLine("cat", "18446744073709551615"), float64(math.MaxUint64)},
		{"non-numeric", statLine("cat", "abc"), -1},
		{"negative", statLine("cat", "-1"), -1},
		{"too few fields", "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0", -1},
		{"no closing paren", "1234 cat S 1 1", -1},
		{"empty input", "", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcStatVsize([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcStatVsize(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseProcLimit_addressSpace reads the second limit the families use, in
// the kernel's own layout, beside a "Max open files" line it must not take.
func TestParseProcLimit_addressSpace(t *testing.T) {
	limits := "Limit                     Soft Limit           Hard Limit           Units     \n" +
		"Max open files            1048576              1048576              files     \n" +
		"Max address space         %s            unlimited            bytes     \n"
	for in, want := range map[string]float64{
		"unlimited":           float64(math.MaxUint64),
		"8589934592":          8589934592,
		"9223372036854775808": 9223372036854775808,
		"bogus":               -1,
	} {
		data := []byte(fmt.Sprintf(limits, in))
		if got := parseProcLimit(data, "Max address space"); got != want {
			t.Errorf("parseProcLimit(address space %s) = %v, want %v", in, got, want)
		}
		if got := parseProcLimit(data, "Max open files"); got != 1048576 {
			t.Errorf("parseProcLimit(open files) = %v beside address space %s, want 1048576", got, in)
		}
	}
	if got := parseProcLimit(nil, "Max address space"); got != -1 {
		t.Errorf("parseProcLimit(nil) = %v, want -1 (a failed read)", got)
	}
}

func TestParseProcNetstatOctets(t *testing.T) {
	const tcp = "TcpExt: SyncookiesSent SyncookiesRecv\nTcpExt: 0 0\n"
	const ipExt = "IpExt: InNoRoutes InOctets OutOctets InMcastOctets\n"
	tests := []struct {
		name   string
		in     string
		rx, tx float64
	}{
		{"kernel layout", tcp + ipExt + "IpExt: 0 14284339956 19615187908 0\nMPTcpExt: A\nMPTcpExt: 1\n", 14284339956, 19615187908},
		{"zero traffic is a reading", ipExt + "IpExt: 0 0 0 0\n", 0, 0},
		{"no IpExt section", tcp, -1, -1},
		{"header without values", tcp + ipExt, -1, -1},
		{"values line of another section", ipExt + "TcpExt: 0 1 2 3\n", -1, -1},
		{"field count mismatch", ipExt + "IpExt: 0 1 2\n", -1, -1},
		{"non-numeric octets", ipExt + "IpExt: 0 x 2 3\n", -1, -1},
		{"non-numeric other field is ignored", ipExt + "IpExt: x 1 2 3\n", 1, 2},
		{"no OutOctets", "IpExt: InOctets\nIpExt: 5\n", -1, -1},
		{"empty input", "", -1, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rx, tx := parseProcNetstatOctets([]byte(tc.in))
			if rx != tc.rx || tx != tc.tx {
				t.Errorf("parseProcNetstatOctets(%q) = %v, %v, want %v, %v", tc.in, rx, tx, tc.rx, tc.tx)
			}
		})
	}
}

// TestProcessFamiliesOnLinux reads the live /proc files: every family appears,
// and the new ones carry plausible values.
func TestProcessFamiliesOnLinux(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("the /proc families are Linux-only")
	}
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
