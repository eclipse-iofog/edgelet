package fieldagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/gps"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/serviceaccount"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/version"
)

// workerFreq returns a non-zero duration, falling back to the default if the
// configured value is zero or negative (a zero timer would fire immediately in
// a tight loop and a zero ticker panics).
func workerFreq(seconds int, defaultSeconds int) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(defaultSeconds) * time.Second
}

const (
	pingBackoffMaxSeconds          = 300 // 5 min max backoff when controller offline
	changesProcessTimeout          = 5 * time.Minute
	localAPITokenRotationInterval  = 30 * time.Second
	serviceAccountRotationInterval = 30 * time.Second
	upgradeScanPollInterval        = 500 * time.Millisecond
	defaultUpgradeScanHours        = 24
)

func changesWorkerFrequency() time.Duration {
	return workerFreq(config.GetInstance().ChangeFrequency, 20)
}

// pingControllerWorker periodically pings the controller.
// Uses exponential backoff when controller is unreachable (edge resilience:
// avoid hammering controller when offline; agent continues with last config).
func (fa *FieldAgent) pingControllerWorker() {
	defer fa.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()

	cfg := config.GetInstance()
	baseInterval := workerFreq(cfg.PingControllerFreqSeconds, 30)
	interval := baseInterval
	consecutiveFailures := 0

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			if !fa.NotProvisioned() {
				logging.LogDebug(moduleName, "Start Ping controller")
				ok, transitioned := fa.pingWithTransition()
				logging.LogDebug(moduleName, "Finished Ping controller")

				if ok {
					consecutiveFailures = 0
					interval = baseInterval
					if transitioned {
						fa.runControllerReconcileAsync()
					}
				} else {
					consecutiveFailures++
					// Exponential backoff: 30s, 60s, 120s, ... cap at 5 min
					backoffSec := cfg.PingControllerFreqSeconds
					if backoffSec < 30 {
						backoffSec = 30
					}
					for i := 0; i < consecutiveFailures-1 && backoffSec < pingBackoffMaxSeconds; i++ {
						backoffSec *= 2
					}
					if backoffSec > pingBackoffMaxSeconds {
						backoffSec = pingBackoffMaxSeconds
					}
					interval = time.Duration(backoffSec) * time.Second
					logging.LogInfo(moduleName, fmt.Sprintf("Controller unreachable, next ping in %v (backoff)", interval))
				}
			}
			timer.Reset(interval)
		}
	}
}

// runChangesWorker periodically gets changes from the controller.
// Uses a self-resetting timer so the interval is re-read from config on every tick.
func (fa *FieldAgent) runChangesWorker() {
	defer fa.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered in changes worker", fmt.Errorf("%v\n%s", r, debug.Stack()))
		}
	}()

	timer := time.NewTimer(changesWorkerFrequency())
	defer timer.Stop()

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			fa.changesWorkerTick(timer)
		}
	}
}

