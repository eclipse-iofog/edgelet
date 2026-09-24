package processmanager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

func TestReconcile_AddWaitsWhileVolumeHeld(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-add-held")
	eng.workload = nil
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/var/lib/data", "rw", models.VolumeMappingTypeVolume),
	}
	presetRuntimeState(ms.MicroserviceUUID, models.MicroserviceStateCreated)

	var scanned []string
	setVolumeHolders(t, func(paths []string) ([]int, error) {
		scanned = append([]string(nil), paths...)
		return []int{4242}, nil
	})

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledAdd != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("create must not be enqueued while a host process holds the volume")
	}
	want := store.PersistentVolumeHostPath(pm.catalogDiskDirectory, ms.MicroserviceUUID, "data", models.VolumeScopePrivate)
	if !pathInList(scanned, want) {
		t.Fatalf("expected scan of %s, got %v", want, scanned)
	}
	assertVolumeWait(t, ms.MicroserviceUUID, models.MicroserviceStateCreated)
}

func TestReconcile_AddProceedsAfterVolumeReleased(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-add-release")
	eng.workload = nil
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/var/lib/data", "rw", models.VolumeMappingTypeVolume),
	}
	held := true
	setVolumeHolders(t, func(paths []string) ([]int, error) {
		if held {
			return []int{4242}, nil
		}
		return nil, nil
	})

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("create must wait while the volume is held")
	}

	held = false
	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledAdd != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionAdd {
		t.Fatalf("create must proceed after the holder exits, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestReconcile_RebuildWaitsWhileVolumeHeld(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-rebuild-held")
	eng.status = exitingStatus("cid-rebuild", "exitCode=1 oomKilled=false")

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("first crash must wait for backoff")
	}

	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/var/lib/data", "rw", models.VolumeMappingTypeVolume),
	}
	ms.Rebuild = true
	held := true
	setVolumeHolders(t, func([]string) ([]int, error) {
		if held {
			return []int{4242}, nil
		}
		return nil, nil
	})

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("rebuild must stay blocked while a host process holds the volume")
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got == nil || got.Status != models.MicroserviceStateExiting {
		t.Fatalf("runtime state must stay EXITING, got %#v", got)
	}
	assertVolumeWaitText(t, got)

	held = false
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionUpdate {
		t.Fatalf("rebuild must proceed without crash backoff after the holder exits, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestReconcile_SkipsVolumeHoldWithoutVolumeMapping(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-no-volume")
	eng.workload = nil
	setVolumeHolders(t, func([]string) ([]int, error) {
		t.Fatal("volume scan must be skipped when the microservice has no VOLUME mapping")
		return nil, nil
	})

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledAdd != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionAdd {
		t.Fatalf("create must proceed with no VOLUME mapping, stats=%+v tasks=%d", stats, len(tasks))
	}
	_ = ms
}

func TestReconcile_BindMountIsNotVolumeHold(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-bind-only")
	eng.workload = nil
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("/var/lib/edgelet/volumes/data/ms-bind-only/data", "/data", "rw", models.VolumeMappingTypeBind),
	}
	setVolumeHolders(t, func([]string) ([]int, error) {
		t.Fatal("BIND mounts must not be treated as a persistent volume hold")
		return nil, nil
	})

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledAdd != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionAdd {
		t.Fatalf("BIND-only create must proceed, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestPersistentVolumeHostPaths_PrivateAndShared(t *testing.T) {
	ms := models.NewMicroservice("uuid-1", "nginx:latest")
	shared := models.NewVolumeMapping("cache", "/cache", "rw", models.VolumeMappingTypeVolume)
	shared.Scope = models.VolumeScopeShared
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
		shared,
		models.NewVolumeMapping("/host/path", "/mnt", "rw", models.VolumeMappingTypeBind),
		models.NewVolumeMapping("secret", "/secret", "ro", models.VolumeMappingTypeVolumeMount),
	}
	got := persistentVolumeHostPaths("/var/lib/edgelet", ms)
	if len(got) != 2 {
		t.Fatalf("expected private and shared paths, got %v", got)
	}
	if got[0] != filepath.Join("/var/lib/edgelet", "volumes", "data", "uuid-1", "data") {
		t.Fatalf("private path %q", got[0])
	}
	if got[1] != filepath.Join("/var/lib/edgelet", "volumes", "shared", "cache") {
		t.Fatalf("shared path %q", got[1])
	}
}

