//go:build linux

package edgelet

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

	dockerresolver "github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
)

func TestImagePullResolverOptions_HTTPRequiresInsecure(t *testing.T) {
	blocked := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		Build()
	_, _, err := imagePullResolverOptions(blocked)
	if !errors.Is(err, registrytls.ErrHTTPRequiresInsecure) {
		t.Fatalf("expected http without insecure to fail, got: %v", err)
	}
}

func TestImagePullResolverOptions_InsecureAndCA(t *testing.T) {
	httpInsecure := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		SetIsPublic(true).
		SetInsecure(true).
		Build()
	opts, ok, err := imagePullResolverOptions(httpInsecure)
	if err != nil {
		t.Fatalf("http insecure: %v", err)
	}
	if !ok {
		t.Fatal("expected resolver for insecure http registry")
	}
	matched := registryHost(t, opts, "registry.internal:5000")
	if matched.Scheme != "http" || matched.Client == nil {
		t.Fatalf("expected plain HTTP for the registry host, got scheme=%q", matched.Scheme)
	}
	other := registryHost(t, opts, "other.example")
	if other.Scheme != "https" {
		t.Fatalf("expected https for a different host, got %q", other.Scheme)
	}

	httpsInsecure := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetIsPublic(true).
		SetInsecure(true).
		Build()
	opts, ok, err = imagePullResolverOptions(httpsInsecure)
	if err != nil {
		t.Fatalf("https insecure: %v", err)
	}
	if !ok {
		t.Fatal("expected resolver for https insecure")
	}
	httpsHost := registryHost(t, opts, "registry.example")
	if httpsHost.Scheme != "https" {
		t.Fatalf("did not expect plain HTTP for https insecure, got %q", httpsHost.Scheme)
	}
	if !transportTLS(t, httpsHost.Client).InsecureSkipVerify {
		t.Fatal("expected skip verify when insecure")
	}

	caPEM := testImagePullCAPEM(t)
	withCA := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetIsPublic(true).
		SetCAB64(base64.StdEncoding.EncodeToString(caPEM)).
		Build()
	opts, ok, err = imagePullResolverOptions(withCA)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	if !ok {
		t.Fatal("expected resolver for extra ca")
	}
	cfg := transportTLS(t, registryHost(t, opts, "registry.example").Client)
	if cfg.RootCAs == nil {
		t.Fatal("expected extra ca to augment the trust store")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("did not expect skip verify when only ca is set")
	}
}

func TestImagePullResolverOptions_PrivateCredentials(t *testing.T) {
	private := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetIsPublic(false).
		SetUserName("user").
		SetPassword("secret").
		Build()
	opts, ok, err := imagePullResolverOptions(private)
	if err != nil {
		t.Fatalf("private: %v", err)
	}
	if !ok || registryHost(t, opts, "registry.example").Authorizer == nil {
		t.Fatal("expected authorizer on private registry")
	}
	creds := imagePullCredentials(private)
	if creds == nil {
		t.Fatal("expected credentials for private registry")
	}
	user, pass, err := creds("registry.example")
	if err != nil {
		t.Fatalf("creds: %v", err)
	}
	if user != "user" || pass != "secret" {
		t.Fatalf("got %s / %s", user, pass)
	}
	user, pass, err = creds("other.example")
	if err != nil {
		t.Fatalf("other host: %v", err)
	}
	if user != "" || pass != "" {
		t.Fatal("expected no credentials for a different registry host")
	}
}

func TestImagePullResolverOptions_NilRegistry(t *testing.T) {
	_, ok, err := imagePullResolverOptions(nil)
	if err != nil {
		t.Fatalf("nil registry: %v", err)
	}
	if ok {
		t.Fatal("expected default pull without a custom resolver")
	}

	public := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetIsPublic(true).
		Build()
	_, ok, err = imagePullResolverOptions(public)
	if err != nil {
		t.Fatalf("public registry: %v", err)
	}
	if ok {
		t.Fatal("expected default pull for public https without ca or insecure")
	}
}

func TestImagePullResolverOptions_PrivateCASucceeds(t *testing.T) {
	caPEM, caKey := testImagePullCA(t)
	serverPEM, serverKey := testImagePullServerCert(t, caPEM, caKey)
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

	withCA := models.NewRegistryBuilder().
		SetURL("https://registry.example").
		SetIsPublic(true).
		SetCAB64(base64.StdEncoding.EncodeToString(caPEM)).
		Build()
	opts, ok, err := imagePullResolverOptions(withCA)
	if err != nil || !ok {
		t.Fatalf("resolver: ok=%v err=%v", ok, err)
	}
	resp, err := registryHost(t, opts, "registry.example").Client.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected extra ca to trust private CA: %v", err)
	}
	_ = resp.Body.Close()
}

func registryHost(t *testing.T, opts dockerresolver.ResolverOptions, host string) dockerresolver.RegistryHost {
	t.Helper()
	if opts.Hosts == nil {
		t.Fatal("expected Hosts")
	}
	hosts, err := opts.Hosts(host)
	if err != nil {
		t.Fatalf("hosts %q: %v", host, err)
	}
	if len(hosts) == 0 {
		t.Fatalf("no hosts for %q", host)
	}
	return hosts[0]
}

func transportTLS(t *testing.T, client *http.Client) *tls.Config {
	t.Helper()
	if client == nil {
		t.Fatal("nil http client")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatal("expected http.Transport TLS config")
	}
	return tr.TLSClientConfig
}

func testImagePullCAPEM(t *testing.T) []byte {
	t.Helper()
	pemBytes, _ := testImagePullCA(t)
	return pemBytes
}

func testImagePullCA(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
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

func testImagePullServerCert(t *testing.T, caPEM []byte, caKey *ecdsa.PrivateKey) ([]byte, []byte) {
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
