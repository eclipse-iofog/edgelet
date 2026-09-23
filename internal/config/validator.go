package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/utils"
)

const (
	cpuLimitMin = 5
	cpuLimitMax = 400
)

func cpuLimitRangeError() error {
	return fmt.Errorf("CPU limit must be between %d and %d (100 = one logical CPU for the Edgelet stack)", cpuLimitMin, cpuLimitMax)
}

// ValidateConfig validates the configuration
func ValidateConfig(cfg *Config) error {
	var errors []string

	// Validate disk limit
	if cfg.DiskLimit < 0.5 || cfg.DiskLimit > utils.MaxDiskConsumptionLimit {
		errors = append(errors, fmt.Sprintf("disk limit must be between 0.5 and %.1f GB", utils.MaxDiskConsumptionLimit))
	}

	// Validate memory limit
	if cfg.MemoryLimit < 128 || cfg.MemoryLimit > 1048576 {
		errors = append(errors, "memory limit must be between 128 and 1048576 MB")
	}

	// Validate CPU limit (cores×100; compare with resource consumption stack CPU)
	if cfg.CPULimit < cpuLimitMin || cfg.CPULimit > cpuLimitMax {
		errors = append(errors, cpuLimitRangeError().Error())
	}

	// Validate log disk limit
	if cfg.LogLimit < 0.5 || cfg.LogLimit > utils.MaxDiskConsumptionLimit {
		errors = append(errors, fmt.Sprintf("log disk limit must be between 0.5 and %.1f GB", utils.MaxDiskConsumptionLimit))
	}

	// Validate log file count
	if cfg.LogFileCount < 1 || cfg.LogFileCount > 100 {
		errors = append(errors, "log file count must be between 1 and 100")
	}

	// Validate log level
	validLogLevels := map[string]bool{
		"DEBUG": true,
		"INFO":  true,
		"WARN":  true,
		"ERROR": true,
		"FATAL": true,
		"OFF":   true,
	}
	if !validLogLevels[strings.ToUpper(cfg.LogLevel)] {
		errors = append(errors, "log level must be one of: DEBUG, INFO, WARN, ERROR, FATAL, OFF")
	}

	// Validate frequencies
	if cfg.StatusFrequency < 1 {
		errors = append(errors, "status frequency must be greater than 0")
	}
	if cfg.ChangeFrequency < 1 {
		errors = append(errors, "change frequency must be greater than 0")
	}

	// Validate edge guard frequency
	if cfg.EdgeGuardFrequency < 0 {
		errors = append(errors, "edge guard frequency must be positive")
	}

	// Validate GPS scan frequency
	if cfg.GPSScanFrequency < 0 {
		errors = append(errors, "GPS scan frequency must be positive")
	}
	if cfg.GPSMode != "" {
		mode := strings.ToLower(strings.TrimSpace(cfg.GPSMode))
		switch mode {
		case "auto", "dynamic", "manual", "off":
		default:
			errors = append(errors, "GPS mode must be one of: auto, dynamic, manual, off")
		}
	}
	if strings.TrimSpace(cfg.GPSCoordinates) != "" {
		if _, err := normalizeGPSCoordinates(cfg.GPSCoordinates); err != nil {
			errors = append(errors, fmt.Sprintf("GPS coordinates are invalid: %v", err))
		}
	}

	// Validate container engine URL
	if cfg.ContainerEngineURL != "" {
		if !strings.HasPrefix(cfg.ContainerEngineURL, "tcp://") && !strings.HasPrefix(cfg.ContainerEngineURL, "unix://") {
			errors = append(errors, "container engine URL must start with 'tcp://' or 'unix://'")
		}
	}

	// Validate container engine (platform capability restricts allowed engines)
	eng := strings.ToLower(strings.TrimSpace(cfg.ContainerEngine))
	allowed := buildmeta.AllowedEngines()
	engineAllowed := false
	for _, candidate := range allowed {
		if eng == candidate {
			engineAllowed = true
			break
		}
	}
	if !engineAllowed {
		errors = append(errors, fmt.Sprintf("containerEngine %q is not supported on this platform (allowed: %s)", cfg.ContainerEngine, strings.Join(allowed, ", ")))
	}
	if eng != constants.EngineDocker && eng != constants.EnginePodman && eng != constants.EngineEdgelet {
		errors = append(errors, "containerEngine must be one of: docker, podman, edgelet")
	}

	// containerEngineUrl rules per engine
	if eng == constants.EngineEdgelet {
		want := constants.EdgeletEngineSocketURL()
		if cfg.ContainerEngineURL != want {
			errors = append(errors, fmt.Sprintf("containerEngineUrl for containerEngine edgelet must be %q (got %q)", want, cfg.ContainerEngineURL))
		}
	}
	if eng == constants.EnginePodman && strings.TrimSpace(cfg.ContainerEngineURL) == "" {
		errors = append(errors, "containerEngineUrl is required when containerEngine is podman (e.g. unix:///run/podman/podman.sock)")
	}

	// Validate shutdown grace period
	if cfg.ShutdownGracePeriodSeconds < 5 || cfg.ShutdownGracePeriodSeconds > 600 {
		errors = append(errors, "shutdownGracePeriodSeconds must be between 5 and 600")
	}

	policy := strings.ToLower(strings.TrimSpace(cfg.ShutdownPolicy))
	if policy == "" {
		policy = DefaultShutdownPolicy(cfg.ContainerEngine)
	}
	switch policy {
	case ShutdownPolicyLeaveRunning, ShutdownPolicyDrainAll:
	default:
		errors = append(errors, fmt.Sprintf("shutdownPolicy must be %q or %q (got %q)", ShutdownPolicyLeaveRunning, ShutdownPolicyDrainAll, cfg.ShutdownPolicy))
	}

	// Validate controller timeouts (edge-friendly)
	if cfg.ControllerRequestTimeoutSeconds < 5 || cfg.ControllerRequestTimeoutSeconds > 300 {
		errors = append(errors, "controllerRequestTimeoutSeconds must be between 5 and 300")
	}
	if cfg.ControllerPingTimeoutSeconds < 5 || cfg.ControllerPingTimeoutSeconds > 300 {
		errors = append(errors, "controllerPingTimeoutSeconds must be between 5 and 300")
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// ValidateProperty validates a single property value
func ValidateProperty(key, value string) error {
	switch key {
	case "diskLimit":
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid disk limit value: %w", err)
		}
		if val < 0.5 || val > utils.MaxDiskConsumptionLimit {
			return fmt.Errorf("disk limit must be between 0.5 and %.1f GB", utils.MaxDiskConsumptionLimit)
		}
	case "memoryLimit":
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid memory limit value: %w", err)
		}
		if val < 128 || val > 1048576 {
			return errors.New("memory limit must be between 128 and 1048576 MB")
		}
	case "cpuLimit":
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid CPU limit value: %w", err)
		}
		if val < cpuLimitMin || val > cpuLimitMax {
			return cpuLimitRangeError()
		}
	case "logLimit":
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid log disk limit value: %w", err)
		}
		if val < 0.5 || val > utils.MaxDiskConsumptionLimit {
			return fmt.Errorf("log disk limit must be between 0.5 and %.1f GB", utils.MaxDiskConsumptionLimit)
		}
	case "logFileCount":
		val, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid log file count value: %w", err)
		}
		if val < 1 || val > 100 {
			return errors.New("log file count must be between 1 and 100")
		}
	case "logLevel":
		validLogLevels := map[string]bool{
			"DEBUG": true,
			"INFO":  true,
			"WARN":  true,
			"ERROR": true,
			"FATAL": true,
			"OFF":   true,
		}
		if !validLogLevels[strings.ToUpper(value)] {
			return errors.New("log level must be one of: DEBUG, INFO, WARN, ERROR, FATAL, OFF")
		}
	case "statusFrequency", "changeFrequency":
		val, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid frequency value: %w", err)
		}
		if val < 1 {
			return errors.New("frequency must be greater than 0")
		}
	case "edgeGuardFrequency", "gpsScanFrequency":
		val, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid frequency value: %w", err)
		}
		if val < 0 {
			return errors.New("frequency must be positive")
		}
	case "gpsMode":
		mode := strings.ToLower(strings.TrimSpace(value))
		switch mode {
		case "auto", "dynamic", "manual", "off":
		default:
			return errors.New("GPS mode must be one of: auto, dynamic, manual, off")
		}
	case "gpsCoordinates":
		if _, err := normalizeGPSCoordinates(value); err != nil {
			return err
		}
	case "containerEngineUrl":
		if value != "" && !strings.HasPrefix(value, "tcp://") && !strings.HasPrefix(value, "unix://") {
			return errors.New("container engine URL must start with 'tcp://' or 'unix://'")
		}
	case "shutdownPolicy":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case ShutdownPolicyLeaveRunning, ShutdownPolicyDrainAll:
		default:
			return fmt.Errorf("shutdownPolicy must be %q or %q", ShutdownPolicyLeaveRunning, ShutdownPolicyDrainAll)
		}
	}

	return nil
}
