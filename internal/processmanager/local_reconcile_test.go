package processmanager

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestLocalDeploymentLaunchInFlight(t *testing.T) {
	nowSec := time.Now().Unix()
	inFlight := &models.LocalDeployedMicroservice{
		LocalUUID:          "local-apply",
		RuntimeState:       "starting",
		State:              "starting",
		Generation:         2,
		ObservedGeneration: 0,
		LastTransitionAt:   nowSec,
	}
	if !localDeploymentLaunchInFlight(inFlight, nowSec) {
		t.Fatal("expected apply-owned starting deployment to be in-flight")
	}

	observed := *inFlight
	observed.ObservedGeneration = 2
	if localDeploymentLaunchInFlight(&observed, nowSec) {
		t.Fatal("expected observed generation to clear in-flight state")
	}

	stale := *inFlight
	stale.LastStartAttemptAt = nowSec - int64(localLaunchInFlightStaleTimeout.Seconds()) - 1
	if localDeploymentLaunchInFlight(&stale, nowSec) {
		t.Fatal("expected stale starting deployment to allow reconcile retry")
	}
}

func TestReconcileLocalDesiredRunning_SkipsLaunchWhenApplyInFlight(t *testing.T) {
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	nowSec := time.Now().Unix()
	item := &models.LocalDeployedMicroservice{
		LocalUUID:          "local-apply",
		RuntimeState:       "starting",
		State:              "starting",
		DesiredState:       "running",
		Generation:         1,
		ObservedGeneration: 0,
		LastTransitionAt:   nowSec,
	}
	launchCalled := false
	pm.launchLocalDeploymentFn = func(*models.LocalDeployedMicroservice, int64) {
		launchCalled = true
	}

	pm.reconcileLocalDesiredRunning(item, nil, nowSec)

	if launchCalled {
		t.Fatal("expected reconcile to skip launch while CLI apply is in-flight")
	}
}

func TestReconcileLocalDesiredRunning_LaunchesWhenNotInFlight(t *testing.T) {
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	nowSec := time.Now().Unix()
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-retry",
		RuntimeState: "failed",
		State:        "failed",
		DesiredState: "running",
		Generation:   1,
	}
	launchCalled := false
	pm.launchLocalDeploymentFn = func(*models.LocalDeployedMicroservice, int64) {
		launchCalled = true
	}

	pm.reconcileLocalDesiredRunning(item, nil, nowSec)

	if !launchCalled {
		t.Fatal("expected reconcile to launch when deployment is not in-flight")
	}
}

type serialLocalLaunchEngine struct {
	lifecycleTestEngine
	inCreate      int32
	maxConcurrent int32
}

func (e *serialLocalLaunchEngine) CreateContainer(ms *models.Microservice, _ string) (string, error) {
	current := atomic.AddInt32(&e.inCreate, 1)
	for {
		prev := atomic.LoadInt32(&e.maxConcurrent)
		if current <= prev || atomic.CompareAndSwapInt32(&e.maxConcurrent, prev, current) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	atomic.AddInt32(&e.inCreate, -1)
	return "cid-" + ms.MicroserviceUUID, nil
}

func TestLaunchLocalMicroserviceWithProgress_SerializesConcurrentLaunchSameUUID(t *testing.T) {
	eng := &serialLocalLaunchEngine{}
	pm := &ProcessManager{
		logger: logging.NewModuleLogger("test-process-manager"),
		engine: eng,
	}
	ms := &models.Microservice{
		MicroserviceUUID: "12939b01-550f-48be-b8ac-2bade1eb19aa",
		ImageName:        "busybox:latest",
	}
	reg := models.NewRegistry(2, "from_cache", true, "", "", "")

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = pm.LaunchLocalMicroserviceWithProgress(ms, reg, "127.0.0.1", nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d launch failed: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&eng.maxConcurrent); got != 1 {
		t.Fatalf("expected max concurrent create=1 for same UUID, got %d", got)
	}
}

func TestBumpLocalFailureMarksStuckAfterThreshold(t *testing.T) {
	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-1",
		RuntimeState: "failed",
		State:        "failed",
		FailureCount: localReconcileMaxFailures - 1,
		RestartCount: 2,
	}

	pm.bumpLocalFailure(item, nil, "failed")

	if item.FailureCount != localReconcileMaxFailures {
		t.Fatalf("expected failure_count=%d, got %d", localReconcileMaxFailures, item.FailureCount)
	}
	if item.RuntimeState != "stuck_in_restart" {
		t.Fatalf("expected runtime_state=stuck_in_restart, got %q", item.RuntimeState)
	}
	if item.State != "stuck_in_restart" {
		t.Fatalf("expected state=stuck_in_restart, got %q", item.State)
	}
	if item.RestartCount != 3 {
		t.Fatalf("expected restart_count increment, got %d", item.RestartCount)
	}
}