func (fa *FieldAgent) changesWorkerTick(timer *time.Timer) {
	defer timer.Reset(changesWorkerFrequency())
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered in changes worker tick", fmt.Errorf("%v\n%s", r, debug.Stack()))
		}
	}()

	logging.LogDebug(moduleName, "Start get IOFog changes list from IOFog controller")

	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		logging.LogDebug(moduleName, "Cannot get change list due to controller status not provisioned or controller not connected")
		return
	}

	apiClient := fa.getAPIClient()
	if apiClient == nil {
		logging.LogError(moduleName, "Unable to get changes", errors.New("api client is not initialized"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	result, err := apiClient.Request(ctx, "config/changes", GET, nil, nil)
	cancel()

	if err != nil {
		if isCertificateError(err) {
			fa.verificationFailed(err)
			logging.LogError(moduleName, "Unable to get changes due to broken certificate", err)
		} else {
			logging.LogError(moduleName, "Unable to get changes", err)
		}
		return
	}

	fa.state.SetLastCommandTime(fa.state.GetLastGetChangesList())

	lastUpdated, ok := result["lastUpdated"].(string)
	if !ok {
		lastUpdated = ""
	}
	logging.LogDebug(moduleName, fmt.Sprintf("Processing changes with lastUpdated: %s", lastUpdated))

	resetChanges := fa.processChangesInWorker(result)

	if lastUpdated != "" && resetChanges {
		logging.LogDebug(moduleName, fmt.Sprintf("Resetting config changes flags with lastUpdated: %s", lastUpdated))
		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		err := apiClient.PatchJSON(ctx2, "config/changes", map[string]any{
			"lastUpdated": lastUpdated,
		})
		cancel2()

		if err != nil {
			logging.LogError(moduleName, "Resetting config changes has failed", err)
		} else {
			logging.LogDebug(moduleName, "Successfully reset config changes flags")
		}
	}

	fa.state.SetInitialization(fa.state.IsInitialization() && !resetChanges)
	logging.LogDebug(moduleName, fmt.Sprintf("Finished getChangesList cycle with initialization: %v", fa.state.IsInitialization()))
}

func (fa *FieldAgent) processChangesInWorker(changes map[string]any) bool {
	if fa.processChangesFn != nil {
		return fa.processChangesFn(changes)
	}
	return fa.processChangesWithTimeout(changes)
}

func (fa *FieldAgent) processChangesWithTimeout(changes map[string]any) bool {
	ctx, cancel := context.WithTimeout(fa.ctx, changesProcessTimeout)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, "Panic recovered in processChanges", fmt.Errorf("%v\n%s", r, debug.Stack()))
				done <- false
			}
		}()
		done <- fa.processChanges(changes)
	}()

	select {
	case reset := <-done:
		return reset
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logging.LogError(moduleName, "processChanges exceeded time limit", ctx.Err())
		}
		return false
	}
}

// postStatusWorker periodically posts status to the controller.
// Uses a self-resetting timer so the interval is re-read from config on every tick.
func (fa *FieldAgent) postStatusWorker() {
	defer fa.wg.Done()

	timer := time.NewTimer(workerFreq(config.GetInstance().StatusFrequency, 10))
	defer timer.Stop()

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			fa.PostStatusHelper()
			timer.Reset(workerFreq(config.GetInstance().StatusFrequency, 10))
		}
	}
}

// PostStatusHelper posts the fog status to the controller
// Exported for use by Edge Guard to immediately notify controller of hardware changes
func (fa *FieldAgent) PostStatusHelper() {
	logging.LogDebug(moduleName, "posting ioFog status")

	status := fa.getFogStatus()

	cfg := config.GetInstance()
	if cfg.Debugging {
		logging.LogInfo(moduleName, fmt.Sprintf("Status: %+v", status))
	}

	connected := fa.IsControllerConnected(false)
	if !connected {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := fa.putStatus(ctx, status)
	cancel()

	if err != nil {
		if isCertificateError(err) {
			fa.verificationFailed(err)
			logging.LogError(moduleName, "Unable to send status due to broken certificate", err)
		} else if IsRetryableControllerError(err) {
			logging.LogWarn(moduleName, fmt.Sprintf("Unable to send status (retryable): %v", err))
		} else if fa.shouldSuppressStatusAuthDeprovision() {
			logging.LogWarn(moduleName, fmt.Sprintf("Unable to send status due to auth failure; deprovision suppressed: %v", err))
		} else if isStatusAuthFailure(err) {
			now := fa.statusAuthNow()
			fa.recordStatusAuthFailure(now)
			if !fa.shouldDeprovisionForStatusAuth(now) {
				logging.LogWarn(moduleName, fmt.Sprintf(
					"status auth failure (attempt %d/%d); deferring deprovision",
					fa.statusAuthFailureCount(), statusDeprovision401MaxAttempts))
			} else if !fa.NotProvisioned() {
				if depErr := fa.invokeDeprovision(true); depErr != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Deprovision failed: %v", depErr))
				}
			}
			logging.LogError(moduleName, "Unable to send status due to unauthorized access", err)
		} else {
			logging.LogError(moduleName, "Unable to send status", err)
		}
		logging.LogDebug(moduleName, "Finished posting ioFog status")
		return
	}
	fa.resetStatusAuthFailure()
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
		pmStatus.RemoveNotRunningMicroserviceStatus()
	})

	logging.LogDebug(moduleName, "Finished posting ioFog status")
}

