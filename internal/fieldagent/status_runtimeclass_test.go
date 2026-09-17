package fieldagent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestGetFogStatus_AppliedRuntimeClassesSortedWithSource(t *testing.T) {
	openFieldAgentTestDB(t)
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineEdgelet
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	for _, rc := range []*models.LocalRuntimeClass{
		{Name: "spin", Handler: "spin", Source: models.RuntimeClassSourceManaged},
		{Name: "nvidia", Handler: "nvidia", Source: models.RuntimeClassSourceLocal},
	} {
		if err := store.GetInstance().UpsertLocalRuntimeClass(rc); err != nil {
			t.Fatalf("upsert %s: %v", rc.Name, err)
		}
	}

	fa := &FieldAgent{config: cfg, state: NewState()}
	status := fa.getFogStatus()
	classes, ok := status["runtimeClasses"].([]models.RuntimeClassStatus)
	if !ok {
		t.Fatalf("runtimeClasses type: %#v", status["runtimeClasses"])
	}
	if len(classes) != 2 {
		t.Fatalf("expected two applied classes, got %#v", classes)
	}
	if classes[0].Name != "nvidia" || classes[0].Handler != "nvidia" || classes[0].Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected nvidia local first, got %#v", classes[0])
	}
	if classes[1].Name != "spin" || classes[1].Handler != "spin" || classes[1].Source != models.RuntimeClassSourceManaged {
		t.Fatalf("expected spin managed second, got %#v", classes[1])
	}

	available, ok := status["availableRuntimes"].([]string)
	if !ok || len(available) == 0 {
		t.Fatalf("availableRuntimes still required, got %#v", status["availableRuntimes"])
	}
	devices, ok := status["availableCdiDevices"].([]string)
	if !ok || devices == nil {
		t.Fatalf("availableCdiDevices must be a slice, got %#v", status["availableCdiDevices"])
	}
}

func TestGetFogStatus_DockerRuntimeClassesAndCDIEmpty(t *testing.T) {
	openFieldAgentTestDB(t)
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	if err := store.GetInstance().UpsertLocalRuntimeClass(&models.LocalRuntimeClass{
		Name:    "spin",
		Handler: "spin",
		Source:  models.RuntimeClassSourceLocal,
	}); err != nil {
		t.Fatalf("upsert class: %v", err)
	}

	fa := &FieldAgent{config: cfg, state: NewState()}
	for _, engineName := range []string{constants.EngineDocker, constants.EnginePodman} {
		cfg.ContainerEngine = engineName
		status := fa.getFogStatus()
		classes, ok := status["runtimeClasses"].([]models.RuntimeClassStatus)
		if !ok || len(classes) != 0 {
			t.Fatalf("%s runtimeClasses want [], got %#v", engineName, status["runtimeClasses"])
		}
		devices, ok := status["availableCdiDevices"].([]string)
		if !ok || len(devices) != 0 {
			t.Fatalf("%s availableCdiDevices want [], got %#v", engineName, status["availableCdiDevices"])
		}
		if !reflect.DeepEqual(status["availableRuntimes"], []string{engineName}) {
			t.Fatalf("%s availableRuntimes=%#v", engineName, status["availableRuntimes"])
		}
	}
}

func TestGetFogStatus_RuntimeClassesJSONNeverNull(t *testing.T) {
	openFieldAgentTestDB(t)
	fa := &FieldAgent{config: config.GetInstance(), state: NewState()}
	status := fa.getFogStatus()
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["runtimeClasses"] == nil {
		t.Fatalf("runtimeClasses must not be null: %s", raw)
	}
	if decoded["availableCdiDevices"] == nil {
		t.Fatalf("availableCdiDevices must not be null: %s", raw)
	}
}
