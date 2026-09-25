package resourceconsumption

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	moduleName = "Resource Consumption Manager"
)

// Manager handles resource consumption monitoring
type Manager struct {
	config         *config.Config
	statusReporter *statusreporter.StatusReporter
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	mu             sync.RWMutex

	processReader    processMetricsReader
	hostCPUReader    hostCPUReader
	runtimePIDReader runtimePIDReader

	cpuHistoryMu sync.Mutex
	cpuHistory   map[string][]float64

	procCPU         map[int32]cpuTimesSample
	lastHost        hostCPUSample
	haveHost        bool
	diskSize        int64
	diskSizeAt      time.Time
	directorySizeFn func(string) int64

	// Limits (in bytes for memory/disk, percentage for CPU)
	diskLimit   int64
	cpuLimit    float64
	memoryLimit int64
}

var (
	instance *Manager
	once     sync.Once
)

// GetInstance returns the singleton ResourceConsumptionManager instance
func GetInstance() *Manager {
	once.Do(func() {
		instance = &Manager{
			config:        config.GetInstance(),
			processReader: gopsutilProcessReader{},
			cpuHistory:    make(map[string][]float64),
			procCPU:       make(map[int32]cpuTimesSample),
		}
	})
	return instance
}

// Start starts the ResourceConsumptionManager
func (rcm *Manager) Start() error {
	logging.LogInfo(moduleName, "Starting Resource Consumption Manager")

	rcm.statusReporter = statusreporter.GetInstance()
	rcm.InstanceConfigUpdated()
	logging.LogDebug(moduleName, fmt.Sprintf("Resource limits set: Disk=%.2f GiB, Memory=%.2f MiB, CPU=%.2f%%",
		float64(rcm.diskLimit)/1_000_000_000, float64(rcm.memoryLimit)/1_000_000, rcm.cpuLimit))

	rcm.ctx, rcm.cancel = context.WithCancel(context.Background())

	logging.LogDebug(moduleName, "Collecting initial resource usage data")
	rcm.ensureHostIdentity()
	rcm.collectUsageData()
	logging.LogDebug(moduleName, fmt.Sprintf("Initial resource usage: Memory=%.2f MiB, CPU=%.2f%%, Disk=%.2f GiB",
		rcm.statusReporter.GetResourceConsumptionManagerStatus().MemoryUsage,
		rcm.statusReporter.GetResourceConsumptionManagerStatus().CPUUsage,
		rcm.statusReporter.GetResourceConsumptionManagerStatus().DiskUsage))

	rcm.wg.Add(1)
	go rcm.runUsageDataWorker()

	logging.LogInfo(moduleName, "Resource Consumption Manager started")
	return nil
}

func (rcm *Manager) collectUsageData() {
	logging.LogDebug(moduleName, "Get usage data")

	trackRuntime := rcm.shouldTrackEmbeddedRuntime()
	runtimePIDs := []int(nil)
	if trackRuntime {
		runtimePIDs = rcm.embeddedRuntimePIDs()
		if len(runtimePIDs) > 1 {
			logging.LogWarn(moduleName, fmt.Sprintf("Multiple embedded containerd child processes detected: count=%d", len(runtimePIDs)))
		}
	}

	sample := rcm.sampleEdgeletUsage(trackRuntime, runtimePIDs)

	agentCPU := rcm.smoothCPU("agent", sample.agentCPU)
	runtimeCPU := 0.0
	if trackRuntime && sample.runtimeAvailable {
		runtimeCPU = rcm.smoothCPU("runtime", sample.runtimeCPU)
	}
	totalCPU := rcm.smoothCPU("total", agentCPU+runtimeCPU)

	agentMemoryMiB := bytesToMiB(sample.agentRSS)
	runtimeMemoryMiB := bytesToMiB(sample.runtimeRSS)
	totalMemoryMiB := agentMemoryMiB + runtimeMemoryMiB
	totalMemoryBytes := sample.agentRSS + sample.runtimeRSS

	diskUsage := rcm.cachedDirectorySize(rcm.config.DiskDirectory)
	availableMemory := rcm.getSystemAvailableMemory()
	totalSystemMemory := rcm.getSystemTotalMemory()
	systemCpus := rcm.getSystemLogicalCPUCount()
	hostID := rcm.ensureHostIdentity()
	availableDisk := rcm.getAvailableDisk()
	totalDiskSpace := rcm.getTotalDiskSpace()

	runtimeAvailable := !trackRuntime || sample.runtimeAvailable
	runtimeDegraded := trackRuntime && !sample.runtimeAvailable

	logging.LogDebug(moduleName, fmt.Sprintf(
		"Edgelet usage: agentCPU=%.2f runtimeCPU=%.2f totalCPU=%.2f agentMem=%.2f MiB runtimeMem=%.2f MiB hostCPU=%.2f runtimeAvailable=%v",
		agentCPU, runtimeCPU, totalCPU, agentMemoryMiB, runtimeMemoryMiB, sample.hostCPU, runtimeAvailable,
	))

	rcm.statusReporter.UpdateResourceConsumptionManagerStatus(func(status *models.ResourceConsumptionManagerStatus) {
		status.AgentCPUPercent = agentCPU
		status.AgentMemoryMiB = agentMemoryMiB
		status.RuntimeCPUPercent = runtimeCPU
		status.RuntimeMemoryMiB = runtimeMemoryMiB
		status.RuntimeAvailable = runtimeAvailable
		status.RuntimeDegraded = runtimeDegraded
		status.RuntimeTracked = trackRuntime
		status.RuntimePIDCount = sample.runtimePIDCount
		status.EdgeletTotalCPUPercent = totalCPU
		status.EdgeletTotalMemoryMiB = totalMemoryMiB

		status.CPUUsage = totalCPU
		status.MemoryUsage = totalMemoryMiB
		status.DiskUsage = float64(diskUsage) / 1_000_000_000
		status.MemoryViolation = totalMemoryBytes > rcm.memoryLimit
		status.DiskViolation = diskUsage > rcm.diskLimit
		status.CPUViolation = totalCPU > rcm.cpuLimit
		status.AvailableMemory = availableMemory
		status.SystemTotalMemory = totalSystemMemory
		status.SystemCpus = systemCpus
		status.SystemOs = hostID.os
		status.SystemOsVersion = hostID.osVersion
		status.SystemKernelVersion = hostID.kernelVersion
		status.AvailableDisk = availableDisk
		status.TotalDiskSpace = totalDiskSpace
		status.TotalCPU = sample.hostCPU
	})

	logging.LogDebug(moduleName, "Finished Get usage data")
}

