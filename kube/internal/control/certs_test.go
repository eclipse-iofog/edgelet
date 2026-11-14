package control

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/datastore"
	"k8s.io/client-go/tools/clientcmd"
)

func TestDefaultTLSDirectory(t *testing.T) {
	if got := tlsDir(""); got != "/var/lib/edgelet-kube/server/tls" {
		t.Fatalf("%s", got)
	}
}

func TestRestartReusesCertificates(t *testing.T) {
	srv := testServer(t)
	srv.TLSSan = []string{"san.example.test"}
	srv.TLSSanSecurity = true
	first, err := ensureCerts(srv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %o", info.Mode().Perm())
	}
	marker := filepath.Join(first.Dir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ca := mustRead(t, first.CACert)
	serving := mustRead(t, first.ServingCert)
	adminCert := mustRead(t, first.AdminCert)
	front := mustRead(t, first.FrontProxyCert)
	kubelet := mustRead(t, first.KubeletCert)
	kubeletServing := mustRead(t, first.KubeletServingCert)
	apiServer := mustRead(t, first.APIServerCert)
	proxy := mustRead(t, first.KubeProxyCert)

	srv.TLSSan = []string{"other.example.test"}
	srv.TLSSanSecurity = false
	second, err := ensureCerts(srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		name string
		path string
		want []byte
	}{
		{"ca", second.CACert, ca},
		{"serving", second.ServingCert, serving},
		{"admin", second.AdminCert, adminCert},
		{"front-proxy", second.FrontProxyCert, front},
		{"kubelet", second.KubeletCert, kubelet},
		{"kubelet-serving", second.KubeletServingCert, kubeletServing},
		{"apiserver", second.APIServerCert, apiServer},
		{"kube-proxy", second.KubeProxyCert, proxy},
	} {
		if got := mustRead(t, pair.path); !bytes.Equal(got, pair.want) {
			t.Fatalf("%s changed", pair.name)
		}
	}
	assertAdmin(t, second.AdminKubeconfig)
	if masters := mastersCount(t, second.Dir); masters != 2 {
		t.Fatalf("privileged principals %d", masters)
	}
	kubeletCert := mustCert(t, second.KubeletCert)
	if kubeletCert.Subject.CommonName != "system:node:node-a" {
		t.Fatalf("kubelet %s", kubeletCert.Subject.CommonName)
	}
	if !sameOrg(kubeletCert, "system:nodes") {
		t.Fatalf("kubelet org %v", kubeletCert.Subject.Organization)
	}
	proxyCert := mustCert(t, second.KubeProxyCert)
	if proxyCert.Subject.CommonName != "system:kube-proxy" || !sameOrg(proxyCert, "system:node-proxier") {
		t.Fatalf("kube-proxy %s %v", proxyCert.Subject.CommonName, proxyCert.Subject.Organization)
	}
	if got := mustCert(t, second.FrontProxyCert); got.Subject.CommonName != "front-proxy-client" {
		t.Fatalf("front-proxy %s", got.Subject.CommonName)
	}
}

func TestServingNames(t *testing.T) {
	node := net.ParseIP("10.1.0.9")
	other := net.ParseIP("10.9.9.9")
	orig := hostIPs
	t.Cleanup(func() { hostIPs = orig })
	hostIPs = func() ([]net.IP, error) {
		return []net.IP{node, other}, nil
	}

	secure := testServer(t)
	secure.TLSSan = []string{"san.example.test"}
	secure.TLSSanSecurity = true
	securePaths, err := ensureCerts(secure)
	if err != nil {
		t.Fatal(err)
	}
	secureCert := mustCert(t, securePaths.ServingCert)
	if hasIP(secureCert, other) {
		t.Fatal("host address is present")
	}
	if !hasIP(secureCert, node) || !hasIP(secureCert, net.ParseIP("127.0.0.1")) || !hasIP(secureCert, net.ParseIP("10.43.0.1")) {
		t.Fatal("node, loopback, or service address is absent")
	}
	if !hasDNS(secureCert, "san.example.test") || !hasDNS(secureCert, "localhost") || !hasDNS(secureCert, "node-a") {
		t.Fatalf("dns %v", secureCert.DNSNames)
	}
	if !hasDNS(secureCert, "kubernetes.default.svc.cluster.local") {
		t.Fatalf("dns %v", secureCert.DNSNames)
	}

	open := testServer(t)
	open.TLSSan = []string{"san.example.test"}
	open.TLSSanSecurity = false
	openPaths, err := ensureCerts(open)
	if err != nil {
		t.Fatal(err)
	}
	openCert := mustCert(t, openPaths.ServingCert)
	if !hasIP(openCert, other) || !hasIP(openCert, net.ParseIP("10.43.0.1")) {
		t.Fatal("host or service address is absent")
	}
	if !hasDNS(openCert, "san.example.test") {
		t.Fatal("tls-san is absent")
	}
	assertAdmin(t, openPaths.AdminKubeconfig)
}

func TestKubeletServingCert(t *testing.T) {
	srv := testServer(t)
	paths, err := ensureCerts(srv)
	if err != nil {
		t.Fatal(err)
	}
	ca := mustCert(t, paths.CACert)
	cert := mustCert(t, paths.KubeletServingCert)
	if err := cert.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if !hasDNS(cert, "node-a") || !hasIP(cert, net.ParseIP("10.1.0.9")) {
		t.Fatalf("names %v %v", cert.DNSNames, cert.IPAddresses)
	}
	if cert.Subject.CommonName != "node-a" {
		t.Fatalf("common name %s", cert.Subject.CommonName)
	}
	client := mustCert(t, paths.APIServerCert)
	if err := client.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if client.Subject.CommonName != "system:apiserver" || !sameOrg(client, "system:masters") {
		t.Fatalf("apiserver client %s %v", client.Subject.CommonName, client.Subject.Organization)
	}
}

func TestAdvertiseAddressUnset(t *testing.T) {
	srv := config.Defaults()
	srv.AdvertiseAddress = "192.0.2.9"
	srv.NodeIP = "192.0.2.8"
	got, err := advertiseAddress(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.9" {
		t.Fatalf("address %s", got)
	}
	srv.AdvertiseAddress = ""
	got, err = advertiseAddress(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.8" {
		t.Fatalf("address %s", got)
	}
	srv.NodeIP = ""
	orig := nodeIPForKubelet
	t.Cleanup(func() { nodeIPForKubelet = orig })
	nodeIPForKubelet = func(string) (net.IP, error) {
		return net.ParseIP("192.0.2.7"), nil
	}
	got, err = advertiseAddress(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.7" {
		t.Fatalf("address %s", got)
	}
	if srv.AdvertisePort != srv.HTTPSListenPort {
		t.Fatalf("advertise port %d listen %d", srv.AdvertisePort, srv.HTTPSListenPort)
	}
}

func TestEncryptionFilePassedOnlyWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	srv := config.Defaults()
	srv.DataDir = dir

	got, err := encryptionConfig(srv)
	if err != nil || got != "" {
		t.Fatalf("config %q err %v", got, err)
	}
	args := apiServerArgs(srv, "unix:///tmp/kine.sock", "192.0.2.10", got, certPaths{})
	if strings.Contains(strings.Join(args, "\n"), "encryption-provider-config") {
		t.Fatal(args)
	}

	srv.SecretsEncryption = true
	got, err = encryptionConfig(srv)
	if err != nil {
		t.Fatal(err)
	}
	want := datastore.EncryptionConfigPath(dir)
	if got != want {
		t.Fatalf("path %s", got)
	}
	args = apiServerArgs(srv, "unix:///tmp/kine.sock", "192.0.2.10", got, certPaths{})
	if !containsArg(args, "--encryption-provider-config="+want) {
		t.Fatal(args)
	}

	srv.SecretsEncryption = false
	if _, err := encryptionConfig(srv); err == nil {
		t.Fatal("expected a startup error")
	}

	srv.SecretsEncryption = true
	if err := os.Remove(want); err != nil {
		t.Fatal(err)
	}
	db := datastore.StateDB(dir)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := encryptionConfig(srv); err == nil {
		t.Fatal("expected a startup error")
	}
}

