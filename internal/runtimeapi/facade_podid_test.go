package runtimeapi

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestAttachPodID_UsesStatusThenEngineRules(t *testing.T) {
	f := NewFacade()
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	entry := map[string]any{}
	f.attachPodID(entry, "app-1", "pause-1")
	if entry["podId"] != "pause-1" {
		t.Fatalf("status podId: %#v", entry["podId"])
	}

	cfg.ContainerEngine = constants.EngineDocker
	entry = map[string]any{}
	f.attachPodID(entry, "cid-1", "")
	if entry["podId"] != "cid-1" {
		t.Fatalf("docker podId: %#v", entry["podId"])
	}

	cfg.ContainerEngine = constants.EnginePodman
	entry = map[string]any{}
	f.attachPodID(entry, "cid-2", "")
	if entry["podId"] != "cid-2" {
		t.Fatalf("podman podId: %#v", entry["podId"])
	}

	cfg.ContainerEngine = constants.EngineEdgelet
	entry = map[string]any{}
	f.attachPodID(entry, "app-1", "")
	if _, ok := entry["podId"]; ok {
		t.Fatalf("expected omitted sandbox podId, got %#v", entry["podId"])
	}
}

func TestFacadeGetRuntimeMicroservice_DockerPodIDEqualsContainerID(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineDocker
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	if err := f.db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-pod-docker",
		ApplicationName:  "edgelet",
		MicroserviceName: "demo",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		State:            "running",
		DesiredState:     "running",
		RuntimeState:     "running",
		ContainerID:      "cid-app",
		Generation:       1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	item, err := f.GetRuntimeMicroservice("local-pod-docker")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if item["containerId"] != "cid-app" {
		t.Fatalf("containerId=%#v", item["containerId"])
	}
	if item["podId"] != "cid-app" {
		t.Fatalf("docker podId should equal containerId, got %#v", item["podId"])
	}
}

func TestFacadeGetRuntimeMicroservice_EdgeletOmitsMissingSandbox(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineEdgelet
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	if err := f.db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-pod-edgelet",
		ApplicationName:  "edgelet",
		MicroserviceName: "demo",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		State:            "running",
		DesiredState:     "running",
		RuntimeState:     "running",
		ContainerID:      "app-1",
		Generation:       1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	item, err := f.GetRuntimeMicroservice("local-pod-edgelet")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if item["containerId"] != "app-1" {
		t.Fatalf("containerId=%#v", item["containerId"])
	}
	if _, ok := item["podId"]; ok {
		t.Fatalf("expected podId omitted without sandbox, got %#v", item["podId"])
	}
}
