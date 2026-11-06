package measure

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

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
