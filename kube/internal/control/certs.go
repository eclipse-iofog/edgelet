package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/internal/config"
)

// certPaths are the TLS files the API processes read.
// The directory is kept across restarts.
type certPaths struct {
	Dir                  string
	CACert               string
	CAKey                string
	ServingCert          string
	ServingKey           string
	ServiceAccountCert   string
	ServiceAccountKey    string
	FrontProxyCA         string
	FrontProxyCAKey      string
	FrontProxyCert       string
	FrontProxyKey        string
	AdminCert            string
	AdminKey             string
	AdminKubeconfig      string
	ControllerCert       string
	ControllerKey        string
	ControllerKubeconfig string
	SchedulerCert        string
	SchedulerKey         string
	SchedulerKubeconfig  string
	KubeletCert          string
	KubeletKey           string
	KubeletServingCert   string
	KubeletServingKey    string
	KubeletKubeconfig    string
	APIServerCert        string
	APIServerKey         string
	KubeProxyCert        string
	KubeProxyKey         string
	KubeProxyKubeconfig  string
}

// tlsDir is the directory that holds the CA, the serving certificate,
// and the client certificates.
func tlsDir(dataDir string) string {
	if dataDir == "" {
		dataDir = bundle.DefaultDataDir
	}
	return filepath.Join(dataDir, "server", "tls")
}

func ensureCerts(srv config.Server) (certPaths, error) {
	dir := tlsDir(srv.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return certPaths{}, fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directory must be owner-traversable
		return certPaths{}, fmt.Errorf("create certificate directory: %w", err)
	}
	admin := srv.WriteKubeconfig
	if admin == "" {
		admin = config.DefaultKubeconfig
	}
	paths := certPathsFor(dir, admin)
	mode, err := fileMode(srv.WriteKubeconfigMode)
	if err != nil {
		return certPaths{}, err
	}

	caCert, caKey, err := ensurePair(paths.CACert, paths.CAKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return newCA("edgelet-kube-ca")
	})
	if err != nil {
		return certPaths{}, err
	}
	if !caCert.IsCA {
		return certPaths{}, fmt.Errorf("certificate %s is not a CA", paths.CACert)
	}
	if _, _, err := ensurePair(paths.ServingCert, paths.ServingKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		tmpl, err := servingCertificate(srv)
		if err != nil {
			return nil, nil, err
		}
		return sign(caCert, caKey, tmpl)
	}); err != nil {
		return certPaths{}, err
	}
	if _, _, err := ensurePair(paths.KubeletServingCert, paths.KubeletServingKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		tmpl, err := kubeletServingCertificate(srv)
		if err != nil {
			return nil, nil, err
		}
		return sign(caCert, caKey, tmpl)
	}); err != nil {
		return certPaths{}, err
	}
	if _, _, err := ensurePair(paths.APIServerCert, paths.APIServerKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return sign(caCert, caKey, clientTemplate("system:apiserver", []string{"system:masters"}))
	}); err != nil {
		return certPaths{}, err
	}
	if _, _, err := ensurePair(paths.ServiceAccountCert, paths.ServiceAccountKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return sign(caCert, caKey, &x509.Certificate{
			Subject:   pkix.Name{CommonName: "edgelet-kube-service-account"},
			KeyUsage:  x509.KeyUsageDigitalSignature,
			NotBefore: time.Now().Add(-time.Hour),
			NotAfter:  time.Now().Add(10 * 365 * 24 * time.Hour),
		})
	}); err != nil {
		return certPaths{}, err
	}

	proxyCA, proxyKey, err := ensurePair(paths.FrontProxyCA, paths.FrontProxyCAKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return newCA("front-proxy-ca")
	})
	if err != nil {
		return certPaths{}, err
	}
	if !proxyCA.IsCA {
		return certPaths{}, fmt.Errorf("certificate %s is not a CA", paths.FrontProxyCA)
	}
	if _, _, err := ensurePair(paths.FrontProxyCert, paths.FrontProxyKey, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return sign(proxyCA, proxyKey, clientTemplate("front-proxy-client", nil))
	}); err != nil {
		return certPaths{}, err
	}

	server := serverURL(srv)
	name, err := nodeName(srv)
	if err != nil {
		return certPaths{}, err
	}
	clients := []struct {
		cert, key, kubeconfig, cn string
		org                       []string
		mode                      os.FileMode
	}{
		{paths.AdminCert, paths.AdminKey, paths.AdminKubeconfig, "system:admin", []string{"system:masters"}, mode},
		{paths.ControllerCert, paths.ControllerKey, paths.ControllerKubeconfig, "system:kube-controller-manager", []string{"system:kube-controller-manager"}, 0o600},
		{paths.SchedulerCert, paths.SchedulerKey, paths.SchedulerKubeconfig, "system:kube-scheduler", []string{"system:kube-scheduler"}, 0o600},
		{paths.KubeletCert, paths.KubeletKey, paths.KubeletKubeconfig, fmt.Sprintf("system:node:%s", name), []string{"system:nodes"}, 0o600},
		{paths.KubeProxyCert, paths.KubeProxyKey, paths.KubeProxyKubeconfig, "system:kube-proxy", []string{"system:node-proxier"}, 0o600},
	}
	for _, client := range clients {
		if err := ensureClient(caCert, caKey, client.cert, client.key, client.kubeconfig, server, client.cn, client.org, client.mode); err != nil {
			return certPaths{}, err
		}
	}
	return paths, nil
}

