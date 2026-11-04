package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
)

// ErrNoCertsInPEM is returned by CATransport when the supplied PEM data
// contains no parseable certificates. A misconfigured or empty CA file
// therefore fails loudly rather than silently producing a transport that
// trusts nothing (which would reject every connection with an opaque error).
var ErrNoCertsInPEM = errors.New("httpclient: no PEM-encoded certificates found")

// caCertPool builds an *x509.CertPool trusting ONLY the CA certificate(s) in
// pem (pinning — the pool holds no other anchors). It returns ErrNoCertsInPEM
// when pem yields no certificates, so an empty or malformed cert file is a
// loud error rather than a silently-empty pool. It is the internal primitive
// CATransport is built on.
func caCertPool(pem []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, ErrNoCertsInPEM
	}
	return pool, nil
}

// CloneDefaultTransport returns a private clone of http.DefaultTransport,
// keeping the standard connection pooling, dial/keepalive timeouts, HTTP/2
// negotiation, and proxy support (ProxyFromEnvironment). The clone is the
// caller's to mutate — set a per-attempt ResponseHeaderTimeout, tune
// MaxIdleConnsPerHost, or pass it as the base RoundTripper of
// NewRetryRoundTripper — without the global footgun of mutating
// http.DefaultTransport itself, which would silently reconfigure every other
// client in the process.
//
// It returns an error when http.DefaultTransport has been replaced by a
// non-*http.Transport RoundTripper (request instrumentation, a test stub): a
// wrapping RoundTripper offers no concrete transport to clone, and failing
// loudly beats silently dropping the wrapper's behavior. It is the building
// block CATransport is assembled on.
func CloneDefaultTransport() (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("httpclient: http.DefaultTransport is not *http.Transport")
	}
	return base.Clone(), nil
}

// CATransport builds an *http.Transport that trusts ONLY the CA certificate(s)
// in pem for TLS verification (pinning). It is cloned from
// http.DefaultTransport (via CloneDefaultTransport), so it keeps the standard
// connection pooling, dial/keepalive timeouts, HTTP/2 negotiation, and proxy
// support (ProxyFromEnvironment). A FRESH TLS config is then installed — RootCAs from
// pem, a TLS 1.2 minimum, and verification always ENABLED (InsecureSkipVerify
// is not set). Any TLS settings already on http.DefaultTransport are
// intentionally NOT carried over, so the returned transport's trust posture
// cannot be weakened by a program that globally mutated the default transport's
// *tls.Config (e.g. set InsecureSkipVerify or an accept-all
// VerifyPeerCertificate hook).
//
// The supplied CA(s) are the SOLE trust anchors: the transport rejects any host
// not chaining to them, including public-CA hosts. This is the right setup for
// a known self-hosted endpoint presenting a private or self-signed certificate
// (a Plex server, an internal API).
//
// The returned *http.Transport is concrete and mutable, so callers may tune
// fields such as MaxIdleConnsPerHost or pass it as the base RoundTripper of
// NewRetryRoundTripper:
//
//	tr, err := httpclient.CATransport(pem)
//	client := httpclient.NewRetryClient(tr, httpclient.DefaultRedirectPolicy, httpclient.TransportConfig{MaxAttempts: 3})
//
// It returns ErrNoCertsInPEM when pem yields no certificates. The caller owns
// reading the PEM bytes (from a file, a secret, an env var), which keeps this
// function I/O-free and lets the caller bound the read as it sees fit.
//
// CATransport requires http.DefaultTransport to be the standard library's
// *http.Transport (the default). If your program has replaced it with a
// wrapping RoundTripper (for example request instrumentation), CATransport
// returns an error.
func CATransport(pem []byte) (*http.Transport, error) {
	pool, err := caCertPool(pem)
	if err != nil {
		return nil, err
	}
	tr, err := CloneDefaultTransport()
	if err != nil {
		return nil, err
	}
	// Install a FRESH TLS config rather than inheriting the cloned base's. This
	// guarantees the documented trust posture — verification on, no inherited
	// InsecureSkipVerify or accept-all VerifyPeerCertificate/VerifyConnection
	// hook — regardless of any global mutation of http.DefaultTransport's TLS
	// config. ForceAttemptHTTP2 (preserved by the clone) keeps HTTP/2 working
	// without needing to carry NextProtos across.
	tr.TLSClientConfig = &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	return tr, nil
}
