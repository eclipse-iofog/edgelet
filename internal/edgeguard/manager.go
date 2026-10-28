package edgeguard

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/fieldagent"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/shirou/gopsutil/v4/cpu"
)

const (
	moduleName = "Edge Guard Manager"
)

// JWK represents a JSON Web Key structure for Ed25519
type JWK struct {
	Kty string `json:"kty"` // Key type (must be "OKP")
	Crv string `json:"crv"` // Curve (must be "Ed25519")
	D   string `json:"d"`   // Private key (base64url encoded)
	X   string `json:"x"`   // Public key (base64url encoded)
	Kid string `json:"kid"` // Key ID (optional)
}

// Manager manages edge device security and attestation
type Manager struct {
	config            *config.Config
	ctx               context.Context
	cancel            context.CancelFunc
	attestationTicker *time.Ticker
	// attestCtx/attestCancel govern only the attestation worker goroutine so it
	// can be canceled independently when InstanceConfigUpdated reschedules it,
	// preventing leaked goroutines from old frequencies.
	attestCtx         context.Context
	attestCancel      context.CancelFunc
	privateKey        ed25519.PrivateKey
	privateKeySource  string
	signatureCache    string
	payloadCache      FingerprintPayload
	payloadCacheValid bool
	observedLinks     map[string]string
	previousFrequency int64
	mu                sync.Mutex
}

var (
	instance *Manager
	once     sync.Once
)

// GetInstance returns the singleton Edge Guard Manager instance
func GetInstance() *Manager {
	once.Do(func() {
		instance = &Manager{
			config: config.GetInstance(),
		}
		instance.ctx, instance.cancel = context.WithCancel(context.Background())
		instance.attestCtx, instance.attestCancel = context.WithCancel(context.Background())
	})
	return instance
}

// Start starts the Edge Guard Manager
func (m *Manager) Start() error {
	logging.LogInfo(moduleName, "Starting Edge Guard Manager")

	attestationFreq := m.config.EdgeGuardFrequency
	if m.config.IOFogUUID == "" || m.config.PrivateKey == "" {
		if attestationFreq > 0 {
			logging.LogWarn(moduleName, "Edge Guard cannot be enabled while agent is not provisioned; forcing edgeGuardFrequency=0")
			m.config.EdgeGuardFrequency = 0
			attestationFreq = 0
		}
	}

	if attestationFreq <= 0 {
		if err := m.deleteStoredSignature(); err != nil {
			logging.LogError(moduleName, "Failed to delete Edge Guard signature while disabled", err)
		}
		logging.LogInfo(moduleName, "Edge Guard Manager disabled (frequency = 0)")
		return nil
	}

	if err := m.loadSignatureCacheFromDB(); err != nil {
		return fmt.Errorf("failed to load edgeguard signature from db: %w", err)
	}

	if err := m.checkHardwareSignature(); err != nil {
		return fmt.Errorf("failed initial edgeguard signature check: %w", err)
	}

	// Ensure a fresh attestation context for Start (supports supervisor restart cycles).
	if m.attestCancel != nil {
		m.attestCancel()
	}
	m.attestCtx, m.attestCancel = context.WithCancel(context.Background())

	// Start periodic attestation.
	duration := time.Duration(attestationFreq) * time.Second
	ticker := time.NewTicker(duration)
	m.attestationTicker = ticker
	go m.attestationWorker(m.attestCtx, ticker)
	m.previousFrequency = attestationFreq
	logging.LogInfo(moduleName, fmt.Sprintf("Edge Guard Manager started with attestation frequency: %d seconds", attestationFreq))

	return nil
}

// Stop stops the Edge Guard Manager
func (m *Manager) Stop() error {
	logging.LogInfo(moduleName, "Stopping Edge Guard Manager")
	if m.cancel != nil {
		m.cancel()
	}
	if m.attestCancel != nil {
		m.attestCancel()
	}
	if m.attestationTicker != nil {
		m.attestationTicker.Stop()
		m.attestationTicker = nil
	}
	return nil
}

// attestationWorker performs periodic attestation
func (m *Manager) attestationWorker(ctx context.Context, ticker *time.Ticker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.performAttestation()
		}
	}
}

