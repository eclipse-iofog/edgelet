package processmanager

import (
	"os"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

// IsControllerWorkload reports whether labels, container ID, and image match the
// singleton control plane deployment record (full controller identity).
func IsControllerWorkload(labels map[string]string, containerID, image string, dep *models.ControlPlaneDeployment) bool {
	if dep == nil {
		return false
	}
	if strings.TrimSpace(dep.ControllerUUID) == "" {
		return false
	}
	if !workloadmeta.IsSystemWorkload(labels) {
		return false
	}
	if !workloadmeta.IsControllerRole(labels) {
		return false
	}
	if workloadmeta.MicroserviceUIDFromLabels(labels) != strings.TrimSpace(dep.ControllerUUID) {
		return false
	}
	if strings.TrimSpace(containerID) == "" || strings.TrimSpace(dep.ContainerID) == "" {
		return false
	}
	if containerID != strings.TrimSpace(dep.ContainerID) {
		return false
	}
	if strings.TrimSpace(image) == "" || strings.TrimSpace(dep.Image) == "" {
		return false
	}
	return imageRefsMatch(image, dep.Image)
}

// IsEdgeletSelfContainer reports whether the container is the edgelet daemon running
// inside its own image (EDGELET_DAEMON=container). Set EDGELET_IMAGE on the host
// container to the image ref used at deploy time so the watchdog can skip self-removal.
func IsEdgeletSelfContainer(labels map[string]string, containerImage string) bool {
	if !workloadmeta.IsEdgeletRole(labels) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("EDGELET_DAEMON")), "container") {
		return false
	}
	expected := strings.TrimSpace(os.Getenv("EDGELET_IMAGE"))
	if expected == "" {
		return false
	}
	return imageRefsMatch(containerImage, expected)
}

func imageRefsMatch(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	_, aliasesA := imageref.Resolve(a, "", true)
	_, aliasesB := imageref.Resolve(b, "", true)
	for _, la := range aliasesA {
		for _, lb := range aliasesB {
			if la == lb {
				return true
			}
		}
	}
	return false
}

// LocalWorkloadsOutOfScope reports whether local microservices and local models
// are excluded because watchdog is enabled.
func LocalWorkloadsOutOfScope(watchdogEnabled bool) bool {
	return watchdogEnabled
}

func cleanupDecisionForContainer(
	labels map[string]string,
	containerID, image string,
	isCurrent, isLatest, watchdogEnabled bool,
	cpDep *models.ControlPlaneDeployment,
) (removeManagedByUUID bool, removeUnknownByID bool) {
	if IsEdgeletSelfContainer(labels, image) {
		return false, false
	}
	if workloadmeta.IsControllerRole(labels) {
		if IsControllerWorkload(labels, containerID, image, cpDep) {
			return false, false
		}
	} else if workloadmeta.IsSystemWorkload(labels) {
		return false, false
	}

	isLocalScope := strings.EqualFold(strings.TrimSpace(labels[workloadmeta.LabelScope]), workloadmeta.ScopeLocal)
	// Agent-managed, non-local workloads that no longer exist in desired latest set
	// should be removed regardless of watchdog setting.
	if !isLatest && workloadmeta.IsManagedByIofog(labels) && !isLocalScope {
		return true, false
	}

	// Unknown workload cleanup remains watchdog-gated.
	if !isCurrent && !isLatest && watchdogEnabled {
		return false, true
	}
	return false, false
}
