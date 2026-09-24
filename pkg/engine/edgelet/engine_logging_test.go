//go:build linux

package edgelet

import (
	"errors"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
)

func captureRuntimeEvents(t *testing.T) *[]runtimeops.RuntimeEvent {
	t.Helper()
	events := &[]runtimeops.RuntimeEvent{}
	runtimeops.SetTestSink(func(e runtimeops.RuntimeEvent) {
		*events = append(*events, e)
	})
	t.Cleanup(func() { runtimeops.SetTestSink(nil) })
	return events
}

func TestEmitCRITeardownStep_DebugOrder(t *testing.T) {
	events := captureRuntimeEvents(t)
	e := &Engine{store: newStateStore()}

	steps := []string{"criStopContainer", "criRemoveContainer", "criStopPodSandbox", "criRemovePodSandbox"}
	for _, step := range steps {
		e.emitCRITeardownStep(step, "cid-1", "sandbox-1", time.Now(), nil, false)
	}

	var gotSteps []string
	for i := range *events {
		ev := &(*events)[i]
		if ev.Event != runtimeops.EventEngineContainerRemove {
			continue
		}
		if step, ok := ev.Fields["step"].(string); ok {
			gotSteps = append(gotSteps, step)
		}
	}
	if len(gotSteps) != len(steps) {
		t.Fatalf("steps=%v want %v", gotSteps, steps)
	}
	for i, wantStep := range steps {
		if gotSteps[i] != wantStep {
			t.Fatalf("order[%d]=%q want %q", i, gotSteps[i], wantStep)
		}
	}
	for i := range *events {
		ev := &(*events)[i]
		if ev.Event != runtimeops.EventEngineContainerRemove {
			continue
		}
		if ev.Level != runtimeops.LevelDebug {
			t.Fatalf("step=%v level=%q", ev.Fields["step"], ev.Level)
		}
		if ev.Engine != edgeletEngineName {
			t.Fatalf("engine=%q", ev.Engine)
		}
		if ev.ContainerID != "cid-1" || ev.SandboxID != "sandbox-1" {
			t.Fatalf("ids containerId=%q sandboxId=%q", ev.ContainerID, ev.SandboxID)
		}
		if ev.DurationMs < 0 {
			t.Fatalf("durationMs=%d", ev.DurationMs)
		}
	}
}

func TestEmitCRITeardownStep_ToleratedStopWarn(t *testing.T) {
	events := captureRuntimeEvents(t)
	e := &Engine{store: newStateStore()}
	stopErr := errors.New("already stopped")
	e.emitCRITeardownStep("criStopContainer", "cid-1", "sandbox-1", time.Now(), stopErr, true)

	var got *runtimeops.RuntimeEvent
	for i := range *events {
		if (*events)[i].Fields["step"] == "criStopContainer" {
			got = &(*events)[i]
		}
	}
	if got == nil {
		t.Fatal("expected criStopContainer event")
	}
	if got.Level != runtimeops.LevelWarn {
		t.Fatalf("level=%q", got.Level)
	}
	if got.ReasonCode != runtimeops.ReasonRemoveFailed {
		t.Fatalf("reasonCode=%q", got.ReasonCode)
	}
	if v, ok := got.Fields["tolerated"].(bool); !ok || !v {
		t.Fatalf("tolerated=%v", got.Fields["tolerated"])
	}
}

func TestEmitEngineInit_InfoFields(t *testing.T) {
	events := captureRuntimeEvents(t)
	e := &Engine{}
	e.emitRuntime(runtimeops.RuntimeEvent{
		Event:      runtimeops.EventEngineInit,
		Level:      runtimeops.LevelInfo,
		Message:    "iofog engine initialized",
		Result:     runtimeops.ResultOK,
		DurationMs: 5,
		Fields: map[string]any{
			"socket":    "/run/edgelet/containerd.sock",
			"namespace": "k8s.io",
		},
	})

	if len(*events) != 1 {
		t.Fatalf("events=%d", len(*events))
	}
	ev := (*events)[0]
	if ev.Event != runtimeops.EventEngineInit || ev.Level != runtimeops.LevelInfo {
		t.Fatalf("event=%s level=%s", ev.Event, ev.Level)
	}
	if ev.Engine != edgeletEngineName {
		t.Fatalf("engine=%q", ev.Engine)
	}
	if ev.Fields["socket"] == "" || ev.Fields["namespace"] == "" {
		t.Fatalf("fields=%v", ev.Fields)
	}
}

func TestEmitEngineSuccess_StartFields(t *testing.T) {
	events := captureRuntimeEvents(t)
	e := &Engine{store: newStateStore()}
	e.store.set("cid-1", &containerState{sandboxID: "sandbox-1"})
	e.emitEngineSuccess(runtimeops.EventEngineContainerStart, "cid-1", "sandbox-1", "", "container started", time.Now().Add(-2*time.Millisecond), nil)

	if len(*events) != 1 {
		t.Fatal("expected one event")
	}
	ev := (*events)[0]
	if ev.Event != runtimeops.EventEngineContainerStart {
		t.Fatalf("event=%s", ev.Event)
	}
	if ev.Level != runtimeops.LevelDebug {
		t.Fatalf("level=%s", ev.Level)
	}
	if ev.ContainerID != "cid-1" || ev.SandboxID != "sandbox-1" {
		t.Fatalf("containerId=%q sandboxId=%q", ev.ContainerID, ev.SandboxID)
	}
	if ev.DurationMs < 0 {
		t.Fatalf("durationMs=%d", ev.DurationMs)
	}
}

func TestContainerGone(t *testing.T) {
	if !containerGone(nil) {
		t.Fatal("nil error is gone")
	}
	if !containerGone(errors.New(`rpc error: code = Unknown desc = failed to set removing state for container "abc": container is already in removing state`)) {
		t.Fatal("already removing must be treated as gone")
	}
	if !containerGone(errors.New("container abc: not found")) {
		t.Fatal("not found must be treated as gone")
	}
	if containerGone(errors.New("permission denied")) {
		t.Fatal("unrelated error must not be treated as gone")
	}
}
