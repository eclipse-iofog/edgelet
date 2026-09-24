package statusreporter

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/cdidevices"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	moduleName = "Status Reporter"
	// Number of modules
	numberOfModules = 7
)

// StatusReporter aggregates and reports status from all modules
type StatusReporter struct {
	config *config.Config
	mu     sync.RWMutex

	// Status objects for each module
	supervisorStatus                 *models.SupervisorStatus
	resourceConsumptionManagerStatus *models.ResourceConsumptionManagerStatus
	fieldAgentStatus                 *models.FieldAgentStatus
	statusReporterStatus             *models.StatusReporterStatus
	processManagerStatus             *models.ProcessManagerStatus
	localAPIStatus                   *models.EdgeletAPIStatus
	sshProxyManagerStatus            *models.SSHProxyManagerStatus
	volumeMountManagerStatus         *models.VolumeMountManagerStatus

	// Context for background workers
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	instance *StatusReporter
	once     sync.Once
)

var listRuntimeClassesForStatus = func() ([]*models.LocalRuntimeClass, error) {
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return nil, nil
	}
	return db.ListLocalRuntimeClasses()
}

// GetInstance returns the singleton StatusReporter instance
func GetInstance() *StatusReporter {
	once.Do(func() {
		instance = &StatusReporter{
			config:                           config.GetInstance(),
			supervisorStatus:                 models.NewSupervisorStatus(numberOfModules),
			resourceConsumptionManagerStatus: models.NewResourceConsumptionManagerStatus(),
			fieldAgentStatus:                 models.NewFieldAgentStatus(),
			statusReporterStatus:             models.NewStatusReporterStatus(),
			processManagerStatus:             models.NewProcessManagerStatus(),
			localAPIStatus:                   models.NewEdgeletAPIStatus(),
			sshProxyManagerStatus:            models.NewSSHProxyManagerStatus(),
			volumeMountManagerStatus:         models.NewVolumeMountManagerStatus(),
		}
	})
	return instance
}

// Start starts the StatusReporter
func (sr *StatusReporter) Start() error {
	logging.LogInfo(moduleName, "Starting Status Reporter")

	// Create context for cancellation
	sr.ctx, sr.cancel = context.WithCancel(context.Background())

	// Start background worker to update system time
	sr.wg.Add(1)
	go sr.setSystemTimeWorker()

	logging.LogInfo(moduleName, "Started Status Reporter")
	return nil
}

// Stop stops the StatusReporter
func (sr *StatusReporter) Stop() error {
	logging.LogDebug(moduleName, "Stopping Status Reporter")

	if sr.cancel != nil {
		sr.cancel()
	}

	// Wait for all workers to finish
	sr.wg.Wait()

	logging.LogDebug(moduleName, "Status Reporter stopped")
	return nil
}

// setSystemTimeWorker periodically updates the system time in status
func (sr *StatusReporter) setSystemTimeWorker() {
	defer sr.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()

	cfg := sr.config
	ticker := time.NewTicker(time.Duration(cfg.SetSystemTimeFreqSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sr.ctx.Done():
			return
		case <-ticker.C:
			logging.LogDebug(moduleName, "Inside setStatusReporterSystemTime")
			sr.mu.Lock()
			nowMs := time.Now().UnixMilli()
			sr.statusReporterStatus.SetSystemTime(nowMs)
			sr.mu.Unlock()
			logging.LogDebug(moduleName, "Finished setStatusReporterSystemTime")
		}
	}
}

