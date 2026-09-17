package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"gopkg.in/yaml.v3"
)

const configLoaderModuleName = "Config Loader"

var errYAMLParse = errors.New("yaml parse error")

// LoadConfig loads configuration from YAML file
func LoadConfig(configPath string) error {
	cfg := GetInstance()
	backupPath := utils.BackupConfigYAMLPath

	// Try to load from primary config file first.
	yamlConfig, err := loadYAMLFile(configPath)
	if err != nil {
		primaryErr := err

		// Missing primary config: fallback to backup, then default bootstrap only if both are missing.
		if os.IsNotExist(primaryErr) {
			yamlConfig, err = loadYAMLFile(backupPath)
			if err != nil {
				if os.IsNotExist(err) {
					logging.LogWarn(configLoaderModuleName, "Config file not found, creating default config")
					yamlConfig = createDefaultYamlConfigForLoader()
					if mkErr := os.MkdirAll(utils.ConfigDir, 0700); mkErr == nil {
						if saveErr := SaveConfigWithYaml(configPath, yamlConfig); saveErr != nil {
							logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to save default config: %v", saveErr))
						}
					}
				} else {
					return fmt.Errorf("failed to load backup config %s after missing primary %s: %w", backupPath, configPath, err)
				}
			}
		} else {
			// Primary exists but is invalid/unreadable; try backup but never generate defaults.
			yamlConfig, err = loadYAMLFile(backupPath)
			if err != nil {
				return fmt.Errorf("failed to load primary config %s (%w) and backup %s (%w)", configPath, primaryErr, backupPath, err)
			}
			logging.LogWarn(
				configLoaderModuleName,
				fmt.Sprintf("Loaded backup config from %s because primary config %s was invalid: %v", backupPath, configPath, primaryErr),
			)
		}
	}

	// Validate and set current profile
	currentProfileStr := yamlConfig.CurrentProfile
	if currentProfileStr == "" {
		currentProfileStr = utils.ConfigSwitcherStateDefault.FullValue()
		yamlConfig.CurrentProfile = currentProfileStr
	}

	currentProfile, err := utils.ParseConfigSwitcherState(currentProfileStr)
	if err != nil {
		// Use default profile
		currentProfile = utils.ConfigSwitcherStateDefault
		yamlConfig.CurrentProfile = currentProfile.FullValue()
	}

	// Ensure current profile exists
	if yamlConfig.GetProfile(currentProfile.FullValue()) == nil {
		currentProfile = utils.ConfigSwitcherStateDefault
		yamlConfig.CurrentProfile = currentProfile.FullValue()
	}

	// Set the config
	cfg.SetYamlConfig(yamlConfig)
	cfg.SetCurrentProfile(currentProfile)
	cfg.SetConfigPath(configPath)

	// Load all configuration values from current profile
	loadConfigValues(cfg)

	return nil
}

// loadYAMLFile loads a YAML file
func loadYAMLFile(path string) (*models.YamlConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- path is from config loader, not user input
	if err != nil {
		return nil, err
	}

	var yamlConfig models.YamlConfig
	if err := yaml.Unmarshal(data, &yamlConfig); err != nil {
		return nil, fmt.Errorf("%w: %w", errYAMLParse, err)
	}

	if yamlConfig.Profiles == nil {
		yamlConfig.Profiles = make(map[string]*models.ProfileConfig)
	}

	return &yamlConfig, nil
}

