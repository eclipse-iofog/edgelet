package fieldagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/serviceaccount"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/version"
)

const (
	moduleName            = "Field Agent"
	DeprovisionScopeAll   = "all"
	DeprovisionScopeLocal = "local"
)

// getArchitectureCode maps config arch to Pot provision FogType (1=amd64, 2=arm64, 3=riscv64, 4=arm).
func getArchitectureCode(arch string) int {
	return config.ArchitectureCode(arch)
}

// FieldAgent handles all communication with the fog controller
type FieldAgent struct {
	config       *config.Config
	apiClient    *APIClient
	orchestrator *Orchestrator
	state        *State
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.RWMutex

	// Callbacks for other modules (will be set by supervisor)
	onMicroservicesUpdate func([]*models.Microservice) error
	onRegistriesUpdate    func([]*models.Registry) error
	onConfigsUpdate       func(changedUUIDs []string) error
	processManager        *processmanager.ProcessManager
	modelMgr              *modelmanager.Manager
	knowledgeMgr          *knowledgemanager.Manager
	modelLastUpdate       int64
	knowledgeLastUpdate   int64

	// Microservice management (for MicroserviceManagerInterface)
	latestMicroservices  []*models.Microservice
	latestDesired        map[string]string
	currentMicroservices []*models.Microservice
	registries           []*models.Registry
	microservicesMu      sync.RWMutex

	// Edge resources

	// Container config map
	containerConfigMap map[string]string // microserviceUUID -> config JSON string
	containerConfigMu  sync.RWMutex

	// bootstrapMu guards bootstrapCacheLoaded (SQLite-first boot sync).
	bootstrapMu          sync.RWMutex
	bootstrapCacheLoaded bool

	// reconnect tracks connect generation and last reconcile for init-path dedup (Plan 22-C).
	reconnect   reconnectState
	reconcileMu sync.Mutex

	// Provisioning lock
	provisioningMu sync.Mutex

	// test hook: allows status POST override in unit tests.
	postStatusFn func(ctx context.Context, status map[string]any) error

	// test hook: replaces Deprovision in status auth gate tests.
	deprovisionFn func(clearCredentials bool) error

	// test hook: replaces DeprovisionWithOptions in unit tests.
	deprovisionOptsFn func(clearCredentials bool, scope string, purgeVolumes bool) error

	// test hook: overrides time.Now for status auth gate tests.
	statusAuthNowFn func() time.Time

	// status auth deprovision gate (Plan 25-B).
	statusAuthFailure statusAuthFailureState
	provisionInFlight atomic.Bool

	// test hook: replaces loadInitialControllerData in unit tests.
	loadInitialControllerDataHook func(isConnected bool)

	// test hook: replaces processChanges in the changes worker.
	processChangesFn func(changes map[string]any) bool

	// test hooks: replace on-demand prune steps for the getChanges prune flag.
	pruneImagesFn    func() error
	pruneModelsFn    func() error
	pruneKnowledgeFn func() error
	pruneVolumesFn   func() error

	// test hook: replaces controllerReconcile in unit tests.
	controllerReconcileHook func() error

	controllerRegister *controllerRegisterState

	upgradeScanMu               sync.Mutex
	upgradeScanReschedule       chan struct{}
	appliedUpgradeScanFrequency int
}

var (
	instance *FieldAgent
	once     sync.Once
)

// GetInstance returns the singleton FieldAgent instance
func GetInstance() *FieldAgent {
	once.Do(func() {
		instance = &FieldAgent{
			config:               config.GetInstance(),
			state:                NewState(),
			latestMicroservices:  make([]*models.Microservice, 0),
			currentMicroservices: make([]*models.Microservice, 0),
			registries:           make([]*models.Registry, 0),
			containerConfigMap:   make(map[string]string),
			controllerRegister:   newControllerRegisterState(),
		}
	})
	return instance
}

// getAPIClient returns the current controller API client for a single request chain.
// Callers must not retain the pointer across goroutine boundaries without their own sync.
func (fa *FieldAgent) getAPIClient() *APIClient {
	fa.mu.RLock()
	client := fa.apiClient
	fa.mu.RUnlock()
	return client
}

func (fa *FieldAgent) setAPIClient(client *APIClient) {
	fa.mu.Lock()
	fa.apiClient = client
	fa.mu.Unlock()
}

func (fa *FieldAgent) replaceAPIClient(client *APIClient) {
	fa.mu.Lock()
	fa.apiClient = client
	if fa.orchestrator != nil {
		fa.orchestrator = NewOrchestrator(client)
	}
	fa.mu.Unlock()
}

