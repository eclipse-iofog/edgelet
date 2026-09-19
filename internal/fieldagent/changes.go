package fieldagent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/proxy"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/version"
)

// processChanges processes changes from the controller
func (fa *FieldAgent) processChanges(changes map[string]any) bool {
	logging.LogDebug(moduleName, fmt.Sprintf("Starting processChanges with changes: %+v", changes))

	resetChanges := true
	initialization := fa.state.IsInitialization()
	logging.LogDebug(moduleName, fmt.Sprintf("Processing changes with initialization flag: %v", initialization))

	// Process deleteNode change
	if deleteNode, ok := changes["deleteNode"].(bool); ok && deleteNode && !initialization {
		logging.LogDebug(moduleName, "Processing deleteNode change")
		if err := fa.deleteNode(); err != nil {
			logging.LogError(moduleName, "Unable to delete node", err)
			resetChanges = false
		}
	} else {
		// Process other changes

		// Process reboot change
		if reboot, ok := changes["reboot"].(bool); ok && reboot && !initialization {
			logging.LogDebug(moduleName, "Processing reboot change")
			if err := fa.reboot(); err != nil {
				logging.LogError(moduleName, "Unable to perform reboot", err)
				resetChanges = false
			}
		}

		// Process config change
		if configChange, ok := changes["config"].(bool); ok && configChange && !initialization {
			logging.LogDebug(moduleName, "Processing config change")
			if err := fa.getFogConfig(); err != nil {
				logging.LogError(moduleName, "Unable to get config", err)
				resetChanges = false
			}
		}

		// Process version change
		if version, ok := changes["version"].(bool); ok && version && !initialization {
			logging.LogDebug(moduleName, "Processing version change")
			if err := fa.changeVersion(); err != nil {
				logging.LogError(moduleName, "Unable to change version", err)
				resetChanges = false
			}
		}

		// Process registries change
		if registries, ok := changes["registries"].(bool); ok && (registries || initialization) {
			if initialization && fa.shouldSkipInitReload() {
				logging.LogDebug(moduleName, "skipping init registries reload; reconnect reconcile already completed")
			} else {
				logging.LogDebug(moduleName, "Processing registries change")
				if err := fa.loadRegistries(false); err != nil {
					logging.LogError(moduleName, "Unable to update registries", err)
					resetChanges = false
				}
			}
		}

		// Process prune change (images + unused local models only; persistent
		// VOLUME data is reclaimed only by explicit volume commands).
		if prune, ok := changes["prune"].(bool); ok && prune && !initialization {
			logging.LogDebug(moduleName, "Processing prune change")
			if err := fa.pruneDanglingImages(); err != nil {
				logging.LogError(moduleName, "Unable to prune dangling images", err)
				resetChanges = false
			}
			if err := fa.pruneUnusedLocalModels(); err != nil {
				logging.LogError(moduleName, "Unable to prune unused local models", err)
				resetChanges = false
			}
			_ = fa.pruneVolumesFn
		}

		// Process volumeMounts change
		if volumeMounts, ok := changes["volumeMounts"].(bool); ok && (volumeMounts || initialization) {
			if initialization && fa.shouldSkipInitReload() {
				logging.LogDebug(moduleName, "skipping init volume mounts reload; reconnect reconcile already completed")
			} else {
				logging.LogDebug(moduleName, "Processing volumeMounts change")
				if err := fa.loadVolumeMounts(); err != nil {
					logging.LogError(moduleName, "Unable to load volume mounts", err)
					resetChanges = false
				}
			}
		}

		// Process models change
		if modelsFlag, ok := changes["models"].(bool); ok && (modelsFlag || initialization) {
			if initialization && fa.shouldSkipInitReload() {
				logging.LogDebug(moduleName, "skipping init models reload; reconnect reconcile already completed")
			} else {
				logging.LogDebug(moduleName, "Processing models change")
				if err := fa.loadModels(false); err != nil {
					logging.LogError(moduleName, "Unable to update models", err)
					resetChanges = false
				}
			}
		}

		// Process runtimeClasses change
		if runtimeClassesFlag, ok := changes["runtimeClasses"].(bool); ok && (runtimeClassesFlag || initialization) {
			if initialization && fa.shouldSkipInitReload() {
				logging.LogDebug(moduleName, "skipping init runtime classes reload; reconnect reconcile already completed")
			} else {
				logging.LogDebug(moduleName, "Processing runtimeClasses change")
				if err := fa.loadRuntimeClasses(false); err != nil {
					logging.LogError(moduleName, "Unable to update runtime classes", err)
					resetChanges = false
				}
			}
		}

		// Process microservice-related changes
		microserviceConfig, ok := changes["microserviceConfig"].(bool)
		if !ok {
			microserviceConfig = false
		}
		microserviceList, ok := changes["microserviceList"].(bool)
		if !ok {
			microserviceList = false
		}
		microserviceModels, ok := changes["microserviceModels"].(bool)
		if !ok {
			microserviceModels = false
		}
		execSessions, ok := changes["execSessions"].(bool)
		if !ok {
			execSessions = false
		}

		if microserviceConfig || microserviceList || microserviceModels || initialization {
			if initialization && fa.shouldSkipInitReload() {
				logging.LogDebug(moduleName, "skipping init microservices reload; reconnect reconcile already completed")
			} else {
				logging.LogDebug(moduleName, fmt.Sprintf("Processing microservice related changes - microserviceConfig: %v, microserviceList: %v, microserviceModels: %v",
					microserviceConfig, microserviceList, microserviceModels))

				// One GET microservices for list and/or catalog-only flags.
				microservices, err := fa.loadMicroservices(false)
				if err != nil {
					logging.LogError(moduleName, "Unable to get microservices list", err)
					resetChanges = false
				} else {
					logging.LogDebug(moduleName, fmt.Sprintf("Loaded %d microservices", len(microservices)))

					// Process microservice config changes
					if microserviceConfig {
						logging.LogDebug(moduleName, "Processing microservice config changes")
						if err := fa.processMicroserviceConfig(microservices); err != nil {
							logging.LogError(moduleName, "Unable to update microservices config", err)
							resetChanges = false
						}
					}
				}
			}
		}

		// Process exec sessions changes
		if execSessions {
			logging.LogDebug(moduleName, "Processing exec sessions changes")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			execSessionManager := GetExecSessionManager()
			sessions, err := execSessionManager.FetchExecSessions(ctx)
			cancel()
			if err != nil {
				logging.LogError(moduleName, "Unable to handle exec sessions", err)
				resetChanges = false
			} else {
				execSessionManager.HandleExecSessions(sessions)
			}
		}

		// Process tunnel change
		if tunnel, ok := changes["tunnel"].(bool); ok && tunnel && !initialization {
			logging.LogDebug(moduleName, "Processing tunnel change")
			if err := fa.updateTunnel(); err != nil {
				logging.LogError(moduleName, "Unable to update tunnel", err)
				resetChanges = false
			}
		}

		// Process log sessions changes
		microserviceLogs, ok := changes["microserviceLogs"].(bool)
		if !ok {
			microserviceLogs = false
		}
		fogLogs, ok := changes["fogLogs"].(bool)
		if !ok {
			fogLogs = false
		}
		if microserviceLogs || fogLogs {
			logging.LogDebug(moduleName, fmt.Sprintf("Processing log sessions changes - microserviceLogs: %v, fogLogs: %v", microserviceLogs, fogLogs))
			// Fetch and handle log sessions
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			logSessionManager := GetLogSessionManager()
			sessions, err := logSessionManager.FetchLogSessions(ctx)
			cancel()
			if err != nil {
				logging.LogError(moduleName, "Unable to handle log sessions", err)
				resetChanges = false
			} else {
				logSessionManager.HandleLogSessions(sessions)
			}
		}
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Finished processing changes with resetChanges: %v", resetChanges))
	return resetChanges
}

// deleteNode deletes the current fog node from controller and deprovisions
func (fa *FieldAgent) deleteNode() error {
	logging.LogDebug(moduleName, "start deleting current fog node from controller and make it deprovision")

	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	client := fa.getAPIClient()
	var err error
	if client != nil {
		err = client.Delete(ctx, "delete-node")
	} else {
		err = errors.New("api client is not initialized")
	}
	cancel()

	if err != nil {
		logging.LogError(moduleName, "Can't send delete node command", err)
	}

	if depErr := fa.Deprovision(true); depErr != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Deprovision after node deletion failed: %v", depErr))
	}
	logging.LogDebug(moduleName, "Finish deleting current fog node from controller and make it deprovision")
	return err
}

