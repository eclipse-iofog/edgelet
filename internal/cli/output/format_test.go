package output

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
)

func TestFormatConfigPatchResult_PrintsRejectedKeys(t *testing.T) {
	out := FormatConfigPatchResult(map[string]any{
		"status": "ok",
		"errorMap": map[string]any{
			"a": "invalid",
		},
	})
	if !strings.Contains(out, "rejected keys") || !strings.Contains(out, "a: invalid") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestFormatMutationRoute_SystemReload(t *testing.T) {
	out := formatMutationRoute("/v1/system/reload", map[string]any{"status": "ok"})
	if out != "configuration reloaded successfully" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestFormatEdgeletAPIHuman_StatusOrder(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/system/status", map[string]any{
		"controllerUrl":          "u",
		"connectionToController": "not provisioned",
		"cpuUsage":               "1%",
		"zzzExtra":               "x",
	})
	expectedPrefix := "connectionToController: not provisioned\ncpuUsage: 1%"
	if len(out) < len(expectedPrefix) || out[:len(expectedPrefix)] != expectedPrefix {
		t.Fatalf("unexpected order output: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_StatusIncludesAvailableNetworkInterfacesAfterTotalCPU(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/system/status", map[string]any{
		"systemTotalCpu":             "3200%",
		"availableNetworkInterfaces": "eth0, wlan0",
		"connectionToController":     "ok",
	})
	totalCPULine := "systemTotalCpu: 3200%"
	availableInterfacesLine := "availableNetworkInterfaces: eth0, wlan0"
	totalIdx := strings.Index(out, totalCPULine)
	availableIdx := strings.Index(out, availableInterfacesLine)
	if totalIdx == -1 || availableIdx == -1 {
		t.Fatalf("expected both status lines in output, got: %s", out)
	}
	if availableIdx < totalIdx {
		t.Fatalf("expected available interfaces after systemTotalCpu, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_StatusRuntimeClassesAndCDI(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/system/status", map[string]any{
		"availableRuntimes": "crun, spin",
		"runtimeClasses": []any{
			map[string]any{"name": "nvidia", "handler": "nvidia", "source": "local"},
			map[string]any{"name": "spin", "handler": "spin", "source": "managed"},
		},
		"availableCdiDevices": []any{"nvidia.com/gpu=0", "nvidia.com/gpu=1"},
	})
	if !strings.Contains(out, "availableRuntimes: crun, spin") {
		t.Fatalf("expected availableRuntimes preserved, got: %s", out)
	}
	if !strings.Contains(out, "runtimeClasses: nvidia (nvidia, local), spin (spin, managed)") {
		t.Fatalf("expected formatted runtime classes, got: %s", out)
	}
	if !strings.Contains(out, "availableCdiDevices: nvidia.com/gpu=0, nvidia.com/gpu=1") {
		t.Fatalf("expected joined CDI list, got: %s", out)
	}
	runtimesIdx := strings.Index(out, "availableRuntimes:")
	classesIdx := strings.Index(out, "runtimeClasses:")
	cdiIdx := strings.Index(out, "availableCdiDevices:")
	if runtimesIdx == -1 || classesIdx < runtimesIdx || cdiIdx < classesIdx {
		t.Fatalf("expected new keys appended after availableRuntimes, got: %s", out)
	}
}

func TestFormatProvisionSuccess(t *testing.T) {
	withUUID := FormatProvisionSuccess("abc-123")
	if withUUID != "agent provisioned successfully (uuid: abc-123)" {
		t.Fatalf("unexpected provision message with UUID: %s", withUUID)
	}
	withoutUUID := FormatProvisionSuccess("<unknown>")
	if withoutUUID != "agent provisioned successfully" {
		t.Fatalf("unexpected provision message without UUID: %s", withoutUUID)
	}
}

func TestFormatVersionHuman_DaemonUnavailableFallback(t *testing.T) {
	out := FormatVersionHuman("v1.2.3", "2026-01-01", "abcdef0", nil, errors.New("dial failure"))
	if !strings.Contains(out, "cli.version: v1.2.3") {
		t.Fatalf("expected cli version output, got: %s", out)
	}
	if !strings.Contains(out, "daemon: unavailable") {
		t.Fatalf("expected daemon unavailable fallback, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_MSListHandlesQueryPath(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/ms?source=all", map[string]any{
		"items": []any{
			map[string]any{
				"uuid":        "u1",
				"application": "app",
				"name":        "ms",
				"state":       "running",
				"containerId": "c1",
				"image":       "img:1",
				"type":        "local",
			},
		},
	})
	if !strings.Contains(out, "UUID") || !strings.Contains(out, "u1") {
		t.Fatalf("expected ms table output, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_ImageListColumns(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/images", map[string]any{
		"items": []any{
			map[string]any{
				"repository":       "demo/app",
				"tag":              "1.0",
				"shortId":          "abc123",
				"createdAt":        "2026-01-01T00:00:00Z",
				"diskUsageHuman":   "1.5 GB",
				"contentSizeHuman": "340 MB",
				"inUse":            float64(2),
			},
			map[string]any{
				"repository":       "legacy/app",
				"tag":              "latest",
				"shortId":          "def456",
				"diskUsageHuman":   "100 MB",
				"contentSizeHuman": nil,
				"inUse":            nil,
			},
		},
	})
	for _, want := range []string{"DISK USAGE", "CONTENT SIZE", "IN USE", "1.5 GB", "340 MB", "2", "-"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %s", want, out)
		}
	}
}

func TestFormatEdgeletAPIHuman_MSLifecycleFormatting(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/ms/abc/start", map[string]any{
		"status":           "ok",
		"microserviceUuid": "abc",
		"warning":          "controller reconcile may restart it",
	})
	if !strings.Contains(out, "microservice start completed successfully") {
		t.Fatalf("expected lifecycle success message, got: %s", out)
	}
}

func TestFormatRegistryInspect_HumanReadable(t *testing.T) {
	out := FormatRegistryInspect(map[string]any{
		"id": 3, "url": "registry.example.com", "isPublic": false,
		"userName": "john", "userEmail": "john@example.com", "password": "s3cr3t",
		"type": "hf", "insecure": true, "ca": "should-not-print",
	}, false)
	if !strings.Contains(out, "TYPE: hf") || !strings.Contains(out, "INSECURE: true") {
		t.Fatalf("expected type and insecure in inspect output, got: %s", out)
	}
	if strings.Contains(out, "should-not-print") || strings.Contains(strings.ToLower(out), "ca:") {
		t.Fatalf("inspect must not print secrets, got: %s", out)
	}
	expectedB64 := base64.StdEncoding.EncodeToString([]byte("s3cr3t"))
	if !strings.Contains(out, "PASSWORD_B64: "+expectedB64) {
		t.Fatalf("expected PASSWORD_B64 output, got: %s", out)
	}
}

func TestFormatLogEntries_PreservesDockerStyleSpacing(t *testing.T) {
	out := FormatLogEntries(map[string]any{
		"entries": []any{
			map[string]any{"line": "line1\n"},
			map[string]any{"line": "\n"},
			map[string]any{"line": "line3\n"},
		},
	}, false)
	if out != "line1\n\nline3\n" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestFormatEdgeletAPIHuman_ModelListColumns(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/models", map[string]any{
		"items": []any{
			map[string]any{
				"name": "llama-2-7b-q2k", "repo": "second-state/Llama-2-7B-Chat-GGUF",
				"revision": "main", "registryId": 5, "state": "Ready", "format": "gguf",
			},
		},
	})
	for _, want := range []string{"NAME", "SOURCE", "REPO", "STATE", "llama-2-7b-q2k", "Ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in model list, got: %s", want, out)
		}
	}
}

