package oci

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

func TestParseRegistryEndpoint(t *testing.T) {
	reg := models.NewRegistryBuilder().SetURL("quay.io").SetType(models.RegistryTypeOCI).Build()
	ep, err := parseRegistryEndpoint(reg, "ai/gemma3")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ep.Host != "quay.io" || ep.Scheme != "https" || ep.PlainHTTP || ep.RepoRef != "quay.io/ai/gemma3" {
		t.Fatalf("unexpected endpoint: %+v", ep)
	}

	httpReg := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		SetInsecure(true).
		Build()
	ep, err = parseRegistryEndpoint(httpReg, "ai/gemma3")
	if err != nil {
		t.Fatalf("http parse: %v", err)
	}
	if !ep.PlainHTTP || ep.Scheme != "http" || ep.Host != "registry.internal:5000" {
		t.Fatalf("expected plain http, got %+v", ep)
	}

	blocked := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		Build()
	if _, err := parseRegistryEndpoint(blocked, "ai/gemma3"); err == nil {
		t.Fatal("expected http without insecure to fail")
	}
}

func TestTLSConfig_InsecureAndCA(t *testing.T) {
	insecure := models.NewRegistryBuilder().
		SetURL("https://registry.example").
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
		SetURL("https://registry.example").
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
