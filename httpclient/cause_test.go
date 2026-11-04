package httpclient_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient"
)

// get issues one GET through client and returns its transport error.
func get(t *testing.T, client *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("GET %s succeeded, want a transport error", url)
	}
	return err
}

// closedPort returns an address on loopback nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// hangUpServer accepts each connection and, once the request has arrived,
// closes it without answering: with an RST when reset is set, else with a FIN.
// accepts counts the connections it took.
func hangUpServer(t *testing.T, reset bool) (addr string, accepts *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepts = new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_, _ = conn.Read(make([]byte, 1024))
			if reset {
				_ = conn.(*net.TCPConn).SetLinger(0)
			}
			conn.Close()
		}
	}()
	return ln.Addr().String(), accepts
}

// hangUpAddr is hangUpServer's address alone.
func hangUpAddr(t *testing.T, reset bool) string {
	t.Helper()
	addr, _ := hangUpServer(t, reset)
	return addr
}

func TestCauseOf(t *testing.T) {
	t.Parallel()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tlsServer.Close)
	mtlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mtlsServer.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	mtlsServer.StartTLS()
	t.Cleanup(mtlsServer.Close)
	plainServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(plainServer.Close)
	stallServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(stallServer.Close)

	tests := []struct {
		name string
		err  func(t *testing.T) error
		want httpclient.Cause
	}{
		{"nil", func(*testing.T) error { return nil }, httpclient.CauseUnknown},
		{"plain", func(*testing.T) error { return errors.New("decode failed") }, httpclient.CauseUnknown},
		{"status_answer", func(*testing.T) error { return &httpclient.StatusError{Code: 503} }, httpclient.CauseUnknown},
		{"caller_canceled", func(*testing.T) error { return fmt.Errorf("get: %w", context.Canceled) }, httpclient.CauseUnknown},
		{"bare_eof", func(*testing.T) error { return io.EOF }, httpclient.CauseUnknown},
		{"dns_not_found_wrapped_twice", func(*testing.T) error {
			return fmt.Errorf("feed: %w", fmt.Errorf("lookup: %w", &net.DNSError{Err: "no such host", Name: "feed.example", IsNotFound: true}))
		}, httpclient.CauseDNS},
		{"dns_timeout", func(*testing.T) error {
			return &net.DNSError{Err: "i/o timeout", Name: "feed.example", IsTimeout: true}
		}, httpclient.CauseDNS},
		{"refused", func(t *testing.T) error { return get(t, &http.Client{}, "http://"+closedPort(t)+"/") }, httpclient.CauseRefused},
		{"reset", func(t *testing.T) error { return get(t, &http.Client{}, "http://"+hangUpAddr(t, true)+"/") }, httpclient.CauseReset},
		{"server_closed", func(t *testing.T) error { return get(t, &http.Client{}, "http://"+hangUpAddr(t, false)+"/") }, httpclient.CauseReset},
		{"server_closed_reduced", func(t *testing.T) error {
			return httpclient.LogSafeError(get(t, &http.Client{}, "http://"+hangUpAddr(t, false)+"/"))
		}, httpclient.CauseReset},
		{"broken_pipe", func(*testing.T) error {
			return &net.OpError{Op: "write", Net: "tcp", Err: &net.OpError{Op: "write", Err: syscall.EPIPE}}
		}, httpclient.CauseReset},
		{"unexpected_eof", func(*testing.T) error { return fmt.Errorf("body: %w", io.ErrUnexpectedEOF) }, httpclient.CauseReset},
		{"x509_unknown_authority", func(t *testing.T) error { return get(t, &http.Client{}, tlsServer.URL) }, httpclient.CauseTLS},
		{"x509_unknown_authority_bare", func(*testing.T) error { return x509.UnknownAuthorityError{} }, httpclient.CauseTLS},
		{"verification_other", func(*testing.T) error {
			return &tls.CertificateVerificationError{Err: x509.SystemRootsError{}}
		}, httpclient.CauseTLS},
		{"x509_hostname", func(*testing.T) error {
			return x509.HostnameError{Certificate: &x509.Certificate{}, Host: "other.example"}
		}, httpclient.CauseTLS},
		{"x509_invalid", func(*testing.T) error { return x509.CertificateInvalidError{Reason: x509.Expired} }, httpclient.CauseTLS},
		{"remote_alert", func(t *testing.T) error { return get(t, mtlsServer.Client(), mtlsServer.URL) }, httpclient.CauseTLS},
		{"plain_http_answer", func(t *testing.T) error {
			return get(t, &http.Client{}, "https"+plainServer.URL[len("http"):])
		}, httpclient.CauseTLS},
		{"record_header", func(*testing.T) error {
			return tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}
		}, httpclient.CauseTLS},
		{"attempt_timeout", func(*testing.T) error { return httpclient.AttemptTimeout(context.DeadlineExceeded) }, httpclient.CauseTimeout},
		{"caller_deadline", func(*testing.T) error { return fmt.Errorf("get: %w", context.DeadlineExceeded) }, httpclient.CauseTimeout},
		{"client_timeout", func(t *testing.T) error {
			return get(t, &http.Client{Timeout: 20 * time.Millisecond}, stallServer.URL)
		}, httpclient.CauseTimeout},
		{"net_timeout", func(*testing.T) error {
			return &net.OpError{Op: "read", Net: "tcp", Err: errTimeout{}}
		}, httpclient.CauseTimeout},
		{"too_large", func(*testing.T) error {
			return fmt.Errorf("feed: %w", &httpclient.ResponseTooLargeError{Limit: 1 << 20})
		}, httpclient.CauseTooLarge},
		{"max_bytes", func(*testing.T) error { return &http.MaxBytesError{Limit: 1 << 20} }, httpclient.CauseTooLarge},
		{"through_permanent", func(*testing.T) error {
			return httpclient.Permanent(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		}, httpclient.CauseRefused},
		{"through_mark", func(*testing.T) error { return httpclient.MarkTransient(&net.DNSError{IsNotFound: true}) }, httpclient.CauseDNS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.err(t)
			if got := httpclient.CauseOf(err); got != tc.want {
				t.Errorf("CauseOf(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
}

// errTimeout is a net.Error reporting a timeout that carries no context
// sentinel.
type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

func TestCauseString(t *testing.T) {
	t.Parallel()
	want := map[httpclient.Cause]string{
		httpclient.CauseUnknown:  "unknown",
		httpclient.CauseDNS:      "dns",
		httpclient.CauseRefused:  "refused",
		httpclient.CauseReset:    "reset",
		httpclient.CauseTimeout:  "timeout",
		httpclient.CauseTLS:      "tls",
		httpclient.CauseTooLarge: "too_large",
		httpclient.Cause(200):    "Cause(200)",
	}
	for c, name := range want {
		if got := c.String(); got != name {
			t.Errorf("Cause(%d).String() = %q, want %q", uint8(c), got, name)
		}
	}
}

// TestServerClosedKeepsEOF pins the hang-up's reduced form: it names the
// hang-up, renders no URL and still matches io.EOF.
func TestServerClosedKeepsEOF(t *testing.T) {
	t.Parallel()
	addr := hangUpAddr(t, false)
	err := httpclient.LogSafeError(get(t, &http.Client{}, "http://user:pw@"+addr+"/?token=secret"))
	if !errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(%v, io.EOF) = false, want true", err)
	}
	if msg := err.Error(); msg != "server closed the connection before responding: EOF" {
		t.Errorf("LogSafeError(hang-up) = %q, want the named hang-up and no URL", msg)
	}
}

func TestCauseText(t *testing.T) {
	t.Parallel()
	names := []string{"unknown", "dns", "refused", "reset", "timeout", "tls", "too_large"}
	for i, name := range names {
		c := httpclient.Cause(i)
		text, err := c.MarshalText()
		if err != nil || string(text) != name {
			t.Errorf("Cause(%d).MarshalText() = %q, %v, want %q", i, text, err, name)
		}
		var back httpclient.Cause
		if err := back.UnmarshalText([]byte(name)); err != nil || back != c {
			t.Errorf("UnmarshalText(%q) = %d, %v, want %d", name, uint8(back), err, i)
		}
	}
	if text, err := httpclient.Cause(len(names)).MarshalText(); err == nil {
		t.Errorf("Cause(%d).MarshalText() = %q, nil, want an error", len(names), text)
	}
	for _, bad := range []string{"", "Reset", "Cause(3)", "3"} {
		back := httpclient.CauseTLS
		if err := back.UnmarshalText([]byte(bad)); err == nil || back != httpclient.CauseTLS {
			t.Errorf("UnmarshalText(%q) = %d, %v, want an error and the value untouched", bad, uint8(back), err)
		}
	}
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("probe", "cause", httpclient.CauseRefused)
	if !strings.Contains(buf.String(), `"cause":"refused"`) {
		t.Errorf("JSON log line %s, want \"cause\":\"refused\"", buf.String())
	}
}

// TestIsTransientFollowsCause pins IsTransient to CauseOf for an error no mark
// or Transient implementation decides: DNS, refused, reset and timeout causes
// are retried, the rest are not.
func TestIsTransientFollowsCause(t *testing.T) {
	t.Parallel()
	errs := []error{
		&net.DNSError{IsNotFound: true},
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		&net.OpError{Op: "read", Err: syscall.ECONNRESET},
		io.ErrUnexpectedEOF,
		&net.OpError{Op: "read", Err: errTimeout{}},
		x509.UnknownAuthorityError{},
		&httpclient.ResponseTooLargeError{Limit: 1},
		&http.MaxBytesError{Limit: 1},
		errors.New("plain"),
	}
	for _, err := range errs {
		c := httpclient.CauseOf(err)
		want := c == httpclient.CauseDNS || c == httpclient.CauseRefused || c == httpclient.CauseReset || c == httpclient.CauseTimeout
		if got := httpclient.IsTransient(err); got != want {
			t.Errorf("IsTransient(%v) = %v with cause %v, want %v", err, got, c, want)
		}
	}
}
