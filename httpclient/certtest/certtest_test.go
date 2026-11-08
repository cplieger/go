package certtest_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/go/httpclient/certtest"
)

func TestSelfSignedCA(t *testing.T) {
	t.Parallel()
	pemBytes := certtest.SelfSignedCA(t)

	block, rest := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("SelfSignedCA returned no decodable PEM block")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("PEM block type = %q, want CERTIFICATE", block.Type)
	}
	if len(rest) != 0 {
		t.Errorf("unexpected trailing data after PEM block: %d bytes", len(rest))
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("certificate is not marked as a CA")
	}
	if !cert.BasicConstraintsValid {
		t.Error("BasicConstraintsValid is false; a real CA chain would reject it")
	}

	// The validity window is backdated so the certificate is usable the
	// instant it is generated: a NotBefore in the future makes every x509
	// verifier reject the chain as not yet valid, and a NotAfter in the past
	// as expired.
	now := time.Now()
	if !cert.NotBefore.Before(now) {
		t.Errorf("NotBefore = %v, want a time before now (%v)", cert.NotBefore, now)
	}
	if !cert.NotAfter.After(now) {
		t.Errorf("NotAfter = %v, want a time after now (%v)", cert.NotAfter, now)
	}

	// The core use case: the PEM must load into a pool (what caCertPool /
	// CATransport do under the hood).
	if pool := x509.NewCertPool(); !pool.AppendCertsFromPEM(pemBytes) {
		t.Error("AppendCertsFromPEM rejected the generated CA")
	}
}

func TestSelfSignedCA_freshPerCall(t *testing.T) {
	t.Parallel()
	// A fresh key per call keeps separate certs mutually untrusted, which is
	// what lets a test assert that pinning CA A rejects a server using CA B.
	first := string(certtest.SelfSignedCA(t))
	second := string(certtest.SelfSignedCA(t))
	if first == second {
		t.Error("SelfSignedCA returned identical PEM on two calls; want a fresh certificate each time")
	}
}

func TestWriteSelfSignedCA(t *testing.T) {
	t.Parallel()
	path := certtest.WriteSelfSignedCA(t)

	if got := filepath.Base(path); got != "ca.pem" {
		t.Errorf("file name = %q, want ca.pem", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if pool := x509.NewCertPool(); !pool.AppendCertsFromPEM(data) {
		t.Error("file contents did not load as a CA certificate")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %#o, want 0o600", perm)
	}
}
