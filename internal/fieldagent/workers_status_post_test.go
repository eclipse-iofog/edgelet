package fieldagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
)

func TestPostStatusHelper_SuccessfulPostSendsAndClearsTerminalStates(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineDocker
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	sr := statusreporter.GetInstance()
	sr.ResetProcessManagerStatus()
	t.Cleanup(func() { sr.ResetProcessManagerStatus() })
	sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("ms-deleted", models.MicroserviceStateDeleted)
		pm.SetMicroservicesState("ms-running", models.MicroserviceStateRunning)
	})

	var sentStatus map[string]any
	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		postStatusFn: func(_ context.Context, status map[string]any) error {
			sentStatus = status
			return nil
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)

	fa.PostStatusHelper()

	if sentStatus == nil {
		t.Fatal("expected status payload to be posted")
	}
	if !payloadHasMicroserviceState(t, sentStatus, "ms-deleted", "DELETED") {
		t.Fatal("expected posted payload to include deleted microservice state")
	}
	if !reflect.DeepEqual(sentStatus["availableRuntimes"], []string{constants.EngineDocker}) {
		t.Fatalf("expected availableRuntimes=[docker], got: %#v", sentStatus["availableRuntimes"])
	}

	statuses := sr.GetProcessManagerStatus().MicroservicesStatus
	if _, ok := statuses["ms-deleted"]; ok {
		t.Fatal("expected deleted state to be cleared after successful post")
	}
	if _, ok := statuses["ms-running"]; !ok {
		t.Fatal("expected running state to remain after successful post")
	}
}

func TestPostStatusHelper_FailedPostRetainsTerminalStatesForRetry(t *testing.T) {
	sr := statusreporter.GetInstance()
	sr.ResetProcessManagerStatus()
	t.Cleanup(func() { sr.ResetProcessManagerStatus() })
	sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("ms-deleted", models.MicroserviceStateDeleted)
	})

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
		postStatusFn: func(_ context.Context, _ map[string]any) error {
			return errors.New("temporary transport error")
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)

	fa.PostStatusHelper()

	statuses := sr.GetProcessManagerStatus().MicroservicesStatus
	st, ok := statuses["ms-deleted"]
	if !ok {
		t.Fatal("expected deleted state to remain when post fails")
	}
	if st == nil || st.Status != models.MicroserviceStateDeleted {
		t.Fatal("expected deleted state to remain unchanged after failed post")
	}
}

func TestGetFogStatus_AnnotatesDuringRestart(t *testing.T) {
	runtimestate.ResetForTests()
	processmanager.SetQuiesced(true)
	t.Cleanup(func() {
		processmanager.SetQuiesced(false)
		runtimestate.ResetForTests()
	})
	runtimestate.GetState().SetAgentPhase("restarting")

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	if status["runtimeAgentPhase"] != "restarting" {
		t.Fatalf("expected runtimeAgentPhase=restarting, got %#v", status["runtimeAgentPhase"])
	}
	quiesced, ok := status["controlPlaneQuiesced"].(bool)
	if !ok || !quiesced {
		t.Fatalf("expected controlPlaneQuiesced=true, got %#v", status["controlPlaneQuiesced"])
	}
	raw, ok := status["microserviceStatus"].(string)
	if !ok || raw == "[]" {
		return
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse microserviceStatus: %v", err)
	}
	for _, item := range items {
		controlRestart, ok := item["controlRestart"].(bool)
		if !ok || !controlRestart {
			t.Fatalf("expected controlRestart annotation on MS item: %#v", item)
		}
	}
}

func TestGetFogStatus_IncludesHostCapacityKeys(t *testing.T) {
	t.Cleanup(func() {
		statusreporter.GetInstance().UpdateResourceConsumptionManagerStatus(func(s *models.ResourceConsumptionManagerStatus) {
			*s = *models.NewResourceConsumptionManagerStatus()
		})
	})

	statusreporter.GetInstance().UpdateResourceConsumptionManagerStatus(func(s *models.ResourceConsumptionManagerStatus) {
		s.SystemCpus = 8
		s.SystemOs = "linux"
		s.SystemOsVersion = "Debian GNU/Linux 12 (bookworm)"
		s.SystemKernelVersion = "6.1.0-18-amd64"
		s.SystemTotalMemory = 16_000_000_000
		s.TotalDiskSpace = 100_000_000_000
		s.AvailableMemory = 8_000_000_000
		s.AvailableDisk = 40_000_000_000
	})

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()

	if status["systemCpus"] != 8 {
		t.Fatalf("systemCpus: got %#v", status["systemCpus"])
	}
	if status["systemTotalMemory"] != float64(16_000_000_000) {
		t.Fatalf("systemTotalMemory: got %#v", status["systemTotalMemory"])
	}
	if status["systemTotalDisk"] != float64(100_000_000_000) {
		t.Fatalf("systemTotalDisk: got %#v", status["systemTotalDisk"])
	}
	if status["systemOs"] != "linux" {
		t.Fatalf("systemOs: got %#v", status["systemOs"])
	}
	if status["systemOsVersion"] != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("systemOsVersion: got %#v", status["systemOsVersion"])
	}
	if status["systemKernelVersion"] != "6.1.0-18-amd64" {
		t.Fatalf("systemKernelVersion: got %#v", status["systemKernelVersion"])
	}
}

func TestGetFogStatus_AvailableRuntimesPerEngine(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	t.Cleanup(func() {
		cfg.ContainerEngine = originalEngine
	})

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
	}

	cfg.ContainerEngine = constants.EngineDocker
	status := fa.getFogStatus()
	if !reflect.DeepEqual(status["availableRuntimes"], []string{constants.EngineDocker}) {
		t.Fatalf("expected docker runtime list, got: %#v", status["availableRuntimes"])
	}

	cfg.ContainerEngine = constants.EnginePodman
	status = fa.getFogStatus()
	if !reflect.DeepEqual(status["availableRuntimes"], []string{constants.EnginePodman}) {
		t.Fatalf("expected podman runtime list, got: %#v", status["availableRuntimes"])
	}

	cfg.ContainerEngine = constants.EngineEdgelet
	status = fa.getFogStatus()
	available, ok := status["availableRuntimes"].([]string)
	if !ok {
		t.Fatalf("expected []string availableRuntimes, got: %#v", status["availableRuntimes"])
	}
	for _, name := range available {
		if strings.HasSuffix(strings.ToLower(name), "-local") {
			t.Fatalf("expected controller payload runtimes to exclude local variants, got: %#v", available)
		}
	}
}

func TestRuntimeNamesForController_SortsAndDeduplicatesDeterministically(t *testing.T) {
	got := runtimeNamesForController(constants.EngineEdgelet, []string{
		"edgelet",
		"crun",
		"spin",
		"crun",
	})
	want := []string{"crun", "edgelet", "spin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected filtered runtimes: got=%v want=%v", got, want)
	}

	got = runtimeNamesForController(constants.EngineDocker, []string{"runc", "crun"})
	want = []string{"crun", "runc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected sorted non-iofog runtimes, got=%v want=%v", got, want)
	}
}

func payloadHasMicroserviceState(t *testing.T, payload map[string]any, uuid, state string) bool {
	t.Helper()
	raw, ok := payload["microserviceStatus"]
	if !ok {
		return false
	}
	rawJSON, ok := raw.(string)
	if !ok {
		return false
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &items); err != nil {
		t.Fatalf("failed to parse microserviceStatus JSON: %v", err)
	}
	for _, item := range items {
		if item["id"] == uuid && item["status"] == state {
			return true
		}
	}
	return false
}
