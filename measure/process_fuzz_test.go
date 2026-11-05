package measure

import (
	"strconv"
	"testing"
)

// FuzzParseProcStatCPU_CommRobustness checks that an arbitrary comm field (which
// may embed spaces, parens, and newlines) never corrupts utime/stime indexing:
// parseProcStatCPU keys off the LAST ')', so with utime=200, stime=100 at the
// fixed trailing positions the result stays 3.0s for any comm.
func FuzzParseProcStatCPU_CommRobustness(f *testing.F) {
	f.Add("cat")
	f.Add("weird (proc) name")
	f.Add("has)trailing")
	f.Add("")
	f.Add("new\nline")
	f.Fuzz(func(t *testing.T, comm string) {
		stat := []byte("1234 (" + comm + ") S 0 0 0 0 0 0 0 0 0 0 200 100")
		if got := parseProcStatCPU(stat, 100); got != 3.0 {
			t.Errorf("parseProcStatCPU with comm %q = %v, want 3.0", comm, got)
		}
	})
}

// FuzzParseProcStatusRSS_ValueAmongNoise checks that a known VmRSS line placed
// first is parsed correctly regardless of arbitrary trailing status noise. kb is
// bounded to uint16 so kB*1024 cannot overflow int64.
func FuzzParseProcStatusRSS_ValueAmongNoise(f *testing.F) {
	f.Add(uint16(1024), "Threads:\t1\n")
	f.Add(uint16(0), "")
	f.Add(uint16(65535), "VmHWM:\t100 kB\nName:\tcat\n")
	f.Fuzz(func(t *testing.T, kb uint16, noise string) {
		data := []byte("VmRSS:\t" + strconv.FormatUint(uint64(kb), 10) + " kB\n" + noise)
		want := int64(kb) * 1024
		if got := parseProcStatusRSS(data); got != want {
			t.Errorf("parseProcStatusRSS kb=%d noise=%q = %d, want %d", kb, noise, got, want)
		}
	})
}

// FuzzParseProcLimit_ValueAmongNoise checks that a known "Max open files"
// line placed first is parsed to its soft-limit field regardless of arbitrary
// trailing /proc/self/limits noise, and exercises the discarded-error path of
// strconv.ParseInt. n is bounded to uint32 so the value is always a valid
// non-negative int64; the parser returns at the FIRST "Max open files" match,
// so the leading known line fixes the expected result.
func FuzzParseProcLimit_ValueAmongNoise(f *testing.F) {
	f.Add(uint32(1024), "Max locked memory  0  0  bytes\n")
	f.Add(uint32(0), "")
	f.Add(uint32(4096), "Max stack size  8388608  unlimited  bytes\n")
	f.Fuzz(func(t *testing.T, n uint32, noise string) {
		data := []byte("Max open files  " + strconv.FormatUint(uint64(n), 10) + "  " +
			strconv.FormatUint(uint64(n), 10) + "  files\n" + noise)
		if got := parseProcLimit(data, "Max open files"); got != int64(n) {
			t.Errorf("parseProcLimit(n=%d, noise=%q) = %d, want %d", n, noise, got, int64(n))
		}
	})
}

// FuzzParseProcStatStartTime_CommRobustness checks that an arbitrary comm
// field (which may embed spaces, parens, and newlines) never corrupts the
// field-22 starttime extraction: parseProcStatStartTime keys off the LAST
// ')', so with starttime fixed at its trailing position the result stays
// 8000000 ticks for any comm. Mirrors FuzzParseProcStatCPU_CommRobustness.
func FuzzParseProcStatStartTime_CommRobustness(f *testing.F) {
	f.Add("cat")
	f.Add("weird (proc) name")
	f.Add("has)trailing")
	f.Add("")
	f.Add("new\nline")
	f.Fuzz(func(t *testing.T, comm string) {
		stat := []byte("1234 (" + comm + ") S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 8000000")
		if got := parseProcStatStartTime(stat); got != 8000000 {
			t.Errorf("parseProcStatStartTime with comm %q = %d, want 8000000", comm, got)
		}
	})
}

// FuzzParseProcStatBtime_ValueAmongNoise checks that a known "btime <epoch>"
// line placed first is parsed to its value regardless of arbitrary trailing
// /proc/stat noise. parseProcStatBtime returns at the FIRST "btime " match,
// so the leading known line fixes the expected result and trailing noise can
// never override it. n is bounded to uint32 so the value is always a valid
// non-negative int64. Completes the /proc parser fuzz set alongside
// parseProcStatCPU, parseProcStatusRSS and parseProcLimit.
func FuzzParseProcStatBtime_ValueAmongNoise(f *testing.F) {
	f.Add(uint32(1700000000), "processes 4242\nctxt 99\n")
	f.Add(uint32(0), "")
	f.Add(uint32(1600000000), "cpu  100 0 50 900\nintr 12345\n")
	f.Fuzz(func(t *testing.T, n uint32, noise string) {
		data := []byte("btime " + strconv.FormatUint(uint64(n), 10) + "\n" + noise)
		if got := parseProcStatBtime(data); got != int64(n) {
			t.Errorf("parseProcStatBtime(n=%d, noise=%q) = %d, want %d", n, noise, got, int64(n))
		}
	})
}

// FuzzParseProcStatVsize_CommRobustness checks that an arbitrary comm never
// corrupts the field-23 vsize extraction, as for the CPU fields.
func FuzzParseProcStatVsize_CommRobustness(f *testing.F) {
	f.Add("cat")
	f.Add("a (b) c")
	f.Add("x) 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 (y")
	f.Fuzz(func(t *testing.T, comm string) {
		if got := parseProcStatVsize([]byte(statLine(comm, "123456789"))); got != 123456789 {
			t.Errorf("parseProcStatVsize(comm=%q) = %v, want 123456789", comm, got)
		}
	})
}

// FuzzParseProcNetstatOctets_ValueAmongNoise checks that a known IpExt section
// placed first is read whatever follows it, and that any input parses without
// a panic.
func FuzzParseProcNetstatOctets_ValueAmongNoise(f *testing.F) {
	f.Add(uint64(1), uint64(2), "MPTcpExt: A\nMPTcpExt: 1\n")
	f.Add(uint64(0), uint64(0), "")
	f.Add(uint64(1<<63), uint64(5), "IpExt: InOctets OutOctets\nIpExt: 9 9\n")
	f.Fuzz(func(t *testing.T, rx, tx uint64, noise string) {
		data := "IpExt: InNoRoutes InOctets OutOctets\nIpExt: 0 " +
			strconv.FormatUint(rx, 10) + " " + strconv.FormatUint(tx, 10) + "\n" + noise
		gotRx, gotTx := parseProcNetstatOctets([]byte(data))
		if gotRx != float64(rx) || gotTx != float64(tx) {
			t.Errorf("parseProcNetstatOctets(%d, %d, noise=%q) = %v, %v", rx, tx, noise, gotRx, gotTx)
		}
		parseProcNetstatOctets([]byte(noise))
	})
}
