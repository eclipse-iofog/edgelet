package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/golang-jwt/jwt/v5"
)

const (
	moduleName                      = "JWT Manager"
	jwtExpiration                   = 10 * time.Minute
	jwtIssuer                       = "https://edgelet.default.svc.bridge.local"
	jwtAudience                     = "https://edgelet.default.svc.bridge.local"
	edgeletAPIAudience              = "edgelet://edgeletapi/v1"
	serviceAccountAudience          = "https://edgelet.default.svc.bridge.local"
	edgeGuardAudience               = "edgelet://edgeguard/v1"
	tokenUseController              = "controller"
	tokenUseEdgeletAPI              = "edgeletapi"
	tokenUseServiceAccount          = "serviceaccount"
	tokenUseEdgeGuard               = "edgeguard"
	rotationLeadFractionDenominator = 5
	maxRotationLeadWindow           = 2 * time.Minute
)

// JWTManager handles JWT token generation and validation
type JWTManager struct {
	mu          sync.RWMutex
	privateKey  ed25519.PrivateKey
	initialized bool
}

var (
	instance *JWTManager
	once     sync.Once
)

// GetInstance returns the singleton JWT manager instance
func GetJWTManager() *JWTManager {
	once.Do(func() {
		instance = &JWTManager{}
	})
	return instance
}

// Reset resets the JWT Manager state to allow re-initialization with new credentials
func (j *JWTManager) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()

	logging.LogDebug(moduleName, "Resetting JWT Manager static state")
	j.privateKey = nil
	j.initialized = false
	logging.LogDebug(moduleName, "JWT Manager static state reset completed")
}

// loadPrivateKeyFromConfig loads and initializes the private key from config
func (j *JWTManager) loadPrivateKeyFromConfig() error {
	cfg := config.GetInstance()
	base64Key := cfg.PrivateKey

	if base64Key == "" {
		return errors.New("private key is not configured")
	}

	// Decode base64-encoded JWK
	keyBytes, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return fmt.Errorf("failed to decode base64 key: %w", err)
	}

	// Parse JWK JSON
	var jwk JWK
	if err := json.Unmarshal(keyBytes, &jwk); err != nil {
		return fmt.Errorf("failed to parse JWK JSON: %w", err)
	}

	// Validate key type and curve
	if jwk.Kty != "OKP" {
		return errors.New("key must be OKP type")
	}
	if jwk.Crv != "Ed25519" {
		return errors.New("key must use Ed25519 curve")
	}

	// Decode the private key (d parameter in JWK)
	privateKeyBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	// Ed25519 private key is 32 bytes
	if len(privateKeyBytes) != ed25519.SeedSize {
		return fmt.Errorf("invalid private key length: expected %d, got %d", ed25519.SeedSize, len(privateKeyBytes))
	}

	// Create Ed25519 private key from seed
	privateKey := ed25519.NewKeyFromSeed(privateKeyBytes)

	j.privateKey = privateKey
	j.initialized = true

	logging.LogDebug(moduleName, "Successfully initialized Ed25519 signer")
	return nil
}

func (j *JWTManager) ensureProvisionedSignerLocked() (*config.Config, error) {
	// Fail-closed (scoped): auth paths depend on SQLite-backed private key durability.
	if store.GetInstance().Conn() == nil {
		return nil, errors.New("sqlite unavailable; auth path blocked")
	}
	cfg := config.GetInstance()
	if cfg.IOFogUUID != "" && strings.TrimSpace(cfg.PrivateKey) == "" {
		if _, err := hydrateProvisionedPrivateKeyFromDB(); err != nil {
			return nil, fmt.Errorf("failed to hydrate private key from sqlite: %w", err)
		}
		cfg = config.GetInstance()
	}
	if cfg.IOFogUUID == "" || strings.TrimSpace(cfg.PrivateKey) == "" {
		return nil, errors.New("agent is not provisioned")
	}
	if !j.initialized {
		if err := j.loadPrivateKeyFromConfig(); err != nil {
			if cfg.IOFogUUID != "" {
				logging.LogError(moduleName, fmt.Sprintf("Failed to initialize signer: %v", err), err)
			}
			return nil, err
		}
	}
	return cfg, nil
}

