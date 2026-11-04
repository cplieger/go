package health_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/health"
)

// Example demonstrates the two-process healthcheck pattern.
// The long-running process creates a marker; the probe process stats it.
func Example() {
	path := filepath.Join(os.TempDir(), ".healthy-example")
	m := health.NewMarker(path)
	defer m.Cleanup()

	m.Set(true)
	fmt.Println("healthy:", m.CheckHealthy())

	m.Set(false)
	fmt.Println("healthy:", m.CheckHealthy())
	// Output:
	// healthy: true
	// healthy: false
}

// ExampleProbeCheck shows how to use ProbeCheck for a testable probe
// that does not call os.Exit.
func ExampleProbeCheck() {
	dir, _ := os.MkdirTemp("", "health-example-*")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, ".healthy")

	// No marker yet — writable dir means unhealthy.
	fmt.Println("code:", health.ProbeCheck(path))

	// Create marker — healthy.
	os.WriteFile(path, nil, 0o600)
	fmt.Println("code:", health.ProbeCheck(path))
	// Output:
	// code: 1
	// code: 0
}

// ExampleLease_Duration builds the argument for WithMaxAge from an app's own
// cadence: tolerate two missed refreshes plus one worst-case run.
func ExampleLease_Duration() {
	lease := health.Lease{
		Interval: 6 * time.Hour, Cycles: 2,
		Timeout: time.Hour, Attempts: 1,
	}
	fmt.Println(lease.Duration())

	// An operator cadence large enough to overflow the same arithmetic
	// written inline saturates instead of wrapping negative.
	absurd := health.Lease{Interval: 900000 * time.Hour, Cycles: 3}
	fmt.Println(absurd.Duration() > 0, 3*absurd.Interval > 0)

	// A non-positive Interval is the documented disable, as is the zero value.
	fmt.Println(health.Lease{Cycles: 3, Floor: time.Hour}.Duration())
	// Output:
	// 13h0m0s
	// true false
	// 0s
}
