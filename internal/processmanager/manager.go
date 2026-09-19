package processmanager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelcatalog"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const (
	ProcessManagerModuleName  = "Process Manager"
	shutdownDrainBaseTimeout  = 30 * time.Second
	shutdownDrainPerContainer = 10 * time.Second
	shutdownDrainMaxTimeout   = 180 * time.Second
	shutdownDrainMaxWorkers   = 8
	shutdownDrainPollInterval = 1 * time.Second
	localReconcileMaxFailures = 5
	maxTaskRetries            = 5
)

// ProcessManager manages container lifecycle via a ContainerEngine.
type ProcessManager struct {
	engine                       engine.ContainerEngine
	engineMu                     sync.RWMutex
	engineName                   string
	microserviceManager          MicroserviceManagerInterface
	containerManager             *ContainerManager
	taskQueue                    *TaskQueue
	updateChan                   chan struct{}
	ctx                          context.Context
	cancel                       context.CancelFunc
	wg                           sync.WaitGroup
	logger                       *logging.ModuleLogger
	startMicroserviceFn          func(microserviceUUID string) error
	removeContainerByIDFn        func(containerID string) error
	launchLocalDeploymentFn      func(item *models.LocalDeployedMicroservice, now int64)
	launchControlPlaneFn         func(item *models.ControlPlaneDeployment, now int64)
	recreateLocalDeploymentFn    func(item *models.LocalDeployedMicroservice, pullImage bool, now int64) error
	recreateControlPlaneFn       func(item *models.ControlPlaneDeployment, pullImage bool, now int64) error
	getContainerStatusFn         func(containerID, microserviceUUID string) (*models.MicroserviceStatus, error)
	reconcileMonitorTick         uint64
	localLaunchLocks             sync.Map // microservice UUID -> *sync.Mutex
	catalogDiskDirectory         string
	controlPlanePullOnRecreate   bool
	controlPlanePullOnRecreateMu sync.Mutex
	execRegistry                 *ExecSessionRegistry
	watchdogLocalModelsMu        sync.Mutex
	watchdogLocalModelsFn        func()
}

// LocalDeployProgressCallback reports local deployment runtime stage transitions.
type LocalDeployProgressCallback func(stage string, message string)

func emitLocalDeployProgress(cb LocalDeployProgressCallback, stage, message string) {
	if cb != nil {
		cb(stage, message)
	}
}

var (
	instance *ProcessManager
	once     sync.Once
)

// GetInstance returns the singleton ProcessManager instance
func GetInstance() *ProcessManager {
	once.Do(func() {
		instance = &ProcessManager{
			logger:       logging.NewModuleLogger(ProcessManagerModuleName),
			updateChan:   make(chan struct{}, 1),
			execRegistry: NewExecSessionRegistry(),
		}
	})
	return instance
}

// SetInstanceForTest replaces the ProcessManager singleton for tests and returns a restore func.
func SetInstanceForTest(pm *ProcessManager) func() {
	prev := instance
	instance = pm
	return func() { instance = prev }
}

// Start starts the ProcessManager with a given container engine.
func (pm *ProcessManager) Start(eng engine.ContainerEngine, microserviceManager MicroserviceManagerInterface) error {
	pm.logger.Info("Starting Process Manager")

	pm.engineMu.Lock()
	pm.engine = eng
	pm.engineMu.Unlock()
	pm.microserviceManager = microserviceManager
	if cfg := config.GetInstance(); cfg != nil {
		pm.engineName = cfg.ContainerEngine
	}
	pm.containerManager = NewContainerManager(eng, microserviceManager, pm.engineName)
	pm.containerManager.catalogDiskDirectory = pm.catalogDiskDirectory

	pm.ctx, pm.cancel = context.WithCancel(context.Background())
	pm.taskQueue = NewTaskQueue(100)

	pm.wg.Add(2)
	go pm.containersMonitor()
	go pm.checkTasks()

	pm.logger.Info("Process Manager started")
	return nil
}

// Stop stops the ProcessManager
func (pm *ProcessManager) Stop() error {
	pm.logger.Info("Stopping Process Manager")

	if pm.cancel != nil {
		pm.cancel()
	}

	if pm.taskQueue != nil {
		pm.taskQueue.Close()
	}

	pm.wg.Wait()
	pm.logger.Info("Process Manager stopped")
	return nil
}

// DrainRuntimeForShutdown best-effort drains runtime tasks before manager teardown.
// It is used during daemon shutdown so containerd can stop without lingering shims.
func (pm *ProcessManager) DrainRuntimeForShutdown(timeout time.Duration) error {
	if pm.engine == nil {
		return nil
	}
	startedAt := time.Now()
	initialRuntimeIDs, err := pm.runtimeWorkloadContainerIDs()
	if err != nil {
		return fmt.Errorf("list running containers during shutdown drain: %w", err)
	}
	targetSet := make(map[string]struct{}, len(initialRuntimeIDs))
	for _, id := range initialRuntimeIDs {
		targetSet[id] = struct{}{}
	}
	if timeout <= 0 {
		timeout = adaptiveShutdownDrainTimeout(len(initialRuntimeIDs))
	}
	deadline := startedAt.Add(timeout)

	if len(initialRuntimeIDs) == 0 {
		pm.emitShutdownDrain("shutdown runtime drain complete: no running workload containers", runtimeops.LevelInfo, "", map[string]any{
			"result":         runtimeops.ResultOK,
			"targetCount":    0,
			"stoppedCount":   0,
			"remainingCount": 0,
			"elapsedMs":      0,
		})
		return nil
	}

	for {
		runtimeIDs, err := pm.runtimeWorkloadContainerIDs()
		if err != nil {
			return fmt.Errorf("list running containers during shutdown drain: %w", err)
		}

		stoppedCount := countStoppedTargets(targetSet, runtimeIDs)
		elapsedMs := time.Since(startedAt).Milliseconds()
		if len(runtimeIDs) == 0 {
			pm.emitShutdownDrain("shutdown runtime drain complete: no running workload containers", runtimeops.LevelInfo, "", map[string]any{
				"result":         runtimeops.ResultOK,
				"targetCount":    len(targetSet),
				"stoppedCount":   stoppedCount,
				"remainingCount": 0,
				"elapsedMs":      elapsedMs,
			})
			return nil
		}

		if time.Now().After(deadline) {
			remaining := strings.Join(runtimeIDs, ",")
			pm.emitShutdownDrain("shutdown runtime drain timed out", runtimeops.LevelError, runtimeops.ReasonShutdownDrainTimeout, map[string]any{
				"remainingContainerIds": remaining,
				"result":                runtimeops.ResultFailed,
				"targetCount":           len(targetSet),
				"stoppedCount":          stoppedCount,
				"remainingCount":        len(runtimeIDs),
				"elapsedMs":             elapsedMs,
			})
			return fmt.Errorf(
				"timed out draining runtime containers after %s; remaining container IDs: %s",
				timeout,
				remaining,
			)
		}

		pm.stopRuntimeContainersConcurrently(runtimeIDs)
		time.Sleep(shutdownDrainPollInterval)
	}
}

// DrainRuntimeForDataPlaneShutdown tears down labeled workload containers from the
// runtime before the embedded data-plane stops. Deploy specs in SQLite are preserved;
// ephemeral runtime refs are cleared so reconcile recreates sandboxes after the socket
// returns (BR-24-I1, BR-24-I3).
func (pm *ProcessManager) DrainRuntimeForDataPlaneShutdown(timeout time.Duration) error {
	if pm.engine == nil {
		return nil
	}
	initialRuntimeIDs, err := pm.runtimeWorkloadContainerIDsWithRetry(3 * time.Second)
	if err != nil {
		pm.clearWorkloadRuntimeRefsForDataPlaneDrainRecovery()
		// Socket may already be down during edgelet-containerd SIGTERM; clear refs so
		// reconcile recreates workloads after the data plane comes back (BR-24-E5).
		pm.logger.Warnf("data-plane drain: runtime list unavailable (%v); cleared workload refs for recovery", err)
		return nil
	}
	drainErr := pm.teardownRuntimeWorkloadForDataPlaneShutdown(initialRuntimeIDs, timeout)
	pm.clearWorkloadRuntimeRefsForDataPlaneDrainRecovery()
	return drainErr
}

func (pm *ProcessManager) runtimeWorkloadContainerIDsWithRetry(budget time.Duration) ([]string, error) {
	if budget <= 0 {
		budget = 10 * time.Second
	}
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		ids, err := pm.runtimeWorkloadContainerIDs()
		if err == nil {
			return ids, nil
		}
		lastErr = err
		if !isRetryableRuntimeListError(err) || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(shutdownDrainPollInterval)
	}
	return nil, lastErr
}

func isRetryableRuntimeListError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection error") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "not ready")
}

func (pm *ProcessManager) clearWorkloadRuntimeRefsForDataPlaneDrainRecovery() {
	if pm == nil {
		return
	}
	if items, err := store.GetInstance().ListLocalWorkloads(); err == nil {
		for _, item := range items {
			if item == nil || item.DesiredState != "running" {
				continue
			}
			pm.clearLocalWorkloadRuntimeRef(item.LocalUUID)
		}
	}
	_ = store.GetInstance().ClearRuntimeContainerRefs("")
	_ = store.GetInstance().ClearControllerMicroserviceRuntimeFields()
}

func (pm *ProcessManager) clearLocalWorkloadRuntimeRef(localUUID string) {
	item, err := store.GetInstance().GetLocalWorkload(localUUID)
	if err != nil || item == nil {
		return
	}
	if item.DesiredState != "running" {
		return
	}
	item.ContainerID = ""
	item.RuntimeState = "pending"
	item.State = item.RuntimeState
	item.FailureCount = 0
	_ = persistLocalWorkloadIfPresent(item)
}

func adaptiveShutdownDrainTimeout(containerCount int) time.Duration {
	if containerCount < 0 {
		containerCount = 0
	}
	timeout := shutdownDrainBaseTimeout + (time.Duration(containerCount) * shutdownDrainPerContainer)
	if timeout > shutdownDrainMaxTimeout {
		return shutdownDrainMaxTimeout
	}
	return timeout
}

func shutdownDrainWorkerCount(containerCount int) int {
	if containerCount <= 0 {
		return 1
	}
	if containerCount < shutdownDrainMaxWorkers {
		return containerCount
	}
	return shutdownDrainMaxWorkers
}