// loadConfigValues loads all configuration values from the current profile
func loadConfigValues(cfg *Config) {
	profile := cfg.GetYamlConfig().GetProfile(cfg.GetCurrentProfile().FullValue())
	if profile == nil {
		return
	}

	// Helper to get property with default
	getProp := func(key, defaultValue string) string {
		val := profile.GetProperty(key)
		if val == "" {
			return defaultValue
		}
		return val
	}

	// Load all config values
	// Note: This is a simplified version. Full implementation would parse all values
	// and handle type conversions properly
	cfg.IOFogUUID = getProp("iofogUuid", "")
	// privateKey durability moved to SQLite; keep runtime value empty until FieldAgent hydrates from DB.
	cfg.PrivateKey = ""
	cfg.ControllerURL = getProp("controllerUrl", "http://localhost:54421/api/v3/")
	cfg.ControllerCert = getProp("controllerCert", "/etc/edgelet/cert.crt")
	cfg.NetworkInterface = getProp("networkInterface", "dynamic")
	cfg.ContainerEngine = getProp("containerEngine", "edgelet")
	cfg.ContainerEngineURL = getProp("containerEngineUrl", "unix:///run/edgelet/containerd.sock")
	cfg.DiskDirectory = getProp("diskDirectory", "/var/lib/edgelet/")
	logDirectory := getProp("logDirectory", "")
	if logDirectory == "" {
		logDirectory = getProp("logDiskDirectory", "/var/log/edgelet/")
	}
	cfg.LogDirectory = logDirectory
	cfg.LogLevel = strings.ToUpper(getProp("logLevel", "INFO"))
	cfg.GPSDevice = getProp("gpsDevice", "/dev/ttyUSB0")
	cfg.GPSMode = strings.ToLower(strings.TrimSpace(getProp("gpsMode", "auto")))
	cfg.GPSCoordinates = getProp("gpsCoordinates", "")
	cfg.Arch = getProp("arch", "auto")
	cfg.Namespace = getProp("namespace", "default")
	cfg.TimeZone = getProp("timeZone", "Europe/Istanbul")
	// HWSignature removed - now stored in separate file: /etc/edgelet/agent-{uuid}.jwt
	// This prevents triggering SIGHUP/reload when signature is updated
	// cfg.HWSignature = getProp("hwSignature", "")

	// Parse numeric values
	parseFloat := func(key, defaultValue string) float64 {
		val := getProp(key, defaultValue)
		var result float64
		if _, err := fmt.Sscanf(val, "%f", &result); err != nil {
			logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to parse config value for %s: %v", key, err))
		}
		return result
	}

	parseInt := func(key, defaultValue string) int {
		val := getProp(key, defaultValue)
		var result int
		if _, err := fmt.Sscanf(val, "%d", &result); err != nil {
			logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to parse config value for %s: %v", key, err))
		}
		return result
	}

	parseInt64 := func(key, defaultValue string) int64 {
		val := getProp(key, defaultValue)
		var result int64
		if _, err := fmt.Sscanf(val, "%d", &result); err != nil {
			logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to parse config value for %s: %v", key, err))
		}
		return result
	}

	cfg.DiskLimit = parseFloat("diskLimit", "10")
	cfg.MemoryLimit = parseFloat("memoryLimit", "4096")
	cfg.CPULimit = parseFloat("cpuLimit", "80")
	logLimitRaw := getProp("logLimit", "")
	if logLimitRaw == "" {
		logLimitRaw = getProp("logDiskLimit", "10")
	}
	var logLimitVal float64
	if _, err := fmt.Sscanf(logLimitRaw, "%f", &logLimitVal); err != nil {
		logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to parse config value for logLimit: %v", err))
	}
	cfg.LogLimit = logLimitVal
	cfg.LogFileCount = parseInt("logFileCount", "10")
	cfg.StatusFrequency = parseInt("statusFrequency", "10")
	cfg.ChangeFrequency = parseInt("changeFrequency", "20")
	cfg.WatchdogEnabled = getProp("watchdogEnabled", "off") != "off"
	cfg.EdgeGuardFrequency = parseInt64("edgeGuardFrequency", "0")
	if cfg.IOFogUUID == "" && cfg.EdgeGuardFrequency > 0 {
		cfg.EdgeGuardFrequency = 0
	}
	cfg.GPSScanFrequency = parseInt64("gpsScanFrequency", "60")
	cfg.SecureMode = ParseSecureMode(getProp("secureMode", "off"))
	cfg.PruningFrequency = parseInt64("pruningFrequency", "0")
	cfg.AvailableDiskThreshold = parseInt64("availableDiskThreshold", "20")
	cfg.UpgradeScanFrequency = parseInt("upgradeScanFrequency", "24")
	cfg.LogReconcileCycleEveryNTicks = parseInt("logReconcileCycleEveryNTicks", "60")
	if cfg.LogReconcileCycleEveryNTicks < 1 {
		cfg.LogReconcileCycleEveryNTicks = 60
	}
	cfg.DevMode = getProp("devMode", "off") != "off"
	cfg.ShutdownGracePeriodSeconds = parseInt("shutdownGracePeriodSeconds", "90")
	if cfg.ShutdownGracePeriodSeconds < 1 {
		cfg.ShutdownGracePeriodSeconds = 90
	}
	cfg.ShutdownPolicy = strings.ToLower(strings.TrimSpace(getProp("shutdownPolicy", "")))
	if cfg.ShutdownPolicy == "" {
		cfg.ShutdownPolicy = DefaultShutdownPolicy(cfg.ContainerEngine)
	}
	cfg.ControllerRequestTimeoutSeconds = parseInt("controllerRequestTimeoutSeconds", "30")
	if cfg.ControllerRequestTimeoutSeconds < 5 {
		cfg.ControllerRequestTimeoutSeconds = 30
	}
	cfg.ControllerPingTimeoutSeconds = parseInt("controllerPingTimeoutSeconds", "60")
	if cfg.ControllerPingTimeoutSeconds < 5 {
		cfg.ControllerPingTimeoutSeconds = 60
	}

	// Update automatic config params based on architecture
	updateAutomaticConfigParams(cfg)
}

