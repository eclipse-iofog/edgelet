package serviceaccount

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

var (
	managerInstance *Manager
	managerOnce     sync.Once
)

const (
	// MountPath is the in-container projection path for serviceaccount material.
	MountPath                      = "/var/run/secrets/edgelet.iofog.org/serviceaccount"
	serviceAccountTokenTTL         = time.Hour
	serviceAccountTypePrefix       = "edgelet.iofog.org~serviceaccount"
	defaultServiceAccountVolumeKey = "default"
	bindMountDirMode               = 0755
	bindMountFileMode              = 0644
	projectionTokenFilename        = "token"
	projectionCAFilename           = "ca.crt"
	projectionSigningJWKFilename   = "edgelet.jwk"
)

var projectionFilenames = []string{
	projectionTokenFilename,
	projectionCAFilename,
	projectionSigningJWKFilename,
}

// Manager manages host-side projected serviceaccount artifacts.
type Manager struct {
	mu          sync.Mutex
	stagingRoot string
}

// NewManager creates a manager using current runtime paths.
func NewManager() *Manager {
	cfg := config.GetInstance()
	// Keep serviceaccount material outside per-microservice volume-mount cleanup tree.
	staging := filepath.Join(cfg.DiskDirectory, "volumes", "serviceaccounts")
	return &Manager{
		stagingRoot: staging,
	}
}

// GetInstance returns singleton service-account manager.
func GetInstance() *Manager {
	managerOnce.Do(func() {
		managerInstance = NewManager()
	})
	return managerInstance
}

// ProjectionDir returns host-side projection path for the microservice token bundle.
func (m *Manager) ProjectionDir(microserviceUUID string) string {
	return filepath.Join(m.stagingRoot, microserviceUUID, serviceAccountTypePrefix, defaultServiceAccountVolumeKey)
}

// WriteProjection writes token, CA, and public signing JWK materials for a microservice.
func (m *Manager) WriteProjection(microserviceUUID, token string, caPEM, jwkJSON []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if microserviceUUID == "" {
		return errors.New("microservice UUID is required")
	}
	if token == "" {
		return errors.New("serviceaccount token is required")
	}
	if len(jwkJSON) == 0 {
		return errors.New("edgelet signing JWK is required")
	}

	return m.writeProjectionAtomic(m.ProjectionDir(microserviceUUID), token, caPEM, jwkJSON)
}

// ReconcileManagedMicroservices mints and projects service-account material for managed microservices.
func (m *Manager) ReconcileManagedMicroservices(microservices []*models.Microservice) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(m.stagingRoot, bindMountDirMode); err != nil {
		return fmt.Errorf("failed to create staging root: %w", err)
	}

	cfg := config.GetInstance()
	activeTokensByMicroservice, err := m.activeTokenByMicroservice()
	if err != nil {
		return err
	}

	active := make(map[string]struct{}, len(microservices))
	for _, ms := range microservices {
		if ms == nil || strings.TrimSpace(ms.MicroserviceUUID) == "" {
			continue
		}
		active[ms.MicroserviceUUID] = struct{}{}

		previous := activeTokensByMicroservice[ms.MicroserviceUUID]
		if err := m.rotateForMicroservice(cfg, ms, previous); err != nil {
			return err
		}
	}

	for uuid := range activeTokensByMicroservice {
		if _, exists := active[uuid]; !exists {
			_ = os.RemoveAll(filepath.Join(m.stagingRoot, uuid))
		}
	}

	return nil
}

// RotateExpiringManagedTokens rotates service-account tokens for active managed
// microservices when rotation policy indicates expiry is close.
func (m *Manager) RotateExpiringManagedTokens(microservices []*models.Microservice, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := config.GetInstance()
	activeTokensByMicroservice, err := m.activeTokenByMicroservice()
	if err != nil {
		return err
	}

	for _, ms := range microservices {
		if ms == nil || strings.TrimSpace(ms.MicroserviceUUID) == "" {
			continue
		}
		current := activeTokensByMicroservice[ms.MicroserviceUUID]
		// Self-heal: if projection files are missing, force immediate remint/reproject.
		needsProjectionHeal := !m.projectionIsReady(ms.MicroserviceUUID)
		if current == nil || needsProjectionHeal || auth.ShouldRotateByLifetime(current.IssuedAt, current.ExpiresAt, now) {
			if err := m.rotateForMicroservice(cfg, ms, current); err != nil {
				return err
			}
		}
	}
	return nil
}

