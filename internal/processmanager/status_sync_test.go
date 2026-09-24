package processmanager

import (
	"sync"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type syncTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *syncTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *syncTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func withStatusSyncClock(t *testing.T) *syncTestClock {
	t.Helper()
	clk := &syncTestClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	resetStatusSyncStateForTest(clk.Now)
	t.Cleanup(func() { resetStatusSyncStateForTest(nil) })
	return clk
}

func TestSyncMicroserviceStatusToReporter_KeepsErrorBeforeGrace(t *testing.T) {
	clk := withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "Container ms-1 ADD operation failed after 5 attempts: docker unavailable"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateFailed)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	runtimeStatus := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	runtimeStatus.ContainerID = "cid-1"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", runtimeStatus)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got == nil {
		t.Fatal("expected microservice status")
	}
	if got.Status != models.MicroserviceStateRunning {
		t.Fatalf("expected RUNNING, got %s", got.Status)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected errorMessage %q before grace, got %#v", errMsg, got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError %q, got %q", errMsg, got.LastError)
	}
	if got.LastErrorAt <= 0 {
		t.Fatalf("expected lastErrorAt > 0, got %d", got.LastErrorAt)
	}

	clk.Advance(errorClearGrace - time.Second)
	stillRunning := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	stillRunning.ContainerID = "cid-1"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", stillRunning)
	got = pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected errorMessage kept at 29s, got %#v", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept at 29s, got %q", got.LastError)
	}
}

func TestSyncMicroserviceStatusToReporter_ClearsCurrentErrorAfterGrace(t *testing.T) {
	clk := withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "Container ms-1 ADD operation failed after 5 attempts: docker unavailable"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateFailed)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	runtimeStatus := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	runtimeStatus.ContainerID = "cid-1"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", runtimeStatus)

	clk.Advance(errorClearGrace)
	stable := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	stable.ContainerID = "cid-1"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", stable)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil {
		t.Fatal("expected explicit cleared errorMessage pointer")
	}
	if *got.ErrorMessage != "" {
		t.Fatalf("expected empty errorMessage after grace, got %q", *got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept after grace, got %q", got.LastError)
	}
}

func TestRunningErrorClearsFromStoredStatus(t *testing.T) {
	clk := withStatusSyncClock(t)
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })
	errMsg := "exitCode=1 oomKilled=false"
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroservicesState("ms-1", models.MicroserviceStateFailed)
		s.SetMicroservicesStatusErrorMessage("ms-1", errMsg)
		running := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
		running.ContainerID = "cid-1"
		syncMicroserviceStatusToReporter(s, "ms-1", running)
	})

	clk.Advance(errorClearGrace - time.Second)
	AdvanceRunningErrorClear()
	got := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-1")
	if got == nil || got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("errorMessage = %#v before grace", got)
	}

	clk.Advance(time.Second)
	AdvanceRunningErrorClear()
	got = statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != "" {
		t.Fatalf("errorMessage = %#v after continuous running", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("lastError = %q", got.LastError)
	}
}

func TestSyncMicroserviceStatusToReporter_StartingPreservesError(t *testing.T) {
	withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "exitCode=1 oomKilled=false"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateExiting)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	starting := models.NewMicroserviceStatusWithState(models.MicroserviceStateStarting)
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", starting)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.Status != models.MicroserviceStateStarting {
		t.Fatalf("expected STARTING, got %s", got.Status)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected STARTING to keep errorMessage, got %#v", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept during STARTING, got %q", got.LastError)
	}
}

func TestSyncMicroserviceStatusToReporter_UpdatingPreservesError(t *testing.T) {
	withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "previous crash"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateExiting)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	updating := models.NewMicroserviceStatusWithState(models.MicroserviceStateUpdating)
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", updating)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected UPDATING to keep errorMessage, got %#v", got.ErrorMessage)
	}
}

func TestSyncMicroserviceStatusToReporter_PreservesErrorWhenNotRunning(t *testing.T) {
	withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "still failing"
	runtimeStatus := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	runtimeStatus.ErrorMessage = &errMsg

	syncMicroserviceStatusToReporter(pmStatus, "ms-1", runtimeStatus)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got == nil || got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected errorMessage %q to remain, got %+v", errMsg, got)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError %q, got %q", errMsg, got.LastError)
	}
	if got.LastErrorAt <= 0 {
		t.Fatalf("expected lastErrorAt > 0, got %d", got.LastErrorAt)
	}
}

func TestSyncMicroserviceStatusToReporter_NewFailureOverwritesLastError(t *testing.T) {
	withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	first := "first crash"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateExiting)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", first)

	second := "second crash"
	runtimeStatus := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	runtimeStatus.ErrorMessage = &second
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", runtimeStatus)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.LastError != second {
		t.Fatalf("expected lastError overwritten, got %q", got.LastError)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != second {
		t.Fatalf("expected current errorMessage %q, got %#v", second, got.ErrorMessage)
	}
}

