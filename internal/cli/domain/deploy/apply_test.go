package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
)

type applyFakeAPI struct {
	multipartPath  string
	multipartPaths []string
	startResult    map[string]any
	statusCalls    []map[string]any
	statusIndex    int
	modelPulls     int
}

func (f *applyFakeAPI) Request(method, path string, _ any) (map[string]any, error) {
	if method == "POST" && path == "/v1/models:pull" {
		f.modelPulls++
		return map[string]any{"status": "running", "operationId": "model-pull-1", "name": "llama-2-7b-q2k"}, nil
	}
	if method == "GET" && strings.HasPrefix(path, "/v1/models:pull/") {
		return map[string]any{
			"status":      "succeeded",
			"operationId": "model-pull-1",
			"name":        "llama-2-7b-q2k",
			"progress":    100,
		}, nil
	}
	if strings.Contains(path, ":apply/") {
		if f.statusIndex >= len(f.statusCalls) {
			return f.statusCalls[len(f.statusCalls)-1], nil
		}
		resp := f.statusCalls[f.statusIndex]
		f.statusIndex++
		return resp, nil
	}
	return map[string]any{}, nil
}

func (f *applyFakeAPI) RequestMultipartFile(method, path, _, filePath string, fields map[string]string) (map[string]any, error) {
	f.multipartPath = path
	f.multipartPaths = append(f.multipartPaths, path)
	if strings.Contains(path, "/models") {
		if fields["dryRun"] == "true" {
			return map[string]any{"accepted": true, "dryRun": true, "kind": "Model", "name": "test-model"}, nil
		}
		return map[string]any{
			"accepted": true,
			"kind":     "Model",
			"name":     "test-model",
			"model":    map[string]any{"name": "test-model"},
		}, nil
	}
	if fields["dryRun"] == "true" {
		return map[string]any{"valid": true, "kind": "Microservice", "name": "demo", "apiVersion": "v3"}, nil
	}
	return f.startResult, nil
}

func (f *applyFakeAPI) IsDaemonRunning() bool { return true }

func TestExecute_DryRunDoesNotPoll(t *testing.T) {
	manifest := writeManifest(t, "kind: Microservice\napiVersion: v3\nname: demo\n")
	api := &applyFakeAPI{}
	_, err := Execute(context.Background(), api, ui.New(ui.Options{}), Request{
		ManifestPath: manifest,
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(api.multipartPath, ":validate") {
		t.Fatalf("expected validate path, got %q", api.multipartPath)
	}
	if api.statusIndex != 0 {
		t.Fatalf("expected no polling, status calls=%d", api.statusIndex)
	}
}

func TestExecute_MicroserviceApplyCollectsStages(t *testing.T) {
	manifest := writeManifest(t, "kind: Microservice\napiVersion: v3\nname: demo\n")
	api := &applyFakeAPI{
		startResult: map[string]any{"status": "running", "operationId": "op-1"},
		statusCalls: []map[string]any{
			{"status": "running", "stage": "persisting"},
			{"status": "running", "stage": "pulling"},
			{"status": "succeeded", "deploymentId": "dep-99"},
		},
	}
	prevInterval := runtimeClassApplyPollInterval
	runtimeClassApplyPollInterval = time.Millisecond
	t.Cleanup(func() { runtimeClassApplyPollInterval = prevInterval })

	result, err := Execute(context.Background(), api, ui.New(ui.Options{}), Request{ManifestPath: manifest})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Stages) != 2 {
		t.Fatalf("expected stages, got %#v", result.Stages)
	}
	stages, ok := result.Data["stages"].([]any)
	if !ok || len(stages) != 2 {
		t.Fatalf("expected stages array in data, got %#v", result.Data["stages"])
	}
}

func TestExecute_RuntimeClassApplyPollSucceeded(t *testing.T) {
	manifest := writeManifest(t, "kind: RuntimeClass\napiVersion: v3\nname: edgelet\n")
	prevStart := startMultipartApply
	prevStatus := fetchApplyStatus
	prevInterval := runtimeClassApplyPollInterval
	t.Cleanup(func() {
		startMultipartApply = prevStart
		fetchApplyStatus = prevStatus
		runtimeClassApplyPollInterval = prevInterval
	})
	runtimeClassApplyPollInterval = time.Millisecond

	polls := 0
	startMultipartApply = func(api run.EdgeletAPIClient, target Target, _ string, _ map[string]string) (map[string]any, error) {
		if target != TargetRuntimeClasses {
			t.Fatalf("expected runtimeclasses target, got %s", target)
		}
		return map[string]any{"status": "running", "operationId": "op-rc"}, nil
	}
	fetchApplyStatus = func(_ run.EdgeletAPIClient, target Target, operationID string) (map[string]any, error) {
		if target != TargetRuntimeClasses || operationID != "op-rc" {
			t.Fatalf("unexpected status fetch: %s %s", target, operationID)
		}
		polls++
		if polls < 2 {
			return map[string]any{"status": "running", "stage": "reconfiguring"}, nil
		}
		return map[string]any{
			"status": "succeeded",
			"runtimeClass": map[string]any{
				"name":    "edgelet-wasmtime",
				"handler": "edgelet-wasmtime",
			},
		}, nil
	}

	result, err := Execute(context.Background(), &applyFakeAPI{}, ui.New(ui.Options{}), Request{ManifestPath: manifest})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Human, "runtimeclass manifest applied successfully") {
		t.Fatalf("unexpected human output: %s", result.Human)
	}
}

