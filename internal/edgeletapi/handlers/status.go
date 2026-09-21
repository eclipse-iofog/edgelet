package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/dnsresolver"
	"github.com/eclipse-iofog/edgelet/internal/fieldagent"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	statusHandlerModuleName = "Status Api Handler"
)

// StatusHandler handles status API requests
type StatusHandler struct {
	// StatusReporter is accessed via singleton
}

// HandleStatus handles GET /v1/system/status
func (h *StatusHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	logging.LogDebug(statusHandlerModuleName, "Handle status Api Handler call")

	if r.Method != http.MethodGet {
		logging.LogError(statusHandlerModuleName, "Request method not allowed", nil)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get status report from StatusReporter
	statusReporter := statusreporter.GetInstance()
	statusReport := statusReporter.GetStatusReport()

	// Parse status report into a map
	statusMap := parseStatusReport(statusReport)
	if buildmeta.HasEmbeddedEngine() && strings.EqualFold(strings.TrimSpace(config.GetInstance().ContainerEngine), "edgelet") {
		augmentWithDNSStatus(statusMap)
	}
	if shouldAugmentCgroupStatus() {
		augmentWithCgroupStatus(statusMap)
	}
	augmentWithRuntimeStatus(statusMap)
	statusMap["runtimeClasses"] = statusreporter.GetAppliedRuntimeClasses()
	statusMap["availableCdiDevices"] = statusreporter.GetAvailableCDIDevices()
	knowledgeStatus, activeKnowledge, knowledgeLastUpdate := fieldagent.GetInstance().FogKnowledgeStatus()
	statusMap["knowledgeStatus"] = knowledgeStatus
	statusMap["activeKnowledge"] = activeKnowledge
	statusMap["knowledgeLastUpdate"] = knowledgeLastUpdate

	// Convert to JSON
	jsonData, err := json.Marshal(statusMap)
	if err != nil {
		logging.LogError(statusHandlerModuleName, "Failed to marshal status", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(jsonData); werr != nil {
		logging.LogWarn(statusHandlerModuleName, fmt.Sprintf("Failed to write response: %v", werr))
	}
	logging.LogDebug(statusHandlerModuleName, "Finished status Api Handler call")
}

// parseStatusReport parses a status report string into a map
func parseStatusReport(statusReport string) map[string]any {
	result := make(map[string]any)
	lines := strings.Split(statusReport, "\n")

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

func augmentWithDNSStatus(status map[string]any) {
	if status == nil {
		return
	}
	s := dnsresolver.GetInstance().Snapshot()
	status["dnsStarted"] = strconv.FormatBool(s.Started)
	status["dnsCompatAliasesEnabled"] = strconv.FormatBool(s.CompatAliasesEnabled)
	status["dnsRateLimitEnabled"] = strconv.FormatBool(s.RateLimitEnabled)
	status["dnsRateLimitRPS"] = strconv.Itoa(s.RateLimitRPS)
	status["dnsRateLimitBurst"] = strconv.Itoa(s.RateLimitBurst)
	status["dnsMaxRequestBytes"] = strconv.Itoa(s.MaxRequestBytes)
	status["dnsMaxQNameBytes"] = strconv.Itoa(s.MaxQNameBytes)
	status["dnsScopeManagedListening"] = strconv.FormatBool(s.ScopeManaged.Listening)
	status["dnsScopeManagedAddress"] = s.ScopeManaged.Address
	status["dnsQueriesTotal"] = strconv.FormatUint(s.QueriesTotal, 10)
	status["dnsSuccessTotal"] = strconv.FormatUint(s.SuccessTotal, 10)
	status["dnsNXDomainTotal"] = strconv.FormatUint(s.NXDomainTotal, 10)
	status["dnsServFailTotal"] = strconv.FormatUint(s.ServFailTotal, 10)
	status["dnsPolicyDeniedTotal"] = strconv.FormatUint(s.PolicyDeniedTotal, 10)
	status["dnsInactiveTotal"] = strconv.FormatUint(s.InactiveTotal, 10)
	status["dnsForwardedTotal"] = strconv.FormatUint(s.ForwardedTotal, 10)
	status["dnsForwardErrTotal"] = strconv.FormatUint(s.ForwardErrTotal, 10)
	status["dnsForwardingDegraded"] = strconv.FormatBool(s.ForwardingDegraded)
	status["dnsForwardTotalUpstream"] = strconv.FormatUint(s.ForwardTotalUpstream, 10)
	status["dnsForwardHealthyUpstream"] = strconv.FormatUint(s.ForwardHealthyUpstream, 10)
	status["dnsForwardLastSuccessUnix"] = strconv.FormatInt(s.ForwardLastSuccessUnix, 10)
	status["dnsForwardLastFailureUnix"] = strconv.FormatInt(s.ForwardLastFailureUnix, 10)
	status["dnsForwardBackoffSkipTotal"] = strconv.FormatUint(s.ForwardBackoffSkipTotal, 10)
	status["dnsRateLimitedTotal"] = strconv.FormatUint(s.RateLimitedTotal, 10)
	status["dnsRejectedTotal"] = strconv.FormatUint(s.RejectedTotal, 10)
	status["dnsHealth"] = deriveDNSHealth(s)
}

func deriveDNSHealth(s dnsresolver.StatsSnapshot) string {
	if !s.Started {
		return "stopped"
	}
	if !s.ScopeManaged.Listening {
		return "degraded"
	}
	if s.ForwardingDegraded {
		return "degraded"
	}
	return "ready"
}
