package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

func openTestSQLite(t *testing.T) {
	t.Helper()
	db := store.GetInstance()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open sqlite for test: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
}

func TestJWTManager_GenerateJWT(t *testing.T) {
	openTestSQLite(t)

	// Setup: Create a test Ed25519 key pair
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	// Create JWK format
	seed := privateKey.Seed()
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(seed),
		"x":   base64.RawURLEncoding.EncodeToString(publicKey),
	}

	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("Failed to marshal JWK: %v", err)
	}

	base64JWK := base64.StdEncoding.EncodeToString(jwkJSON)

	// Set up config
	cfg := config.GetInstance()
	cfg.PrivateKey = base64JWK
	cfg.IOFogUUID = "test-uuid-123"

	// Reset JWT manager
	manager := GetJWTManager()
	manager.Reset()

	// Generate JWT
	token, err := manager.GenerateJWT()
	if err != nil {
		t.Fatalf("Failed to generate JWT: %v", err)
	}

	if token == "" {
		t.Fatal("Generated token is empty")
	}

	// Validate the token
	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return publicKey, nil
	})

	if err != nil {
		t.Fatalf("Failed to parse JWT: %v", err)
	}

	if !parsedToken.Valid {
		t.Fatal("Parsed token is not valid")
	}

	// Check claims
	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("Failed to extract claims")
	}

	if claims["sub"] != "test-uuid-123" {
		t.Errorf("Expected subject 'test-uuid-123', got '%v'", claims["sub"])
	}

	if claims["iss"] != jwtIssuer {
		t.Errorf("Expected issuer '%s', got '%v'", jwtIssuer, claims["iss"])
	}
	if _, exists := claims["kid"]; exists {
		t.Fatal("kid must not be present in JWT payload claims")
	}
	if _, exists := parsedToken.Header["kid"]; exists {
		t.Fatal("kid must not be present in JWT header")
	}
	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		t.Fatalf("expected jti string claim, got %#v", claims["jti"])
	}
	if matched, _ := regexp.MatchString(`^[A-Z2-7]+$`, jti); !matched {
		t.Fatalf("expected base32 hash-like jti, got %q", jti)
	}

	// Check expiration
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatal("Expiration claim is not a number")
	}

	expTime := time.Unix(int64(exp), 0)
	if expTime.Before(time.Now()) {
		t.Error("Token is already expired")
	}

	if expTime.After(time.Now().Add(jwtExpiration + time.Minute)) {
		t.Error("Token expiration is too far in the future")
	}
}

func TestJWTManager_ValidateJWT(t *testing.T) {
	openTestSQLite(t)

	// Setup: Create a test Ed25519 key pair
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	// Create JWK format
	seed := privateKey.Seed()
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(seed),
		"x":   base64.RawURLEncoding.EncodeToString(publicKey),
	}

	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("Failed to marshal JWK: %v", err)
	}

	base64JWK := base64.StdEncoding.EncodeToString(jwkJSON)

	// Set up config
	cfg := config.GetInstance()
	cfg.PrivateKey = base64JWK
	cfg.IOFogUUID = "test-uuid-123"

	// Reset JWT manager
	manager := GetJWTManager()
	manager.Reset()

	// Generate JWT
	token, err := manager.GenerateJWT()
	if err != nil {
		t.Fatalf("Failed to generate JWT: %v", err)
	}

	// Validate JWT
	validatedToken, err := manager.ValidateJWT(token)
	if err != nil {
		t.Fatalf("Failed to validate JWT: %v", err)
	}

	if !validatedToken.Valid {
		t.Fatal("Validated token is not valid")
	}
}

func TestJWTManager_Reset(t *testing.T) {
	openTestSQLite(t)

	manager := GetJWTManager()
	manager.Reset()

	// After reset, GenerateJWT should reinitialize
	cfg := config.GetInstance()
	cfg.PrivateKey = "invalid"
	cfg.IOFogUUID = "test-uuid"

	_, err := manager.GenerateJWT()
	if err == nil {
		t.Error("Expected error with invalid key, got nil")
	}
}