func (j *JWTManager) signPurposeJWTLocked(subject, audience string, ttl time.Duration, tokenUse string, extraClaims map[string]any) (string, string, int64, int64, error) {
	if strings.TrimSpace(subject) == "" {
		return "", "", 0, 0, errors.New("subject is required")
	}
	if strings.TrimSpace(audience) == "" {
		return "", "", 0, 0, errors.New("audience is required")
	}
	if ttl <= 0 {
		ttl = jwtExpiration
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":      subject,
		"iss":      jwtIssuer,
		"aud":      []string{audience},
		"exp":      now.Add(ttl).Unix(),
		"iat":      now.Unix(),
		"nbf":      now.Unix(),
		"tokenUse": tokenUse,
	}
	for k, v := range extraClaims {
		claims[k] = v
	}
	jti, err := generateJWTID(claims)
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("failed to generate token jti: %w", err)
	}
	claims["jti"] = jti
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenString, err := token.SignedString(j.privateKey)
	if err != nil {
		return "", "", 0, 0, err
	}
	iatSec, ok := claims["iat"].(int64)
	if !ok {
		return "", "", 0, 0, errors.New("missing iat claim")
	}
	expSec, ok := claims["exp"].(int64)
	if !ok {
		return "", "", 0, 0, errors.New("missing exp claim")
	}
	return tokenString, jti, iatSec, expSec, nil
}

// GenerateJWT generates a controller-auth JWT token with the configured private key.
// Kept as compatibility wrapper for existing controller paths.
func (j *JWTManager) GenerateJWT() (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cfg, err := j.ensureProvisionedSignerLocked()
	if err != nil {
		return "", err
	}
	aud := strings.TrimSpace(cfg.ControllerURL)
	if aud == "" {
		aud = jwtAudience
	}
	token, _, _, _, err := j.signPurposeJWTLocked(cfg.IOFogUUID, aud, jwtExpiration, tokenUseController, nil)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GenerateControllerJWT creates a signed JWT for controller communication.
func (j *JWTManager) GenerateControllerJWT(controllerAudience string, ttl time.Duration) (string, string, int64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cfg, err := j.ensureProvisionedSignerLocked()
	if err != nil {
		return "", "", 0, 0, err
	}
	aud := strings.TrimSpace(controllerAudience)
	if aud == "" {
		aud = cfg.ControllerURL
	}
	if strings.TrimSpace(aud) == "" {
		aud = jwtAudience
	}
	return j.signPurposeJWTLocked(cfg.IOFogUUID, aud, ttl, tokenUseController, nil)
}

// GenerateEdgeletAPITokenJWT creates a provisioned local admin token.
func (j *JWTManager) GenerateEdgeletAPITokenJWT(subject string, ttl time.Duration, extraClaims map[string]any) (string, string, int64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cfg, err := j.ensureProvisionedSignerLocked()
	if err != nil {
		return "", "", 0, 0, err
	}
	sub := strings.TrimSpace(subject)
	if sub == "" {
		sub = "system:edgeletadmin:" + cfg.IOFogUUID
	}
	return j.signPurposeJWTLocked(sub, edgeletAPIAudience, ttl, tokenUseEdgeletAPI, extraClaims)
}

// ValidateJWT validates a JWT token using the public key derived from the private key
func (j *JWTManager) ValidateJWT(tokenString string) (*jwt.Token, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Lazy initialize for validation paths that did not call GenerateJWT first.
	if !j.initialized {
		if err := j.loadPrivateKeyFromConfig(); err != nil {
			return nil, fmt.Errorf("JWT manager not initialized: %w", err)
		}
	}

	// Get public key from private key
	publicKey, ok := j.privateKey.Public().(ed25519.PublicKey)
	if !ok {
		publicKey = ed25519.PublicKey{}
	}

	// Parse and validate token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return publicKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT: %w", err)
	}

	if !token.Valid {
		return nil, errors.New("invalid JWT token")
	}

	return token, nil
}

// JWK represents a JSON Web Key structure
type JWK struct {
	Kty string `json:"kty"` // Key type (must be "OKP")
	Crv string `json:"crv"` // Curve (must be "Ed25519")
	Kid string `json:"kid"` // Key ID (optional)
	D   string `json:"d"`   // Private key (base64url encoded)
	X   string `json:"x"`   // Public key (base64url encoded, optional for private key)
}

// generateJWTID returns a deterministic hash string derived from claims + nonce.
func generateJWTID(claims jwt.MapClaims) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := map[string]any{
		"claims": claims,
		"nonce":  hex.EncodeToString(nonce),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]), nil
}

