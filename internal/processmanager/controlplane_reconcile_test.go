package processmanager

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/controlplane"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type controlPlaneCaptureEngine struct {
	lifecycleTestEngine
	lastMS *models.Microservice
}

func (e *controlPlaneCaptureEngine) CreateContainer(ms *models.Microservice, _ string) (string, error) {
	e.lastMS = ms
	return "cp-cid-" + ms.MicroserviceUUID, nil
}

type controlPlaneStatsEngine struct {
	controlPlaneCaptureEngine
	stats *engine.ContainerStats
}

func (e *controlPlaneStatsEngine) GetContainerStats(string) (*engine.ContainerStats, error) {
	if e.stats == nil {
		return &engine.ContainerStats{}, nil
	}
	return e.stats, nil
}

func TestReconcileControlPlane_NoDeploymentIsNoOp(t *testing.T) {
	openLocalReconcileTestDB(t)
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	pm.reconcileControlPlane()
}

func TestReconcileControlPlane_LaunchesMissingContainer(t *testing.T) {
	openLocalReconcileTestDB(t)

	eng := &controlPlaneCaptureEngine{}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-launch-1",
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   minimalControlPlaneManifestYAML(),
		DesiredState:   "running",
		Generation:     1,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	pm.reconcileControlPlane()

	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("expected runtime_state=running, got %q", got.RuntimeState)
	}
	if got.ContainerID != "cp-cid-cp-launch-1" {
		t.Fatalf("expected container id cp-cid-cp-launch-1, got %q", got.ContainerID)
	}
	if eng.lastMS == nil {
		t.Fatal("expected CreateContainer to be called")
	}
	assertControlPlaneLaunchSpec(t, eng.lastMS)
}

func TestReconcileControlPlane_SkipsLaunchWhenApplyInFlight(t *testing.T) {
	openLocalReconcileTestDB(t)
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-inflight",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         2,
		ObservedGeneration: 0,
		RuntimeState:       "starting",
		LastStartAttemptAt: 9999999999,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	launchCalled := false
	pm.launchControlPlaneFn = func(*models.ControlPlaneDeployment, int64) {
		launchCalled = true
	}
	pm.reconcileControlPlane()

	if launchCalled {
		t.Fatal("expected reconcile to skip launch while apply is in-flight")
	}
}

func TestReconcileControlPlane_RecreatesOnGenerationBump(t *testing.T) {
	openLocalReconcileTestDB(t)

	eng := &controlPlaneCaptureEngine{}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-recreate",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         2,
		ObservedGeneration: 1,
		RuntimeState:       "running",
		ContainerID:        "old-cid",
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.7.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-recreate",
		},
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateRunning}, nil
	}

	pm.reconcileControlPlane()

	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.ObservedGeneration != 2 {
		t.Fatalf("expected observed generation 2, got %d", got.ObservedGeneration)
	}
	if got.ContainerID != "cp-cid-cp-recreate" {
		t.Fatalf("expected recreated container id, got %q", got.ContainerID)
	}
}

func TestReconcileControlPlane_RecreatesWithPullOnRecreateFlag(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-recreate-pull",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         2,
		ObservedGeneration: 1,
		RuntimeState:       "running",
		ContainerID:        "old-cid",
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	pm.SetControlPlanePullOnRecreate(true)

	pullImage := false
	pm.recreateControlPlaneFn = func(_ *models.ControlPlaneDeployment, wantPull bool, _ int64) error {
		pullImage = wantPull
		return nil
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateRunning}, nil
	}

	eng := &controlPlaneCaptureEngine{}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.7.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-recreate-pull",
		},
	}
	pm.engine = eng
	pm.containerManager = NewContainerManager(eng, nil, "docker")

	pm.reconcileControlPlane()

	if !pullImage {
		t.Fatal("expected recreateControlPlaneFn to receive pullImage=true")
	}
	if pm.consumeControlPlanePullOnRecreate() {
		t.Fatal("expected pull-on-recreate flag to be consumed")
	}
}

func TestReconcileControlPlane_ReportsContainerStatsWhenRegistered(t *testing.T) {
	openLocalReconcileTestDB(t)
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &controlPlaneStatsEngine{
		stats: &engine.ContainerStats{CPUUsage: 12.5, MemoryUsage: 987654},
	}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:       "cp-stats",
		Namespace:            "default",
		Name:                 "pot",
		ManifestYAML:         minimalControlPlaneManifestYAML(),
		DesiredState:         "running",
		Generation:           1,
		ObservedGeneration:   1,
		RuntimeState:         "running",
		ContainerID:          "old-cid",
		ControllerRegistered: true,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.7.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-stats",
		},
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateRunning}, nil
	}

	pm.reconcileControlPlane()

	msStatus := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("cp-stats")
	if msStatus == nil {
		t.Fatal("expected controller microservice status")
	}
	if msStatus.CPUUsage != 0 || msStatus.MemoryUsage != 0 {
		t.Fatalf("reconcile must not sample usage, got cpu=%v memory=%d", msStatus.CPUUsage, msStatus.MemoryUsage)
	}

	pm.sampleRunningContainerStats()
	msStatus = statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("cp-stats")
	if msStatus.CPUUsage != 12.5 {
		t.Fatalf("expected cpuUsage=12.5, got %v", msStatus.CPUUsage)
	}
	if msStatus.MemoryUsage != 987654 {
		t.Fatalf("expected memoryUsage=987654, got %d", msStatus.MemoryUsage)
	}
}

