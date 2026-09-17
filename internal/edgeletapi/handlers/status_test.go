package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestAugmentWithDNSStatusAddsKeys(t *testing.T) {
	m := map[string]any{
		"edgeletDaemon": "running",
	}
	augmentWithDNSStatus(m)

	requiredKeys := []string{
		"dnsStarted",
		"dnsCompatAliasesEnabled",
		"dnsRateLimitEnabled",
		"dnsRateLimitRPS",
		"dnsRateLimitBurst",
		"dnsMaxRequestBytes",
		"dnsMaxQNameBytes",
		"dnsScopeManagedListening",
		"dnsScopeManagedAddress",
		"dnsQueriesTotal",
		"dnsSuccessTotal",
		"dnsNXDomainTotal",
		"dnsServFailTotal",
		"dnsPolicyDeniedTotal",
		"dnsInactiveTotal",
		"dnsForwardedTotal",
		"dnsForwardErrTotal",
		"dnsForwardingDegraded",
		"dnsForwardTotalUpstream",
		"dnsForwardHealthyUpstream",
		"dnsForwardLastSuccessUnix",
		"dnsForwardLastFailureUnix",
		"dnsForwardBackoffSkipTotal",
		"dnsRateLimitedTotal",
		"dnsRejectedTotal",
		"dnsHealth",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing expected key %q", key)
		}
	}
}

func TestHandleStatus_ExcludesDNSKeysWithoutEmbeddedEdgeletEngine(t *testing.T) {
	embedded := false
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	defer buildmeta.SetHasEmbeddedEngineForTest(nil)

	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	if _, ok := payload["dnsStarted"]; ok {
		t.Fatalf("expected dnsStarted to be absent without embedded edgelet engine, payload=%v", payload)
	}
}

func TestHandleStatus_IncludesDNSKeysForEmbeddedEdgeletEngine(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	defer buildmeta.SetHasEmbeddedEngineForTest(nil)

	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineEdgelet
	defer func() { cfg.ContainerEngine = originalEngine }()

	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	if _, ok := payload["dnsStarted"]; !ok {
		t.Fatalf("expected dnsStarted to be present for embedded edgelet engine, payload=%v", payload)
	}
}

func TestHandleStatus_IncludesAvailableNetworkInterfaces(t *testing.T) {
	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	if _, ok := payload["availableNetworkInterfaces"]; !ok {
		t.Fatalf("expected availableNetworkInterfaces key in status payload, payload=%v", payload)
	}
}

func TestHandleStatus_IncludesAvailableRuntimes(t *testing.T) {
	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	if _, ok := payload["availableRuntimes"]; !ok {
		t.Fatalf("expected availableRuntimes key in status payload, payload=%v", payload)
	}
	if _, ok := payload["runtimeClasses"].([]any); !ok {
		t.Fatalf("expected runtimeClasses JSON array, payload=%v", payload)
	}
	if _, ok := payload["availableCdiDevices"].([]any); !ok {
		t.Fatalf("expected availableCdiDevices JSON array, payload=%v", payload)
	}
}

func TestHandleStatus_RuntimeClassesAndCDIAreJSONArrays(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineDocker
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })

	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	classes, ok := payload["runtimeClasses"].([]any)
	if !ok {
		t.Fatalf("runtimeClasses type: %#v", payload["runtimeClasses"])
	}
	if len(classes) != 0 {
		t.Fatalf("docker runtimeClasses want [], got %#v", classes)
	}
	devices, ok := payload["availableCdiDevices"].([]any)
	if !ok {
		t.Fatalf("availableCdiDevices type: %#v", payload["availableCdiDevices"])
	}
	if len(devices) != 0 {
		t.Fatalf("docker CDI devices want [], got %#v", devices)
	}
	if _, isString := payload["runtimeClasses"].(string); isString {
		t.Fatal("runtimeClasses must not be a joined string")
	}
}
