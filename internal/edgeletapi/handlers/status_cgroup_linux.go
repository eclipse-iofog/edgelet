//go:build linux && cgo

package handlers

import (
	"strconv"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/cgroups"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func augmentWithCgroupStatus(status map[string]any) {
	if status == nil {
		return
	}
	snap := cgroups.GetSnapshot()
	status["cgroupMode"] = snap.Mode
	status["cgroupDriver"] = snap.Driver
	status["cgroupNested"] = strconv.FormatBool(snap.Nested)
	status["cgroupDelegatedControllers"] = snap.DelegatedControllers
	status["cgroupAgentPath"] = snap.AgentCgroupPath
	status["cgroupContainerdPath"] = snap.ContainerdCgroupPath
}

func shouldAugmentCgroupStatus() bool {
	if !buildmeta.HasEmbeddedEngine() {
		return false
	}
	cfg := config.GetInstance()
	if cfg == nil {
		return false
	}
	return cfg.ContainerEngine == constants.EngineEdgelet
}
