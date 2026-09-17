package handlers

import (
	"strconv"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
)

func augmentWithRuntimeStatus(status map[string]any) {
	if status == nil {
		return
	}
	cfg := config.GetInstance()
	rs := runtimestate.GetState()
	status["runtime.engine"] = cfg.ContainerEngine
	status["runtime.containerEngineUrl"] = cfg.ContainerEngineURL
	status["runtime.pendingRestart"] = strconv.FormatBool(rs.PendingRestart())
	status["runtime.engineReady"] = strconv.FormatBool(rs.EngineReady())
	status["runtime.shutdownPolicy"] = cfg.ShutdownPolicy
	if phase := rs.AgentPhase(); phase != "" {
		status["runtime.agentPhase"] = phase
	}
}