// GetStatusReport returns a formatted status report string (for CLI)
func (sr *StatusReporter) GetStatusReport() string {
	logging.LogInfo(moduleName, "Start Getting Status Report")

	sr.mu.RLock()
	defer sr.mu.RUnlock()

	var result string
	rcs := sr.resourceConsumptionManagerStatus
	diskUsage := rcs.DiskUsage
	availableDisk := float64(rcs.AvailableDisk) / 1024.0 / 1024.0
	availableMemory := float64(rcs.AvailableMemory) / 1024.0 / 1024.0
	hostCPU := rcs.TotalCPU
	memoryUsage := rcs.MemoryUsage
	cpuUsage := rcs.CPUUsage

	logging.LogDebug(moduleName, fmt.Sprintf(
		"Status values: agentCPU=%.2f runtimeCPU=%.2f totalCPU=%.2f agentMem=%.2f MiB runtimeMem=%.2f MiB totalMem=%.2f MiB hostCPU=%.2f%%",
		rcs.AgentCPUPercent, rcs.RuntimeCPUPercent, cpuUsage, rcs.AgentMemoryMiB, rcs.RuntimeMemoryMiB, memoryUsage, hostCPU,
	))

	// Get connection status
	var connectionStatus string
	currentStatus := sr.fieldAgentStatus.ControllerStatus
	logging.LogDebug(moduleName, fmt.Sprintf("Current controller status from StatusReporter: %s (ControllerVerified: %v)",
		currentStatus, sr.fieldAgentStatus.ControllerVerified))

	switch currentStatus {
	case models.ControllerStatusNotProvisioned:
		connectionStatus = "not provisioned"
	case models.ControllerStatusBrokenCertificate:
		connectionStatus = "broken certificate"
	case models.ControllerStatusNotConnected:
		connectionStatus = "not connected"
	case models.ControllerStatusOK:
		connectionStatus = "ok"
	default:
		// Default to "not connected" if status is unknown
		logging.LogDebug(moduleName, fmt.Sprintf("Unknown controller status: %s, defaulting to 'not connected'", currentStatus))
		connectionStatus = "not connected"
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Connection status for report: %s", connectionStatus))

	// Format system time
	systemTime := time.UnixMilli(sr.statusReporterStatus.SystemTime)
	dateFormat := systemTime.Format("02/01/2006 03:04 PM")

	// Get daemon status
	daemonStatus := string(sr.supervisorStatus.DaemonStatus)
	if daemonStatus == "" {
		// Default to RUNNING if not set (shouldn't happen, but safety check)
		daemonStatus = "RUNNING"
	}
	result += fmt.Sprintf("Edgelet daemon                : %s\n", daemonStatus)
	result += fmt.Sprintf("Agent CPU                     : %.6f\n", rcs.AgentCPUPercent/100.0)
	result += fmt.Sprintf("Agent memory                  : %d\n", stackMemoryMiBToBytes(rcs.AgentMemoryMiB))
	if rcs.RuntimeTracked {
		result += fmt.Sprintf("Runtime CPU                   : %.6f\n", rcs.RuntimeCPUPercent/100.0)
		result += fmt.Sprintf("Runtime memory                : %d\n", stackMemoryMiBToBytes(rcs.RuntimeMemoryMiB))
		result += fmt.Sprintf("Runtime available             : %t\n", rcs.RuntimeAvailable)
		if rcs.RuntimeDegraded {
			result += "Runtime degraded              : true\n"
		}
	}
	result += fmt.Sprintf("Memory Usage                : %.2f MiB\n", memoryUsage)
	if diskUsage < 1 {
		result += fmt.Sprintf("Disk Usage                  : %.2f MiB\n", diskUsage*1024)
	} else {
		result += fmt.Sprintf("Disk Usage                  : %.2f GiB\n", diskUsage)
	}
	result += fmt.Sprintf("CPU Usage                   : %.4f\n", cpuUsage)
	result += fmt.Sprintf("Running Microservices       : %d\n", sr.processManagerStatus.RunningMicroservicesCount)
	result += fmt.Sprintf("Connection to Controller    : %s\n", connectionStatus)
	result += fmt.Sprintf("System Time                 : %s\n", dateFormat)

	// Calculate total disk for percentage
	totalDisk := float64(rcs.TotalDiskSpace) / 1024.0 / 1024.0
	diskPercent := 0.0
	if totalDisk > 0 {
		diskPercent = (availableDisk / totalDisk) * 100.0
	}

	result += fmt.Sprintf("System CPUs                 : %d\n", rcs.SystemCpus)
	result += fmt.Sprintf("System OS                   : %s\n", rcs.SystemOs)
	if rcs.SystemOsVersion != "" {
		result += fmt.Sprintf("System OS version           : %s\n", rcs.SystemOsVersion)
	}
	if rcs.SystemKernelVersion != "" {
		result += fmt.Sprintf("System kernel version       : %s\n", rcs.SystemKernelVersion)
	}
	result += fmt.Sprintf("System Total Memory         : %d\n", rcs.SystemTotalMemory)
	result += fmt.Sprintf("System Total Disk           : %d\n", rcs.TotalDiskSpace)
	result += fmt.Sprintf("System Available Disk       : %.2f MB (%.2f %%)\n", availableDisk, diskPercent)
	result += fmt.Sprintf("System Available Memory     : %.2f MB\n", availableMemory)
	result += fmt.Sprintf("System Total CPU            : %.2f %%\n", hostCPU)
	result += fmt.Sprintf("Edgelet stack CPU             : %.6f\n", rcs.EdgeletTotalCPUPercent/100.0)
	result += fmt.Sprintf("Edgelet stack memory          : %d\n", stackMemoryMiBToBytes(rcs.EdgeletTotalMemoryMiB))
	availableInterfaces := getAvailableNetworkInterfaces()
	availableInterfacesLine := "none"
	if len(availableInterfaces) > 0 {
		availableInterfacesLine = strings.Join(availableInterfaces, ", ")
	}
	result += fmt.Sprintf("Available Network Interfaces : %s\n", availableInterfacesLine)
	availableRuntimesLine := strings.Join(GetAvailableRuntimes(), ", ")
	result += fmt.Sprintf("Available Runtimes          : %s\n", availableRuntimesLine)
	result += fmt.Sprintf("Runtime Classes             : %s\n", formatRuntimeClassesLine(GetAppliedRuntimeClasses()))
	result += fmt.Sprintf("Available CDI Devices       : %s\n", strings.Join(GetAvailableCDIDevices(), ", "))

	logging.LogDebug(moduleName, "Finished Getting Status Report")
	return result
}

// GetAvailableRuntimes returns deterministic runtime names exposed by current engine mode.
func GetAvailableRuntimes() []string {
	cfg := config.GetInstance()
	if cfg == nil {
		return []string{constants.EngineDocker}
	}
	return getAvailableRuntimesForEngine(strings.ToLower(strings.TrimSpace(cfg.ContainerEngine)), embeddedEdgeletRuntimes(cfg.ContainerEngine))
}

func embeddedEdgeletRuntimes(engineName string) bool {
	return buildmeta.HasEmbeddedEngine() && strings.EqualFold(strings.TrimSpace(engineName), constants.EngineEdgelet)
}

func getAvailableRuntimesForEngine(engineName string, embeddedEdgelet bool) []string {
	switch engineName {
	case constants.EnginePodman:
		if !embeddedEdgelet {
			if external, err := listExternalRuntimesForStatus(constants.EnginePodman); err == nil && len(external) > 0 {
				return sortedUniqueStrings(external)
			}
		}
		return []string{constants.EnginePodman}
	case constants.EngineEdgelet:
		baseline := []string{"crun"}
		if !embeddedEdgelet {
			return baseline
		}

		extrasMap := map[string]struct{}{}
		items, err := listRuntimeClassesForStatus()
		if err == nil {
			for _, rc := range items {
				if rc == nil {
					continue
				}
				rc.Normalize()
				if rc.RuntimeName != "" {
					extrasMap[rc.RuntimeName] = struct{}{}
				}
			}
		}
		for _, handler := range listCatalogRuntimesForStatus() {
			handler = strings.TrimSpace(handler)
			if handler == "" {
				continue
			}
			extrasMap[handler] = struct{}{}
		}

		extras := make([]string, 0, len(extrasMap))
		for runtimeName := range extrasMap {
			if runtimeName == "crun" {
				continue
			}
			extras = append(extras, runtimeName)
		}
		slices.Sort(extras)

		runtimes := make([]string, 0, len(baseline)+len(extras))
		runtimes = append(runtimes, baseline...)
		runtimes = append(runtimes, extras...)
		return runtimes
	case constants.EngineDocker:
		if !embeddedEdgelet {
			if external, err := listExternalRuntimesForStatus(constants.EngineDocker); err == nil && len(external) > 0 {
				return sortedUniqueStrings(external)
			}
		}
		return []string{constants.EngineDocker}
	default:
		return []string{constants.EngineDocker}
	}
}

func sortedUniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for item := range set {
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}

// GetAppliedRuntimeClasses returns applied RuntimeClasses for status (sorted by name).
// Docker and podman report an empty list.
func GetAppliedRuntimeClasses() (out []models.RuntimeClassStatus) {
	out = make([]models.RuntimeClassStatus, 0)
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("applied runtime class status panicked: %v", r))
			out = []models.RuntimeClassStatus{}
		}
	}()
	cfg := config.GetInstance()
	engineName := ""
	if cfg != nil {
		engineName = cfg.ContainerEngine
	}
	if !strings.EqualFold(strings.TrimSpace(engineName), constants.EngineEdgelet) {
		return out
	}
	items, err := listRuntimeClassesForStatus()
	if err != nil || len(items) == 0 {
		return out
	}
	for _, rc := range items {
		if rc == nil {
			continue
		}
		rc.Normalize()
		if rc.Name == "" {
			continue
		}
		source := rc.Source
		if source == "" {
			source = models.RuntimeClassSourceLocal
		}
		out = append(out, models.RuntimeClassStatus{
			Name:    rc.Name,
			Handler: rc.Handler,
			Source:  source,
		})
	}
	slices.SortFunc(out, func(a, b models.RuntimeClassStatus) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// GetAvailableCDIDevices returns unique sorted fully-qualified CDI device names.
func GetAvailableCDIDevices() (devices []string) {
	devices = []string{}
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("CDI device status panicked: %v", r))
			devices = []string{}
		}
	}()
	engineName := ""
	if cfg := config.GetInstance(); cfg != nil {
		engineName = cfg.ContainerEngine
	}
	listed := cdidevices.ListAvailable(engineName)
	if listed == nil {
		return devices
	}
	return listed
}