func TestCreateContainer_RefusesWhileVolumeHeld(t *testing.T) {
	eng := &lifecycleTestEngine{}
	cm := newLifecycleCM(eng, &models.Registry{})
	cm.catalogDiskDirectory = t.TempDir()
	ms := models.NewMicroservice("ms-create-held", "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	setVolumeHolders(t, func([]string) ([]int, error) {
		return []int{99}, nil
	})

	err := cm.createContainerWithPull(context.Background(), ms, false)
	if !errors.Is(err, errVolumeInUse) {
		t.Fatalf("expected volume wait, got %v", err)
	}
	if eng.createdID != "" {
		t.Fatal("container must not be created while the volume is held")
	}
}

func TestLaunchLocalMicroservice_RefusesWhileVolumeHeld(t *testing.T) {
	eng := &lifecycleTestEngine{}
	pm := &ProcessManager{
		engine:               eng,
		engineName:           "edgelet",
		ctx:                  context.Background(),
		catalogDiskDirectory: t.TempDir(),
		logger:               logging.NewModuleLogger(ProcessManagerModuleName),
	}
	ms := models.NewMicroservice("ms-local-held", "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	setVolumeHolders(t, func([]string) ([]int, error) {
		return []int{77}, nil
	})

	_, err := pm.LaunchLocalMicroserviceWithProgress(ms, models.NewRegistry(2, "from_cache", true, "", "", ""), "127.0.0.1", nil)
	if !errors.Is(err, errVolumeInUse) {
		t.Fatalf("expected volume wait, got %v", err)
	}
	if eng.createdID != "" {
		t.Fatal("local create must not run while the volume is held")
	}
}

func newVolumeHoldPM(t *testing.T, uuid string) (*ProcessManager, *models.Microservice, *lifecycleTestEngine) {
	t.Helper()
	pm, ms, eng := newBackoffReconcilePM(t, uuid)
	pm.catalogDiskDirectory = t.TempDir()
	if pm.containerManager != nil {
		pm.containerManager.catalogDiskDirectory = pm.catalogDiskDirectory
	}
	return pm, ms, eng
}

func setVolumeHolders(t *testing.T, fn func([]string) ([]int, error)) {
	t.Helper()
	prev := listVolumePathHolders
	listVolumePathHolders = fn
	t.Cleanup(func() { listVolumePathHolders = prev })
}

func presetRuntimeState(uuid string, state models.MicroserviceState) {
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroservicesStatus(uuid, models.NewMicroserviceStatusWithState(state))
	})
}

func assertVolumeWait(t *testing.T, uuid string, state models.MicroserviceState) {
	t.Helper()
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(uuid)
	if got == nil {
		t.Fatal("missing status")
	}
	if got.Status != state {
		t.Fatalf("runtime state = %s, want %s", got.Status, state)
	}
	assertVolumeWaitText(t, got)
}

func assertVolumeWaitText(t *testing.T, got *models.MicroserviceStatus) {
	t.Helper()
	msg := ""
	if got.ErrorMessage != nil {
		msg = *got.ErrorMessage
	}
	if !volumeWaitText(msg) || !volumeWaitText(got.LastError) {
		t.Fatalf("status text = %q lastError = %q", msg, got.LastError)
	}
}

func volumeWaitText(msg string) bool {
	return strings.Contains(msg, "volume in use") && strings.Contains(msg, "leftover process")
}

func pathInList(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
