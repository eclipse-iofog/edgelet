package registrytls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestParse_HTTPRequiresInsecure(t *testing.T) {
	blocked := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		Build()
	if _, err := Parse(blocked); !errors.Is(err, ErrHTTPRequiresInsecure) {
		t.Fatalf("expected http without insecure to fail, got: %v", err)
	}

	allowed := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		SetInsecure(true).
		Build()
	ep, err := Parse(allowed)
	if err != nil {
		t.Fatalf("http insecure parse: %v", err)
	}
	if !ep.PlainHTTP || ep.Scheme != "http" || ep.Host != "registry.internal:5000" {
		t.Fatalf("expected plain http, got %+v", ep)
	}
}

func TestTLSConfig_InsecureAndCA(t *testing.T) {
	insecure := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetInsecure(true).
		Build()
	cfg, err := TLSConfig(insecure)
	if err != nil {
		t.Fatalf("tls insecure: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected skip verify when insecure")
	}

	certPEM := testCACertPEM(t)
	withCA := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetCAB64(base64.StdEncoding.EncodeToString(certPEM)).
		Build()
	cfg, err = TLSConfig(withCA)
	if err != nil {
		t.Fatalf("tls ca: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected custom CA to augment the trust store")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("did not expect skip verify when only ca is set")
	}
}

func TestHTTPClient_ExtraCAAndInsecure(t *testing.T) {
	caPEM, caKey := testCA(t)
	serverPEM, serverKey := testServerCert(t, caPEM, caKey)

	cert, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	plain := models.NewRegistryBuilder().SetURL("https://registry.example").Build()
	plainClient, err := HTTPClient(plain)
	if err != nil {
		t.Fatalf("plain client: %v", err)
	}
	if _, err := plainClient.Get(srv.URL); err == nil {
		t.Fatal("expected private CA handshake to fail without extra ca")
	}

	withCA := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetCAB64(base64.StdEncoding.EncodeToString(caPEM)).
		Build()
	caClient, err := HTTPClient(withCA)
	if err != nil {
		t.Fatalf("ca client: %v", err)
	}
	resp, err := caClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected extra ca to trust private CA: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	insecure := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetInsecure(true).
		Build()
	insecureClient, err := HTTPClient(insecure)
	if err != nil {
		t.Fatalf("insecure client: %v", err)
	}
	resp, err = insecureClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected insecure to skip verify: %v", err)
	}
	_ = resp.Body.Close()
}

func testCACertPEM(t *testing.T) []byte {
	t.Helper()
	pemBytes, _ := testCA(t)
	return pemBytes
}

func testCA(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "edgelet-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

func testServerCert(t *testing.T, caPEM []byte, caKey *ecdsa.PrivateKey) ([]byte, []byte) {
	t.Helper()
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("decode ca pem")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "registry.example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "registry.example"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
