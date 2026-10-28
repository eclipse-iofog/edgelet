package processmanager

import (
	"context"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type recreateTestEngine struct {
	lifecycleTestEngine
	pullCalled  bool
	removeCalls int
}

func (e *recreateTestEngine) PullImage(string, *models.Registry, *engine.PullImageOptions) error {
	e.pullCalled = true
	return nil
}

func (e *recreateTestEngine) RemoveContainer(string, bool) error {
	e.removeCalls++
	e.workload = nil
	return nil
}

func TestRecreateContainer_RemovesExistingThenCreates(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &recreateTestEngine{
		lifecycleTestEngine: lifecycleTestEngine{
			workload: &engine.Container{
				ID:    "cid-old",
				Image: "nginx:old",
				Labels: map[string]string{
					workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
					workloadmeta.LabelMicroserviceUID: "ms-recreate",
				},
			},
		},
	}
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: &models.Registry{URL: "from_cache"}}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	ms := models.NewMicroservice("ms-recreate", "nginx:latest")
	ms.RegistryID = 1
	ctx := context.Background()

	id, err := cm.RecreateContainer(ctx, ms, RecreateOptions{PullImage: false})
	if err != nil {
		t.Fatalf("RecreateContainer: %v", err)
	}
	if id != "cid-new" {
		t.Fatalf("expected cid-new, got %q", id)
	}
	if eng.workload != nil {
		t.Fatalf("expected old workload removed, still have %v", eng.workload)
	}
}

func TestRecreateContainer_NoExistingContainer(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &recreateTestEngine{}
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: &models.Registry{URL: "from_cache"}}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	ms := models.NewMicroservice("ms-new", "nginx:latest")
	ms.RegistryID = 1

	id, err := cm.RecreateContainer(context.Background(), ms, RecreateOptions{PullImage: false})
	if err != nil {
		t.Fatalf("RecreateContainer: %v", err)
	}
	if id != "cid-new" {
		t.Fatalf("expected cid-new, got %q", id)
	}
	if eng.removeCalls != 0 {
		t.Fatalf("expected no remove call, got %d", eng.removeCalls)
	}
}

func TestRecreateContainer_PullImageFalse(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &recreateTestEngine{}
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: &models.Registry{URL: "https://registry.example"}}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	ms := models.NewMicroservice("ms-no-pull", "nginx:latest")
	ms.RegistryID = 1

	if _, err := cm.RecreateContainer(context.Background(), ms, RecreateOptions{PullImage: false}); err != nil {
		t.Fatalf("RecreateContainer: %v", err)
	}
	if eng.pullCalled {
		t.Fatal("expected pull to be skipped when PullImage=false")
	}
}

type noImageTestEngine struct {
	recreateTestEngine
}

func (e *noImageTestEngine) FindLocalImage(string, string, bool) (bool, error) { return false, nil }

func TestRecreateContainer_ImageNotInCache(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	base := recreateTestEngine{
		lifecycleTestEngine: lifecycleTestEngine{
			workload: &engine.Container{
				ID: "cid-old",
				Labels: map[string]string{
					workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
					workloadmeta.LabelMicroserviceUID: "ms-missing-image",
				},
			},
		},
	}
	eng := &noImageTestEngine{recreateTestEngine: base}
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: &models.Registry{URL: "from_cache"}}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	ms := models.NewMicroservice("ms-missing-image", "nginx:missing")
	ms.RegistryID = 1

	_, err := cm.RecreateContainer(context.Background(), ms, RecreateOptions{PullImage: false})
	if err == nil {
		t.Fatal("expected error when image not in cache")
	}
	if eng.removeCalls != 0 {
		t.Fatalf("missing image must not remove the container, removes=%d", eng.removeCalls)
	}
}

func TestRecreateContainer_EmitsLifecycleEvents(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	events := captureEvents(t)
	eng := &recreateTestEngine{}
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: &models.Registry{URL: "from_cache"}}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	ms := models.NewMicroservice("ms-events", "nginx:latest")
	ms.RegistryID = 1

	if _, err := cm.RecreateContainer(context.Background(), ms, RecreateOptions{PullImage: false}); err != nil {
		t.Fatalf("RecreateContainer: %v", err)
	}

	want := []string{
		runtimeops.EventContainerUpdatePhase,
		runtimeops.EventContainerCreating,
		runtimeops.EventContainerCreated,
		runtimeops.EventContainerStarting,
		runtimeops.EventContainerStarted,
	}
	if !containsSubsequence(eventNames(*events), want) {
		t.Fatalf("event sequence got %v want subsequence %v", eventNames(*events), want)
	}
}