func (fa *FieldAgent) putStatus(ctx context.Context, status map[string]any) error {
	if fa.postStatusFn != nil {
		return fa.postStatusFn(ctx, status)
	}
	apiClient := fa.getAPIClient()
	if apiClient == nil {
		return errors.New("api client is not initialized")
	}
	return apiClient.PutJSON(ctx, "status", status)
}

// getFogStatus creates the fog status report
func (fa *FieldAgent) getFogStatus() map[string]any {
	logging.LogDebug(moduleName, "get Fog Status")

	// Get StatusReporter instance to get status from all modules
	statusReporter := statusreporter.GetInstance()

	supervisorStatus := statusReporter.GetSupervisorStatus()
	resourceConsumptionStatus := statusReporter.GetResourceConsumptionManagerStatus()
	processManagerStatus := statusReporter.GetProcessManagerStatus()
	fieldAgentStatus := statusReporter.GetFieldAgentStatus()
	statusReporterStatus := statusReporter.GetStatusReporterStatus()
	sshManagerStatus := statusReporter.GetSSHProxyManagerStatus()
	volumeMountStatus := statusReporter.GetVolumeMountManagerStatus()

	// Get daemon status string
	daemonStatusStr := "UNKNOWN"
	if supervisorStatus.DaemonStatus != "" {
		daemonStatusStr = string(supervisorStatus.DaemonStatus)
	}

	// Get microservice status JSON
	microserviceStatusJSON := processManagerStatus.GetJSONMicroservicesStatus()
	if microserviceStatusJSON == "" {
		microserviceStatusJSON = "[]"
	}
	microserviceStatusJSON = enrichMicroserviceStatusExecSessionIDs(
		microserviceStatusJSON,
		GetExecSessionManager().ListActiveControllerSessionIDs,
	)
	microserviceStatusJSON = annotateMicroserviceStatusForControlRestart(microserviceStatusJSON)

	// Get repository status JSON
	repositoryStatusJSON := processManagerStatus.GetJSONRegistriesStatus()
	if repositoryStatusJSON == "" {
		repositoryStatusJSON = "[]"
	}

	// Get tunnel status JSON
	tunnelStatusJSON := sshManagerStatus.GetJSONProxyStatus()
	if tunnelStatusJSON == "" {
		tunnelStatusJSON = "{}"
	}

	// Get warning message
	warningMessage := supervisorStatus.WarningMessage
	if warningMessage == "" {
		warningMessage = ""
	}
	controllerRuntimes := runtimeNamesForController(
		fa.config.ContainerEngine,
		statusreporter.GetAvailableRuntimes(),
	)

	status := map[string]any{
		"daemonStatus":              daemonStatusStr,
		"daemonOperatingDuration":   supervisorStatus.GetOperationDuration(),
		"daemonLastStart":           supervisorStatus.DaemonLastStart,
		"warningMessage":            warningMessage,
		"memoryUsage":               resourceConsumptionStatus.MemoryUsage,
		"diskUsage":                 resourceConsumptionStatus.DiskUsage,
		"cpuUsage":                  resourceConsumptionStatus.CPUUsage,
		"memoryViolation":           resourceConsumptionStatus.MemoryViolation,
		"diskViolation":             resourceConsumptionStatus.DiskViolation,
		"cpuViolation":              resourceConsumptionStatus.CPUViolation,
		"systemAvailableDisk":       float64(resourceConsumptionStatus.AvailableDisk),
		"systemAvailableMemory":     float64(resourceConsumptionStatus.AvailableMemory),
		"systemTotalCpu":            resourceConsumptionStatus.TotalCPU,
		"microserviceStatus":        microserviceStatusJSON,
		"repositoryCount":           len(processManagerStatus.GetRegistriesStatus()),
		"repositoryStatus":          repositoryStatusJSON,
		"systemTime":                statusReporterStatus.SystemTime,
		"lastStatusTime":            statusReporterStatus.LastUpdate,
		"ipAddress":                 network.GetInstance().GetCurrentIPAddress(), // Get from NetworkInterfaceManager
		"ipAddressExternal":         fa.config.IPAddressExternal,
		"microserviceMessageCounts": "[]",
		"lastCommandTime":           fieldAgentStatus.LastCommandTime,
		"tunnelStatus":              tunnelStatusJSON,
		"version":                   version.GetVersion(), // Version from build-time ldflags
		"isReadyToUpgrade":          fieldAgentStatus.ReadyToUpgrade,
		"isReadyToRollback":         fieldAgentStatus.ReadyToRollback,
		"activeVolumeMounts":        volumeMountStatus.ActiveMounts,
		"volumeMountLastUpdate":     volumeMountStatus.LastUpdate,
		"gpsStatus":                 string(gps.GetInstance().GetStatus().GetHealthStatus()), // Get from GpsManager
		"availableRuntimes":         controllerRuntimes,
		"runtimeClasses":            statusreporter.GetAppliedRuntimeClasses(),
		"availableCdiDevices":       statusreporter.GetAvailableCDIDevices(),
	}

	modelStatus, activeModels, modelLastUpdate := fa.fogModelStatus()
	status["modelStatus"] = modelStatus
	status["activeModels"] = activeModels
	status["modelLastUpdate"] = modelLastUpdate

	knowledgeStatus, activeKnowledge, knowledgeLastUpdate := fa.fogKnowledgeStatus()
	status["knowledgeStatus"] = knowledgeStatus
	status["activeKnowledge"] = activeKnowledge
	status["knowledgeLastUpdate"] = knowledgeLastUpdate

	if phase := runtimestate.GetState().AgentPhase(); phase != "" {
		status["runtimeAgentPhase"] = phase
	}
	if processmanager.IsQuiesced() {
		status["controlPlaneQuiesced"] = true
	}

	return status
}