// checkHardwareSignature checks hardware signature
func (m *Manager) checkHardwareSignature() error {
	logging.LogDebug(moduleName, "Started checking hardware signature")

	if m.config.IOFogUUID == "" || m.config.PrivateKey == "" {
		logging.LogDebug(moduleName, "Skipping hardware signature check - agent not provisioned")
		return nil
	}

	payload := m.collectStableFingerprintPayload()
	fullCanonical, err := canonicalizeFingerprintPayload(payload)
	if err != nil {
		return fmt.Errorf("failed to canonicalize fingerprint payload: %w", err)
	}
	logging.LogDebug(moduleName, fmt.Sprintf("Collected canonical fingerprint payload length: %d", len(fullCanonical)))
	logging.LogDebug(moduleName, fmt.Sprintf("Canonical fingerprint payload: %s", fullCanonical))

	identityCanonical, err := canonicalizeIdentityFingerprint(payload)
	if err != nil {
		return fmt.Errorf("failed to canonicalize identity fingerprint: %w", err)
	}
	newHash := m.hashData(identityCanonical)
	liveLinks := linkLockFromPayload(payload)

	storedSignature := m.getCachedSignature()
	if storedSignature == "" {
		logging.LogInfo(moduleName, "Storing initial hardware signature in SQLite")
		if err := m.persistBaseline(newHash, liveLinks, payload); err != nil {
			return fmt.Errorf("failed to save initial hardware signature: %w", err)
		}
		return nil
	}

	storedHash, err := auth.EdgeGuardHashFromJWT(storedSignature)
	if err != nil || storedHash == "" {
		logging.LogWarn(moduleName, fmt.Sprintf("Invalid stored Edge Guard JWT; re-baselining: %v", err))
		if err := m.persistBaseline(newHash, liveLinks, payload); err != nil {
			return fmt.Errorf("failed to re-baseline hardware signature: %w", err)
		}
		return nil
	}

	storedLinks, linksPresent, err := auth.EdgeGuardLinksFromJWT(storedSignature)
	if err != nil || !linksPresent {
		if err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Invalid Edge Guard link lock; re-baselining without deprovision: %v", err))
		} else {
			logging.LogWarn(moduleName, "Stored Edge Guard JWT has no link lock; re-baselining without deprovision")
		}
		if err := m.persistBaseline(newHash, liveLinks, payload); err != nil {
			return fmt.Errorf("failed to re-baseline hardware signature: %w", err)
		}
		return nil
	}

	action, lockLinks := evaluateSignature(true, storedHash, newHash, storedLinks, liveLinks)
	switch action {
	case signatureDeprovision:
		logging.LogWarn(moduleName, "Hardware signature mismatch detected")
		logging.LogDebug(moduleName, fmt.Sprintf("Stored hash (prefix): %s...", storedHash[:minInt(16, len(storedHash))]))
		logging.LogDebug(moduleName, fmt.Sprintf("New hash (prefix): %s...", newHash[:minInt(16, len(newHash))]))
		m.logPayloadDiff(payload)
		m.handleHardwareChange()
		return nil
	case signatureRefreshLock:
		m.logDownwardLinks(storedLinks, liveLinks)
		signature, err := m.signWithPrivateKey(newHash, lockLinks)
		if err != nil {
			return fmt.Errorf("failed to refresh edgeguard attestation JWT: %w", err)
		}
		if signature != storedSignature {
			if err := m.saveSignatureToDB(signature); err != nil {
				return fmt.Errorf("failed to refresh edgeguard attestation JWT: %w", err)
			}
			logging.LogDebug(moduleName, "Refreshed Edge Guard attestation JWT (hardware unchanged)")
		}
		m.setPayloadCache(payload)
		return nil
	default:
		return fmt.Errorf("unexpected edgeguard signature action %d", action)
	}
}

// min returns the minimum of two integers
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) getCachedSignature() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.signatureCache
}

func (m *Manager) setCachedSignature(signature string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signatureCache = signature
}

func (m *Manager) loadSignatureCacheFromDB() error {
	db := store.GetInstance()
	signature, found, err := db.GetEdgeGuardSignature()
	if err != nil {
		return err
	}
	if !found {
		m.setCachedSignature("")
		return nil
	}

	m.setCachedSignature(signature)
	return nil
}

func (m *Manager) saveSignatureToDB(signature string) error {
	if err := store.GetInstance().UpsertEdgeGuardSignature(signature); err != nil {
		return err
	}
	m.setCachedSignature(signature)
	return nil
}

func (m *Manager) deleteStoredSignature() error {
	if err := store.GetInstance().DeleteEdgeGuardSignature(); err != nil {
		return err
	}
	m.setCachedSignature("")
	m.clearPayloadCache()
	return nil
}

func (m *Manager) setPayloadCache(payload FingerprintPayload) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.payloadCache = payload
	m.payloadCacheValid = true
}

func (m *Manager) getPayloadCache() (FingerprintPayload, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.payloadCache, m.payloadCacheValid
}

