package probe_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/cplieger/go/health/probe"
)

// ExampleURL shows the HTTP liveness probe for containers that wrap a
// third-party server exposing an HTTP endpoint.
func ExampleURL() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := probe.URL(context.Background(), srv.URL)
	fmt.Println("healthy:", err == nil)
	// Output:
	// healthy: true
}

// ExampleCheck shows the multi-URL probe behind cmd/probe: all URLs
// must answer 2xx within one shared timeout.
func ExampleCheck() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	code := probe.Check(os.Stderr, probe.DefaultTimeout, srv.URL)
	fmt.Println("exit code:", code)
	// Output:
	// exit code: 0
}
