package models

import (
	"encoding/json"
	"testing"
)

func TestProcessManagerStatusPruneMicroserviceStatus(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.SetMicroservicesState("ms-a", MicroserviceStateDeleted)
	pm.SetMicroservicesState("ms-b", MicroserviceStateRunning)

	pm.PruneMicroserviceStatus(func(_ string, status *MicroserviceStatus) bool {
		return status != nil && status.Status == MicroserviceStateDeleted
	})

	if st := pm.GetMicroserviceStatus("ms-a"); st == nil || st.Status != MicroserviceStateUnknown {
		t.Fatal("expected ms-a to be pruned from map")
	}
	if st := pm.GetMicroserviceStatus("ms-b"); st == nil || st.Status != MicroserviceStateRunning {
		t.Fatal("expected ms-b to remain running after prune")
	}
}

func TestProcessManagerStatusClearMicroserviceStatuses(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.SetMicroservicesState("ms-a", MicroserviceStateRunning)
	pm.SetRunningMicroservicesCount(1)

	pm.ClearMicroserviceStatuses()

	if got := len(pm.MicroservicesStatus); got != 0 {
		t.Fatalf("expected empty microservice status map, got len=%d", got)
	}
	if pm.RunningMicroservicesCount != 0 {
		t.Fatalf("expected running count reset to 0, got=%d", pm.RunningMicroservicesCount)
	}
}

func TestProcessManagerStatusPruneMicroserviceStatus_NoOpForNilPredicate(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.SetMicroservicesState("ms-a", MicroserviceStateRunning)

	pm.PruneMicroserviceStatus(nil)

	if got := len(pm.MicroservicesStatus); got != 1 {
		t.Fatalf("expected prune with nil predicate to keep entries, got len=%d", got)
	}
}

func TestProcessManagerStatusPruneMicroserviceStatus_RemovesInvalidEntries(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.SetMicroservicesState("valid-ms", MicroserviceStateRunning)
	pm.MicroservicesStatus[""] = NewMicroserviceStatusWithState(MicroserviceStateRunning)
	pm.MicroservicesStatus["nil-ms"] = nil

	pm.PruneMicroserviceStatus(func(uuid string, status *MicroserviceStatus) bool {
		return uuid == "" || status == nil
	})

	if _, ok := pm.MicroservicesStatus[""]; ok {
		t.Fatal("expected empty uuid entry to be pruned")
	}
	if _, ok := pm.MicroservicesStatus["nil-ms"]; ok {
		t.Fatal("expected nil status entry to be pruned")
	}
	if st, ok := pm.MicroservicesStatus["valid-ms"]; !ok || st == nil || st.Status != MicroserviceStateRunning {
		t.Fatal("expected valid status entry to remain")
	}
}

func TestGetJSONMicroservicesStatus_PodIDNextToContainerID(t *testing.T) {
	pm := NewProcessManagerStatus()
	withPod := NewMicroserviceStatusWithState(MicroserviceStateRunning)
	withPod.ContainerID = "app-1"
	withPod.PodID = "pause-1"
	pm.SetMicroservicesStatus("ms-edgelet", withPod)

	dockerOnly := NewMicroserviceStatusWithState(MicroserviceStateRunning)
	dockerOnly.ContainerID = "cid-1"
	dockerOnly.PodID = "cid-1"
	pm.SetMicroservicesStatus("ms-docker", dockerOnly)

	missing := NewMicroserviceStatusWithState(MicroserviceStateRunning)
	missing.ContainerID = "app-2"
	pm.SetMicroservicesStatus("ms-missing-sandbox", missing)

	raw := pm.GetJSONMicroservicesStatus()
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse: %v raw=%s", err, raw)
	}
	byID := map[string]map[string]any{}
	for _, item := range items {
		id, ok := item["id"].(string)
		if !ok {
			t.Fatalf("expected id string, got %#v", item["id"])
		}
		byID[id] = item
	}
	if byID["ms-edgelet"]["containerId"] != "app-1" || byID["ms-edgelet"]["podId"] != "pause-1" {
		t.Fatalf("edgelet item: %#v", byID["ms-edgelet"])
	}
	if byID["ms-docker"]["podId"] != "cid-1" {
		t.Fatalf("docker item: %#v", byID["ms-docker"])
	}
	if _, ok := byID["ms-missing-sandbox"]["podId"]; ok {
		t.Fatalf("expected missing sandbox to omit podId, got %#v", byID["ms-missing-sandbox"])
	}
}