func annotateMicroserviceStatusForControlRestart(rawJSON string) string {
	phase := runtimestate.GetState().AgentPhase()
	if phase != "restarting" && !processmanager.IsQuiesced() {
		return rawJSON
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &items); err != nil || len(items) == 0 {
		return rawJSON
	}
	for _, item := range items {
		item["controlRestart"] = true
		if phase == "restarting" {
			item["agentPhase"] = phase
		}
	}
	out, err := json.Marshal(items)
	if err != nil {
		return rawJSON
	}
	return string(out)
}

func runtimeNamesForController(_ string, available []string) []string {
	filteredSet := make(map[string]struct{}, len(available))
	for _, runtimeName := range available {
		name := strings.TrimSpace(runtimeName)
		if name == "" {
			continue
		}
		filteredSet[name] = struct{}{}
	}
	filtered := make([]string, 0, len(filteredSet))
	for name := range filteredSet {
		filtered = append(filtered, name)
	}
	slices.Sort(filtered)
	return filtered
}

// isUnauthorizedError checks if an error is an unauthorized error
func isUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(strings.ToLower(errStr), "unauthorized") ||
		strings.Contains(strings.ToLower(errStr), "401")
}

func (fa *FieldAgent) localAPITokenRotationWorker() {
	defer fa.wg.Done()
	timer := time.NewTimer(localAPITokenRotationInterval)
	defer timer.Stop()

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			token, err := auth.GetLocalTokenManager().LoadToken()
			if err != nil {
				if reconcileErr := auth.EnsureEdgeletAPITokenForCurrentState(); reconcileErr != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("edgelet-api token reconcile failed: %v", reconcileErr))
				}
				timer.Reset(localAPITokenRotationInterval)
				continue
			}
			rotate, err := auth.ShouldRotateEdgeletAPIToken(token, time.Now())
			if err != nil || rotate {
				if reconcileErr := auth.EnsureEdgeletAPITokenForCurrentState(); reconcileErr != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("edgelet-api token rotation failed: %v", reconcileErr))
				}
			}
			timer.Reset(localAPITokenRotationInterval)
		}
	}
}

// upgradeScanWorker periodically evaluates OTA readiness for controller status.
// It waits until the supervisor is operational before each scan and resets its
// timer immediately when upgradeScanFrequency changes.
func (fa *FieldAgent) upgradeScanWorker() {
	defer fa.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered in upgrade scan worker", fmt.Errorf("%v", r))
		}
	}()

	fa.ensureUpgradeScanRescheduleChan()

	timer := time.NewTimer(upgradeScanInterval())
	defer stopTimer(timer)
	fa.setAppliedUpgradeScanFrequency(fa.upgradeScanFrequencyHours())

	runScan := func() bool {
		if !fa.waitForDaemonOperational(fa.ctx) {
			return false
		}
		fa.scanVersionReadiness()
		return true
	}

	if !runScan() {
		return
	}

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			if !runScan() {
				return
			}
			fa.resetUpgradeScanTimer(timer)
		case <-fa.upgradeScanReschedule:
			if fa.upgradeScanFrequencyChanged() {
				if !runScan() {
					return
				}
			}
			fa.resetUpgradeScanTimer(timer)
		}
	}
}