func TestReconcileLocalDesiredRunning_CreatedNonRestartableRecreates(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-1",
		ManifestYAML: minimalLocalManifestYAML(),
		Generation:   1,
		FailureCount: 2,
		RuntimeState: "created",
		State:        "created",
		ContainerID:  "old-container",
		DesiredState: "running",
	}
	container := &engine.Container{ID: "old-container"}
	recreateCalled := false
	pm.startMicroserviceFn = func(_ string) error {
		return &engine.NonRestartableContainerError{
			ContainerID: "old-container",
			Reason:      "CONTAINER_EXITED",
			ExitCode:    255,
			Message:     "container is in CONTAINER_EXITED state",
		}
	}
	pm.recreateLocalDeploymentFn = func(target *models.LocalDeployedMicroservice, pullImage bool, _ int64) error {
		recreateCalled = true
		if pullImage {
			t.Fatal("expected pullImage=false for created non-restartable recreate")
		}
		target.ContainerID = "new-container"
		target.RuntimeState = "running"
		target.State = "running"
		target.LastError = ""
		target.FailureCount = 0
		return nil
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateCreated}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 123)

	if !recreateCalled {
		t.Fatal("expected recreate to be called for non-restartable created container")
	}
	if item.RuntimeState != "running" || item.State != "running" {
		t.Fatalf("expected running state after recreate, got runtime=%q state=%q", item.RuntimeState, item.State)
	}
	if item.FailureCount != 0 {
		t.Fatalf("expected failure count reset after recreate, got %d", item.FailureCount)
	}
	if item.LastError != "" {
		t.Fatalf("expected last error cleared after recreate, got %q", item.LastError)
	}
}

func TestReconcileLocalDesiredRunning_CreatedTransientStartErrorBumpsFailure(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-2",
		Generation:   1,
		RuntimeState: "created",
		State:        "created",
		DesiredState: "running",
	}
	container := &engine.Container{ID: "container-2"}
	pm.startMicroserviceFn = func(_ string) error { return errors.New("temporary start error") }
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateCreated}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 456)

	if item.FailureCount != 1 {
		t.Fatalf("expected failure count incremented to 1, got %d", item.FailureCount)
	}
	if item.RuntimeState != "created" || item.State != "created" {
		t.Fatalf("expected created state retained for transient error, got runtime=%q state=%q", item.RuntimeState, item.State)
	}
}

func TestReconcileLocalDesiredRunning_CreatedNonRestartableRecreateLaunchFailureTracksFailure(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-3",
		Generation:   1,
		RuntimeState: "created",
		State:        "created",
		DesiredState: "running",
	}
	container := &engine.Container{ID: "container-3"}
	pm.startMicroserviceFn = func(_ string) error {
		return &engine.NonRestartableContainerError{ContainerID: "container-3", Reason: "CONTAINER_EXITED", ExitCode: 255, Message: "terminal"}
	}
	pm.removeContainerByIDFn = func(_ string) error { return nil }
	pm.recreateLocalDeploymentFn = func(target *models.LocalDeployedMicroservice, _ bool, _ int64) error {
		pm.bumpLocalFailure(target, errors.New("recreate launch failed"), "failed")
		return errors.New("recreate launch failed")
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateCreated}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 789)

	if item.FailureCount != 1 {
		t.Fatalf("expected failure count incremented after recreate launch failure, got %d", item.FailureCount)
	}
	if item.LastError == "" {
		t.Fatal("expected last error set on recreate launch failure")
	}
}

