package pruning

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

func TestStart_DoesNotRunImmediateFrequencyPrune(t *testing.T) {
	disk, dataMarker, sharedMarker := writePersistentVolumeMarkers(t)
	cfg := pruneTestConfig(t, disk, 1)

	var pruned atomic.Bool
	recorder := &volumePruneRecorder{disk: disk}
	m := &Manager{
		config:          cfg,
		containerEngine: recorder,
		pruneContainersHook: func() {
			pruned.Store(true)
		},
		pruneVolumesHook: func() {
			t.Error("persistent volume prune hook must not run on start")
		},
		pruneImagesHook: func() {
			pruned.Store(true)
		},
		pruneModelsHook: func() {
			pruned.Store(true)
		},
		pruneKnowledgeHook: func() {
			pruned.Store(true)
		},
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	assertNoImmediateFrequencyPrune(t, &pruned)
	assertPersistentVolumeMarkers(t, dataMarker, sharedMarker)
	if recorder.calls.Load() != 0 {
		t.Fatalf("engine volume prune must not run on start, got %d calls", recorder.calls.Load())
	}
}

func TestRunScheduledPrune_OrderOmitsPersistentVolumes(t *testing.T) {
	order := make([]string, 0, 4)
	volumeCalls := 0
	m := &Manager{
		pruneContainersHook: func() { order = append(order, "containers") },
		pruneVolumesHook:    func() { volumeCalls++ },
		pruneImagesHook:     func() { order = append(order, "images") },
		pruneModelsHook:     func() { order = append(order, "models") },
		pruneKnowledgeHook:  func() { order = append(order, "knowledge") },
	}

	m.runScheduledPrune()
	if volumeCalls != 0 {
		t.Fatalf("persistent volume prune must not run on the scheduled job, got %d calls", volumeCalls)
	}
	if len(order) != 4 || order[0] != "containers" || order[1] != "images" || order[2] != "models" || order[3] != "knowledge" {
		t.Fatalf("expected prune order containers->images->models->knowledge, got %v", order)
	}
}

func TestRunScheduledPrune_DoesNotCallEngineVolumePrune(t *testing.T) {
	recorder := &volumePruneRecorder{}
	m := &Manager{
		containerEngine:     recorder,
		pruneContainersHook: func() {},
		pruneImagesHook:     func() {},
		pruneModelsHook:     func() {},
		pruneKnowledgeHook:  func() {},
	}
	m.runScheduledPrune()
	if recorder.calls.Load() != 0 {
		t.Fatalf("scheduled prune must not call volume prune, got %d calls", recorder.calls.Load())
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

func TestTriggerPruneOnThresholdBreach_OmitsPersistentVolumes(t *testing.T) {
	disk, dataMarker, sharedMarker := writePersistentVolumeMarkers(t)
	cfg := pruneTestConfig(t, disk, 0)
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
	volumeCalls := 0
	recorder := &volumePruneRecorder{disk: disk}
	m := &Manager{
		config:              cfg,
		containerEngine:     recorder,
		pruneContainersHook: func() { order = append(order, "containers") },
		pruneVolumesHook:    func() { volumeCalls++ },
		pruneImagesHook:     func() { order = append(order, "images") },
		pruneModelsHook:     func() { order = append(order, "models") },
		pruneKnowledgeHook:  func() { order = append(order, "knowledge") },
	}

	m.triggerPruneOnThresholdBreach()
	if volumeCalls != 0 {
		t.Fatalf("persistent volume prune must not run on disk-threshold prune, got %d calls", volumeCalls)
	}
	if recorder.calls.Load() != 0 {
		t.Fatalf("engine volume prune must not run on disk-threshold prune, got %d calls", recorder.calls.Load())
	}
	if len(order) != 4 || order[0] != "containers" || order[1] != "images" || order[2] != "models" || order[3] != "knowledge" {
		t.Fatalf("expected threshold prune order containers->images->models->knowledge, got %v", order)
	}
	assertPersistentVolumeMarkers(t, dataMarker, sharedMarker)
}

func TestChangePruningFreqInterval_EnablingDoesNotRunImmediatePrune(t *testing.T) {
	disk, dataMarker, sharedMarker := writePersistentVolumeMarkers(t)
	cfg := pruneTestConfig(t, disk, 1)

	var pruned atomic.Bool
	recorder := &volumePruneRecorder{disk: disk}
	m := &Manager{
		config:          cfg,
		containerEngine: recorder,
		pruneContainersHook: func() {
			pruned.Store(true)
		},
		pruneVolumesHook: func() {
			t.Error("persistent volume prune hook must not run when enabling frequency")
		},
		pruneImagesHook: func() {
			pruned.Store(true)
		},
		pruneModelsHook: func() {
			pruned.Store(true)
		},
		pruneKnowledgeHook: func() {
			pruned.Store(true)
		},
	}

	m.ChangePruningFreqInterval()
	t.Cleanup(func() { _ = m.Stop() })
	assertNoImmediateFrequencyPrune(t, &pruned)
	assertPersistentVolumeMarkers(t, dataMarker, sharedMarker)
	if recorder.calls.Load() != 0 {
		t.Fatalf("engine volume prune must not run when enabling frequency, got %d calls", recorder.calls.Load())
	}
}

func TestGetUnwantedImagesList_KeepsRunningLocalMicroserviceImage(t *testing.T) {
	alpine := "docker.io/library/alpine:3.19"
	busybox := "docker.io/library/busybox:1.36"
	eng := &pruneListEngine{
		images: []engine.ImageInfo{
			{ID: "sha256:alpine", RepoTags: []string{alpine}},
			{ID: "sha256:busybox", RepoTags: []string{busybox}},
		},
		running: []engine.Container{
			{
				ID:    "vol-it-a",
				Image: alpine,
				Labels: map[string]string{
					workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
					workloadmeta.LabelMicroserviceUID: "local-uuid",
				},
			},
		},
	}
	m := &Manager{
		containerEngine:       eng,
		getMicroserviceImages: func() []string { return nil },
	}

	got := m.getUnwantedImagesList(context.Background(), eng)
	if containsImageRef(got, alpine) {
		t.Fatalf("running local microservice image %q must stay in the keep-set, unwanted=%v", alpine, got)
	}
	if !containsImageRef(got, busybox) {
		t.Fatalf("unused image %q must be pruned, unwanted=%v", busybox, got)
	}
}

func TestGetUnwantedImagesList_KeepsConfiguredLocalImageWhenNotRunning(t *testing.T) {
	alpine := "docker.io/library/alpine:3.19"
	busybox := "docker.io/library/busybox:1.36"
	eng := &pruneListEngine{
		images: []engine.ImageInfo{
			{ID: "sha256:alpine", RepoTags: []string{alpine}},
			{ID: "sha256:busybox", RepoTags: []string{busybox}},
		},
	}
	m := &Manager{
		containerEngine:       eng,
		getMicroserviceImages: func() []string { return []string{alpine} },
	}

	got := m.getUnwantedImagesList(context.Background(), eng)
	if containsImageRef(got, alpine) {
		t.Fatalf("configured local-deploy image %q must stay in the keep-set, unwanted=%v", alpine, got)
	}
	if !containsImageRef(got, busybox) {
		t.Fatalf("unused image %q must be pruned, unwanted=%v", busybox, got)
	}
}

func TestGetUnwantedImagesList_KeepsInUseSandboxImage(t *testing.T) {
	pause := "docker.io/portainer/pause:latest"
	busybox := "docker.io/library/busybox:1.36"
	eng := &pruneListEngine{
		images: []engine.ImageInfo{
			{ID: "sha256:pause", RepoTags: []string{pause}, InUse: 2},
			{ID: "sha256:busybox", RepoTags: []string{busybox}, InUse: 0},
		},
	}
	m := &Manager{containerEngine: eng}

	got := m.getUnwantedImagesList(context.Background(), eng)
	if containsImageRef(got, pause) {
		t.Fatalf("in-use sandbox image %q must stay in the keep-set, unwanted=%v", pause, got)
	}
	if !containsImageRef(got, busybox) {
		t.Fatalf("unused image %q must be pruned, unwanted=%v", busybox, got)
	}
}

func TestRunScheduledPrune_ImageAndModelHooksStillFire(t *testing.T) {
	var imageCalls, modelCalls, knowledgeCalls int
	m := &Manager{
		pruneContainersHook: func() {},
		pruneImagesHook:     func() { imageCalls++ },
		pruneModelsHook:     func() { modelCalls++ },
		pruneKnowledgeHook:  func() { knowledgeCalls++ },
	}

	m.runScheduledPrune()
	if imageCalls != 1 || modelCalls != 1 || knowledgeCalls != 1 {
		t.Fatalf("expected image, model, and knowledge prune once, images=%d models=%d knowledge=%d", imageCalls, modelCalls, knowledgeCalls)
	}
}

func TestRunScheduledPrune_KnowledgeHookFiresWithModels(t *testing.T) {
	order := make([]string, 0, 2)
	m := &Manager{
		pruneContainersHook: func() {},
		pruneImagesHook:     func() {},
		pruneModelsHook:     func() { order = append(order, "models") },
		pruneKnowledgeHook:  func() { order = append(order, "knowledge") },
	}

	m.runScheduledPrune()
	if len(order) != 2 || order[0] != "models" || order[1] != "knowledge" {
		t.Fatalf("expected dangling knowledge on the same tick as unused local models, got %v", order)
	}
}

type pruneListEngine struct {
	engine.ContainerEngine
	images  []engine.ImageInfo
	running []engine.Container
}

func (e *pruneListEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	return e.images, nil
}

func (e *pruneListEngine) GetRunningContainers() ([]engine.Container, error) {
	if e.running == nil {
		return []engine.Container{}, nil
	}
	return e.running, nil
}

func containsImageRef(refs []string, want string) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

type volumePruneRecorder struct {
	engine.ContainerEngine
	disk  string
	calls atomic.Int32
}

func (r *volumePruneRecorder) PruneVolumes(_ context.Context) (*engine.VolumePruneReport, error) {
	r.calls.Add(1)
	if r.disk != "" {
		_ = os.RemoveAll(filepath.Join(r.disk, "volumes", "data"))
		_ = os.RemoveAll(filepath.Join(r.disk, "volumes", "shared"))
	}
	return &engine.VolumePruneReport{}, nil
}

func pruneTestConfig(t *testing.T, disk string, frequency int64) *config.Config {
	t.Helper()
	cfg := config.GetInstance()
	originalDisk := cfg.DiskDirectory
	originalFreq := cfg.PruningFrequency
	cfg.DiskDirectory = disk
	cfg.PruningFrequency = frequency
	t.Cleanup(func() {
		cfg.DiskDirectory = originalDisk
		cfg.PruningFrequency = originalFreq
	})
	return cfg
}

func writePersistentVolumeMarkers(t *testing.T) (disk, dataMarker, sharedMarker string) {
	t.Helper()
	disk = t.TempDir()
	dataMarker = filepath.Join(disk, "volumes", "data", "ms-uuid", "name", "marker")
	sharedMarker = filepath.Join(disk, "volumes", "shared", "shared-name", "marker")
	for _, marker := range []string{dataMarker, sharedMarker} {
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return disk, dataMarker, sharedMarker
}

func assertPersistentVolumeMarkers(t *testing.T, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("persistent volume marker missing: %s: %v", marker, err)
		}
	}
}

func assertNoImmediateFrequencyPrune(t *testing.T, pruned *atomic.Bool) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if pruned.Load() {
		t.Fatal("frequency prune must not run on start or when enabling pruningFrequency")
	}
}