// reboot performs a remote reboot (placeholder - would need system commands)
func (fa *FieldAgent) reboot() error {
	logging.LogInfo(moduleName, "start Remote reboot of Linux machine from IOFog controller")
	// Execute reboot command (requires root privileges)
	// Note: This is a dangerous operation and should be carefully controlled
	output, _, err := utils.ExecuteCommand("sudo reboot")
	if err != nil {
		logging.LogError(moduleName, "Failed to execute reboot command", err)
		return err
	}
	logging.LogInfo(moduleName, "Reboot command executed: "+output)
	logging.LogInfo(moduleName, "Finished Remote reboot of Linux machine from IOFog controller")
	return nil
}

// changeVersion changes the agent version (placeholder)
func (fa *FieldAgent) changeVersion() error {
	logging.LogInfo(moduleName, "Starting change version action")

	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		return nil
	}

	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	client := fa.getAPIClient()
	if client == nil {
		cancel()
		return errors.New("api client is not initialized")
	}
	result, err := client.Request(ctx, "version", GET, nil, nil)
	cancel()

	if err != nil {
		return fmt.Errorf("unable to get version command: %w", err)
	}

	// Extract version command from result (flat v3.8 or legacy nested).
	actionData, err := version.NormalizeVersionResponse(result)
	if err != nil {
		return fmt.Errorf("failed to normalize version response: %w", err)
	}
	if actionData == nil {
		logging.LogDebug(moduleName, fmt.Sprintf("Version change result: %+v", result))
		return nil
	}

	versionHandler := version.GetInstance()
	if err := versionHandler.ChangeVersion(actionData); err != nil {
		return fmt.Errorf("failed to change version: %w", err)
	}

	logging.LogInfo(moduleName, "Finished change version operation, received from ioFog controller")
	return nil
}

