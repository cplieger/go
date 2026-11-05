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
	tests := []struct {
		name       string
		goos       string
		cpuSeconds float64
		rss        int64
		openFDs    int
		maxFDs     int64
		want       uint32
	}{
		{"linux all healthy", "linux", 1.5, 4096, 12, 1024, procOKAll},
		{"linux cpu read failed", "linux", -1, 4096, 12, 1024, procOKAll &^ procOKCPU},
		// cpuSeconds == 0 and openFDs == 0 are HEALTHY readings: the failure
		// guards are strict (< 0), so a <= 0 mutation would wrongly degrade them.
		{"linux cpu zero healthy", "linux", 0, 4096, 12, 1024, procOKAll},
		{"linux rss zero", "linux", 1.5, 0, 12, 1024, procOKAll &^ procOKRSS},
		{"linux rss negative", "linux", 1.5, -1, 12, 1024, procOKAll &^ procOKRSS},
		{"linux fds read failed", "linux", 1.5, 4096, -1, 1024, procOKAll &^ procOKOpenFDs},
		{"linux fds zero healthy", "linux", 1.5, 4096, 0, 1024, procOKAll},
		// maxFDs: the unlimited sentinel is a successful reading (not degraded);
		// a non-positive maxFDs that is NOT the sentinel is a failed limits read.
		{"linux max fds unlimited healthy", "linux", 1.5, 4096, 12, unlimitedMaxFDs, procOKAll},
		{"linux max fds read failed", "linux", 1.5, 4096, 12, 0, procOKAll &^ procOKMaxFDs},
		{"linux all failed", "linux", -1, 0, -1, 0, 0},
		{"non-linux all failed stays quiet", "darwin", -1, 0, -1, 0, procOKAll},
		{"non-linux healthy", "windows", 1.5, 4096, 12, 1024, procOKAll},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &processMetricsData{cpuSeconds: tc.cpuSeconds, rss: tc.rss, openFDs: tc.openFDs, maxFDs: tc.maxFDs}
			got := procOKMask(tc.goos, d)
			if got != tc.want {
				t.Errorf("procOKMask(%q, {cpu:%v rss:%d fds:%d max:%d}) = %#05b, want %#05b",
					tc.goos, tc.cpuSeconds, tc.rss, tc.openFDs, tc.maxFDs, got, tc.want)
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
// the value where its `>`/`>=` comparison diverges: a zero CPU/openFDs reading
// is present, a zero RSS/maxFDs reading is absent.
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

	if (&processMetricsData{maxFDs: 0}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{maxFDs: 1}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=1) = false, want true")
	}
	// The unlimited sentinel (-1) is a present, successful reading despite being
	// non-positive, so hasMaxFDs must accept it via the sentinel branch.
	if !(&processMetricsData{maxFDs: unlimitedMaxFDs}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=unlimitedMaxFDs) = false, want true (sentinel branch)")
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

// TestParseProcLimitsMaxFDs pins the "single field" boundary: with exactly one
// field after the "Max open files" label, the parser reports it (the guard is
// len(fields) >= 1, not > 1).
func TestParseProcLimitsMaxFDs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		// Real kernel shape: soft, hard, then the "files" unit. fields[0] is the
		// soft limit this parser reports.
		{"soft hard unit", "Max open files            1024                 4096                 files\n", 1024},
		{"single field", "Max open files 4096\n", 4096},
		{"label only", "Max open files\n", 0},
		{"absent", "Max locked memory         0                    0                    bytes\n", 0},
		// "unlimited" is a valid, successful reading, mapped to the sentinel.
		{"unlimited soft", "Max open files            unlimited            unlimited            files\n", unlimitedMaxFDs},
		{"unlimited mixed case", "Max open files Unlimited\n", unlimitedMaxFDs},
		// A malformed (non-numeric, non-"unlimited") value is a failed read.
		{"malformed value", "Max open files notanumber\n", 0},
		// A malformed negative value is a failed read, never a collision with
		// the unlimitedMaxFDs sentinel (-1).
		{"negative malformed", "Max open files  -1  4096  files\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcLimitsMaxFDs([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcLimitsMaxFDs(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestMaxFDsValue pins the exposition rendering: the unlimited sentinel becomes
// float64(math.MaxUint64) ("1.8446744073709552e+19", what client_golang emits
// for an unlimited limit); every other value is decimal.
func TestMaxFDsValue(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"unlimited sentinel", unlimitedMaxFDs, "1.8446744073709552e+19"},
		{"finite limit", 1024, "1024"},
		{"zero", 0, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(maxFDsValue(tc.in)); got != tc.want {
				t.Errorf("formatValue(maxFDsValue(%d)) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReadProcFDs_readsAllEntries verifies readProcFDs reads every fd entry, not
// just one: a live process always has at least its standard descriptors plus the
// directory handle open, so the open count is >= 1.
func TestReadProcFDs_readsAllEntries(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd)")
	}
	open, _ := readProcFDs()
	if open < 1 {
		t.Fatalf("readProcFDs() open = %d, want >= 1 (must read all fd entries)", open)
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

// TestWriteProcess_LinuxFDsEmitted pins the Linux-only fd emit wiring in
// processFamilies: when /proc/self/fd and /proc/self/limits are readable,
// WriteProcess emits process_open_fds and process_max_fds, the latter
// rendered through maxFDsValue (a positive integer, or the
// float64(math.MaxUint64) sentinel for an unlimited soft limit). The
// always-present-metric assertions in TestWriteProcess
// never check these two series, so the hasOpenFDs/hasMaxFDs emit composition and
// the maxFDsValue call site are otherwise unexercised end-to-end.
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
	// process_max_fds is either a positive integer (a finite limit) or the
	// unlimited sentinel rendered as float64(math.MaxUint64)
	// ("1.8446744073709552e+19"), so parse it as a float rather than an int.
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
