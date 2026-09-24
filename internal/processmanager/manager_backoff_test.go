package processmanager

import (
	"context"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestHandleLatestMicroservices_ExitingTicksDoNotMarkStuck(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newBackoffReconcilePM(t, "ms-exit-ticks")
	eng.status = exitingStatus("cid-exit", "exitCode=1 oomKilled=false")

	for i := 0; i < AbnormalNumberOfRestarts; i++ {
		pm.handleLatestMicroservices(&reconcileCycleStats{})
		ms.SetIsUpdating(false)
		if ms.IsStuckInRestart {
			t.Fatalf("tick %d marked stuck", i+1)
		}
	}
	if GetRestartStuckChecker().IsStuck(ms.MicroserviceUUID) {
		t.Fatal("sitting EXITING ticks must not mark stuck")
	}
	if tasks := drainTasks(pm.taskQueue); len(tasks) != 0 {
		t.Fatalf("expected no recreate while backing off, got %d", len(tasks))
	}
}

func TestHandleLatestMicroservices_CrashRecreateDelaysThenDoubles(t *testing.T) {
	clk := withRestartClock(t)
	pm, ms, eng := newBackoffReconcilePM(t, "ms-delay")
	eng.status = exitingStatus("cid-1", "exitCode=1 oomKilled=false")

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("first crash must wait 10s")
	}
	if GetRestartStuckChecker().ConsecutiveFailures(ms.MicroserviceUUID) != 1 {
		t.Fatalf("n=%d", GetRestartStuckChecker().ConsecutiveFailures(ms.MicroserviceUUID))
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.Status != models.MicroserviceStateExiting {
		t.Fatalf("expected EXITING while waiting, got %s", got.Status)
	}
	if got.RestartCount != 1 {
		t.Fatalf("restartCount=%d", got.RestartCount)
	}

	clk.Advance(10 * time.Second)
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionUpdate {
		t.Fatalf("expected UPDATE after 10s, stats=%+v tasks=%d", stats, len(tasks))
	}
	ms.SetIsUpdating(false)

	eng.status = newRunningStatus("cid-1")
	pm.handleLatestMicroservices(&reconcileCycleStats{})
	ms.SetIsUpdating(false)
	_ = drainTasks(pm.taskQueue)

	eng.status = exitingStatus("cid-2", "exitCode=1 oomKilled=false")
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("second crash must wait 20s")
	}
	if GetRestartStuckChecker().ConsecutiveFailures(ms.MicroserviceUUID) != 2 {
		t.Fatalf("n=%d", GetRestartStuckChecker().ConsecutiveFailures(ms.MicroserviceUUID))
	}

	clk.Advance(20 * time.Second)
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks = drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 {
		t.Fatalf("expected UPDATE after 20s, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestHandleLatestMicroservices_RebuildSkipsDelay(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newBackoffReconcilePM(t, "ms-rebuild")
	eng.status = exitingStatus("cid-rebuild", "exitCode=1 oomKilled=false")
	pm.handleLatestMicroservices(&reconcileCycleStats{})
	_ = drainTasks(pm.taskQueue)
	ms.SetIsUpdating(false)

	ms.Rebuild = true
	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 {
		t.Fatalf("rebuild must recreate immediately, stats=%+v tasks=%d", stats, len(tasks))
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if got.RestartCount != 0 {
		t.Fatalf("rebuild must reset restartCount, got %d", got.RestartCount)
	}
}

func TestHandleLatestMicroservices_CatalogReadySkipsDelay(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newBackoffReconcilePM(t, "ms-catalog")
	errMsg := "exitCode=1 oomKilled=false"
	prev := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	prev.ErrorMessage = &errMsg
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroservicesStatus(ms.MicroserviceUUID, prev)
	})
	GetRestartStuckChecker().RecordIfNew(ms.MicroserviceUUID)
	GetRestartStuckChecker().MarkCatalogWaiting(ms.MicroserviceUUID)
	eng.workload = nil

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledAdd != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionAdd {
		t.Fatalf("catalog Ready must start immediately, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func TestHandleLatestMicroservices_FirstNonRestartableSkipsDelay(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newBackoffReconcilePM(t, "ms-cri")
	msg := "CRI reason=CONTAINER_EXITED exitCode=255 message=container is in CONTAINER_EXITED state"
	eng.status = models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	eng.status.ErrorMessage = &msg
	eng.status.ContainerID = "cid-cri"

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledUpdate != 1 || len(tasks) != 1 {
		t.Fatalf("first non-restartable recreate must run immediately, stats=%+v tasks=%d", stats, len(tasks))
	}
	ms.SetIsUpdating(false)

	GetRestartStuckChecker().ObserveRunning(ms.MicroserviceUUID)
	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledUpdate != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("the next crash after a non-restartable recreate must wait")
	}
}

func TestRetryTask_NotImmediate(t *testing.T) {
	clk := withRestartClock(t)
	pm := &ProcessManager{
		engineName: "docker",
		ctx:        context.Background(),
		taskQueue:  NewTaskQueue(10),
		logger:     logging.NewModuleLogger(ProcessManagerModuleName),
	}
	task := NewContainerTask(TaskActionUpdate, "ms-retry-task")
	task.OperationID = "op-retry"
	pm.retryTask(task)
	if task.Retries != 1 {
		t.Fatalf("retries=%d", task.Retries)
	}
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("retry must not re-enqueue immediately")
	}
	clk.Advance(10 * time.Second)
	pm.enqueueDueRetries()
	tasks := drainTasks(pm.taskQueue)
	if len(tasks) != 1 || tasks[0] != task {
		t.Fatalf("expected retry after delay, got %#v", tasks)
	}
}

func newBackoffReconcilePM(t *testing.T, uuid string) (*ProcessManager, *models.Microservice, *lifecycleTestEngine) {
	t.Helper()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })
	ms := models.NewMicroservice(uuid, "nginx:latest")
	eng := &lifecycleTestEngine{
		workload: labeledWorkload("cid-"+uuid, uuid),
	}
	pm := &ProcessManager{
		engineName:          "docker",
		ctx:                 context.Background(),
		logger:              logging.NewModuleLogger(ProcessManagerModuleName),
		microserviceManager: &invariantMicroserviceManager{microservice: ms},
		containerManager:    newLifecycleCM(eng, &models.Registry{}),
		engine:              eng,
		taskQueue:           NewTaskQueue(10),
	}
	return pm, ms, eng
}

func labeledWorkload(id, msUUID string) *engine.Container {
	return &engine.Container{
		ID: id,
		Labels: map[string]string{
			workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
			workloadmeta.LabelMicroserviceUID: msUUID,
		},
	}
}

func exitingStatus(containerID, errMsg string) *models.MicroserviceStatus {
	st := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	st.ContainerID = containerID
	msg := errMsg
	st.ErrorMessage = &msg
	return st
}

func newRunningStatus(containerID string) *models.MicroserviceStatus {
	st := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	st.ContainerID = containerID
	return st
}

func drainTasks(q *TaskQueue) []*ContainerTask {
	var out []*ContainerTask
	for {
		task, ok := q.TryGet()
		if !ok {
			return out
		}
		out = append(out, task)
	}
}