func TestShouldRotateByLifetime(t *testing.T) {
	now := time.Now()
	iatSec := now.Unix()
	expSec := now.Add(10 * time.Minute).Unix()

	if ShouldRotateByLifetime(iatSec, expSec, now.Add(7*time.Minute)) {
		t.Fatal("token should not rotate yet when outside lead window")
	}
	if !ShouldRotateByLifetime(iatSec, expSec, now.Add(8*time.Minute)) {
		t.Fatal("token should rotate at <=20% remaining lifetime")
	}
	if !ShouldRotateByLifetime(iatSec, expSec, now.Add(10*time.Minute+time.Second)) {
		t.Fatal("expired token must rotate")
	}
}

func TestProvisionedPublicJWKJSON(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	x := base64.RawURLEncoding.EncodeToString(publicKey)
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(privateKey.Seed()),
		"x":   x,
	}
	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("failed to marshal jwk: %v", err)
	}
	cfg := config.GetInstance()
	cfg.PrivateKey = base64.StdEncoding.EncodeToString(jwkJSON)
	cfg.IOFogUUID = "jwk-test-uuid"

	raw, err := ProvisionedPublicJWKJSON()
	if err != nil {
		t.Fatalf("ProvisionedPublicJWKJSON: %v", err)
	}
	var pub map[string]any
	if err := json.Unmarshal(raw, &pub); err != nil {
		t.Fatalf("parse public JWK: %v", err)
	}
	if _, exists := pub["d"]; exists {
		t.Fatal("public JWK must not include d")
	}
	if pub["kty"] != "OKP" || pub["crv"] != "Ed25519" || pub["alg"] != "EdDSA" || pub["x"] != x {
		t.Fatalf("unexpected public JWK: %#v", pub)
	}
	if GetProvisionedPublicKey() != x {
		t.Fatalf("GetProvisionedPublicKey mismatch: got %q want %q", GetProvisionedPublicKey(), x)
	}
}

func TestEdgeGuardHashFromJWT_StableAcrossRegeneration(t *testing.T) {
	openTestSQLite(t)

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	seed := privateKey.Seed()
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(seed),
		"x":   base64.RawURLEncoding.EncodeToString(publicKey),
	}
	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("marshal jwk: %v", err)
	}

	cfg := config.GetInstance()
	cfg.PrivateKey = base64.StdEncoding.EncodeToString(jwkJSON)
	cfg.IOFogUUID = "edgeguard-test-uuid"

	manager := GetJWTManager()
	manager.Reset()

	wantHash := "dGVzdC1oYXNoLXN0YWJsZQ=="
	token1, _, _, _, err := manager.GenerateEdgeGuardJWT(wantHash, 10*time.Minute)
	if err != nil {
		t.Fatalf("generate edgeguard jwt 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	token2, _, _, _, err := manager.GenerateEdgeGuardJWT(wantHash, 10*time.Minute)
	if err != nil {
		t.Fatalf("generate edgeguard jwt 2: %v", err)
	}
	if token1 == token2 {
		t.Fatal("expected different JWT strings for successive signings")
	}

	got1, err := EdgeGuardHashFromJWT(token1)
	if err != nil {
		t.Fatalf("extract hash from token1: %v", err)
	}
	got2, err := EdgeGuardHashFromJWT(token2)
	if err != nil {
		t.Fatalf("extract hash from token2: %v", err)
	}
	if got1 != wantHash || got2 != wantHash {
		t.Fatalf("hash claims: got1=%q got2=%q want=%q", got1, got2, wantHash)
	}
	if got1 != got2 {
		t.Fatalf("expected stable hash across JWTs, got1=%q got2=%q", got1, got2)
	}
}