func (pm *ProcessManager) runtimeWorkloadContainerIDs() ([]string, error) {
	running, err := pm.engine.GetRunningContainers()
	if err != nil {
		return nil, err
	}
	idsSet := make(map[string]struct{}, len(running))
	for _, container := range running {
		if msUUID := pm.engine.GetContainerMicroserviceUUID(container); msUUID != "" {
			if strings.TrimSpace(container.ID) == "" {
				continue
			}
			idsSet[container.ID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func countStoppedTargets(targetSet map[string]struct{}, remaining []string) int {
	if len(targetSet) == 0 {
		return 0
	}
	remainingSet := make(map[string]struct{}, len(remaining))
	for _, id := range remaining {
		remainingSet[id] = struct{}{}
	}
	stopped := 0
	for id := range targetSet {
		if _, ok := remainingSet[id]; !ok {
			stopped++
		}
	}
	return stopped
}

func (pm *ProcessManager) stopRuntimeContainersConcurrently(containerIDs []string) {
	if len(containerIDs) == 0 {
		return
	}
	workerCount := shutdownDrainWorkerCount(len(containerIDs))
	jobs := make(chan string)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for containerID := range jobs {
			pm.stopOneRuntimeContainerForDrain(containerID)
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	for _, id := range containerIDs {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
}

func (pm *ProcessManager) stopOneRuntimeContainerForDrain(containerID string) {
	msUUID := ""
	if c, err := pm.engine.GetContainerByID(containerID); err == nil && c != nil {
		msUUID = pm.engine.GetContainerMicroserviceUUID(*c)
	}
	stopStart := time.Now()
	if err := pm.engine.StopContainer(containerID); err != nil {
		pm.emitShutdownDrain("shutdown drain: graceful stop failed", runtimeops.LevelWarn, runtimeops.ReasonStopFailed, map[string]any{
			"containerId": containerID,
			"msUUID":      msUUID,
			"error":       err.Error(),
			"durationMs":  time.Since(stopStart).Milliseconds(),
		})
		if killErr := pm.engine.KillContainer(containerID); killErr != nil {
			pm.emitShutdownDrain("shutdown drain: force stop failed", runtimeops.LevelWarn, runtimeops.ReasonStopFailed, map[string]any{
				"containerId": containerID,
				"msUUID":      msUUID,
				"error":       killErr.Error(),
			})
		}
		return
	}
	pm.emitShutdownDrain("shutdown drain: container stopped", runtimeops.LevelInfo, "", map[string]any{
		"containerId": containerID,
		"msUUID":      msUUID,
		"result":      runtimeops.ResultOK,
		"durationMs":  time.Since(stopStart).Milliseconds(),
	})
}

// StopRunningMicroservices stops all running microservices matching the iofogUuid.
// When withCleanup=true (deprovision flow), containers are also removed along with their volumes.
func (pm *ProcessManager) StopRunningMicroservices(iofogUUID string, withCleanup bool) error {
	return pm.StopRunningMicroservicesWithScope(iofogUUID, withCleanup, true)
}

// StopRunningMicroservicesWithScope stops running microservices matching iofogUuid.
// includeLocal=false preserves local-scope workloads during deprovision cleanup.
func (pm *ProcessManager) StopRunningMicroservicesWithScope(iofogUUID string, withCleanup bool, includeLocal bool) error {
	pm.logger.Info("Stop running Microservices")

	// Get all containers regardless of state for complete cleanup
	allContainers, err := pm.engine.GetAllContainers()
	if err != nil {
		pm.logger.Errorf("Error getting all containers: %v", err)
		return err
	}

	cfg := config.GetInstance()
	runningMicroserviceUuids := make([]string, 0)

	for _, container := range allContainers {
		msUUID := workloadmeta.MicroserviceUIDFromLabels(container.Labels)
		if msUUID == "" {
			continue
		}

		if !workloadmeta.IsManagedByIofog(container.Labels) {
			continue
		}
		if !includeLocal && strings.EqualFold(strings.TrimSpace(container.Labels[workloadmeta.LabelScope]), workloadmeta.ScopeLocal) {
			continue
		}
		containerIOFogUUID := strings.TrimSpace(container.Labels[workloadmeta.LabelNodeUID])
		if (containerIOFogUUID != "" && containerIOFogUUID == iofogUUID) || cfg.WatchdogEnabled {
			runningMicroserviceUuids = append(runningMicroserviceUuids, msUUID)
		}
	}

	// Stop (and optionally remove) each matching container.
	// removeImage=false: deprovision path (no image removal).
	for _, msUUID := range runningMicroserviceUuids {
		if withCleanup {
			if err := pm.containerManager.RemoveContainerByMicroserviceUUID(pm.operationContext(msUUID), msUUID, true, false); err != nil {
				pm.logger.Warnf("Error removing microservice %s: %v", msUUID, err)
			}
		} else {
			if err := pm.containerManager.StopContainerByMicroserviceUUID(pm.operationContext(msUUID), msUUID); err != nil {
				pm.logger.Warnf("Error stopping microservice %s: %v", msUUID, err)
			}
		}
	}

	pm.logger.Info("Stopped running Microservices")
	return nil
}

// GetName returns the module name
func (pm *ProcessManager) GetName() string {
	return ProcessManagerModuleName
}

// GetModuleIndex returns the module index
func (pm *ProcessManager) GetModuleIndex() int {
	return utils.ProcessManager
}

// GetLatestMicroservices returns the latest microservices from the microservice manager
func (pm *ProcessManager) GetLatestMicroservices() []*models.Microservice {
	if pm.microserviceManager == nil {
		return []*models.Microservice{}
	}
	return pm.microserviceManager.GetLatestMicroservices()
}

// ConfiguredMicroserviceImages returns image names that scheduled prune must keep:
// controller-managed desired microservices plus local-deployed workloads that are
// not gone. Stopped or restarting local deploys stay in the keep-set so their
// images are not deleted before reconcile recreates the container.
func (pm *ProcessManager) ConfiguredMicroserviceImages() []string {
	seen := make(map[string]struct{})
	names := make([]string, 0)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, ms := range pm.GetLatestMicroservices() {
		if ms != nil {
			add(ms.ImageName)
		}
	}
	items, err := store.GetInstance().ListLocalWorkloads()
	if err != nil {
		return names
	}
	for _, item := range items {
		if item == nil || item.IsGone() {
			continue
		}
		add(item.ImageName)
	}
	return names
}

// SetWatchdogLocalModelsCallback runs when watchdog is on so local Model rows
// and trees are removed (fleet-desired models are kept).
func (pm *ProcessManager) SetWatchdogLocalModelsCallback(fn func()) {
	if pm == nil {
		return
	}
	pm.watchdogLocalModelsMu.Lock()
	defer pm.watchdogLocalModelsMu.Unlock()
	pm.watchdogLocalModelsFn = fn
}

func (pm *ProcessManager) cleanupLocalModelsForWatchdog() {
	if pm == nil || !LocalWorkloadsOutOfScope(config.GetInstance().WatchdogEnabled) {
		return
	}
	pm.watchdogLocalModelsMu.Lock()
	fn := pm.watchdogLocalModelsFn
	pm.watchdogLocalModelsMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Update notifies the ProcessManager of changes
// updates registries and notifies monitor thread
func (pm *ProcessManager) Update() {
	pm.logger.Debug("updates registries list according to the last changes")

	// Update registries status
	// Remove registries that no longer exist
	if pm.microserviceManager != nil {
		status := statusreporter.GetInstance().GetProcessManagerStatus()
		if status != nil && status.RegistriesStatus != nil {
			// Filter out registries that don't exist in microservice manager
			// This is a simplified version
			pm.logger.Debug("Updated registries status")
		}
	}

	// Notify the monitor thread to restart immediately instead of waiting
	pm.notifyMonitorThread()
}

// notifyMonitorThread wakes up the monitor thread immediately
func (pm *ProcessManager) notifyMonitorThread() {
	if pm == nil || pm.updateChan == nil {
		return
	}
	select {
	case pm.updateChan <- struct{}{}:
	default:
		// Channel full, already notified
	}
}

// containersMonitor monitors containers and handles lifecycle
// uses wait/notify pattern
func (pm *ProcessManager) containersMonitor() {
	defer pm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(ProcessManagerModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()
	cfg := config.GetInstance()
	interval := time.Duration(cfg.MonitorContainersStatusFreqSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			// Continue to monitoring
		case <-pm.updateChan:
			// Continue to monitoring immediately
		}

		if IsQuiesced() {
			pm.logger.Debug("Reconcile quiesced (pending engine restart)")
			continue
		}

		pm.logger.Debug("Start Monitoring containers")
		tickStart := time.Now()
		reconcileStats := &reconcileCycleStats{}

		pm.reconcileControlPlane()
		pm.handleLatestMicroservices(reconcileStats)
		pm.enqueueDueRetries()
		pm.reconcileLocalDeployments()
		pm.deleteRemainingMicroservices()
		pm.cleanupLocalModelsForWatchdog()
		pm.pruneStaleProcessManagerStatuses()
		pm.updateRunningMicroservicesCount()
		pm.updateCurrentMicroservices()

		desiredCount := 0
		if pm.microserviceManager != nil {
			desiredCount = len(pm.microserviceManager.GetLatestMicroservices())
		}
		runningCount := 0
		if pmStatus := statusreporter.GetInstance().GetProcessManagerStatus(); pmStatus != nil {
			runningCount = pmStatus.RunningMicroservicesCount
		}
		pm.reconcileMonitorTick++
		if shouldEmitReconcileCycle(reconcileStats, pm.reconcileMonitorTick, cfg.LogReconcileCycleEveryNTicks) {
			pm.emitReconcileCycle(tickStart, reconcileStats, desiredCount, runningCount)
		}

		pm.logger.Debug("Finished Monitoring containers")
	}
}

func (pm *ProcessManager) reconcileLocalDeployments() {
	if LocalWorkloadsOutOfScope(config.GetInstance().WatchdogEnabled) {
		return
	}
	items, err := store.GetInstance().ListLocalWorkloads()
	if err != nil {
		pm.logger.Warnf("local reconcile list deployments failed: %v", err)
		return
	}
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.LocalUUID) == "" {
			continue
		}
		pm.reconcileOneLocalDeployment(item)
	}
}

func (pm *ProcessManager) pruneStaleProcessManagerStatuses() {
	if pm.microserviceManager == nil || pm.engine == nil {
		pm.logger.Debug("process-manager status prune skipped: dependencies not initialized")
		return
	}

	managedUUIDs := make(map[string]struct{})
	for _, ms := range pm.microserviceManager.GetLatestMicroservices() {
		if ms == nil {
			continue
		}
		uuid := strings.TrimSpace(ms.MicroserviceUUID)
		if uuid != "" {
			managedUUIDs[uuid] = struct{}{}
		}
	}

	localItems, err := store.GetInstance().ListLocalWorkloads()
	if err != nil {
		pm.logger.Warnf("process-manager status prune skipped: local deployment list unavailable err=%v", err)
		return
	}
	localUUIDs := make(map[string]struct{}, len(localItems))
	for _, item := range localItems {
		if item == nil {
			continue
		}
		uuid := strings.TrimSpace(item.LocalUUID)
		if uuid != "" {
			localUUIDs[uuid] = struct{}{}
		}
	}

	runtimeContainers, err := pm.engine.GetAllContainers()
	if err != nil {
		pm.logger.Warnf("process-manager status prune skipped: runtime container list unavailable err=%v", err)
		return
	}
	runtimeUUIDs := make(map[string]struct{}, len(runtimeContainers))
	for _, container := range runtimeContainers {
		uuid := strings.TrimSpace(workloadmeta.MicroserviceUIDFromLabels(container.Labels))
		if uuid == "" {
			uuid = strings.TrimSpace(pm.engine.GetContainerMicroserviceUUID(container))
		}
		if uuid != "" {
			runtimeUUIDs[uuid] = struct{}{}
		}
	}

	totalSeen := 0
	pruned := 0
	droppedInvalid := 0
	keptManaged := 0
	keptLocal := 0
	keptRuntimeOnly := 0
	statusreporter.GetInstance().PruneProcessManagerStatus(func(uuid string, status *models.MicroserviceStatus) bool {
		totalSeen++
		trimmedUUID := strings.TrimSpace(uuid)
		if trimmedUUID == "" || status == nil {
			droppedInvalid++
			pruned++
			return true
		}
		if _, ok := managedUUIDs[trimmedUUID]; ok {
			keptManaged++
			return false
		}
		if _, ok := localUUIDs[trimmedUUID]; ok {
			keptLocal++
			return false
		}
		if _, ok := runtimeUUIDs[trimmedUUID]; ok {
			keptRuntimeOnly++
			return false
		}
		// Keep terminal tombstones until FieldAgent posts status and calls
		// RemoveNotRunningMicroserviceStatus after a successful controller PUT.
		if isProcessManagerStatusReportTombstone(status) {
			return false
		}
		pruned++
		return true
	})
	pm.logger.Debugf(
		"process-manager status prune total=%d pruned=%d droppedInvalid=%d keptManaged=%d keptLocal=%d keptRuntimeOnly=%d",
		totalSeen,
		pruned,
		droppedInvalid,
		keptManaged,
		keptLocal,
		keptRuntimeOnly,
	)
}

func loadLocalWorkload(uuid string) *models.LocalDeployedMicroservice {
	item, err := store.GetInstance().GetLocalWorkload(strings.TrimSpace(uuid))
	if err != nil || item == nil {
		return nil
	}
	return item
}

func persistLocalWorkloadIfPresent(item *models.LocalDeployedMicroservice) error {
	if item == nil || strings.TrimSpace(item.LocalUUID) == "" {
		return nil
	}
	if loadLocalWorkload(item.LocalUUID) == nil {
		return nil
	}
	return store.GetInstance().UpsertLocalWorkload(item)
}

func (pm *ProcessManager) reconcileOneLocalDeployment(item *models.LocalDeployedMicroservice) {
	if item == nil {
		return
	}
	current := loadLocalWorkload(item.LocalUUID)
	if current == nil {
		return
	}
	item = current
	nowSec := time.Now().Unix()
	item.NormalizeDefaults()
	item.LastReconcileAt = nowSec

	desired := strings.ToLower(strings.TrimSpace(item.DesiredState))
	if desired == "" {
		desired = "running"
	}

	var container *engine.Container
	var err error
	if pm.containerManager != nil {
		container, err = pm.containerManager.GetContainerForMicroservice(item.LocalUUID)
	}
	if err != nil {
		item.LastError = err.Error()
		item.RuntimeState = "unknown"
		_ = persistLocalWorkloadIfPresent(item)
		return
	}

	switch desired {
	case "stopped":
		pm.reconcileLocalDesiredStopped(item, container, nowSec)
	case "deleted":
		pm.reconcileLocalDesiredDeleted(item, container, nowSec)
	default:
		pm.reconcileLocalDesiredRunning(item, container, nowSec)
	}
}

func (pm *ProcessManager) reconcileLocalDesiredDeleted(item *models.LocalDeployedMicroservice, container *engine.Container, now int64) {
	if loadLocalWorkload(item.LocalUUID) == nil {
		return
	}
	item.LastTransitionAt = now
	item.ObservedGeneration = item.Generation
	if item.DeletedAt == nil {
		ts := now
		item.DeletedAt = &ts
	}
	if container != nil && strings.TrimSpace(container.ID) != "" {
		if err := pm.removeLocalContainerByID(container.ID); err != nil {
			item.LastError = err.Error()
			item.RuntimeState = "deleting"
			item.State = item.RuntimeState
			_ = persistLocalWorkloadIfPresent(item)
			return
		}
	}
	pm.releaseCatalog(item.LocalUUID)
	_ = store.GetInstance().DeleteLocalWorkload(item.LocalUUID)
}

func (pm *ProcessManager) reconcileLocalDesiredStopped(item *models.LocalDeployedMicroservice, container *engine.Container, now int64) {
	item.ObservedGeneration = item.Generation
	item.LastTransitionAt = now
	if container != nil {
		if err := pm.StopMicroservice(item.LocalUUID); err != nil {
			item.LastError = err.Error()
			item.RuntimeState = "stopping"
			item.State = item.RuntimeState
			_ = persistLocalWorkloadIfPresent(item)
			return
		}
	}
	item.RuntimeState = "stopped"
	item.State = item.RuntimeState
	item.FailureCount = 0
	_ = persistLocalWorkloadIfPresent(item)
}

func (pm *ProcessManager) reconcileLocalDesiredRunning(item *models.LocalDeployedMicroservice, container *engine.Container, now int64) {
	if container == nil {
		if localDeploymentLaunchInFlight(item, now) {
			pm.logger.Debugf(
				"Skipping local reconcile launch for %s: apply in-flight (generation=%d observed=%d)",
				item.LocalUUID,
				item.Generation,
				item.ObservedGeneration,
			)
			return
		}
		pm.launchLocalDeploymentWithHook(item, now)
		return
	}

	status, err := pm.getLocalContainerStatus(container.ID, item.LocalUUID)
	if err != nil {
		item.RuntimeState = "unknown"
		item.State = item.RuntimeState
		item.LastError = err.Error()
		item.LastTransitionAt = now
		_ = persistLocalWorkloadIfPresent(item)
		return
	}

	runtime := strings.ToLower(string(status.Status))
	item.RuntimeState = runtime
	item.State = runtime
	item.ContainerID = container.ID
	item.LastTransitionAt = now

	switch runtime {
	case "running":
		if pm.refreshLocalCatalogIfNeeded(item, now) {
			return
		}
		item.ObservedGeneration = item.Generation
		item.FailureCount = 0
	case "created":
		if err := pm.startLocalMicroservice(item.LocalUUID); err != nil {
			if nr, ok := engine.IsNonRestartableContainerError(err); ok {
				pm.logger.Warnf(
					"local reconcile created start failed with non-restartable terminal state localUUID=%s containerID=%s reason=%s exitCode=%d criMessage=%q decision=recreate",
					item.LocalUUID,
					container.ID,
					nr.Reason,
					nr.ExitCode,
					nr.Message,
				)
				if recErr := pm.recreateLocalDeployment(item, false, now); recErr != nil {
					_ = recErr
				}
				return
			}
			pm.bumpLocalFailure(item, err, "created")
		} else {
			item.RuntimeState = "running"
			item.State = item.RuntimeState
			item.ObservedGeneration = item.Generation
			item.FailureCount = 0
		}
	case "exiting":
		if force, reason, exitCode := shouldForceRecreateFromStatus(status); force {
			pm.logger.Warnf(
				"local reconcile exiting with non-restartable terminal state localUUID=%s containerID=%s reason=%s exitCode=%d decision=recreate",
				item.LocalUUID,
				container.ID,
				reason,
				exitCode,
			)
			if recErr := pm.recreateLocalDeployment(item, false, now); recErr == nil {
				return
			}
		}
		if err := pm.startLocalMicroservice(item.LocalUUID); err != nil {
			if nr, ok := engine.IsNonRestartableContainerError(err); ok {
				pm.logger.Warnf(
					"local reconcile exiting start failed with non-restartable terminal state localUUID=%s containerID=%s reason=%s exitCode=%d criMessage=%q decision=recreate",
					item.LocalUUID,
					container.ID,
					nr.Reason,
					nr.ExitCode,
					nr.Message,
				)
				if recErr := pm.recreateLocalDeployment(item, false, now); recErr == nil {
					return
				}
			}
			pm.bumpLocalFailure(item, err, "exiting")
		} else {
			item.RuntimeState = "running"
			item.State = item.RuntimeState
			item.ObservedGeneration = item.Generation
			item.FailureCount = 0
		}
		_ = persistLocalWorkloadIfPresent(item)
		return
	case "failed", "unknown":
		pm.bumpLocalFailure(item, fmt.Errorf("runtime state=%s", runtime), runtime)
	default:
		if status.ErrorMessage != nil {
			item.LastError = strings.TrimSpace(*status.ErrorMessage)
		}
	}

	_ = persistLocalWorkloadIfPresent(item)
}

func (pm *ProcessManager) bumpLocalFailure(item *models.LocalDeployedMicroservice, cause error, runtime string) {
	item.FailureCount++
	item.RestartCount++
	if cause != nil {
		item.LastError = cause.Error()
	}
	if item.FailureCount >= localReconcileMaxFailures {
		item.RuntimeState = "stuck_in_restart"
		item.State = item.RuntimeState
		return
	}
	item.RuntimeState = runtime
	item.State = item.RuntimeState
}

func (pm *ProcessManager) launchLocalDeployment(item *models.LocalDeployedMicroservice, now int64) {
	doc, err := decodeLocalDeployManifest(item.ManifestYAML)
	if err != nil {
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		pm.bumpLocalFailure(item, err, item.RuntimeState)
		item.LastTransitionAt = now
		_ = persistLocalWorkloadIfPresent(item)
		return
	}

	image := doc.ManifestImage()
	localMS := models.BuildMicroserviceFromLocalManifest(doc, item.LocalUUID, image)
	registry := models.NewRegistry(2, "from_cache", true, "", "", "")
	if doc.Spec.Registry != nil {
		if reg, err := store.GetInstance().GetLocalRegistry(*doc.Spec.Registry); err == nil && reg != nil {
			registry = reg
			localMS.RegistryID = reg.ID
		}
	}

	item.RuntimeState = "starting"
	item.State = item.RuntimeState
	item.LastStartAttemptAt = now
	item.LastTransitionAt = now
	_ = persistLocalWorkloadIfPresent(item)

	hostIP := network.GetInstance().GetCurrentIPAddress()
	containerID, err := pm.LaunchLocalMicroservice(localMS, registry, hostIP)
	if err != nil {
		if errors.Is(err, modelcatalog.ErrWaiting) {
			item.RuntimeState = "queued"
			item.State = item.RuntimeState
			item.LastError = models.CatalogWaitingMessage
			item.LastTransitionAt = now
			_ = persistLocalWorkloadIfPresent(item)
			return
		}
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		pm.bumpLocalFailure(item, err, item.RuntimeState)
		item.LastTransitionAt = now
		_ = persistLocalWorkloadIfPresent(item)
		return
	}
	item.ContainerID = containerID
	item.ImageName = image
	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.ObservedGeneration = item.Generation
	item.FailureCount = 0
	item.LastTransitionAt = now
	_ = persistLocalWorkloadIfPresent(item)
}

func (pm *ProcessManager) startLocalMicroservice(microserviceUUID string) error {
	if pm.startMicroserviceFn != nil {
		return pm.startMicroserviceFn(microserviceUUID)
	}
	return pm.StartMicroservice(microserviceUUID)
}

func (pm *ProcessManager) removeLocalContainerByID(containerID string) error {
	if pm.removeContainerByIDFn != nil {
		return pm.removeContainerByIDFn(containerID)
	}
	return pm.RemoveContainerByContainerID(containerID)
}

func (pm *ProcessManager) launchLocalDeploymentWithHook(item *models.LocalDeployedMicroservice, now int64) {
	if pm.launchLocalDeploymentFn != nil {
		pm.launchLocalDeploymentFn(item, now)
		return
	}
	pm.launchLocalDeployment(item, now)
}

func (pm *ProcessManager) getLocalContainerStatus(containerID, microserviceUUID string) (*models.MicroserviceStatus, error) {
	if pm.getContainerStatusFn != nil {
		return pm.getContainerStatusFn(containerID, microserviceUUID)
	}
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.GetContainerStatus(containerID, microserviceUUID)
}

// checkTasks is a long-running goroutine that drains the task queue.
// It blocks on each Get() call and exits cleanly when the context is canceled.
func (pm *ProcessManager) checkTasks() {
	defer pm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(ProcessManagerModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()
	for {
		task, ok := pm.taskQueue.Get(pm.ctx)
		if !ok {
			// Context canceled — stop processing.
			return
		}

		if err := pm.executeTask(task); err != nil {
			pm.logger.Errorf("Error executing task %s for microservice %s: %v", task.Action, task.MicroserviceUUID, err)
			if task.Retries < maxTaskRetries {
				pm.retryTask(task)
			} else {
				pm.emitTaskFailed(task, err)
				pm.logger.Errorf("Task %s for microservice %s failed after %d retries, giving up", task.Action, task.MicroserviceUUID, task.Retries)
				// Set FAILED status and error message so controller receives it
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesState(task.MicroserviceUUID, models.MicroserviceStateFailed)
					errMsg := fmt.Sprintf("Container %s %s operation failed after 5 attempts: %v", task.MicroserviceUUID, task.Action, err)
					s.SetMicroservicesStatusErrorMessage(task.MicroserviceUUID, errMsg)
				})
				// Release the in-flight lock so the reconciliation loop can run
				if ms := pm.microserviceManager.FindLatestMicroserviceByUUID(task.MicroserviceUUID); ms != nil {
					ms.SetIsUpdating(false)
				}
			}
		}
	}
}

func (pm *ProcessManager) retryTask(task *ContainerTask) {
	task.IncrementRetries()
	pm.emitTaskRetry(task)
	checker := GetRestartStuckChecker()
	delay := checker.ArmFailure(task.MicroserviceUUID)
	if delay <= 0 {
		pm.taskQueue.Add(task)
		return
	}
	checker.ParkTask(task)
}

func (pm *ProcessManager) enqueueDueRetries() {
	for _, task := range GetRestartStuckChecker().TakeDueTasks() {
		if task == nil {
			continue
		}
		pm.addTask(task)
	}
}

func (pm *ProcessManager) emitTaskRetry(task *ContainerTask) {
	opID := task.OperationID
	if opID == "" {
		opID = uuid.NewString()
		task.OperationID = opID
	}
	runtimeops.Emit(pm.ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventTaskRetry,
		Level:       runtimeops.LevelWarn,
		Module:      ProcessManagerModuleName,
		OperationID: opID,
		Engine:      pm.engineName,
		MsUUID:      task.MicroserviceUUID,
		Source:      runtimeops.SourceTask,
		Message:     "task scheduled for retry",
		Fields: map[string]any{
			"action":     string(task.Action),
			"attempt":    task.Retries,
			"maxRetries": maxTaskRetries,
		},
	})
}

func (pm *ProcessManager) emitTaskFailed(task *ContainerTask, err error) {
	opID := task.OperationID
	if opID == "" {
		opID = uuid.NewString()
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	runtimeops.Emit(pm.ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventTaskFailed,
		Level:       runtimeops.LevelError,
		Module:      ProcessManagerModuleName,
		OperationID: opID,
		Engine:      pm.engineName,
		MsUUID:      task.MicroserviceUUID,
		ReasonCode:  runtimeops.ReasonTaskExhaustedRetries,
		Source:      runtimeops.SourceTask,
		Message:     "task failed after max retries",
		Error:       errMsg,
		Fields: map[string]any{
			"action":  string(task.Action),
			"retries": task.Retries,
		},
	})
}

// operationContext returns a context with a new operationId for non-task call paths.
func (pm *ProcessManager) operationContext(msUUID string) context.Context {
	base := pm.ctx
	if base == nil {
		base = context.Background()
	}
	return runtimeops.WithOperation(base, uuid.NewString(), pm.engineName, msUUID)
}

// executeTask executes a container task
func (pm *ProcessManager) executeTask(task *ContainerTask) error {
	if task.OperationID == "" {
		task.OperationID = uuid.NewString()
	}
	opCtx := runtimeops.WithOperation(pm.ctx, task.OperationID, pm.engineName, task.MicroserviceUUID)
	start := time.Now()

	runtimeops.Emit(opCtx, runtimeops.RuntimeEvent{
		Event:   runtimeops.EventTaskStarted,
		Level:   runtimeops.LevelInfo,
		Module:  ProcessManagerModuleName,
		Source:  runtimeops.SourceTask,
		Message: "task started",
		Fields:  map[string]any{"action": string(task.Action)},
	})

	pm.logger.Debugf("Executing task %s for microservice %s", task.Action, task.MicroserviceUUID)

	ms := pm.microserviceManager.FindLatestMicroserviceByUUID(task.MicroserviceUUID)

	var err error
	switch task.Action {
	case TaskActionAdd:
		if ms != nil {
			err = pm.containerManager.AddContainer(opCtx, ms)
			err = normalizeCatalogTaskError(task.MicroserviceUUID, err)
		}
	case TaskActionUpdate:
		if ms != nil {
			err = pm.containerManager.UpdateContainer(opCtx, ms, false)
			err = normalizeCatalogTaskError(task.MicroserviceUUID, err)
		}
	case TaskActionRemove:
		err = pm.containerManager.RemoveContainerByMicroserviceUUID(opCtx, task.MicroserviceUUID, false, false)
	case TaskActionRemoveWithCleanup:
		// removeImage=true: clean removal
		err = pm.containerManager.RemoveContainerByMicroserviceUUID(opCtx, task.MicroserviceUUID, true, true)
	case TaskActionStop:
		err = pm.containerManager.StopContainerByMicroserviceUUID(opCtx, task.MicroserviceUUID)
	case TaskActionCreateExec:
		if ms != nil {
			// Exec session creation would create an interactive exec session
			// This requires WebSocket support and is typically handled by the EdgeletAPI
			// For now, log that exec was requested
			pm.logger.Infof("Exec session requested for microservice %s (handled by EdgeletAPI)", ms.MicroserviceUUID)
		}
	default:
		pm.logger.Warnf("Unknown task action: %s", task.Action)
	}

	if err == nil {
		runtimeops.Emit(opCtx, runtimeops.RuntimeEvent{
			Event:      runtimeops.EventTaskCompleted,
			Level:      runtimeops.LevelInfo,
			Module:     ProcessManagerModuleName,
			Source:     runtimeops.SourceTask,
			Result:     runtimeops.ResultOK,
			DurationMs: time.Since(start).Milliseconds(),
			Message:    "task completed",
			Fields:     map[string]any{"action": string(task.Action)},
		})
	}
	return err
}

// addTask adds a task to the queue and emits a structured enqueue audit event.
func (pm *ProcessManager) addTask(task *ContainerTask) {
	if task.OperationID == "" {
		task.OperationID = uuid.NewString()
	}
	pm.taskQueue.Add(task)
	queueDepth := 0
	if pm.taskQueue != nil {
		queueDepth = pm.taskQueue.Size()
	}
	runtimeops.Emit(pm.ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventTaskEnqueued,
		Level:       runtimeops.LevelInfo,
		Module:      ProcessManagerModuleName,
		OperationID: task.OperationID,
		Engine:      pm.engineName,
		MsUUID:      task.MicroserviceUUID,
		Source:      runtimeops.SourceTask,
		Message:     "task enqueued",
		Fields: map[string]any{
			"action":     string(task.Action),
			"queueDepth": queueDepth,
		},
	})
}

// handleLatestMicroservices is the core reconciliation loop.
//
// It compares desired state (latestMicroservices from controller) against actual
// Docker state and converges them:
//   - container missing, should exist  → schedule ADD
//   - container exists, marked delete  → schedule REMOVE
//   - container exists, should exist   → schedule UPDATE if configuration drifted
//
// An ADD or UPDATE task sets ms.IsUpdating = true; the loop skips any microservice
// that already has an in-flight task, providing exactly-once per-cycle semantics and
// preventing task-queue flooding.
func (pm *ProcessManager) handleLatestMicroservices(stats *reconcileCycleStats) {
	pm.logger.Debug("Start handle latest microservices")

	latestMicroservices := pm.microserviceManager.GetLatestMicroservices()
	// Sort by schedule ascending
	slices.SortFunc(latestMicroservices, func(a, b *models.Microservice) int {
		return cmp.Compare(a.Schedule, b.Schedule)
	})

	for _, ms := range latestMicroservices {
		// Skip microservices that already have an in-flight ADD or UPDATE task.
		// IsUpdating is set before enqueueing and cleared when the task finishes,
		// ensuring we never flood the queue with duplicate tasks.
		if ms.GetIsUpdating() {
			continue
		}
		if pm.isControlPlaneManagedMicroservice(ms) {
			continue
		}

		container, err := pm.containerManager.GetContainerForMicroservice(ms.MicroserviceUUID)
		if err != nil {
			pm.logger.Warnf("Error getting container for microservice %s: %v", ms.MicroserviceUUID, err)
			continue
		}

		if ms.Delete {
			// Desired state: deleted.
			if container != nil {
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateMarkedForDeletion)
				})
				if stats != nil {
					stats.scheduledRemove++
				}
				pm.emitReconcileDecision(ms.MicroserviceUUID, "REMOVE", "delete_requested", "scheduling container removal", runtimeops.LevelInfo, nil)
				pm.deleteMicroservice(ms)
			} else {
				pm.releaseCatalog(ms.MicroserviceUUID)
			}
			// If container is already gone, nothing to do.
			continue
		}

		// Desired state: running.
		if container == nil {
			if !pm.applyCatalogStartGate(ms) {
				continue
			}
			skip := pm.recreateSkipReason(ms)
			if ms.IsStuckInRestart && !ms.Rebuild {
				existing := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
				if forceRecreate, reason, exitCode := shouldForceRecreateFromStatus(existing); forceRecreate {
					pm.emitReconcileDecision(ms.MicroserviceUUID, "ADD", "stuck_gate_bypass", "bypassing stuck gate for terminal runtime state", runtimeops.LevelWarn, map[string]any{
						"containerId": existing.ContainerID,
						"exitCode":    exitCode,
						"detail":      reason,
					})
					ms.IsStuckInRestart = false
					if skip == recreateSkipNone {
						skip = recreateSkipNonRestartable
					}
				} else {
					pm.logger.Debugf("Skipping stuck microservice %s (rebuild not requested)", ms.MicroserviceUUID)
					continue
				}
			}
			// If status is FAILED and Rebuild not requested, skip — do not re-add
			if pmStatus := statusreporter.GetInstance().GetProcessManagerStatus(); pmStatus != nil {
				if st := pmStatus.LookupMicroserviceStatus(ms.MicroserviceUUID); st != nil &&
					st.Status == models.MicroserviceStateFailed && !ms.Rebuild && !ms.Models.HasItems() {
					pm.logger.Debugf("Skipping failed microservice %s (rebuild not requested)", ms.MicroserviceUUID)
					continue
				}
			}
			if crashDrivenMissing(lookupReporterStatus(ms.MicroserviceUUID)) && !ms.Rebuild {
				pm.recordRestart(ms.MicroserviceUUID)
			}
			if !GetRestartStuckChecker().Ready(ms.MicroserviceUUID, skip) {
				pm.emitRestartBackoff(ms.MicroserviceUUID, "ADD")
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateQueued)
				})
				continue
			}
			// Container is missing — schedule creation. Clear stuck flag when Rebuild=true.
			if stats != nil {
				stats.scheduledAdd++
			}
			pm.emitReconcileDecision(ms.MicroserviceUUID, "ADD", "missing_container", "scheduling container creation", runtimeops.LevelInfo, map[string]any{
				"imageName": ms.ImageName,
			})
			ms.IsStuckInRestart = false
			statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
				s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateUnknown)
			})
			pm.addMicroservice(ms)
			continue
		}

		// Container exists and should keep running.
		// Skip containers stuck in restart loop (unless a rebuild was explicitly requested).
		if ms.IsStuckInRestart && !ms.Rebuild {
			continue
		}

		pm.refreshCatalogProjection(ms)

		status, err := pm.engine.GetContainerStatus(container.ID, ms.MicroserviceUUID)
		if err != nil {
			pm.logger.Warnf("Error getting microservice status for %s: %v", ms.MicroserviceUUID, err)
			continue
		}
		checker := GetRestartStuckChecker()
		if status.Status == models.MicroserviceStateRunning {
			checker.ObserveRunning(ms.MicroserviceUUID)
		}
		skip := pm.recreateSkipReason(ms)
		if forceRecreate, reason, exitCode := shouldForceRecreateFromStatus(status); forceRecreate {
			if !ms.Rebuild {
				pm.recordRestart(ms.MicroserviceUUID)
			}
			if skip == recreateSkipNone {
				skip = recreateSkipNonRestartable
			}
			if !checker.Ready(ms.MicroserviceUUID, skip) {
				pm.emitRestartBackoff(ms.MicroserviceUUID, "UPDATE")
				pm.syncRuntimeStatus(ms.MicroserviceUUID, status)
				continue
			}
			sandboxID, _ := pm.engine.GetContainerSandboxID(container.ID)
			if stats != nil {
				stats.scheduledUpdate++
			}
			pm.emitReconcileDecision(ms.MicroserviceUUID, "UPDATE", "non_restartable", "recreating after non-restartable terminal state", runtimeops.LevelWarn, map[string]any{
				"containerId": container.ID,
				"sandboxId":   sandboxID,
				"exitCode":    exitCode,
				"detail":      reason,
				"criMessage":  safeErrorMessage(status),
			})
			ms.IsStuckInRestart = false
			ms.SetIsUpdating(true)
			statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
				s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateUpdating)
			})
			pm.addTask(NewContainerTask(TaskActionUpdate, ms.MicroserviceUUID))
			continue
		}
		if status.Status == models.MicroserviceStateCreated {
			if !checker.Ready(ms.MicroserviceUUID, skip) {
				pm.emitRestartBackoff(ms.MicroserviceUUID, "START")
				pm.syncRuntimeStatus(ms.MicroserviceUUID, status)
				continue
			}
			sandboxID, _ := pm.engine.GetContainerSandboxID(container.ID)
			pm.emitReconcileDecision(ms.MicroserviceUUID, "START", "created_container", "starting created container", runtimeops.LevelInfo, map[string]any{
				"containerId": container.ID,
				"sandboxId":   sandboxID,
				"criMessage":  safeErrorMessage(status),
			})
			if startErr := pm.containerManager.StartContainerByMicroserviceUUID(pm.reconcileOperationContext(ms.MicroserviceUUID), ms.MicroserviceUUID); startErr != nil {
				if nr, ok := engine.IsNonRestartableContainerError(startErr); ok {
					pm.recordFailedStart(ms.MicroserviceUUID)
					if skip == recreateSkipNone {
						skip = recreateSkipNonRestartable
					}
					if !checker.Ready(ms.MicroserviceUUID, skip) {
						pm.emitRestartBackoff(ms.MicroserviceUUID, "UPDATE")
						pm.syncRuntimeStatus(ms.MicroserviceUUID, status)
						continue
					}
					if stats != nil {
						stats.scheduledUpdate++
					}
					pm.emitReconcileDecision(ms.MicroserviceUUID, "UPDATE", "non_restartable", "recreating after non-restartable start failure", runtimeops.LevelWarn, map[string]any{
						"containerId": container.ID,
						"sandboxId":   sandboxID,
						"exitCode":    nr.ExitCode,
						"detail":      nr.Reason,
						"criMessage":  nr.Message,
					})
					ms.IsStuckInRestart = false
					ms.SetIsUpdating(true)
					statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
						s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateUpdating)
					})
					pm.addTask(NewContainerTask(TaskActionUpdate, ms.MicroserviceUUID))
					continue
				}
				pm.recordFailedStart(ms.MicroserviceUUID)
				if checker.IsStuckInContainerCreation(ms.MicroserviceUUID) {
					pm.emitReconcileDecision(ms.MicroserviceUUID, "START", "stuck_in_restart", "created start failed repeatedly", runtimeops.LevelWarn, map[string]any{
						"containerId": container.ID,
						"sandboxId":   sandboxID,
						"error":       startErr.Error(),
					})
					status.Status = models.MicroserviceStateStuckInRestart
					ms.IsStuckInRestart = true
					stuckMsg := stuckInRestartErrorMessage(ms.MicroserviceUUID, fmt.Sprintf("Container repeatedly failing to start: %v", startErr))
					status.ErrorMessage = &stuckMsg
					statusreporter.GetInstance().UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
						pmStatus.SetMicroservicesStatus(ms.MicroserviceUUID, status)
					})
					continue
				}
				pm.emitReconcileDecision(ms.MicroserviceUUID, "START", "retry_start", "created start failed", runtimeops.LevelWarn, map[string]any{
					"containerId": container.ID,
					"sandboxId":   sandboxID,
					"error":       startErr.Error(),
				})
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesStatusErrorMessage(ms.MicroserviceUUID, startErr.Error())
					s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateCreated)
				})
			} else {
				pm.emitReconcileDecision(ms.MicroserviceUUID, "START", "created_container", "created container started", runtimeops.LevelInfo, map[string]any{
					"containerId": container.ID,
					"sandboxId":   sandboxID,
				})
			}
			continue
		}

		// Detect containers stuck in exit/creation loops and mark them accordingly.
		// Prefer existing error message from engine (e.g. Docker) when available; use static fallback otherwise
		switch status.Status {
		case models.MicroserviceStateExiting:
			if !ms.Rebuild {
				pm.recordRestart(ms.MicroserviceUUID)
			}
			if checker.IsStuck(ms.MicroserviceUUID) {
				status.Status = models.MicroserviceStateStuckInRestart
				ms.IsStuckInRestart = true
				stuckMsg := stuckInRestartErrorMessage(ms.MicroserviceUUID, "Container repeatedly exiting")
				status.ErrorMessage = &stuckMsg
			}
		case models.MicroserviceStateCreated:
			if checker.IsStuckInContainerCreation(ms.MicroserviceUUID) {
				status.Status = models.MicroserviceStateStuckInRestart
				ms.IsStuckInRestart = true
				stuckMsg := stuckInRestartErrorMessage(ms.MicroserviceUUID, "Container repeatedly failing to start")
				status.ErrorMessage = &stuckMsg
			}
		}

		// Merge per-container CPU/memory stats for running containers (best-effort).
		if status.Status == models.MicroserviceStateRunning {
			if stats, err := pm.engine.GetContainerStats(container.ID); err == nil {
				status.CPUUsage = stats.CPUUsage
				status.MemoryUsage = stats.MemoryUsage
			}
		}

		pm.syncRuntimeStatus(ms.MicroserviceUUID, status)

		pm.updateMicroservice(container, ms, stats)
	}

	pm.logger.Debug("Finished handle latest microservices")
}

