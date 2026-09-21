package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/edgeguard"
	"github.com/eclipse-iofog/edgelet/internal/edgeletapi"
	"github.com/eclipse-iofog/edgelet/internal/engines"
	"github.com/eclipse-iofog/edgelet/internal/fieldagent"
	"github.com/eclipse-iofog/edgelet/internal/gps"
	"github.com/eclipse-iofog/edgelet/internal/healthcheck"
	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/pruning"
	"github.com/eclipse-iofog/edgelet/internal/resourceconsumption"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

const (
	moduleName = "Supervisor"
)

var requestDaemonRestart = func(reason string, cause error) {
	logging.LogError(moduleName, reason, cause)
	if err := signalSelfForSupervisor(syscall.SIGTERM); err != nil {
		logging.LogError(moduleName, "Failed to signal daemon for restart", err)
	}
}

var setContainerdUnexpectedExitHandler = func(svc *containerd.Service, handler func(error)) {
	svc.SetUnexpectedExitHandler(handler)
}

var (
	supervisorNewContainerEngine = engines.NewContainerEngine
	engineInitRetryWait          = time.Sleep
)

// Supervisor orchestrates all ioFog modules
type Supervisor struct {
	config *config.Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// containerEngine is set once ProcessManager is wired to a runtime backend.
	containerEngine engine.ContainerEngine
	engineWireMu    sync.Mutex

	// Embedded containerd service (non-nil only when containerEngine=iofog)
	containerdSvc *containerd.Service

	// containerdAttachOnly: control plane attaches to data-plane containerd; do not stop on control shutdown.
	containerdAttachOnly bool

	// Module instances
	statusReporter             *statusreporter.StatusReporter
	networkInterfaceManager    *network.Manager
	resourceConsumptionManager *resourceconsumption.Manager
	fieldAgent                 *fieldagent.FieldAgent
	processManager             *processmanager.ProcessManager
	gpsManager                 *gps.Manager
	localAPI                   *edgeletapi.EdgeletAPI
	dockerPruningManager       *pruning.Manager
	edgeGuardManager           *edgeguard.Manager
	healthcheckRunner          *healthcheck.Runner

	// Edgelet API monitoring
	localAPIMonitorTicker *time.Ticker

	// Reload context captured before LoadConfig in BeginConfigReload.
	pendingReloadCtx *reloadEngineContext

	reloadMu sync.Mutex
}

// NewSupervisor creates a new Supervisor instance
func NewSupervisor() *Supervisor {
	return &Supervisor{
		config: config.GetInstance(),
	}
}

// SetPrestartedContainerd injects an embedded containerd service already started in main
// (embedded engine). Supervisor will not start containerd again; it only runs
// the watchdog and stops containerd on shutdown unless attach-only (runtime split).
func (s *Supervisor) SetPrestartedContainerd(svc *containerd.Service) {
	s.containerdSvc = svc
}

// SetContainerdAttachOnly marks containerd as owned by the data-plane unit (Plan 11 split).
func (s *Supervisor) SetContainerdAttachOnly(v bool) {
	s.containerdAttachOnly = v
}

