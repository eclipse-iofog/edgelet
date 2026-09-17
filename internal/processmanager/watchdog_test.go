package processmanager

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
)

func controllerWatchdogLabels(uuid string) map[string]string {
	return map[string]string{
		workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
		workloadmeta.LabelMicroserviceUID: uuid,
		workloadmeta.LabelSystem:          "true",
		workloadmeta.LabelRole:            workloadmeta.RoleController,
		workloadmeta.LabelScope:           workloadmeta.ScopeLocal,
	}
}

func TestIsControllerWorkload_FullIdentityMatch(t *testing.T) {
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		ContainerID:    "cid-abc",
		Image:          "ghcr.io/datasance/controller:3.7.0",
	}
	labels := controllerWatchdogLabels("cp-uuid-1")
	if !IsControllerWorkload(labels, "cid-abc", "ghcr.io/datasance/controller:3.7.0", dep) {
		t.Fatal("expected full controller identity to match deployment")
	}
}

func TestIsControllerWorkload_RejectsPartialIdentity(t *testing.T) {
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		ContainerID:    "cid-abc",
		Image:          "ghcr.io/datasance/controller:3.7.0",
	}
	labels := controllerWatchdogLabels("cp-uuid-1")

	tests := []struct {
		name        string
		containerID string
		image       string
		labels      map[string]string
		dep         *models.ControlPlaneDeployment
	}{
		{name: "wrong uuid", labels: controllerWatchdogLabels("other"), containerID: "cid-abc", image: dep.Image, dep: dep},
		{name: "wrong container id", labels: labels, containerID: "cid-other", image: dep.Image, dep: dep},
		{name: "wrong image", labels: labels, containerID: "cid-abc", image: "ghcr.io/datasance/controller:9.9.9", dep: dep},
		{name: "missing dep", labels: labels, containerID: "cid-abc", image: dep.Image, dep: nil},
		{name: "missing system label", labels: func() map[string]string {
			l := controllerWatchdogLabels("cp-uuid-1")
			delete(l, workloadmeta.LabelSystem)
			return l
		}(), containerID: "cid-abc", image: dep.Image, dep: dep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsControllerWorkload(tt.labels, tt.containerID, tt.image, tt.dep) {
				t.Fatal("expected partial controller identity to be rejected")
			}
		})
	}
}

func TestIsControllerWorkload_ImageAliasMatch(t *testing.T) {
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		ContainerID:    "cid-abc",
		Image:          "docker.io/library/alpine:3.19",
	}
	labels := controllerWatchdogLabels("cp-uuid-1")
	if !IsControllerWorkload(labels, "cid-abc", "alpine:3.19", dep) {
		t.Fatal("expected docker hub alias to match controller image")
	}
}

func TestWatchdog_CleanupDecision_SkipsControllerWithFullIdentity(t *testing.T) {
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		ContainerID:    "cid-abc",
		Image:          "ghcr.io/datasance/controller:3.7.0",
	}
	labels := controllerWatchdogLabels("cp-uuid-1")

	removeManaged, removeUnknown := cleanupDecisionForContainer(
		labels,
		"cid-abc",
		"ghcr.io/datasance/controller:3.7.0",
		false,
		false,
		true,
		dep,
	)
	if removeManaged || removeUnknown {
		t.Fatalf("expected controller with full identity to be preserved, got managed=%v unknown=%v", removeManaged, removeUnknown)
	}
}

func TestWatchdog_CleanupDecision_RemovesControllerIdentityMismatch(t *testing.T) {
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		ContainerID:    "cid-abc",
		Image:          "ghcr.io/datasance/controller:3.7.0",
	}
	labels := controllerWatchdogLabels("cp-uuid-1")

	removeManaged, removeUnknown := cleanupDecisionForContainer(
		labels,
		"cid-stale",
		"ghcr.io/datasance/controller:3.7.0",
		false,
		false,
		true,
		dep,
	)
	if removeManaged {
		t.Fatal("expected local-scope controller mismatch not removed by managed uuid path")
	}
	if !removeUnknown {
		t.Fatal("expected stale controller container to be removed by unknown id when watchdog enabled")
	}
}

