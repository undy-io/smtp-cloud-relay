package httpclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildWithTLSCAPEM(t *testing.T) {
	client, err := Build(Config{
		Timeout:             30 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSCAPEM:            validTestCertPEM(t),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("unexpected transport type: %T", client.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("expected RootCAs to be configured")
	}
}

func TestBuildWithTLSCAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte(validTestCertPEM(t)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	client, err := Build(Config{
		Timeout:             30 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSCAFile:           path,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("unexpected transport type: %T", client.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("expected RootCAs to be configured")
	}
}

func TestBuildWithInvalidTLSCAPEM(t *testing.T) {
	_, err := Build(Config{
		Timeout:             30 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSCAPEM:            "not a cert",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tls ca pem does not contain valid certificates") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func validTestCertPEM(t *testing.T) string {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate() error: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if len(pemBytes) == 0 {
		t.Fatal("failed to encode PEM")
	}
	return string(pemBytes)
}