// Start starts all modules in the correct order
func (s *Supervisor) Start() error {
	logging.LogDebug(moduleName, "Starting Supervisor")

	// Open SQLite database before any module starts
	db := store.GetInstance()
	if err := db.Open(s.config.DiskDirectory); err != nil {
		return fmt.Errorf("failed to open SQLite database: %w", err)
	}
	logging.LogInfo(moduleName, "SQLite database opened")
	if err := s.ensureDefaultLocalRegistriesOnStartup(db); err != nil {
		return fmt.Errorf("failed to seed default local registries: %w", err)
	}

	// Create context for cancellation
	s.ctx, s.cancel = context.WithCancel(context.Background())

	// Start StatusReporter first
	s.statusReporter = statusreporter.GetInstance()
	if err := s.statusReporter.Start(); err != nil {
		return err
	}
	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetModuleStatus(utils.StatusReporter, models.ModuleStatusRunning)
	})

	// Set daemon status to STARTING
	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetDaemonStatus(models.ModuleStatusStarting).
			SetDaemonLastStart(time.Now().UnixMilli()).
			SetOperationDuration(0)
	})

	// Start Network Interface Manager
	s.networkInterfaceManager = network.GetInstance()
	if err := s.networkInterfaceManager.Start(); err != nil {
		return err
	}

	// Start Resource Consumption Manager
	s.resourceConsumptionManager = resourceconsumption.GetInstance()
	if err := s.startModule(s.resourceConsumptionManager); err != nil {
		return err
	}

	// Start Field Agent
	s.fieldAgent = fieldagent.GetInstance()
	if err := s.startModule(s.fieldAgent); err != nil {
		return err
	}

	// Start Process Manager
	s.processManager = processmanager.GetInstance()
	// Instantiate the container engine based on configuration.
	cfg := config.GetInstance()
	runtimestate.GetState().RecordStartupEngine(cfg.ContainerEngine)
	processmanager.SetQuiesced(false)
	runtimestate.GetState().SetPendingRestart(false)
	runtimestate.GetState().SetAgentPhase("running")

	// If the embedded iofog engine is selected, ensure containerd is running before the engine.
	// Startup ownership is in cmd/edgelet bootstrap; Supervisor only consumes prestarted runtime.
	if cfg.ContainerEngine == constants.EngineEdgelet {
		if s.containerdSvc == nil {
			return fmt.Errorf("embedded containerd must be prestarted before Supervisor when containerEngine=%q", constants.EngineEdgelet)
		}
		logging.LogInfo(moduleName, "Using embedded containerd started before Supervisor")
		s.configureContainerdFailFastHandler()

		// Watchdog for embedded containerd socket liveness.
		s.wg.Add(1)
		go s.containerdWatchdog()
	}

	engConfig := engine.EngineConfig{
		SocketURL:  cfg.ContainerEngineURL,
		APIVersion: cfg.DockerAPIVersion,
		LogDir:     cfg.LogDirectory + "containers",
	}

	var eng engine.ContainerEngine
	var engErr error
	engineDegraded := false
	if cfg.ContainerEngine == constants.EngineDocker || cfg.ContainerEngine == constants.EnginePodman {
		eng, engErr = s.initExternalEngineWithRetry(cfg.ContainerEngine, engConfig)
		if engErr != nil {
			logging.LogError(moduleName,
				fmt.Sprintf("%s engine unavailable after %d init attempts (retry budget %s); running degraded until socket is ready",
					cfg.ContainerEngine, engineInitMaxRetries, engineInitTotalWaitBudget()),
				engErr)
			s.markExternalEngineDegraded()
			s.startExternalEngineRecovery(cfg.ContainerEngine, engConfig)
			engineDegraded = true
			engErr = nil
		}
	} else {
		eng, engErr = supervisorNewContainerEngine(cfg.ContainerEngine, engConfig)
		if engErr != nil {
			return fmt.Errorf("failed to create container engine %q: %w", cfg.ContainerEngine, engErr)
		}
		if initErr := eng.Init(engConfig); initErr != nil {
			return fmt.Errorf("failed to init container engine %q: %w", cfg.ContainerEngine, initErr)
		}
		eng = engines.WrapWithLoggingIfExternal(eng, cfg.ContainerEngine)
	}
	if engErr != nil {
		return engErr
	}
	if eng != nil {
		if err := s.wireContainerEngine(eng); err != nil {
			return err
		}
	}

	// Start HealthcheckRunner when using iofog engine (Docker/Podman use native healthcheck)
	if eng != nil && cfg.ContainerEngine == constants.EngineEdgelet {
		var hcEng healthcheck.HealthcheckEngine
		if he, ok := eng.(healthcheck.HealthcheckEngine); ok {
			hcEng = he
		}
		s.healthcheckRunner = healthcheck.NewRunner(eng, hcEng, s.fieldAgent)
		if err := s.healthcheckRunner.Start(s.ctx); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Healthcheck runner failed to start: %v", err))
		}
	}

	// Start Edgelet API early so local CLI works before optional modules finish starting.
	s.localAPI = edgeletapi.GetInstance()
	s.config.SetReloadCallback(s.ReloadFromDisk)
	s.config.SetGPSConfigCallback(s.fieldAgent.InstanceGPSConfigUpdated)
	if err := s.localAPI.Start(); err != nil {
		return fmt.Errorf("failed to start Edgelet API server: %w", err)
	}
	s.fieldAgent.SetOnConfigsUpdate(func(changedUUIDs []string) error {
		s.localAPI.NotifyMicroserviceConfigChanged(changedUUIDs)
		return nil
	})

	s.localAPIMonitorTicker = time.NewTicker(10 * time.Second)
	s.wg.Add(1)
	go s.monitorLocalAPI()

	if engineDegraded {
		s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
			status.SetDaemonStatus(models.ModuleStatusWarning)
		})
	} else {
		s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
			status.SetDaemonStatus(models.ModuleStatusRunning)
		})
	}

	// Start GPS Manager
	s.gpsManager = gps.GetInstance()
	if err := s.startModule(s.gpsManager); err != nil {
		return err
	}

	// Start Pruning Manager — inject engine so non-Docker engines (iofog/containerd) are pruned correctly.
	// Also wire in the microservice image callback so scheduled/threshold pruning protects
	// controller-managed and local-deployed microservice images.
	s.dockerPruningManager = pruning.GetInstance()
	pm := s.processManager
	s.dockerPruningManager.SetGetMicroservicesCallback(func() []string {
		return pm.ConfiguredMicroserviceImages()
	})
	s.dockerPruningManager.SetEngine(eng)
	pruneUnusedLocalModels := func() {
		modelsRoot := modelpull.Root(s.config.DiskDirectory)
		mm := modelmanager.New(store.GetInstance(), modelsRoot)
		mm.SetLiveConfig(s.config)
		mm.SetDiskPolicy(s.config.DiskDirectory, s.config.AvailableDiskThreshold, nil)
		if _, err := mm.PruneDangling(); err != nil {
			logging.LogError(moduleName, "Error pruning unused local models", err)
		}
	}
	pruneUnusedLocalKnowledge := func() {
		knowledgeRoot := modelpull.KnowledgeRoot(s.config.DiskDirectory)
		km := knowledgemanager.New(store.GetInstance(), knowledgeRoot)
		km.SetLiveConfig(s.config)
		km.SetDiskPolicy(s.config.DiskDirectory, s.config.AvailableDiskThreshold, nil)
		if _, err := km.PruneDangling(); err != nil {
			logging.LogError(moduleName, "Error pruning unused local knowledge", err)
		}
	}
	s.dockerPruningManager.SetPruneModelsCallback(pruneUnusedLocalModels)
	s.dockerPruningManager.SetPruneKnowledgeCallback(pruneUnusedLocalKnowledge)
	s.processManager.SetWatchdogLocalModelsCallback(pruneUnusedLocalModels)
	s.processManager.SetWatchdogLocalKnowledgeCallback(pruneUnusedLocalKnowledge)
	if err := s.dockerPruningManager.Start(); err != nil {
		logging.LogError(moduleName, "Failed to start Pruning Manager", err)
	}

	// Start Edge Guard Manager
	s.edgeGuardManager = edgeguard.GetInstance()
	if err := s.edgeGuardManager.Start(); err != nil {
		logging.LogError(moduleName, "Failed to start Edge Guard Manager", err)
	}

	// Start operation duration worker
	s.wg.Add(1)
	go s.operationDurationWorker()

	logging.LogDebug(moduleName, "Started Supervisor")
	return nil
}