func TestFormatEdgeletAPIHuman_ModelListSourceColumn(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/models", map[string]any{
		"items": []any{
			map[string]any{
				"name": "operator-model", "source": "local", "repo": "org/local",
				"revision": "main", "registryId": 1, "state": "Ready", "format": "gguf",
			},
			map[string]any{
				"name": "fleet-model", "source": "managed", "repo": "org/fleet",
				"revision": "main", "registryId": 5, "state": "Pulling", "format": "gguf",
			},
		},
	})
	if !strings.Contains(out, "SOURCE") || !strings.Contains(out, "local") || !strings.Contains(out, "managed") {
		t.Fatalf("expected source column with local and managed rows, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_ModelInspectSourceAndUUID(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/models/fleet-model", map[string]any{
		"name": "fleet-model", "source": "managed", "uuid": "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		"bindRefCount": 2, "state": "Ready", "repo": "org/fleet",
	})
	for _, want := range []string{"source: managed", "uuid: 3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f", "bindRefCount: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in model inspect, got: %s", want, out)
		}
	}
}

func TestFormatEdgeletAPIHuman_MSInspectFullFallsBackToJSON(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/ms/ms-1", map[string]any{
		"uuid": "ms-1", "name": "infer", "state": "queued",
		"statusText": "waiting for model download: test-model (Pulling)",
		"models": map[string]any{
			"bindPath":    "/models",
			"permissions": "ro",
			"items":       []any{map[string]any{"name": "test-model"}},
		},
		"raw": map[string]any{"engineInspect": map[string]any{"id": "ctr"}},
	})
	if out != "" {
		t.Fatalf("full inspect must leave human empty for JSON fallback, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_MSInspectSummaryCard(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/ms/ms-1", map[string]any{
		"uuid": "ms-1", "name": "infer", "state": "queued",
		"statusText": "waiting for model download: test-model (Pulling)",
		"models": map[string]any{
			"bindPath":    "/models",
			"permissions": "ro",
			"items":       []any{map[string]any{"name": "test-model"}},
		},
	})
	for _, want := range []string{"uuid: ms-1", "models.bindPath: /models", "models.permissions: ro", "models.items: test-model", "test-model"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in summary inspect, got: %s", want, out)
		}
	}
}

