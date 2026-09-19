package processmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/controlplane"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type controlPlaneRestartTrackEngine struct {
	lifecycleTestEngine
	stopCount  int
	startCount int
	status     models.MicroserviceState
}

func (e *controlPlaneRestartTrackEngine) StopContainer(string) error {
	e.stopCount++
	e.status = models.MicroserviceStateCreated
	return nil
}

func (e *controlPlaneRestartTrackEngine) StartContainer(string) error {
	e.startCount++
	e.status = models.MicroserviceStateRunning
	return nil
}

func (e *controlPlaneRestartTrackEngine) GetContainerStatus(string, string) (*models.MicroserviceStatus, error) {
	if e.status == "" {
		e.status = models.MicroserviceStateRunning
	}
	return &models.MicroserviceStatus{Status: e.status}, nil
}

func controlPlaneRestartTestDeployment(uuid string) *models.ControlPlaneDeployment {
	return &models.ControlPlaneDeployment{
		ControllerUUID:     uuid,
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         1,
		ObservedGeneration: 1,
		RuntimeState:       "running",
		ContainerID:        "old-cid",
	}
}

func TestRestartControlPlaneDeployment_EmbeddedRecreatesWithoutPull(t *testing.T) {
	openLocalReconcileTestDB(t)

	recreateCalled := false
	pm := &ProcessManager{
		logger:     logging.NewModuleLogger("test-process-manager"),
		engineName: "edgelet",
	}
	pm.recreateControlPlaneFn = func(_ *models.ControlPlaneDeployment, pullImage bool, _ int64) error {
		recreateCalled = true
		if pullImage {
			t.Fatal("expected pullImage=false for embedded default restart")
		}
		return nil
	}

	eng := &controlPlaneRestartTrackEngine{}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.8.0-beta.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-restart-embedded",
		},
	}
	pm.engine = eng
	pm.containerManager = NewContainerManager(eng, nil, "edgelet")

	dep := controlPlaneRestartTestDeployment("cp-restart-embedded")
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	if err := pm.RestartControlPlaneDeployment(dep, false); err != nil {
		t.Fatalf("RestartControlPlaneDeployment: %v", err)
	}
	if !recreateCalled {
		t.Fatal("expected recreateControlPlaneDeployment path for embedded engine")
	}
	if eng.stopCount != 0 || eng.startCount != 0 {
		t.Fatalf("expected no in-place stop/start, got stop=%d start=%d", eng.stopCount, eng.startCount)
	}
}

func TestRestartControlPlaneDeployment_DockerInPlaceStopStart(t *testing.T) {
	openLocalReconcileTestDB(t)

	recreateCalled := false
	pm := &ProcessManager{
		logger:     logging.NewModuleLogger("test-process-manager"),
		engineName: "docker",
	}
	pm.recreateControlPlaneFn = func(*models.ControlPlaneDeployment, bool, int64) error {
		recreateCalled = true
		return nil
	}

	eng := &controlPlaneRestartTrackEngine{}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.8.0-beta.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-restart-docker",
		},
	}
	pm.engine = eng
	pm.containerManager = NewContainerManager(eng, nil, "docker")

	dep := controlPlaneRestartTestDeployment("cp-restart-docker")
	dep.LastError = "previous crash"
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	if err := pm.RestartControlPlaneDeployment(dep, false); err != nil {
		t.Fatalf("RestartControlPlaneDeployment: %v", err)
	}
	if recreateCalled {
		t.Fatal("expected in-place stop+start for docker without pull")
	}
	if eng.stopCount != 1 || eng.startCount != 1 {
		t.Fatalf("expected stop=1 start=1, got stop=%d start=%d", eng.stopCount, eng.startCount)
	}

	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("expected runtime_state=running, got %q", got.RuntimeState)
	}
	if got.LastError != "previous crash" {
		t.Fatalf("expected last_error kept after restart, got %q", got.LastError)
	}
}