func (s *Supervisor) configureContainerdFailFastHandler() {
	if s.containerdSvc == nil {
		return
	}
	setContainerdUnexpectedExitHandler(s.containerdSvc, func(err error) {
		requestDaemonRestart("Embedded containerd exited unexpectedly after readiness; requesting immediate daemon restart", err)
	})
}

// startModule starts a module and updates its status
func (s *Supervisor) startModule(module Module) error {
	logging.LogInfo(moduleName, "Starting "+module.GetName())
	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetModuleStatus(module.GetModuleIndex(), models.ModuleStatusStarting)
	})

	if err := module.Start(); err != nil {
		return err
	}

	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetModuleStatus(module.GetModuleIndex(), models.ModuleStatusRunning)
	})
	logging.LogInfo(moduleName, "Started "+module.GetName())
	return nil
}

const (
	engineInitMaxRetries     = 12
	engineInitInitialBackoff = 2 * time.Second
	engineInitMaxBackoff     = 30 * time.Second
)

// engineInitTotalWaitBudget returns worst-case wall time spent sleeping between
// external-engine init attempts (2s initial backoff, doubles capped at 30s).
func engineInitTotalWaitBudget() time.Duration {
	total := time.Duration(0)
	backoff := engineInitInitialBackoff
	for attempt := 1; attempt < engineInitMaxRetries; attempt++ {
		total += backoff
		backoff *= 2
		if backoff > engineInitMaxBackoff {
			backoff = engineInitMaxBackoff
		}
	}
	return total
}

