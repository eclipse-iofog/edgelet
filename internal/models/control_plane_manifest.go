//revive:disable:nested-structs
package models

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	controlPlaneKind       = "ControlPlane"
	controlPlaneAPIVersion = "edgelet.iofog.org/v1"
	// ControlPlaneTLS*Filename are container-relative names under /etc/iofog/controller-cert/.
	ControlPlaneTLSCertFilename = "tls.crt"
	ControlPlaneTLSKeyFilename  = "tls.key"
	ControlPlaneTLSCAFilename   = "ca.crt"
)

// ControlPlaneManifest is the operator YAML for kind ControlPlane.
type ControlPlaneManifest struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name" json:"name"`
		Namespace string            `yaml:"namespace,omitempty" json:"namespace,omitempty"`
		Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	} `yaml:"metadata" json:"metadata"`
	Spec ControlPlaneManifestSpec `yaml:"spec" json:"spec"`
}

// ControlPlaneManifestSpec holds controller deployment configuration.
type ControlPlaneManifestSpec struct {
	Controller struct {
		Image      string `yaml:"image" json:"image"`
		Registry   *int   `yaml:"registry,omitempty" json:"registry,omitempty"`
		Port       *int   `yaml:"port,omitempty" json:"port,omitempty"`
		PublicURL  string `yaml:"publicUrl,omitempty" json:"publicUrl,omitempty"`
		TrustProxy *bool  `yaml:"trustProxy,omitempty" json:"trustProxy,omitempty"`
	} `yaml:"controller" json:"controller"`
	Console  ControlPlaneConsoleSpec `yaml:"console,omitempty" json:"console,omitempty"`
	Auth     *ControlPlaneAuthSpec   `yaml:"auth,omitempty" json:"auth,omitempty"`
	Database *struct {
		Provider     string `yaml:"provider,omitempty" json:"provider,omitempty"`
		User         string `yaml:"user,omitempty" json:"user,omitempty"`
		Host         string `yaml:"host,omitempty" json:"host,omitempty"`
		Port         *int   `yaml:"port,omitempty" json:"port,omitempty"`
		Password     string `yaml:"password,omitempty" json:"password,omitempty"`
		DatabaseName string `yaml:"databaseName,omitempty" json:"databaseName,omitempty"`
		SSL          *bool  `yaml:"ssl,omitempty" json:"ssl,omitempty"`
		CA           string `yaml:"ca,omitempty" json:"ca,omitempty"`
	} `yaml:"database,omitempty" json:"database,omitempty"`
	Events *struct {
		AuditEnabled     *bool `yaml:"auditEnabled,omitempty" json:"auditEnabled,omitempty"`
		RetentionDays    *int  `yaml:"retentionDays,omitempty" json:"retentionDays,omitempty"`
		CleanupInterval  *int  `yaml:"cleanupInterval,omitempty" json:"cleanupInterval,omitempty"`
		CaptureIPAddress *bool `yaml:"captureIpAddress,omitempty" json:"captureIpAddress,omitempty"`
	} `yaml:"events,omitempty" json:"events,omitempty"`
	SystemMicroservices *struct {
		Router map[string]string `yaml:"router,omitempty" json:"router,omitempty"`
		NATS   map[string]string `yaml:"nats,omitempty" json:"nats,omitempty"`
	} `yaml:"systemMicroservices,omitempty" json:"systemMicroservices,omitempty"`
	NATS *struct {
		Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	} `yaml:"nats,omitempty" json:"nats,omitempty"`
	LogLevel string                 `yaml:"logLevel,omitempty" json:"logLevel,omitempty"`
	TLS      *ControlPlaneTLSConfig `yaml:"tls,omitempty" json:"tls,omitempty"`
	Vault    *ControlPlaneVaultSpec `yaml:"vault,omitempty" json:"vault,omitempty"`
	// Forbidden in Edgelet ControlPlane YAML (potctl REST import only).
	SiteCA  any `yaml:"siteCA,omitempty" json:"-"`
	LocalCA any `yaml:"localCA,omitempty" json:"-"`
}