func testServer(t *testing.T) config.Server {
	t.Helper()
	dir := t.TempDir()
	srv := config.Defaults()
	srv.DataDir = dir
	srv.WriteKubeconfig = filepath.Join(dir, "edgelet-kube.yaml")
	srv.NodeName = "node-a"
	srv.NodeIP = "10.1.0.9"
	return srv
}

func assertAdmin(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig mode %o", info.Mode().Perm())
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(cfg.CertData)
	if block == nil {
		t.Fatal("client certificate is missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "system:admin" || !sameOrg(cert, "system:masters") || len(cert.Subject.Organization) != 1 {
		t.Fatalf("subject %s %v", cert.Subject.CommonName, cert.Subject.Organization)
	}
}

func mastersCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".crt") {
			continue
		}
		cert := mustCert(t, filepath.Join(dir, entry.Name()))
		if sameOrg(cert, "system:masters") {
			count++
		}
	}
	return count
}

func sameOrg(cert *x509.Certificate, org string) bool {
	for _, got := range cert.Subject.Organization {
		if got == org {
			return true
		}
	}
	return false
}

func hasIP(cert *x509.Certificate, ip net.IP) bool {
	for _, got := range cert.IPAddresses {
		if got.Equal(ip) {
			return true
		}
	}
	return false
}

func hasDNS(cert *x509.Certificate, name string) bool {
	for _, got := range cert.DNSNames {
		if strings.EqualFold(got, name) {
			return true
		}
	}
	return false
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	cert, err := loadCert(path)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