func advanceEngineInitBackoff(backoff time.Duration) time.Duration {
	backoff *= 2
	if backoff > engineInitMaxBackoff {
		return engineInitMaxBackoff
	}
	return backoff
}

func initExternalEngineAttempt(engineType string, cfg engine.EngineConfig) (engine.ContainerEngine, error) {
	eng, createErr := supervisorNewContainerEngine(engineType, cfg)
	if createErr != nil {
		return nil, createErr
	}
	if initErr := eng.Init(cfg); initErr != nil {
		return nil, initErr
	}
	return engines.WrapWithLoggingIfExternal(eng, engineType), nil
}

// initExternalEngineWithRetry creates and initializes Docker or Podman engine with
// exponential backoff when the socket may be temporarily unavailable (e.g. host reboot).
// There is no fallback to containerEngine=edgelet; callers must handle a returned error.
func (s *Supervisor) initExternalEngineWithRetry(engineType string, cfg engine.EngineConfig) (engine.ContainerEngine, error) {
	var lastErr error
	backoff := engineInitInitialBackoff

	for attempt := 1; attempt <= engineInitMaxRetries; attempt++ {
		eng, err := initExternalEngineAttempt(engineType, cfg)
		if err == nil {
			logging.LogInfo(moduleName, fmt.Sprintf("%s engine initialized successfully after %d attempt(s)", engineType, attempt))
			return eng, nil
		}
		lastErr = err
		logging.LogWarn(moduleName, fmt.Sprintf("%s engine init attempt %d/%d failed (socket may be unavailable): %v", engineType, attempt, engineInitMaxRetries, err))
		if attempt < engineInitMaxRetries {
			logging.LogInfo(moduleName, fmt.Sprintf("Retrying in %v...", backoff))
			engineInitRetryWait(backoff)
			backoff = advanceEngineInitBackoff(backoff)
		}
	}

	return nil, fmt.Errorf("%s engine unavailable after %d init attempts (retry wait budget %s): %w",
		engineType, engineInitMaxRetries, engineInitTotalWaitBudget(), lastErr)
}

func (s *Supervisor) markExternalEngineDegraded() {
	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetModuleStatus(utils.ProcessManager, models.ModuleStatusWarning).
			SetDaemonStatus(models.ModuleStatusWarning)
	})
}

