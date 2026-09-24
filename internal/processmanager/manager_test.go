package processmanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestProcessManager_GetInstance(t *testing.T) {
	pm1 := GetInstance()
	pm2 := GetInstance()

	if pm1 != pm2 {
		t.Error("GetInstance should return the same instance")
	}
}

func TestProcessManager_GetName(t *testing.T) {
	pm := GetInstance()
	name := pm.GetName()

	if name != ProcessManagerModuleName {
		t.Errorf("Expected %s, got %s", ProcessManagerModuleName, name)
	}
}

func TestProcessManager_GetModuleIndex(t *testing.T) {
	pm := GetInstance()
	index := pm.GetModuleIndex()

	if index < 0 {
		t.Error("Module index should be non-negative")
	}
}

func TestLaunchLocalMicroserviceWithProgress_EngineNotInitialized(t *testing.T) {
	pm := &ProcessManager{}
	called := false
	_, err := pm.LaunchLocalMicroserviceWithProgress(&models.Microservice{}, models.NewRegistry(2, "from_cache", true, "", "", ""), "", func(stage string, message string) {
		called = true
		_ = stage
		_ = message
	})
	if err == nil {
		t.Fatal("expected engine initialization error")
	}
	if !strings.Contains(err.Error(), "engine is not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected no callback events when engine is missing")
	}
}

func TestStreamMicroserviceLogs_EngineNotInitialized(t *testing.T) {
	pm := &ProcessManager{}
	err := pm.StreamMicroserviceLogs("ms-1", &engine.TailConfig{Follow: true, Lines: 10}, nil)
	if err == nil {
		t.Fatal("expected engine initialization error")
	}
	if !strings.Contains(err.Error(), "engine is not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Note: deleteRemainingMicroservices and updateRunningMicroservicesCount
// require initialized ProcessManager with Docker client and microservice manager.
// These are tested in integration tests.

func TestCountManagedRunningContainers_UsesCanonicalLabels(t *testing.T) {
	containers := []engine.Container{
		{
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
				workloadmeta.LabelMicroserviceUID: "ms-1",
			},
		},
		{
			Labels: map[string]string{
				"example.com/non-canonical-identity": "not-ms",
			},
		},
		{
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy: workloadmeta.ManagedByValue,
			},
		},
	}

	got := countManagedRunningContainers(containers)
	if got != 1 {
		t.Fatalf("expected 1 managed container, got %d", got)
	}
}

func TestCleanupDecisionForContainer_RemovesManagedStaleEvenWhenNotCurrentAndWatchdogOff(t *testing.T) {
	labels := map[string]string{
		workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
		workloadmeta.LabelMicroserviceUID: "ms-stale",
		workloadmeta.LabelScope:           workloadmeta.ScopeManaged,
	}

	removeManagedByUUID, removeUnknownByID := cleanupDecisionForContainer(
		labels,
		"cid-stale",
		"busybox:latest",
		false, // not in current set
		false, // not in latest set
		false, // watchdog disabled
		nil,
	)
	if !removeManagedByUUID {
		t.Fatal("expected stale managed workload to be removed by uuid when watchdog is disabled")
	}
	if removeUnknownByID {
		t.Fatal("did not expect unknown-by-id removal path for managed stale workload")
	}
}

func TestCleanupDecisionForContainer_DoesNotRemoveLocalScopeAsManagedStale(t *testing.T) {
	labels := map[string]string{
		workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
		workloadmeta.LabelMicroserviceUID: "local-ms",
		workloadmeta.LabelScope:           workloadmeta.ScopeLocal,
	}

	removeManagedByUUID, removeUnknownByID := cleanupDecisionForContainer(
		labels,
		"cid-local",
		"busybox:latest",
		false,
		false,
		false,
		nil,
	)
	if removeManagedByUUID || removeUnknownByID {
		t.Fatal("expected local scope workload to be preserved by managed stale cleanup")
	}
}

func TestAddMicroservice_QueuesTaskAndMarksUpdating(t *testing.T) {
	pm := &ProcessManager{
		ctx:       context.Background(),
		taskQueue: NewTaskQueue(10),
		logger:    logging.NewModuleLogger(ProcessManagerModuleName),
	}
	ms := models.NewMicroservice("ms-maint", "busybox:latest")

	pm.addMicroservice(ms)
	if pm.taskQueue.Size() != 1 {
		t.Fatalf("expected one queued task after add, got %d", pm.taskQueue.Size())
	}
	if !ms.GetIsUpdating() {
		t.Fatal("expected microservice marked updating after enqueue")
	}
}

func TestContainerAlreadyRemoved(t *testing.T) {
	if !containerAlreadyRemoved(nil) {
		t.Fatal("nil error is already removed")
	}
	if !containerAlreadyRemoved(errors.New("container is already in removing state")) {
		t.Fatal("already removing must be treated as success")
	}
	if !containerAlreadyRemoved(errors.New("No such container: abc")) {
		t.Fatal("no such container must be treated as success")
	}
	if containerAlreadyRemoved(errors.New("permission denied")) {
		t.Fatal("unrelated error must not be treated as success")
	}
}