// updateTunnel updates SSH tunnel configuration
func (fa *FieldAgent) updateTunnel() error {
	logging.LogDebug(moduleName, "Updating SSH tunnel configuration")

	// Get proxy config from controller
	proxyConfig, err := fa.getProxyConfig()
	if err != nil {
		return fmt.Errorf("failed to get proxy config: %w", err)
	}

	if proxyConfig == nil {
		logging.LogDebug(moduleName, "No proxy config received, skipping tunnel update")
		return nil
	}

	// Update SSH proxy manager
	proxyManager := proxy.GetInstance()
	if err := proxyManager.Update(proxyConfig); err != nil {
		return fmt.Errorf("failed to update SSH proxy: %w", err)
	}

	logging.LogDebug(moduleName, "SSH tunnel configuration updated successfully")
	return nil
}

// getProxyConfig gets proxy configuration from controller
func (fa *FieldAgent) getProxyConfig() (map[string]any, error) {
	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		return nil, nil
	}

	// Request tunnel config from controller
	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	defer cancel()

	client := fa.getAPIClient()
	if client == nil {
		return nil, errors.New("api client is not initialized")
	}
	response, err := client.Request(ctx, "tunnel", GET, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to request tunnel config: %w", err)
	}

	// Extract tunnel config from response
	if tunnelObj, ok := response["tunnel"].(map[string]any); ok {
		return tunnelObj, nil
	}

	return nil, nil
}

