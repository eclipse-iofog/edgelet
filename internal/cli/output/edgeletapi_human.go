package output

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

var statusOutputOrder = []string{
	"connectionToController",
	"agentCpuPercent",
	"agentMemoryMiB",
	"runtimeCpuPercent",
	"runtimeMemoryMiB",
	"runtimeAvailable",
	"runtimeDegraded",
	"edgeletTotalCpuPercent",
	"edgeletTotalMemoryMiB",
	"cpuUsage",
	"diskUsage",
	"edgeletDaemon",
	"memoryUsage",
	"runningMicroservices",
	"systemAvailableDisk",
	"systemAvailableMemory",
	"systemTime",
	"systemTotalCpu",
	"availableNetworkInterfaces",
	"availableRuntimes",
	"runtimeClasses",
	"availableCdiDevices",
}

var infoOutputOrder = []string{
	"iofogUuid",
	"namespace",
	"networkInterface",
	"ipAddress",
	"controllerUrl",
	"controllerCert",
	"secureMode",
	"containerEngine",
	"containerEngineUrl",
	"arch",
	"availableDiskThreshold",
	"changeFrequency",
	"statusFrequency",
	"cpuLimit",
	"memoryLimit",
	"diskLimit",
	"diskDirectory",
	"logLimit",
	"logDirectory",
	"logFilesCount",
	"logLevel",
	"upgradeScanFrequency",
	"pruningFrequency",
	"edgeGuardFrequency",
	"gpsCoordinates",
	"gpsDevice",
	"gpsScanFrequency",
	"gpsMode",
	"watchdogEnabled",
	"developerMode",
	"timeZone",
}

var infoAliasToCanonical = map[string]string{
	"arch":                 "fogType",
	"changeFrequency":      "changeUpdateFrequency",
	"statusFrequency":      "statusUpdateFrequency",
	"cpuLimit":             "cpuUsageLimit",
	"memoryLimit":          "memoryRamLimit",
	"diskLimit":            "diskUsageLimit",
	"logLevel":             "logFilesLevel",
	"upgradeScanFrequency": "readyToUpgradeScanFrequency",
}

// FormatEdgeletAPIHuman renders human-readable output for a EdgeletAPI v1 route payload.
func FormatEdgeletAPIHuman(routePath string, result map[string]any) string {
	routePath = stripQuery(routePath)
	switch routePath {
	case "/v1/system/status":
		return formatStatusMap(result, statusOutputOrder)
	case "/v1/system/info":
		return formatInfoWithAliasOrder(result)
	case "/v1/ms":
		return formatMSList(result)
	case "/v1/images":
		return formatImageList(result)
	case "/v1/models":
		return formatModelList(result)
	case "/v1/deploy/registries":
		return formatRegistryList(result)
	case "/v1/deploy/runtimeclasses":
		return formatRuntimeClassList(result)
	case "/v1/system/controlplane":
		return formatControlPlaneStatus(result)
	case "/v1/system/controlplane/manifest":
		return formatControlPlaneManifest(result)
	default:
		if human := formatMutationRoute(routePath, result); human != "" {
			return human
		}
		if strings.HasPrefix(routePath, "/v1/ms/") {
			return formatMSInspect(result)
		}
		if strings.HasPrefix(routePath, "/v1/models/") {
			return formatModelInspect(result)
		}
		return ""
	}
}

var msInspectOrder = []string{
	"uuid",
	"name",
	"application",
	"source",
	"type",
	"state",
	"statusText",
	"errorMessage",
	"lastError",
	"containerId",
	"podId",
	"image",
	"desiredState",
	"runtimeState",
	"healthStatus",
	"percentage",
	"restartCount",
}