// Start starts the FieldAgent and all background workers
func (fa *FieldAgent) Start() error {
	logging.LogDebug(moduleName, "Starting Field Agent")

	// Initialize API client
	apiClient, err := NewAPIClient()
	if err != nil {
		logging.LogError(moduleName, "Failed to create API client", err)
		return err
	}
	fa.setAPIClient(apiClient)
	logging.LogDebug(moduleName, "API client initialized")

	version.GetInstance().SetVersionRefreshFunc(fa.fetchControllerVersion)

	// Initialize Orchestrator
	fa.orchestrator = NewOrchestrator(apiClient)
	logging.LogDebug(moduleName, "Orchestrator initialized")

	// Create context for cancellation
	fa.ctx, fa.cancel = context.WithCancel(context.Background())

	// Initialize JWT Manager first if we have the private key
	cfg := config.GetInstance()

	// Private key durability is DB-only. Hydrate in-memory value from SQLite at startup.
	if err := fa.hydratePrivateKeyFromDB(); err != nil {
		logging.LogError(moduleName, "Failed to hydrate private key from SQLite; auth/provision-reconcile paths will remain blocked", err)
		cfg.PrivateKey = ""
		auth.GetJWTManager().Reset()
	}
	fa.hydrateControllerRegisterState()

	// Enforce invariant: unprovisioned agent cannot keep edge guard enabled.
	if cfg.IOFogUUID == "" && cfg.EdgeGuardFrequency > 0 {
		logging.LogWarn(moduleName, "Unprovisioned agent detected with edgeGuardFrequency>0; forcing edgeGuardFrequency=0")
		cfg.EdgeGuardFrequency = 0
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Checking provisioning status: UUID exists=%v, PrivateKey exists=%v",
		cfg.IOFogUUID != "", cfg.PrivateKey != ""))

	if cfg.IOFogUUID != "" && cfg.PrivateKey != "" {
		logging.LogDebug(moduleName, "Agent appears provisioned, initializing JWT Manager")
		// Try to generate JWT to verify private key is valid
		jwt, err := auth.GetJWTManager().GenerateJWT()
		if err != nil {
			logging.LogError(moduleName, "Failed to initialize JWT Manager", err)
			fa.state.SetControllerStatus(models.ControllerStatusNotProvisioned)
			fa.state.SetControllerVerified(false)
			// Update StatusReporter
			statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
				status.ControllerStatus = models.ControllerStatusNotProvisioned
				status.ControllerVerified = false
			})
			logging.LogWarn(moduleName, "JWT initialization failed, setting status to NOT_PROVISIONED")
		} else {
			if jwt == "" {
				logging.LogError(moduleName, "JWT generation returned empty token", errors.New("JWT is empty"))
				fa.state.SetControllerStatus(models.ControllerStatusNotProvisioned)
				fa.state.SetControllerVerified(false)
				statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
					status.ControllerStatus = models.ControllerStatusNotProvisioned
					status.ControllerVerified = false
				})
			} else {
				// Credentials are valid; controller reachability is established by ping workers only.
				fa.state.SetControllerStatus(models.ControllerStatusNotConnected)
				fa.state.SetControllerVerified(false)
				statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
					status.ControllerStatus = models.ControllerStatusNotConnected
					status.ControllerVerified = false
				})
				logging.LogInfo(moduleName, "JWT Manager initialized successfully (credentials valid; controller reachability pending)")
			}
		}
	} else {
		logging.LogInfo(moduleName, "Agent not provisioned (missing UUID or privateKey), setting status to NOT_PROVISIONED")
		fa.state.SetControllerStatus(models.ControllerStatusNotProvisioned)
		fa.state.SetControllerVerified(false)
		// Update StatusReporter
		statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
			status.ControllerStatus = models.ControllerStatusNotProvisioned
			status.ControllerVerified = false
		})
	}

	// Keep edgelet-api token file in sync with current provisioning state.
	if err := auth.EnsureEdgeletAPITokenForCurrentState(); err != nil {
		return fmt.Errorf("failed to reconcile edgelet-api JWT token: %w", err)
	}

	// Start background workers
	logging.LogDebug(moduleName, "Starting background workers")
	fa.wg.Add(8)
	go fa.bootstrapControllerSync()
	go fa.pingControllerWorker()
	go fa.runChangesWorker()
	go fa.postStatusWorker()
	go fa.upgradeScanWorker()
	go fa.localAPITokenRotationWorker()
	go fa.serviceAccountTokenRotationWorker()
	go fa.controllerRegisterWorker()

	logging.LogInfo(moduleName, "Field Agent started successfully")
	return nil
}

// Stop stops the FieldAgent and all background workers
func (fa *FieldAgent) Stop() error {
	logging.LogDebug(moduleName, "Stopping Field Agent")

	if fa.cancel != nil {
		fa.cancel()
	}

	// Wait for all workers to finish
	fa.wg.Wait()

	logging.LogDebug(moduleName, "Field Agent stopped")
	return nil
}

// GetName returns the module name
func (fa *FieldAgent) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index
func (fa *FieldAgent) GetModuleIndex() int {
	return utils.FieldAgent
}

// SetOnMicroservicesUpdate sets the callback for microservices updates
func (fa *FieldAgent) SetOnMicroservicesUpdate(callback func([]*models.Microservice) error) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.onMicroservicesUpdate = callback
}

// SetOnRegistriesUpdate sets the callback for registries updates
func (fa *FieldAgent) SetOnRegistriesUpdate(callback func([]*models.Registry) error) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.onRegistriesUpdate = callback
}

// SetOnConfigsUpdate sets the callback when microservice config content changes.
func (fa *FieldAgent) SetOnConfigsUpdate(callback func(changedUUIDs []string) error) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.onConfigsUpdate = callback
}

