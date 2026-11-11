package health_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/health/v2/probe"
)

// A service that always listens serves its Status on /healthz, and its health
// subcommand asks that endpoint at the URL LocalURL derives from the listen
// address. The failure line goes to io.Discard because it names the test
// server's random port.
func Example() {
	st := health.NewStatus()
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", health.CheckHandler(
		health.Check{Signal: st, Reason: "the service is starting or shutting down"},
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, _ := probe.LocalURL(srv.Listener.Addr().String(), "/healthz")
	fmt.Println(probe.Check(io.Discard, time.Second, u))
	st.Set(true)
	fmt.Println(probe.Check(io.Discard, time.Second, u))
	// Output:
	// 1
	// 0
}

// A service that opens no port mirrors its verdict to a marker file, which
// its health subcommand reads with RunProbe.
func Example_withMarker() {
	dir, _ := os.MkdirTemp("", "health-example-*")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "myservice.healthy")

	st := health.NewStatus(health.WithMarker(path))
	st.Set(true)
	_, err := os.Stat(path)
	fmt.Println(err == nil)

	st.Set(false)
	_, err = os.Stat(path)
	fmt.Println(err == nil)
	// Output:
	// true
	// false
}

// Work that finishes after shutdown begins cannot report the service healthy.
func ExampleStatus_BeginDrain() {
	st := health.NewStatus()
	st.Set(true)
	st.BeginDrain()
	st.Set(true)
	fmt.Println(st.Healthy())
	// Output: false
}

// Fresh beside the Status names a work loop that stopped refreshing a healthy
// verdict, with its own reason.
func ExampleStatus_Fresh() {
	st := health.NewStatus()
	h := health.CheckHandler(
		health.Check{Signal: st, Reason: "the service is starting or shutting down"},
		health.Check{Signal: st.Fresh(time.Millisecond), Reason: "the work loop has stopped"},
	)
	st.Set(true)
	time.Sleep(2 * time.Millisecond)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output: 503 {"status":"unhealthy","reasons":["the work loop has stopped"]}
}

// A service with several work loops gives each loop its own deadline. One
// owner sets the Status on every tick, healthy only while every loop holds
// its deadline, and /healthz names the loop that stopped.
func Example_severalWorkLoops() {
	const lease = time.Minute
	var syncBeat, importBeat atomic.Int64 // each loop stores the time of its last pass
	syncBeat.Store(time.Now().UnixNano())
	importBeat.Store(time.Now().Add(-time.Hour).UnixNano())
	within := func(beat *atomic.Int64) health.Signal {
		return health.SignalFunc(func() bool { return time.Since(time.Unix(0, beat.Load())) <= lease })
	}
	syncOK, importOK := within(&syncBeat), within(&importBeat)

	st := health.NewStatus()
	h := health.CheckHandler(
		health.Check{Signal: st, Reason: "the service is not running all its work loops"},
		health.Check{Signal: syncOK, Reason: "the sync loop has stopped"},
		health.Check{Signal: importOK, Reason: "the import loop has stopped"},
	)
	st.Set(syncOK.Healthy() && importOK.Healthy()) // the owner's tick

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output: 503 {"status":"unhealthy","reasons":["the service is not running all its work loops","the import loop has stopped"]}
}

// SignalFunc turns any condition the service already tracks into a Check.
func ExampleSignalFunc() {
	queueFull := false
	h := health.CheckHandler(health.Check{
		Signal: health.SignalFunc(func() bool { return !queueFull }),
		Reason: "the job queue is full",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output: 200 {"status":"ok"}
}

func ExampleMarkerPath() {
	fmt.Println(health.MarkerPath("myservice"))
	// Output: /dev/shm/myservice.healthy
}

// ExampleLease_Duration builds the argument for WithMaxAge and Fresh from a
// service's own cadence: tolerate two missed refreshes plus one worst-case run.
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