func TestIsEdgeletSelfContainer_Match(t *testing.T) {
	t.Setenv("EDGELET_DAEMON", "container")
	t.Setenv("EDGELET_IMAGE", "ghcr.io/eclipse-iofog/edgelet:v1")

	labels := map[string]string{
		workloadmeta.LabelRole: workloadmeta.RoleEdgelet,
	}
	if !IsEdgeletSelfContainer(labels, "ghcr.io/eclipse-iofog/edgelet:v1") {
		t.Fatal("expected edgelet self container to match EDGELET_IMAGE")
	}
}

func TestIsEdgeletSelfContainer_RejectsWithoutEnv(t *testing.T) {
	labels := map[string]string{
		workloadmeta.LabelRole: workloadmeta.RoleEdgelet,
	}
	t.Setenv("EDGELET_DAEMON", "")
	t.Setenv("EDGELET_IMAGE", "")
	if IsEdgeletSelfContainer(labels, "ghcr.io/eclipse-iofog/edgelet:v1") {
		t.Fatal("expected edgelet self check to fail without container env")
	}
}

func TestWatchdog_CleanupDecision_SkipsEdgeletSelfContainer(t *testing.T) {
	t.Setenv("EDGELET_DAEMON", "container")
	t.Setenv("EDGELET_IMAGE", "edgelet:local")

	labels := map[string]string{
		workloadmeta.LabelRole:            workloadmeta.RoleEdgelet,
		workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
		workloadmeta.LabelMicroserviceUID: "edgelet-self",
		workloadmeta.LabelScope:           workloadmeta.ScopeManaged,
	}

	removeManaged, removeUnknown := cleanupDecisionForContainer(
		labels,
		"edgelet-cid",
		"edgelet:local",
		false,
		false,
		true,
		nil,
	)
	if removeManaged || removeUnknown {
		t.Fatalf("expected edgelet self container to be skipped, got managed=%v unknown=%v", removeManaged, removeUnknown)
	}
}

func TestWatchdog_CleanupDecision_StillSkipsOtherSystemWorkloads(t *testing.T) {
	labels := map[string]string{
		workloadmeta.LabelSystem:          "true",
		workloadmeta.LabelRole:            workloadmeta.RoleRouter,
		workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
		workloadmeta.LabelMicroserviceUID: "router-1",
		workloadmeta.LabelScope:           workloadmeta.ScopeManaged,
	}

	removeManaged, removeUnknown := cleanupDecisionForContainer(
		labels,
		"router-cid",
		"router:latest",
		false,
		false,
		true,
		nil,
	)
	if removeManaged || removeUnknown {
		t.Fatalf("expected non-controller system workload to be preserved, got managed=%v unknown=%v", removeManaged, removeUnknown)
	}
}

func TestLocalWorkloadsOutOfScope(t *testing.T) {
	if !LocalWorkloadsOutOfScope(true) {
		t.Fatal("expected local workloads out of scope when watchdog is enabled")
	}
	if LocalWorkloadsOutOfScope(false) {
		t.Fatal("expected local workloads in scope when watchdog is disabled")
	}
}

func TestCleanupLocalModelsForWatchdog_InvokesCallbackWhenEnabled(t *testing.T) {
	called := false
	pm := &ProcessManager{}
	pm.SetWatchdogLocalModelsCallback(func() { called = true })

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	pm.cleanupLocalModelsForWatchdog()
	if !called {
		t.Fatal("expected local model cleanup when watchdog is enabled")
	}
}

func TestCleanupLocalModelsForWatchdog_SkippedWhenDisabled(t *testing.T) {
	called := false
	pm := &ProcessManager{}
	pm.SetWatchdogLocalModelsCallback(func() { called = true })

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	pm.cleanupLocalModelsForWatchdog()
	if called {
		t.Fatal("expected no local model cleanup when watchdog is disabled")
	}
}
