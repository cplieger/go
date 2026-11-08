package httpcache_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/cplieger/go/httpcache"
)

func ExampleNewRoundTripper() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	cache := httpcache.NewRoundTripper(nil, httpcache.Config{})
	client := &http.Client{Transport: cache, Timeout: 5 * time.Second}
	for range 2 {
		resp, err := client.Get(srv.URL)
		if err != nil {
			fmt.Println(err)
			return
		}
		_ = resp.Body.Close()
	}
	// The second GET carried the validator, the server answered 304, and the
	// cache served the held copy.
	fmt.Println(cache.State().Hits)
	// Output: 1
}
