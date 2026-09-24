package config

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils"
)

// Config represents the runtime configuration
type Config struct {
	mu sync.RWMutex

	// YAML config
	yamlConfig     *models.YamlConfig
	currentProfile utils.ConfigSwitcherState
	configPath     string // Path to the config file

	// Directly configurable params
	IOFogUUID                       string
	PrivateKey                      string
	ControllerURL                   string
	ContainerEngine                 string
	ControllerCert                  string
	NetworkInterface                string
	ContainerEngineURL              string
	DiskLimit                       float64
	MemoryLimit                     float64
	DiskDirectory                   string
	CPULimit                        float64
	LogLimit                        float64
	LogDirectory                    string
	LogFileCount                    int
	LogLevel                        string
	StatusFrequency                 int
	ChangeFrequency                 int
	WatchdogEnabled                 bool
	EdgeGuardFrequency              int64
	GPSDevice                       string
	GPSScanFrequency                int64
	GPSCoordinates                  string
	GPSMode                         string
	Arch                            string
	SecureMode                      bool
	IPAddressExternal               string
	RouterUUID                      string
	IsRouterInterior                bool
	PruningFrequency                int64
	AvailableDiskThreshold          int64
	UpgradeScanFrequency            int
	DevMode                         bool
	TimeZone                        string
	Namespace                       string
	ShutdownGracePeriodSeconds      int
	ShutdownPolicy                  string
	ControllerRequestTimeoutSeconds int
	ControllerPingTimeoutSeconds    int
	// HWSignature removed - now stored in separate file: /etc/edgelet/agent-{uuid}.jwt
	// This prevents triggering SIGHUP/reload when signature is updated

	// Automatic configurable params (calculated)
	StatusReportFreqSeconds            int
	PingControllerFreqSeconds          int
	MonitorContainersStatusFreqSeconds int
	// LogReconcileCycleEveryNTicks emits reconcile.cycle at Info when idle (no scheduling) every N monitor ticks.
	LogReconcileCycleEveryNTicks       int
	MonitorRegistriesStatusFreqSeconds int
	HealthcheckIntervalSeconds         int // Interval for exec-based healthcheck (iofog engine only)
	GetUsageDataFreqSeconds            int64
	DockerAPIVersion                   string
	SetSystemTimeFreqSeconds           int
	MonitorSSHTunnelStatusFreqSeconds  int

	// Debugging flag
	Debugging bool

	// Reload callback
	reloadCallback func() error
	// GPS config update callback
	gpsConfigCallback func() error
	// Reload snapshot for warm containerEngineUrl revert (set before SetConfig from API).
	reloadPriorContainerEngineURL string
}

var (
	instance                     *Config
	once                         sync.Once
	suppressReloadForDeprovision atomic.Bool
	suppressReloadForInProcess   atomic.Bool
	lastReloadSuccessful         atomic.Bool
)

// SuppressReloadForDeprovision sets a flag so the config watcher skips SIGHUP
// when the config file is written during deprovision. Prevents CLI timeout.
func SuppressReloadForDeprovision() { suppressReloadForDeprovision.Store(true) }

// RestoreReloadAfterDeprovision clears the suppression flag.
func RestoreReloadAfterDeprovision() { suppressReloadForDeprovision.Store(false) }

// IsReloadSuppressedForDeprovision returns whether SIGHUP should be skipped.
func IsReloadSuppressedForDeprovision() bool { return suppressReloadForDeprovision.Load() }

// SuppressReloadForInProcessMutation sets a flag so the config watcher skips SIGHUP
// while the daemon writes config and applies TriggerReloadCallback in-process (API PATCH).
func SuppressReloadForInProcessMutation() { suppressReloadForInProcess.Store(true) }

// RestoreReloadAfterInProcessMutation clears in-process reload suppression.
func RestoreReloadAfterInProcessMutation() { suppressReloadForInProcess.Store(false) }

// IsReloadSuppressedForInProcessMutation returns whether watcher SIGHUP should be skipped.
func IsReloadSuppressedForInProcessMutation() bool { return suppressReloadForInProcess.Load() }

