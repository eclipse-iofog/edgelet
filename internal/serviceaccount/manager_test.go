package serviceaccount

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/golang-jwt/jwt/v5"
)

func TestWriteProjection(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp
	utils.VarRun = filepath.Join(tmp, "run")

	m := NewManager()
	err := m.WriteProjection("ms-1", "jwt-token", []byte("ca-bytes"), []byte(`{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","x":"abc"}`))
	if err != nil {
		t.Fatalf("WriteProjection returned error: %v", err)
	}

	tokenBytes, err := os.ReadFile(filepath.Join(m.ProjectionDir("ms-1"), "token"))
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}
	if string(tokenBytes) != "jwt-token" {
		t.Fatalf("unexpected token file contents: %q", string(tokenBytes))
	}

	if _, err := os.Stat(filepath.Join(m.ProjectionDir("ms-1"), "ca.crt")); err != nil {
		t.Fatalf("expected ca.crt to be created: %v", err)
	}
	jwkBytes, err := os.ReadFile(filepath.Join(m.ProjectionDir("ms-1"), "edgelet.jwk"))
	if err != nil {
		t.Fatalf("expected edgelet.jwk to be created: %v", err)
	}
	if string(jwkBytes) != `{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","x":"abc"}` {
		t.Fatalf("unexpected edgelet.jwk contents: %q", string(jwkBytes))
	}
}

func TestProjectionDir_UsesDedicatedServiceaccountsRoot(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp

	m := NewManager()
	expected := filepath.Join(tmp, "volumes", "serviceaccounts", "ms-1", "edgelet.iofog.org~serviceaccount", "default")
	if got := m.ProjectionDir("ms-1"); got != expected {
		t.Fatalf("unexpected projection dir: got=%q want=%q", got, expected)
	}
}

func TestWriteProjectionValidation(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp

	m := NewManager()
	if err := m.WriteProjection("", "jwt-token", nil, []byte(`{"kty":"OKP"}`)); err == nil {
		t.Fatal("expected validation error for empty microservice UUID")
	}
	if err := m.WriteProjection("ms-1", "", nil, []byte(`{"kty":"OKP"}`)); err == nil {
		t.Fatal("expected validation error for empty token")
	}
	if err := m.WriteProjection("ms-1", "jwt-token", nil, nil); err == nil {
		t.Fatal("expected validation error for empty signing JWK")
	}
}

func TestRotateExpiringManagedTokens_RotatesAndLinksJTI(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp
	cfg.Namespace = "default"
	cfg.IOFogUUID = "agent-uuid"

	db := store.GetInstance()
	if err := db.Open(tmp); err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	key := createEd25519JWKBase64(t)
	if err := db.UpsertAgentPrivateKey(key); err != nil {
		t.Fatalf("failed to seed private key: %v", err)
	}
	cfg.PrivateKey = key
	auth.GetJWTManager().Reset()

	ms := models.NewMicroservice("ms-1", "alpine:latest")
	ms.ApplicationName = "app"
	ms.MicroserviceName = "svc"

	m := NewManager()
	if err := m.ReconcileManagedMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}
	before, err := db.ListActiveServiceAccountTokens()
	if err != nil || len(before) != 1 {
		t.Fatalf("expected one active token after reconcile, got len=%d err=%v", len(before), err)
	}

	// Force expiration to trigger rotation.
	expired := before[0]
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := db.UpsertServiceAccountToken(expired); err != nil {
		t.Fatalf("failed to force-expire token: %v", err)
	}

	if err := m.RotateExpiringManagedTokens([]*models.Microservice{ms}, time.Now()); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}

	after, err := db.ListServiceAccountTokens()
	if err != nil {
		t.Fatalf("failed to list tokens: %v", err)
	}
	var activeCount int
	var rotated *models.ServiceAccountToken
	for _, item := range after {
		if item.RevokedAt == nil {
			activeCount++
			rotated = item
		}
	}
	if activeCount != 1 || rotated == nil {
		t.Fatalf("expected exactly one active token after rotation, got %d", activeCount)
	}
	if rotated.RotatedFromJTI == "" {
		t.Fatal("expected rotated_from_jti to be set")
	}
}

