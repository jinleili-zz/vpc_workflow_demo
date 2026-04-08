package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewReloadableMTLSHTTPClient(t *testing.T) {
	ca1 := newCertificateAuthority(t, "ca-1")
	clientCert := issueCertificate(t, ca1, certificateRequest{
		commonName: "top-nsp",
		isClient:   true,
	})

	dir := t.TempDir()
	caPath := writeBytes(t, dir, "ca.pem", ca1.certPEM)
	certPath := writeBytes(t, dir, "client.crt", clientCert.certPEM)
	keyPath := writeBytes(t, dir, "client.key", clientCert.keyPEM)

	client, err := newReloadableMTLSHTTPClient(context.Background(), TLSClientConfig{
		Enabled:          true,
		CACertPath:       caPath,
		CertPath:         certPath,
		KeyPath:          keyPath,
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newReloadableMTLSHTTPClient failed: %v", err)
	}

	rt := client.Transport.(*reloadableTransport)
	transport := rt.current.Load().(*http.Transport)
	if transport.TLSClientConfig == nil {
		t.Fatalf("TLSClientConfig should not be nil")
	}
	if len(transport.TLSClientConfig.RootCAs.Subjects()) != 1 {
		t.Fatalf("RootCAs subjects = %d, want 1", len(transport.TLSClientConfig.RootCAs.Subjects()))
	}
	if len(transport.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("Certificates len = %d, want 1", len(transport.TLSClientConfig.Certificates))
	}
	if string(transport.TLSClientConfig.Certificates[0].Certificate[0]) != string(clientCert.cert.Raw) {
		t.Fatalf("client certificate was not loaded")
	}
}

func TestNewReloadableMTLSHTTPClientFailsWhenCAUnreadable(t *testing.T) {
	dir := t.TempDir()
	_, err := newReloadableMTLSHTTPClient(context.Background(), TLSClientConfig{
		Enabled:          true,
		CACertPath:       filepath.Join(dir, "missing-ca.pem"),
		CertPath:         filepath.Join(dir, "client.crt"),
		KeyPath:          filepath.Join(dir, "client.key"),
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected error for unreadable CA file")
	}
}

func TestNewReloadableMTLSHTTPClientFailsWhenClientCertMissing(t *testing.T) {
	ca := newCertificateAuthority(t, "ca")
	dir := t.TempDir()
	caPath := writeBytes(t, dir, "ca.pem", ca.certPEM)

	_, err := newReloadableMTLSHTTPClient(context.Background(), TLSClientConfig{
		Enabled:          true,
		CACertPath:       caPath,
		CertPath:         filepath.Join(dir, "missing.crt"),
		KeyPath:          filepath.Join(dir, "missing.key"),
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected error for missing client cert")
	}
}

func TestReloadableMTLSHTTPClientReloadsWhenCAChanges(t *testing.T) {
	ca1 := newCertificateAuthority(t, "ca-1")
	ca2 := newCertificateAuthority(t, "ca-2")
	clientCert := issueCertificate(t, ca1, certificateRequest{
		commonName: "top-nsp",
		isClient:   true,
	})

	dir := t.TempDir()
	caPath := writeBytes(t, dir, "ca.pem", ca1.certPEM)
	certPath := writeBytes(t, dir, "client.crt", clientCert.certPEM)
	keyPath := writeBytes(t, dir, "client.key", clientCert.keyPEM)

	client, err := newReloadableMTLSHTTPClient(context.Background(), TLSClientConfig{
		Enabled:          true,
		CACertPath:       caPath,
		CertPath:         certPath,
		KeyPath:          keyPath,
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newReloadableMTLSHTTPClient failed: %v", err)
	}

	rt := client.Transport.(*reloadableTransport)
	original := rt.current.Load().(*http.Transport)

	time.Sleep(25 * time.Millisecond)
	writeBytes(t, dir, "ca.pem", append(ca1.certPEM, ca2.certPEM...))

	waitForCondition(t, time.Second, func() bool {
		current := rt.current.Load().(*http.Transport)
		return current != original && len(current.TLSClientConfig.RootCAs.Subjects()) == 2
	})
}

func TestReloadableMTLSHTTPClientReloadsWhenClientCertChanges(t *testing.T) {
	ca := newCertificateAuthority(t, "ca")
	clientCert1 := issueCertificate(t, ca, certificateRequest{
		commonName: "top-nsp-1",
		isClient:   true,
	})
	clientCert2 := issueCertificate(t, ca, certificateRequest{
		commonName: "top-nsp-2",
		isClient:   true,
	})

	dir := t.TempDir()
	caPath := writeBytes(t, dir, "ca.pem", ca.certPEM)
	certPath := writeBytes(t, dir, "client.crt", clientCert1.certPEM)
	keyPath := writeBytes(t, dir, "client.key", clientCert1.keyPEM)

	client, err := newReloadableMTLSHTTPClient(context.Background(), TLSClientConfig{
		Enabled:          true,
		CACertPath:       caPath,
		CertPath:         certPath,
		KeyPath:          keyPath,
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newReloadableMTLSHTTPClient failed: %v", err)
	}

	rt := client.Transport.(*reloadableTransport)
	original := rt.current.Load().(*http.Transport)
	originalRaw := append([]byte(nil), original.TLSClientConfig.Certificates[0].Certificate[0]...)

	time.Sleep(25 * time.Millisecond)
	writeBytes(t, dir, "client.crt", clientCert2.certPEM)
	writeBytes(t, dir, "client.key", clientCert2.keyPEM)

	waitForCondition(t, time.Second, func() bool {
		current := rt.current.Load().(*http.Transport)
		if current == original {
			return false
		}
		return string(current.TLSClientConfig.Certificates[0].Certificate[0]) != string(originalRaw)
	})
}

type certificateAuthority struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

type issuedCertificate struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
}

type certificateRequest struct {
	commonName string
	dnsNames   []string
	ipAddrs    []net.IP
	isClient   bool
}

func newCertificateAuthority(t *testing.T, commonName string) certificateAuthority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	return certificateAuthority{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(mustMarshalECPrivateKey(t, key)),
		cert:    cert,
		key:     key,
	}
}

func issueCertificate(t *testing.T, ca certificateAuthority, req certificateRequest) issuedCertificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}

	keyUsage := x509.KeyUsageDigitalSignature
	extUsage := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if req.isClient {
		extUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: req.commonName,
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    keyUsage,
		ExtKeyUsage: extUsage,
		DNSNames:    req.dnsNames,
		IPAddresses: req.ipAddrs,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	return issuedCertificate{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(mustMarshalECPrivateKey(t, key)),
		cert:    cert,
	}
}

func mustMarshalECPrivateKey(t *testing.T, key *ecdsa.PrivateKey) *pem.Block {
	t.Helper()

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
}

func mustLoadKeyPair(t *testing.T, certPEM, keyPEM []byte) tls.Certificate {
	t.Helper()

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert
}

func mustCertPool(t *testing.T, certPEM []byte) *x509.CertPool {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("AppendCertsFromPEM failed")
	}
	return pool
}

func writeBytes(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return path
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %v", timeout)
}