// RunInProcessConfigMutation suppresses file-watcher SIGHUP for fn (config save + reload).
func RunInProcessConfigMutation(fn func() error) error {
	SuppressReloadForInProcessMutation()
	defer RestoreReloadAfterInProcessMutation()
	return fn()
}

// SetLastReloadSuccessful updates the last reload result.
func SetLastReloadSuccessful(ok bool) { lastReloadSuccessful.Store(ok) }

// IsLastReloadSuccessful indicates if the most recent config reload succeeded.
func IsLastReloadSuccessful() bool { return lastReloadSuccessful.Load() }

// GetInstance returns the singleton config instance
func GetInstance() *Config {
	once.Do(func() {
		instance = &Config{
			Debugging: false,
		}
		lastReloadSuccessful.Store(true)
	})
	return instance
}

// SnapshotForReload records containerEngineUrl before an in-process config mutation (API PATCH).
func (c *Config) SnapshotForReload() {
	c.mu.RLock()
	prior := c.ContainerEngineURL
	c.mu.RUnlock()
	c.mu.Lock()
	c.reloadPriorContainerEngineURL = prior
	c.mu.Unlock()
}

// ConsumeReloadPriorContainerEngineURL returns and clears a reload snapshot if present.
func (c *Config) ConsumeReloadPriorContainerEngineURL() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reloadPriorContainerEngineURL == "" {
		return "", false
	}
	prior := c.reloadPriorContainerEngineURL
	c.reloadPriorContainerEngineURL = ""
	return prior, true
}

// SetReloadCallback sets the callback for configuration reload
func (c *Config) SetReloadCallback(cb func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadCallback = cb
}

// TriggerReloadCallback triggers the configuration reload callback
func (c *Config) TriggerReloadCallback() error {
	c.mu.RLock()
	cb := c.reloadCallback
	c.mu.RUnlock()

	if cb != nil {
		return cb()
	}
	return nil
}

// SetGPSConfigCallback sets the callback for GPS configuration updates.
func (c *Config) SetGPSConfigCallback(cb func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gpsConfigCallback = cb
}

// TriggerGPSConfigCallback triggers the GPS configuration update callback.
func (c *Config) TriggerGPSConfigCallback() error {
	c.mu.RLock()
	cb := c.gpsConfigCallback
	c.mu.RUnlock()

	if cb != nil {
		return cb()
	}
	return nil
}

// GetYamlConfig returns the YAML config (thread-safe)
func (c *Config) GetYamlConfig() *models.YamlConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.yamlConfig
}

// SetYamlConfig sets the YAML config (thread-safe)
func (c *Config) SetYamlConfig(yc *models.YamlConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.yamlConfig = yc
}

// GetCurrentProfile returns the current profile (thread-safe)
func (c *Config) GetCurrentProfile() utils.ConfigSwitcherState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentProfile
}

// SetCurrentProfile sets the current profile (thread-safe)
func (c *Config) SetCurrentProfile(profile utils.ConfigSwitcherState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentProfile = profile
}

// SetConfigPath sets the config file path (thread-safe)
func (c *Config) SetConfigPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configPath = path
}

func (c *Config) SetRouterUUID(uuid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.RouterUUID = strings.TrimSpace(uuid)
}

func (c *Config) SetRouterInterior(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.IsRouterInterior = v
}

// GetConfigPath returns the config file path (thread-safe)
func (c *Config) GetConfigPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.configPath
}

// GetProperty gets a property from the current profile
func (c *Config) GetProperty(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.yamlConfig == nil {
		return ""
	}

	profile := c.yamlConfig.GetProfile(c.currentProfile.FullValue())
	if profile == nil {
		return ""
	}

	return profile.GetProperty(key)
}

// SetProperty sets a property in the current profile
func (c *Config) SetProperty(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.yamlConfig == nil {
		return ErrConfigNotLoaded
	}

	profile := c.yamlConfig.GetProfile(c.currentProfile.FullValue())
	if profile == nil {
		return ErrProfileNotFound
	}

	profile.SetProperty(key, value)
	return nil
}

// GetConfigReport returns a formatted config report string (for info endpoint)
// Uses IPAddressExternal from config
func (c *Config) GetConfigReport() string {
	return c.GetConfigReportWithIP(c.IPAddressExternal)
}

