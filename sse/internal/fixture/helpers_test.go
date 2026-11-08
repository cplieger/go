package fixture

import (
	"net/http"
	"testing"
)

// testClient has no timeout because it reads streams; every request carries
// the test's context.
var testClient = &http.Client{}

// get opens a stream with the given headers. The caller closes the body.
func get(t *testing.T, url string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