// addMicroservice queues a microservice for creation.
// It marks the microservice as "in-flight" (IsUpdating=true) before enqueuing so that
// subsequent reconciliation cycles skip it until the ADD task finishes.
func (pm *ProcessManager) addMicroservice(ms *models.Microservice) {
	ms.SetIsUpdating(true)
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		if ms.Rebuild {
			status.ResetMicroservicesRestartCount(ms.MicroserviceUUID)
		}
		status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateQueued)
	})
	pm.addTask(NewContainerTask(TaskActionAdd, ms.MicroserviceUUID))
}

// deleteMicroservice queues a microservice for deletion
func (pm *ProcessManager) deleteMicroservice(ms *models.Microservice) {
	// Set status to MARKED_FOR_DELETION (already set in handleLatestMicroservices, but set again for safety)
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateMarkedForDeletion)
	})
	if ms.DeleteWithCleanup {
		pm.addTask(NewContainerTask(TaskActionRemoveWithCleanup, ms.MicroserviceUUID))
	} else {
		pm.addTask(NewContainerTask(TaskActionRemove, ms.MicroserviceUUID))
	}
}

// updateMicroservice checks if a container needs updating
func (pm *ProcessManager) updateMicroservice(container *engine.Container, ms *models.Microservice, stats *reconcileCycleStats) {
	pm.logger.Debug("Start update microservice")

	ms.ContainerID = container.ID

	ip, err := pm.engine.GetContainerIPAddress(container.ID)
	if err != nil {
		pm.logger.Warnf("Can't get IP address for microservice %s: %v", ms.MicroserviceUUID, err)
		ip = "0.0.0.0"
	}
	ms.ContainerIPAddress = &ip

	status, err := pm.engine.GetContainerStatus(container.ID, ms.MicroserviceUUID)
	if err != nil {
		pm.logger.Warnf("Error getting microservice status: %v", err)
		return
	}

	shouldUpdate := pm.shouldContainerBeUpdated(ms, container, status)
	if shouldUpdate {
		if ms.IsStuckInRestart {
			ms.IsStuckInRestart = false
		}
		skip := pm.recreateSkipReason(ms)
		if status.Status != models.MicroserviceStateRunning && !ms.Rebuild {
			pm.recordRestart(ms.MicroserviceUUID)
			if !GetRestartStuckChecker().Ready(ms.MicroserviceUUID, skip) {
				pm.emitRestartBackoff(ms.MicroserviceUUID, "UPDATE")
				return
			}
		}
		reason := reconcileUpdateReason(ms, status)
		if stats != nil {
			stats.scheduledUpdate++
		}
		pm.emitReconcileDecision(ms.MicroserviceUUID, "UPDATE", reason, "scheduling container update", runtimeops.LevelInfo, map[string]any{
			"containerId": container.ID,
		})
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
			if ms.Rebuild {
				status.ResetMicroservicesRestartCount(ms.MicroserviceUUID)
			}
			status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateUpdating)
		})
		pm.addTask(NewContainerTask(TaskActionUpdate, ms.MicroserviceUUID))
	}

	pm.logger.Debug("Finished update microservice")
}