// ControlPlaneConsoleSpec configures EdgeOps Console exposure.
type ControlPlaneConsoleSpec struct {
	Port *int   `yaml:"port,omitempty" json:"port,omitempty"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty"`
}

// ControlPlaneAuthSpec configures Controller OIDC (embedded or external).
type ControlPlaneAuthSpec struct {
	Mode                      string                        `yaml:"mode,omitempty" json:"mode,omitempty"`
	InsecureAllowHTTP         *bool                         `yaml:"insecureAllowHttp,omitempty" json:"insecureAllowHttp,omitempty"`
	InsecureAllowBootstrapLog *bool                         `yaml:"insecureAllowBootstrapLog,omitempty" json:"insecureAllowBootstrapLog,omitempty"`
	Bootstrap                 *ControlPlaneAuthBootstrap    `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
	IssuerURL                 string                        `yaml:"issuerUrl,omitempty" json:"issuerUrl,omitempty"`
	IssuerCA                  string                        `yaml:"issuerCa,omitempty" json:"issuerCa,omitempty"`
	Client                    *ControlPlaneAuthClient       `yaml:"client,omitempty" json:"client,omitempty"`
	ConsoleClient             string                        `yaml:"consoleClient,omitempty" json:"consoleClient,omitempty"`
	ConsoleClientEnabled      *bool                         `yaml:"consoleClientEnabled,omitempty" json:"consoleClientEnabled,omitempty"`
	RateLimit                 *ControlPlaneAuthRateLimit    `yaml:"rateLimit,omitempty" json:"rateLimit,omitempty"`
	SessionStore              *ControlPlaneAuthSessionStore `yaml:"sessionStore,omitempty" json:"sessionStore,omitempty"`
	TokenTTL                  *ControlPlaneAuthTokenTTL     `yaml:"tokenTtl,omitempty" json:"tokenTtl,omitempty"`
	OIDCTTL                   *ControlPlaneAuthOIDCTTL      `yaml:"oidcTtl,omitempty" json:"oidcTtl,omitempty"`
}

// ControlPlaneAuthBootstrap holds embedded OIDC bootstrap admin credentials.
type ControlPlaneAuthBootstrap struct {
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
}

// ControlPlaneAuthClient holds OIDC confidential client credentials.
type ControlPlaneAuthClient struct {
	ID     string `yaml:"id,omitempty" json:"id,omitempty"`
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// ControlPlaneAuthRateLimit configures auth endpoint rate limiting.
type ControlPlaneAuthRateLimit struct {
	Enabled              *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	MaxRequestsPerWindow *int  `yaml:"maxRequestsPerWindow,omitempty" json:"maxRequestsPerWindow,omitempty"`
	WindowMs             *int  `yaml:"windowMs,omitempty" json:"windowMs,omitempty"`
}

// ControlPlaneAuthSessionStore configures OAuth BFF session storage.
type ControlPlaneAuthSessionStore struct {
	Type   string `yaml:"type,omitempty" json:"type,omitempty"`
	TTLMs  *int   `yaml:"ttlMs,omitempty" json:"ttlMs,omitempty"`
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// ControlPlaneAuthTokenTTL configures JWT access/refresh token TTL overrides.
type ControlPlaneAuthTokenTTL struct {
	AccessTokenTTLSeconds  *int `yaml:"accessTokenTtlSeconds,omitempty" json:"accessTokenTtlSeconds,omitempty"`
	RefreshTokenTTLSeconds *int `yaml:"refreshTokenTtlSeconds,omitempty" json:"refreshTokenTtlSeconds,omitempty"`
}

// ControlPlaneAuthOIDCTTL configures embedded OIDC provider TTL overrides.
type ControlPlaneAuthOIDCTTL struct {
	InteractionTTLSeconds *int `yaml:"interactionTtlSeconds,omitempty" json:"interactionTtlSeconds,omitempty"`
	GrantTTLSeconds       *int `yaml:"grantTtlSeconds,omitempty" json:"grantTtlSeconds,omitempty"`
	SessionTTLSeconds     *int `yaml:"sessionTtlSeconds,omitempty" json:"sessionTtlSeconds,omitempty"`
	IDTokenTTLSeconds     *int `yaml:"idTokenTtlSeconds,omitempty" json:"idTokenTtlSeconds,omitempty"`
}

// ControlPlaneTLSBase64 holds inline TLS material for ControlPlane listeners.
type ControlPlaneTLSBase64 struct {
	Cert string `yaml:"cert,omitempty" json:"cert,omitempty"`
	Key  string `yaml:"key,omitempty" json:"key,omitempty"`
	CA   string `yaml:"ca,omitempty" json:"ca,omitempty"`
}

// ControlPlaneTLSConfig configures TLS for the control plane controller.
type ControlPlaneTLSConfig struct {
	Path   string                 `yaml:"path,omitempty" json:"path,omitempty"`
	Base64 *ControlPlaneTLSBase64 `yaml:"base64,omitempty" json:"base64,omitempty"`
}

// ControlPlaneVaultSpec mirrors optional vault provider blocks.
type ControlPlaneVaultSpec struct {
	Enabled   *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Provider  string `yaml:"provider,omitempty" json:"provider,omitempty"`
	BasePath  string `yaml:"basePath,omitempty" json:"basePath,omitempty"`
	Hashicorp *struct {
		Address string `yaml:"address,omitempty" json:"address,omitempty"`
		Token   string `yaml:"token,omitempty" json:"token,omitempty"`
		Mount   string `yaml:"mount,omitempty" json:"mount,omitempty"`
	} `yaml:"hashicorp,omitempty" json:"hashicorp,omitempty"`
	AWS *struct {
		Region      string `yaml:"region,omitempty" json:"region,omitempty"`
		AccessKeyID string `yaml:"accessKeyId,omitempty" json:"accessKeyId,omitempty"`
		AccessKey   string `yaml:"accessKey,omitempty" json:"accessKey,omitempty"`
	} `yaml:"aws,omitempty" json:"aws,omitempty"`
	Azure *struct {
		URL          string `yaml:"url,omitempty" json:"url,omitempty"`
		TenantID     string `yaml:"tenantId,omitempty" json:"tenantId,omitempty"`
		ClientID     string `yaml:"clientId,omitempty" json:"clientId,omitempty"`
		ClientSecret string `yaml:"clientSecret,omitempty" json:"clientSecret,omitempty"`
	} `yaml:"azure,omitempty" json:"azure,omitempty"`
	Google *struct {
		ProjectID   string `yaml:"projectId,omitempty" json:"projectId,omitempty"`
		Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	} `yaml:"google,omitempty" json:"google,omitempty"`
}

// ValidEmbeddedAuthForTest returns a minimal valid embedded auth block for unit tests.
func ValidEmbeddedAuthForTest() *ControlPlaneAuthSpec {
	return &ControlPlaneAuthSpec{
		Mode: "embedded",
		Bootstrap: &ControlPlaneAuthBootstrap{
			Username: "admin",
			Password: "AdminPass123!",
		},
	}
}

// ParseControlPlaneManifest decodes and validates ControlPlane YAML.
func ParseControlPlaneManifest(manifest string) (*ControlPlaneManifest, error) {
	doc := &ControlPlaneManifest{}
	dec := yaml.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(manifest))))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil {
		return nil, fmt.Errorf("invalid manifest YAML: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

// NormalizeDefaults applies metadata defaults before validation or env build.
func (m *ControlPlaneManifest) NormalizeDefaults() {
	if m == nil {
		return
	}
	if strings.TrimSpace(m.Metadata.Namespace) == "" {
		m.Metadata.Namespace = ControlPlaneDefaultNamespace
	}
}

// Validate checks ControlPlane manifest semantics.
func (m *ControlPlaneManifest) Validate() error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.APIVersion) != controlPlaneAPIVersion {
		return fmt.Errorf("apiVersion must be %s", controlPlaneAPIVersion)
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != controlPlaneKind {
		return fmt.Errorf("kind must be %s", controlPlaneKind)
	}
	if len(m.Metadata.Labels) > 0 {
		return errors.New("metadata.labels are forbidden on ControlPlane manifests")
	}
	name := strings.TrimSpace(m.Metadata.Name)
	if name == "" {
		return errors.New("metadata.name is required")
	}
	if len(name) > 63 || !localDeployNamePattern.MatchString(name) {
		return errors.New("metadata.name must be <= 63 characters and follow DNS-1123 label format")
	}
	ns := strings.TrimSpace(m.Metadata.Namespace)
	if ns != "" {
		if len(ns) > 63 || !localDeployNamePattern.MatchString(ns) {
			return errors.New("metadata.namespace must be <= 63 characters and follow DNS-1123 label format")
		}
	}
	if m.Spec.SiteCA != nil {
		return errors.New("spec.siteCA is not supported in ControlPlane manifests; import via Controller REST API after deploy")
	}
	if m.Spec.LocalCA != nil {
		return errors.New("spec.localCA is not supported in ControlPlane manifests; import via Controller REST API after deploy")
	}
	if strings.TrimSpace(m.Spec.Controller.Image) == "" {
		return errors.New("spec.controller.image is required")
	}
	if m.Spec.Controller.Port != nil && *m.Spec.Controller.Port <= 0 {
		return errors.New("spec.controller.port must be positive when set")
	}
	if m.Spec.Console.Port != nil && *m.Spec.Console.Port <= 0 {
		return errors.New("spec.console.port must be positive when set")
	}
	if err := validateControlPlaneAuth(m.Spec.Auth); err != nil {
		return err
	}
	return validateControlPlaneTLS(m.Spec.TLS)
}

func validateControlPlaneAuth(auth *ControlPlaneAuthSpec) error {
	if auth == nil {
		return errors.New("spec.auth is required")
	}
	mode := strings.TrimSpace(auth.Mode)
	if mode == "" {
		return errors.New("spec.auth.mode is required")
	}
	switch mode {
	case "embedded":
		if err := validateEmbeddedAuth(auth); err != nil {
			return err
		}
	case "external":
		if err := validateExternalAuth(auth); err != nil {
			return err
		}
	default:
		return errors.New("spec.auth.mode must be embedded or external")
	}
	return validateControlPlaneAuthOptions(auth)
}

func validateEmbeddedAuth(auth *ControlPlaneAuthSpec) error {
	if auth.Bootstrap == nil || strings.TrimSpace(auth.Bootstrap.Username) == "" {
		return errors.New("spec.auth.bootstrap.username is required when spec.auth.mode is embedded")
	}
	return validateBootstrapPassword(auth.Bootstrap.Password)
}

func validateExternalAuth(auth *ControlPlaneAuthSpec) error {
	if strings.TrimSpace(auth.IssuerURL) == "" {
		return errors.New("spec.auth.issuerUrl is required when spec.auth.mode is external")
	}
	if auth.Client == nil || strings.TrimSpace(auth.Client.ID) == "" {
		return errors.New("spec.auth.client.id is required when spec.auth.mode is external")
	}
	if auth.Client == nil || strings.TrimSpace(auth.Client.Secret) == "" {
		return errors.New("spec.auth.client.secret is required when spec.auth.mode is external")
	}
	return nil
}

const bootstrapPasswordComplexityMessage = "spec.auth.bootstrap.password must be at least 12 characters with 1 uppercase letter and 1 special character" // #nosec G101 -- validation error text, not a credential

func validateBootstrapPassword(password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("spec.auth.bootstrap.password is required when spec.auth.mode is embedded")
	}
	if len(password) < 12 {
		return errors.New(bootstrapPasswordComplexityMessage)
	}
	hasUpper := false
	hasSpecial := false
	for _, r := range password {
		if unicode.IsUpper(r) {
			hasUpper = true
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			hasSpecial = true
		}
	}
	if !hasUpper || !hasSpecial {
		return errors.New(bootstrapPasswordComplexityMessage)
	}
	return nil
}

func validateControlPlaneAuthOptions(auth *ControlPlaneAuthSpec) error {
	if err := validateControlPlaneIssuerCA(auth.IssuerCA); err != nil {
		return err
	}
	if auth.SessionStore != nil {
		sessionType := strings.TrimSpace(auth.SessionStore.Type)
		if sessionType != "" && sessionType != "memory" && sessionType != "database" {
			return errors.New("spec.auth.sessionStore.type must be memory or database")
		}
		if auth.SessionStore.TTLMs != nil && *auth.SessionStore.TTLMs <= 0 {
			return errors.New("spec.auth.sessionStore.ttlMs must be positive when set")
		}
	}
	if auth.RateLimit != nil {
		if auth.RateLimit.MaxRequestsPerWindow != nil && *auth.RateLimit.MaxRequestsPerWindow <= 0 {
			return errors.New("spec.auth.rateLimit.maxRequestsPerWindow must be positive when set")
		}
		if auth.RateLimit.WindowMs != nil && *auth.RateLimit.WindowMs <= 0 {
			return errors.New("spec.auth.rateLimit.windowMs must be positive when set")
		}
	}
	if auth.TokenTTL != nil {
		if auth.TokenTTL.AccessTokenTTLSeconds != nil && *auth.TokenTTL.AccessTokenTTLSeconds <= 0 {
			return errors.New("spec.auth.tokenTtl.accessTokenTtlSeconds must be positive when set")
		}
		if auth.TokenTTL.RefreshTokenTTLSeconds != nil && *auth.TokenTTL.RefreshTokenTTLSeconds <= 0 {
			return errors.New("spec.auth.tokenTtl.refreshTokenTtlSeconds must be positive when set")
		}
	}
	if auth.OIDCTTL != nil {
		if auth.OIDCTTL.InteractionTTLSeconds != nil && *auth.OIDCTTL.InteractionTTLSeconds <= 0 {
			return errors.New("spec.auth.oidcTtl.interactionTtlSeconds must be positive when set")
		}
		if auth.OIDCTTL.GrantTTLSeconds != nil && *auth.OIDCTTL.GrantTTLSeconds <= 0 {
			return errors.New("spec.auth.oidcTtl.grantTtlSeconds must be positive when set")
		}
		if auth.OIDCTTL.SessionTTLSeconds != nil && *auth.OIDCTTL.SessionTTLSeconds <= 0 {
			return errors.New("spec.auth.oidcTtl.sessionTtlSeconds must be positive when set")
		}
		if auth.OIDCTTL.IDTokenTTLSeconds != nil && *auth.OIDCTTL.IDTokenTTLSeconds <= 0 {
			return errors.New("spec.auth.oidcTtl.idTokenTtlSeconds must be positive when set")
		}
	}
	return nil
}

func validateControlPlaneIssuerCA(ca string) error {
	trimmed := strings.TrimSpace(ca)
	if trimmed == "" {
		return nil
	}
	if _, err := base64.StdEncoding.DecodeString(trimmed); err != nil {
		return fmt.Errorf("spec.auth.issuerCa must be valid base64: %w", err)
	}
	return nil
}

// ValidateControlPlaneTLSPath canonicalizes and validates a host TLS directory path.
func ValidateControlPlaneTLSPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("spec.tls.path must not be empty")
	}
	if !isValidHostPath(trimmed) {
		return "", errors.New("spec.tls.path must be an absolute host path")
	}
	if tlsPathHasTraversal(trimmed) {
		return "", errors.New("spec.tls.path must not contain parent directory traversal")
	}
	cleaned := filepath.Clean(trimmed)
	if isTLSPathRoot(cleaned) {
		return "", errors.New("spec.tls.path must not be root")
	}
	return cleaned, nil
}

// StatControlPlaneTLSDir returns metadata for a validated host TLS directory.
func StatControlPlaneTLSDir(hostDir string) (os.FileInfo, error) {
	cleaned, err := ValidateControlPlaneTLSPath(hostDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("spec.tls.path must be a directory")
	}
	return info, nil
}

// StatControlPlaneTLSFile returns metadata for a known TLS file under a validated host directory.
func StatControlPlaneTLSFile(hostDir, basename string) (os.FileInfo, error) {
	switch basename {
	case ControlPlaneTLSCertFilename, ControlPlaneTLSKeyFilename, ControlPlaneTLSCAFilename:
	default:
		return nil, fmt.Errorf("invalid control plane TLS filename %q", basename)
	}
	cleaned, err := ValidateControlPlaneTLSPath(hostDir)
	if err != nil {
		return nil, err
	}
	return os.Stat(filepath.Join(cleaned, basename))
}

func tlsPathHasTraversal(path string) bool {
	for _, part := range splitPathParts(path) {
		if part == ".." {
			return true
		}
	}
	return false
}

func splitPathParts(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func isTLSPathRoot(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return true
	}
	vol := filepath.VolumeName(cleaned)
	if vol == "" {
		return false
	}
	rest := strings.TrimPrefix(cleaned, vol)
	rest = strings.Trim(rest, `/\`)
	return rest == ""
}

func validateControlPlaneTLS(tls *ControlPlaneTLSConfig) error {
	if tls == nil {
		return nil
	}
	path := strings.TrimSpace(tls.Path)
	hasPath := path != ""
	hasBase64 := tls.Base64 != nil && (strings.TrimSpace(tls.Base64.Cert) != "" ||
		strings.TrimSpace(tls.Base64.Key) != "" ||
		strings.TrimSpace(tls.Base64.CA) != "")
	if hasPath && hasBase64 {
		return errors.New("spec.tls.path and spec.tls.base64 are mutually exclusive")
	}
	if !hasPath && !hasBase64 {
		return nil
	}
	if hasPath {
		cleaned, err := ValidateControlPlaneTLSPath(path)
		if err != nil {
			return err
		}
		tls.Path = cleaned
		if _, err := StatControlPlaneTLSDir(cleaned); err != nil {
			if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
				return fmt.Errorf("spec.tls.path must exist on the Edgelet host: %w", err)
			}
			return err
		}
		for _, file := range []string{ControlPlaneTLSCertFilename, ControlPlaneTLSKeyFilename} {
			if _, err := StatControlPlaneTLSFile(cleaned, file); err != nil {
				return fmt.Errorf("spec.tls.path must contain %s: %w", file, err)
			}
		}
		return nil
	}
	b := tls.Base64
	if strings.TrimSpace(b.Cert) == "" {
		return errors.New("spec.tls.base64.cert is required when spec.tls.base64 is set")
	}
	if strings.TrimSpace(b.Key) == "" {
		return errors.New("spec.tls.base64.key is required when spec.tls.base64 is set")
	}
	for _, item := range []struct {
		field string
		value string
	}{
		{"cert", b.Cert},
		{"key", b.Key},
		{"ca", b.CA},
	} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.value)); err != nil {
			return fmt.Errorf("spec.tls.base64.%s must be valid base64: %w", item.field, err)
		}
	}
	return nil
}

// ManifestControllerImage returns spec.controller.image trimmed.
func (m *ControlPlaneManifest) ManifestControllerImage() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.Spec.Controller.Image)
}

// ControllerRegistryID returns spec.controller.registry when set.
func (m *ControlPlaneManifest) ControllerRegistryID() (int, bool) {
	if m == nil || m.Spec.Controller.Registry == nil {
		return 0, false
	}
	return *m.Spec.Controller.Registry, true
}