func formatMSInspect(result map[string]any) string {
	if len(result) == 0 {
		return ""
	}
	if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
		return ""
	}
	if _, hasRaw := result["raw"]; hasRaw {
		// Full inspect includes nested engine state; empty human falls back to JSON.
		return ""
	}
	var b strings.Builder
	seen := make(map[string]bool, len(result))
	for _, key := range msInspectOrder {
		value, ok := result[key]
		if !ok {
			continue
		}
		if formatted, ok := formatInspectScalar(value); ok {
			_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatted)
			seen[key] = true
		}
	}
	if catalog := formatCatalogInspect(result["models"]); catalog != "" {
		_, _ = fmt.Fprint(&b, catalog)
		seen["models"] = true
	}
	remaining := make([]string, 0, len(result))
	for key := range result {
		if seen[key] || key == "models" || key == "manifestYAML" {
			continue
		}
		remaining = append(remaining, key)
	}
	slices.Sort(remaining)
	for _, key := range remaining {
		if formatted, ok := formatInspectScalar(result[key]); ok {
			_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatted)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCatalogInspect(raw any) string {
	catalog, ok := raw.(map[string]any)
	if !ok || len(catalog) == 0 {
		return ""
	}
	var b strings.Builder
	if bind := MapValueAsRawString(catalog, "bindPath"); strings.TrimSpace(bind) != "" {
		_, _ = fmt.Fprintf(&b, "models.bindPath: %s\n", bind)
	}
	if perms := MapValueAsRawString(catalog, "permissions"); strings.TrimSpace(perms) != "" {
		_, _ = fmt.Fprintf(&b, "models.permissions: %s\n", perms)
	}
	if names := catalogItemNames(catalog["items"]); names != "" {
		_, _ = fmt.Fprintf(&b, "models.items: %s\n", names)
	}
	return b.String()
}

func catalogItemNames(raw any) string {
	switch items := raw.(type) {
	case []any:
		names := make([]string, 0, len(items))
		for _, item := range items {
			switch typed := item.(type) {
			case map[string]any:
				if name := strings.TrimSpace(MapValueAsRawString(typed, "name")); name != "" {
					names = append(names, name)
				}
			case string:
				if name := strings.TrimSpace(typed); name != "" {
					names = append(names, name)
				}
			}
		}
		return strings.Join(names, ", ")
	case []string:
		return strings.Join(items, ", ")
	default:
		return ""
	}
}

func formatInspectScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case map[string]any:
		return "", false
	case []any:
		if len(typed) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if _, isMap := item.(map[string]any); isMap {
				return "", false
			}
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, ", "), true
	case []string:
		if len(typed) == 0 {
			return "", false
		}
		return strings.Join(typed, ", "), true
	case *string:
		if typed == nil {
			return "", false
		}
		return *typed, true
	default:
		s := strings.TrimSpace(fmt.Sprintf("%v", typed))
		if s == "" || s == "<nil>" {
			return "", false
		}
		return s, true
	}
}