// getFogConfig gets the fog configuration from controller
func (fa *FieldAgent) getFogConfig() error {
	logging.LogInfo(moduleName, "Starting Get ioFog config")

	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		return nil
	}

	if fa.state.IsInitialization() {
		// Post config instead
		return fa.postFogConfig()
	}

	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	client := fa.getAPIClient()
	if client == nil {
		cancel()
		return errors.New("api client is not initialized")
	}
	configs, err := client.Request(ctx, "config", GET, nil, nil)
	cancel()

	if err != nil {
		return fmt.Errorf("unable to get ioFog config: %w", err)
	}

	// Check for nested config objects (instance-config or agent-config)
	if instanceConfig, ok := configs["instance-config"].(map[string]any); ok {
		configs = instanceConfig
	} else if agentConfig, ok := configs["agent-config"].(map[string]any); ok {
		configs = agentConfig
	}

	// Map controller config keys to agent config keys (short codes)
	configMap := make(map[string]any)

	// Mapping from controller JSON keys to internal short codes (Pot agent-service.js).
	keyMapping := map[string]string{
		"diskLimit":              "d",
		"diskDirectory":          "dl",
		"memoryLimit":            "m",
		"cpuLimit":               "p",
		"controllerUrl":          "a",
		"controllerCert":         "ac",
		"containerEngineUrl":     "cu",
		"containerEngine":        "ce",
		"networkInterface":       "n",
		"logLimit":               "l",
		"logDirectory":           "ld",
		"logFileCount":           "lc",
		"logLevel":               "ll",
		"statusFrequency":        "sf",
		"changeFrequency":        "cf",
		"watchdogEnabled":        "wd",
		"edgeGuardFrequency":     "egf",
		"gpsMode":                "gps",
		"gpsDevice":              "gpsd",
		"gpsScanFrequency":       "gpsf",
		"arch":                   "ft",
		"secureMode":             "sec",
		"pruningFrequency":       "pf",
		"availableDiskThreshold": "dt",
		"upgradeScanFrequency":   "uf",
		"devMode":                "dev",
		"timeZone":               "tz",
	}

	for k, v := range configs {
		if shortKey, ok := keyMapping[k]; ok {
			configMap[shortKey] = v
		}
	}

	// Apply only keys that differ from local config (controller sends full snapshot).
	cfg := config.GetInstance()
	changedMap := cfg.FilterChangedConfigKeys(configMap)
	if len(changedMap) > 0 {
		logging.LogDebug(moduleName, fmt.Sprintf("Applying config changes: %+v", changedMap))
		errorMap := cfg.SetConfig(changedMap)

		if len(errorMap) > 0 {
			logging.LogError(moduleName, fmt.Sprintf("Errors applying config: %+v", errorMap), nil)
		} else {
			logging.LogInfo(moduleName, "Configuration applied successfully")
		}
	} else if len(configMap) > 0 {
		logging.LogDebug(moduleName, "Controller config unchanged; skipping apply")
	} else {
		logging.LogDebug(moduleName, "No matching config fields found to apply")
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Config result: %+v", configs))
	logging.LogInfo(moduleName, "Finished Get ioFog config")
	return nil
}

