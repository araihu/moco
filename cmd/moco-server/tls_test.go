package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSConfiguration(t *testing.T) {
	t.Parallel()
	if config, err := loadTLSConfiguration("", ""); config != nil || err != nil {
		t.Fatalf("HTTP configuration: %v, %v", config, err)
	}
	for _, files := range [][2]string{{"cert", ""}, {"", "key"}, {"missing-cert", "missing-key"}} {
		if _, err := loadTLSConfiguration(files[0], files[1]); err == nil {
			t.Fatal("invalid TLS configuration accepted")
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadTLSConfiguration(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS minimum version must be 1.2")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to load test trust root")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent || response.TLS == nil {
		t.Fatalf("unexpected HTTPS response: %d", response.StatusCode)
	}
	legacy := &http.Transport{TLSClientConfig: &tls.Config{ //nolint:gosec // Verify that obsolete protocols are rejected.
		RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11,
	}}
	defer legacy.CloseIdleConnections()
	legacyClient := &http.Client{Transport: legacy, Timeout: 5 * time.Second}
	if response, err := legacyClient.Do(request.Clone(t.Context())); err == nil {
		_ = response.Body.Close()
		t.Fatal("server accepted obsolete TLS")
	}
	for _, notAfter := range []time.Time{time.Now().Add(-time.Hour), time.Now().Add(2 * time.Hour)} {
		template.NotBefore, template.NotAfter = notAfter.Add(-30*time.Minute), notAfter
		invalidDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: invalidDER}), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTLSConfiguration(certPath, keyPath); err == nil {
			t.Fatal("certificate outside its validity interval accepted")
		}
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("invalid PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTLSConfiguration(certPath, keyPath); err == nil {
		t.Fatal("invalid private key accepted")
	}
}