func formatStatusMap(result map[string]any, preferred []string) string {
	if len(result) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(result))
	var b strings.Builder
	for _, key := range preferred {
		value, ok := result[key]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatStatusValue(key, value))
		seen[key] = true
	}
	remaining := make([]string, 0, len(result))
	for key := range result {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	slices.Sort(remaining)
	for _, key := range remaining {
		_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatStatusValue(key, result[key]))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatStatusValue(key string, value any) string {
	switch key {
	case "runtimeClasses":
		return formatRuntimeClassesStatusValue(value)
	case "availableCdiDevices":
		return formatJoinedStatusList(value)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func formatRuntimeClassesStatusValue(value any) string {
	parts := make([]string, 0)
	switch typed := value.(type) {
	case []models.RuntimeClassStatus:
		for _, item := range typed {
			parts = append(parts, item.Display())
		}
	case []any:
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(MapValueAsRawString(item, "name"))
			handler := strings.TrimSpace(MapValueAsRawString(item, "handler"))
			source := strings.TrimSpace(MapValueAsRawString(item, "source"))
			if name == "" {
				continue
			}
			parts = append(parts, name+" ("+handler+", "+source+")")
		}
	case []map[string]any:
		for _, item := range typed {
			name := strings.TrimSpace(MapValueAsRawString(item, "name"))
			handler := strings.TrimSpace(MapValueAsRawString(item, "handler"))
			source := strings.TrimSpace(MapValueAsRawString(item, "source"))
			if name == "" {
				continue
			}
			parts = append(parts, name+" ("+handler+", "+source+")")
		}
	default:
		return fmt.Sprintf("%v", value)
	}
	return strings.Join(parts, ", ")
}

func formatJoinedStatusList(value any) string {
	switch typed := value.(type) {
	case []string:
		return strings.Join(typed, ", ")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			s := strings.TrimSpace(fmt.Sprintf("%v", item))
			if s == "" {
				continue
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", value)
	}
}

func formatFlatMapWithOrder(result map[string]any, preferred []string) string {
	if len(result) == 0 {
		return ""
	}
	for _, value := range result {
		switch value.(type) {
		case map[string]any, []any:
			return ""
		}
	}
	seen := make(map[string]bool, len(result))
	var b strings.Builder
	for _, key := range preferred {
		value, ok := result[key]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(&b, "%s: %v\n", key, value)
		seen[key] = true
	}
	remaining := make([]string, 0, len(result))
	for key := range result {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	slices.Sort(remaining)
	for _, key := range remaining {
		_, _ = fmt.Fprintf(&b, "%s: %v\n", key, result[key])
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatInfoWithAliasOrder(result map[string]any) string {
	if len(result) == 0 {
		return ""
	}
	for _, value := range result {
		switch value.(type) {
		case map[string]any, []any:
			return ""
		}
	}

	seenCanonical := make(map[string]bool, len(result))
	var b strings.Builder
	for _, alias := range infoOutputOrder {
		canonical := alias
		if mapped, ok := infoAliasToCanonical[alias]; ok {
			canonical = mapped
		}
		value, ok := result[canonical]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(&b, "%s: %v\n", alias, value)
		seenCanonical[canonical] = true
	}

	remainingAliases := make([]string, 0, len(result))
	for canonical := range result {
		if seenCanonical[canonical] {
			continue
		}
		remainingAliases = append(remainingAliases, canonical)
	}
	slices.Sort(remainingAliases)
	for _, key := range remainingAliases {
		_, _ = fmt.Fprintf(&b, "%s: %v\n", key, result[key])
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatMSList(result map[string]any) string {
	rawItems, ok := result["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return "No microservices found."
	}
	rows := [][]string{
		{"UUID", "APPLICATIONNAME", "MICROSERVICENAME", "STATE", "CONTAINERID", "IMAGE", "TYPE"},
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			MapValueAsString(item, "uuid"),
			MapValueAsString(item, "application"),
			MapValueAsString(item, "name"),
			MapValueAsString(item, "state"),
			MapValueAsString(item, "containerId"),
			MapValueAsString(item, "image"),
			MapValueAsString(item, "type"),
		})
	}
	return formatAlignedTable(rows)
}

func formatRegistryList(result map[string]any) string {
	rawItems, ok := result["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return "No registries found."
	}
	rows := [][]string{
		{"ID", "URL", "TYPE", "INSECURE", "PUBLIC", "USERNAME", "EMAIL"},
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			MapValueAsString(item, "id"),
			MapValueAsString(item, "url"),
			ValueOrDefault(MapValueAsString(item, "type"), "oci"),
			formatBoolFlag(item["insecure"]),
			MapValueAsString(item, "isPublic"),
			MapValueAsString(item, "userName"),
			MapValueAsString(item, "userEmail"),
		})
	}
	return formatAlignedTable(rows)
}

func formatModelList(result map[string]any) string {
	rawItems, ok := result["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return "No models found."
	}
	rows := [][]string{
		{"NAME", "SOURCE", "REPO", "REVISION", "REGISTRY", "STATE", "FORMAT"},
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			MapValueAsString(item, "name"),
			ValueOrDefault(MapValueAsString(item, "source"), "-"),
			MapValueAsString(item, "repo"),
			ValueOrDefault(MapValueAsString(item, "revision"), "-"),
			MapValueAsString(item, "registryId"),
			ValueOrDefault(MapValueAsString(item, "state"), "-"),
			ValueOrDefault(MapValueAsString(item, "format"), "-"),
		})
	}
	return formatAlignedTable(rows)
}

var modelInspectOrder = []string{
	"name",
	"source",
	"uuid",
	"bindRefCount",
	"repo",
	"revision",
	"registryId",
	"format",
	"state",
	"files",
	"generation",
	"observedGeneration",
	"resolvedRevision",
	"digest",
	"revisionFloating",
	"totalBytes",
	"contentPath",
	"lastError",
}

func formatModelInspect(result map[string]any) string {
	if len(result) == 0 {
		return ""
	}
	if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
		return ""
	}
	var b strings.Builder
	seen := make(map[string]bool, len(result))
	for _, key := range modelInspectOrder {
		value, ok := result[key]
		if !ok {
			continue
		}
		if formatted, ok := formatInspectScalar(value); ok {
			_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatted)
			seen[key] = true
		}
	}
	remaining := make([]string, 0, len(result))
	for key := range result {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	slices.Sort(remaining)
	for _, key := range remaining {
		if formatted, ok := formatInspectScalar(result[key]); ok {
			_, _ = fmt.Fprintf(&b, "%s: %s\n", key, formatted)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatBoolFlag(raw any) string {
	switch v := raw.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", raw)))
		if s == "true" || s == "1" {
			return "true"
		}
		return "false"
	}
}

func formatRuntimeClassList(result map[string]any) string {
	rawItems, ok := result["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return "No runtime classes found."
	}
	rows := [][]string{
		{"NAME", "HANDLER", "RUNTIME"},
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			MapValueAsString(item, "name"),
			MapValueAsString(item, "handler"),
			MapValueAsString(item, "runtimeName"),
		})
	}
	return formatAlignedTable(rows)
}

var controlPlaneStatusOrder = []string{
	"controllerUuid",
	"namespace",
	"name",
	"image",
	"containerId",
	"state",
	"desiredState",
	"runtimeState",
	"generation",
	"observedGeneration",
	"restartCount",
	"lastError",
	"lastTransitionAt",
	"source",
	"type",
}

func formatControlPlaneStatus(result map[string]any) string {
	if _, hasUUID := result["controllerUuid"]; !hasUUID {
		if MapValueAsString(result, "status") == "ok" {
			return "control plane deployment removed successfully"
		}
		return formatFlatMapWithOrder(result, nil)
	}
	return formatFlatMapWithOrder(result, controlPlaneStatusOrder)
}

func formatControlPlaneManifest(result map[string]any) string {
	yaml := strings.TrimSpace(MapValueAsString(result, "manifestYaml"))
	if yaml == "" {
		return "control plane manifest unavailable"
	}
	masked := MapValueAsString(result, "masked")
	header := "manifestYaml:"
	if masked == "true" {
		header = "manifestYaml (secrets masked):"
	}
	return header + "\n" + yaml
}

func formatImageList(result map[string]any) string {
	rawItems, ok := result["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return "No images found."
	}
	rows := [][]string{
		{"REPOSITORY", "TAG", "IMAGE ID", "CREATED", "DISK USAGE", "CONTENT SIZE", "IN USE"},
	}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			ValueOrDefault(MapValueAsString(item, "repository"), "<none>"),
			ValueOrDefault(MapValueAsString(item, "tag"), "<none>"),
			ValueOrDefault(MapValueAsString(item, "shortId"), "<none>"),
			humanizeCreated(MapValueAsString(item, "createdAt")),
			ValueOrDefault(MapValueAsString(item, "diskUsageHuman"), "0 B"),
			formatOptionalHumanSize(MapValueAsString(item, "contentSizeHuman")),
			formatOptionalInUse(item["inUse"]),
		})
	}
	return formatAlignedTable(rows)
}

func formatOptionalHumanSize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "<unknown>" {
		return "-"
	}
	return value
}

func formatOptionalInUse(raw any) string {
	switch v := raw.(type) {
	case nil:
		return "-"
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" || s == "<nil>" {
			return "-"
		}
		return s
	}
}

func stripQuery(path string) string {
	if idx := strings.Index(path, "?"); idx >= 0 {
		return path[:idx]
	}
	return path
}
