package fieldagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestPostFogConfig_UsesControllerConfigKeys(t *testing.T) {
	cfg := config.GetInstance()
	cfg.IOFogUUID = "agent-1"
	cfg.NetworkInterface = "eth0"
	cfg.ContainerEngineURL = "unix:///var/run/docker.sock"
	cfg.DiskLimit = 500
	cfg.DiskDirectory = "/var/lib/edgelet/"
	cfg.MemoryLimit = 8192
	cfg.CPULimit = 90
	cfg.LogLimit = 25
	cfg.LogDirectory = "/var/log/edgelet/"
	cfg.LogFileCount = 10
	cfg.StatusFrequency = 10
	cfg.ChangeFrequency = 20
	cfg.LogLevel = "INFO"

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/agent/config" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		ctx:    context.Background(),
		apiClient: &APIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
			jwtManager: auth.GetJWTManager(),
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)

	if err := fa.postFogConfig(); err != nil {
		t.Fatalf("postFogConfig: %v", err)
	}

	assertConfigKeyFloat(t, body, "diskLimit", 500)
	assertConfigKeyFloat(t, body, "memoryLimit", 8192)
	assertConfigKeyFloat(t, body, "cpuLimit", 90)
	assertConfigKeyFloat(t, body, "logLimit", 25)
	if got, ok := body["logDirectory"].(string); !ok || got != "/var/log/edgelet/" {
		t.Fatalf("logDirectory=%v want /var/log/edgelet/", body["logDirectory"])
	}
	for _, legacy := range []string{
		"diskConsumptionLimit",
		"memoryConsumptionLimit",
		"processorConsumptionLimit",
		"logDiskConsumptionLimit",
		"logDiskDirectory",
		"deviceScanFrequency",
	} {
		if _, ok := body[legacy]; ok {
			t.Fatalf("legacy key %q should not be sent", legacy)
		}
	}
}

func TestGetFogConfig_AppliesControllerConfigKeys(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "edgelet-config-wire-*.yaml")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

	content := `currentProfile: default
profiles:
  default:
    diskLimit: "10"
    memoryLimit: "4096"
    cpuLimit: "80"
    logLevel: "INFO"
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp config: %v", err)
	}
	if err := config.LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("load config: %v", err)
	}

	var getCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/config") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			atomic.AddInt32(&getCount, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"diskLimit":    500,
				"memoryLimit":  8192,
				"cpuLimit":     90,
				"logLimit":     25,
				"logDirectory": "/var/log/custom/",
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	cfg.ControllerURL = server.URL
	cfg.IOFogUUID = "agent-wire-test"
	t.Cleanup(func() {
		cfg.ControllerURL = origURL
		cfg.IOFogUUID = origUUID
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		ctx:    ctx,
		apiClient: &APIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
			jwtManager: auth.GetJWTManager(),
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)
	fa.state.SetInitialization(false)

	if err := fa.getFogConfig(); err != nil {
		t.Fatalf("getFogConfig: %v", err)
	}
	if got := atomic.LoadInt32(&getCount); got != 1 {
		t.Fatalf("expected one GET config, got %d", got)
	}
	if cfg.DiskLimit != 500 {
		t.Fatalf("DiskLimit=%v want 500", cfg.DiskLimit)
	}
	if cfg.MemoryLimit != 8192 {
		t.Fatalf("MemoryLimit=%v want 8192", cfg.MemoryLimit)
	}
	if cfg.CPULimit != 90 {
		t.Fatalf("CPULimit=%v want 90", cfg.CPULimit)
	}
	if cfg.LogLimit != 25 {
		t.Fatalf("LogLimit=%v want 25", cfg.LogLimit)
	}
	if cfg.LogDirectory != "/var/log/custom/" {
		t.Fatalf("LogDirectory=%q want /var/log/custom/", cfg.LogDirectory)
	}
}

func TestGetFogConfig_OmitsRemovedDeviceScanFrequency(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "edgelet-config-device-scan-*.yaml")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

	content := `currentProfile: default
profiles:
  default:
    changeFrequency: "20"
    logLevel: "INFO"
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp config: %v", err)
	}
	if err := config.LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("load config: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/config") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"changeFrequency":     30,
				"deviceScanFrequency": 60,
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	cfg.ControllerURL = server.URL
	cfg.IOFogUUID = "agent-wire-device-scan"
	t.Cleanup(func() {
		cfg.ControllerURL = origURL
		cfg.IOFogUUID = origUUID
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		ctx:    ctx,
		apiClient: &APIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
			jwtManager: auth.GetJWTManager(),
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)
	fa.state.SetInitialization(false)

	if err := fa.getFogConfig(); err != nil {
		t.Fatalf("getFogConfig: %v", err)
	}
	if cfg.ChangeFrequency != 30 {
		t.Fatalf("ChangeFrequency=%d want 30", cfg.ChangeFrequency)
	}
}

func TestGetFogConfig_SkipsUnchangedControllerConfig(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "edgelet-config-unchanged-*.yaml")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

	content := `currentProfile: default
profiles:
  default:
    diskLimit: "500"
    memoryLimit: "8192"
    cpuLimit: "90"
    logLimit: "25"
    logDirectory: "/var/log/custom/"
    containerEngine: edgelet
    containerEngineUrl: "` + config.DefaultContainerEngineURLForEngine("edgelet") + `"
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp config: %v", err)
	}
	if err := config.LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("load config: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/config") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"diskLimit":          500,
				"memoryLimit":        8192,
				"cpuLimit":           90,
				"logLimit":           25,
				"logDirectory":       "/var/log/custom/",
				"containerEngineUrl": config.DefaultContainerEngineURLForEngine("edgelet"),
				"changeFrequency":    20,
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	cfg.ControllerURL = server.URL
	cfg.IOFogUUID = "agent-wire-unchanged"
	t.Cleanup(func() {
		cfg.ControllerURL = origURL
		cfg.IOFogUUID = origUUID
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		ctx:    ctx,
		apiClient: &APIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
			jwtManager: auth.GetJWTManager(),
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)
	fa.state.SetInitialization(false)

	if err := fa.getFogConfig(); err != nil {
		t.Fatalf("getFogConfig: %v", err)
	}
	if cfg.ChangeFrequency != 20 {
		t.Fatalf("ChangeFrequency=%d want unchanged 20", cfg.ChangeFrequency)
	}
}

func assertConfigKeyFloat(t *testing.T, body map[string]any, key string, want float64) {
	t.Helper()
	raw, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in body: %#v", key, body)
	}
	switch v := raw.(type) {
	case float64:
		if v != want {
			t.Fatalf("%s=%v want %v", key, v, want)
		}
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			t.Fatalf("%s number parse: %v", key, err)
		}
		if f != want {
			t.Fatalf("%s=%v want %v", key, f, want)
		}
	default:
		t.Fatalf("%s has unexpected type %T", key, raw)
	}
}
