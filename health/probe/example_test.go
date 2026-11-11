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
