package models

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

// PodIDForEngine returns the status pod id for a running container.
// The edgelet engine uses the pause / sandbox id. Docker and podman reuse the
// app container id. Unknown values are omitted by callers (empty string).
func PodIDForEngine(engineName, containerID, sandboxID string) string {
	containerID = strings.TrimSpace(containerID)
	sandboxID = strings.TrimSpace(sandboxID)
	switch strings.ToLower(strings.TrimSpace(engineName)) {
	case constants.EngineDocker, constants.EnginePodman:
		return containerID
	default:
		return sandboxID
	}
}

// ApplyPodID sets PodID from the current engine and optional sandbox id.
func (m *MicroserviceStatus) ApplyPodID(engineName, sandboxID string) {
	if m == nil {
		return
	}
	m.PodID = PodIDForEngine(engineName, m.ContainerID, sandboxID)
}