func TestFormatEdgeletAPIHuman_MSInspectDurabilityKeys(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/ms/ms-1", map[string]any{
		"uuid":         "ms-1",
		"name":         "infer",
		"state":        "running",
		"errorMessage": "crash",
		"lastError":    "crash",
		"lastErrorAt":  int64(1726660000123),
		"restartCount": 4,
	})
	for _, want := range []string{
		"errorMessage: crash",
		"lastError: crash",
		"lastErrorAt: 1726660000123",
		"restartCount: 4",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in inspect, got: %s", want, out)
		}
	}
	errIdx := strings.Index(out, "errorMessage:")
	lastIdx := strings.Index(out, "lastError:")
	atIdx := strings.Index(out, "lastErrorAt:")
	if errIdx < 0 || lastIdx < errIdx || atIdx < lastIdx {
		t.Fatalf("expected errorMessage then lastError then lastErrorAt, got: %s", out)
	}
}

func TestFormatEdgeletAPIHuman_RegistryListShowsType(t *testing.T) {
	out := FormatEdgeletAPIHuman("/v1/deploy/registries", map[string]any{
		"items": []any{
			map[string]any{"id": 5, "url": "https://huggingface.co", "type": "hf", "insecure": false, "isPublic": true},
		},
	})
	for _, want := range []string{"TYPE", "INSECURE", "hf"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in registry list, got: %s", want, out)
		}
	}
	if strings.Contains(out, "password") {
		t.Fatalf("registry list must not print secrets, got: %s", out)
	}
}

func TestFormatDeployApplyResult_KindSpecific(t *testing.T) {
	msOut := formatDeployApplyResult(map[string]any{
		"accepted": true, "kind": "Microservice", "deploymentId": "dep-1",
	})
	if !strings.Contains(msOut, "microservice manifest applied successfully") {
		t.Fatalf("expected microservice apply output, got: %s", msOut)
	}
}

func TestFormatDeployStageLine(t *testing.T) {
	if got := ui.FormatDeployStageLine("pulling"); !strings.Contains(got, "(pulling)") {
		t.Fatalf("expected stage in progress line, got: %s", got)
	}
}