// Clear removes projected service-account materials and metadata.
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = os.RemoveAll(m.stagingRoot)
	_ = store.GetInstance().ClearServiceAccountTokens()
}

func ensureServiceAccountVolumeMapping(ms *models.Microservice, projectionDir string) {
	if ms.VolumeMappings == nil {
		ms.VolumeMappings = make([]*models.VolumeMapping, 0)
	}
	for _, vm := range ms.VolumeMappings {
		if vm == nil {
			continue
		}
		if vm.ContainerDestination == MountPath {
			vm.HostDestination = projectionDir
			vm.AccessMode = "ro"
			vm.Type = models.VolumeMappingTypeBind
			return
		}
	}
	ms.VolumeMappings = append(ms.VolumeMappings, models.NewVolumeMapping(projectionDir, MountPath, "ro", models.VolumeMappingTypeBind))
}

func (m *Manager) activeTokenByMicroservice() (map[string]*models.ServiceAccountToken, error) {
	items, err := store.GetInstance().ListActiveServiceAccountTokens()
	if err != nil {
		return nil, fmt.Errorf("failed to load active service-account tokens: %w", err)
	}
	result := make(map[string]*models.ServiceAccountToken)
	for _, item := range items {
		if item == nil || item.TokenUse != "serviceaccount" || strings.TrimSpace(item.MicroserviceUUID) == "" {
			continue
		}
		if _, exists := result[item.MicroserviceUUID]; !exists {
			result[item.MicroserviceUUID] = item
		}
	}
	return result, nil
}

func (m *Manager) rotateForMicroservice(cfg *config.Config, ms *models.Microservice, previous *models.ServiceAccountToken) error {
	app := strings.TrimSpace(ms.ApplicationName)
	if app == "" {
		app = "default"
	}
	name := strings.TrimSpace(ms.MicroserviceName)
	if name == "" {
		name = ms.MicroserviceUUID
	}
	subject := fmt.Sprintf("system:serviceaccount:%s:%s", app, name)
	rbac := normalizeServiceAccountRules(ms)
	extraClaims := map[string]any{
		"edgelet.iofog.org": map[string]any{
			"namespace": cfg.Namespace,
			"node": map[string]any{
				"uuid":      cfg.IOFogUUID,
				"publicKey": auth.GetProvisionedPublicKey(),
			},
			"microservice": map[string]any{
				"application": app,
				"name":        name,
				"uuid":        ms.MicroserviceUUID,
			},
			"rbac": map[string]any{
				"version":      rbac.Version,
				"rulesByGroup": rbac.RulesByGroup,
			},
		},
	}

	token, jti, iat, exp, err := auth.GetJWTManager().GenerateServiceAccountJWT(subject, serviceAccountTokenTTL, extraClaims)
	if err != nil {
		return fmt.Errorf("failed to mint service-account token for %s: %w", ms.MicroserviceUUID, err)
	}

	projectionDir := m.ProjectionDir(ms.MicroserviceUUID)
	caPEM, caErr := auth.ReadEdgeletAPICACertPEM()
	if caErr != nil {
		return fmt.Errorf("failed to read edgeletapi CA: %w", caErr)
	}
	jwkJSON, jwkErr := auth.ProvisionedPublicJWKJSON()
	if jwkErr != nil {
		return fmt.Errorf("failed to read edgelet signing JWK: %w", jwkErr)
	}
	if err := m.writeProjectionAtomic(projectionDir, token, caPEM, jwkJSON); err != nil {
		return err
	}
	ensureServiceAccountVolumeMapping(ms, projectionDir)

	rotatedFromJTI := ""
	if previous != nil {
		rotatedFromJTI = previous.JTI
	}
	claimsJSON, _ := json.Marshal(extraClaims)
	rulesByGroupJSON, _ := json.Marshal(rbac.RulesByGroup)
	serviceAccountName := ""
	roleRefKind := ""
	roleRefName := ""
	if ms.ServiceAccount != nil {
		serviceAccountName = ms.ServiceAccount.Name
		roleRefKind = ms.ServiceAccount.RoleRef.Kind
		roleRefName = ms.ServiceAccount.RoleRef.Name
	}
	if err := store.GetInstance().UpsertServiceAccountToken(&models.ServiceAccountToken{
		ID:                 ms.MicroserviceUUID + ":" + jti,
		TokenUse:           "serviceaccount",
		PrincipalType:      "serviceaccount",
		Subject:            subject,
		MicroserviceUUID:   ms.MicroserviceUUID,
		ApplicationName:    app,
		ServiceAccountName: serviceAccountName,
		RoleRefKind:        roleRefKind,
		RoleRefName:        roleRefName,
		RBACVersion:        rbac.Version,
		RulesByGroupJSON:   string(rulesByGroupJSON),
		ClaimsJSON:         string(claimsJSON),
		Issuer:             "https://edgelet.default.svc.bridge.local",
		Audience:           "https://edgelet.default.svc.bridge.local",
		Alg:                "EdDSA",
		JTI:                jti,
		TokenSHA256:        auth.TokenSHA256(token),
		IssuedAt:           iat,
		NotBefore:          iat,
		ExpiresAt:          exp,
		RotatedFromJTI:     rotatedFromJTI,
	}); err != nil {
		return fmt.Errorf("failed to persist service-account token metadata: %w", err)
	}
	if previous != nil && previous.JTI != "" {
		_ = store.GetInstance().RevokeServiceAccountToken(previous.JTI, time.Now().Unix())
	}
	return nil
}

