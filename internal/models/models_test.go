package models

import (
	"testing"
)

func TestMicroserviceStateFromText(t *testing.T) {
	tests := []struct {
		input    string
		expected MicroserviceState
	}{
		{"QUEUED", MicroserviceStateQueued},
		{"RUNNING", MicroserviceStateRunning},
		{"STOPPED", MicroserviceStateStopped},
		{"unknown", MicroserviceStateUnknown},
		{"invalid", MicroserviceStateUnknown},
	}

	for _, tt := range tests {
		result := MicroserviceStateFromText(tt.input)
		if result != tt.expected {
			t.Errorf("MicroserviceStateFromText(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestPortMapping(t *testing.T) {
	pm := NewPortMapping(8080, 80, false)
	if pm.Outside != 8080 || pm.Inside != 80 || pm.UDP {
		t.Error("PortMapping creation failed")
	}

	pm2 := NewPortMapping(8080, 80, false)
	if !pm.Equals(pm2) {
		t.Error("PortMapping equality check failed")
	}
}

func TestEnvVar(t *testing.T) {
	ev := NewEnvVar("KEY", "VALUE")
	if ev.Key != "KEY" || ev.Value != "VALUE" {
		t.Error("EnvVar creation failed")
	}

	ev2 := NewEnvVar("KEY", "VALUE")
	if !ev.Equals(ev2) {
		t.Error("EnvVar equality check failed")
	}
}

func TestRoute(t *testing.T) {
	r := NewRoute()
	if len(r.Receivers) != 0 {
		t.Error("Route should start with empty receivers")
	}

	r.SetReceivers([]string{"receiver1", "receiver2"})
	if len(r.Receivers) != 2 {
		t.Error("Route receivers not set correctly")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry(1, "https://registry.example.com", true, "user", "pass", "user@example.com")
	if reg.ID != 1 || reg.URL != "https://registry.example.com" || !reg.IsPublic {
		t.Error("Registry creation failed")
	}
	if reg.Type != RegistryTypeOCI || reg.CAB64 != "" || reg.Insecure {
		t.Errorf("NewRegistry should default to oci, empty CA, insecure=false, got %+v", reg)
	}

	// Test builder
	builder := NewRegistryBuilder()
	reg2 := builder.SetID(2).SetURL("https://registry2.example.com").SetIsPublic(false).Build()
	if reg2.ID != 2 || reg2.URL != "https://registry2.example.com" || reg2.IsPublic {
		t.Error("RegistryBuilder failed")
	}
}

func TestBuiltInLocalRegistries(t *testing.T) {
	if !IsBuiltInLocalRegistryID(BuiltInRegistryDockerIO) ||
		!IsBuiltInLocalRegistryID(BuiltInRegistryFromCache) ||
		!IsBuiltInLocalRegistryID(BuiltInRegistryHuggingFace) {
		t.Fatal("expected ids 1-3 to be built-in")
	}
	if IsBuiltInLocalRegistryID(4) || IsBuiltInLocalRegistryID(0) {
		t.Fatal("expected id 0 and 4 not to be built-in")
	}
	if HighestBuiltInLocalRegistryID() != BuiltInRegistryHuggingFace {
		t.Fatalf("highest built-in id: got %d", HighestBuiltInLocalRegistryID())
	}

	got := BuiltInLocalRegistries()
	if len(got) != 3 {
		t.Fatalf("expected 3 local built-ins, got %d", len(got))
	}
	if got[0].ID != BuiltInRegistryDockerIO || got[0].Type != RegistryTypeOCI || got[0].URL != "docker.io" {
		t.Fatalf("unexpected docker.io row: %+v", got[0])
	}
	if got[1].ID != BuiltInRegistryFromCache || got[1].Type != RegistryTypeOCI || got[1].URL != "from_cache" {
		t.Fatalf("unexpected from_cache row: %+v", got[1])
	}
	if got[2].ID != BuiltInRegistryHuggingFace || got[2].Type != RegistryTypeHF || got[2].URL != DefaultHuggingFaceHubURL {
		t.Fatalf("unexpected Hugging Face row: %+v", got[2])
	}
	if !got[2].IsPublic || got[2].Insecure || got[2].CAB64 != "" {
		t.Fatalf("Hugging Face built-in must be public TLS with system CAs, got %+v", got[2])
	}

	ctrl := BuiltInControllerRegistries()
	if len(ctrl) != 2 {
		t.Fatalf("expected 2 controller built-ins, got %d", len(ctrl))
	}
	for _, reg := range ctrl {
		if reg.ID == BuiltInRegistryHuggingFace {
			t.Fatal("controller built-ins must not include Hugging Face Hub")
		}
	}
}

func TestMicroservice(t *testing.T) {
	ms := NewMicroservice("uuid-123", "image:tag")
	if ms.MicroserviceUUID != "uuid-123" || ms.ImageName != "image:tag" {
		t.Error("Microservice creation failed")
	}

	if err := ms.Validate(); err != nil {
		t.Errorf("Valid microservice should not fail validation: %v", err)
	}

	ms2 := NewMicroservice("", "image:tag")
	if err := ms2.Validate(); err == nil {
		t.Error("Microservice with empty UUID should fail validation")
	}
}

func TestMicroserviceStatus(t *testing.T) {
	ms := NewMicroserviceStatus()
	if ms.Status != MicroserviceStateUnknown {
		t.Error("MicroserviceStatus should start with UNKNOWN state")
	}

	ms.AddExecSessionID("exec-1")
	if len(ms.GetExecSessionIDs()) != 1 {
		t.Error("Exec session ID not added correctly")
	}

	ms.RemoveExecSessionID("exec-1")
	if len(ms.GetExecSessionIDs()) != 0 {
		t.Error("Exec session ID not removed correctly")
	}
}

func TestFieldAgentStatus(t *testing.T) {
	fa := NewFieldAgentStatus()
	if fa.ControllerStatus != ControllerStatusNotConnected {
		t.Error("FieldAgentStatus should start with NOT_CONNECTED")
	}
}

func TestStatusReporterStatus(t *testing.T) {
	sr := NewStatusReporterStatus()
	if sr.SystemTime == 0 || sr.LastUpdate == 0 {
		t.Error("StatusReporterStatus should have non-zero timestamps")
	}
}

func TestExecMessage(t *testing.T) {
	em := NewExecMessage(ExecMessageTypeStdout, []byte("test"), "uuid-123", "exec-1")
	if em.Type != ExecMessageTypeStdout || em.MicroserviceUUID != "uuid-123" {
		t.Error("ExecMessage creation failed")
	}
}

func TestLogMessage(t *testing.T) {
	lm := NewLogMessage(LogMessageTypeLogLine, []byte("log line"), "session-1", "uuid-123", "iofog-123")
	if lm.Type != LogMessageTypeLogLine || lm.SessionID != "session-1" {
		t.Error("LogMessage creation failed")
	}
}

func TestYamlConfig(t *testing.T) {
	yc := NewYamlConfig()
	if yc.Profiles == nil {
		t.Error("YamlConfig profiles should be initialized")
	}

	profile := NewProfileConfig()
	profile.SetProperty("key1", "value1")
	if profile.GetProperty("key1") != "value1" {
		t.Error("ProfileConfig property not set correctly")
	}
}

// Helper function
func stringPtr(s string) *string {
	return &s
}
