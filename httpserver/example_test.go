package httpserver_test

import (
	"fmt"
	"log/slog"
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

func ExampleOK() {
	rr := httptest.NewRecorder()
	httpserver.OK(rr)
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

func ExampleWithRecordRouteMetric() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /beat/{id}", func(http.ResponseWriter, *http.Request) {})
	h := httpserver.Logging(
		httpserver.WithLogger(slog.New(slog.DiscardHandler)),
		httpserver.WithRecordRouteMetric(func(m httpserver.RequestMetric) {
			fmt.Println(m.Method, m.Path, m.Status)
		}),
	)(mux)

	// A HEAD probe records HEAD, not the pattern's GET, so the metric and the
	// access line agree on the method; every id records as one series.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodHead, "/beat/7", nil))
	// A non-standard method collapses onto one bucket, so a caller cannot mint
	// a series per invented token. The access line still records the token.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", "/beat/7", nil))
	// Nothing matched: every unrouted request shares one path label.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	// Output:
	// HEAD /beat/{id} 200
	// other unmatched 405
	// GET unmatched 404
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