func upgradeScanInterval() time.Duration {
	return time.Duration(upgradeScanFrequencyHours(config.GetInstance().UpgradeScanFrequency)) * time.Hour
}

func upgradeScanFrequencyHours(hours int) int {
	if hours <= 0 {
		return defaultUpgradeScanHours
	}
	return hours
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (fa *FieldAgent) ensureUpgradeScanRescheduleChan() {
	fa.upgradeScanMu.Lock()
	defer fa.upgradeScanMu.Unlock()
	if fa.upgradeScanReschedule == nil {
		fa.upgradeScanReschedule = make(chan struct{}, 1)
	}
}

func (fa *FieldAgent) notifyUpgradeScanReschedule() {
	fa.ensureUpgradeScanRescheduleChan()
	select {
	case fa.upgradeScanReschedule <- struct{}{}:
	default:
	}
}

func (fa *FieldAgent) rescheduleUpgradeScanIfFrequencyChanged() {
	freq := fa.upgradeScanFrequencyHours()
	fa.upgradeScanMu.Lock()
	prev := fa.appliedUpgradeScanFrequency
	changed := prev != 0 && prev != freq
	fa.upgradeScanMu.Unlock()
	if changed {
		logging.LogDebug(moduleName, fmt.Sprintf("Rescheduling upgrade scan worker (frequency %dh -> %dh)", prev, freq))
		fa.notifyUpgradeScanReschedule()
	}
}

func (fa *FieldAgent) setAppliedUpgradeScanFrequency(hours int) {
	fa.upgradeScanMu.Lock()
	fa.appliedUpgradeScanFrequency = hours
	fa.upgradeScanMu.Unlock()
}

func (fa *FieldAgent) upgradeScanFrequencyHours() int {
	return upgradeScanFrequencyHours(config.GetInstance().UpgradeScanFrequency)
}

func (fa *FieldAgent) upgradeScanFrequencyChanged() bool {
	fa.upgradeScanMu.Lock()
	defer fa.upgradeScanMu.Unlock()
	return fa.appliedUpgradeScanFrequency != 0 &&
		fa.appliedUpgradeScanFrequency != fa.upgradeScanFrequencyHours()
}

func (fa *FieldAgent) waitForDaemonOperational(ctx context.Context) bool {
	if statusreporter.GetInstance().DaemonOperational() {
		return true
	}
	ticker := time.NewTicker(upgradeScanPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if statusreporter.GetInstance().DaemonOperational() {
				return true
			}
		}
	}
}

func (fa *FieldAgent) resetUpgradeScanTimer(timer *time.Timer) {
	stopTimer(timer)
	timer.Reset(upgradeScanInterval())
	fa.setAppliedUpgradeScanFrequency(fa.upgradeScanFrequencyHours())
}

func (fa *FieldAgent) scanVersionReadiness() {
	versionHandler := version.GetInstance()
	readyUpgrade := versionHandler.IsReadyToUpgrade()
	readyRollback := versionHandler.IsReadyToRollback()

	statusreporter.GetInstance().UpdateFieldAgentStatus(func(status *models.FieldAgentStatus) {
		status.ReadyToUpgrade = readyUpgrade
		status.ReadyToRollback = readyRollback
	})

	fa.retryOTAReprovisionIfNeeded()
}

func (fa *FieldAgent) serviceAccountTokenRotationWorker() {
	defer fa.wg.Done()
	timer := time.NewTimer(serviceAccountRotationInterval)
	defer timer.Stop()

	for {
		select {
		case <-fa.ctx.Done():
			return
		case <-timer.C:
			if !fa.NotProvisioned() {
				if err := serviceaccount.GetInstance().RotateExpiringManagedTokens(fa.GetLatestMicroservices(), time.Now()); err != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("service-account token rotation failed: %v", err))
				}
			}
			timer.Reset(serviceAccountRotationInterval)
		}
	}
}