func (m *Manager) clearPayloadCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.payloadCache = FingerprintPayload{}
	m.payloadCacheValid = false
	m.observedLinks = nil
}

func (m *Manager) logPayloadDiff(current FingerprintPayload) {
	previous, ok := m.getPayloadCache()
	if !ok {
		return
	}
	logFingerprintDiff(previous, current)
}

func (m *Manager) persistBaseline(hash string, links []auth.EdgeGuardLink, payload FingerprintPayload) error {
	signature, err := m.signWithPrivateKey(hash, links)
	if err != nil {
		return err
	}
	if err := m.saveSignatureToDB(signature); err != nil {
		return err
	}
	m.setPayloadCache(payload)
	m.clearLinkObservations()
	return nil
}

func (m *Manager) logDownwardLinks(locked, live []auth.EdgeGuardLink) {
	m.mu.Lock()
	notices, next := applyDownwardObservations(m.observedLinks, locked, live)
	m.observedLinks = next
	m.mu.Unlock()
	for _, notice := range notices {
		logging.LogWarn(moduleName, notice.message)
	}
}

func (m *Manager) clearLinkObservations() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observedLinks = nil
}

// handleHardwareChange handles hardware change detection
func (m *Manager) handleHardwareChange() {
	logging.LogWarn(moduleName, "Hardware change detected - deprovisioning agent")

	// Wrap in error handling
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Error handling hardware change", fmt.Errorf("%v", r))
		}
	}()

	// 1. Update daemon status to WARNING with message "HW signature changed"
	statusreporter.GetInstance().UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetDaemonStatus(models.ModuleStatusWarning)
		status.SetWarningMessage("HW signature changed")
	})

	// 2. Immediately send status to controller
	fieldagent.GetInstance().PostStatusHelper()

	// 3. Deprovision the agent (false = don't clear credentials)
	if err := fieldagent.GetInstance().Deprovision(false); err != nil {
		logging.LogError(moduleName, "Failed to deprovision agent", err)
	}

	// 4. Delete stored baseline signature.
	if err := m.deleteStoredSignature(); err != nil {
		logging.LogError(moduleName, "Failed to delete hardware signature from DB", err)
	}
}

func (m *Manager) collectStableFingerprintPayload() FingerprintPayload {
	payload := collectPlatformFingerprint(context.Background())
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		cpuData := cpuInfo[0]
		physicalCores, err := cpu.Counts(false)
		if err != nil || physicalCores <= 0 {
			physicalCores = int(cpuData.Cores)
		}
		payload.CPU = CPUIdentity{
			Vendor:        cpuData.VendorID,
			ModelName:     cpuData.ModelName,
			Family:        cpuData.Family,
			Model:         cpuData.Model,
			Stepping:      cpuData.Stepping,
			PhysicalCores: physicalCores,
		}
	}
	payload.GPUDevices = deriveGPUDevices(payload.PCIDevices)
	return payload
}

// hashData hashes the hardware data
func (m *Manager) hashData(data string) string {
	hash := sha256.Sum256([]byte(data))
	return base64.StdEncoding.EncodeToString(hash[:])
}

// signWithPrivateKey signs the identity hash and the locked NIC link states.
func (m *Manager) signWithPrivateKey(hash string, links []auth.EdgeGuardLink) (string, error) {
	if links == nil {
		links = []auth.EdgeGuardLink{}
	}
	tokenString, _, _, _, err := auth.GetJWTManager().GenerateEdgeGuardJWT(hash, links, 10*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to sign edgeguard token: %w", err)
	}
	return tokenString, nil
}

// initializePrivateKey initializes the Ed25519 private key from JWK
func (m *Manager) initializePrivateKey() error {
	privateKeyStr := m.config.PrivateKey
	if privateKeyStr == "" {
		return errors.New("private key is not configured")
	}

	// Decode base64-encoded JWK
	keyBytes, err := base64.StdEncoding.DecodeString(privateKeyStr)
	if err != nil {
		// Try as raw string
		keyBytes = []byte(privateKeyStr)
	}

	// Parse JWK JSON
	var jwk JWK
	if err := json.Unmarshal(keyBytes, &jwk); err != nil {
		return fmt.Errorf("failed to parse JWK JSON: %w", err)
	}

	// Validate key type
	if jwk.Kty != "OKP" {
		return errors.New("key must be OKP type")
	}

	// Validate curve
	if jwk.Crv != "Ed25519" {
		return errors.New("key must use Ed25519 curve")
	}

	// Decode the private key (d parameter in JWK)
	privateKeyBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	// Ed25519 private key seed is 32 bytes
	if len(privateKeyBytes) != ed25519.SeedSize {
		return fmt.Errorf("invalid private key length: expected %d, got %d", ed25519.SeedSize, len(privateKeyBytes))
	}

	// Create Ed25519 private key from seed
	m.privateKey = ed25519.NewKeyFromSeed(privateKeyBytes)
	m.privateKeySource = privateKeyStr

	logging.LogDebug(moduleName, "Successfully initialized Ed25519 signer")
	return nil
}