func formatRuntimeClassesLine(items []models.RuntimeClassStatus) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Display())
	}
	return strings.Join(parts, ", ")
}

// Status getters (thread-safe)

// GetSupervisorStatus returns the supervisor status
func (sr *StatusReporter) GetSupervisorStatus() *models.SupervisorStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.supervisorStatus
}

// DaemonOperational reports whether the supervisor is running or degraded-but-operational.
func (sr *StatusReporter) DaemonOperational() bool {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	if sr.supervisorStatus == nil {
		return false
	}
	switch sr.supervisorStatus.DaemonStatus {
	case models.ModuleStatusRunning, models.ModuleStatusWarning:
		return true
	default:
		return false
	}
}

// GetResourceConsumptionManagerStatus returns the resource consumption manager status
func (sr *StatusReporter) GetResourceConsumptionManagerStatus() *models.ResourceConsumptionManagerStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.resourceConsumptionManagerStatus
}

// GetFieldAgentStatus returns the field agent status
func (sr *StatusReporter) GetFieldAgentStatus() *models.FieldAgentStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.fieldAgentStatus
}

// GetStatusReporterStatus returns the status reporter status
func (sr *StatusReporter) GetStatusReporterStatus() *models.StatusReporterStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.statusReporterStatus
}