func certPathsFor(dir, admin string) certPaths {
	return certPaths{
		Dir:                  dir,
		CACert:               filepath.Join(dir, "ca.crt"),
		CAKey:                filepath.Join(dir, "ca.key"),
		ServingCert:          filepath.Join(dir, "serving.crt"),
		ServingKey:           filepath.Join(dir, "serving.key"),
		ServiceAccountCert:   filepath.Join(dir, "service-account.crt"),
		ServiceAccountKey:    filepath.Join(dir, "service-account.key"),
		FrontProxyCA:         filepath.Join(dir, "front-proxy-ca.crt"),
		FrontProxyCAKey:      filepath.Join(dir, "front-proxy-ca.key"),
		FrontProxyCert:       filepath.Join(dir, "front-proxy-client.crt"),
		FrontProxyKey:        filepath.Join(dir, "front-proxy-client.key"),
		AdminCert:            filepath.Join(dir, "client-admin.crt"),
		AdminKey:             filepath.Join(dir, "client-admin.key"),
		AdminKubeconfig:      admin,
		ControllerCert:       filepath.Join(dir, "client-controller-manager.crt"),
		ControllerKey:        filepath.Join(dir, "client-controller-manager.key"),
		ControllerKubeconfig: filepath.Join(dir, "controller-manager.kubeconfig"),
		SchedulerCert:        filepath.Join(dir, "client-scheduler.crt"),
		SchedulerKey:         filepath.Join(dir, "client-scheduler.key"),
		SchedulerKubeconfig:  filepath.Join(dir, "scheduler.kubeconfig"),
		KubeletCert:          filepath.Join(dir, "client-kubelet.crt"),
		KubeletKey:           filepath.Join(dir, "client-kubelet.key"),
		KubeletServingCert:   filepath.Join(dir, "kubelet-serving.crt"),
		KubeletServingKey:    filepath.Join(dir, "kubelet-serving.key"),
		KubeletKubeconfig:    filepath.Join(dir, "kubelet.kubeconfig"),
		APIServerCert:        filepath.Join(dir, "client-kube-apiserver.crt"),
		APIServerKey:         filepath.Join(dir, "client-kube-apiserver.key"),
		KubeProxyCert:        filepath.Join(dir, "client-kube-proxy.crt"),
		KubeProxyKey:         filepath.Join(dir, "client-kube-proxy.key"),
		KubeProxyKubeconfig:  filepath.Join(dir, "kube-proxy.kubeconfig"),
	}
}

