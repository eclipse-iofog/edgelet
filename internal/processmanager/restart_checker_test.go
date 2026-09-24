package processmanager

import (
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestRestartDelay(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, 0},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{6, 300 * time.Second},
		{12, 300 * time.Second},
	}
	for _, tc := range cases {
		if got := restartDelay(tc.n); got != tc.want {
			t.Fatalf("n=%d delay=%s want=%s", tc.n, got, tc.want)
		}
	}
}

func TestIsStuckReadsDoNotRecord(t *testing.T) {
	clk := withRestartClock(t)
	checker := GetRestartStuckChecker()
	for i := 0; i < AbnormalNumberOfRestarts; i++ {
		if checker.IsStuck("ms-tick") {
			t.Fatalf("read %d marked stuck without a recorded event", i+1)
		}
		clk.Advance(5 * time.Second)
	}
	if checker.IsStuck("ms-tick") {
		t.Fatal("monitor reads must not mark stuck")
	}
	if checker.ConsecutiveFailures("ms-tick") != 0 {
		t.Fatalf("reads must not increment consecutive failures, got %d", checker.ConsecutiveFailures("ms-tick"))
	}
}

func TestTenRecordedEventsMarkStuck(t *testing.T) {
	withRestartClock(t)
	checker := GetRestartStuckChecker()
	for i := 0; i < AbnormalNumberOfRestarts; i++ {
		checker.ObserveRunning("ms-loop")
		if !checker.RecordIfNew("ms-loop") {
			t.Fatalf("expected record on event %d", i+1)
		}
	}
	if !checker.IsStuck("ms-loop") {
		t.Fatal("expected stuck after 10 recorded events")
	}
}

func TestBackoffNResetsAfterStableRunning(t *testing.T) {
	clk := withRestartClock(t)
	checker := GetRestartStuckChecker()
	if !checker.RecordIfNew("ms-stable") {
		t.Fatal("expected first crash recorded")
	}
	if checker.ConsecutiveFailures("ms-stable") != 1 {
		t.Fatalf("n=%d", checker.ConsecutiveFailures("ms-stable"))
	}

	running := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
	running.ContainerID = "cid-stable"
	pmStatus := models.NewProcessManagerStatus()
	syncMicroserviceStatusToReporter(pmStatus, "ms-stable", running)
	maybeResetRestartBackoff("ms-stable", running)
	if checker.ConsecutiveFailures("ms-stable") != 1 {
		t.Fatal("n must stay until the running grace elapses")
	}

	clk.Advance(errorClearGrace)
	syncMicroserviceStatusToReporter(pmStatus, "ms-stable", running)
	maybeResetRestartBackoff("ms-stable", running)
	if checker.ConsecutiveFailures("ms-stable") != 0 {
		t.Fatalf("expected n reset after stable running, got %d", checker.ConsecutiveFailures("ms-stable"))
	}
}

func TestReadySkipRebuildAndCatalogAndFirstNonRestartable(t *testing.T) {
	withRestartClock(t)
	checker := GetRestartStuckChecker()
	checker.RecordIfNew("ms-skip")
	if checker.Ready("ms-skip", recreateSkipNone) {
		t.Fatal("expected wait after first crash")
	}
	if !checker.Ready("ms-skip", recreateSkipRebuild) {
		t.Fatal("rebuild must not wait")
	}

	checker.RecordIfNew("ms-cat")
	checker.MarkCatalogWaiting("ms-cat")
	if !checker.ConsumeCatalogReadySkip("ms-cat") {
		t.Fatal("expected catalog ready skip")
	}
	if !checker.Ready("ms-cat", recreateSkipCatalogReady) {
		t.Fatal("catalog becoming Ready must not wait")
	}

	checker.RecordIfNew("ms-cri")
	if !checker.Ready("ms-cri", recreateSkipNonRestartable) {
		t.Fatal("first non-restartable recreate must not wait")
	}
	checker.ObserveRunning("ms-cri")
	checker.RecordIfNew("ms-cri")
	if checker.Ready("ms-cri", recreateSkipNonRestartable) {
		t.Fatal("the next crash after a non-restartable recreate must wait")
	}
}

func TestTaskParkUntilDelayElapses(t *testing.T) {
	clk := withRestartClock(t)
	checker := GetRestartStuckChecker()
	task := NewContainerTask(TaskActionAdd, "ms-retry")
	delay := checker.ArmFailure("ms-retry")
	if delay != 10*time.Second {
		t.Fatalf("delay=%s", delay)
	}
	checker.ParkTask(task)
	if due := checker.TakeDueTasks(); len(due) != 0 {
		t.Fatalf("expected no due tasks, got %d", len(due))
	}
	clk.Advance(10 * time.Second)
	due := checker.TakeDueTasks()
	if len(due) != 1 || due[0] != task {
		t.Fatalf("expected parked task after delay, got %#v", due)
	}
}

func withRestartClock(t *testing.T) *syncTestClock {
	t.Helper()
	clk := withStatusSyncClock(t)
	resetRestartStuckCheckerForTest(clk.Now)
	t.Cleanup(func() { resetRestartStuckCheckerForTest(nil) })
	return clk
}
