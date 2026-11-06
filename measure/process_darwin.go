//go:build darwin && !ios

package measure

import "syscall"

// procExpected is the set of process families macOS gives (procOKDarwin).
const procExpected = procOKDarwin

// readPlatformProcess fills the macOS readings client_golang serves without
// cgo: CPU time from getrusage, open descriptors from /dev/fd and the two soft
// limits from getrlimit.
func readPlatformProcess(d *processMetricsData) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err == nil {
		d.cpuSeconds = float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e9
	}
	d.openFDs = readOpenFDs("/dev/fd")
	d.maxFDs = softLimit(syscall.RLIMIT_NOFILE)
	d.maxVsize = softLimit(syscall.RLIMIT_AS)
}

// softLimit returns the soft limit of resource as getrlimit reports it, so
// "unlimited" reads as RLIM_INFINITY (2^63-1), what client_golang serves.
// Returns -1 when the call fails.
func softLimit(resource int) float64 {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(resource, &lim); err != nil {
		return -1
	}
	return float64(lim.Cur)
}

// readKernelStartTime returns -1: the start time needs the kern.proc.pid
// sysctl, which the standard library does not wrap, so the package-init
// anchor stands in.
func readKernelStartTime() float64 { return -1 }
