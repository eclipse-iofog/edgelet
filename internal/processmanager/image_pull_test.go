package processmanager

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestUpdatePullMissKeepsRunningContainer(t *testing.T) {
	clk := withRestartClock(t)
	pm, ms, eng := newPullMissPM(t, "ms-pull-running")

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	runOneTask(t, pm)

	if countOp(eng.ops, "pull") != 1 || countOp(eng.ops, "remove") != 0 || countOp(eng.ops, "create") != 0 {
		t.Fatalf("ops=%v", eng.ops)
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.Status != models.MicroserviceStateRunning {
		t.Fatalf("status=%s", got.Status)
	}
	if got.ImagePullStatus != models.ImagePullStatusFailed {
		t.Fatalf("imagePullStatus=%q", got.ImagePullStatus)
	}
	if got.ErrorMessage == nil || !strings.Contains(*got.ErrorMessage, "denied") {
		t.Fatalf("errorMessage=%#v", got.ErrorMessage)
	}
	if got.RestartCount != 0 {
		t.Fatalf("restartCount=%d", got.RestartCount)
	}
	if ms.GetIsUpdating() {
		t.Fatal("pull miss left the update in flight")
	}
	_ = clk
}

func TestCreatePullMissStaysQueued(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newPullMissPM(t, "ms-pull-queued")
	eng.workload = nil

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	runOneTask(t, pm)

	if countOp(eng.ops, "create") != 0 || countOp(eng.ops, "remove") != 0 {
		t.Fatalf("ops=%v", eng.ops)
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.Status != models.MicroserviceStateQueued {
		t.Fatalf("status=%s", got.Status)
	}
	if got.Status == models.MicroserviceStateFailed {
		t.Fatal("create pull miss must not be FAILED")
	}
	if got.ImagePullStatus != models.ImagePullStatusFailed {
		t.Fatalf("imagePullStatus=%q", got.ImagePullStatus)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage == "" {
		t.Fatal("expected registry error")
	}
	if got.RestartCount != 0 {
		t.Fatalf("restartCount=%d", got.RestartCount)
	}
}

func TestPullMissDelayBlocksThenQueuesOneUpdate(t *testing.T) {
	clk := withRestartClock(t)
	pm, ms, eng := newPullMissPM(t, "ms-pull-delay")

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	runOneTask(t, pm)
	ms.SetIsUpdating(false)

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("second reconcile must not queue while the pull delay is active")
	}

	clk.Advance(10 * time.Second)
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionUpdate {
		t.Fatalf("expected one update after the delay, stats=%+v tasks=%d", stats, len(tasks))
	}
	if countOp(eng.ops, "remove") != 0 {
		t.Fatalf("container removed, ops=%v", eng.ops)
	}
}

func TestRebuildSkipsPullDelayOnce(t *testing.T) {
	clk := withRestartClock(t)
	pm, ms, _ := newPullMissPM(t, "ms-pull-rebuild")

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	runOneTask(t, pm)
	ms.SetIsUpdating(false)

	ms.Rebuild = true
	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 1 {
		t.Fatalf("rebuild must queue immediately, stats=%+v", stats)
	}
	runOneTask(t, pm)
	ms.SetIsUpdating(false)

	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("rebuild must wait while the flag stays set")
	}

	clk.Advance(20 * time.Second)
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 {
		t.Fatalf("expected one update after the rebuild wait, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestImageChangeTriesPullImmediately(t *testing.T) {
	withRestartClock(t)
	pm, ms, _ := newPullMissPM(t, "ms-pull-new-image")

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	runOneTask(t, pm)
	ms.SetIsUpdating(false)

	ms.ImageName = "nginx:other"
	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 {
		t.Fatalf("a new image must try immediately, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestOldImageRunningKeepsPullErrorAndDelay(t *testing.T) {
	clk := withRestartClock(t)
	const uuid = "ms-old-running"
	msg := "unable to pull nginx:new: denied"
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetImagePullMiss(uuid, models.MicroserviceStateRunning, msg)
	})
	var delay time.Duration
	for delay <= errorClearGrace {
		delay = GetRestartStuckChecker().ArmPullMiss(uuid, "nginx:new")
	}

	running := newRunningStatus("cid-old")
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		syncMicroserviceStatusToReporter(s, uuid, running)
	})
	clk.Advance(errorClearGrace)
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		syncMicroserviceStatusToReporter(s, uuid, newRunningStatus("cid-old"))
	})
	maybeResetRestartBackoff(uuid, running)
	AdvanceRunningErrorClear()

	got := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus(uuid)
	if got == nil || got.ErrorMessage == nil || *got.ErrorMessage != msg {
		t.Fatalf("errorMessage=%#v", got)
	}
	if got.ImagePullStatus != models.ImagePullStatusFailed {
		t.Fatalf("imagePullStatus=%q", got.ImagePullStatus)
	}
	if GetRestartStuckChecker().RemainingPullDelay(uuid) <= 0 {
		t.Fatal("pull delay was cleared while the old image was still running")
	}
}

