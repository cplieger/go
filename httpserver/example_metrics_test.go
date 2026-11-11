package httpserver_test

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/cplieger/go/httpserver"
)

// An app with no port of its own serves an opt-in metrics and health listener.
// ListenMetrics binds first, so a port in use fails before any work starts.
func ExampleListenMetrics() {
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "app_up 1\n")
	})
	healthz := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ms, err := httpserver.ListenMetrics(ctx, "127.0.0.1:0", metrics, healthz)
	if err != nil {
		fmt.Println("refusing to start:", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- ms.Serve(ctx) }()

	for _, path := range []string{"/metrics", "/healthz", "/api"} {
		resp, err := http.Get("http://" + ms.Addr().String() + path)
		if err != nil {
			fmt.Println(err)
			continue
		}
		_ = resp.Body.Close()
		fmt.Println(path, resp.StatusCode)
	}
	cancel()
	fmt.Println("stopped:", <-done)
	// Output:
	// /metrics 200
	// /healthz 200
	// /api 404
	// stopped: <nil>
}
