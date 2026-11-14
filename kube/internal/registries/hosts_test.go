package registries

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dockerconfig "github.com/containerd/containerd/v2/core/remotes/docker/config"
)

func TestMissingFileUsesUpstream(t *testing.T) {
	dir := t.TempDir()
	auths, err := Prepare(dir, filepath.Join(dir, "registries.yaml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 0 {
		t.Fatalf("auths %#v", auths)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("hosts written for a missing file: %d", len(entries))
	}
	if got := endpoints(t, dir, "docker.io"); len(got) != 1 || got[0] != "https://registry-1.docker.io/v2" {
		t.Fatalf("docker.io endpoints %v", got)
	}
	if got := endpoints(t, dir, "registry.k8s.io"); len(got) != 1 || got[0] != "https://registry.k8s.io/v2" {
		t.Fatalf("registry.k8s.io endpoints %v", got)
	}
	if got := endpoints(t, dir, "quay.io"); len(got) != 1 || got[0] != "https://quay.io/v2" {
		t.Fatalf("quay.io endpoints %v", got)
	}
}

func TestPrivateRegistryFileAndDisableDefault(t *testing.T) {
	dir := t.TempDir()
	ignored := filepath.Join(dir, "ignored.yaml")
	if err := os.WriteFile(ignored, []byte("mirrors:\n  quay.io:\n    endpoint:\n      - https://ignored.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	caFile, certFile, keyFile := writeCert(t)
	chosen := filepath.Join(dir, "site.yaml")
	body := fmt.Sprintf(`
mirrors:
  docker.io:
    endpoint:
      - https://mirror.example.com/v2
    rewrite:
      "^library/(.*)": "docker/$1"
  "*":
    endpoint:
      - https://cache.example.com/v2
configs:
  mirror.example.com:
    auth:
      username: user
      password: secret
    tls:
      ca_file: %s
      cert_file: %s
      key_file: %s
      insecure_skip_verify: true
  quay.io:
    tls:
      insecure_skip_verify: true
`, caFile, certFile, keyFile)
	if err := os.WriteFile(chosen, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(dir, "certs.d")
	auths, err := Prepare(hosts, chosen, true)
	if err != nil {
		t.Fatal(err)
	}
	if auths["mirror.example.com"].Username != "user" || auths["mirror.example.com"].Password != "secret" {
		t.Fatalf("auths %#v", auths)
	}
	if got := endpoints(t, hosts, "docker.io"); len(got) != 1 || got[0] != "https://mirror.example.com/v2" {
		t.Fatalf("docker.io endpoints %v", got)
	}
	rewrites := rewritesFor(t, hosts, "docker.io", "mirror.example.com")
	if rewrites["^library/(.*)"] != "docker/$1" {
		t.Fatalf("rewrite %#v", rewrites)
	}
	hostsBody, err := os.ReadFile(filepath.Join(hosts, "docker.io", "hosts.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(hostsBody)
	for _, want := range []string{
		"ca = " + strconv.Quote(caFile),
		"client = [[" + strconv.Quote(certFile) + ", " + strconv.Quote(keyFile) + "]]",
		"skip_verify = true",
		"override_path = false",
	} {
		if want == "override_path = false" {
			if strings.Contains(text, "override_path") {
				t.Fatalf("override_path set\n%s", text)
			}
			continue
		}
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "registry-1.docker.io") {
		t.Fatalf("upstream fallback still present\n%s", text)
	}
	if got := endpoints(t, hosts, "quay.io"); len(got) != 1 || got[0] != "https://quay.io/v2" {
		t.Fatalf("quay.io lost its endpoint %v", got)
	}
	quay, err := os.ReadFile(filepath.Join(hosts, "quay.io", "hosts.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(quay), "skip_verify = true") {
		t.Fatalf("quay tls\n%s", quay)
	}
	if got := endpoints(t, hosts, "registry.k8s.io"); len(got) != 1 || got[0] != "https://cache.example.com/v2" {
		t.Fatalf("wildcard endpoints %v", got)
	}
	if got := endpoints(t, hosts, "docker.io"); len(got) != 1 || got[0] == "https://cache.example.com/v2" {
		t.Fatalf("wildcard replaced docker.io %v", got)
	}
}

func TestWildcardKeepsUpstreamWhenFallbackStays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs.d")
	file := filepath.Join(t.TempDir(), "registries.yaml")
	if err := os.WriteFile(file, []byte(`
mirrors:
  "*":
    endpoint:
      - https://cache.example.com/v2
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dir, file, false); err != nil {
		t.Fatal(err)
	}
	got := endpoints(t, dir, "quay.io")
	if len(got) != 2 || got[0] != "https://cache.example.com/v2" || got[1] != "https://quay.io/v2" {
		t.Fatalf("endpoints %v", got)
	}
}

func TestEndpointPathIsKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs.d")
	file := filepath.Join(t.TempDir(), "registries.yaml")
	if err := os.WriteFile(file, []byte(`
mirrors:
  docker.io:
    endpoint:
      - https://registry.example.com/project/registry
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dir, file, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "docker.io", "hosts.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "override_path = true") {
		t.Fatalf("hosts\n%s", body)
	}
	got := endpoints(t, dir, "docker.io")
	if len(got) != 1 || got[0] != "https://registry.example.com/project/registry" {
		t.Fatalf("endpoints %v", got)
	}
}

func TestAuthToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs.d")
	file := filepath.Join(t.TempDir(), "registries.yaml")
	if err := os.WriteFile(file, []byte("configs:\n  registry.example:\n    auth:\n      auth: dXNlcjpwYXNz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	auths, err := Prepare(dir, file, false)
	if err != nil {
		t.Fatal(err)
	}
	if auths["registry.example"].Token != "dXNlcjpwYXNz" {
		t.Fatalf("auths %#v", auths)
	}
}

func TestLocalhostEndpointUsesHTTP(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs.d")
	file := filepath.Join(t.TempDir(), "registries.yaml")
	if err := os.WriteFile(file, []byte("mirrors:\n  docker.io:\n    endpoint:\n      - 127.0.0.1:5000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dir, file, true); err != nil {
		t.Fatal(err)
	}
	got := endpoints(t, dir, "docker.io")
	if len(got) != 1 || got[0] != "http://127.0.0.1:5000/v2" {
		t.Fatalf("endpoints %v", got)
	}
	if got = endpoints(t, dir, "quay.io"); len(got) != 1 || got[0] != "https://quay.io/v2" {
		t.Fatalf("unlisted registry %v", got)
	}
}

func writeCert(t *testing.T) (caFile, certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile = filepath.Join(dir, "ca.pem")
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(caFile, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	return caFile, certFile, keyFile
}

func endpoints(t *testing.T, dir, host string) []string {
	t.Helper()
	fn := dockerconfig.ConfigureHosts(t.Context(), dockerconfig.HostOptions{
		HostDir: dockerconfig.HostDirFromRoot(dir),
	})
	got, err := fn(host)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(got))
	for _, item := range got {
		out = append(out, item.Scheme+"://"+item.Host+item.Path)
	}
	return out
}

func rewritesFor(t *testing.T, dir, registry, host string) map[string]string {
	t.Helper()
	fn := dockerconfig.ConfigureHosts(t.Context(), dockerconfig.HostOptions{
		HostDir: dockerconfig.HostDirFromRoot(dir),
	})
	got, err := fn(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got {
		if item.Host == host {
			return item.Rewrites
		}
	}
	t.Fatalf("host %s not found in %s", host, registry)
	return nil
}