func getAvailableNetworkInterfaces() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Unable to list network interfaces for status report: %v", err))
		return nil
	}

	names := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.TrimSpace(iface.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// GetProcessManagerStatus returns the process manager status
func (sr *StatusReporter) GetProcessManagerStatus() *models.ProcessManagerStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.processManagerStatus
}

// GetLocalAPIStatus returns the local API status
func (sr *StatusReporter) GetLocalAPIStatus() *models.EdgeletAPIStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.localAPIStatus
}

// GetSSHProxyManagerStatus returns the SSH proxy manager status
func (sr *StatusReporter) GetSSHProxyManagerStatus() *models.SSHProxyManagerStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.sshProxyManagerStatus
}

// GetVolumeMountManagerStatus returns the volume mount manager status
func (sr *StatusReporter) GetVolumeMountManagerStatus() *models.VolumeMountManagerStatus {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.volumeMountManagerStatus
}

// Status setters (thread-safe, return status for chaining)

// UpdateSupervisorStatus updates the supervisor status securely
func (sr *StatusReporter) UpdateSupervisorStatus(fn func(*models.SupervisorStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.supervisorStatus)
}

// UpdateResourceConsumptionManagerStatus updates the resource consumption manager status securely
func (sr *StatusReporter) UpdateResourceConsumptionManagerStatus(fn func(*models.ResourceConsumptionManagerStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.resourceConsumptionManagerStatus)
}

