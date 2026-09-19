package output

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

func formatMutationRoute(routePath string, result map[string]any) string {
	switch routePath {
	case "/v1/system/config":
		return FormatConfigPatchResult(result)
	case "/v1/system/controller/cert":
		return "controller certificate updated successfully"
	case "/v1/system/config/switch":
		return FormatSwitchResult(result)
	case "/v1/system/reload":
		return "configuration reloaded successfully"
	case "/v1/runtime/drain":
		return "runtime microservice drain completed successfully"
	case "/v1/images:pull":
		return formatImagePullResult(result)
	case "/v1/images:load":
		return formatImageLoadResult(result)
	case "/v1/images:prune", "/v1/system/prune":
		return formatImagePruneResult(result)
	case "/v1/images:remove":
		return formatImageRemoveResult(result)
	case "/v1/models:prune":
		return formatModelPruneResult(result)
	case "/v1/volumes:prune":
		return formatVolumePruneResult(result)
	case "/v1/models:pull":
		return formatModelPullResult(result)
	case "/v1/deploy/microservices:validate", "/v1/deploy/registries:validate", "/v1/deploy/models:validate", "/v1/deploy/runtimeclasses:validate", "/v1/deploy/controlplane:validate":
		return formatDeployValidateResult(result)
	case "/v1/deploy/microservices:apply", "/v1/deploy/registries:apply", "/v1/deploy/models:apply", "/v1/deploy/runtimeclasses:apply", "/v1/deploy/controlplane:apply":
		return formatDeployApplyResult(result)
	case "/v1/system/controlplane/restart":
		return formatControlPlaneRestartResult(result)
	default:
		if strings.HasPrefix(routePath, "/v1/ms/") {
			if strings.HasSuffix(routePath, "/start") || strings.HasSuffix(routePath, "/stop") ||
				strings.HasSuffix(routePath, "/restart") || strings.HasSuffix(routePath, "/kill") {
				return formatMSLifecycleResult(routePath, result)
			}
			if _, ok := result["microserviceUuid"]; ok {
				if status, hasStatus := result["status"]; hasStatus && fmt.Sprintf("%v", status) == "ok" {
					return formatMSLifecycleResult(routePath, result)
				}
			}
		}
		if strings.HasPrefix(routePath, "/v1/models/") {
			if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
				return formatModelRemoveResult(result)
			}
		}
		if strings.HasPrefix(routePath, "/v1/volumes/shared/") {
			if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
				return formatVolumeRemoveResult(result, true)
			}
		}
		if strings.HasPrefix(routePath, "/v1/volumes/") {
			if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
				return formatVolumeRemoveResult(result, false)
			}
		}
		if strings.HasPrefix(routePath, "/v1/deploy/registries/") {
			if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
				return formatRegistryRemoveResult(result)
			}
		}
		if strings.HasPrefix(routePath, "/v1/deploy/runtimeclasses/") {
			if status, ok := result["status"]; ok && fmt.Sprintf("%v", status) == "ok" {
				return formatRuntimeClassRemoveResult(result)
			}
			return formatRuntimeClassInspect(result)
		}
		return ""
	}
}

