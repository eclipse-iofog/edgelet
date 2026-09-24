package processmanager

import (
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestReconcileRunningDoesNotReadUsage(t *testing.T) {
	pm, _ := newReconcileSchedulePM(t)
	pm.markReconcile("ms-a")
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); idle {
		t.Fatal("marked workload should reconcile")
	}
	eng := recordingEngineOf(t, pm)
	eng.mu.Lock()
	statsN := eng.statsN
	statusN := eng.statusN
	eng.mu.Unlock()
	if statsN != 0 {
		t.Fatalf("reconcile of a running container read usage %d times", statsN)
	}
	if statusN != 1 {
		t.Fatalf("reconcile should read status once, got %d", statusN)
	}
}

func TestSampleRunningContainerStatsUpdatesStatus(t *testing.T) {
	pm, _ := newReconcileSchedulePM(t)
	statusreporter.GetInstance().ResetProcessManagerStatus()
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := recordingEngineOf(t, pm)
	eng.mu.Lock()
	eng.stats = &engine.ContainerStats{CPUUsage: 7.5, MemoryUsage: 12345}
	eng.mu.Unlock()

	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		st := models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
		st.ContainerID = "cid-a"
		s.SetMicroservicesStatus("ms-a", st)
	})

	pm.sampleRunningContainerStats()
	got := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-a")
	if got == nil {
		t.Fatal("expected stored status")
	}
	if got.CPUUsage != 7.5 {
		t.Fatalf("cpu = %v want 7.5", got.CPUUsage)
	}
	if got.MemoryUsage != 12345 {
		t.Fatalf("memory = %d want 12345", got.MemoryUsage)
	}
	eng.mu.Lock()
	statsN := eng.statsN
	eng.mu.Unlock()
	if statsN != 1 {
		t.Fatalf("stats tick should sample once, got %d", statsN)
	}
}

func TestContainerStatsIntervalMatchesStatusPost(t *testing.T) {
	if containerStatsInterval != 10*time.Second {
		t.Fatalf("container stats interval = %s want 10s", containerStatsInterval)
	}
}
