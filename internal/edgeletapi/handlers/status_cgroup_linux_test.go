//go:build linux && cgo

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/cgroups"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestHandleStatus_IncludesCgroupKeysForEmbeddedEdgeletEngine(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	defer buildmeta.SetHasEmbeddedEngineForTest(nil)

	prev := cgroups.GetGlobalPolicy()
	cgroups.SetGlobalPolicy(&cgroups.CgroupPolicy{
		Mode:                 cgroups.ModeV2,
		Driver:               cgroups.DriverSystemd,
		DelegatedControllers: []string{"cpu", "memory", "pids"},
		AgentCgroupPath:      "/edgelet/agent",
		ContainerdCgroupPath: "/edgelet/agent/containerd",
	})
	defer cgroups.SetGlobalPolicy(prev)

	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = constants.EngineEdgelet
	defer func() { cfg.ContainerEngine = originalEngine }()

	handler := &StatusHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/system/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleStatus(rec, req)

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode status payload: %v", err)
	}
	for _, key := range []string{
		"cgroupMode",
		"cgroupDriver",
		"cgroupNested",
		"cgroupDelegatedControllers",
		"cgroupAgentPath",
		"cgroupContainerdPath",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing expected key %q in payload=%v", key, payload)
		}
	}
	if payload["cgroupMode"] != "v2" {
		t.Fatalf("cgroupMode = %q want v2", payload["cgroupMode"])
	}
}