// FormatConfigPatchResult renders PATCH /v1/system/config output.
func FormatConfigPatchResult(result map[string]any) string {
	if len(result) == 0 {
		return ""
	}
	status, ok := result["status"].(string)
	if !ok {
		status = ""
	}
	errorMap, ok := result["errorMap"].(map[string]any)
	if !ok {
		errorMap = map[string]any{}
	}
	var b strings.Builder
	if len(errorMap) == 0 {
		if status == "" {
			status = "ok"
		}
		_, _ = fmt.Fprintf(&b, "config update: %s (all requested changes accepted)", status)
		if pending, ok := result["pendingRestart"].(bool); ok && pending {
			if msg, ok := result["message"].(string); ok && msg != "" {
				_, _ = b.WriteString("\n")
				_, _ = b.WriteString(msg)
			} else {
				_, _ = b.WriteString("\nRestart required: systemctl restart edgelet")
			}
		}
		return b.String()
	}
	_, _ = fmt.Fprintf(&b, "config update: %s\n", status)
	_, _ = fmt.Fprintln(&b, "rejected keys:")
	keys := make([]string, 0, len(errorMap))
	for k := range errorMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(&b, "  - %s: %v\n", k, errorMap[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatConfigMutationOutput renders accepted/rejected config key changes.
func FormatConfigMutationOutput(setMap, before, patchResult map[string]any) string {
	if len(setMap) == 0 {
		return "config update: no changes requested"
	}
	errorMap, ok := patchResult["errorMap"].(map[string]any)
	if !ok {
		errorMap = map[string]any{}
	}
	status, ok := patchResult["status"].(string)
	if !ok {
		status = ""
	}
	if status == "" {
		status = "ok"
	}
	var accepted []string
	var rejected []string
	keys := make([]string, 0, len(setMap))
	for k := range setMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if _, failed := errorMap[key]; failed {
			rejected = append(rejected, fmt.Sprintf("%s (%v)", key, errorMap[key]))
			continue
		}
		oldVal := "<unknown>"
		if before != nil {
			if v, ok := before[key]; ok {
				oldVal = fmt.Sprintf("%v", v)
			}
		}
		accepted = append(accepted, fmt.Sprintf("%s: %s -> %v", key, oldVal, setMap[key]))
	}
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "config update: %s\n", status)
	if len(accepted) > 0 {
		_, _ = fmt.Fprintln(&b, "accepted:")
		for _, line := range accepted {
			_, _ = fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	if len(rejected) > 0 {
		_, _ = fmt.Fprintln(&b, "rejected:")
		for _, line := range rejected {
			_, _ = fmt.Fprintf(&b, "  - %s\n", line)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatSwitchResult renders profile switch output.
func FormatSwitchResult(result map[string]any) string {
	oldProfile := fmt.Sprintf("%v", result["oldProfile"])
	profile := fmt.Sprintf("%v", result["profile"])
	reloaded := fmt.Sprintf("%v", result["reloaded"])
	return fmt.Sprintf("switched profile %s -> %s (reloaded=%s)", oldProfile, profile, reloaded)
}

// FormatProvisionSuccess renders provision completion output.
func FormatProvisionSuccess(agentUUID string) string {
	if strings.TrimSpace(agentUUID) == "" || agentUUID == "<unknown>" {
		return "agent provisioned successfully"
	}
	return fmt.Sprintf("agent provisioned successfully (uuid: %s)", agentUUID)
}

// FormatRegistryInspect renders registry inspect human output.
func FormatRegistryInspect(result map[string]any, passwordPlain bool) string {
	if len(result) == 0 {
		return "Error[NOT_FOUND]: registry not found"
	}
	password := MapValueAsString(result, "password")
	lines := []string{
		fmt.Sprintf("ID: %s", MapValueAsString(result, "id")),
		fmt.Sprintf("URL: %s", MapValueAsString(result, "url")),
		fmt.Sprintf("TYPE: %s", ValueOrDefault(MapValueAsString(result, "type"), "oci")),
		fmt.Sprintf("INSECURE: %s", formatBoolFlag(result["insecure"])),
		fmt.Sprintf("PUBLIC: %s", MapValueAsString(result, "isPublic")),
		fmt.Sprintf("USERNAME: %s", MapValueAsString(result, "userName")),
		fmt.Sprintf("EMAIL: %s", MapValueAsString(result, "userEmail")),
	}
	if MapValueAsString(result, "isPublic") == "false" {
		if passwordPlain {
			lines = append(lines, fmt.Sprintf("PASSWORD: %s", password))
		} else {
			lines = append(lines, fmt.Sprintf("PASSWORD_B64: %s", base64.StdEncoding.EncodeToString([]byte(password))))
		}
	}
	return strings.Join(lines, "\n")
}

func formatImagePullResult(result map[string]any) string {
	return fmt.Sprintf(
		"image pulled successfully: %s (engine=%s, platform=%s)",
		MapValueAsString(result, "resolvedImage"),
		ValueOrDefault(MapValueAsString(result, "engine"), "<unknown>"),
		ValueOrDefault(MapValueAsString(result, "platform"), "<none>"),
	)
}

func formatImageRemoveResult(result map[string]any) string {
	return fmt.Sprintf(
		"image removed successfully: %s (engine=%s)",
		ValueOrDefault(MapValueAsString(result, "removed"), ValueOrDefault(MapValueAsString(result, "selector"), "<unknown>")),
		ValueOrDefault(MapValueAsString(result, "engine"), "<unknown>"),
	)
}

func formatImageLoadResult(result map[string]any) string {
	return fmt.Sprintf(
		"image archive loaded successfully: %s image imported (engine=%s)",
		MapValueAsString(result, "count"),
		ValueOrDefault(MapValueAsString(result, "engine"), "<unknown>"),
	)
}

func formatModelPullResult(result map[string]any) string {
	if name := MapValueAsString(result, "name"); name != "<unknown>" {
		return fmt.Sprintf("model pulled successfully: %s", name)
	}
	return "model pulled successfully"
}

func formatModelPruneResult(result map[string]any) string {
	removed := result["removed"]
	count := 0
	switch v := removed.(type) {
	case []any:
		count = len(v)
	case []string:
		count = len(v)
	}
	if count == 0 {
		if n := MapValueAsString(result, "removedCount"); n != "<unknown>" {
			return fmt.Sprintf("pruned dangling models: removed=%s", n)
		}
		return "pruned dangling models: removed=0"
	}
	return fmt.Sprintf("pruned dangling models: removed=%d", count)
}

func formatModelRemoveResult(result map[string]any) string {
	if name := MapValueAsString(result, "name"); name != "<unknown>" {
		return fmt.Sprintf("model removed successfully (name=%s)", name)
	}
	return "model removed successfully"
}

func formatVolumeRemoveResult(result map[string]any, shared bool) string {
	if shared {
		if name := MapValueAsString(result, "name"); name != "<unknown>" {
			return fmt.Sprintf("shared volume removed successfully (name=%s)", name)
		}
		return "shared volume removed successfully"
	}
	if uuid := MapValueAsString(result, "uuid"); uuid != "<unknown>" {
		return fmt.Sprintf("persistent volume removed successfully (uuid=%s)", uuid)
	}
	return "persistent volume removed successfully"
}

func formatVolumePruneResult(result map[string]any) string {
	dryRun := true
	switch typed := result["dryRun"].(type) {
	case bool:
		dryRun = typed
	case string:
		dryRun = !strings.EqualFold(typed, "false")
	}
	count := 0
	switch typed := result["candidates"].(type) {
	case []any:
		count = len(typed)
	case []string:
		count = len(typed)
	}
	if dryRun {
		return fmt.Sprintf("volume prune dry-run: candidates=%d; pass --yes to destroy", count)
	}
	deleted := 0
	switch typed := result["deleted"].(type) {
	case []any:
		deleted = len(typed)
	case []string:
		deleted = len(typed)
	}
	return fmt.Sprintf("pruned persistent volumes: deleted=%d", deleted)
}

func formatImagePruneResult(result map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(MapValueAsString(result, "mode")))
	engineName := ValueOrDefault(MapValueAsString(result, "engine"), "<unknown>")
	switch mode {
	case "containers":
		return fmt.Sprintf(
			"pruned containers: deleted=%s (engine=%s)",
			MapValueAsString(result, "deletedCount"),
			engineName,
		)
	case "volumes":
		return fmt.Sprintf(
			"pruned volumes: deleted=%s disk reclaimed=%s (engine=%s)",
			MapValueAsString(result, "deletedCount"),
			ValueOrDefault(MapValueAsString(result, "spaceReclaimedHuman"), "-"),
			engineName,
		)
	case "all":
		return fmt.Sprintf(
			"pruned all: containers=%s volumes=%s images=%s disk reclaimed=%s (engine=%s)",
			MapValueAsString(result, "containersDeletedCount"),
			MapValueAsString(result, "volumesDeletedCount"),
			MapValueAsString(result, "imagesDeletedCount"),
			ValueOrDefault(MapValueAsString(result, "spaceReclaimedHuman"), "-"),
			engineName,
		)
	}
	return fmt.Sprintf(
		"pruned dangling images: deleted=%s disk reclaimed=%s (engine=%s)",
		MapValueAsString(result, "deletedCount"),
		ValueOrDefault(MapValueAsString(result, "spaceReclaimedHuman"), "-"),
		engineName,
	)
}

func formatRegistryRemoveResult(result map[string]any) string {
	if id := MapValueAsString(result, "id"); id != "<unknown>" {
		return fmt.Sprintf("registry removed successfully (id=%s)", id)
	}
	return "registry removed successfully"
}

func formatRuntimeClassInspect(result map[string]any) string {
	if len(result) == 0 {
		return "Error[NOT_FOUND]: runtimeclass not found"
	}
	lines := []string{
		fmt.Sprintf("NAME: %s", MapValueAsString(result, "name")),
		fmt.Sprintf("HANDLER: %s", MapValueAsString(result, "handler")),
		fmt.Sprintf("RUNTIME: %s", MapValueAsString(result, "runtimeName")),
	}
	return strings.Join(lines, "\n")
}

func formatRuntimeClassRemoveResult(result map[string]any) string {
	if name := MapValueAsString(result, "name"); name != "<unknown>" {
		return fmt.Sprintf("runtimeclass removed successfully (name=%s)", name)
	}
	return "runtimeclass removed successfully"
}

func formatControlPlaneRestartResult(result map[string]any) string {
	uuid := MapValueAsString(result, "controllerUuid")
	runtimeState := MapValueAsString(result, "runtimeState")
	return fmt.Sprintf("control plane restarted successfully (controllerUuid=%s, runtimeState=%s)", uuid, runtimeState)
}

func formatMSLifecycleResult(path string, result map[string]any) string {
	operation := "operation"
	switch {
	case strings.HasSuffix(path, "/start"):
		operation = "start"
	case strings.HasSuffix(path, "/stop"):
		operation = "stop"
	case strings.HasSuffix(path, "/restart"):
		operation = "restart"
	case strings.HasSuffix(path, "/kill"):
		operation = "kill"
	default:
		operation = "rm"
	}
	uuid := MapValueAsString(result, "microserviceUuid")
	msg := fmt.Sprintf("microservice %s completed successfully", operation)
	if uuid != "<unknown>" {
		msg = fmt.Sprintf("microservice %s completed successfully (uuid=%s)", operation, uuid)
	}
	if warning := strings.TrimSpace(MapValueAsString(result, "warning")); warning != "" && warning != "<unknown>" {
		msg += "\nwarning: " + warning
	}
	return msg
}

func formatDeployValidateResult(result map[string]any) string {
	if valid, ok := result["valid"].(bool); ok && valid {
		return fmt.Sprintf("manifest is valid (kind=%v name=%v apiVersion=%v)", result["kind"], result["name"], result["apiVersion"])
	}
	return "manifest validation result unavailable"
}

func formatDeployApplyResult(result map[string]any) string {
	status := normalizeOperationStatus(MapValueAsString(result, "status"))
	kind := strings.ToLower(strings.TrimSpace(MapValueAsString(result, "kind")))
	operationID := strings.TrimSpace(MapValueAsString(result, "operationId"))
	if accepted, ok := result["accepted"].(bool); ok && accepted {
		switch kind {
		case "registry":
			if reg, ok := result["registry"].(map[string]any); ok {
				return fmt.Sprintf("registry manifest applied successfully (id=%s url=%s)", MapValueAsString(reg, "id"), MapValueAsString(reg, "url"))
			}
			return "registry manifest applied successfully"
		case "model":
			if item, ok := result["model"].(map[string]any); ok {
				return fmt.Sprintf("model manifest applied successfully (name=%s)", MapValueAsString(item, "name"))
			}
			if name := MapValueAsString(result, "name"); name != "<unknown>" {
				return fmt.Sprintf("model manifest applied successfully (name=%s)", name)
			}
			return "model manifest applied successfully"
		case "microservice":
			if id := MapValueAsString(result, "deploymentId"); id != "<unknown>" {
				return fmt.Sprintf("microservice manifest applied successfully (deploymentId=%s)", id)
			}
			return "microservice manifest applied successfully"
		case "runtimeclass":
			if item, ok := result["runtimeClass"].(map[string]any); ok {
				return fmt.Sprintf(
					"runtimeclass manifest applied successfully (name=%s handler=%s)",
					ValueOrDefault(MapValueAsString(item, "name"), "<unknown>"),
					ValueOrDefault(MapValueAsString(item, "handler"), "<unknown>"),
				)
			}
			return "runtimeclass manifest applied successfully"
		case "controlplane":
			return fmt.Sprintf(
				"controlplane manifest applied successfully (controllerUuid=%s namespace=%s name=%s mode=%s)",
				MapValueAsString(result, "controllerUuid"),
				MapValueAsString(result, "namespace"),
				MapValueAsString(result, "name"),
				MapValueAsString(result, "mode"),
			)
		default:
			if id := MapValueAsString(result, "deploymentId"); id != "<unknown>" {
				return fmt.Sprintf("manifest applied successfully (deploymentId=%s)", id)
			}
			return "manifest applied successfully"
		}
	}
	if kind == "runtimeclass" || result["runtimeClass"] != nil {
		switch status {
		case "succeeded":
			if item, ok := result["runtimeClass"].(map[string]any); ok {
				return fmt.Sprintf(
					"runtimeclass manifest applied successfully (name=%s handler=%s)",
					ValueOrDefault(MapValueAsString(item, "name"), "<unknown>"),
					ValueOrDefault(MapValueAsString(item, "handler"), "<unknown>"),
				)
			}
			return "runtimeclass manifest applied successfully"
		case "failed":
			code, message := formatDeployApplyError(result)
			return fmt.Sprintf("Error[%s]: %s", code, message)
		case "queued", "running":
			if operationID != "" && operationID != "<unknown>" {
				return formatRuntimeClassApplyInProgress(operationID, status, normalizeOperationStage(MapValueAsString(result, "stage")))
			}
			return "runtimeclass apply is in progress"
		}
	}
	if status == "succeeded" {
		if id := MapValueAsString(result, "deploymentId"); id != "<unknown>" {
			return fmt.Sprintf("microservice manifest applied successfully (deploymentId=%s)", id)
		}
		return "microservice manifest applied successfully"
	}
	return "manifest apply result unavailable"
}

func formatDeployApplyError(result map[string]any) (string, string) {
	code := "INTERNAL"
	message := ""
	if rawErr, ok := result["error"].(map[string]any); ok {
		if c := strings.TrimSpace(MapValueAsString(rawErr, "code")); c != "" && c != "<unknown>" {
			code = c
		}
		if m := strings.TrimSpace(MapValueAsString(rawErr, "message")); m != "" && m != "<unknown>" {
			message = m
		}
	}
	if message == "" || message == "<unknown>" {
		message = strings.TrimSpace(MapValueAsString(result, "error"))
	}
	if message == "" || message == "<unknown>" {
		message = "deploy apply failed"
	}
	return code, message
}

func normalizeOperationStatus(raw string) string {
	status := strings.TrimSpace(strings.ToLower(raw))
	if status == "" || status == "<unknown>" {
		return "running"
	}
	return status
}

func normalizeOperationStage(raw string) string {
	stage := strings.TrimSpace(strings.ToLower(raw))
	if stage == "" || stage == "<unknown>" {
		return ""
	}
	return stage
}

func formatRuntimeClassApplyInProgress(operationID, status, stage string) string {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		operationID = "<unknown>"
	}
	status = normalizeOperationStatus(status)
	stage = normalizeOperationStage(stage)
	if stage != "" {
		return fmt.Sprintf(
			"runtimeclass apply is still in progress (operationId=%s status=%s stage=%s)\npoll endpoint: GET /v1/deploy/runtimeclasses:apply/%s",
			operationID, status, stage, operationID,
		)
	}
	return fmt.Sprintf(
		"runtimeclass apply is still in progress (operationId=%s status=%s)\npoll endpoint: GET /v1/deploy/runtimeclasses:apply/%s",
		operationID, status, operationID,
	)
}
