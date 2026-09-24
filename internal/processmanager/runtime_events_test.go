package processmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type runtimeWakeEngine struct {
	lifecycleTestEngine
	mu         sync.Mutex
	byUUID     map[string]*engine.Container
	status     *models.MicroserviceStatus
	ip         string
	statusN    int
	ipN        int
	specN      int
	containers []string
}

func (e *runtimeWakeEngine) GetContainer(msUUID string) (*engine.Container, error) {
	e.mu.Lock()
	e.containers = append(e.containers, msUUID)
	c := e.byUUID[msUUID]
	e.mu.Unlock()
	if c == nil {
		return nil, nil
	}
	cp := *c
	return &cp, nil
}

func (e *runtimeWakeEngine) GetContainerStatus(string, string) (*models.MicroserviceStatus, error) {
	e.mu.Lock()
	e.statusN++
	st := e.status
	e.mu.Unlock()
	if st == nil {
		return newRunningStatus("cid"), nil
	}
	cp := *st
	return &cp, nil
}

func (e *runtimeWakeEngine) GetContainerIPAddress(string) (string, error) {
	e.mu.Lock()
	e.ipN++
	ip := e.ip
	e.mu.Unlock()
	if ip == "" {
		ip = "10.0.0.2"
	}
	return ip, nil
}

func (e *runtimeWakeEngine) AreMicroserviceAndContainerEqual(string, *models.Microservice, *models.Registry) bool {
	e.mu.Lock()
	e.specN++
	e.mu.Unlock()
	return true
}

func (e *runtimeWakeEngine) calls() (containers []string, statusN, ipN, specN int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	containers = append([]string(nil), e.containers...)
	return containers, e.statusN, e.ipN, e.specN
}

func newRuntimeEventPM(t *testing.T) (*ProcessManager, *runtimeWakeEngine, *models.Microservice) {
	t.Helper()
	openLocalReconcileTestDB(t)
	resetRestartStuckCheckerForTest(nil)
	t.Cleanup(func() {
		statusreporter.GetInstance().ResetProcessManagerStatus()
		resetRestartStuckCheckerForTest(nil)
	})

	msA := models.NewMicroservice("ms-a", "nginx:latest")
	msB := models.NewMicroservice("ms-b", "redis:latest")
	msm := &listMicroserviceManager{items: []*models.Microservice{msA, msB}}
	eng := &runtimeWakeEngine{
		byUUID: map[string]*engine.Container{
			"ms-a": labeledWorkload("cid-a", "ms-a"),
			"ms-b": labeledWorkload("cid-b", "ms-b"),
		},
		ip:     "10.1.1.5",
		status: newRunningStatus("cid-a"),
	}
	pm := &ProcessManager{
		engineName:          "edgelet",
		ctx:                 context.Background(),
		logger:              logging.NewModuleLogger(ProcessManagerModuleName),
		engine:              eng,
		microserviceManager: msm,
		containerManager:    NewContainerManager(eng, msm, "edgelet"),
		taskQueue:           NewTaskQueue(4),
		updateChan:          make(chan struct{}, 1),
	}
	return pm, eng, msA
}

func TestRuntimeExit_ReconcilesThatWorkloadAndRecordsBackoff(t *testing.T) {
	pm, eng, _ := newRuntimeEventPM(t)
	eng.status = exitingStatus("cid-a", "CRI reason=Error exitCode=1 message=exited")

	pm.ReconcileRuntimeEvent("ms-a", runtimeEventExit)

	containers, _, _, _ := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("exit wake inspected %v", containers)
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-a")
	if st == nil || st.RestartCount != 1 {
		t.Fatalf("restart count=%v", st)
	}
	if GetRestartStuckChecker().ConsecutiveFailures("ms-a") < 1 {
		t.Fatal("exit must record restart backoff")
	}
	if queued := drainTasks(pm.taskQueue); len(queued) != 0 {
		t.Fatalf("exit inside backoff must not start immediately, queued %d", len(queued))
	}
}

func TestRuntimeOOM_ReconcilesWithExistingReason(t *testing.T) {
	pm, eng, _ := newRuntimeEventPM(t)
	const oomReason = "CRI reason=OOMKilled exitCode=137 message=OOMKilled"
	eng.status = exitingStatus("cid-a", oomReason)

	pm.ReconcileRuntimeEvent("ms-a", runtimeEventOOM)

	containers, _, _, _ := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("oom wake inspected %v", containers)
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-a")
	if st == nil || st.ErrorMessage == nil || *st.ErrorMessage != oomReason {
		t.Fatalf("status=%v", st)
	}
	if st.RestartCount != 1 {
		t.Fatalf("restart count=%d", st.RestartCount)
	}
}

func TestRuntimeDelete_MissingContainerWhenDesiredRunning(t *testing.T) {
	pm, eng, _ := newRuntimeEventPM(t)
	delete(eng.byUUID, "ms-a")

	pm.ReconcileRuntimeEvent("ms-a", runtimeEventDelete)

	containers, _, _, _ := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("delete wake inspected %v", containers)
	}
	queued := drainTasks(pm.taskQueue)
	if len(queued) != 1 || queued[0].Action != TaskActionAdd || queued[0].MicroserviceUUID != "ms-a" {
		t.Fatalf("queued=%v", queued)
	}
}