// GetContainerConfig returns the container config string for a microservice UUID
func (fa *FieldAgent) GetContainerConfig(microserviceUUID string) (string, bool) {
	fa.containerConfigMu.RLock()
	defer fa.containerConfigMu.RUnlock()
	config, exists := fa.containerConfigMap[microserviceUUID]
	return config, exists
}

// SetProcessManager sets the ProcessManager reference
func (fa *FieldAgent) SetProcessManager(pm *processmanager.ProcessManager) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.processManager = pm
}

// SetModelManager sets the Model manager used for fleet-desired Model ingest.
func (fa *FieldAgent) SetModelManager(m *modelmanager.Manager) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.modelMgr = m
}

func (fa *FieldAgent) modelManager() *modelmanager.Manager {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.modelMgr != nil {
		return fa.modelMgr
	}
	disk := ""
	if fa.config != nil {
		disk = fa.config.DiskDirectory
	}
	fa.modelMgr = modelmanager.New(store.GetInstance(), modelpull.Root(disk))
	fa.modelMgr.SetLiveConfig(fa.config)
	return fa.modelMgr
}

// SetKnowledgeManager sets the Knowledge manager used for fleet-desired Knowledge ingest.
func (fa *FieldAgent) SetKnowledgeManager(m *knowledgemanager.Manager) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.knowledgeMgr = m
}

func (fa *FieldAgent) knowledgeManager() *knowledgemanager.Manager {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.knowledgeMgr != nil {
		return fa.knowledgeMgr
	}
	disk := ""
	if fa.config != nil {
		disk = fa.config.DiskDirectory
	}
	fa.knowledgeMgr = knowledgemanager.New(store.GetInstance(), modelpull.KnowledgeRoot(disk))
	fa.knowledgeMgr.SetLiveConfig(fa.config)
	return fa.knowledgeMgr
}

func (fa *FieldAgent) pruneDanglingImages() error {
	if fa != nil && fa.pruneImagesFn != nil {
		return fa.pruneImagesFn()
	}
	if fa == nil || fa.processManager == nil {
		return errors.New("process manager is not initialized")
	}
	_, err := fa.processManager.PruneDanglingImages()
	return err
}

func (fa *FieldAgent) pruneUnusedLocalModels() error {
	if fa != nil && fa.pruneModelsFn != nil {
		return fa.pruneModelsFn()
	}
	if fa == nil {
		return errors.New("field agent is not initialized")
	}
	_, err := fa.modelManager().PruneDangling()
	return err
}

func (fa *FieldAgent) pruneUnusedLocalKnowledge() error {
	if fa != nil && fa.pruneKnowledgeFn != nil {
		return fa.pruneKnowledgeFn()
	}
	if fa == nil {
		return errors.New("field agent is not initialized")
	}
	_, err := fa.knowledgeManager().PruneDangling()
	return err
}

func (fa *FieldAgent) setModelLastUpdate(ts int64) {
	fa.mu.Lock()
	fa.modelLastUpdate = ts
	fa.mu.Unlock()
}

func (fa *FieldAgent) getModelLastUpdate() int64 {
	fa.mu.RLock()
	defer fa.mu.RUnlock()
	return fa.modelLastUpdate
}

func (fa *FieldAgent) setKnowledgeLastUpdate(ts int64) {
	fa.mu.Lock()
	fa.knowledgeLastUpdate = ts
	fa.mu.Unlock()
}

func (fa *FieldAgent) getKnowledgeLastUpdate() int64 {
	fa.mu.RLock()
	defer fa.mu.RUnlock()
	return fa.knowledgeLastUpdate
}

// FogKnowledgeStatus returns additive fog and local knowledge status keys.
// knowledgeStatus is a JSON string; activeKnowledge is the managed count only.
func (fa *FieldAgent) FogKnowledgeStatus() (knowledgeStatus string, activeKnowledge int, knowledgeLastUpdate int64) {
	if fa == nil {
		return "[]", 0, 0
	}
	return fa.fogKnowledgeStatus()
}

// SetControllerStatus updates the agent controller connection status.
func (fa *FieldAgent) SetControllerStatus(status models.ControllerStatus) {
	fa.state.SetControllerStatus(status)
}

// NotProvisioned checks if the agent is not provisioned
func (fa *FieldAgent) NotProvisioned() bool {
	logging.LogDebug(moduleName, "Started checking provisioned")
	status := fa.state.GetControllerStatus()
	notProvisioned := status == models.ControllerStatusNotProvisioned
	// if notProvisioned {
	// 	logging.LogWarn(moduleName, "Not provisioned")
	// }
	logging.LogDebug(moduleName, fmt.Sprintf("Finished checking provisioned: %v", !notProvisioned))
	return notProvisioned
}

func (fa *FieldAgent) hydratePrivateKeyFromDB() error {
	db := store.GetInstance()
	if db.Conn() == nil {
		return errors.New("sqlite not open")
	}

	privateKey, found, err := db.GetAgentPrivateKey()
	if err != nil {
		return err
	}
	if !found {
		fa.config.PrivateKey = ""
		return nil
	}

	fa.config.PrivateKey = privateKey
	return nil
}