func (s *Supervisor) wireContainerEngine(eng engine.ContainerEngine) error {
	s.engineWireMu.Lock()
	defer s.engineWireMu.Unlock()
	if s.containerEngine != nil {
		return nil
	}
	if err := s.processManager.Start(eng, s.fieldAgent); err != nil {
		return err
	}
	s.containerEngine = eng
	runtimestate.GetState().SetEngineReady(true)
	processmanager.TryResumeReconcileAfterDataPlaneEngineReady()
	s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
		status.SetModuleStatus(utils.ProcessManager, models.ModuleStatusRunning)
	})
	s.fieldAgent.SetProcessManager(s.processManager)
	s.fieldAgent.OnProcessManagerReady()
	fieldagent.GetLogSessionManager().SetProcessManager(s.processManager)
	fieldagent.GetLogSessionManager().SetEngine(eng)
	fieldagent.GetExecSessionManager().SetProcessManager(s.processManager)
	if s.dockerPruningManager != nil {
		s.dockerPruningManager.SetEngine(eng)
	}
	return nil
}

func (s *Supervisor) startExternalEngineRecovery(engineType string, cfg engine.EngineConfig) {
	s.wg.Add(1)
	go s.runExternalEngineRecovery(engineType, cfg)
}

func (s *Supervisor) runExternalEngineRecovery(engineType string, _ engine.EngineConfig) {
	defer s.wg.Done()
	backoff := engineInitInitialBackoff
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		liveCfg := config.GetInstance()
		if liveCfg.ContainerEngine != engineType {
			logging.LogInfo(moduleName, fmt.Sprintf("%s engine recovery stopped: containerEngine is now %q", engineType, liveCfg.ContainerEngine))
			return
		}
		engConfig := s.liveExternalEngineConfig()
		eng, err := initExternalEngineAttempt(engineType, engConfig)
		if err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("%s engine recovery attempt failed: %v", engineType, err))
			engineInitRetryWait(backoff)
			backoff = advanceEngineInitBackoff(backoff)
			continue
		}
		logging.LogInfo(moduleName, fmt.Sprintf("%s engine recovered; activating container runtime", engineType))
		if err := s.wireContainerEngine(eng); err != nil {
			logging.LogError(moduleName, "Failed to activate recovered container engine", err)
			engineInitRetryWait(backoff)
			backoff = advanceEngineInitBackoff(backoff)
			continue
		}
		s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
			status.SetDaemonStatus(models.ModuleStatusRunning)
		})
		return
	}
}

// Stop stops all modules gracefully in reverse order
func (s *Supervisor) Stop() error {
	logging.LogDebug(moduleName, "Stopping Supervisor")

	runtimestate.GetState().SetAgentPhase("restarting")

	// Cancel context to signal all workers to stop
	if s.cancel != nil {
		s.cancel()
	}

	// Stop Edgelet API monitor
	if s.localAPIMonitorTicker != nil {
		s.localAPIMonitorTicker.Stop()
	}

	// Stop Edgelet API server
	if s.localAPI != nil {
		if err := s.localAPI.Stop(); err != nil {
			logging.LogError(moduleName, "Error shutting down Edgelet API", err)
		}
	}

	// Stop modules in reverse order
	if s.edgeGuardManager != nil {
		if err := s.edgeGuardManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Edge Guard Manager", err)
		}
	}

	if s.dockerPruningManager != nil {
		if err := s.dockerPruningManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Edgelet Pruning Manager", err)
		}
	}

	if s.healthcheckRunner != nil {
		if err := s.healthcheckRunner.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Healthcheck Runner", err)
		}
	}

	if s.gpsManager != nil {
		if err := s.gpsManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping GPS Manager", err)
		}
	}

	if s.processManager != nil {
		processmanager.SetQuiesced(true)
		if s.shouldDrainRuntimeOnControlStop() {
			drainTimeout := time.Duration(s.config.ShutdownDrainTimeout()) * time.Second
			if err := s.processManager.DrainRuntimeForShutdown(drainTimeout); err != nil {
				logging.LogError(moduleName, "Runtime drain during shutdown timed out", err)
			}
		} else {
			logging.LogInfo(moduleName, "Skipping runtime drain on control-plane stop (leave-running policy)")
		}
		if err := s.processManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Process Manager", err)
		}
	}

	if s.fieldAgent != nil {
		if err := s.fieldAgent.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Field Agent", err)
		}
	}

	if s.resourceConsumptionManager != nil {
		if err := s.resourceConsumptionManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Resource Consumption Manager", err)
		}
	}

	if s.networkInterfaceManager != nil {
		if err := s.networkInterfaceManager.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Network Interface Manager", err)
		}
	}

	// Stop embedded containerd when control plane owns it (monolithic path).
	if s.containerdSvc != nil && !s.containerdAttachOnly {
		logging.LogInfo(moduleName, "Stopping embedded containerd")
		s.containerdSvc.Stop()
	} else if s.containerdSvc != nil && s.containerdAttachOnly {
		logging.LogInfo(moduleName, "Leaving data-plane containerd running (runtime split attach-only)")
	}

	if s.statusReporter != nil {
		if err := s.statusReporter.Stop(); err != nil {
			logging.LogError(moduleName, "Error stopping Status Reporter", err)
		}
	}

	// Wait for all workers to finish
	s.wg.Wait()

	// Close SQLite database last
	if err := store.GetInstance().Close(); err != nil {
		logging.LogError(moduleName, "Error closing SQLite database", err)
	}

	logging.LogDebug(moduleName, "Stopped Supervisor")
	return nil
}

