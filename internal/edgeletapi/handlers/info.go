package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/gps"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	infoHandlerModuleName = "Info Api Handler"
)

// InfoHandler handles info API requests
type InfoHandler struct {
	// Will be injected with Configuration when needed
}

// HandleInfo handles GET /v1/system/info
func (h *InfoHandler) HandleInfo(w http.ResponseWriter, r *http.Request) {
	logging.LogDebug(infoHandlerModuleName, "Processing info http request")

	if r.Method != http.MethodGet {
		logging.LogError(infoHandlerModuleName, "Request method not allowed", nil)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get actual config from Configuration
	cfg := config.GetInstance()

	// Get IP address from NetworkInterfaceManager
	ipAddress := network.GetInstance().GetCurrentIPAddress()
	if ipAddress == "" {
		ipAddress = "unable to retrieve ip address"
	}

	// Get config report (with IP address)
	configReport := cfg.GetConfigReportWithIP(ipAddress)

	// Parse config report into map
	infoMap := parseInfoReport(configReport)
	infoMap["networkInterface"] = formatInfoNetworkInterface(cfg.NetworkInterface, network.GetInstance().GetNetworkInterface())
	infoMap["gpsStatus"] = string(gps.GetInstance().GetStatus().GetHealthStatus())

	// Convert to JSON
	jsonData, err := json.Marshal(infoMap)
	if err != nil {
		logging.LogError(infoHandlerModuleName, "Failed to marshal info", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(jsonData); werr != nil {
		logging.LogWarn(infoHandlerModuleName, fmt.Sprintf("Failed to write response: %v", werr))
	}
	logging.LogDebug(infoHandlerModuleName, "Finished processing info http request")
}

// parseInfoReport parses an info report string into a map
func parseInfoReport(infoReport string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(infoReport, "\n")

	for _, line := range lines {
		parts := strings.SplitN(line, " : ", 2)
		if len(parts) == 2 {
			key := normalizeReportKey(parts[0])
			if key == "" {
				continue
			}
			value := strings.TrimSpace(parts[1])
			result[key] = value
		}
	}

	return result
}

func formatInfoNetworkInterface(configuredValue string, detected *network.NetworkInterfaceInfo) string {
	configured := strings.TrimSpace(configuredValue)
	if !strings.EqualFold(configured, "dynamic") {
		return configuredValue
	}

	if detected != nil && detected.Interface != nil {
		detectedName := strings.TrimSpace(detected.Interface.Name)
		if detectedName != "" {
			return fmt.Sprintf("dynamic (%s)", detectedName)
		}
	}

	return "dynamic"
}