// shouldContainerBeUpdated determines if a running container needs to be rebuilt.
func (pm *ProcessManager) shouldContainerBeUpdated(ms *models.Microservice, container *engine.Container, status *models.MicroserviceStatus) bool {
	pm.logger.Debug("Start should Container Be Updated")

	if ms.GetIsUpdating() {
		pm.logger.Debugf("Skipping update check for %s — task already in flight", ms.MicroserviceUUID)
		return false
	}

	if ms.Rebuild {
		pm.logger.Debugf("Rebuild requested for %s — bypassing state checks", ms.MicroserviceUUID)
		return true
	}

	switch status.Status {
	case models.MicroserviceStateQueued,
		models.MicroserviceStateUpdating,
		models.MicroserviceStateStuckInRestart:
		return false
	default:
	}

	isNotRunning := status.Status != models.MicroserviceStateRunning
	registry, regErr := resolveRegistryForMicroservice(pm.microserviceManager, ms)
	if regErr != nil {
		pm.logger.Warnf("drift check: %v microservice=%s", regErr, ms.MicroserviceUUID)
	}
	areNotEqual := !pm.engine.AreMicroserviceAndContainerEqual(container.ID, ms, registry)
	isRebuild := ms.Rebuild

	result := isNotRunning || areNotEqual || isRebuild
	pm.logger.Debugf("Finished should Container Be Updated: notRunning=%v configDrifted=%v rebuild=%v → %v",
		isNotRunning, areNotEqual, isRebuild, result)
	return result
}