func ensureClient(ca *x509.Certificate, caKey *ecdsa.PrivateKey, certPath, keyPath, kubeconfig, server, cn string, org []string, mode os.FileMode) error {
	cert, key, err := ensurePair(certPath, keyPath, func() (*x509.Certificate, *ecdsa.PrivateKey, error) {
		return sign(ca, caKey, clientTemplate(cn, org))
	})
	if err != nil {
		return err
	}
	return writeKubeconfig(kubeconfig, ca, cert, key, server, mode)
}

func ensurePair(certPath, keyPath string, issue func() (*x509.Certificate, *ecdsa.PrivateKey, error)) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certThere, err := fileThere(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyThere, err := fileThere(keyPath)
	if err != nil {
		return nil, nil, err
	}
	if certThere && keyThere {
		return loadPair(certPath, keyPath)
	}
	if certThere || keyThere {
		return nil, nil, fmt.Errorf("incomplete certificate pair %s", certPath)
	}
	cert, key, err := issue()
	if err != nil {
		return nil, nil, err
	}
	if err := writePair(certPath, keyPath, cert, key); err != nil {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		return nil, nil, err
	}
	return cert, key, nil
}

func fileThere(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("%s is a directory", path)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// serverURL is the address stored in kubeconfigs. Clients on this node use
// the listen address.
func serverURL(srv config.Server) string {
	port := srv.HTTPSListenPort
	if port == 0 {
		port = config.DefaultHTTPSListenPort
	}
	return "https://" + net.JoinHostPort(apiHost(srv.BindAddress), strconv.Itoa(port))
}

func fileMode(raw string) (os.FileMode, error) {
	if raw == "" {
		raw = config.DefaultKubeconfigMode
	}
	parsed, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fmt.Errorf("write-kubeconfig-mode %q is not a file mode", raw)
	}
	return os.FileMode(parsed), nil
}

func writeKubeconfig(path string, ca, cert *x509.Certificate, key *ecdsa.PrivateKey, server string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- kubeconfig parent must be traversable
		return fmt.Errorf("create kubeconfig directory: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user:
    client-certificate-data: %s
    client-key-data: %s
`, base64.StdEncoding.EncodeToString(caPEM), server, base64.StdEncoding.EncodeToString(certPEM), base64.StdEncoding.EncodeToString(keyPEM))
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return fmt.Errorf("write kubeconfig %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("write kubeconfig %s: %w", path, err)
	}
	return nil
}

func apiHost(bind string) string {
	switch bind {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	default:
		return bind
	}
}

func clientTemplate(cn string, org []string) *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn, Organization: org},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(10 * 365 * 24 * time.Hour),
	}
}

func newCA(cn string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func sign(ca *x509.Certificate, caKey *ecdsa.PrivateKey, tmpl *x509.Certificate) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber = serial
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, key.Public(), caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func loadPair(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := loadCert(certPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := loadKey(keyPath)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, fmt.Errorf("certificate %s does not match %s", certPath, keyPath)
	}
	return cert, key, nil
}

func loadCert(path string) (*x509.Certificate, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- path is a certificate under the server tls directory
	if err != nil {
		return nil, fmt.Errorf("read certificate %s: %w", path, err)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("certificate %s is not PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate %s: %w", path, err)
	}
	return cert, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- path is a key under the server tls directory
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("key %s is not PEM", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key %s: %w", path, err)
	}
	return key, nil
}

func writePair(certPath, keyPath string, cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	if err := writeCert(certPath, cert); err != nil {
		return err
	}
	return writeKey(keyPath, key)
}

func writeCert(path string, cert *x509.Certificate) error {
	body := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, body, 0o644); err != nil { // #nosec G306 -- certificate PEM is not a private key
		return fmt.Errorf("write certificate %s: %w", path, err)
	}
	return nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write key %s: %w", path, err)
	}
	return nil
}