func TestRestartControlPlaneDeployment_PullRecreates(t *testing.T) {
	openLocalReconcileTestDB(t)

	var gotPull bool
	pm := &ProcessManager{
		logger:     logging.NewModuleLogger("test-process-manager"),
		engineName: "docker",
	}
	pm.recreateControlPlaneFn = func(_ *models.ControlPlaneDeployment, pullImage bool, _ int64) error {
		gotPull = pullImage
		return nil
	}

	eng := &controlPlaneRestartTrackEngine{}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.8.0-beta.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-restart-pull",
		},
	}
	pm.engine = eng
	pm.containerManager = NewContainerManager(eng, nil, "docker")

	dep := controlPlaneRestartTestDeployment("cp-restart-pull")
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	if err := pm.RestartControlPlaneDeployment(dep, true); err != nil {
		t.Fatalf("RestartControlPlaneDeployment: %v", err)
	}
	if !gotPull {
		t.Fatal("expected recreate with pullImage=true")
	}
	if eng.stopCount != 0 || eng.startCount != 0 {
		t.Fatalf("expected no in-place stop/start with pull, got stop=%d start=%d", eng.stopCount, eng.startCount)
	}
}

func TestRestartControlPlaneDeployment_LaunchesMissingContainer(t *testing.T) {
	openLocalReconcileTestDB(t)

	launchCalled := false
	eng := &lifecycleTestEngine{}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engineName:       "docker",
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}
	pm.launchControlPlaneFn = func(item *models.ControlPlaneDeployment, _ int64) {
		launchCalled = true
		item.RuntimeState = "running"
		item.State = item.RuntimeState
		item.ContainerID = "cp-cid-launched"
		item.LastError = ""
		_ = store.GetInstance().UpsertSystemControlPlane(item)
	}

	dep := controlPlaneRestartTestDeployment("cp-restart-launch")
	dep.ContainerID = ""
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	if err := pm.RestartControlPlaneDeployment(dep, false); err != nil {
		t.Fatalf("RestartControlPlaneDeployment: %v", err)
	}
	if !launchCalled {
		t.Fatal("expected launch path when container is missing")
	}
}

func openControlPlaneVolumeTest(t *testing.T) string {
	t.Helper()
	disk := t.TempDir()
	cfg := config.GetInstance()
	orig := cfg.DiskDirectory
	cfg.DiskDirectory = disk
	t.Cleanup(func() { cfg.DiskDirectory = orig })

	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(disk); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return disk
}

func writeControlPlaneVolumeMarkers(t *testing.T, disk, uuid string) (dbMarker, logMarker string) {
	t.Helper()
	dbMarker = filepath.Join(store.PersistentVolumeHostPath(disk, uuid, controlplane.VolumeDBName, models.VolumeScopePrivate), "keep.txt")
	logMarker = filepath.Join(store.PersistentVolumeHostPath(disk, uuid, controlplane.VolumeLogName, models.VolumeScopePrivate), "keep.txt")
	for _, marker := range []string{dbMarker, logMarker} {
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dbMarker, logMarker
}

func TestDeleteControlPlaneRemovesPrivateVolumeDirs(t *testing.T) {
	disk := openControlPlaneVolumeTest(t)
	uuid := "cp-delete-volumes"
	dbMarker, logMarker := writeControlPlaneVolumeMarkers(t, disk, uuid)

	dep := controlPlaneRestartTestDeployment(uuid)
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	eng := &lifecycleTestEngine{}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engineName:       "docker",
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}
	if err := pm.DeleteControlPlane(); err != nil {
		t.Fatalf("DeleteControlPlane: %v", err)
	}
	for _, marker := range []string{dbMarker, logMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("expected control-plane volume removed: %s err=%v", marker, err)
		}
	}
}

func TestRestartControlPlaneKeepsPrivateVolumeDirs(t *testing.T) {
	disk := openControlPlaneVolumeTest(t)
	uuid := "cp-restart-volumes"
	dbMarker, logMarker := writeControlPlaneVolumeMarkers(t, disk, uuid)

	dep := controlPlaneRestartTestDeployment(uuid)
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	eng := &controlPlaneRestartTrackEngine{}
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:3.8.0-beta.0",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: uuid,
		},
	}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger("test-process-manager"),
		engineName:       "docker",
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
	}
	if err := pm.RestartControlPlaneDeployment(dep, false); err != nil {
		t.Fatalf("RestartControlPlaneDeployment: %v", err)
	}
	for _, marker := range []string{dbMarker, logMarker} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("control-plane restart must keep volume data: %s: %v", marker, err)
		}
	}
}