// stuckInRestartErrorMessage returns the existing error message from the status reporter
// when available (e.g. from Docker/engine), otherwise the static fallback
func stuckInRestartErrorMessage(microserviceUUID, fallback string) string {
	existing := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(microserviceUUID)
	if existing != nil && existing.ErrorMessage != nil && *existing.ErrorMessage != "" {
		return *existing.ErrorMessage
	}
	return fallback
}

func lookupReporterStatus(uuid string) *models.MicroserviceStatus {
	pmStatus := statusreporter.GetInstance().GetProcessManagerStatus()
	if pmStatus == nil {
		return nil
	}
	return pmStatus.LookupMicroserviceStatus(uuid)
}

func crashDrivenMissing(existing *models.MicroserviceStatus) bool {
	if existing == nil {
		return false
	}
	switch existing.Status {
	case models.MicroserviceStateQueued, models.MicroserviceStateUnknown,
		models.MicroserviceStateDeleted, models.MicroserviceStateMarkedForDeletion:
		return false
	default:
		return true
	}
}

func bumpRestartCount(uuid string) {
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.IncrementMicroservicesRestartCount(uuid)
	})
}

func (pm *ProcessManager) recordRestart(uuid string) {
	if GetRestartStuckChecker().RecordIfNew(uuid) {
		bumpRestartCount(uuid)
	}
}