func normalizeServiceAccountRules(ms *models.Microservice) models.RBACEnvelopeV1 {
	if ms == nil || ms.ServiceAccount == nil {
		return (&models.ServiceAccount{}).CanonicalRBACV1()
	}
	return ms.ServiceAccount.CanonicalRBACV1()
}

func (m *Manager) writeProjectionAtomic(projectionDir, token string, caPEM, jwkJSON []byte) error {
	if err := os.MkdirAll(projectionDir, bindMountDirMode); err != nil {
		return fmt.Errorf("failed to create projection dir: %w", err)
	}
	versionDir := fmt.Sprintf("..%d", time.Now().UnixNano())
	versionPath := filepath.Join(projectionDir, versionDir)
	if err := os.MkdirAll(versionPath, bindMountDirMode); err != nil {
		return fmt.Errorf("failed to create service-account version dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(versionPath, projectionTokenFilename), []byte(token), bindMountFileMode); err != nil {
		return fmt.Errorf("failed to write service-account token projection: %w", err)
	}
	if err := os.WriteFile(filepath.Join(versionPath, projectionCAFilename), caPEM, bindMountFileMode); err != nil {
		return fmt.Errorf("failed to write service-account ca projection: %w", err)
	}
	if err := os.WriteFile(filepath.Join(versionPath, projectionSigningJWKFilename), jwkJSON, bindMountFileMode); err != nil {
		return fmt.Errorf("failed to write service-account signing JWK projection: %w", err)
	}
	tmpData := filepath.Join(projectionDir, "..data.tmp")
	_ = os.Remove(tmpData)
	if err := os.Symlink(versionDir, tmpData); err != nil {
		return fmt.Errorf("failed to create temp data symlink: %w", err)
	}
	if err := os.Rename(tmpData, filepath.Join(projectionDir, "..data")); err != nil {
		return fmt.Errorf("failed to activate data symlink: %w", err)
	}
	for _, name := range projectionFilenames {
		linkPath := filepath.Join(projectionDir, name)
		_ = os.Remove(linkPath)
		if err := os.Symlink(filepath.Join("..data", name), linkPath); err != nil {
			return fmt.Errorf("failed to create service-account key symlink %s: %w", name, err)
		}
	}
	m.cleanupOldVersions(projectionDir)
	return nil
}

func (m *Manager) cleanupOldVersions(projectionDir string) {
	entries, err := os.ReadDir(projectionDir)
	if err != nil {
		return
	}
	var latest string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "..") && name != "..data" && name != "..data.tmp" {
			if name > latest {
				latest = name
			}
		}
	}
	if latest == "" {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "..") && name != "..data" && name != "..data.tmp" && name != latest {
			_ = os.RemoveAll(filepath.Join(projectionDir, name))
		}
	}
}

func (m *Manager) projectionIsReady(microserviceUUID string) bool {
	projectionDir := m.ProjectionDir(microserviceUUID)
	for _, name := range projectionFilenames {
		if _, err := os.Stat(filepath.Join(projectionDir, name)); err != nil {
			return false
		}
	}
	return true
}
