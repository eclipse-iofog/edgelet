package hf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
)

func TestParseHubBase(t *testing.T) {
	reg := models.NewRegistryBuilder().SetURL("huggingface.co").SetType(models.RegistryTypeHF).Build()
	base, err := parseHubBase(reg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if base != "https://huggingface.co" {
		t.Fatalf("base: got %q", base)
	}

	httpReg := models.NewRegistryBuilder().
		SetURL("http://hf.internal").
		SetType(models.RegistryTypeHF).
		SetInsecure(true).
		Build()
	base, err = parseHubBase(httpReg)
	if err != nil {
		t.Fatalf("http parse: %v", err)
	}
	if base != "http://hf.internal" {
		t.Fatalf("expected plain http, got %q", base)
	}

	blocked := models.NewRegistryBuilder().SetURL("http://hf.internal").SetType(models.RegistryTypeHF).Build()
	if _, err := parseHubBase(blocked); err == nil {
		t.Fatal("expected http without insecure to fail")
	}
}

func TestTLSConfig_InsecureAndCA(t *testing.T) {
	insecure := models.NewRegistryBuilder().
		SetURL("https://hf.example").
		SetInsecure(true).
		Build()
	cfg, err := registrytls.TLSConfig(insecure)
	if err != nil {
		t.Fatalf("tls insecure: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected skip verify when insecure")
	}

	certPEM := testCACertPEM(t)
	withCA := models.NewRegistryBuilder().
		SetURL("https://hf.example").
		SetCAB64(base64.StdEncoding.EncodeToString(certPEM)).
		Build()
	cfg, err = registrytls.TLSConfig(withCA)
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

func testCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
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
		t.Fatalf("cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