func (pm *ProcessManager) recordFailedStart(uuid string) {
	GetRestartStuckChecker().RecordFailedStart(uuid)
	bumpRestartCount(uuid)
}

func (pm *ProcessManager) recreateSkipReason(ms *models.Microservice) recreateSkip {
	if ms == nil {
		return recreateSkipNone
	}
	checker := GetRestartStuckChecker()
	if ms.Rebuild {
		checker.ResetAfterRebuild(ms.MicroserviceUUID)
		return recreateSkipRebuild
	}
	if checker.ConsumeCatalogReadySkip(ms.MicroserviceUUID) {
		return recreateSkipCatalogReady
	}
	return recreateSkipNone
}

func (pm *ProcessManager) emitRestartBackoff(uuid, decision string) {
	checker := GetRestartStuckChecker()
	pm.emitReconcileDecision(uuid, decision, "restart_backoff", "delaying crash-driven recreate", runtimeops.LevelInfo, map[string]any{
		"consecutiveFailures": checker.ConsecutiveFailures(uuid),
		"delayMs":             checker.RemainingDelay(uuid).Milliseconds(),
	})
}

func (pm *ProcessManager) syncRuntimeStatus(uuid string, status *models.MicroserviceStatus) {
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
		existing := pmStatus.MicroservicesStatus[uuid]
		if existing != nil && existing.HealthStatus != nil && status.HealthStatus == nil {
			status.HealthStatus = existing.HealthStatus
		}
		syncMicroserviceStatusToReporter(pmStatus, uuid, status)
	})
	maybeResetRestartBackoff(uuid, status)
}

func maybeResetRestartBackoff(uuid string, status *models.MicroserviceStatus) {
	if status != nil && status.Status == models.MicroserviceStateRunning && statusGrace.elapsed(uuid) {
		GetRestartStuckChecker().ResetBackoff(uuid)
	}
}

func shouldForceRecreateFromStatus(status *models.MicroserviceStatus) (bool, string, int32) {
	reason, exitCode := parseCRIReasonAndExitCode(safeErrorMessage(status))
	if engine.IsNonRestartableCRIReason(reason) {
		return true, reason, exitCode
	}
	return false, "", 0
}

func safeErrorMessage(status *models.MicroserviceStatus) string {
	if status == nil || status.ErrorMessage == nil {
		return ""
	}
	return strings.TrimSpace(*status.ErrorMessage)
}

func parseCRIReasonAndExitCode(message string) (string, int32) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "", 0
	}
	reason := ""
	if idx := strings.Index(trimmed, "CRI reason="); idx >= 0 {
		start := idx + len("CRI reason=")
		end := len(trimmed)
		if space := strings.Index(trimmed[start:], " "); space >= 0 {
			end = start + space
		}
		reason = strings.TrimSpace(trimmed[start:end])
	}
	exitCode := int32(0)
	if idx := strings.Index(trimmed, "exitCode="); idx >= 0 {
		start := idx + len("exitCode=")
		end := len(trimmed)
		if space := strings.Index(trimmed[start:], " "); space >= 0 {
			end = start + space
		}
		if parsed, err := strconv.ParseInt(strings.TrimSpace(trimmed[start:end]), 10, 32); err == nil {
			exitCode = int32(parsed)
		}
	}
	return reason, exitCode
}

// deleteRemainingMicroservices deletes containers that are no longer in the latest microservices list
func (pm *ProcessManager) deleteRemainingMicroservices() {
	pm.logger.Debug("Start delete Remaining Microservices")

	// Get latest and current microservice UUIDs
	latestMicroservices := pm.microserviceManager.GetLatestMicroservices()
	currentMicroservices := pm.microserviceManager.GetCurrentMicroservices()

	latestUUIDs := make(map[string]bool)
	currentUUIDs := make(map[string]bool)

	for _, ms := range latestMicroservices {
		latestUUIDs[ms.MicroserviceUUID] = true
	}
	for _, ms := range currentMicroservices {
		currentUUIDs[ms.MicroserviceUUID] = true
	}

	allContainers, err := pm.engine.GetAllContainers()
	if err != nil {
		pm.logger.Errorf("Error getting all containers: %v", err)
		return
	}

	oldAgentUUIDs := make([]string, 0)
	unknownContainerIDs := make([]string, 0)

	cfg := config.GetInstance()

	var cpDep *models.ControlPlaneDeployment
	if item, found, err := store.GetInstance().GetSystemControlPlane(); err == nil && found {
		cpDep = item
	}

	for _, container := range allContainers {
		msUUID := workloadmeta.MicroserviceUIDFromLabels(container.Labels)
		if msUUID == "" {
			continue
		}

		isCurrent := currentUUIDs[msUUID]
		isLatest := latestUUIDs[msUUID]

		removeManagedByUUID, removeUnknownByID := cleanupDecisionForContainer(
			container.Labels,
			container.ID,
			container.Image,
			isCurrent,
			isLatest,
			cfg.WatchdogEnabled,
			cpDep,
		)
		if removeManagedByUUID {
			oldAgentUUIDs = append(oldAgentUUIDs, msUUID)
			continue
		}
		if removeUnknownByID {
			unknownContainerIDs = append(unknownContainerIDs, container.ID)
		}
	}

	// Delete old agent containers
	for _, uuid := range oldAgentUUIDs {
		pm.logger.Infof("Deleting old agent microservice: %s", uuid)
		if err := pm.containerManager.RemoveContainerByMicroserviceUUID(pm.operationContext(uuid), uuid, false, false); err != nil {
			pm.logger.Errorf("Error deleting old agent microservice %s: %v", uuid, err)
		}
	}

	// Delete unknown containers by concrete container ID so watchdog can remove
	// non-iofog containers that don't resolve via microservice UUID lookup.
	for _, containerID := range unknownContainerIDs {
		pm.logger.Infof("Deleting unknown container: %s", containerID)
		if err := pm.containerManager.RemoveContainerByID(pm.operationContext(containerID), containerID, false, false); err != nil {
			pm.logger.Errorf("Error deleting unknown container %s: %v", containerID, err)
		}
	}

	pm.logger.Debug("Finished delete Remaining Microservices")
}

func isProcessManagerStatusReportTombstone(status *models.MicroserviceStatus) bool {
	if status == nil {
		return false
	}
	switch status.Status {
	case models.MicroserviceStateDeleted,
		models.MicroserviceStateUnknown,
		models.MicroserviceStateDeleting,
		models.MicroserviceStateMarkedForDeletion:
		return true
	default:
		return false
	}
}

// updateRunningMicroservicesCount updates the count of running managed microservices.
func (pm *ProcessManager) updateRunningMicroservicesCount() {
	pm.logger.Debug("Update running microservice count")

	containers, err := pm.engine.GetRunningContainers()
	if err != nil {
		pm.logger.Errorf("Error getting running containers: %v", err)
		return
	}

	count := 0
	count = countManagedRunningContainers(containers)

	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		status.SetRunningMicroservicesCount(count)
	})
	pm.logger.Debugf("Updated running microservices count: %d", count)
}

