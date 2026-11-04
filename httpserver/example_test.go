package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cplieger/go/httpserver"
)

func ExampleValidRequestID() {
	fmt.Println(httpserver.ValidRequestID("abc-123_XYZ"))
	fmt.Println(httpserver.ValidRequestID("bad id"))
	// Output:
	// true
	// false
}

func ExampleOk() {
	rr := httptest.NewRecorder()
	httpserver.Ok(rr)
	fmt.Print(rr.Body.String())
	// Output:
	// {"ok":true}
}

func ExampleWriteError() {
	rr := httptest.NewRecorder()
	// r may be nil; the request_id field is then omitted.
	httpserver.WriteError(rr, nil, http.StatusBadRequest, "bad_request", "invalid payload")
	fmt.Print(rr.Body.String())
	// Output:
	// {"error":"invalid payload","code":"bad_request"}
}

func ExampleMethodNotAllowed() {
	rr := httptest.NewRecorder()
	// A route serving GET and POST refuses everything else, and RFC 9110
	// requires the 405 to advertise BOTH permitted methods, not just one.
	httpserver.MethodNotAllowed(rr, nil, http.MethodGet, http.MethodPost)
	fmt.Println(rr.Code)
	fmt.Println(rr.Header().Get("Allow"))
	fmt.Print(rr.Body.String())
	// Output:
	// 405
	// GET, POST
	// {"error":"method not allowed","code":"method_not_allowed"}
}

func ExampleRouteMetricLabels() {
	// The labels for a request that matched "GET /beat/{id}". A HEAD probe
	// records HEAD, not the pattern's GET, so the metric and the access line
	// agree on the method.
	head := &http.Request{Method: http.MethodHead, Pattern: "GET /beat/{id}"}
	fmt.Println(httpserver.RouteMetricLabels(head))

	// A non-standard method collapses onto one bucket, so a caller cannot mint a
	// series per invented token. The access line still records the token itself.
	probe := &http.Request{Method: "PROPFIND", Pattern: "/"}
	fmt.Println(httpserver.RouteMetricLabels(probe))

	// Nothing matched: every unrouted request shares one path label.
	missed := &http.Request{Method: http.MethodGet}
	fmt.Println(httpserver.RouteMetricLabels(missed))
	// Output:
	// HEAD /beat/{id}
	// other /
	// GET unmatched
}

func ExampleNewStaticTokenVerifier() {
	v := httpserver.NewStaticTokenVerifier("s3cr3t")
	fmt.Println(v.Verify("s3cr3t"))
	fmt.Println(v.Verify("wrong"))
	// An unset secret fails closed: even an empty presented value is rejected.
	unset := httpserver.NewStaticTokenVerifier("")
	fmt.Println(unset.Verify(""))
	// Output:
	// true
	// false
	// false
}

func ExampleGzip() {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` + strings.Repeat(`"item",`, 400) + `"item"]}`))
	})
	// The event stream must reach the client write by write, so it is skipped.
	events := func(r *http.Request) bool { return r.URL.Path == "/events" }
	h := httpserver.Chain(api, httpserver.Recoverer(), httpserver.Gzip(httpserver.WithGzipSkip(events)))

	for _, path := range []string{"/items", "/events"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		fmt.Printf("%s: %q\n", path, rec.Header().Get("Content-Encoding"))
	}
	// Output:
	// /items: "gzip"
	// /events: ""
}