// monitorLocalAPI monitors the Edgelet API server and restarts it if it dies
func (s *Supervisor) monitorLocalAPI() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.localAPIMonitorTicker.C:
			logging.LogDebug(moduleName, "Check local API status")
			// Edgelet API runs in a goroutine, so we can't easily check if it's dead
			// In Go, if the server crashes, it will be logged but we can't restart it
			// For now, we just log that we're checking
			logging.LogDebug(moduleName, "Finished checking local API status")
		}
	}
}

func (s *Supervisor) ensureDefaultLocalRegistriesOnStartup(db *store.DB) error {
	if db == nil || db.Conn() == nil {
		return nil
	}
	if err := db.EnsureDefaultLocalRegistries(); err != nil {
		return err
	}
	logging.LogDebug(moduleName, "Default local registries ensured on startup")
	return nil
}

// operationDurationWorker periodically updates the operation duration
func (s *Supervisor) operationDurationWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()

	logging.LogDebug(moduleName, "Start checking operation duration")

	cfg := s.config
	ticker := time.NewTicker(time.Duration(cfg.StatusReportFreqSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.statusReporter.UpdateSupervisorStatus(func(status *models.SupervisorStatus) {
				status.SetOperationDuration(time.Now().UnixMilli())
			})
			logging.LogDebug(moduleName, "Finished checking operation duration")
		}
	}
}

// containerdWatchdog runs periodic containerd socket liveness checks when
// containerEngine=iofog. The runtime is a managed child process; watchdog does
// not run a nested runtime restart loop and instead requests daemon restart.
func containerdWatchdogInterval() time.Duration {
	if processmanager.IsQuiescedForDataPlaneDrain() {
		return 2 * time.Second
	}
	return 30 * time.Second
}

const containerdWatchdogFailureThreshold = 3

// containerdWatchdogShouldSkipEscalation reports whether intentional data-plane
// downtime must not trigger a control-plane restart.
func containerdWatchdogShouldSkipEscalation(attachOnly bool) bool {
	return attachOnly || processmanager.IsQuiescedForDataPlaneDrain()
}

// containerdWatchdogShouldEscalateUnhealthy reports whether repeated socket check
// failures should request a control-plane restart.
func containerdWatchdogShouldEscalateUnhealthy(consecutiveFailures int) bool {
	if processmanager.IsQuiescedForDataPlaneDrain() {
		return false
	}
	return consecutiveFailures >= containerdWatchdogFailureThreshold
}