func TestReconcileLocalDesiredRunning_ExitingDockerInPlaceStart(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	pm.engineName = "docker"
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-docker-exiting",
		ManifestYAML: minimalLocalManifestYAML(),
		Generation:   1,
		RuntimeState: "exiting",
		State:        "exiting",
		ContainerID:  "old-container",
		DesiredState: "running",
	}
	container := &engine.Container{ID: "old-container"}
	startCalled := false
	pm.startMicroserviceFn = func(uuid string) error {
		startCalled = true
		if uuid != item.LocalUUID {
			t.Fatalf("expected start for %q, got %q", item.LocalUUID, uuid)
		}
		return nil
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateExiting}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 999)

	if !startCalled {
		t.Fatal("expected in-place start for docker exiting container")
	}
	if item.RuntimeState != "running" || item.State != "running" {
		t.Fatalf("expected running state after start, got runtime=%q state=%q", item.RuntimeState, item.State)
	}
	if item.FailureCount != 0 {
		t.Fatalf("expected failure count reset after start, got %d", item.FailureCount)
	}
}

func TestReconcileLocalDesiredRunning_ExitingDockerStartFailureBumpsFailure(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	pm.engineName = "docker"
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-docker-exiting-fail",
		ManifestYAML: minimalLocalManifestYAML(),
		Generation:   1,
		RuntimeState: "exiting",
		State:        "exiting",
		ContainerID:  "old-container",
		DesiredState: "running",
		FailureCount: 0,
	}
	container := &engine.Container{ID: "old-container"}
	pm.startMicroserviceFn = func(_ string) error {
		return errors.New("docker start failed")
	}
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateExiting}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 999)

	if item.FailureCount != 1 {
		t.Fatalf("expected failure count incremented to 1, got %d", item.FailureCount)
	}
	if item.RuntimeState != "exiting" {
		t.Fatalf("expected runtime_state=exiting, got %q", item.RuntimeState)
	}
}

func TestReconcileLocalDesiredRunning_ExitingDockerStartResetsPriorFailureCount(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	pm.engineName = "docker"
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-docker-exiting-recover",
		ManifestYAML: minimalLocalManifestYAML(),
		Generation:   1,
		RuntimeState: "exiting",
		State:        "exiting",
		ContainerID:  "old-container",
		DesiredState: "running",
		FailureCount: 3,
	}
	container := &engine.Container{ID: "old-container"}
	pm.startMicroserviceFn = func(_ string) error { return nil }
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{Status: models.MicroserviceStateExiting}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 999)

	if item.FailureCount != 0 {
		t.Fatalf("expected failure count reset after successful start, got %d", item.FailureCount)
	}
	if item.RuntimeState != "running" {
		t.Fatalf("expected runtime_state=running, got %q", item.RuntimeState)
	}
}

