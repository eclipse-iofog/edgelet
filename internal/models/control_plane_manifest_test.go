//revive:disable:nested-structs
package models

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const controlPlaneTestImage = "ghcr.io/datasance/controller:3.8.0-beta.0"

func validControlPlaneManifestForTest() *ControlPlaneManifest {
	doc := &ControlPlaneManifest{}
	doc.APIVersion = controlPlaneAPIVersion
	doc.Kind = controlPlaneKind
	doc.Metadata.Name = "pot"
	doc.Metadata.Namespace = "default"
	doc.Spec.Controller.Image = controlPlaneTestImage
	doc.Spec.Auth = ValidEmbeddedAuthForTest()
	return doc
}

func TestControlPlaneManifestValidate_Minimal(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected minimal manifest to pass: %v", err)
	}
}

func TestControlPlaneManifestValidate_RequiresAuth(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth = nil
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth is required") {
		t.Fatalf("expected auth required error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_RequiresAuthMode(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Mode = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.mode is required") {
		t.Fatalf("expected auth mode required error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_EmbeddedRequiresBootstrapUsername(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Bootstrap.Username = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.bootstrap.username is required") {
		t.Fatalf("expected bootstrap username error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_EmbeddedPasswordComplexity(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Bootstrap.Password = "short"
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), bootstrapPasswordComplexityMessage) {
		t.Fatalf("expected password complexity error, got: %v", err)
	}

	doc.Spec.Auth.Bootstrap.Password = "alllowercase12!"
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), bootstrapPasswordComplexityMessage) {
		t.Fatalf("expected missing uppercase error, got: %v", err)
	}

	doc.Spec.Auth.Bootstrap.Password = "NoSpecialChar12"
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), bootstrapPasswordComplexityMessage) {
		t.Fatalf("expected missing special char error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_ExternalRequiresClientAndIssuer(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Mode = "external"
	doc.Spec.Auth.Bootstrap = nil
	doc.Spec.Auth.IssuerURL = "https://auth.example.com/realms/pot"
	doc.Spec.Auth.Client = &ControlPlaneAuthClient{ID: "client", Secret: "secret"}
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected valid external auth, got: %v", err)
	}

	doc.Spec.Auth.IssuerURL = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.issuerUrl is required") {
		t.Fatalf("expected issuerUrl error, got: %v", err)
	}

	doc.Spec.Auth.IssuerURL = "https://auth.example.com/realms/pot"
	doc.Spec.Auth.Client.ID = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.client.id is required") {
		t.Fatalf("expected client.id error, got: %v", err)
	}

	doc.Spec.Auth.Client.ID = "client"
	doc.Spec.Auth.Client.Secret = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.client.secret is required") {
		t.Fatalf("expected client.secret error, got: %v", err)
	}
}