// performAttestation performs device attestation.
func (m *Manager) performAttestation() {
	if err := m.checkHardwareSignature(); err != nil {
		logging.LogError(moduleName, "Edge Guard attestation failed", err)
	}
}

// GetName returns the module name
func (m *Manager) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index (Edge Guard Manager doesn't have a specific index)
func (m *Manager) GetModuleIndex() int {
	return -1 // Edge Guard Manager is not tracked in status
}

// InstanceConfigUpdated handles configuration updates
func (m *Manager) InstanceConfigUpdated() {
	logging.LogDebug(moduleName, "Handling Edge Guard configuration update")

	currentFreq := m.config.EdgeGuardFrequency
	if m.config.IOFogUUID == "" || m.config.PrivateKey == "" {
		if currentFreq > 0 {
			logging.LogWarn(moduleName, "Edge Guard cannot be enabled while agent is not provisioned; forcing edgeGuardFrequency=0")
			m.config.EdgeGuardFrequency = 0
		}
		currentFreq = 0
	}

	m.mu.Lock()
	previousFreq := m.previousFrequency
	m.mu.Unlock()

	// If frequency hasn't changed, no action needed
	if currentFreq == previousFreq {
		logging.LogDebug(moduleName, fmt.Sprintf("Edge Guard frequency unchanged: %d", currentFreq))
		return
	}

	logging.LogInfo(moduleName, fmt.Sprintf("Edge Guard frequency changed from %d to %d", previousFreq, currentFreq))

	// If frequency changed to 0, stop Edge Guard and clear signature baseline.
	if currentFreq <= 0 {
		m.mu.Lock()
		if m.attestationTicker != nil {
			m.attestationTicker.Stop()
			m.attestationTicker = nil
		}
		if m.attestCancel != nil {
			m.attestCancel()
		}
		m.previousFrequency = currentFreq
		m.mu.Unlock()

		if err := m.deleteStoredSignature(); err != nil {
			logging.LogError(moduleName, "Failed to delete Edge Guard signature from DB", err)
		}
		logging.LogInfo(moduleName, "Edge Guard Manager disabled (frequency = 0)")
		return
	}

	// If frequency changed from 0 to > 0, clear baseline and create a fresh one.
	if previousFreq <= 0 && currentFreq > 0 {
		if err := m.deleteStoredSignature(); err != nil {
			logging.LogError(moduleName, "Failed to reset Edge Guard signature before enabling", err)
			return
		}

		if err := m.checkHardwareSignature(); err != nil {
			logging.LogError(moduleName, "Failed to establish initial Edge Guard signature", err)
			return
		}

		m.mu.Lock()
		if m.attestCancel != nil {
			m.attestCancel()
		}
		m.attestCtx, m.attestCancel = context.WithCancel(context.Background())
		duration := time.Duration(currentFreq) * time.Second
		ticker := time.NewTicker(duration)
		m.attestationTicker = ticker
		go m.attestationWorker(m.attestCtx, ticker)
		m.previousFrequency = currentFreq
		m.mu.Unlock()
		logging.LogInfo(moduleName, fmt.Sprintf("Edge Guard Manager enabled with attestation frequency: %d seconds", currentFreq))
		return
	}

	// If frequency changed but still > 0, cancel old goroutine and restart with new frequency.
	if previousFreq > 0 && currentFreq > 0 {
		m.mu.Lock()
		if m.attestCancel != nil {
			m.attestCancel()
		}
		if m.attestationTicker != nil {
			m.attestationTicker.Stop()
		}

		m.attestCtx, m.attestCancel = context.WithCancel(context.Background())
		duration := time.Duration(currentFreq) * time.Second
		ticker := time.NewTicker(duration)
		m.attestationTicker = ticker
		go m.attestationWorker(m.attestCtx, ticker)
		m.previousFrequency = currentFreq
		m.mu.Unlock()
		logging.LogInfo(moduleName, fmt.Sprintf("Edge Guard Manager frequency updated to: %d seconds", currentFreq))
	}
}
