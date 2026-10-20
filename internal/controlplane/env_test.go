//revive:disable:nested-structs
package controlplane

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controlPlaneTestImage = "ghcr.io/datasance/controller:3.8.0-beta.0"

func validControlPlaneDocForEnvTest() *models.ControlPlaneManifest {
	doc := &models.ControlPlaneManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "ControlPlane"
	doc.Metadata.Name = "pot"
	doc.Spec.Controller.Image = controlPlaneTestImage
	doc.Spec.Auth = models.ValidEmbeddedAuthForTest()
	return doc
}

func TestBuildControllerEnv_MinimalRemoteIdentity(t *testing.T) {
	doc := validControlPlaneDocForEnvTest()

	env, err := BuildControllerEnv(doc, "uuid-1")
	if err != nil {
		t.Fatalf("BuildControllerEnv: %v", err)
	}
	if env["CONTROL_PLANE"] != "Remote" {
		t.Fatalf("CONTROL_PLANE=%q, want Remote", env["CONTROL_PLANE"])
	}
	if env["CONTROLLER_UUID"] != "uuid-1" {
		t.Fatalf("CONTROLLER_UUID=%q", env["CONTROLLER_UUID"])
	}
	if env["CONTROLLER_NAMESPACE"] != models.ControlPlaneDefaultNamespace {
		t.Fatalf("CONTROLLER_NAMESPACE=%q", env["CONTROLLER_NAMESPACE"])
	}
	if env["CONTROLLER_NAME"] != "pot" {
		t.Fatalf("CONTROLLER_NAME=%q", env["CONTROLLER_NAME"])
	}
	if env["AUTH_MODE"] != "embedded" {
		t.Fatalf("AUTH_MODE=%q", env["AUTH_MODE"])
	}
	if _, ok := env["API_PORT"]; ok {
		t.Fatal("expected API_PORT omitted when port unset")
	}
	if _, ok := env["SERVER_DEV_MODE"]; ok {
		t.Fatal("expected SERVER_DEV_MODE omitted when TLS unset")
	}
}