func TestDesiredImageClearsPullFailure(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newPullMissPM(t, "ms-pull-match")
	eng.configDrifted = false
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetImagePullMiss(ms.MicroserviceUUID, models.MicroserviceStateRunning, "unable to pull nginx:new: denied")
	})
	GetRestartStuckChecker().ArmPullMiss(ms.MicroserviceUUID, ms.ImageName)

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.ImagePullStatus != "" {
		t.Fatalf("imagePullStatus=%q", got.ImagePullStatus)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != "" {
		t.Fatalf("errorMessage=%#v", got.ErrorMessage)
	}
	if GetRestartStuckChecker().HasPullTracking(ms.MicroserviceUUID) {
		t.Fatal("pull delay remained after the desired image was running")
	}
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("matching spec must not queue another pull")
	}
}

func TestPullMissesDoNotReachFailedCap(t *testing.T) {
	withRestartClock(t)
	pm, ms, _ := newPullMissPM(t, "ms-pull-cap")
	task := NewContainerTask(TaskActionUpdate, ms.MicroserviceUUID)
	for i := 0; i < 6; i++ {
		pm.finishTask(task, newImagePullMiss("nginx:new", errors.New("denied")))
	}
	if task.Retries != 0 {
		t.Fatalf("pull miss incremented retries to %d", task.Retries)
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.Status == models.MicroserviceStateFailed {
		t.Fatal("pull misses reached FAILED")
	}
	if got.RestartCount != 0 {
		t.Fatalf("restartCount=%d", got.RestartCount)
	}
	if got.ImagePullStatus != models.ImagePullStatusFailed {
		t.Fatalf("imagePullStatus=%q", got.ImagePullStatus)
	}
	if GetRestartStuckChecker().IsStuck(ms.MicroserviceUUID) {
		t.Fatal("pull misses marked the workload stuck")
	}
	if GetRestartStuckChecker().ConsecutiveFailures(ms.MicroserviceUUID) != 0 {
		t.Fatal("pull misses incremented the crash counter")
	}

	other := NewContainerTask(TaskActionUpdate, "ms-create-cap")
	other.Retries = maxTaskRetries
	pm.microserviceManager = &invariantMicroserviceManager{microservice: models.NewMicroservice("ms-create-cap", "nginx:latest")}
	pm.finishTask(other, errors.New("create failed"))
	failed := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("ms-create-cap")
	if failed.Status != models.MicroserviceStateFailed {
		t.Fatalf("create failure status=%s", failed.Status)
	}
}

func TestLocalPullMissDoesNotCountTowardFailureCap(t *testing.T) {
	withRestartClock(t)
	openLocalReconcileTestDB(t)
	eng := &lifecycleTestEngine{missingImage: true, pullErr: errors.New("denied")}
	pm := &ProcessManager{
		logger: logging.NewModuleLogger(ProcessManagerModuleName),
		engine: eng,
		ctx:    t.Context(),
	}
	item := &models.LocalDeployedMicroservice{
		LocalUUID:    "local-pull-miss",
		ManifestYAML: minimalLocalManifestYAML(),
		DesiredState: "running",
		Generation:   1,
		RestartCount: 2,
	}
	if err := store.GetInstance().UpsertLocalWorkload(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	nowSec := time.Now().Unix()
	pm.reconcileLocalDesiredRunning(item, nil, nowSec)
	if item.FailureCount != 0 {
		t.Fatalf("failureCount=%d", item.FailureCount)
	}
	if item.RestartCount != 2 {
		t.Fatalf("restartCount=%d", item.RestartCount)
	}
	if item.RuntimeState != "queued" {
		t.Fatalf("runtime=%q", item.RuntimeState)
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(item.LocalUUID)
	if got.ImagePullStatus != models.ImagePullStatusFailed || got.Status != models.MicroserviceStateQueued {
		t.Fatalf("status=%s imagePullStatus=%q", got.Status, got.ImagePullStatus)
	}
	if countOp(eng.ops, "create") != 0 {
		t.Fatalf("ops=%v", eng.ops)
	}
	started := item.LastStartAttemptAt

	pm.reconcileLocalDesiredRunning(item, nil, nowSec+30)
	if item.FailureCount != 0 || item.LastStartAttemptAt != started {
		t.Fatalf("delay did not hold the retry, failure=%d started=%d last=%d", item.FailureCount, started, item.LastStartAttemptAt)
	}

	createEng := &lifecycleTestEngine{createErr: errors.New("create failed")}
	pm.engine = createEng
	createItem := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-create-fail",
		ApplicationName:  "local-app",
		MicroserviceName: "local-create-fail",
		ManifestYAML:     minimalLocalManifestYAML(),
		DesiredState:     "running",
		Generation:       1,
	}
	if err := store.GetInstance().UpsertLocalWorkload(createItem); err != nil {
		t.Fatalf("upsert create: %v", err)
	}
	pm.reconcileLocalDesiredRunning(createItem, nil, time.Now().Unix())
	if createItem.FailureCount != 1 || createItem.RuntimeState != "failed" {
		t.Fatalf("create failure count=%d runtime=%q", createItem.FailureCount, createItem.RuntimeState)
	}
}

func TestControlPlanePullMissDoesNotRemove(t *testing.T) {
	withRestartClock(t)
	openLocalReconcileTestDB(t)
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &controlPlaneCaptureEngine{}
	eng.missingImage = true
	eng.pullErr = errors.New("denied")
	eng.workload = &engine.Container{
		ID:    "old-cid",
		Image: "ghcr.io/datasance/controller:old",
		Labels: map[string]string{
			workloadmeta.LabelMicroserviceUID: "cp-pull-miss",
		},
	}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger(ProcessManagerModuleName),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
		ctx:              t.Context(),
	}
	dep := &models.ControlPlaneDeployment{
		ControllerUUID:     "cp-pull-miss",
		Namespace:          "default",
		Name:               "pot",
		ManifestYAML:       minimalControlPlaneManifestYAML(),
		DesiredState:       "running",
		Generation:         2,
		ObservedGeneration: 1,
		RuntimeState:       "running",
		ContainerID:        "old-cid",
		RestartCount:       2,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	for i := 0; i < 6; i++ {
		pm.reconcileControlPlane()
	}
	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.ContainerID != "old-cid" {
		t.Fatalf("container id %q", got.ContainerID)
	}
	if got.FailureCount != 0 || got.RestartCount != 2 {
		t.Fatalf("failure=%d restart=%d", got.FailureCount, got.RestartCount)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("runtime=%q", got.RuntimeState)
	}
	if got.ObservedGeneration != 1 {
		t.Fatalf("observed generation %d", got.ObservedGeneration)
	}
	if countOp(eng.ops, "remove") != 0 || eng.lastMS != nil {
		t.Fatalf("controller was replaced, ops=%v last=%v", eng.ops, eng.lastMS)
	}
	status := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("cp-pull-miss")
	if status.Status != models.MicroserviceStateRunning || status.ImagePullStatus != models.ImagePullStatusFailed {
		t.Fatalf("status=%s imagePullStatus=%q", status.Status, status.ImagePullStatus)
	}
	if status.ErrorMessage == nil || *status.ErrorMessage == "" {
		t.Fatal("expected pull error")
	}
}

func TestControlPlaneCreateFailureStillReachesCap(t *testing.T) {
	openLocalReconcileTestDB(t)
	eng := &lifecycleTestEngine{createErr: errors.New("create failed")}
	pm := &ProcessManager{
		logger:           logging.NewModuleLogger(ProcessManagerModuleName),
		engine:           eng,
		containerManager: NewContainerManager(eng, nil, "docker"),
		ctx:              t.Context(),
	}
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-create-fail",
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   minimalControlPlaneManifestYAML(),
		DesiredState:   "running",
		Generation:     1,
		RuntimeState:   "queued",
		FailureCount:   localReconcileMaxFailures - 1,
	}
	if err := store.GetInstance().UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pm.reconcileControlPlane()
	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get control plane: found=%v err=%v", found, err)
	}
	if got.FailureCount != localReconcileMaxFailures || got.RuntimeState != "stuck_in_restart" {
		t.Fatalf("failure=%d runtime=%q", got.FailureCount, got.RuntimeState)
	}
}

func newPullMissPM(t *testing.T, uuid string) (*ProcessManager, *models.Microservice, *lifecycleTestEngine) {
	t.Helper()
	openLocalReconcileTestDB(t)
	reg := models.NewRegistry(1, "https://registry.example", false, "", "", "")
	if err := store.GetInstance().UpsertLocalRegistry(reg); err != nil {
		t.Fatalf("upsert registry: %v", err)
	}
	pm, ms, eng := newBackoffReconcilePM(t, uuid)
	eng.configDrifted = true
	eng.pullErr = errors.New("denied")
	eng.missingImage = true
	eng.status = newRunningStatus("cid-" + uuid)
	pm.containerManager = newLifecycleCM(eng, reg)
	ms.RegistryID = 1
	ms.ImageName = "nginx:new"
	if eng.workload != nil {
		eng.workload.Image = "nginx:old"
	}
	return pm, ms, eng
}

func runOneTask(t *testing.T, pm *ProcessManager) {
	t.Helper()
	tasks := drainTasks(pm.taskQueue)
	if len(tasks) != 1 {
		t.Fatalf("tasks=%d", len(tasks))
	}
	err := pm.executeTask(tasks[0])
	pm.finishTask(tasks[0], err)
}
