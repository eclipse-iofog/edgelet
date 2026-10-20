//revive:disable:nested-structs
package controlplane

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controlPlaneRemote = "Remote"

var controlPlaneRouterArchSlots = []struct {
	arch string
	slot int
}{
	{"amd64", 1},
	{"arm64", 2},
	{"riscv64", 3},
	{"arm", 4},
}

// BuildControllerEnv projects a validated ControlPlane manifest into Controller container env.
// Omitted spec blocks emit no variables; CONTROL_PLANE=Remote is always set.
func BuildControllerEnv(doc *models.ControlPlaneManifest, controllerUUID string) (map[string]string, error) {
	if doc == nil {
		return nil, errors.New("manifest is nil")
	}
	if strings.TrimSpace(controllerUUID) == "" {
		return nil, errors.New("controllerUUID is required")
	}
	doc.NormalizeDefaults()
	if err := doc.Validate(); err != nil {
		return nil, err
	}

	env := map[string]string{
		"CONTROL_PLANE":        controlPlaneRemote,
		"CONTROLLER_UUID":      strings.TrimSpace(controllerUUID),
		"CONTROLLER_NAMESPACE": strings.TrimSpace(doc.Metadata.Namespace),
		"CONTROLLER_NAME":      strings.TrimSpace(doc.Metadata.Name),
	}

	setEnvIfNonEmpty(env, "CONTROLLER_PUBLIC_URL", doc.Spec.Controller.PublicURL)
	if doc.Spec.Controller.TrustProxy != nil {
		setEnv(env, "TRUST_PROXY", strconv.FormatBool(*doc.Spec.Controller.TrustProxy))
	}
	if doc.Spec.Controller.Port != nil {
		setEnv(env, "API_PORT", strconv.Itoa(*doc.Spec.Controller.Port))
	}
	if doc.Spec.Console.Port != nil {
		setEnv(env, "CONSOLE_PORT", strconv.Itoa(*doc.Spec.Console.Port))
	}
	setEnvIfNonEmpty(env, "CONSOLE_URL", doc.Spec.Console.URL)
	setEnvIfNonEmpty(env, "LOG_LEVEL", doc.Spec.LogLevel)

	projectAuthEnv(env, doc.Spec.Auth)
	projectDatabaseEnv(env, doc.Spec.Database)
	projectEventsEnv(env, doc.Spec.Events)
	projectSystemMicroserviceEnv(env, doc.Spec.SystemMicroservices)
	projectNATSEnv(env, doc.Spec.NATS)
	projectTLSEnv(env, doc.Spec.TLS)
	projectVaultEnv(env, doc.Spec.Vault)

	return env, nil
}