// postFogConfig posts the fog configuration to controller
func (fa *FieldAgent) postFogConfig() error {
	logging.LogDebug(moduleName, "Post ioFog config")

	// Check if provisioned and connected
	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		logging.LogDebug(moduleName, "Skipping postFogConfig: not provisioned or not connected")
		return nil
	}

	cfg := config.GetInstance()

	// Parse GPS coordinates
	latitude := 0.0
	longitude := 0.0
	if cfg.GPSCoordinates != "" {
		coords := strings.Split(cfg.GPSCoordinates, ",")
		if len(coords) == 2 {
			if lat, err := strconv.ParseFloat(strings.TrimSpace(coords[0]), 64); err == nil {
				latitude = lat
			}
			if lon, err := strconv.ParseFloat(strings.TrimSpace(coords[1]), 64); err == nil {
				longitude = lon
			}
		}
	}

	// Get network interface name
	networkInterfaceName := cfg.NetworkInterface
	if networkInterfaceName == "" {
		networkInterfaceName = "UNKNOWN"
	}

	// Build config data
	configData := map[string]any{
		"networkInterface":       networkInterfaceName,
		"containerEngineUrl":     cfg.ContainerEngineURL,
		"diskLimit":              cfg.DiskLimit,
		"diskDirectory":          cfg.DiskDirectory,
		"memoryLimit":            cfg.MemoryLimit,
		"cpuLimit":               cfg.CPULimit,
		"logLimit":               cfg.LogLimit,
		"logDirectory":           cfg.LogDirectory,
		"logFileCount":           cfg.LogFileCount,
		"statusFrequency":        cfg.StatusFrequency,
		"changeFrequency":        cfg.ChangeFrequency,
		"watchdogEnabled":        cfg.WatchdogEnabled,
		"edgeGuardFrequency":     cfg.EdgeGuardFrequency,
		"gpsDevice":              cfg.GPSDevice,
		"gpsScanFrequency":       cfg.GPSScanFrequency,
		"gpsMode":                strings.ToLower(cfg.GPSMode),
		"latitude":               latitude,
		"longitude":              longitude,
		"logLevel":               strings.ToUpper(cfg.LogLevel),
		"availableDiskThreshold": cfg.AvailableDiskThreshold,
		"pruningFrequency":       cfg.PruningFrequency,
		"upgradeScanFrequency":   cfg.UpgradeScanFrequency,
	}

	// Use context from FieldAgent (daemon mode) or create new one (CLI mode)
	var ctx context.Context
	var cancel context.CancelFunc
	if fa.ctx != nil {
		ctx, cancel = context.WithTimeout(fa.ctx, 30*time.Second)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	}
	defer cancel()

	client := fa.getAPIClient()
	if client == nil {
		return errors.New("api client is not initialized")
	}
	// Post config using PATCH method
	_, err := client.Request(ctx, "config", PATCH, nil, configData)
	if err != nil {
		logging.LogError(moduleName, "Failed to post fog config to controller", err)
		return err
	}

	logging.LogInfo(moduleName, "Fog config posted to controller successfully")
	return nil
}

// InstanceGPSConfigUpdated sends dedicated GPS config updates to the controller.
func (fa *FieldAgent) InstanceGPSConfigUpdated() error {
	logging.LogDebug(moduleName, "Start ioFog GPS configuration update")
	if err := fa.postGPSConfig(); err != nil {
		logging.LogError(moduleName, "Error posting updated GPS config", err)
		return err
	}
	logging.LogDebug(moduleName, "Finished ioFog GPS configuration update")
	return nil
}

// postGPSConfig posts dedicated GPS coordinates to controller.
func (fa *FieldAgent) postGPSConfig() error {
	logging.LogDebug(moduleName, "Post ioFog GPS config")

	// Check if provisioned and connected
	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		logging.LogDebug(moduleName, "Skipping postGPSConfig: not provisioned or not connected")
		return nil
	}

	latitude, longitude, ok := parseGPSCoordinates(config.GetInstance().GPSCoordinates)
	if !ok {
		logging.LogWarn(moduleName, "Skipping postGPSConfig due to invalid or empty gpsCoordinates")
		return nil
	}

	body := map[string]any{
		"latitude":  latitude,
		"longitude": longitude,
	}

	// Use context from FieldAgent (daemon mode) or create new one (CLI mode).
	var ctx context.Context
	var cancel context.CancelFunc
	if fa.ctx != nil {
		ctx, cancel = context.WithTimeout(fa.ctx, 30*time.Second)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	}
	defer cancel()

	client := fa.getAPIClient()
	if client == nil {
		return errors.New("api client is not initialized")
	}
	_, err := client.Request(ctx, "config/gps", PATCH, nil, body)
	if err != nil {
		logging.LogError(moduleName, "Failed to post GPS config to controller", err)
		return err
	}

	logging.LogInfo(moduleName, "GPS config posted to controller successfully")
	return nil
}

func parseGPSCoordinates(raw string) (float64, float64, bool) {
	coords := strings.TrimSpace(raw)
	if coords == "" {
		return 0, 0, false
	}

	parts := strings.Split(coords, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}

	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, false
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, false
	}

	return lat, lon, true
}