// createDefaultYamlConfigForLoader creates a default YAML config for the loader
func createDefaultYamlConfigForLoader() *models.YamlConfig {
	yamlConfig := models.NewYamlConfig()
	yamlConfig.CurrentProfile = utils.ConfigSwitcherStateDefault.FullValue()

	// Create default profile with default values
	defaultProfile := models.NewProfileConfig()
	defaultProfile.SetProperty("controllerUrl", "http://localhost:54421/api/v3/")
	defaultProfile.SetProperty("controllerCert", "/etc/edgelet/cert.crt")
	defaultProfile.SetProperty("networkInterface", "dynamic")
	defaultProfile.SetProperty("containerEngine", "edgelet")
	defaultProfile.SetProperty("containerEngineUrl", "unix:///run/edgelet/containerd.sock")
	defaultProfile.SetProperty("diskDirectory", "/var/lib/edgelet/")
	defaultProfile.SetProperty("diskLimit", "10")
	defaultProfile.SetProperty("memoryLimit", "4096")
	defaultProfile.SetProperty("cpuLimit", "80")
	defaultProfile.SetProperty("logDirectory", "/var/log/edgelet/")
	defaultProfile.SetProperty("logLimit", "10")
	defaultProfile.SetProperty("logFileCount", "10")
	defaultProfile.SetProperty("logLevel", "INFO")
	defaultProfile.SetProperty("statusFrequency", "10")
	defaultProfile.SetProperty("changeFrequency", "20")
	defaultProfile.SetProperty("watchdogEnabled", "off")
	defaultProfile.SetProperty("edgeGuardFrequency", "0")
	defaultProfile.SetProperty("gpsDevice", "/dev/ttyUSB0")
	defaultProfile.SetProperty("gpsScanFrequency", "60")
	defaultProfile.SetProperty("gpsMode", "auto")
	defaultProfile.SetProperty("gpsCoordinates", "0,0")
	defaultProfile.SetProperty("arch", "auto")
	defaultProfile.SetProperty("secureMode", "off")
	defaultProfile.SetProperty("pruningFrequency", "0")
	defaultProfile.SetProperty("availableDiskThreshold", "20")
	defaultProfile.SetProperty("upgradeScanFrequency", "24")
	defaultProfile.SetProperty("logReconcileCycleEveryNTicks", "60")
	defaultProfile.SetProperty("devMode", "off")
	defaultProfile.SetProperty("timeZone", "")
	defaultProfile.SetProperty("namespace", "default")

	yamlConfig.Profiles[utils.ConfigSwitcherStateDefault.FullValue()] = defaultProfile

	return yamlConfig
}

// updateAutomaticConfigParams updates automatic configuration parameters based on architecture
func updateAutomaticConfigParams(cfg *Config) {
	// For now, use same values for all architectures
	// This can be customized based on arch type
	cfg.StatusReportFreqSeconds = 5
	cfg.PingControllerFreqSeconds = 30
	cfg.MonitorContainersStatusFreqSeconds = 5
	cfg.MonitorRegistriesStatusFreqSeconds = 60
	cfg.HealthcheckIntervalSeconds = 30
	cfg.GetUsageDataFreqSeconds = 5
	cfg.DockerAPIVersion = "1.47"
	cfg.SetSystemTimeFreqSeconds = 5
	cfg.MonitorSSHTunnelStatusFreqSeconds = 30
}

// SaveConfig saves the configuration to YAML file
func SaveConfig(configPath string) error {
	cfg := GetInstance()
	yamlConfig := cfg.GetYamlConfig()
	if yamlConfig == nil {
		return ErrConfigNotLoaded
	}

	return SaveConfigWithYaml(configPath, yamlConfig)
}

// SaveConfigWithYaml saves the provided YAML config to file (avoids lock acquisition)
func SaveConfigWithYaml(configPath string, yamlConfig *models.YamlConfig) error {
	if yamlConfig == nil {
		return ErrConfigNotLoaded
	}

	data, err := yaml.Marshal(yamlConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	cleanPath := filepath.Clean(configPath)
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to set temp config file permissions: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp config file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp config file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, cleanPath); err != nil {
		return fmt.Errorf("failed to atomically replace config file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		logging.LogWarn(configLoaderModuleName, fmt.Sprintf("Failed to sync config directory %s: %v", dir, err))
	}

	return nil
}

func syncDirectory(path string) error {
	dirHandle, err := os.Open(path) // #nosec G304 -- config directory from loader constant
	if err != nil {
		return err
	}
	defer func() {
		_ = dirHandle.Close()
	}()
	return dirHandle.Sync()
}