// IsControllerConnected reports whether live controller I/O should proceed.
// Cache reads pass fromFile=true; live paths use cached reachability from ping workers.
func (fa *FieldAgent) IsControllerConnected(fromFile bool) bool {
	if fromFile {
		logging.LogDebug(moduleName, "checked is Controller Connected: true (cache read)")
		return true
	}
	if fa.NotProvisioned() {
		logging.LogDebug(moduleName, "checked is Controller Connected: false (not provisioned)")
		return false
	}
	isConnected := fa.state.IsControllerVerified() && fa.state.GetControllerStatus() == models.ControllerStatusOK
	logging.LogDebug(moduleName, fmt.Sprintf("checked is Controller Connected: %v", isConnected))
	return isConnected
}

// verificationFailed handles controller verification failure
func (fa *FieldAgent) verificationFailed(err error) {
	logging.LogDebug(moduleName, "Start verification Failed of controller")

	fa.state.SetConnected(false)

	if !fa.NotProvisioned() {
		var controllerStatus models.ControllerStatus
		// Check if it's a certificate error
		if isCertificateError(err) {
			controllerStatus = models.ControllerStatusBrokenCertificate
		} else {
			controllerStatus = models.ControllerStatusNotConnected
		}
		fa.state.SetControllerStatus(controllerStatus)
		// Update StatusReporter
		statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
			status.ControllerStatus = controllerStatus
			status.ControllerVerified = false
		})
		logging.LogWarn(moduleName, fmt.Sprintf("controller verification failed: %s", controllerStatus))
	}
	fa.state.SetControllerVerified(false)
	logging.LogDebug(moduleName, "Finished verification Failed of Controller")
}

// isCertificateError checks if an error is a certificate-related error
func isCertificateError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "certificate") || contains(errStr, "tls") || contains(errStr, "ssl")
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// ping pings the controller to check connectivity.
func (fa *FieldAgent) ping() bool {
	ok, _ := fa.pingWithTransition()
	return ok
}

// pingWithTransition pings the controller and reports whether this success was a reconnect
// transition from NOT_CONNECTED or BROKEN_CERTIFICATE (Plan 22-C).
func (fa *FieldAgent) pingWithTransition() (ok bool, transitioned bool) {
	logging.LogDebug(moduleName, "Started Ping")

	if fa.NotProvisioned() {
		logging.LogDebug(moduleName, "Agent not provisioned, skipping ping")
		return false, false
	}

	timeoutSec := config.GetInstance().ControllerPingTimeoutSeconds
	if timeoutSec < 5 {
		timeoutSec = 60
	}
	ctx, cancel := context.WithTimeout(fa.ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	client := fa.getAPIClient()
	if client == nil {
		logging.LogError(moduleName, "API client not initialized for ping", errors.New("api client is nil"))
		return false, false
	}
	logging.LogDebug(moduleName, "Calling API client ping")
	pingOK, err := client.Ping(ctx)
	if err != nil {
		if IsControllerNotReady(err) {
			logging.LogWarn(moduleName, "controller not ready")
			fa.state.SetControllerVerified(false)
			if !fa.NotProvisioned() {
				fa.state.SetControllerStatus(models.ControllerStatusNotConnected)
				statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
					status.ControllerStatus = models.ControllerStatusNotConnected
					status.ControllerVerified = false
				})
			}
			logging.LogDebug(moduleName, "Finished Ping: false (not ready)")
			return false, false
		}
		logging.LogError(moduleName, fmt.Sprintf("Error pinging controller: %v", err), err)
		fa.verificationFailed(err)
		logging.LogDebug(moduleName, "Finished Ping: false (error)")
		return false, false
	}

	if pingOK {
		transitioned = fa.noteControllerReachable()
		fa.state.SetControllerStatus(models.ControllerStatusOK)
		fa.state.SetControllerVerified(true)
		statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
			status.ControllerStatus = models.ControllerStatusOK
			status.ControllerVerified = true
		})
		logging.LogInfo(moduleName, "Controller ping successful, status set to OK")
		logging.LogDebug(moduleName, fmt.Sprintf("Updated StatusReporter: ControllerStatus=%s, ControllerVerified=%v",
			models.ControllerStatusOK, true))

		verifyStatus := statusreporter.GetInstance().GetFieldAgentStatus()
		logging.LogDebug(moduleName, fmt.Sprintf("Verified StatusReporter state: ControllerStatus=%s, ControllerVerified=%v",
			verifyStatus.ControllerStatus, verifyStatus.ControllerVerified))

		logging.LogDebug(moduleName, "Finished Ping: true")
		return true, transitioned
	}

	logging.LogWarn(moduleName, "Controller ping returned false (no status in response)")
	fa.state.SetControllerVerified(false)
	if !fa.NotProvisioned() {
		fa.state.SetControllerStatus(models.ControllerStatusNotConnected)
		statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
			status.ControllerStatus = models.ControllerStatusNotConnected
			status.ControllerVerified = false
		})
	}
	logging.LogDebug(moduleName, "Finished Ping: false")
	return false, false
}

