//go:build !linux && !(darwin && !ios)

package measure

// procExpected is empty: this platform gives no process family beyond the
// start time and the Go ones.
const procExpected = 0

// readPlatformProcess reads nothing; every reading keeps its failure sentinel.
func readPlatformProcess(*processMetricsData) {}

// readKernelStartTime returns -1, so the package-init anchor stands in.
func readKernelStartTime() float64 { return -1 }
