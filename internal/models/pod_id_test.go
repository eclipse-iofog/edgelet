package models

import (
	"encoding/json"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestPodIDForEngine_EdgeletUsesSandbox(t *testing.T) {
	got := PodIDForEngine(constants.EngineEdgelet, "app-1", "pause-1")
	if got != "pause-1" {
		t.Fatalf("got %q want pause-1", got)
	}
}

func TestPodIDForEngine_DockerAndPodmanReuseContainerID(t *testing.T) {
	if got := PodIDForEngine(constants.EngineDocker, "cid-1", ""); got != "cid-1" {
		t.Fatalf("docker got %q want cid-1", got)
	}
	if got := PodIDForEngine(constants.EnginePodman, "cid-2", "ignored"); got != "cid-2" {
		t.Fatalf("podman got %q want cid-2", got)
	}
}

func TestPodIDForEngine_MissingSandboxOmitted(t *testing.T) {
	if got := PodIDForEngine(constants.EngineEdgelet, "app-1", ""); got != "" {
		t.Fatalf("expected empty pod id, got %q", got)
	}
	if got := PodIDForEngine(constants.EngineDocker, "", ""); got != "" {
		t.Fatalf("expected empty docker pod id, got %q", got)
	}
}

func TestMicroserviceStatus_PodIDOmittedWhenEmpty(t *testing.T) {
	status := NewMicroserviceStatus()
	status.ContainerID = "app-1"
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["podId"]; ok {
		t.Fatalf("expected podId omitted, got %s", raw)
	}

	status.PodID = "pause-1"
	raw, err = json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["podId"] != "pause-1" {
		t.Fatalf("expected podId pause-1, got %#v", decoded["podId"])
	}
}
