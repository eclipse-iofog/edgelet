package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

// FilterChangedConfigKeys returns only entries whose normalized value differs from
// the current in-memory config. Used when applying full controller config snapshots
// so unchanged keys are not re-written or validated as mutations.
func (c *Config) FilterChangedConfigKeys(configMap map[string]any) map[string]any {
	if len(configMap) == 0 {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	effectiveEngine := c.effectiveEngineForBatchLocked(configMap)
	changed := make(map[string]any, len(configMap))
	for option, value := range configMap {
		if _, ok := ConfigParamMap[option]; !ok {
			continue
		}
		if c.configKeyUnchangedLocked(option, value, effectiveEngine) {
			continue
		}
		changed[option] = value
	}
	return changed
}

func (c *Config) effectiveEngineForBatchLocked(configMap map[string]any) string {
	if raw, ok := configMap["ce"]; ok {
		return normalizeIncomingValue(raw)
	}
	return c.ContainerEngine
}

func (c *Config) configKeyUnchangedLocked(option string, value any, effectiveEngine string) bool {
	incoming := normalizeIncomingValue(value)
	fieldName := ConfigParamMap[option]

	switch fieldName {
	case "diskLimit":
		v, err := strconv.ParseFloat(incoming, 64)
		return err == nil && v == c.DiskLimit
	case "diskDirectory":
		return incoming == c.DiskDirectory
	case "memoryLimit":
		v, err := strconv.ParseFloat(incoming, 64)
		return err == nil && v == c.MemoryLimit
	case "cpuLimit":
		v, err := strconv.ParseFloat(incoming, 64)
		return err == nil && v == c.CPULimit
	case "controllerURL":
		return incoming == c.ControllerURL
	case "controllerCert":
		return incoming == c.ControllerCert
	case "containerEngineURL":
		if strings.EqualFold(strings.TrimSpace(effectiveEngine), constants.EngineEdgelet) {
			return strings.TrimSpace(incoming) == constants.EdgeletEngineSocketURL()
		}
		return strings.TrimSpace(incoming) == strings.TrimSpace(c.ContainerEngineURL)
	case "containerEngine":
		return strings.EqualFold(incoming, c.ContainerEngine)
	case "networkInterface":
		return incoming == c.NetworkInterface
	case "logLimit":
		v, err := strconv.ParseFloat(incoming, 64)
		return err == nil && v == c.LogLimit
	case "logDirectory":
		return incoming == c.LogDirectory
	case "logFileCount":
		v, err := strconv.Atoi(incoming)
		return err == nil && v == c.LogFileCount
	case "logLevel":
		return strings.EqualFold(incoming, c.LogLevel)
	case "statusFrequency":
		v, err := strconv.Atoi(incoming)
		return err == nil && v == c.StatusFrequency
	case "changeFrequency":
		v, err := strconv.Atoi(incoming)
		return err == nil && v == c.ChangeFrequency
	case "watchdogEnabled":
		return watchdogEnabledEqual(incoming, c.WatchdogEnabled)
	case "edgeGuardFrequency":
		v, err := strconv.ParseInt(incoming, 10, 64)
		return err == nil && v == c.EdgeGuardFrequency
	case "gpsMode":
		return strings.EqualFold(incoming, c.GPSMode)
	case "gpsCoordinates":
		current, err := normalizeGPSCoordinates(c.GPSCoordinates)
		if err != nil {
			return false
		}
		incomingNorm, err := normalizeGPSCoordinates(incoming)
		return err == nil && incomingNorm == current
	case "gpsDevice":
		return incoming == c.GPSDevice
	case "gpsScanFrequency":
		v, err := strconv.ParseInt(incoming, 10, 64)
		return err == nil && v == c.GPSScanFrequency
	case "arch":
		return strings.EqualFold(incoming, c.Arch)
	case "secureMode":
		return ParseSecureMode(incoming) == c.SecureMode
	case "pruningFrequency":
		v, err := strconv.ParseInt(incoming, 10, 64)
		return err == nil && v == c.PruningFrequency
	case "availableDiskThreshold":
		v, err := strconv.ParseInt(incoming, 10, 64)
		return err == nil && v == c.AvailableDiskThreshold
	case "upgradeScanFrequency":
		v, err := strconv.Atoi(incoming)
		return err == nil && v == c.UpgradeScanFrequency
	case "devMode":
		return devModeEqual(incoming, c.DevMode)
	case "timeZone":
		return incoming == c.TimeZone
	default:
		return false
	}
}

func normalizeIncomingValue(value any) string {
	return strings.TrimPrefix(strings.TrimSpace(fmt.Sprintf("%v", value)), "+")
}

func watchdogEnabledEqual(incoming string, current bool) bool {
	normalized := strings.ToLower(strings.TrimSpace(incoming))
	switch normalized {
	case "off", "false", "0", "no":
		return !current
	default:
		return current
	}
}

func devModeEqual(incoming string, current bool) bool {
	return (strings.ToLower(strings.TrimSpace(incoming)) != "off") == current
}