func TestRotateExpiringManagedTokens_SelfHealsMissingProjection(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp
	cfg.Namespace = "default"
	cfg.IOFogUUID = "agent-uuid"

	db := store.GetInstance()
	if err := db.Open(tmp); err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key := createEd25519JWKBase64(t)
	if err := db.UpsertAgentPrivateKey(key); err != nil {
		t.Fatalf("failed to seed private key: %v", err)
	}
	cfg.PrivateKey = key
	auth.GetJWTManager().Reset()

	ms := models.NewMicroservice("ms-1", "alpine:latest")
	ms.ApplicationName = "app"
	ms.MicroserviceName = "svc"

	m := NewManager()
	if err := m.ReconcileManagedMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}

	// Simulate update/remove cleanup side effect that wipes projection files.
	if err := os.RemoveAll(m.ProjectionDir("ms-1")); err != nil {
		t.Fatalf("failed to remove projection dir: %v", err)
	}

	if err := m.RotateExpiringManagedTokens([]*models.Microservice{ms}, time.Now()); err != nil {
		t.Fatalf("rotation self-heal failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(m.ProjectionDir("ms-1"), "token")); err != nil {
		t.Fatalf("expected token projection to be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.ProjectionDir("ms-1"), "ca.crt")); err != nil {
		t.Fatalf("expected ca projection to be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.ProjectionDir("ms-1"), "edgelet.jwk")); err != nil {
		t.Fatalf("expected signing JWK projection to be restored: %v", err)
	}
}

func TestReconcileManagedMicroservices_EmitsCanonicalRBACEnvelope(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp
	cfg.Namespace = "default"
	cfg.IOFogUUID = "agent-uuid"

	db := store.GetInstance()
	if err := db.Open(tmp); err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key := createEd25519JWKBase64(t)
	if err := db.UpsertAgentPrivateKey(key); err != nil {
		t.Fatalf("failed to seed private key: %v", err)
	}
	cfg.PrivateKey = key
	auth.GetJWTManager().Reset()

	ms := models.NewMicroservice("ms-1", "alpine:latest")
	ms.ApplicationName = "app"
	ms.MicroserviceName = "svc"
	ms.ServiceAccount = &models.ServiceAccount{
		Name: "default",
		Rules: []models.ServiceAccountRule{
			{
				APIGroups: []string{"edgelet.iofog.org/v1"},
				Resources: []string{"config"},
				Verbs:     []string{"patch", "get"},
			},
			{
				APIGroups: []string{"kuksa.val/v2"},
				Resources: []string{"Vehicle.Speed"},
				Verbs:     []string{"patch"},
			},
		},
	}

	m := NewManager()
	if err := m.ReconcileManagedMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	tokenBytes, err := os.ReadFile(filepath.Join(m.ProjectionDir("ms-1"), "token"))
	if err != nil {
		t.Fatalf("failed to read projected token: %v", err)
	}
	token, _, err := jwt.NewParser().ParseUnverified(string(tokenBytes), jwt.MapClaims{})
	if err != nil {
		t.Fatalf("failed to parse projected token: %v", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("type assertion failed for claims")
	}
	iofog, ok := claims["edgelet.iofog.org"].(map[string]any)
	if !ok {
		t.Fatal("missing iofog.org claims")
	}
	rbac, ok := iofog["rbac"].(map[string]any)
	if !ok {
		t.Fatal("missing canonical rbac envelope")
	}
	if rbac["version"] != "v1" {
		t.Fatalf("expected rbac version v1, got %#v", rbac["version"])
	}
	rulesByGroup, ok := rbac["rulesByGroup"].(map[string]any)
	if !ok {
		t.Fatal("missing rulesByGroup")
	}
	if _, ok := rulesByGroup["kuksa.val/v2"]; !ok {
		t.Fatal("expected external group inside rulesByGroup")
	}
	if _, exists := claims["kuksa.val/v2"]; exists {
		t.Fatal("external groups must not be top-level claims in canonical payload")
	}

	assertProjectedSigningJWKVerifiesToken(t, m.ProjectionDir("ms-1"))
}

func TestReconcileManagedMicroservices_ProjectsPublicSigningJWK(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = tmp
	utils.SNAPCommon = tmp
	cfg.Namespace = "default"
	cfg.IOFogUUID = "agent-uuid"

	db := store.GetInstance()
	if err := db.Open(tmp); err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key := createEd25519JWKBase64(t)
	if err := db.UpsertAgentPrivateKey(key); err != nil {
		t.Fatalf("failed to seed private key: %v", err)
	}
	cfg.PrivateKey = key
	auth.GetJWTManager().Reset()

	ms := models.NewMicroservice("ms-1", "alpine:latest")
	ms.ApplicationName = "app"
	ms.MicroserviceName = "svc"

	m := NewManager()
	if err := m.ReconcileManagedMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	assertProjectedSigningJWKVerifiesToken(t, m.ProjectionDir("ms-1"))
}

func assertProjectedSigningJWKVerifiesToken(t *testing.T, projectionDir string) {
	t.Helper()
	jwkBytes, err := os.ReadFile(filepath.Join(projectionDir, "edgelet.jwk"))
	if err != nil {
		t.Fatalf("failed to read projected signing JWK: %v", err)
	}
	var jwk map[string]any
	if err := json.Unmarshal(jwkBytes, &jwk); err != nil {
		t.Fatalf("failed to parse projected signing JWK: %v", err)
	}
	if _, exists := jwk["d"]; exists {
		t.Fatal("projected signing JWK must not include private key material")
	}
	if jwk["kty"] != "OKP" || jwk["crv"] != "Ed25519" || jwk["alg"] != "EdDSA" {
		t.Fatalf("unexpected projected JWK fields: %#v", jwk)
	}
	x, ok := jwk["x"].(string)
	if !ok || strings.TrimSpace(x) == "" {
		t.Fatal("projected signing JWK missing x")
	}
	pub, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil {
		t.Fatalf("failed to decode projected JWK x: %v", err)
	}
	tokenBytes, err := os.ReadFile(filepath.Join(projectionDir, "token"))
	if err != nil {
		t.Fatalf("failed to read projected token: %v", err)
	}
	parsed, err := jwt.Parse(string(tokenBytes), func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return ed25519.PublicKey(pub), nil
	})
	if err != nil {
		t.Fatalf("projected token must verify with edgelet.jwk: %v", err)
	}
	if parsed == nil || !parsed.Valid {
		t.Fatal("projected token is not valid under edgelet.jwk")
	}
}

func createEd25519JWKBase64(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	seed := priv.Seed()
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(seed),
		"x":   base64.RawURLEncoding.EncodeToString(pub),
	}
	raw, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("failed to marshal jwk: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