// UpdateFieldAgentStatus updates the field agent status securely
func (sr *StatusReporter) UpdateFieldAgentStatus(fn func(*models.FieldAgentStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.fieldAgentStatus)
}

// UpdateStatusReporterStatus updates the status reporter status securely
func (sr *StatusReporter) UpdateStatusReporterStatus(fn func(*models.StatusReporterStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.statusReporterStatus)
}

// UpdateProcessManagerStatus updates the process manager status securely
func (sr *StatusReporter) UpdateProcessManagerStatus(fn func(*models.ProcessManagerStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.processManagerStatus)
}

// ResetProcessManagerStatus clears all process-manager status data.
func (sr *StatusReporter) ResetProcessManagerStatus() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	sr.processManagerStatus.ClearMicroserviceStatuses()
}

// PruneProcessManagerStatus removes process-manager entries matching predicate.
func (sr *StatusReporter) PruneProcessManagerStatus(predicate func(uuid string, status *models.MicroserviceStatus) bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	sr.processManagerStatus.PruneMicroserviceStatus(predicate)
}

// UpdateLocalAPIStatus updates the local API status securely
func (sr *StatusReporter) UpdateLocalAPIStatus(fn func(*models.EdgeletAPIStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.localAPIStatus)
}

// UpdateSSHProxyManagerStatus updates the SSH proxy manager status securely
func (sr *StatusReporter) UpdateSSHProxyManagerStatus(fn func(*models.SSHProxyManagerStatus)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
	fn(sr.sshProxyManagerStatus)
}

// UpdateVolumeMountManagerStatus updates the volume mount manager status
func (sr *StatusReporter) UpdateVolumeMountManagerStatus(activeMounts int64, lastUpdate int64) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.volumeMountManagerStatus.SetActiveMounts(activeMounts)
	sr.volumeMountManagerStatus.SetLastUpdate(lastUpdate)
	sr.statusReporterStatus.SetLastUpdate(time.Now().UnixMilli())
}

// GetName returns the module name
func (sr *StatusReporter) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index
func (sr *StatusReporter) GetModuleIndex() int {
	return utils.StatusReporter
}

func stackMemoryMiBToBytes(mib float64) int64 {
	if mib <= 0 {
		return 0
	}
	return int64(mib * 1024 * 1024) // #nosec G115 -- RSS MiB fits in int64
}
