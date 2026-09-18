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

func TestGetJSONMicroservicesStatus_DurabilityFieldsOmitempty(t *testing.T) {
	pm := NewProcessManagerStatus()
	empty := NewMicroserviceStatusWithState(MicroserviceStateRunning)
	empty.ContainerID = "cid-empty"
	pm.SetMicroservicesStatus("ms-empty", empty)

	full := NewMicroserviceStatusWithState(MicroserviceStateRunning)
	full.ContainerID = "cid-full"
	msg := ""
	full.ErrorMessage = &msg
	full.LastError = "exitCode=1"
	full.LastErrorAt = 1726660000123
	full.RestartCount = 4
	pm.SetMicroservicesStatus("ms-full", full)

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
	if _, ok := byID["ms-empty"]["lastError"]; ok {
		t.Fatalf("expected empty lastError omitted, got %#v", byID["ms-empty"])
	}
	if _, ok := byID["ms-empty"]["lastErrorAt"]; ok {
		t.Fatalf("expected zero lastErrorAt omitted, got %#v", byID["ms-empty"])
	}
	if _, ok := byID["ms-empty"]["restartCount"]; ok {
		t.Fatalf("expected zero restartCount omitted, got %#v", byID["ms-empty"])
	}
	if byID["ms-full"]["errorMessage"] != "" {
		t.Fatalf("expected explicit empty errorMessage, got %#v", byID["ms-full"]["errorMessage"])
	}
	if byID["ms-full"]["lastError"] != "exitCode=1" {
		t.Fatalf("expected lastError, got %#v", byID["ms-full"])
	}
	if byID["ms-full"]["lastErrorAt"] != float64(1726660000123) {
		t.Fatalf("expected lastErrorAt, got %#v", byID["ms-full"]["lastErrorAt"])
	}
	if byID["ms-full"]["restartCount"] != float64(4) {
		t.Fatalf("expected restartCount 4, got %#v", byID["ms-full"]["restartCount"])
	}
}

func TestSetMicroservicesStatusErrorMessage_RecordsLastErrorExceptQueued(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.SetMicroservicesState("ms-fail", MicroserviceStateFailed)
	pm.SetMicroservicesStatusErrorMessage("ms-fail", "task failed")
	failed := pm.GetMicroserviceStatus("ms-fail")
	if failed.LastError != "task failed" || failed.LastErrorAt == 0 {
		t.Fatalf("expected failure to record lastError, got %+v", failed)
	}

	pm.SetMicroservicesState("ms-wait", MicroserviceStateQueued)
	pm.SetMicroservicesStatusErrorMessage("ms-wait", CatalogWaitingMessage)
	waiting := pm.GetMicroserviceStatus("ms-wait")
	if waiting.LastError != "" || waiting.LastErrorAt != 0 {
		t.Fatalf("expected queued wait text not to record lastError, got %+v", waiting)
	}
}

func TestResetMicroservicesRestartCount_KeepsLastError(t *testing.T) {
	pm := NewProcessManagerStatus()
	st := NewMicroserviceStatusWithState(MicroserviceStateFailed)
	st.LastError = "crash"
	st.LastErrorAt = 99
	st.RestartCount = 7
	pm.SetMicroservicesStatus("ms-1", st)

	pm.ResetMicroservicesRestartCount("ms-1")
	got := pm.GetMicroserviceStatus("ms-1")
	if got.RestartCount != 0 {
		t.Fatalf("expected restartCount 0, got %d", got.RestartCount)
	}
	if got.LastError != "crash" || got.LastErrorAt != 99 {
		t.Fatalf("expected lastError kept, got %+v", got)
	}
}

func TestIncrementMicroservicesRestartCount(t *testing.T) {
	pm := NewProcessManagerStatus()
	pm.IncrementMicroservicesRestartCount("ms-1")
	pm.IncrementMicroservicesRestartCount("ms-1")
	got := pm.GetMicroserviceStatus("ms-1")
	if got.RestartCount != 2 {
		t.Fatalf("expected restartCount 2, got %d", got.RestartCount)
	}
}
