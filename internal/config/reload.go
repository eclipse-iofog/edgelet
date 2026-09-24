package config

import (
	"errors"
	"fmt"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const reloadModuleName = "ConfigReload"

// ReloadHooks supplies supervisor-specific steps for a full config reload.
type ReloadHooks struct {
	ConfigPath    string
	BeginReload   func()
	NotifyModules func() error
}

// FullReload reads config from disk, validates it, updates the logger, and notifies modules.
func FullReload(hooks ReloadHooks) error {
	logging.LogInfo(reloadModuleName, "Reloading configuration...")
	SetLastReloadSuccessful(false)

	if hooks.BeginReload != nil {
		hooks.BeginReload()
	}

	configPath := hooks.ConfigPath
	if configPath == "" {
		return errors.New("config path is required")
	}

	if err := LoadConfig(configPath); err != nil {
		logging.LogError(reloadModuleName, "Failed to reload configuration", err)
		logging.LogWarn(reloadModuleName, "Rejected configuration reload; keeping last-known-good runtime config")
		return fmt.Errorf("failed to reload configuration: %w", err)
	}

	cfg := GetInstance()
	if err := ValidateConfig(cfg); err != nil {
		logging.LogError(reloadModuleName, "Configuration validation failed after reload", err)
		logging.LogWarn(reloadModuleName, "Rejected configuration reload; keeping last-known-good runtime config")
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	SetLastReloadSuccessful(true)
	ScheduleIPAddressExternalRefresh()

	logLimitMB := logging.DaemonLogBudgetMB(cfg.LogLimit, logging.SeriesControlPlane, logging.RuntimeSplitFromEnv())
	if err := logging.InstanceConfigUpdated(cfg.LogDirectory, logLimitMB, cfg.LogFileCount, cfg.LogLevel); err != nil {
		logging.LogError(reloadModuleName, "Failed to update logger configuration", err)
	}

	if hooks.NotifyModules != nil {
		if err := hooks.NotifyModules(); err != nil {
			logging.LogError(reloadModuleName, "Failed to notify modules of config reload", err)
			SetLastReloadSuccessful(false)
			return err
		}
	}

	logging.LogInfo(reloadModuleName, "Configuration reloaded successfully")
	return nil
}