// Provision provisions the agent with the controller
func (fa *FieldAgent) Provision(key string) error {
	fa.setProvisionInFlight(true)
	defer fa.setProvisionInFlight(false)

	logging.LogDebug(moduleName, "Start provisioning")

	// Use fa.ctx if available (daemon mode), otherwise use context.Background() (CLI mode)
	parentCtx := fa.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	// Initialize apiClient if nil (CLI mode - Start() hasn't been called)
	if fa.getAPIClient() == nil {
		apiClient, err := NewAPIClient()
		if err != nil {
			return fmt.Errorf("failed to initialize API client: %w", err)
		}
		fa.setAPIClient(apiClient)
	}

	body, err := buildProvisionRequestBody(key)
	if err != nil {
		return err
	}

	apiClient := fa.getAPIClient()
	if apiClient == nil {
		return errors.New("api client is not initialized")
	}
	result, err := apiClient.Request(ctx, "provision", POST, nil, body)
	if err != nil {
		return fmt.Errorf("provisioning failed: %w", err)
	}

	// Extract UUID, private key, and namespace from result and save to config
	cfg := config.GetInstance()
	updated := false

	if uuid, ok := result["uuid"].(string); ok && uuid != "" {
		cfg.IOFogUUID = uuid
		if err = cfg.SetProperty("iofogUuid", uuid); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to set iofogUuid in YAML config: %v", err))
		}
		updated = true
	}
	if privateKey, ok := result["privateKey"].(string); ok && privateKey != "" {
		// DB-only private key durability: persist to SQLite first (fail-closed for auth/provision paths).
		if err := store.GetInstance().UpsertAgentPrivateKey(privateKey); err != nil {
			return fmt.Errorf("provisioning failed to persist private key to sqlite: %w", err)
		}
		cfg.PrivateKey = privateKey
		updated = true
	}
	if namespace, ok := result["namespace"].(string); ok && namespace != "" {
		cfg.Namespace = namespace
		if err = cfg.SetProperty("namespace", namespace); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to set namespace in YAML config: %v", err))
		}
		updated = true
	}

	// Save config to disk
	if updated {
		// Ensure privateKey is not persisted in YAML (DB is the durable source).
		yamlConfig := cfg.GetYamlConfig()
		if yamlConfig != nil {
			profile := yamlConfig.GetProfile(cfg.GetCurrentProfile().FullValue())
			if profile != nil {
				profile.SetProperty("privateKey", "")
			}
		}

		configPath := utils.ConfigYAMLPath
		if err := config.SaveConfig(configPath); err != nil {
			logging.LogError(moduleName, "Failed to save config updates after provisioning", err)
			return fmt.Errorf("provisioning succeeded but failed to save config: %w", err)
		}
	}

	// Reprovision semantics: new private key invalidates the Edge Guard baseline signature.
	if err := store.GetInstance().DeleteEdgeGuardSignature(); err != nil {
		return fmt.Errorf("provisioning failed to reset edgeguard signature baseline: %w", err)
	}

	// Reset JWT manager to use new credentials
	// IMPORTANT: Reset AFTER config is saved so JWT manager can reload from updated config
	auth.GetJWTManager().Reset()
	if err := auth.EnsureEdgeletAPITokenForCurrentState(); err != nil {
		return fmt.Errorf("provisioning succeeded but failed to rotate edgelet-api JWT: %w", err)
	}

	// Recreate API client with new credentials
	// This is critical because the API client was created before provisioning (without UUID/privateKey)
	newAPIClient, err := NewAPIClient()
	if err != nil {
		logging.LogError(moduleName, "Failed to recreate API client after provisioning", err)
		return fmt.Errorf("provisioning succeeded but failed to recreate API client: %w", err)
	}
	fa.replaceAPIClient(newAPIClient)

	// Test JWT generation to ensure it works
	if _, testErr := auth.GetJWTManager().GenerateJWT(); testErr != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("JWT generation test failed after provisioning: %v", testErr))
	}

	// Set status to OK since provisioning succeeded
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)
	clearSupervisorWarningAfterProvision()

	// If daemon is running (ctx is set), load controller data and post config.
	// API client and JWT were already refreshed synchronously above; do not call Update()
	// here — its async client swap races with postFogConfig and duplicates work.
	if fa.ctx != nil {
		fa.loadInitialControllerData(true)

		// Post fog config to controller
		// This sends the agent configuration to the controller and establishes the connection
		if err := fa.postFogConfig(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to post fog config after provisioning (non-critical): %v", err))
			// Don't fail provisioning for this
		}

		fa.PostStatusHelper()
	}

	fa.resetStatusAuthFailure()

	logging.LogDebug(moduleName, "Finished provisioning")
	return nil
}

func clearSupervisorWarningAfterProvision() {
	statusreporter.GetInstance().UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetWarningMessage("")
		if status.DaemonStatus == models.ModuleStatusWarning {
			status.SetDaemonStatus(models.ModuleStatusRunning)
		}
	})
}

// getDeprovisionBody builds the deprovision request body with microservice UUIDs
func (fa *FieldAgent) getDeprovisionBody() map[string]any {
	// Get all microservice UUIDs from latest and current (using a set to avoid duplicates)
	uuidSet := make(map[string]bool)

	latest := fa.GetLatestMicroservices()
	current := fa.GetCurrentMicroservices()

	for _, ms := range latest {
		if ms.MicroserviceUUID != "" {
			uuidSet[ms.MicroserviceUUID] = true
		}
	}
	for _, ms := range current {
		if ms.MicroserviceUUID != "" {
			uuidSet[ms.MicroserviceUUID] = true
		}
	}

	// Convert set to array
	uuids := make([]string, 0, len(uuidSet))
	for uuid := range uuidSet {
		uuids = append(uuids, uuid)
	}

	return map[string]any{
		"microserviceUuids": uuids,
	}
}