// GenerateServiceAccountJWT creates a signed JWT for a managed microservice service-account identity.
func (j *JWTManager) GenerateServiceAccountJWT(subject string, ttl time.Duration, extraClaims map[string]any) (string, string, int64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.ensureProvisionedSignerLocked(); err != nil {
		return "", "", 0, 0, err
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return j.signPurposeJWTLocked(subject, serviceAccountAudience, ttl, tokenUseServiceAccount, extraClaims)
}

// GenerateEdgeGuardJWT creates a dedicated JWT for edgeguard attestation payloads.
func (j *JWTManager) GenerateEdgeGuardJWT(hash string, ttl time.Duration) (string, string, int64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cfg, err := j.ensureProvisionedSignerLocked()
	if err != nil {
		return "", "", 0, 0, err
	}
	extra := map[string]any{"hash": hash}
	return j.signPurposeJWTLocked("system:edgeguard:"+cfg.IOFogUUID, edgeGuardAudience, ttl, tokenUseEdgeGuard, extra)
}

// EdgeGuardHashFromJWT returns the hardware fingerprint hash claim from a stored Edge Guard JWT.
// Comparison must use this stable hash, not the full JWT string (iat/exp/jti change every signing).
func EdgeGuardHashFromJWT(tokenString string) (string, error) {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return "", errors.New("edgeguard jwt is empty")
	}
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("parse edgeguard jwt: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("edgeguard jwt claims are invalid")
	}
	raw, ok := claims["hash"]
	if !ok {
		return "", errors.New("edgeguard jwt missing hash claim")
	}
	hash, ok := raw.(string)
	if !ok || strings.TrimSpace(hash) == "" {
		return "", errors.New("edgeguard jwt hash claim is invalid")
	}
	return hash, nil
}

// TokenSHA256 computes the stable hash used for persisted token metadata.
func TokenSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

// ShouldRotateByLifetime applies token rotation trigger policy:
// rotate when remaining lifetime is <=20% of TTL, capped by a max lead window.
func ShouldRotateByLifetime(iat, exp int64, now time.Time) bool {
	if exp <= 0 {
		return true
	}
	remaining := time.Unix(exp, 0).Sub(now)
	if remaining <= 0 {
		return true
	}

	ttlSeconds := exp - iat
	if ttlSeconds <= 0 {
		return remaining <= maxRotationLeadWindow
	}
	leadWindow := time.Duration(ttlSeconds/rotationLeadFractionDenominator) * time.Second
	if leadWindow > maxRotationLeadWindow {
		leadWindow = maxRotationLeadWindow
	}
	if leadWindow <= 0 {
		leadWindow = time.Second
	}
	return remaining <= leadWindow
}

// GetProvisionedPublicKey returns the base64url public key from provisioned JWK.
func GetProvisionedPublicKey() string {
	jwk, err := provisionedSigningJWK()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(jwk.X)
}

// ProvisionedPublicJWKJSON returns the compact public-only signing JWK for
// projected workload verification (no private key material).
func ProvisionedPublicJWKJSON() ([]byte, error) {
	jwk, err := provisionedSigningJWK()
	if err != nil {
		return nil, err
	}
	x := strings.TrimSpace(jwk.X)
	if x == "" {
		return nil, errors.New("provisioned public key is missing")
	}
	kty := strings.TrimSpace(jwk.Kty)
	if kty == "" {
		kty = "OKP"
	}
	crv := strings.TrimSpace(jwk.Crv)
	if crv == "" {
		crv = "Ed25519"
	}
	if kty != "OKP" || crv != "Ed25519" {
		return nil, fmt.Errorf("unsupported signing JWK: kty=%s crv=%s", kty, crv)
	}
	return json.Marshal(struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		Alg string `json:"alg"`
		X   string `json:"x"`
	}{
		Kty: kty,
		Crv: crv,
		Alg: "EdDSA",
		X:   x,
	})
}

func provisionedSigningJWK() (JWK, error) {
	cfg := config.GetInstance()
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		if _, err := hydrateProvisionedPrivateKeyFromDB(); err != nil {
			return JWK{}, err
		}
		cfg = config.GetInstance()
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return JWK{}, errors.New("agent is not provisioned")
	}
	keyBytes, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	if err != nil {
		return JWK{}, fmt.Errorf("failed to decode provisioned JWK: %w", err)
	}
	var jwk JWK
	if err := json.Unmarshal(keyBytes, &jwk); err != nil {
		return JWK{}, fmt.Errorf("failed to parse provisioned JWK: %w", err)
	}
	return jwk, nil
}
