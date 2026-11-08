// Package certtest provides throwaway self-signed certificate material for
// tests. It is the test-support companion to httpclient's CA-pinning surface
// (CATransport and the caCertPool builder it wraps): a consumer that pins a
// private or self-signed CA needs a valid CA certificate to exercise its
// wiring, and hand-rolling the crypto/x509 dance in every such test is
// error-prone boilerplate.
//
// The generated certificate is a minimal ECDSA CA valid from one hour before
// it is made until one hour after, so clock skew in a test does not reject it.
// It is meant to be parsed and loaded into a pool (or fed to CATransport),
// never to secure a real production connection.
//
// This lives in its own package rather than in httpclient so the
// certificate-generation code is never linked into a consumer's production
// binary: it is reachable only from the _test.go files that import it, the same
// way the standard library ships net/http/httptest alongside net/http.
package certtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SelfSignedCA generates a throwaway self-signed CA certificate and returns it
// PEM-encoded. Use it to build an x509.CertPool or an httpclient.CATransport in
// tests without touching disk or the system trust store. A fresh key is
// generated on every call, so certificates from separate calls are distinct and
// mutually untrusted (handy for asserting that a pin is actually enforced). It
// fails the test via tb.Fatalf on any crypto error.
func SelfSignedCA(tb testing.TB) []byte {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("certtest: generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "httpclient-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatalf("certtest: create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// WriteSelfSignedCA generates a throwaway self-signed CA certificate (see
// SelfSignedCA), writes it PEM-encoded to a "ca.pem" file under tb.TempDir(),
// and returns the path. Use it for code under test that reads a CA from a file
// path (for example a PLEX_CA_CERT_PATH-style setting) rather than from bytes.
// The file is created mode 0o600 and is removed with the test's temp dir.
func WriteSelfSignedCA(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "ca.pem")
	if err := os.WriteFile(path, SelfSignedCA(tb), 0o600); err != nil {
		tb.Fatalf("certtest: write CA file: %v", err)
	}
	return path
}