// Deprovision deprovisions the agent
// clearCredentials=true skip controller request
func (fa *FieldAgent) Deprovision(clearCredentials bool) error {
	return fa.DeprovisionWithOptions(clearCredentials, DeprovisionScopeAll, false)
}

func normalizeDeprovisionScope(scope string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	if normalized == "" {
		return DeprovisionScopeAll, nil
	}
	switch normalized {
	case DeprovisionScopeAll, DeprovisionScopeLocal:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid deprovision scope %q (allowed: %s|%s)", scope, DeprovisionScopeAll, DeprovisionScopeLocal)
	}
}

// DeprovisionWithScope deprovisions the agent and controls cleanup scope.
// scope=all removes managed+local workloads; scope=local preserves local workloads.
// Persistent VOLUME trees are preserved unless purgeVolumes is set via DeprovisionWithOptions.
func (fa *FieldAgent) DeprovisionWithScope(clearCredentials bool, scope string) error {
	return fa.DeprovisionWithOptions(clearCredentials, scope, false)
}

// DeprovisionWithOptions deprovisions the agent with cleanup scope and optional
// persistent-volume purge. Automatic callers (delete-node, Edge Guard, status-auth)
// must pass purgeVolumes=false so volumes/data and volumes/shared remount later.
func (fa *FieldAgent) DeprovisionWithOptions(clearCredentials bool, scope string, purgeVolumes bool) error {
	if fa.deprovisionOptsFn != nil {
		return fa.deprovisionOptsFn(clearCredentials, scope, purgeVolumes)
	}
	normalizedScope, scopeErr := normalizeDeprovisionScope(scope)
	if scopeErr != nil {
		return scopeErr
	}
	preserveLocal := normalizedScope == DeprovisionScopeLocal
	logging.LogInfo(moduleName, "Start Deprovisioning")

	// Acquire provisioning lock
	if !fa.provisioningMu.TryLock() {
		msg := "Provisioning in progress"
		logging.LogInfo(moduleName, msg)
		return fmt.Errorf("%s", msg)
	}
	defer fa.provisioningMu.Unlock()

	// Check if already not provisioned
	if fa.NotProvisioned() {
		logging.LogInfo(moduleName, "Finished Deprovisioning : Failure - not provisioned")
		return errors.New("\nFailure - not provisioned")
	}

	// Store configuration values before clearing them
	iofogUUID := fa.config.IOFogUUID
	// Note: privateKey and namespace are stored but not used in Go

	// Deprovision invariant: force edge guard disabled and clear DB-backed secret state.
	fa.config.EdgeGuardFrequency = 0
	func() {
		db := store.GetInstance()
		if db.Conn() == nil {
			logging.LogWarn(moduleName, "SQLite not open during deprovision; private-key dependent paths will stay blocked")
			return
		}
		if err := db.DeleteEdgeGuardSignature(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to delete edgeguard signature from SQLite: %v", err))
		}
		if err := db.DeleteAgentPrivateKey(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to delete private key from SQLite: %v", err))
		}
	}()

	// Attempt deprovision request if not token expired
	deprovisionRequestSuccessful := false
	if !clearCredentials {
		logging.LogDebug(moduleName, "Attempting deprovision request to controller")
		ctx := context.Background()
		_, err := fa.orchestrator.Request(ctx, "deprovision", POST, nil, fa.getDeprovisionBody())
		if err != nil {
			logging.LogError(moduleName, "Unable to make deprovision request", err)
		} else {
			logging.LogInfo(moduleName, "Deprovision request completed successfully")
			deprovisionRequestSuccessful = true
		}
	} else {
		logging.LogInfo(moduleName, "Skipping deprovision request due to expired token")
	}

	// Update status to NOT_PROVISIONED
	statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
		status.ControllerStatus = models.ControllerStatusNotProvisioned
	})

	// Clear configuration AFTER the deprovision request attempt
	configUpdated := true
	func() {
		defer func() {
			if r := recover(); r != nil {
				configUpdated = false
				logging.LogError(moduleName, "Error saving config updates", fmt.Errorf("%v", r))
			}
		}()

		fa.config.IOFogUUID = ""
		fa.config.PrivateKey = ""
		fa.config.Namespace = "default"
		fa.config.EdgeGuardFrequency = 0

		// Update YAML config properties before saving
		// The SaveConfig() function uses GetYamlConfig() which doesn't reflect in-memory struct changes
		// We need to update the YAML config's Properties map directly
		cfg := config.GetInstance()
		yamlConfig := cfg.GetYamlConfig()
		if yamlConfig != nil {
			profile := yamlConfig.GetProfile(cfg.GetCurrentProfile().FullValue())
			if profile != nil {
				profile.SetProperty("iofogUuid", "")
				profile.SetProperty("privateKey", "")
				profile.SetProperty("namespace", "default")
				profile.SetProperty("edgeGuardFrequency", "0")
				logging.LogDebug(moduleName, "Updated YAML config properties: iofogUuid, privateKey, namespace, edgeGuardFrequency")
			} else {
				logging.LogWarn(moduleName, "Profile not found in YAML config, cannot update properties")
			}
		} else {
			logging.LogWarn(moduleName, "YAML config not loaded, cannot update properties")
		}

		// Save config updates
		// Suppress SIGHUP so the watcher does not disrupt the HTTP response to the CLI.
		config.SuppressReloadForDeprovision()
		defer config.RestoreReloadAfterDeprovision()

		configPath := utils.ConfigYAMLPath
		if err := config.SaveConfig(configPath); err != nil {
			logging.LogError(moduleName, "Error saving config updates", err)
			configUpdated = false
		} else {
			logging.LogDebug(moduleName, "Configuration cleared successfully")
		}

		// Reset JWT Manager
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Failed to reset JWT Manager: %v", r))
					// Don't fail deprovisioning for JWT reset failure
				}
			}()
			auth.GetJWTManager().Reset()
			logging.LogDebug(moduleName, "JWT Manager reset completed")
		}()
	}()

	if err := auth.EnsureEdgeletAPITokenForCurrentState(); err != nil {
		return fmt.Errorf("deprovisioning succeeded but failed to rotate edgelet-api JWT: %w", err)
	}

	if configUpdated {
		// Update config backup file
		// Note: This might not be implemented in Go yet, but we'll log it
		logging.LogDebug(moduleName, "Config backup file update requested")
	}

	// Set state early so NotProvisioned() is true before slow cleanup — avoids 401 handler
	// re-entering Deprovision while this call still holds the lock
	fa.state.SetControllerStatus(models.ControllerStatusNotProvisioned)
	fa.state.SetControllerVerified(false)

	// Clear microservice manager
	fa.Clear()
	// Clear stale runtime status cache so /v1/ms and CLI cannot show ghost entries
	// after deprovision while cleanup continues in background.
	statusreporter.GetInstance().ResetProcessManagerStatus()

	// Run slow cleanup in background so HTTP handler can return quickly (avoids CLI timeout)
	go func() {
		// For scope=all, purge persisted local desired-state first so local reconciler
		// cannot recreate workloads while cleanup is still in progress.
		fa.clearSQLiteCacheTablesOnDeprovision(preserveLocal)

		// Stop running microservices
		if fa.processManager != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.LogError(moduleName, "Error stopping running microservices", fmt.Errorf("%v", r))
					}
				}()
				if err := fa.processManager.StopRunningMicroservicesWithScope(iofogUUID, true, !preserveLocal); err != nil {
					logging.LogError(moduleName, "Error stopping running microservices", err)
				}
			}()
		}

		// Lite all-scope deprovision: prune residual containers after workload removal.
		// Do not prune persistent VOLUME data here.
		fa.clearLiteRuntimeArtifactsOnDeprovision(preserveLocal, func() error {
			if fa.processManager == nil {
				return nil
			}
			_, err := fa.processManager.PruneContainers()
			return err
		})

		// Persistent VOLUME trees under volumes/data and volumes/shared stay unless
		// the operator asked to purge workload claims. Ledger rows keep
		// unreferenced_at NULL so re-provision remounts the same host paths
		// and orphan prune does not start a grace clock.
		fa.applyDeprovisionVolumePolicy(preserveLocal, purgeVolumes)

		// Clear service-account token projections and metadata.
		serviceaccount.GetInstance().Clear()

		fa.resetControllerRegisterState()

		// Run again after runtime cleanup for best-effort convergence.
		fa.clearSQLiteCacheTablesOnDeprovision(preserveLocal)

		// Notify modules AFTER configuration is cleared
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Some module notifications failed during deprovisioning: %v", r))
				}
			}()
			logging.LogDebug(moduleName, "Notifying modules after configuration update")
			if fa.onMicroservicesUpdate != nil {
				if err := fa.onMicroservicesUpdate([]*models.Microservice{}); err != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Error notifying microservices update: %v", err))
				}
			}
			if fa.onRegistriesUpdate != nil {
				if err := fa.onRegistriesUpdate([]*models.Registry{}); err != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Error notifying registries update: %v", err))
				}
			}
			if fa.onConfigsUpdate != nil {
				if err := fa.onConfigsUpdate(nil); err != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Error notifying configs update: %v", err))
				}
			}
			logging.LogDebug(moduleName, "Module notification completed")
		}()

		resultMessage := "Success - cleaned up locally"
		if preserveLocal {
			resultMessage = "Success - deprovisioned while preserving local microservices"
		}
		if deprovisionRequestSuccessful {
			resultMessage = "Success - deprovisioned from controller and cleaned up locally"
			if preserveLocal {
				resultMessage = "Success - deprovisioned from controller and preserved local microservices"
			}
		} else if !clearCredentials {
			resultMessage = "Success - cleaned up locally (controller deprovision failed)"
			if preserveLocal {
				resultMessage = "Success - preserved local microservices (controller deprovision failed)"
			}
		}
		logging.LogInfo(moduleName, fmt.Sprintf("Finished Deprovisioning : %s", resultMessage))
	}()

	logging.LogInfo(moduleName, "Deprovision accepted — cleanup running in background")
	return nil
}