func (s *Supervisor) containerdWatchdog() {
	defer s.wg.Done()

	interval := containerdWatchdogInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutiveFailures := 0

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.containerdSvc == nil {
				return
			}
			if s.containerdSvc.IsHealthy() {
				consecutiveFailures = 0
				processmanager.TryResumeReconcileAfterDataPlaneEngineReady()

				nextInterval := containerdWatchdogInterval()
				if nextInterval != interval {
					ticker.Stop()
					interval = nextInterval
					ticker = time.NewTicker(interval)
				}
				continue
			}
			if containerdWatchdogShouldSkipEscalation(s.containerdAttachOnly) {
				// Runtime split: data-plane unit owns containerd lifecycle; socket
				// downtime during edgelet-containerd stop/start must not restart control plane.
				logging.LogDebug(moduleName, "containerd watchdog skipping escalation (attach-only or data-plane quiesce)")
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			logging.LogWarn(moduleName, fmt.Sprintf(
				"Embedded containerd socket is unresponsive (%d/%d consecutive checks)",
				consecutiveFailures, containerdWatchdogFailureThreshold,
			))
			if containerdWatchdogShouldEscalateUnhealthy(consecutiveFailures) {
				requestDaemonRestart(
					"Embedded containerd is persistently unhealthy; requesting daemon restart via SIGTERM",
					errors.New("containerd watchdog reached failure threshold"),
				)
				return
			}
		}
	}
}

// GetName returns the supervisor module name
func (s *Supervisor) GetName() string {
	return moduleName
}

// GetModuleIndex returns the supervisor module index
func (s *Supervisor) GetModuleIndex() int {
	return utils.ProcessManager
}

// ReloadFromDisk performs a full hot reload: read disk, validate, update logger, notify modules.
func (s *Supervisor) ReloadFromDisk() error {
	return config.FullReload(config.ReloadHooks{
		ConfigPath:    utils.ConfigYAMLPath,
		BeginReload:   s.BeginConfigReload,
		NotifyModules: s.ReloadConfig,
	})
}

// BeginConfigReload snapshots engine connection settings before LoadConfig replaces them.
func (s *Supervisor) BeginConfigReload() {
	s.pendingReloadCtx = s.captureReloadEngineContext()
}

// ReloadConfig notifies all modules that configuration has been reloaded.
func (s *Supervisor) ReloadConfig() error {
	reloadCtx := s.pendingReloadCtx
	s.pendingReloadCtx = nil
	if reloadCtx == nil {
		reloadCtx = s.captureReloadEngineContext()
	}
	return s.ReloadConfigWithContext(reloadCtx)
}

// reloadHotConfig applies hot config keys to running modules.
func (s *Supervisor) reloadHotConfig() error {
	logging.LogInfo(moduleName, "Start updating agent configurations")

	// Notify all modules in the same order
	if s.fieldAgent != nil {
		if err := s.fieldAgent.Update(); err != nil {
			logging.LogError(moduleName, "Failed to update FieldAgent", err)
		}
	}

	if s.processManager != nil {
		s.processManager.Update()
	}

	if s.resourceConsumptionManager != nil {
		s.resourceConsumptionManager.InstanceConfigUpdated()
	}

	if s.networkInterfaceManager != nil {
		// Run network update asynchronously to avoid blocking
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.LogError(moduleName, "Panic recovered", fmt.Errorf("%v", r))
				}
			}()
			if err := s.networkInterfaceManager.UpdateNetworkInterface(); err != nil {
				logging.LogError(moduleName, "Failed to update network interface", err)
			}
		}()
	}

	if s.dockerPruningManager != nil {
		s.dockerPruningManager.ChangePruningFreqInterval()
	}

	// Update Edgelet API (was missing)
	if s.localAPI != nil {
		s.localAPI.Update()
	}

	if s.edgeGuardManager != nil {
		s.edgeGuardManager.InstanceConfigUpdated()
	}

	if s.gpsManager != nil {
		s.gpsManager.InstanceConfigUpdated()
	}

	logging.LogInfo(moduleName, "Finished updating agent configurations")
	return nil
}

func (s *Supervisor) shouldDrainRuntimeOnControlStop() bool {
	if s.containerdAttachOnly {
		return false
	}
	return !config.GetInstance().LeaveRunningOnControlStop()
}