// Stop stops the ResourceConsumptionManager
func (rcm *Manager) Stop() error {
	logging.LogDebug(moduleName, "Stopping Resource Consumption Manager")

	if rcm.cancel != nil {
		rcm.cancel()
	}

	rcm.wg.Wait()

	logging.LogDebug(moduleName, "Resource Consumption Manager stopped")
	return nil
}

// GetName returns the module name
func (rcm *Manager) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index
func (rcm *Manager) GetModuleIndex() int {
	return utils.ResourceConsumptionManager
}

// InstanceConfigUpdated updates limits when configuration changes
func (rcm *Manager) InstanceConfigUpdated() {
	rcm.mu.Lock()
	defer rcm.mu.Unlock()

	rcm.diskLimit = int64(rcm.config.DiskLimit * 1_000_000_000)
	rcm.memoryLimit = int64(rcm.config.MemoryLimit * 1_000_000)
	rcm.cpuLimit = rcm.config.CPULimit
}

func (rcm *Manager) shouldTrackEmbeddedRuntime() bool {
	cfg := rcm.config
	if cfg == nil {
		return false
	}
	if !buildmeta.HasEmbeddedEngine() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.ContainerEngine), constants.EngineEdgelet)
}

func (rcm *Manager) runUsageDataWorker() {
	defer rcm.wg.Done()

	cfg := rcm.config
	ticker := time.NewTicker(time.Duration(cfg.GetUsageDataFreqSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rcm.ctx.Done():
			return
		case <-ticker.C:
			rcm.collectUsageData()
		}
	}
}

func (rcm *Manager) getSystemAvailableMemory() int64 {
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		logging.LogError(moduleName, "Error getting system available memory", err)
		return 0
	}
	return int64(vmStat.Available) // #nosec G115 -- system available memory is below int64 max in practice
}

func (rcm *Manager) getSystemTotalMemory() int64 {
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		logging.LogError(moduleName, "Error getting system total memory", err)
		return 0
	}
	return int64(vmStat.Total) // #nosec G115 -- system total memory is below int64 max in practice
}

func (rcm *Manager) getSystemLogicalCPUCount() int {
	return runtime.NumCPU()
}

func (rcm *Manager) getAvailableDisk() int64 {
	cfg := rcm.config
	usage, err := disk.Usage(cfg.DiskDirectory)
	if err != nil {
		logging.LogError(moduleName, "Error getting available disk", err)
		return 0
	}
	return int64(usage.Free) // #nosec G115 -- disk size is below int64 max in practice
}

func (rcm *Manager) getTotalDiskSpace() int64 {
	cfg := rcm.config
	usage, err := disk.Usage(cfg.DiskDirectory)
	if err != nil {
		logging.LogError(moduleName, "Error getting total disk space", err)
		return 0
	}
	return int64(usage.Total) // #nosec G115 -- disk size is below int64 max in practice
}

func (rcm *Manager) cachedDirectorySize(path string) int64 {
	now := time.Now()
	rcm.mu.RLock()
	if !rcm.diskSizeAt.IsZero() && now.Sub(rcm.diskSizeAt) < diskWalkInterval {
		size := rcm.diskSize
		rcm.mu.RUnlock()
		return size
	}
	rcm.mu.RUnlock()
	size := rcm.directorySize(path)
	rcm.mu.Lock()
	rcm.diskSize = size
	rcm.diskSizeAt = now
	rcm.mu.Unlock()
	return size
}

func (rcm *Manager) directorySize(path string) int64 {
	if rcm.directorySizeFn != nil {
		return rcm.directorySizeFn(path)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0
	}

	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		logging.LogError(moduleName, fmt.Sprintf("Error calculating directory size for %s", path), err)
		return 0
	}
	return size
}