func TestReconcileLocalDesiredRunning_ExitingNonRestartableRecreates(t *testing.T) {
	openLocalReconcileTestDB(t)

	pm := &ProcessManager{}
	pm.logger = logging.NewModuleLogger("test-process-manager")
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-exiting",
		ManifestYAML: minimalLocalManifestYAML(),
		Generation:   1,
		RuntimeState: "exiting",
		State:        "exiting",
		ContainerID:  "old-container",
		DesiredState: "running",
	}
	container := &engine.Container{ID: "old-container"}
	recreateCalled := false
	pm.recreateLocalDeploymentFn = func(target *models.LocalDeployedMicroservice, pullImage bool, _ int64) error {
		recreateCalled = true
		if pullImage {
			t.Fatal("expected pullImage=false for exiting non-restartable recreate")
		}
		target.ContainerID = "new-container"
		target.RuntimeState = "running"
		target.State = "running"
		target.LastError = ""
		target.FailureCount = 0
		return nil
	}
	criMsg := "CRI reason=CONTAINER_EXITED exitCode=143 message=container is in CONTAINER_EXITED state"
	pm.getContainerStatusFn = func(_, _ string) (*models.MicroserviceStatus, error) {
		return &models.MicroserviceStatus{
			Status:       models.MicroserviceStateExiting,
			ErrorMessage: &criMsg,
		}, nil
	}

	pm.reconcileLocalDesiredRunning(item, container, 999)

	if !recreateCalled {
		t.Fatal("expected recreate for exiting CONTAINER_EXITED state")
	}
	if item.RuntimeState != "running" || item.State != "running" {
		t.Fatalf("expected running state after recreate, got runtime=%q state=%q", item.RuntimeState, item.State)
	}
	if item.FailureCount != 0 {
		t.Fatalf("expected failure count reset after recreate, got %d", item.FailureCount)
	}
}

func TestReconcileLocalDesiredDeleted_DeletesRowWhenContainerGone(t *testing.T) {
	openLocalReconcileTestDB(t)
	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-gc",
		ApplicationName:  "edgelet",
		MicroserviceName: "gc-ms",
		ManifestYAML:     minimalLocalManifestYAML(),
		DesiredState:     "deleted",
		RuntimeState:     "deleting",
		ImageName:        "busybox:latest",
	}
	if err := store.GetInstance().UpsertLocalWorkload(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	pm.reconcileLocalDesiredDeleted(item, nil, time.Now().Unix())
	got, err := store.GetInstance().GetLocalWorkload("local-gc")
	if err == nil && got != nil {
		t.Fatalf("expected local row deleted, got %+v", got)
	}
}

func TestReconcileLocalDesiredDeleted_DoesNotReinsertMissingRow(t *testing.T) {
	openLocalReconcileTestDB(t)
	stale := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-missing",
		ApplicationName:  "edgelet",
		MicroserviceName: "missing",
		ManifestYAML:     minimalLocalManifestYAML(),
		DesiredState:     "deleted",
		RuntimeState:     "deleted",
	}
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	pm.reconcileLocalDesiredDeleted(stale, nil, time.Now().Unix())
	got, err := store.GetInstance().GetLocalWorkload("local-missing")
	if err == nil && got != nil {
		t.Fatal("expected stale deleted reconcile not to insert a tombstone")
	}
}

func TestReconcileOneLocalDeployment_StaleRunningSnapshotAfterDeleteIsNoop(t *testing.T) {
	openLocalReconcileTestDB(t)
	stale := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-stale",
		ApplicationName:  "edgelet",
		MicroserviceName: "stale",
		ManifestYAML:     minimalLocalManifestYAML(),
		DesiredState:     "running",
		RuntimeState:     "running",
	}
	launchCalled := false
	pm := &ProcessManager{logger: logging.NewModuleLogger("test-process-manager")}
	pm.launchLocalDeploymentFn = func(*models.LocalDeployedMicroservice, int64) {
		launchCalled = true
	}
	pm.reconcileOneLocalDeployment(stale)
	if launchCalled {
		t.Fatal("expected missing row not to launch from stale running snapshot")
	}
	got, err := store.GetInstance().GetLocalWorkload("local-stale")
	if err == nil && got != nil {
		t.Fatal("expected stale running snapshot not to upsert a row")
	}
}

func openLocalReconcileTestDB(t *testing.T) {
	t.Helper()
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
}

func minimalLocalManifestYAML() string {
	return `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: local-ms
spec:
  image: busybox:latest
  container:
    hostNetworkMode: false
    isPrivileged: false
`
}
