package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workflow_qoder/internal/config"
)

func TestNewServerTLSConfigWithClientAuth(t *testing.T) {
	ca := newTestCA(t, "ca-1")
	serverCert := newIssuedCert(t, ca, certSpec{
		commonName: "az-nsp",
		dnsNames:   []string{"localhost"},
		ipAddrs:    []net.IP{net.ParseIP("127.0.0.1")},
	})

	dir := t.TempDir()
	cfg := config.TLSConfig{
		Enabled:          true,
		Mode:             "process",
		CACertPath:       writeTLSFile(t, dir, "ca.pem", ca.certPEM),
		CertPath:         writeTLSFile(t, dir, "server.crt", serverCert.certPEM),
		KeyPath:          writeTLSFile(t, dir, "server.key", serverCert.keyPEM),
		ClientAuth:       true,
		CAReloadInterval: time.Hour,
	}

	tlsConfig, err := newServerTLSConfig(cfg)
	if err != nil {
		t.Fatalf("newServerTLSConfig failed: %v", err)
	}
	if tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want %v", tlsConfig.ClientAuth, tls.RequireAndVerifyClientCert)
	}
	if tlsConfig.ClientCAs == nil {
		t.Fatalf("ClientCAs should not be nil")
	}
}

func TestNewServerTLSConfigGetCertificateReloadsLeaf(t *testing.T) {
	ca := newTestCA(t, "ca-1")
	serverCert1 := newIssuedCert(t, ca, certSpec{commonName: "az-1"})
	serverCert2 := newIssuedCert(t, ca, certSpec{commonName: "az-2"})

	dir := t.TempDir()
	caPath := writeTLSFile(t, dir, "ca.pem", ca.certPEM)
	certPath := writeTLSFile(t, dir, "server.crt", serverCert1.certPEM)
	keyPath := writeTLSFile(t, dir, "server.key", serverCert1.keyPEM)

	tlsConfig, err := newServerTLSConfig(config.TLSConfig{
		Enabled:    true,
		Mode:       "process",
		CACertPath: caPath,
		CertPath:   certPath,
		KeyPath:    keyPath,
	})
	if err != nil {
		t.Fatalf("newServerTLSConfig failed: %v", err)
	}

	writeTLSFile(t, dir, "server.crt", serverCert2.certPEM)
	writeTLSFile(t, dir, "server.key", serverCert2.keyPEM)

	cert, err := tlsConfig.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}
	if string(cert.Certificate[0]) != string(serverCert2.certDER) {
		t.Fatalf("GetCertificate did not return updated certificate")
	}
}

func TestNewServerTLSConfigReloadsClientCAs(t *testing.T) {
	ca1 := newTestCA(t, "ca-1")
	ca2 := newTestCA(t, "ca-2")
	serverCert := newIssuedCert(t, ca1, certSpec{commonName: "az"})
	clientCert1 := newIssuedCert(t, ca1, certSpec{commonName: "top-1", isClient: true})
	clientCert2 := newIssuedCert(t, ca2, certSpec{commonName: "top-2", isClient: true})

	dir := t.TempDir()
	caPath := writeTLSFile(t, dir, "ca.pem", ca1.certPEM)
	certPath := writeTLSFile(t, dir, "server.crt", serverCert.certPEM)
	keyPath := writeTLSFile(t, dir, "server.key", serverCert.keyPEM)

	tlsConfig, err := newServerTLSConfig(config.TLSConfig{
		Enabled:          true,
		Mode:             "process",
		CACertPath:       caPath,
		CertPath:         certPath,
		KeyPath:          keyPath,
		ClientAuth:       true,
		CAReloadInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newServerTLSConfig failed: %v", err)
	}

	if err := tlsConfig.VerifyPeerCertificate([][]byte{clientCert1.certDER}, nil); err != nil {
		t.Fatalf("VerifyPeerCertificate with initial CA failed: %v", err)
	}

	time.Sleep(25 * time.Millisecond)
	writeTLSFile(t, dir, "ca.pem", ca2.certPEM)

	waitForTLSCondition(t, time.Second, func() bool {
		return tlsConfig.VerifyPeerCertificate([][]byte{clientCert2.certDER}, nil) == nil
	})
}

func TestAdvertisedNSPAddrUsesTLSSchemeAndExplicitAddr(t *testing.T) {
	t.Run("tls disabled uses http", func(t *testing.T) {
		cfg := &config.NSPConfig{
			ServiceType: "az",
			Region:      "cn-test",
			AZ:          "cn-test-1a",
			Port:        8080,
			TLS: config.TLSConfig{
				Enabled: false,
			},
		}

		server := &Server{cfg: cfg}
		if addr := server.advertisedNSPAddr(); addr != "http://az-nsp-cn-test-1a:8080" {
			t.Fatalf("NSPAddr = %q", addr)
		}
	})

	t.Run("process mode uses https", func(t *testing.T) {
		cfg := &config.NSPConfig{
			ServiceType: "az",
			Region:      "cn-test",
			AZ:          "cn-test-1a",
			Port:        8443,
			TLS: config.TLSConfig{
				Enabled: true,
				Mode:    "process",
			},
		}

		server := &Server{cfg: cfg}
		if addr := server.advertisedNSPAddr(); addr != "https://az-nsp-cn-test-1a:8443" {
			t.Fatalf("NSPAddr = %q", addr)
		}
	})

	t.Run("explicit address wins", func(t *testing.T) {
		t.Setenv("NSP_ADDR", "https://lb-addr:443")
		cfg := &config.NSPConfig{
			ServiceType: "az",
			Region:      "cn-test",
			AZ:          "cn-test-1a",
			Port:        8080,
			TLS: config.TLSConfig{
				Enabled: true,
				Mode:    "lb",
			},
		}

		server := &Server{cfg: cfg}
		if addr := server.advertisedNSPAddr(); addr != "https://lb-addr:443" {
			t.Fatalf("NSPAddr = %q", addr)
		}
	})
}

type testCA struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

type testCert struct {
	certPEM []byte
	keyPEM  []byte
	certDER []byte
}

type certSpec struct {
	commonName string
	dnsNames   []string
	ipAddrs    []net.IP
	isClient   bool
}

func newTestCA(t *testing.T, cn string) testCA {
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
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
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

	return testCA{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(mustECKey(t, key)),
		cert:    cert,
		key:     key,
	}
}

func newIssuedCert(t *testing.T, ca testCA, spec certSpec) testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}

	extUsage := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if spec.isClient {
		extUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: spec.commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  extUsage,
		DNSNames:     spec.dnsNames,
		IPAddresses:  spec.ipAddrs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	return testCert{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(mustECKey(t, key)),
		certDER: der,
	}
}

func mustECKey(t *testing.T, key *ecdsa.PrivateKey) *pem.Block {
	t.Helper()

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	return &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
}

func writeTLSFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return path
}

func waitForTLSCondition(t *testing.T, timeout time.Duration, fn func() bool) {
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
