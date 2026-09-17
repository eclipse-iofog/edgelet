package deploy

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/output"
)

// FormatValidateHuman renders validate endpoint output.
func FormatValidateHuman(result map[string]any) string {
	if valid, ok := result["valid"].(bool); ok && valid {
		return fmt.Sprintf("manifest is valid (kind=%v name=%v apiVersion=%v)", result["kind"], result["name"], result["apiVersion"])
	}
	return "manifest validation result unavailable"
}

// FormatApplyHuman renders apply endpoint output for human mode.
func FormatApplyHuman(result map[string]any) string {
	status := normalizeStatus(output.MapValueAsString(result, "status"))
	kind := strings.ToLower(strings.TrimSpace(output.MapValueAsString(result, "kind")))
	operationID := strings.TrimSpace(output.MapValueAsString(result, "operationId"))

	if accepted, ok := result["accepted"].(bool); ok && accepted {
		switch strings.ToLower(strings.TrimSpace(output.MapValueAsString(result, "kind"))) {
		case "registry":
			if reg, ok := result["registry"].(map[string]any); ok {
				return fmt.Sprintf("registry manifest applied successfully (id=%s url=%s)", output.MapValueAsString(reg, "id"), output.MapValueAsString(reg, "url"))
			}
			return "registry manifest applied successfully"
		case "model":
			if item, ok := result["model"].(map[string]any); ok {
				return fmt.Sprintf("model manifest applied successfully (name=%s)", output.MapValueAsString(item, "name"))
			}
			if name := output.MapValueAsString(result, "name"); name != "<unknown>" {
				return fmt.Sprintf("model manifest applied successfully (name=%s)", name)
			}
			return "model manifest applied successfully"
		case "microservice":
			if id := output.MapValueAsString(result, "deploymentId"); id != "<unknown>" {
				return fmt.Sprintf("microservice manifest applied successfully (deploymentId=%s)", id)
			}
			return "microservice manifest applied successfully"
		case "runtimeclass":
			if item, ok := result["runtimeClass"].(map[string]any); ok {
				return fmt.Sprintf(
					"runtimeclass manifest applied successfully (name=%s handler=%s)",
					output.ValueOrDefault(output.MapValueAsString(item, "name"), "<unknown>"),
					output.ValueOrDefault(output.MapValueAsString(item, "handler"), "<unknown>"),
				)
			}
			return "runtimeclass manifest applied successfully"
		case "controlplane":
			return fmt.Sprintf(
				"controlplane manifest applied successfully (controllerUuid=%s namespace=%s name=%s mode=%s)",
				output.MapValueAsString(result, "controllerUuid"),
				output.MapValueAsString(result, "namespace"),
				output.MapValueAsString(result, "name"),
				output.MapValueAsString(result, "mode"),
			)
		default:
			if id := output.MapValueAsString(result, "deploymentId"); id != "<unknown>" {
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
					output.ValueOrDefault(output.MapValueAsString(item, "name"), "<unknown>"),
					output.ValueOrDefault(output.MapValueAsString(item, "handler"), "<unknown>"),
				)
			}
			return "runtimeclass manifest applied successfully"
		case "failed":
			code, message := ApplyError(result)
			return fmt.Sprintf("Error[%s]: %s", code, message)
		case "queued", "running":
			if operationID != "" && operationID != "<unknown>" {
				return FormatRuntimeClassInProgress(operationID, status, normalizeStage(output.MapValueAsString(result, "stage")))
			}
			return "runtimeclass apply is in progress"
		}
	}

	if status == "succeeded" {
		if controllerUUID := output.MapValueAsString(result, "controllerUuid"); controllerUUID != "<unknown>" && controllerUUID != "" {
			return fmt.Sprintf(
				"controlplane manifest applied successfully (controllerUuid=%s namespace=%s name=%s mode=%s)",
				controllerUUID,
				output.MapValueAsString(result, "namespace"),
				output.MapValueAsString(result, "name"),
				output.MapValueAsString(result, "mode"),
			)
		}
		if id := output.MapValueAsString(result, "deploymentId"); id != "<unknown>" {
			return fmt.Sprintf("microservice manifest applied successfully (deploymentId=%s)", id)
		}
		return "microservice manifest applied successfully"
	}
	if status == "failed" {
		code, message := ApplyError(result)
		return fmt.Sprintf("Error[%s]: %s", code, message)
	}
	return "manifest apply result unavailable"
}

// ApplyError extracts structured apply failure details.
func ApplyError(result map[string]any) (code, message string) {
	code = "INTERNAL"
	message = ""
	if rawErr, ok := result["error"].(map[string]any); ok {
		if c := strings.TrimSpace(output.MapValueAsString(rawErr, "code")); c != "" && c != "<unknown>" {
			code = c
		}
		if m := strings.TrimSpace(output.MapValueAsString(rawErr, "message")); m != "" && m != "<unknown>" {
			message = m
		}
	}
	if message == "" || message == "<unknown>" {
		message = strings.TrimSpace(output.MapValueAsString(result, "error"))
	}
	if message == "" || message == "<unknown>" {
		message = "deploy apply failed"
	}
	return code, message
}

// FormatRuntimeClassInProgress renders a timeout/in-progress message for runtimeclass apply.
func FormatRuntimeClassInProgress(operationID, status, stage string) string {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		operationID = "<unknown>"
	}
	status = normalizeStatus(status)
	stage = normalizeStage(stage)
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

func normalizeStatus(raw string) string {
	status := strings.TrimSpace(strings.ToLower(raw))
	if status == "" || status == "<unknown>" {
		return "running"
	}
	return status
}

func normalizeStage(raw string) string {
	stage := strings.TrimSpace(strings.ToLower(raw))
	if stage == "" || stage == "<unknown>" {
		return ""
	}
	return stage
}

// WithStages attaches observed poll stages to a result map for structured output.
func WithStages(result map[string]any, stages []string) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	if len(stages) == 0 {
		return result
	}
	out := make(map[string]any, len(result)+1)
	for k, v := range result {
		out[k] = v
	}
	stageValues := make([]any, len(stages))
	for i, stage := range stages {
		stageValues[i] = stage
	}
	out["stages"] = stageValues
	return out
}
