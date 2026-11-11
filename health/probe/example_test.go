package probe_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/cplieger/go/health/v2/probe"
)

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

// A listener on every address is probed on loopback; a missing port is an
// error the caller wraps with the name of its own setting.
func ExampleLocalURL() {
	for _, addr := range []string{":8080", "[::1]:8080", "8080"} {
		u, err := probe.LocalURL(addr, "/healthz")
		if err != nil {
			fmt.Println("error:", err)
			continue
		}
		fmt.Println(u)
	}
	// Output:
	// http://127.0.0.1:8080/healthz
	// http://[::1]:8080/healthz
	// error: address 8080: missing port in address
}