func projectAuthEnv(env map[string]string, auth *models.ControlPlaneAuthSpec) {
	if auth == nil {
		return
	}
	setEnvIfNonEmpty(env, "AUTH_MODE", auth.Mode)
	if auth.InsecureAllowHTTP != nil {
		setEnv(env, "AUTH_INSECURE_ALLOW_HTTP", strconv.FormatBool(*auth.InsecureAllowHTTP))
	}
	if auth.InsecureAllowBootstrapLog != nil {
		setEnv(env, "AUTH_INSECURE_ALLOW_BOOTSTRAP_LOG", strconv.FormatBool(*auth.InsecureAllowBootstrapLog))
	}
	if auth.Bootstrap != nil {
		setEnvIfNonEmpty(env, "OIDC_BOOTSTRAP_ADMIN_USERNAME", auth.Bootstrap.Username)
		setEnvIfNonEmpty(env, "OIDC_BOOTSTRAP_ADMIN_PASSWORD", auth.Bootstrap.Password)
	}
	setEnvIfNonEmpty(env, "OIDC_ISSUER_URL", auth.IssuerURL)
	setEnvIfNonEmpty(env, "OIDC_ISSUER_TLS_BASE64_CA", auth.IssuerCA)
	if auth.Client != nil {
		setEnvIfNonEmpty(env, "OIDC_CLIENT_ID", auth.Client.ID)
		setEnvIfNonEmpty(env, "OIDC_CLIENT_SECRET", auth.Client.Secret)
	}
	setEnvIfNonEmpty(env, "OIDC_CONSOLE_CLIENT_ID", auth.ConsoleClient)
	if auth.ConsoleClientEnabled != nil {
		setEnv(env, "AUTH_CONSOLE_CLIENT_ENABLED", strconv.FormatBool(*auth.ConsoleClientEnabled))
	}
	if auth.RateLimit != nil {
		if auth.RateLimit.Enabled != nil {
			setEnv(env, "AUTH_RATE_LIMIT_ENABLED", strconv.FormatBool(*auth.RateLimit.Enabled))
		}
		if auth.RateLimit.MaxRequestsPerWindow != nil {
			setEnv(env, "AUTH_RATE_LIMIT_MAX_REQUESTS", strconv.Itoa(*auth.RateLimit.MaxRequestsPerWindow))
		}
		if auth.RateLimit.WindowMs != nil {
			setEnv(env, "AUTH_RATE_LIMIT_WINDOW_MS", strconv.Itoa(*auth.RateLimit.WindowMs))
		}
	}
	if auth.SessionStore != nil {
		setEnvIfNonEmpty(env, "AUTH_SESSION_STORE_TYPE", auth.SessionStore.Type)
		if auth.SessionStore.TTLMs != nil {
			setEnv(env, "AUTH_SESSION_STORE_TTL_MS", strconv.Itoa(*auth.SessionStore.TTLMs))
		}
		setEnvIfNonEmpty(env, "AUTH_SESSION_SECRET", auth.SessionStore.Secret)
	}
	if auth.TokenTTL != nil {
		if auth.TokenTTL.AccessTokenTTLSeconds != nil {
			setEnv(env, "AUTH_ACCESS_TOKEN_TTL_SECONDS", strconv.Itoa(*auth.TokenTTL.AccessTokenTTLSeconds))
		}
		if auth.TokenTTL.RefreshTokenTTLSeconds != nil {
			setEnv(env, "AUTH_REFRESH_TOKEN_TTL_SECONDS", strconv.Itoa(*auth.TokenTTL.RefreshTokenTTLSeconds))
		}
	}
	if auth.OIDCTTL != nil {
		if auth.OIDCTTL.InteractionTTLSeconds != nil {
			setEnv(env, "AUTH_OIDC_INTERACTION_TTL_SECONDS", strconv.Itoa(*auth.OIDCTTL.InteractionTTLSeconds))
		}
		if auth.OIDCTTL.GrantTTLSeconds != nil {
			setEnv(env, "AUTH_OIDC_GRANT_TTL_SECONDS", strconv.Itoa(*auth.OIDCTTL.GrantTTLSeconds))
		}
		if auth.OIDCTTL.SessionTTLSeconds != nil {
			setEnv(env, "AUTH_OIDC_SESSION_TTL_SECONDS", strconv.Itoa(*auth.OIDCTTL.SessionTTLSeconds))
		}
		if auth.OIDCTTL.IDTokenTTLSeconds != nil {
			setEnv(env, "AUTH_OIDC_ID_TOKEN_TTL_SECONDS", strconv.Itoa(*auth.OIDCTTL.IDTokenTTLSeconds))
		}
	}
}

func projectDatabaseEnv(env map[string]string, db *struct {
	Provider     string `yaml:"provider,omitempty" json:"provider,omitempty"`
	User         string `yaml:"user,omitempty" json:"user,omitempty"`
	Host         string `yaml:"host,omitempty" json:"host,omitempty"`
	Port         *int   `yaml:"port,omitempty" json:"port,omitempty"`
	Password     string `yaml:"password,omitempty" json:"password,omitempty"`
	DatabaseName string `yaml:"databaseName,omitempty" json:"databaseName,omitempty"`
	SSL          *bool  `yaml:"ssl,omitempty" json:"ssl,omitempty"`
	CA           string `yaml:"ca,omitempty" json:"ca,omitempty"`
}) {
	if db == nil {
		return
	}
	setEnvIfNonEmpty(env, "DB_PROVIDER", db.Provider)
	setEnvIfNonEmpty(env, "DB_USERNAME", db.User)
	setEnvIfNonEmpty(env, "DB_HOST", db.Host)
	if db.Port != nil {
		setEnv(env, "DB_PORT", strconv.Itoa(*db.Port))
	}
	setEnvIfNonEmpty(env, "DB_PASSWORD", db.Password)
	setEnvIfNonEmpty(env, "DB_NAME", db.DatabaseName)
	if db.SSL != nil {
		setEnv(env, "DB_USE_SSL", strconv.FormatBool(*db.SSL))
	}
	setEnvIfNonEmpty(env, "DB_SSL_CA", db.CA)
}

func projectEventsEnv(env map[string]string, events *struct {
	AuditEnabled     *bool `yaml:"auditEnabled,omitempty" json:"auditEnabled,omitempty"`
	RetentionDays    *int  `yaml:"retentionDays,omitempty" json:"retentionDays,omitempty"`
	CleanupInterval  *int  `yaml:"cleanupInterval,omitempty" json:"cleanupInterval,omitempty"`
	CaptureIPAddress *bool `yaml:"captureIpAddress,omitempty" json:"captureIpAddress,omitempty"`
}) {
	if events == nil {
		return
	}
	if events.AuditEnabled != nil {
		setEnv(env, "EVENT_AUDIT_ENABLED", strconv.FormatBool(*events.AuditEnabled))
	}
	if events.RetentionDays != nil {
		setEnv(env, "EVENT_RETENTION_DAYS", strconv.Itoa(*events.RetentionDays))
	}
	if events.CleanupInterval != nil {
		setEnv(env, "EVENT_CLEANUP_INTERVAL", strconv.Itoa(*events.CleanupInterval))
	}
	if events.CaptureIPAddress != nil {
		setEnv(env, "EVENT_CAPTURE_IP_ADDRESS", strconv.FormatBool(*events.CaptureIPAddress))
	}
}