func TestParseControlPlaneManifest_IssuerCA(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: external
    issuerUrl: https://auth.example.com/realms/pot
    issuerCa: cGVt
    client:
      id: client
      secret: secret
`
	doc, err := ParseControlPlaneManifest(manifest)
	if err != nil {
		t.Fatalf("ParseControlPlaneManifest: %v", err)
	}
	if doc.Spec.Auth.IssuerCA != "cGVt" {
		t.Fatalf("issuerCa=%q", doc.Spec.Auth.IssuerCA)
	}
}

func TestControlPlaneManifestValidate_IssuerCA(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	ca := base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	doc.Spec.Auth.IssuerCA = ca
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected embedded issuerCa to pass: %v", err)
	}

	doc.Spec.Auth.Mode = "external"
	doc.Spec.Auth.Bootstrap = nil
	doc.Spec.Auth.IssuerURL = "https://auth.example.com/realms/pot"
	doc.Spec.Auth.Client = &ControlPlaneAuthClient{ID: "client", Secret: "secret"}
	doc.Spec.Auth.IssuerCA = ca
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected external issuerCa to pass: %v", err)
	}

	doc.Spec.Auth.IssuerCA = "not-base64!!!"
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.issuerCa must be valid base64") {
		t.Fatalf("expected issuerCa base64 error, got: %v", err)
	}

	doc.Spec.Auth.IssuerCA = "   "
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected blank issuerCa to pass: %v", err)
	}
}

func TestControlPlaneManifestValidate_RequiresControllerImage(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Controller.Image = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.controller.image is required") {
		t.Fatalf("expected controller image error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_ForbidsMetadataLabels(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Metadata.Labels = map[string]string{"team": "edge"}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "metadata.labels are forbidden") {
		t.Fatalf("expected labels forbidden error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_RejectsSiteCA(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: AdminPass123!
  siteCA:
    cert: ignored
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "siteCA") {
		t.Fatalf("expected siteCA rejection, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_RejectsLocalCA(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: AdminPass123!
  localCA:
    cert: ignored
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "localCA") {
		t.Fatalf("expected localCA rejection, got: %v", err)
	}
}

func TestParseControlPlaneManifest_RejectsMissingAuth(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "spec.auth is required") {
		t.Fatalf("expected missing auth error, got: %v", err)
	}
}

func TestParseControlPlaneManifest_RejectsLegacyAuthURL(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: external
    url: https://auth.example.com/
    bootstrap:
      username: admin
      password: AdminPass123!
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("expected legacy auth.url rejection, got: %v", err)
	}
}

func TestParseControlPlaneManifest_RejectsLegacyAuthExternal(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: external
    external:
      issuerUrl: https://auth.example.com/
      clientId: client
      clientSecret: secret
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("expected legacy auth.external rejection, got: %v", err)
	}
}

func TestParseControlPlaneManifest_RejectsLegacyECNViewerPort(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: AdminPass123!
  ecnViewerPort: 8008
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "ecnViewerPort") {
		t.Fatalf("expected legacy ecnViewerPort rejection, got: %v", err)
	}
}

func TestParseControlPlaneManifest_RejectsLegacyHTTPS(t *testing.T) {
	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: AdminPass123!
  https:
    path: /etc/certs
`
	_, err := ParseControlPlaneManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected legacy spec.https rejection, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_InvalidAuthMode(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Mode = "keycloak"
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.mode must be embedded or external") {
		t.Fatalf("expected auth mode error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_InvalidSessionStoreType(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.SessionStore = &ControlPlaneAuthSessionStore{Type: "redis"}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.auth.sessionStore.type must be memory or database") {
		t.Fatalf("expected session store type error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_TLSEmptyBlockAllowed(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.TLS = &ControlPlaneTLSConfig{}
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected empty tls block to pass: %v", err)
	}
}

func TestControlPlaneManifestValidate_TLSPathAndBase64MutuallyExclusive(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	dir := t.TempDir()
	writeControlPlaneCertFiles(t, dir, false)
	doc.Spec.TLS = &ControlPlaneTLSConfig{
		Path: dir,
		Base64: &ControlPlaneTLSBase64{
			Cert: base64.StdEncoding.EncodeToString([]byte("cert")),
			Key:  base64.StdEncoding.EncodeToString([]byte("key")),
		},
	}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_TLSPathRequiresAbsoluteExistingDir(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.TLS = &ControlPlaneTLSConfig{Path: "relative/certs"}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected absolute path error, got: %v", err)
	}

	dir := t.TempDir()
	doc.Spec.TLS.Path = dir
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "tls.crt") {
		t.Fatalf("expected missing tls.crt error, got: %v", err)
	}

	writeControlPlaneCertFiles(t, dir, true)
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected valid tls path to pass: %v", err)
	}
}

func TestValidateControlPlaneTLSPath_RejectsTraversalAndRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	valid := filepath.Join(base, "certs")
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "valid", path: valid, wantErr: ""},
		{name: "relative", path: "relative/certs", wantErr: "absolute"},
		{name: "parent traversal", path: valid + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside", wantErr: "traversal"},
		{name: "embedded traversal", path: filepath.Join(base, "nested") + string(filepath.Separator) + ".." + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape", wantErr: "traversal"},
		{name: "unix root", path: string(filepath.Separator), wantErr: "root"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateControlPlaneTLSPath(tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateControlPlaneTLSPath(%q): %v", tc.path, err)
				}
				if got != filepath.Clean(tc.path) {
					t.Fatalf("got cleaned path %q, want %q", got, filepath.Clean(tc.path))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateControlPlaneTLSPath(%q) err=%v, want substring %q", tc.path, err, tc.wantErr)
			}
		})
	}
}

func TestStatControlPlaneTLSFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeControlPlaneCertFiles(t, dir, true)

	info, err := StatControlPlaneTLSFile(dir, ControlPlaneTLSCertFilename)
	if err != nil {
		t.Fatalf("StatControlPlaneTLSFile(cert): %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected file, got directory")
	}

	if _, err := StatControlPlaneTLSFile(dir, "evil.pem"); err == nil || !strings.Contains(err.Error(), "invalid control plane TLS filename") {
		t.Fatalf("expected invalid basename error, got: %v", err)
	}

	traversal := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	if _, err := StatControlPlaneTLSFile(traversal, ControlPlaneTLSCertFilename); err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal rejection, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_TLSPathCanonicalizes(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	dir := t.TempDir()
	writeControlPlaneCertFiles(t, dir, false)
	doc.Spec.TLS = &ControlPlaneTLSConfig{Path: dir + string(filepath.Separator)}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Spec.TLS.Path != filepath.Clean(dir) {
		t.Fatalf("got canonical path %q, want %q", doc.Spec.TLS.Path, filepath.Clean(dir))
	}
}

func TestControlPlaneManifestValidate_TLSPathRejectsTraversal(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	dir := t.TempDir()
	writeControlPlaneCertFiles(t, dir, false)
	traversal := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	doc.Spec.TLS = &ControlPlaneTLSConfig{Path: traversal}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal rejection, got: %v", err)
	}
}

func TestControlPlaneManifestValidate_TLSBase64RequiresValidEncoding(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.TLS = &ControlPlaneTLSConfig{
		Base64: &ControlPlaneTLSBase64{
			Cert: "not-base64!!!",
			Key:  base64.StdEncoding.EncodeToString([]byte("key")),
		},
	}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("expected base64 validation error, got: %v", err)
	}
}

func TestControlPlaneManifestNormalizeDefaults(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Metadata.Namespace = ""
	doc.NormalizeDefaults()
	if doc.Metadata.Namespace != ControlPlaneDefaultNamespace {
		t.Fatalf("expected namespace default %q, got %q", ControlPlaneDefaultNamespace, doc.Metadata.Namespace)
	}
}

func TestControlPlaneManifestMaskSecrets(t *testing.T) {
	doc := validControlPlaneManifestForTest()
	doc.Spec.Auth.Client = &ControlPlaneAuthClient{ID: "client", Secret: "top-secret"}
	doc.Spec.Auth.IssuerCA = base64.StdEncoding.EncodeToString([]byte("ca-pem"))
	doc.Spec.Auth.SessionStore = &ControlPlaneAuthSessionStore{Secret: "session-secret"}
	doc.Spec.TLS = &ControlPlaneTLSConfig{
		Base64: &ControlPlaneTLSBase64{Cert: "cert-b64", Key: "key-b64", CA: "ca-b64"},
	}
	doc.MaskSecrets()
	if doc.Spec.Auth.Bootstrap.Password != controlPlaneSecretMask {
		t.Fatalf("expected masked bootstrap password, got %q", doc.Spec.Auth.Bootstrap.Password)
	}
	if doc.Spec.Auth.Client.Secret != controlPlaneSecretMask {
		t.Fatalf("expected masked client secret, got %q", doc.Spec.Auth.Client.Secret)
	}
	if doc.Spec.Auth.IssuerCA != controlPlaneSecretMask {
		t.Fatalf("expected masked issuer CA, got %q", doc.Spec.Auth.IssuerCA)
	}
	if doc.Spec.Auth.SessionStore.Secret != controlPlaneSecretMask {
		t.Fatalf("expected masked session secret, got %q", doc.Spec.Auth.SessionStore.Secret)
	}
	if doc.Spec.TLS.Base64.Key != controlPlaneSecretMask {
		t.Fatalf("expected masked tls key, got %q", doc.Spec.TLS.Base64.Key)
	}
}

func writeControlPlaneCertFiles(t *testing.T, dir string, withIntermediate bool) {
	t.Helper()
	for _, name := range []string{ControlPlaneTLSCertFilename, ControlPlaneTLSKeyFilename} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("pem"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if withIntermediate {
		if err := os.WriteFile(filepath.Join(dir, ControlPlaneTLSCAFilename), []byte("ca"), 0o600); err != nil {
			t.Fatalf("write intermediate cert: %v", err)
		}
	}
}