func TestExecute_ModelApplyWaitsForPull(t *testing.T) {
	manifest := writeManifest(t, "kind: Model\napiVersion: edgelet.iofog.org/v1\nmetadata:\n  name: llama-2-7b-q2k\n")
	api := &applyFakeAPI{
		startResult: map[string]any{
			"accepted": true,
			"kind":     "Model",
			"name":     "llama-2-7b-q2k",
			"model":    map[string]any{"name": "llama-2-7b-q2k"},
		},
	}
	result, err := Execute(context.Background(), api, nil, Request{ManifestPath: manifest})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.multipartPath != "/v1/deploy/models:apply" {
		t.Fatalf("expected models apply path, got %q", api.multipartPath)
	}
	if !strings.Contains(result.Human, "model manifest applied successfully") {
		t.Fatalf("unexpected human output: %s", result.Human)
	}
	if api.modelPulls != 1 {
		t.Fatalf("expected 1 model pull after apply, got %d", api.modelPulls)
	}
}

func TestExecute_ModelDryRunDoesNotPull(t *testing.T) {
	manifest := writeManifest(t, "kind: Model\napiVersion: edgelet.iofog.org/v1\nmetadata:\n  name: llama-2-7b-q2k\n")
	api := &applyFakeAPI{
		startResult: map[string]any{
			"accepted": true,
			"dryRun":   true,
			"kind":     "Model",
			"name":     "llama-2-7b-q2k",
		},
	}
	_, err := Execute(context.Background(), api, nil, Request{ManifestPath: manifest, DryRun: true})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.statusIndex != 0 {
		t.Fatalf("expected no apply polling, status calls=%d", api.statusIndex)
	}
	if api.modelPulls != 0 {
		t.Fatalf("expected no model pull on dry-run, got %d", api.modelPulls)
	}
}

func TestDetectTargetFromManifest_Model(t *testing.T) {
	path := writeManifest(t, "kind: Model\napiVersion: edgelet.iofog.org/v1\n")
	target, err := DetectTargetFromManifest(path)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if target != TargetModels {
		t.Fatalf("expected models target, got %q", target)
	}
}

func TestExecute_ModelThenMicroserviceApplyOrder(t *testing.T) {
	manifest := writeManifest(t, strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: infer
spec:
  image: nginx:latest
---
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: test-model
spec:
  repo: org/model
  registry: 1
`)+"\n")
	api := &applyFakeAPI{
		startResult: map[string]any{"status": "succeeded", "deploymentId": "dep-ms"},
	}
	result, err := Execute(context.Background(), api, nil, Request{ManifestPath: manifest})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(api.multipartPaths) < 2 {
		t.Fatalf("expected model apply then microservice apply, got %v", api.multipartPaths)
	}
	if api.multipartPaths[0] != "/v1/deploy/models:apply" {
		t.Fatalf("models must apply first, got %v", api.multipartPaths)
	}
	if !strings.Contains(api.multipartPaths[1], "/v1/deploy/microservices:apply") {
		t.Fatalf("microservices must apply after models, got %v", api.multipartPaths)
	}
	if api.modelPulls != 1 {
		t.Fatalf("expected model pull before microservice apply, got %d", api.modelPulls)
	}
	if !strings.Contains(result.Human, "model manifest applied successfully") {
		t.Fatalf("expected model apply text, got %s", result.Human)
	}
	if !strings.Contains(result.Human, "microservice manifest applied successfully") {
		t.Fatalf("expected microservice apply text, got %s", result.Human)
	}
}

func TestExecute_ModelThenMicroserviceDryRunDoesNotPull(t *testing.T) {
	manifest := writeManifest(t, strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: test-model
spec:
  repo: org/model
  registry: 1
---
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: infer
spec:
  image: nginx:latest
`)+"\n")
	api := &applyFakeAPI{}
	result, err := Execute(context.Background(), api, nil, Request{ManifestPath: manifest, DryRun: true})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if api.modelPulls != 0 {
		t.Fatalf("dry-run must not pull models, got %d", api.modelPulls)
	}
	if len(api.multipartPaths) < 2 {
		t.Fatalf("expected model then microservice dry-run calls, got %v", api.multipartPaths)
	}
	if api.multipartPaths[0] != "/v1/deploy/models:apply" {
		t.Fatalf("models dry-run first, got %v", api.multipartPaths)
	}
	if !strings.Contains(api.multipartPaths[1], ":validate") {
		t.Fatalf("microservice dry-run must validate only, got %v", api.multipartPaths)
	}
	if result.Data["models"] == nil || result.Data["microservices"] == nil {
		t.Fatalf("expected combined dry-run data, got %#v", result.Data)
	}
}

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
