package models

import "testing"

func TestRuntimeClassBlockingUUIDs_LocalRunningAndController(t *testing.T) {
	item := &LocalRuntimeClass{Name: "spin", Handler: "spin"}
	item.Normalize()

	localRunning := &LocalDeployedMicroservice{
		LocalUUID:    "local-running",
		RuntimeState: "running",
		ManifestYAML: `apiVersion: edgelet.iofog.org/v1
kind: Microservice
spec:
  container:
    runtime: spin
`,
	}
	localStopped := &LocalDeployedMicroservice{
		LocalUUID:    "local-stopped",
		RuntimeState: "stopped",
		ManifestYAML: `spec:
  container:
    runtime: spin
`,
	}
	runtime := "spin"
	controller := &Microservice{
		MicroserviceUUID: "ctrl-ms",
		Runtime:          &runtime,
	}
	otherRuntime := "wasmedge"
	unrelated := &Microservice{
		MicroserviceUUID: "other-ms",
		Runtime:          &otherRuntime,
	}

	got := RuntimeClassBlockingUUIDs(item, []*LocalDeployedMicroservice{localRunning, localStopped}, []*Microservice{controller, unrelated})
	if len(got) != 2 || got[0] != "ctrl-ms" || got[1] != "local-running" {
		t.Fatalf("expected controller and running local uuids, got %v", got)
	}
}

func TestRuntimeFromManifestYAML(t *testing.T) {
	if got := RuntimeFromManifestYAML("spec:\n  container:\n    runtime: NVIDIA\n"); got != "nvidia" {
		t.Fatalf("expected nvidia, got %q", got)
	}
	if got := RuntimeFromManifestYAML("not yaml: ["); got != "" {
		t.Fatalf("expected empty on invalid yaml, got %q", got)
	}
}