func TestRuntimeStart_RefreshesStatusAndIPWithoutSpecCompare(t *testing.T) {
	pm, eng, ms := newRuntimeEventPM(t)
	if !GetRestartStuckChecker().RecordIfNew("ms-a") {
		t.Fatal("expected a new exit streak")
	}
	if GetRestartStuckChecker().RecordIfNew("ms-a") {
		t.Fatal("exit streak should still be open")
	}

	pm.ReconcileRuntimeEvent("ms-a", runtimeEventStart)

	containers, statusN, ipN, specN := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("start wake inspected %v", containers)
	}
	if statusN != 1 || ipN != 1 {
		t.Fatalf("status calls=%d ip calls=%d", statusN, ipN)
	}
	if specN != 0 {
		t.Fatalf("start must not compare the container spec, calls=%d", specN)
	}
	if ms.ContainerIPAddress == nil || *ms.ContainerIPAddress != "10.1.1.5" {
		t.Fatalf("ip=%v", ms.ContainerIPAddress)
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-a")
	if st == nil || st.Status != models.MicroserviceStateRunning {
		t.Fatalf("status=%v", st)
	}
	if st.IPAddress == nil || *st.IPAddress != "10.1.1.5" {
		t.Fatalf("status ip=%v", st.IPAddress)
	}
	if !GetRestartStuckChecker().RecordIfNew("ms-a") {
		t.Fatal("start must clear the exit streak")
	}
}

func TestRuntimeEventStream_DegradedTickReconcilesAllThenRecovers(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	if !pm.NoteRuntimeEventStreamUnavailable() {
		t.Fatal("the first outage must be reported once")
	}
	if pm.NoteRuntimeEventStreamUnavailable() {
		t.Fatal("a down stream must not be reported again on the next tick")
	}
	if !pm.RuntimeEventStreamDegraded() {
		t.Fatal("expected the event stream to be down")
	}

	pm.reconcileScheduled(&reconcileCycleStats{})
	got := trace.snapshot()
	want := []string{
		"container:cp-1",
		"control-plane",
		"container:ms-a",
		"container:ms-b",
		"container:local-1",
		"local:local-1",
	}
	if len(got) != len(want) {
		t.Fatalf("degraded tick = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("degraded tick = %v", got)
		}
	}

	pm.reconcileScheduled(&reconcileCycleStats{})
	if again := trace.snapshot(); len(again) != 2*len(want) {
		t.Fatalf("second degraded tick = %v", again)
	}

	pm.NoteRuntimeEventStreamHealthy()
	if pm.RuntimeEventStreamDegraded() {
		t.Fatal("a successful subscribe must leave the degraded state")
	}
	pm.reconcileScheduled(&reconcileCycleStats{})
	if after := trace.snapshot(); len(after) != 2*len(want) {
		t.Fatalf("recovered tick inspected more workloads: %v", after)
	}
}

func TestRuntimeExit_DuringArmedRestartWaitDoesNotQueueSecondStart(t *testing.T) {
	pm, eng, _ := newRuntimeEventPM(t)
	clk := withRestartClock(t)
	eng.status = exitingStatus("cid-a", "CRI reason=Error exitCode=1 message=exited")
	after := &manualAfter{now: clk.Now}
	pm.reconcileDeadlineAfter = after.AfterFunc

	task := NewContainerTask(TaskActionAdd, "ms-a")
	pm.retryTask(task)
	pm.ReconcileRuntimeEvent("ms-a", runtimeEventExit)

	if queued := drainTasks(pm.taskQueue); len(queued) != 0 {
		t.Fatalf("exit during an armed restart wait queued %d starts", len(queued))
	}
	if due := GetRestartStuckChecker().TakeDueTasks(); len(due) != 0 {
		t.Fatalf("the scheduled start must stay parked, got %d", len(due))
	}
	containers, _, _, _ := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("exit wake inspected %v", containers)
	}

	clk.Advance(10 * time.Second)
	after.fireDue()
	pm.reconcileScheduled(&reconcileCycleStats{})
	if queued := drainTasks(pm.taskQueue); len(queued) != 0 {
		t.Fatalf("the original wait must not enqueue a second start early, got %d", len(queued))
	}
}

func TestRuntimeExit_MonitorAppliesTheWake(t *testing.T) {
	pm, eng, _ := newRuntimeEventPM(t)
	eng.status = exitingStatus("cid-a", "CRI reason=Error exitCode=1 message=exited")
	pm.runtimeEvents = make(chan runtimeEventSignal, 1)
	pm.setReconcileMonitorRunning(true)
	done := make(chan struct{})
	go func() {
		ev := <-pm.runtimeEvents
		pm.deliverRuntimeEvent(ev)
		close(done)
	}()

	pm.ReconcileRuntimeEvent("ms-a", runtimeEventExit)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the monitor did not apply the container event")
	}
	containers, _, _, _ := eng.calls()
	if len(containers) != 1 || containers[0] != "ms-a" {
		t.Fatalf("monitor wake inspected %v", containers)
	}
}