func (fa *FieldAgent) clearSQLiteCacheTablesOnDeprovision(preserveLocal bool) {
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error clearing SQLite cache tables: %v", r))
		}
	}()
	db := store.GetInstance()
	if db.Conn() == nil {
		return
	}
	if err := db.ClearControllerMicroservices(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error clearing controller_microservices table: %v", err))
	}
	if err := db.ClearControllerRegistries(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error clearing controller_registries table: %v", err))
	}
	if err := db.ClearControllerModels(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error clearing controller_models table: %v", err))
	}
	if err := db.ClearControllerRuntimeClasses(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error clearing controller_runtime_classes table: %v", err))
	}
	if err := db.ClearControllerKnowledge(); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error clearing controller_knowledge table: %v", err))
	}
	fa.setModelLastUpdate(0)
	fa.setKnowledgeLastUpdate(0)
	if !preserveLocal {
		if err := db.ClearLocalWorkloads(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error clearing local_workloads table: %v", err))
		}
		if err := db.ClearRuntimeContainerRefs(store.RuntimeScopeLocal); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error clearing local runtime_container_refs: %v", err))
		}
	}
	logging.LogDebug(moduleName, "SQLite cache tables cleared on deprovision")
}

func (fa *FieldAgent) clearVolumeMountsOnDeprovision(preserveLocal bool, clearAllFn func() error, clearLocalFn func() error) {
	clearFn := clearAllFn
	if preserveLocal {
		clearFn = clearLocalFn
	}
	if clearFn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Error clearing volume mounts", fmt.Errorf("%v", r))
		}
	}()
	if err := clearFn(); err != nil {
		logging.LogError(moduleName, "Error clearing volume mounts", err)
	}
}