// GetConfigReportWithIP returns a formatted config report string with provided IP address
func (c *Config) GetConfigReportWithIP(ipAddress string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result strings.Builder

	// Helper function to build report line
	buildLine := func(label, value string) {
		// Right pad label to 40 characters
		paddedLabel := label
		if len(paddedLabel) < 40 {
			paddedLabel += strings.Repeat(" ", 40-len(paddedLabel))
		}
		_, _ = result.WriteString(paddedLabel)
		_, _ = result.WriteString(" : ")
		_, _ = result.WriteString(value)
		_, _ = result.WriteString("\n")
	}

	// IP address is passed as parameter
	if ipAddress == "" {
		ipAddress = "unable to retrieve ip address"
	}

	// iofog UUID
	if c.IOFogUUID != "" {
		buildLine("ioFog UUID", c.IOFogUUID)
	} else {
		buildLine("ioFog UUID", "not provisioned")
	}

	// IP address
	buildLine("IP Address", ipAddress)

	// Network interface
	buildLine("Network Interface", c.NetworkInterface)

	// Secure mode
	secureModeStr := "off"
	if c.SecureMode {
		secureModeStr = "on"
	}
	buildLine("Secure Mode", secureModeStr)

	// Controller URL
	buildLine("Controller URL", c.ControllerURL)

	// Controller cert
	buildLine("Controller Cert", c.ControllerCert)

	// Container engine socket URL
	buildLine("Container Engine URL", c.ContainerEngineURL)

	// Container engine
	buildLine("Container Engine", c.ContainerEngine)

	// Disk limit
	buildLine("Disk Usage Limit", fmt.Sprintf("%.2f GiB", c.DiskLimit))

	// Disk directory
	buildLine("Disk Directory", c.DiskDirectory)

	// Memory limit
	buildLine("Memory RAM Limit", fmt.Sprintf("%.2f MiB", c.MemoryLimit))

	// CPU limit
	buildLine("CPU Usage Limit", fmt.Sprintf("%.2f%%", c.CPULimit))

	// Log limit
	buildLine("Log Limit", fmt.Sprintf("%.2f GiB", c.LogLimit))

	// Log directory
	buildLine("Log Directory", c.LogDirectory)

	// Log file count
	buildLine("Log Files Count", fmt.Sprintf("%d", c.LogFileCount))

	// Log level
	buildLine("Log Files Level", c.LogLevel)

	// Status frequency
	buildLine("Status Update Frequency", fmt.Sprintf("%d", c.StatusFrequency))

	// Change frequency
	buildLine("Change Update Frequency", fmt.Sprintf("%d", c.ChangeFrequency))

	// Watchdog enabled
	watchdogStr := "off"
	if c.WatchdogEnabled {
		watchdogStr = "on"
	}
	buildLine("Watchdog Enabled", watchdogStr)

	// Edge guard frequency
	buildLine("Edge Guard Frequency", fmt.Sprintf("%d", c.EdgeGuardFrequency))

	// GPS device
	buildLine("GPS Device", c.GPSDevice)

	// GPS scan frequency
	buildLine("GPS Scan Frequency", fmt.Sprintf("%d", c.GPSScanFrequency))

	// GPS mode
	buildLine("GPS Mode", strings.ToLower(c.GPSMode))

	// GPS coordinates
	buildLine("GPS Coordinates", c.GPSCoordinates)

	// Architecture (resolved for display; never "auto")
	buildLine("Fog Type", DisplayArch(c.Arch))

	// Pruning frequency
	buildLine("Pruning Frequency", fmt.Sprintf("%d", c.PruningFrequency))

	// Available disk threshold
	buildLine("Available Disk Threshold", fmt.Sprintf("%d", c.AvailableDiskThreshold))

	// Ready to upgrade scan frequency
	buildLine("Ready To Upgrade Scan Frequency", fmt.Sprintf("%d", c.UpgradeScanFrequency))

	// Dev mode
	devModeStr := "off"
	if c.DevMode {
		devModeStr = "on"
	}
	buildLine("Developer's Mode", devModeStr)

	// Time zone
	buildLine("Time Zone", c.TimeZone)

	// Namespace
	namespace := c.Namespace
	if namespace == "" {
		namespace = "default"
	}
	buildLine("Namespace", namespace)

	return result.String()
}
