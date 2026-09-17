package pruning

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func TestIsManagedContainer_UsesCanonicalLabelsOnly(t *testing.T) {
	managed := engine.Container{
		Labels: map[string]string{
			workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
			workloadmeta.LabelMicroserviceUID: "ms-1",
		},
	}
	if !isManagedContainer(managed) {
		t.Fatal("expected canonical managed labels to be treated as managed")
	}

	nonCanonicalOnly := engine.Container{
		Labels: map[string]string{
			"example.com/pretend-service": "x",
			"example.com/pretend-node":    "y",
		},
	}
	if isManagedContainer(nonCanonicalOnly) {
		t.Fatal("containers without canonical managed-by + microservice uid must not be treated as managed")
	}
}

func TestShouldRunImmediateFrequencyPrune(t *testing.T) {
	m := &Manager{}

	if m.shouldRunImmediateFrequencyPrune(0) {
		t.Fatal("expected no immediate run for disabled frequency")
	}

	if !m.shouldRunImmediateFrequencyPrune(1) {
		t.Fatal("expected immediate run when enabling frequency from 0 to 1")
	}
	m.setLastAppliedPruningFrequency(1)

	if m.shouldRunImmediateFrequencyPrune(1) {
		t.Fatal("expected no immediate run when frequency does not change")
	}

	if !m.shouldRunImmediateFrequencyPrune(2) {
		t.Fatal("expected immediate run when frequency changes from 1 to 2")
	}

	m.setLastAppliedPruningFrequency(2)
	if !m.shouldRunImmediateFrequencyPrune(1) {
		t.Fatal("expected immediate run when frequency changes from 2 to 1")
	}
}

func TestRunScheduledPrune_Order(t *testing.T) {
	order := make([]string, 0, 4)
	m := &Manager{
		pruneContainersHook: func() { order = append(order, "containers") },
		pruneVolumesHook:    func() { order = append(order, "volumes") },
		pruneImagesHook:     func() { order = append(order, "images") },
		pruneModelsHook:     func() { order = append(order, "models") },
	}

	m.runScheduledPrune()
	if len(order) != 4 || order[0] != "containers" || order[1] != "volumes" || order[2] != "images" || order[3] != "models" {
		t.Fatalf("expected prune order containers->volumes->images->models, got %v", order)
	}
}

func TestTriggerPruneOnFrequency_SkipsWhenAlreadyPruning(t *testing.T) {
	called := false
	m := &Manager{
		isPruning: true,
		pruneContainersHook: func() {
			called = true
		},
		pruneVolumesHook: func() {
			called = true
		},
		pruneImagesHook: func() {
			called = true
		},
	}

	m.triggerPruneOnFrequency()
	if called {
		t.Fatal("expected no prune steps when prune is already running")
	}
}

func TestTriggerPruneOnThresholdBreach_UsesScheduledOrder(t *testing.T) {
	cfg := config.GetInstance()
	originalThreshold := cfg.AvailableDiskThreshold
	cfg.AvailableDiskThreshold = 80
	t.Cleanup(func() {
		cfg.AvailableDiskThreshold = originalThreshold
	})

	sr := statusreporter.GetInstance()
	sr.UpdateResourceConsumptionManagerStatus(func(rcm *models.ResourceConsumptionManagerStatus) {
		rcm.TotalDiskSpace = 100
		rcm.AvailableDisk = 10
	})

	order := make([]string, 0, 4)
	m := &Manager{
		config:              cfg,
		pruneContainersHook: func() { order = append(order, "containers") },
		pruneVolumesHook:    func() { order = append(order, "volumes") },
		pruneImagesHook:     func() { order = append(order, "images") },
		pruneModelsHook:     func() { order = append(order, "models") },
	}

	m.triggerPruneOnThresholdBreach()
	if len(order) != 4 || order[0] != "containers" || order[1] != "volumes" || order[2] != "images" || order[3] != "models" {
		t.Fatalf("expected threshold prune order containers->volumes->images->models, got %v", order)
	}
}
