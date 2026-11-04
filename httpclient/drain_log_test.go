package httpclient_test

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/go/httpclient"
)

// webhookCredential is the path segment that IS the credential for a
// webhook-shaped URL (a Discord webhook token is the canonical case).
const webhookCredential = "verysecrettrailertoken"

// malformedTrailerServer serves one chunked response whose trailer section is
// missing its colon, so net/textproto rejects it while the BODY read is in
// flight and renders the offending bytes verbatim:
//
//	malformed MIME header: missing colon: "<remote bytes>"
//
// The trailer echoes the request URI, which is what an edge fronting a webhook
// does; for a webhook the path is the credential. This is the real net/http
// error path, not a synthetic error value: the point of the test is that the
// error text is authored by the far end and reaches a log line by itself.
func malformedTrailerServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n"+
					"Content-Type: text/plain\r\n"+
					"Trailer: X-Echo\r\n"+
					"Transfer-Encoding: chunked\r\n"+
					"\r\n"+
					"5\r\nhello\r\n"+
					"0\r\n"+
					"this-is-not-a-header"+req.URL.RequestURI()+"\r\n"+
					"\r\n")
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

// TestDrain_does_not_log_remote_authored_body_error swaps slog.Default; not parallel.
//
// Drain logs on the PACKAGE-level slog.Default(), which no httpclient option can
// reach: it takes no logger, and the client's WithLogger does not route this
// site. So a consumer whose request URL carries its credential in the path
// cannot close this by any caller-side move (knell could not, and hand-rolled
// a local drain instead). The body-read error's text is remote-authored, so
// the only fix is for the site to stop putting it in a record.
func TestDrain_does_not_log_remote_authored_body_error(t *testing.T) {
	srv := malformedTrailerServer(t)

	var buf bytes.Buffer
	swapDefaultLogger(t, bufLogger(&buf))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv+"/api/webhooks/1234567890/"+webhookCredential, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // DrainClose below closes it
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	httpclient.DrainClose(resp.Body)

	logged := buf.String()
	// The drain must be seen to fail, or the test proves nothing about the
	// path that logs.
	if !strings.Contains(logged, "failed to drain response body") {
		t.Fatalf("malformed trailer did not reach Drain's failure path; log was:\n%s", logged)
	}
	if strings.Contains(logged, webhookCredential) {
		t.Errorf("drain log leaked the credential from the request path:\n%s", logged)
	}
	// Nothing the far end authored may reach the record, credential-shaped or not.
	if strings.Contains(logged, "malformed MIME header") {
		t.Errorf("drain log carried remote-authored error text:\n%s", logged)
	}
}