func (fa *FieldAgent) clearLiteRuntimeArtifactsOnDeprovision(preserveLocal bool, pruneContainersFn func() error) {
	if preserveLocal {
		return
	}
	if buildmeta.HasEmbeddedEngine() {
		engineType := strings.ToLower(strings.TrimSpace(fa.config.ContainerEngine))
		if engineType == constants.EngineEdgelet {
			return
		}
	}
	engineType := strings.ToLower(strings.TrimSpace(fa.config.ContainerEngine))
	if engineType != constants.EngineDocker && engineType != constants.EnginePodman {
		return
	}

	logging.LogDebug(moduleName, "Start lite runtime artifact prune on deprovision (containers)")
	if pruneContainersFn != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.LogError(moduleName, "Error during deprovision container prune", fmt.Errorf("%v", r))
				}
			}()
			if err := pruneContainersFn(); err != nil {
				logging.LogError(moduleName, "Error during deprovision container prune", err)
			}
		}()
	}
	logging.LogDebug(moduleName, "Finished lite runtime artifact prune on deprovision")
}

// Update updates the FieldAgent when configuration changes.
// Most workers re-read config on every tick; upgradeScanWorker is notified
// explicitly when upgradeScanFrequency changes so its timer is reset immediately.
// NewAPIClient() is moved into the background goroutine so the SIGHUP handler
// goroutine is never blocked by DNS resolution for the new controller URL.
func (fa *FieldAgent) Update() error {
	logging.LogDebug(moduleName, "Updating Field Agent due to config change")

	fa.rescheduleUpgradeScanIfFrequencyChanged()

	if err := fa.hydratePrivateKeyFromDB(); err != nil {
		logging.LogError(moduleName, "Failed to hydrate private key from SQLite during update; blocking private-key dependent paths", err)
		fa.config.PrivateKey = ""
	}

	// Reset JWT so it is reloaded from the updated credentials on next use.
	// Only the Reset itself needs the lock; client creation is done async.
	fa.mu.Lock()
	auth.GetJWTManager().Reset()
	fa.mu.Unlock()
	if err := auth.EnsureEdgeletAPITokenForCurrentState(); err != nil {
		logging.LogError(moduleName, "Failed to reconcile edgelet-api JWT token during update", err)
		return fmt.Errorf("failed to reconcile edgelet-api JWT token during update: %w", err)
	}

	// Recreate the API client and post fog config asynchronously so that the
	// SIGHUP handler goroutine returns immediately (no DNS / TLS blocking).
	go func() {
		logging.LogDebug(moduleName, "Recreating API client after config change")
		apiClient, err := NewAPIClient()
		if err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Failed to recreate API client: %v", err), err)
			return
		}
		fa.replaceAPIClient(apiClient)

		if !fa.shouldPostFogConfigAfterUpdate() {
			logging.LogWarn(moduleName, "Skipping postFogConfig because last config reload was rejected")
			return
		}

		logging.LogDebug(moduleName, "Starting asynchronous postFogConfig")
		if err := fa.postFogConfig(); err != nil {
			logging.LogError(moduleName, "Failed to post updated fog config", err)
		} else {
			logging.LogDebug(moduleName, "Successfully posted fog config")
		}
	}()

	logging.LogDebug(moduleName, "Field Agent update dispatched (workers self-update via dynamic timers)")
	return nil
}

func (fa *FieldAgent) shouldPostFogConfigAfterUpdate() bool {
	return config.IsLastReloadSuccessful()
}