func TestBuildControllerEnv_FullProjection(t *testing.T) {
	port := 51121
	consolePort := 8008
	dbPort := 5432
	dbSSL := true
	audit := true
	retention := 14
	cleanup := 86400
	captureIP := false
	natsEnabled := true
	vaultEnabled := true
	registry := 1
	trustProxy := true
	insecureAllowHTTP := false
	insecureAllowBootstrapLog := false
	consoleClientEnabled := true
	rateLimitEnabled := true
	maxRequests := 60
	windowMs := 60000
	sessionTTLMs := 600000
	accessTokenTTL := 900
	refreshTokenTTL := 3600
	interactionTTL := 600
	grantTTL := 600
	oidcSessionTTL := 3600
	idTokenTTL := 900

	doc := &models.ControlPlaneManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "ControlPlane"
	doc.Metadata.Name = "pot"
	doc.Metadata.Namespace = "cp-ns"
	doc.Spec.Controller.Image = controlPlaneTestImage
	doc.Spec.Controller.Registry = &registry
	doc.Spec.Controller.Port = &port
	doc.Spec.Controller.PublicURL = "https://controller.example.com"
	doc.Spec.Controller.TrustProxy = &trustProxy
	doc.Spec.Console.Port = &consolePort
	doc.Spec.Console.URL = "https://console.example.com"
	doc.Spec.LogLevel = "info"
	doc.Spec.Auth = &models.ControlPlaneAuthSpec{
		Mode:                      "external",
		InsecureAllowHTTP:         &insecureAllowHTTP,
		InsecureAllowBootstrapLog: &insecureAllowBootstrapLog,
		IssuerURL:                 "https://auth.example.com/realms/pot",
		Client: &models.ControlPlaneAuthClient{
			ID:     "pot-controller",
			Secret: "secret",
		},
		ConsoleClient:        "ecn-viewer",
		ConsoleClientEnabled: &consoleClientEnabled,
		RateLimit: &models.ControlPlaneAuthRateLimit{
			Enabled:              &rateLimitEnabled,
			MaxRequestsPerWindow: &maxRequests,
			WindowMs:             &windowMs,
		},
		SessionStore: &models.ControlPlaneAuthSessionStore{
			Type:   "database",
			TTLMs:  &sessionTTLMs,
			Secret: "session-secret",
		},
		TokenTTL: &models.ControlPlaneAuthTokenTTL{
			AccessTokenTTLSeconds:  &accessTokenTTL,
			RefreshTokenTTLSeconds: &refreshTokenTTL,
		},
		OIDCTTL: &models.ControlPlaneAuthOIDCTTL{
			InteractionTTLSeconds: &interactionTTL,
			GrantTTLSeconds:       &grantTTL,
			SessionTTLSeconds:     &oidcSessionTTL,
			IDTokenTTLSeconds:     &idTokenTTL,
		},
	}
	doc.Spec.Database = &struct {
		Provider     string `yaml:"provider,omitempty" json:"provider,omitempty"`
		User         string `yaml:"user,omitempty" json:"user,omitempty"`
		Host         string `yaml:"host,omitempty" json:"host,omitempty"`
		Port         *int   `yaml:"port,omitempty" json:"port,omitempty"`
		Password     string `yaml:"password,omitempty" json:"password,omitempty"`
		DatabaseName string `yaml:"databaseName,omitempty" json:"databaseName,omitempty"`
		SSL          *bool  `yaml:"ssl,omitempty" json:"ssl,omitempty"`
		CA           string `yaml:"ca,omitempty" json:"ca,omitempty"`
	}{
		Provider:     "postgres",
		User:         "cp",
		Host:         "db.local",
		Port:         &dbPort,
		Password:     "pw",
		DatabaseName: "controller",
		SSL:          &dbSSL,
		CA:           "ca-b64",
	}
	doc.Spec.Events = &struct {
		AuditEnabled     *bool `yaml:"auditEnabled,omitempty" json:"auditEnabled,omitempty"`
		RetentionDays    *int  `yaml:"retentionDays,omitempty" json:"retentionDays,omitempty"`
		CleanupInterval  *int  `yaml:"cleanupInterval,omitempty" json:"cleanupInterval,omitempty"`
		CaptureIPAddress *bool `yaml:"captureIpAddress,omitempty" json:"captureIpAddress,omitempty"`
	}{
		AuditEnabled:     &audit,
		RetentionDays:    &retention,
		CleanupInterval:  &cleanup,
		CaptureIPAddress: &captureIP,
	}
	doc.Spec.SystemMicroservices = &struct {
		Router map[string]string `yaml:"router,omitempty" json:"router,omitempty"`
		NATS   map[string]string `yaml:"nats,omitempty" json:"nats,omitempty"`
	}{
		Router: map[string]string{
			"amd64":   "router-amd64",
			"arm64":   "router-arm64",
			"riscv64": "router-riscv",
			"arm":     "router-arm",
		},
		NATS: map[string]string{
			"amd64": "nats-amd64",
			"arm":   "nats-arm",
		},
	}
	doc.Spec.NATS = &struct {
		Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	}{Enabled: &natsEnabled}
	doc.Spec.Vault = &models.ControlPlaneVaultSpec{
		Enabled:  &vaultEnabled,
		Provider: "hashicorp",
		BasePath: "pot/secrets",
		Hashicorp: &struct {
			Address string `yaml:"address,omitempty" json:"address,omitempty"`
			Token   string `yaml:"token,omitempty" json:"token,omitempty"`
			Mount   string `yaml:"mount,omitempty" json:"mount,omitempty"`
		}{
			Address: "http://vault:8200",
			Token:   "token",
			Mount:   "kv",
		},
	}
	cert := base64.StdEncoding.EncodeToString([]byte("cert"))
	key := base64.StdEncoding.EncodeToString([]byte("key"))
	doc.Spec.TLS = &models.ControlPlaneTLSConfig{
		Base64: &models.ControlPlaneTLSBase64{Cert: cert, Key: key},
	}

	env, err := BuildControllerEnv(doc, "uuid-full")
	if err != nil {
		t.Fatalf("BuildControllerEnv: %v", err)
	}

	assertEnv(t, env, "CONTROLLER_PUBLIC_URL", "https://controller.example.com")
	assertEnv(t, env, "TRUST_PROXY", "true")
	assertEnv(t, env, "API_PORT", "51121")
	assertEnv(t, env, "CONSOLE_PORT", "8008")
	assertEnv(t, env, "CONSOLE_URL", "https://console.example.com")
	assertEnv(t, env, "LOG_LEVEL", "info")
	assertEnv(t, env, "AUTH_MODE", "external")
	assertEnv(t, env, "AUTH_INSECURE_ALLOW_HTTP", "false")
	assertEnv(t, env, "AUTH_INSECURE_ALLOW_BOOTSTRAP_LOG", "false")
	assertEnv(t, env, "OIDC_ISSUER_URL", "https://auth.example.com/realms/pot")
	assertEnv(t, env, "OIDC_CLIENT_ID", "pot-controller")
	assertEnv(t, env, "OIDC_CLIENT_SECRET", "secret")
	assertEnv(t, env, "OIDC_CONSOLE_CLIENT_ID", "ecn-viewer")
	assertEnv(t, env, "AUTH_CONSOLE_CLIENT_ENABLED", "true")
	assertEnv(t, env, "AUTH_RATE_LIMIT_ENABLED", "true")
	assertEnv(t, env, "AUTH_RATE_LIMIT_MAX_REQUESTS", "60")
	assertEnv(t, env, "AUTH_RATE_LIMIT_WINDOW_MS", "60000")
	assertEnv(t, env, "AUTH_SESSION_STORE_TYPE", "database")
	assertEnv(t, env, "AUTH_SESSION_STORE_TTL_MS", "600000")
	assertEnv(t, env, "AUTH_SESSION_SECRET", "session-secret")
	assertEnv(t, env, "AUTH_ACCESS_TOKEN_TTL_SECONDS", "900")
	assertEnv(t, env, "AUTH_REFRESH_TOKEN_TTL_SECONDS", "3600")
	assertEnv(t, env, "AUTH_OIDC_INTERACTION_TTL_SECONDS", "600")
	assertEnv(t, env, "AUTH_OIDC_GRANT_TTL_SECONDS", "600")
	assertEnv(t, env, "AUTH_OIDC_SESSION_TTL_SECONDS", "3600")
	assertEnv(t, env, "AUTH_OIDC_ID_TOKEN_TTL_SECONDS", "900")
	assertEnv(t, env, "DB_PROVIDER", "postgres")
	assertEnv(t, env, "DB_PORT", "5432")
	assertEnv(t, env, "DB_USE_SSL", "true")
	assertEnv(t, env, "EVENT_AUDIT_ENABLED", "true")
	assertEnv(t, env, "EVENT_RETENTION_DAYS", "14")
	assertEnv(t, env, "ROUTER_IMAGE_1", "router-amd64")
	assertEnv(t, env, "ROUTER_IMAGE_2", "router-arm64")
	assertEnv(t, env, "ROUTER_IMAGE_3", "router-riscv")
	assertEnv(t, env, "ROUTER_IMAGE_4", "router-arm")
	assertEnv(t, env, "NATS_IMAGE_1", "nats-amd64")
	assertEnv(t, env, "NATS_IMAGE_4", "nats-arm")
	if _, ok := env["NATS_IMAGE_2"]; ok {
		t.Fatal("expected NATS_IMAGE_2 omitted")
	}
	assertEnv(t, env, "NATS_ENABLED", "true")
	assertEnv(t, env, "VAULT_ENABLED", "true")
	assertEnv(t, env, "VAULT_HASHICORP_ADDRESS", "http://vault:8200")
	assertEnv(t, env, "TLS_BASE64_CERT", cert)
	assertEnv(t, env, "TLS_BASE64_KEY", key)
	assertEnv(t, env, "SERVER_DEV_MODE", "false")
	if _, ok := env["OIDC_ISSUER_TLS_BASE64_CA"]; ok {
		t.Fatal("expected OIDC_ISSUER_TLS_BASE64_CA omitted")
	}
}

