package runtimeapi

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestFacadeGetRuntimeMicroservice_CatalogAndWaitStatus(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	if err := f.db.UpsertLocalModel(&models.LocalModel{
		Name:       "test-model",
		Source:     models.ModelSourceLocal,
		Repo:       "org/test-model",
		RegistryID: 1,
		State:      models.ModelStatePulling,
	}); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	manifest := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: infer
spec:
  image: nginx:latest
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: test-model
`) + "\n"
	if err := f.db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-inspect-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "infer",
		SourceName:       "local-cli",
		ManifestYAML:     manifest,
		ImageName:        "nginx:latest",
		State:            "queued",
		DesiredState:     "running",
		RuntimeState:     "queued",
		LastError:        models.CatalogWaitingMessage,
		Generation:       1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	item, err := f.GetRuntimeMicroservice("local-inspect-1")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	catalog, ok := item["models"].(map[string]any)
	if !ok {
		t.Fatalf("expected catalog on inspect, got %#v", item)
	}
	if catalog["bindPath"] != "/models" || catalog["permissions"] != "ro" {
		t.Fatalf("unexpected catalog fields: %#v", catalog)
	}
	items, ok := catalog["items"].([]map[string]any)
	if !ok || len(items) != 1 || items[0]["name"] != "test-model" {
		t.Fatalf("expected catalog item names, got %#v", catalog["items"])
	}
	statusText, ok := item["statusText"].(string)
	if !ok {
		t.Fatalf("expected statusText string, got %#v", item["statusText"])
	}
	if !strings.Contains(statusText, "test-model") || !strings.Contains(statusText, models.ModelStatePulling) {
		t.Fatalf("expected wait status with model name and state, got %q", statusText)
	}
}

func TestFacadeGetRuntimeMicroservice_CatalogFailStatus(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	if err := f.db.UpsertLocalModel(&models.LocalModel{
		Name:       "test-model",
		Source:     models.ModelSourceLocal,
		Repo:       "org/test-model",
		RegistryID: 1,
		State:      models.ModelStateFailed,
		LastError:  "pull denied",
	}); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	manifest := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: infer
spec:
  image: nginx:latest
  models:
    bindPath: /models
    items:
      - name: test-model
`) + "\n"
	if err := f.db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-inspect-fail",
		ApplicationName:  "edgelet",
		MicroserviceName: "infer",
		SourceName:       "local-cli",
		ManifestYAML:     manifest,
		ImageName:        "nginx:latest",
		State:            "failed",
		DesiredState:     "running",
		RuntimeState:     "failed",
		Generation:       1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	item, err := f.GetRuntimeMicroservice("local-inspect-fail")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	statusText, ok := item["statusText"].(string)
	if !ok {
		t.Fatalf("expected statusText string, got %#v", item["statusText"])
	}
	if !strings.Contains(statusText, "test-model") || !strings.Contains(statusText, "pull denied") {
		t.Fatalf("expected fail status with model name and last error, got %q", statusText)
	}
}

func TestAttachDurabilityInspect_OmitemptyAndFallback(t *testing.T) {
	item := map[string]any{"uuid": "ms-1"}
	attachDurabilityInspect(item, nil, "", 0)
	if _, ok := item["lastError"]; ok {
		t.Fatalf("expected empty lastError omitted, got %#v", item)
	}
	if _, ok := item["restartCount"]; ok {
		t.Fatalf("expected zero restartCount omitted, got %#v", item)
	}

	attachDurabilityInspect(item, nil, "db crash", 3)
	if item["lastError"] != "db crash" || item["restartCount"] != 3 {
		t.Fatalf("expected DB fallback, got %#v", item)
	}

	empty := ""
	status := &models.MicroserviceStatus{
		ErrorMessage: &empty,
		LastError:    "reporter crash",
		LastErrorAt:  1726660000123,
		RestartCount: 4,
	}
	attachDurabilityInspect(item, status, "db crash", 3)
	if item["lastError"] != "reporter crash" {
		t.Fatalf("expected reporter lastError to win, got %#v", item["lastError"])
	}
	if item["lastErrorAt"] != int64(1726660000123) {
		t.Fatalf("expected lastErrorAt, got %#v", item["lastErrorAt"])
	}
	if item["restartCount"] != 4 {
		t.Fatalf("expected reporter restartCount, got %#v", item["restartCount"])
	}
	gotErr, ok := item["errorMessage"].(*string)
	if !ok || gotErr == nil || *gotErr != "" {
		t.Fatalf("expected explicit empty errorMessage pointer, got %#v", item["errorMessage"])
	}
}

func TestFacadeGetRuntimeMicroservice_LocalDurabilityFromDB(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.sr.ResetProcessManagerStatus()
	t.Cleanup(func() { f.sr.ResetProcessManagerStatus() })

	if err := f.db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-durability",
		ApplicationName:  "edgelet",
		MicroserviceName: "keep",
		SourceName:       "local-cli",
		ImageName:        "nginx:latest",
		ManifestYAML: `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: keep
spec:
  image: nginx:latest
`,
		State:        "running",
		DesiredState: "running",
		RuntimeState: "running",
		LastError:    "previous crash",
		RestartCount: 2,
		Generation:   1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	item, err := f.GetRuntimeMicroservice("local-durability")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if item["lastError"] != "previous crash" {
		t.Fatalf("expected lastError from sqlite, got %#v", item["lastError"])
	}
	if item["restartCount"] != 2 {
		t.Fatalf("expected restartCount from sqlite, got %#v", item["restartCount"])
	}
	if _, ok := item["lastErrorAt"]; ok {
		t.Fatalf("expected lastErrorAt omitted when reporter is empty, got %#v", item["lastErrorAt"])
	}
}