func TestSyncMicroserviceStatusToReporter_NewContainerGenerationRestartsGrace(t *testing.T) {
	clk := withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "crashed"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateExiting)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	first := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	first.ContainerID = "cid-old"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", first)

	clk.Advance(errorClearGrace - time.Second)
	recreated := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	recreated.ContainerID = "cid-new"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", recreated)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected error kept after new generation, got %#v", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept after new generation, got %q", got.LastError)
	}

	clk.Advance(errorClearGrace)
	stable := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	stable.ContainerID = "cid-new"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", stable)
	got = pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != "" {
		t.Fatalf("expected current error cleared after new generation grace, got %#v", got.ErrorMessage)
	}
}

func TestSyncMicroserviceStatusToReporter_ShorterGraceViaTestHook(t *testing.T) {
	clk := withStatusSyncClock(t)
	setStatusSyncGraceForTest(time.Second)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "hook crash"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateFailed)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)

	running := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	running.ContainerID = "cid-1"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", running)
	clk.Advance(time.Second)
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning))

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != "" {
		t.Fatalf("expected current error cleared after injected grace, got %#v", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept, got %q", got.LastError)
	}
}

func TestRebuildZerosRestartCountKeepsLastError(t *testing.T) {
	withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()
	errMsg := "crash before rebuild"
	pmStatus.SetMicroservicesState("ms-1", models.MicroserviceStateFailed)
	pmStatus.SetMicroservicesStatusErrorMessage("ms-1", errMsg)
	st := pmStatus.GetMicroserviceStatus("ms-1")
	st.RestartCount = 4
	pmStatus.SetMicroservicesStatus("ms-1", st)

	pmStatus.ResetMicroservicesRestartCount("ms-1")

	running := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	running.ContainerID = "cid-rebuild"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", running)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.RestartCount != 0 {
		t.Fatalf("expected restartCount 0 after rebuild, got %d", got.RestartCount)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept after rebuild, got %q", got.LastError)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected current error kept before grace, got %#v", got.ErrorMessage)
	}
}

func TestSyncMicroserviceStatusToReporter_HealthyRunningInspectDoesNotReattachExitText(t *testing.T) {
	clk := withStatusSyncClock(t)
	pmStatus := models.NewProcessManagerStatus()

	exitText := "exitCode=1 oomKilled=false error=config missing"
	crashed := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	crashed.ErrorMessage = &exitText
	crashed.ContainerID = "cid-crash"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", crashed)

	// Healthy running inspect leaves ErrorMessage unset (formatter returns empty).
	running := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	running.ContainerID = "cid-healthy"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", running)

	got := pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != exitText {
		t.Fatalf("expected current error kept before grace, got %#v", got.ErrorMessage)
	}
	if got.LastError != exitText {
		t.Fatalf("expected lastError %q, got %q", exitText, got.LastError)
	}

	clk.Advance(errorClearGrace)
	stable := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	stable.ContainerID = "cid-healthy"
	syncMicroserviceStatusToReporter(pmStatus, "ms-1", stable)

	got = pmStatus.GetMicroserviceStatus("ms-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != "" {
		t.Fatalf("expected current error cleared after grace, got %#v", got.ErrorMessage)
	}
	if got.LastError != exitText {
		t.Fatalf("expected lastError kept after grace, got %q", got.LastError)
	}
}

func TestSyncControlPlaneProcessManagerStatus_FirstRunningUsesGrace(t *testing.T) {
	withStatusSyncClock(t)
	sr := statusreporter.GetInstance()
	sr.ResetProcessManagerStatus()
	t.Cleanup(func() { sr.ResetProcessManagerStatus() })

	errMsg := "controller crashed"
	sr.UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
		pmStatus.SetMicroservicesState("cp-1", models.MicroserviceStateExiting)
		pmStatus.SetMicroservicesStatusErrorMessage("cp-1", errMsg)
	})

	pm := &ProcessManager{}
	item := &models.ControlPlaneDeployment{ControllerUUID: "cp-1", RuntimeState: "running"}
	container := &engine.Container{ID: "cp-cid"}
	status := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	status.ContainerID = "cp-cid"
	pm.syncControlPlaneProcessManagerStatus(item, container, status)

	got := sr.GetProcessManagerStatus().GetMicroserviceStatus("cp-1")
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Fatalf("expected control plane first RUNNING to keep error before grace, got %#v", got.ErrorMessage)
	}
	if got.LastError != errMsg {
		t.Fatalf("expected lastError kept, got %q", got.LastError)
	}
}