func countManagedRunningContainers(containers []engine.Container) int {
	count := 0
	for _, c := range containers {
		if workloadmeta.IsManagedByIofog(c.Labels) {
			count++
		}
	}
	return count
}

// updateCurrentMicroservices updates the current microservices list
func (pm *ProcessManager) updateCurrentMicroservices() {
	pm.logger.Debug("Start update current Microservices")

	latestMicroservices := pm.microserviceManager.GetLatestMicroservices()
	currentMicroservices := make([]*models.Microservice, 0)

	for _, ms := range latestMicroservices {
		if !ms.Delete {
			currentMicroservices = append(currentMicroservices, ms)
		}
	}

	pm.microserviceManager.SetCurrentMicroservices(currentMicroservices)
	pm.logger.Debug("Finished update current Microservices")
}

// GetContainerForMicroservice resolves runtime container by microservice UUID.
func (pm *ProcessManager) GetContainerForMicroservice(microserviceUUID string) (*engine.Container, error) {
	if pm.containerManager == nil {
		return nil, errors.New("process manager is not initialized")
	}
	return pm.containerManager.GetContainerForMicroservice(microserviceUUID)
}

// GetContainerByIDPrefix resolves a container by ID prefix.
// Returns the matched container and a list of matching IDs for ambiguity reporting.
func (pm *ProcessManager) GetContainerByIDPrefix(prefix string) (*engine.Container, []string, error) {
	if pm.engine == nil {
		return nil, nil, errors.New("process manager engine is not initialized")
	}
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return nil, nil, errors.New("container id prefix is required")
	}
	all, err := pm.engine.GetAllContainers()
	if err != nil {
		return nil, nil, err
	}
	matches := make([]engine.Container, 0)
	ids := make([]string, 0)
	for _, cont := range all {
		if strings.HasPrefix(cont.ID, trimmed) {
			matches = append(matches, cont)
			ids = append(ids, cont.ID)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil, nil
	case 1:
		c := matches[0]
		return &c, ids, nil
	default:
		slices.Sort(ids)
		return nil, ids, nil
	}
}

// GetContainerByID resolves one container by concrete container id.
func (pm *ProcessManager) GetContainerByID(containerID string) (*engine.Container, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.GetContainerByID(containerID)
}

func decodeLocalDeployManifest(manifestYAML string) (*models.LocalDeployManifest, error) {
	doc := &models.LocalDeployManifest{}
	dec := yaml.NewDecoder(strings.NewReader(manifestYAML))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil {
		return nil, fmt.Errorf("invalid persisted manifest: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted manifest: %w", err)
	}
	return doc, nil
}

func (pm *ProcessManager) resolveMicroserviceForLifecycle(microserviceUUID string) (*models.Microservice, error) {
	if pm.microserviceManager != nil {
		if ms := pm.microserviceManager.FindLatestMicroserviceByUUID(microserviceUUID); ms != nil {
			return ms, nil
		}
	}
	item, err := store.GetInstance().GetLocalWorkload(microserviceUUID)
	if err != nil || item == nil {
		return nil, errors.New("microservice spec not found")
	}
	doc, err := decodeLocalDeployManifest(item.ManifestYAML)
	if err != nil {
		return nil, err
	}
	image := doc.ManifestImage()
	localMS := models.BuildMicroserviceFromLocalManifest(doc, item.LocalUUID, image)
	if doc.Spec.Registry != nil {
		if reg, regErr := store.GetInstance().GetLocalRegistry(*doc.Spec.Registry); regErr == nil && reg != nil {
			localMS.RegistryID = reg.ID
		}
	}
	return localMS, nil
}

func (pm *ProcessManager) shouldRecreateForStatus(status *models.MicroserviceStatus) bool {
	if force, _, _ := shouldForceRecreateFromStatus(status); force {
		return true
	}
	return status != nil && status.Status == models.MicroserviceStateExiting
}

func (pm *ProcessManager) recreateMicroservice(microserviceUUID string, pullImage bool) (string, error) {
	if pm.containerManager == nil {
		return "", errors.New("process manager is not initialized")
	}
	ms, err := pm.resolveMicroserviceForLifecycle(microserviceUUID)
	if err != nil {
		return "", err
	}
	var managedMS *models.Microservice
	if pm.microserviceManager != nil {
		managedMS = pm.microserviceManager.FindLatestMicroserviceByUUID(microserviceUUID)
	}
	if managedMS != nil {
		managedMS.SetIsUpdating(true)
		defer managedMS.SetIsUpdating(false)
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(microserviceUUID, models.MicroserviceStateUpdating)
		})
	}
	newID, err := pm.containerManager.RecreateContainer(pm.operationContext(microserviceUUID), ms, RecreateOptions{PullImage: pullImage})
	if err != nil {
		return "", err
	}
	pm.updateLocalContainerAfterRecreate(microserviceUUID, newID)
	return newID, nil
}

func (pm *ProcessManager) recreateLocalDeployment(item *models.LocalDeployedMicroservice, pullImage bool, now int64) error {
	if pm.recreateLocalDeploymentFn != nil {
		return pm.recreateLocalDeploymentFn(item, pullImage, now)
	}
	if pm.containerManager == nil {
		err := errors.New("process manager is not initialized")
		pm.bumpLocalFailure(item, err, "failed")
		return err
	}
	ms, err := pm.resolveMicroserviceForLifecycle(item.LocalUUID)
	if err != nil {
		pm.bumpLocalFailure(item, err, "failed")
		return err
	}
	newID, err := pm.containerManager.RecreateContainer(pm.reconcileOperationContext(item.LocalUUID), ms, RecreateOptions{PullImage: pullImage})
	if err != nil {
		pm.bumpLocalFailure(item, err, "failed")
		return err
	}
	item.ContainerID = newID
	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.ObservedGeneration = item.Generation
	item.FailureCount = 0
	item.LastTransitionAt = now
	if pm.engine != nil {
		if sandboxID, sbErr := pm.engine.GetContainerSandboxID(newID); sbErr == nil && sandboxID != "" {
			_ = store.GetInstance().UpsertRuntimeContainerRef(item.LocalUUID, store.RuntimeScopeLocal, newID, sandboxID)
		}
	}
	return persistLocalWorkloadIfPresent(item)
}

func (pm *ProcessManager) updateLocalContainerAfterRecreate(microserviceUUID, containerID string) {
	item, err := store.GetInstance().GetLocalWorkload(microserviceUUID)
	if err != nil || item == nil {
		return
	}
	item.ContainerID = containerID
	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.LastTransitionAt = time.Now().Unix()
	_ = persistLocalWorkloadIfPresent(item)
	if pm.engine != nil {
		if sandboxID, sbErr := pm.engine.GetContainerSandboxID(containerID); sbErr == nil && sandboxID != "" {
			_ = store.GetInstance().UpsertRuntimeContainerRef(microserviceUUID, store.RuntimeScopeLocal, containerID, sandboxID)
		}
	}
}

// StartMicroservice starts a runtime microservice container.
func (pm *ProcessManager) StartMicroservice(microserviceUUID string) error {
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}
	ctx := pm.operationContext(microserviceUUID)

	container, err := pm.containerManager.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	if container == nil {
		_, err := pm.recreateMicroservice(microserviceUUID, true)
		return err
	}
	if pm.engine == nil {
		return errors.New("process manager engine is not initialized")
	}

	status, err := pm.engine.GetContainerStatus(container.ID, microserviceUUID)
	if err != nil {
		return err
	}
	if status.Status == models.MicroserviceStateRunning {
		return nil
	}
	if engine.SupportsInPlaceRestart(pm.engineName) {
		return pm.containerManager.StartContainerByMicroserviceUUID(ctx, microserviceUUID)
	}
	if pm.shouldRecreateForStatus(status) {
		_, err := pm.recreateMicroservice(microserviceUUID, false)
		return err
	}

	startErr := pm.containerManager.StartContainerByMicroserviceUUID(ctx, microserviceUUID)
	if startErr == nil {
		return nil
	}
	if _, ok := engine.IsNonRestartableContainerError(startErr); ok {
		_, err := pm.recreateMicroservice(microserviceUUID, false)
		return err
	}
	return startErr
}

// StopMicroservice stops a runtime microservice container.
func (pm *ProcessManager) StopMicroservice(microserviceUUID string) error {
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}
	return pm.containerManager.StopContainerByMicroserviceUUID(pm.operationContext(microserviceUUID), microserviceUUID)
}

// KillMicroservice sends a forceful kill signal to a runtime microservice container.
func (pm *ProcessManager) KillMicroservice(microserviceUUID string) error {
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}
	return pm.containerManager.KillContainerByMicroserviceUUID(pm.operationContext(microserviceUUID), microserviceUUID)
}

// RestartMicroservice performs stop then start for a runtime microservice container.
func (pm *ProcessManager) RestartMicroservice(microserviceUUID string) error {
	if err := pm.StopMicroservice(microserviceUUID); err != nil {
		return err
	}
	if engine.SupportsInPlaceRestart(pm.engineName) {
		return pm.StartMicroservice(microserviceUUID)
	}
	_, err := pm.recreateMicroservice(microserviceUUID, false)
	return err
}

// RemoveMicroservice removes a runtime microservice container.
func (pm *ProcessManager) RemoveMicroservice(microserviceUUID string) error {
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}
	return pm.containerManager.RemoveContainerByMicroserviceUUID(pm.operationContext(microserviceUUID), microserviceUUID, false, false)
}

// RemoveContainerByContainerID removes a runtime container by concrete container id.
func (pm *ProcessManager) RemoveContainerByContainerID(containerID string) error {
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}
	err := pm.containerManager.RemoveContainerByID(pm.operationContext(containerID), containerID, false, false)
	if containerAlreadyRemoved(err) {
		return nil
	}
	return err
}

func containerAlreadyRemoved(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "already in removing")
}

// GetMicroserviceUUIDForContainer derives a microservice selector from a container.
func (pm *ProcessManager) GetMicroserviceUUIDForContainer(container engine.Container) string {
	if pm.engine == nil {
		return ""
	}
	return pm.engine.GetContainerMicroserviceUUID(container)
}

// ListImages returns local runtime images from the active engine.
func (pm *ProcessManager) ListImages() ([]engine.ImageInfo, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.ListImages(context.Background())
}

// PullImage pulls an image using optional registry credentials and platform selector.
func (pm *ProcessManager) PullImage(imageRef string, registry *models.Registry, platform string) error {
	if err := models.RequireOCIForImagePull(registry); err != nil {
		return err
	}
	if pm.engine == nil {
		return errors.New("process manager engine is not initialized")
	}
	return pm.engine.PullImage(imageRef, registry, &engine.PullImageOptions{Platform: platform})
}

// PullImageWithProgress pulls an image and reports progress percent when available.
func (pm *ProcessManager) PullImageWithProgress(imageRef string, registry *models.Registry, platform string, onProgress func(float32)) error {
	if err := models.RequireOCIForImagePull(registry); err != nil {
		return err
	}
	if pm.engine == nil {
		return errors.New("process manager engine is not initialized")
	}
	return pm.engine.PullImage(imageRef, registry, &engine.PullImageOptions{
		Platform:         platform,
		ProgressCallback: onProgress,
	})
}