func TestBuildControllerEnv_IssuerCA(t *testing.T) {
	doc := validControlPlaneDocForEnvTest()
	ca := base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	doc.Spec.Auth.IssuerCA = ca

	env, err := BuildControllerEnv(doc, "uuid-issuer-ca")
	if err != nil {
		t.Fatalf("BuildControllerEnv: %v", err)
	}
	assertEnv(t, env, "OIDC_ISSUER_TLS_BASE64_CA", ca)
}

func TestBuildControllerEnv_EmbeddedBootstrapProjection(t *testing.T) {
	insecureAllowHTTP := true
	doc := validControlPlaneDocForEnvTest()
	doc.Spec.Auth.InsecureAllowHTTP = &insecureAllowHTTP

	env, err := BuildControllerEnv(doc, "uuid-bootstrap")
	if err != nil {
		t.Fatalf("BuildControllerEnv: %v", err)
	}
	assertEnv(t, env, "AUTH_MODE", "embedded")
	assertEnv(t, env, "AUTH_INSECURE_ALLOW_HTTP", "true")
	assertEnv(t, env, "OIDC_BOOTSTRAP_ADMIN_USERNAME", "admin")
	assertEnv(t, env, "OIDC_BOOTSTRAP_ADMIN_PASSWORD", "AdminPass123!")
}

func TestBuildControllerEnv_TLSPathFilenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		models.ControlPlaneTLSCertFilename,
		models.ControlPlaneTLSKeyFilename,
		models.ControlPlaneTLSCAFilename,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	doc := validControlPlaneDocForEnvTest()
	doc.Spec.TLS = &models.ControlPlaneTLSConfig{Path: dir}

	env, err := BuildControllerEnv(doc, "uuid-tls")
	if err != nil {
		t.Fatalf("BuildControllerEnv: %v", err)
	}
	assertEnv(t, env, "TLS_PATH_CERT", models.ControlPlaneTLSCertFilename)
	assertEnv(t, env, "TLS_PATH_KEY", models.ControlPlaneTLSKeyFilename)
	assertEnv(t, env, "TLS_PATH_INTERMEDIATE_CERT", models.ControlPlaneTLSCAFilename)
	assertEnv(t, env, "INTERMEDIATE_CERT", filepath.Join(ContainerCertMountPath, models.ControlPlaneTLSCAFilename))
	assertEnv(t, env, "SERVER_DEV_MODE", "false")
}

func TestBuildControllerEnv_RequiresAuth(t *testing.T) {
	doc := validControlPlaneDocForEnvTest()
	doc.Spec.Auth = nil
	if _, err := BuildControllerEnv(doc, "uuid-no-auth"); err == nil {
		t.Fatal("expected auth validation error")
	}
}

func assertEnv(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	if env[key] != want {
		t.Fatalf("%s=%q, want %q", key, env[key], want)
	}
}