func TestReconcileControlPlane_OmitsContainerStatsBeforeRegister(t *testing.T) {
	openLocalReconcileTestDB(t)
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &controlPlaneStatsEngine{
		stats: &engine.ContainerStats{CPUUsage: 12.5, MemoryUsage: 987654},
	}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-no-stats",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         1,
		ObservedGeneration: 1,
		RuntimeState:       "running",
		ContainerID:        "old-cid",
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.7.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-no-stats",
		},
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateRunning}, nil
	}

	pm.reconcileControlPlane()
	pm.sampleRunningContainerStats()

	msStatus := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("cp-no-stats")
	if msStatus == nil {
		t.Fatal("expected controller microservice status")
	}
	if msStatus.CPUUsage != 0 || msStatus.MemoryUsage != 0 {
		t.Fatalf("expected stats omitted before register, got cpu=%v memory=%d", msStatus.CPUUsage, msStatus.MemoryUsage)
	}
}

func TestBuildControlPlaneLaunchSpec(t *testing.T) {
	openLocalReconcileTestDB(t)
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}

	item := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-spec",
		ManifestYAML:   minimalControlPlaneManifestYAML(),
	}
	_, ms, _, err := pm.buildControlPlaneLaunchSpec(item)
	if err != nil {
		t.Fatalf("build launch spec: %v", err)
	}
	assertControlPlaneLaunchSpec(t, ms)
}

func assertControlPlaneLaunchSpec(t *testing.T, ms *models.Microservice) {
	t.Helper()
	if ms == nil {
		t.Fatal("microservice is nil")
	}
	if !ms.IsController || !ms.IsSystem {
		t.Fatal("expected controller system flags")
	}
	if ms.ApplicationName != "default" || ms.MicroserviceName != "pot" {
		t.Fatalf("unexpected identity application=%q name=%q", ms.ApplicationName, ms.MicroserviceName)
	}
	if len(ms.PortMappings) != 2 {
		t.Fatalf("expected 2 port mappings, got %d", len(ms.PortMappings))
	}
	if ms.PortMappings[0].Outside != controlplane.HostAPIPort || ms.PortMappings[1].Outside != controlplane.HostConsolePort {
		t.Fatalf("unexpected host ports: %+v", ms.PortMappings)
	}

	hasNetRaw := false
	for _, cap := range ms.CapAdd {
		if strings.EqualFold(cap, "NET_RAW") {
			hasNetRaw = true
		}
	}
	if !hasNetRaw {
		t.Fatal("expected NET_RAW in CapAdd")
	}

	volumes := map[string]string{}
	for _, vm := range ms.VolumeMappings {
		volumes[vm.HostDestination] = vm.ContainerDestination
	}
	if volumes[controlplane.VolumeDBName] != controlplane.ContainerDBPath {
		t.Fatalf("missing db volume mapping: %#v", volumes)
	}
	if volumes[controlplane.VolumeLogName] != controlplane.ContainerLogPath {
		t.Fatalf("missing log volume mapping: %#v", volumes)
	}

	in := workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         "node-1",
		RuntimeEngine:    workloadmeta.RuntimeEngineDocker,
		IsController:     true,
		IsSystem:         true,
	}
	labels := workloadmeta.BuildLabels(in)
	if labels[workloadmeta.LabelRole] != workloadmeta.RoleController {
		t.Fatalf("expected role=controller label, got %q", labels[workloadmeta.LabelRole])
	}
	if labels[workloadmeta.LabelAppPartOf] != "default" || labels[workloadmeta.LabelAppName] != "pot" {
		t.Fatalf("unexpected dns identity labels: %#v", labels)
	}
}

func TestReconcileControlPlaneDesiredRunning_KeepsLastErrorOnFirstRunning(t *testing.T) {
	openLocalReconcileTestDB(t)
	withStatusSyncClock(t)
	sr := statusreporter.GetInstance()
	sr.ResetProcessManagerStatus()
	t.Cleanup(func() { sr.ResetProcessManagerStatus() })

	errMsg := "controller crashed"
	sr.UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
		pmStatus.SetMicroservicesState("cp-keep-err", models.MicroserviceStateExiting)
		pmStatus.SetMicroservicesStatusErrorMessage("cp-keep-err", errMsg)
	})

	eng := &controlPlaneCaptureEngine{}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}
	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-keep-err",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         1,
		ObservedGeneration: 1,
		RuntimeState:       "exiting",
		ContainerID:        "old-cid",
		LastError:          errMsg,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.8.0-beta.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-keep-err",
		},
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateRunning, ContainerID: "old-cid"}, nil
	}

	pm.reconcileControlPlane()

	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected sqlite last_error kept on first running, got %q", got.LastError)
	}
	msStatus := sr.GetProcessManagerStatus().GetMicroserviceStatus("cp-keep-err")
	if msStatus.ErrorMessage == nil || *msStatus.ErrorMessage != errMsg {
		t.Fatalf("expected reporter error kept before grace, got %#v", msStatus.ErrorMessage)
	}
	if msStatus.LastError != errMsg {
		t.Fatalf("expected reporter lastError kept, got %q", msStatus.LastError)
	}
}

func minimalControlPlaneManifestYAML() string {
	return `apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
  namespace: default
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: AdminPass123!
`
}