func projectSystemMicroserviceEnv(env map[string]string, sys *struct {
	Router map[string]string `yaml:"router,omitempty" json:"router,omitempty"`
	NATS   map[string]string `yaml:"nats,omitempty" json:"nats,omitempty"`
}) {
	if sys == nil {
		return
	}
	for _, slot := range controlPlaneRouterArchSlots {
		if sys.Router != nil {
			setEnvIfNonEmpty(env, fmt.Sprintf("ROUTER_IMAGE_%d", slot.slot), sys.Router[slot.arch])
		}
		if sys.NATS != nil {
			setEnvIfNonEmpty(env, fmt.Sprintf("NATS_IMAGE_%d", slot.slot), sys.NATS[slot.arch])
		}
	}
}

func projectNATSEnv(env map[string]string, nats *struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}) {
	if nats == nil || nats.Enabled == nil {
		return
	}
	setEnv(env, "NATS_ENABLED", strconv.FormatBool(*nats.Enabled))
}

func projectTLSEnv(env map[string]string, tls *models.ControlPlaneTLSConfig) {
	if tls == nil {
		return
	}
	if path := strings.TrimSpace(tls.Path); path != "" {
		setEnv(env, "SERVER_DEV_MODE", "false")
		setEnv(env, "TLS_PATH_CERT", models.ControlPlaneTLSCertFilename)
		setEnv(env, "TLS_PATH_KEY", models.ControlPlaneTLSKeyFilename)
		if _, err := models.StatControlPlaneTLSFile(path, models.ControlPlaneTLSCAFilename); err == nil {
			setEnv(env, "TLS_PATH_INTERMEDIATE_CERT", models.ControlPlaneTLSCAFilename)
			setEnv(env, "INTERMEDIATE_CERT", filepath.Join(ContainerCertMountPath, models.ControlPlaneTLSCAFilename))
		}
		return
	}
	if tls.Base64 == nil {
		return
	}
	setEnv(env, "SERVER_DEV_MODE", "false")
	setEnvIfNonEmpty(env, "TLS_BASE64_CERT", tls.Base64.Cert)
	setEnvIfNonEmpty(env, "TLS_BASE64_KEY", tls.Base64.Key)
	setEnvIfNonEmpty(env, "TLS_BASE64_INTERMEDIATE_CERT", tls.Base64.CA)
}

func projectVaultEnv(env map[string]string, vault *models.ControlPlaneVaultSpec) {
	if vault == nil {
		return
	}
	if vault.Enabled != nil {
		setEnv(env, "VAULT_ENABLED", strconv.FormatBool(*vault.Enabled))
	}
	setEnvIfNonEmpty(env, "VAULT_PROVIDER", vault.Provider)
	setEnvIfNonEmpty(env, "VAULT_BASE_PATH", vault.BasePath)
	if vault.Hashicorp != nil {
		setEnvIfNonEmpty(env, "VAULT_HASHICORP_ADDRESS", vault.Hashicorp.Address)
		setEnvIfNonEmpty(env, "VAULT_HASHICORP_TOKEN", vault.Hashicorp.Token)
		setEnvIfNonEmpty(env, "VAULT_HASHICORP_MOUNT", vault.Hashicorp.Mount)
	}
	if vault.AWS != nil {
		setEnvIfNonEmpty(env, "VAULT_AWS_REGION", vault.AWS.Region)
		setEnvIfNonEmpty(env, "VAULT_AWS_ACCESS_KEY_ID", vault.AWS.AccessKeyID)
		setEnvIfNonEmpty(env, "VAULT_AWS_ACCESS_KEY", vault.AWS.AccessKey)
	}
	if vault.Azure != nil {
		setEnvIfNonEmpty(env, "VAULT_AZURE_URL", vault.Azure.URL)
		setEnvIfNonEmpty(env, "VAULT_AZURE_TENANT_ID", vault.Azure.TenantID)
		setEnvIfNonEmpty(env, "VAULT_AZURE_CLIENT_ID", vault.Azure.ClientID)
		setEnvIfNonEmpty(env, "VAULT_AZURE_CLIENT_SECRET", vault.Azure.ClientSecret)
	}
	if vault.Google != nil {
		setEnvIfNonEmpty(env, "VAULT_GOOGLE_PROJECT_ID", vault.Google.ProjectID)
		setEnvIfNonEmpty(env, "VAULT_GOOGLE_CREDENTIALS", vault.Google.Credentials)
	}
}

func setEnv(env map[string]string, key, value string) {
	env[key] = value
}

func setEnvIfNonEmpty(env map[string]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	setEnv(env, key, strings.TrimSpace(value))
}