// LoadImageFromPath imports an image archive from daemon-local path.
func (pm *ProcessManager) LoadImageFromPath(path string) ([]engine.LoadedImage, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.LoadImageFromPath(context.Background(), path)
}

// RemoveImage removes an image by ID or name reference.
func (pm *ProcessManager) RemoveImage(selector string) error {
	if pm.engine == nil {
		return errors.New("process manager engine is not initialized")
	}
	return pm.engine.DeleteImage(context.Background(), selector)
}

// PruneDanglingImages prunes dangling images.
func (pm *ProcessManager) PruneDanglingImages() (*engine.ImagePruneReport, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.PruneDangling(context.Background())
}

// PruneContainers prunes stopped/orphaned containers.
func (pm *ProcessManager) PruneContainers() (*engine.ContainerPruneReport, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.PruneContainers(context.Background())
}

// PruneVolumes prunes unused/orphaned volume artifacts.
func (pm *ProcessManager) PruneVolumes() (*engine.VolumePruneReport, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.PruneVolumes(context.Background())
}

// ListAllContainers returns every listed container from the active engine.
func (pm *ProcessManager) ListAllContainers() ([]engine.Container, error) {
	if pm == nil || pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.GetAllContainers()
}

// InspectContainerRaw returns full engine-native inspect payload for a container.
func (pm *ProcessManager) InspectContainerRaw(containerID string) (map[string]any, error) {
	if pm.engine == nil {
		return nil, errors.New("process manager engine is not initialized")
	}
	return pm.engine.InspectContainerRaw(containerID)
}

// GetContainerSandboxID returns the pause / sandbox id for a workload container.
func (pm *ProcessManager) GetContainerSandboxID(containerID string) (string, error) {
	if pm.engine == nil {
		return "", errors.New("process manager engine is not initialized")
	}
	return pm.engine.GetContainerSandboxID(containerID)
}

// LaunchLocalMicroservice creates and starts a locally deployed microservice.
func (pm *ProcessManager) LaunchLocalMicroservice(ms *models.Microservice, registry *models.Registry, hostIP string) (string, error) {
	return pm.LaunchLocalMicroserviceWithProgress(ms, registry, hostIP, nil)
}

// LaunchLocalMicroserviceWithProgress creates and starts a locally deployed microservice
// while reporting stage transitions.
func (pm *ProcessManager) LaunchLocalMicroserviceWithProgress(ms *models.Microservice, registry *models.Registry, hostIP string, progress LocalDeployProgressCallback) (string, error) {
	if pm.engine == nil {
		return "", errors.New("process manager engine is not initialized")
	}
	if ms == nil {
		return "", errors.New("microservice is nil")
	}
	return pm.withLocalLaunchLock(ms.MicroserviceUUID, func() (string, error) {
		return pm.launchLocalMicroserviceWithProgressLocked(ms, registry, hostIP, progress)
	})
}

func (pm *ProcessManager) launchLocalMicroserviceWithProgressLocked(ms *models.Microservice, registry *models.Registry, hostIP string, progress LocalDeployProgressCallback) (string, error) {
	if err := pm.gateLocalCatalog(ms); err != nil {
		return "", err
	}
	if strings.TrimSpace(hostIP) == "" {
		hostIP = network.GetInstance().GetCurrentIPAddress()
	}
	if registry != nil {
		if err := models.RequireOCIForImagePull(registry); err != nil {
			return "", err
		}
		emitLocalDeployProgress(progress, "pulling", "resolving and preparing image")
		pullRef, lookupRefs, fromCache := imageref.ResolveForRegistry(ms.ImageName, registry.URL)
		pullSucceeded := false
		opts := &engine.PullImageOptions{Platform: msPlatform(ms)}
		if !fromCache {
			if err := pm.engine.PullImage(pullRef, registry, opts); err != nil {
				pm.logger.Warnf("local pull failed for %s, continuing with cache: %v", pullRef, err)
			} else {
				pullSucceeded = true
			}
		}
		registryURL, lookupFromCache := imageref.MatchParamsOptional(registry.URL)
		matchedRef := ""
		for _, ref := range lookupRefs {
			exists, err := pm.engine.FindLocalImage(ref, registryURL, lookupFromCache)
			if err != nil {
				return "", err
			}
			if exists {
				matchedRef = ref
				break
			}
		}
		if matchedRef == "" {
			return "", fmt.Errorf("image not found in local cache for refs: %v", lookupRefs)
		}
		if pullSucceeded {
			ms.ImageName = pullRef
		} else {
			ms.ImageName = matchedRef
		}
	}
	emitLocalDeployProgress(progress, "creating", "creating container")
	containerID, err := pm.engine.CreateContainer(ms, hostIP)
	if err != nil {
		return "", err
	}
	if sandboxID, _ := pm.engine.GetContainerSandboxID(containerID); sandboxID != "" {
		_ = store.GetInstance().UpsertRuntimeContainerRef(ms.MicroserviceUUID, store.RuntimeScopeLocal, containerID, sandboxID)
	}
	emitLocalDeployProgress(progress, "starting", "starting container")
	if err := pm.engine.StartContainer(containerID); err != nil {
		_ = pm.engine.RemoveContainer(containerID, false)
		_ = store.GetInstance().DeleteRuntimeContainerRef(ms.MicroserviceUUID, store.RuntimeScopeLocal)
		return "", fmt.Errorf("failed to start local microservice runtime: %w", err)
	}
	return containerID, nil
}

// TailMicroserviceLogs returns bounded logs for one runtime microservice.
func (pm *ProcessManager) TailMicroserviceLogs(microserviceUUID string, cfg *engine.TailConfig) ([]map[string]any, error) {
	container, err := pm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return nil, err
	}
	if container == nil {
		return nil, fmt.Errorf("container not found for microservice: %s", microserviceUUID)
	}
	handler := &collectLogTailHandler{
		entries: make([]collectedLogLine, 0),
		done:    make(chan struct{}),
	}
	sessionID := fmt.Sprintf("edgeletapi-log-%d", time.Now().UnixNano())
	if err := pm.engine.TailContainerLogs(container.ID, sessionID, microserviceUUID, handler, cfg); err != nil {
		return nil, err
	}
	// Some engines stream bounded logs asynchronously; wait for completion so
	// non-follow requests return collected lines instead of racing empty output.
	select {
	case <-handler.done:
	case <-time.After(5 * time.Second):
	}
	if handler.err != nil {
		return nil, handler.err
	}
	result := make([]map[string]any, 0, len(handler.entries))
	for _, item := range handler.entries {
		result = append(result, map[string]any{
			"ts":     item.ts,
			"stream": item.stream,
			"line":   item.line,
		})
	}
	return result, nil
}

// StreamMicroserviceLogs streams logs for one runtime microservice using the provided tail handler.
func (pm *ProcessManager) StreamMicroserviceLogs(microserviceUUID string, cfg *engine.TailConfig, handler engine.LogTailHandler) error {
	if pm.engine == nil {
		return errors.New("process manager engine is not initialized")
	}
	if handler == nil {
		return errors.New("log tail handler is nil")
	}
	container, err := pm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	if container == nil {
		return fmt.Errorf("container not found for microservice: %s", microserviceUUID)
	}
	sessionID := fmt.Sprintf("edgeletapi-log-stream-%d", time.Now().UnixNano())
	return pm.engine.TailContainerLogs(container.ID, sessionID, microserviceUUID, handler, cfg)
}

// ExecSessionCallbackInterface defines the interface for exec session callbacks
type ExecSessionCallbackInterface interface {
	GetStdinReader() io.Reader
	GetStdoutWriter() io.Writer
	GetStderrWriter() io.Writer
	OnComplete()
	OnError(err error)
	IsRunning() bool
}

type collectedLogLine struct {
	ts     string
	stream string
	line   string
}

type collectLogTailHandler struct {
	mu      sync.Mutex
	entries []collectedLogLine
	err     error
	done    chan struct{}
	once    sync.Once
}

func (h *collectLogTailHandler) OnLogLine(_, _ string, line []byte, st engine.StreamType) {
	stream := "stdout"
	if st == engine.Stderr {
		stream = "stderr"
	}
	h.mu.Lock()
	h.entries = append(h.entries, collectedLogLine{
		ts:     time.Now().UTC().Format(time.RFC3339Nano),
		stream: stream,
		line:   string(line),
	})
	h.mu.Unlock()
}

func (h *collectLogTailHandler) OnComplete(_ string) {
	h.once.Do(func() {
		close(h.done)
	})
}

func (h *collectLogTailHandler) OnError(_ string, err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
	h.once.Do(func() {
		close(h.done)
	})
}

// CreateExecSession creates and starts an exec session for a microservice.
// Legacy field-agent path — returns the runtime exec id (not wire sessionId).
// Prefer CreateControllerExecSession / CreateLocalExecSession for Plan 23 multi-session exec.
func (pm *ProcessManager) CreateExecSession(microserviceUUID string, command []string, callback ExecSessionCallbackInterface) (string, error) {
	pm.logger.Infof("Creating exec session for microservice: %s", microserviceUUID)

	container, err := pm.containerManager.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return "", fmt.Errorf("failed to get container: %w", err)
	}
	if container == nil {
		return "", fmt.Errorf("container not found for microservice: %s", microserviceUUID)
	}

	sessionID := uuid.NewString()
	execIDHint := runtimeExecIDController(container.ID, sessionID)
	engineExecID, err := pm.prepareAndCreateExec(container.ID, execIDHint, command)
	if err != nil {
		return "", err
	}
	pm.launchExecSessionIO(engineExecID, callback)
	pm.logger.Infof("Exec session started: %s for microservice: %s", engineExecID, microserviceUUID)
	return engineExecID, nil
}

// GetExecSessionStatus reports whether the exec process identified by execID is running.
func (pm *ProcessManager) GetExecSessionStatus(execID string) (any, error) {
	running, err := pm.engine.GetExecSessionStatus(execID)
	if err != nil {
		return nil, err
	}
	return running, nil
}

// GetExecSessionExitCode returns the exit status for completed exec sessions.
func (pm *ProcessManager) GetExecSessionExitCode(execID string) (int, error) {
	return pm.engine.GetExecSessionExitCode(execID)
}

// ResizeExecSession resizes a running tty exec session.
func (pm *ProcessManager) ResizeExecSession(execID string, cols, rows uint32) error {
	return pm.engine.ResizeExecSession(execID, cols, rows)
}

// StopExecSession kills and deregisters the exec process in the engine.
// Called when the controller closes the WebSocket so the exec ID can be reused.
func (pm *ProcessManager) StopExecSession(_ string, execID string) error {
	return pm.engine.StopExecSession(execID)
}

func msPlatform(ms *models.Microservice) string {
	if ms == nil || ms.Platform == nil {
		return ""
	}
	return strings.TrimSpace(*ms.Platform)
}
