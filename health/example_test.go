package health_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/health/v2"
)

// Example demonstrates the two-process healthcheck pattern.
// The long-running process creates a marker; the probe process stats it.
func Example() {
	path := filepath.Join(os.TempDir(), ".healthy-example")
	m := health.NewMarker(path)
	defer m.Cleanup()

	m.Set(true)
	fmt.Println("healthy:", m.Healthy())

	m.Set(false)
	fmt.Println("healthy:", m.Healthy())
	// Output:
	// healthy: true
	// healthy: false
}

// ExampleInspect shows the probe decision without os.Exit: Inspect reads the
// marker and Healthy is the verdict RunProbe turns into its exit code.
func ExampleInspect() {
	dir, _ := os.MkdirTemp("", "health-example-*")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, ".healthy")

	// No marker yet in a writable dir: unhealthy.
	f := health.Inspect(path)
	fmt.Println(f.Healthy(), f.Reason())

	// Create the marker: healthy.
	os.WriteFile(path, nil, 0o600)
	fmt.Println(health.Inspect(path).Healthy())
	// Output:
	// false unhealthy: marker absent
	// true
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
