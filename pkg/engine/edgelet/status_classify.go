package edgelet

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// classifyMissingTask maps a container that has no containerd task.
// Containerd deletes the task after the process exits. The CRI state is the
// source of truth: exited is finished, running stays running, and created has
// not started. Crash text is recorded only for an exited container.
func classifyMissingTask(state runtimeapi.ContainerState, reason string, exitCode int32, message string) (models.MicroserviceState, string) {
	var msState models.MicroserviceState
	switch state {
	case runtimeapi.ContainerState_CONTAINER_CREATED:
		msState = models.MicroserviceStateCreated
	case runtimeapi.ContainerState_CONTAINER_RUNNING:
		msState = models.MicroserviceStateRunning
	case runtimeapi.ContainerState_CONTAINER_EXITED:
		msState = models.MicroserviceStateExiting
	default:
		msState = models.MicroserviceStateUnknown
	}
	if msState != models.MicroserviceStateExiting {
		return msState, ""
	}
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if reason == "" && message == "" && exitCode == 0 {
		return msState, ""
	}
	return msState, fmt.Sprintf("CRI reason=%s exitCode=%d message=%s", reasonOrUnknown(reason), exitCode, message)
}

func reasonOrUnknown(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "UNKNOWN"
	}
	return trimmed
}
