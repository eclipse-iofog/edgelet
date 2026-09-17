package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type msInspectEnvelope struct {
	Success bool           `json:"success"`
	Data    map[string]any `json:"data"`
}

func TestHandleMicroservices_InspectIncludesCatalogAndWaitStatus(t *testing.T) {
	handler := setupModelAPI(t)
	if err := store.GetInstance().UpsertLocalModel(&models.LocalModel{
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
    permissions: rw
    items:
      - name: test-model
`) + "\n"
	if err := store.GetInstance().UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "ms-catalog-wait",
		ApplicationName:  "edgelet",
		MicroserviceName: "infer",
		SourceName:       "local-cli",
		ManifestYAML:     manifest,
		ImageName:        "nginx:latest",
		State:            "queued",
		DesiredState:     "running",
		RuntimeState:     "queued",
		Generation:       1,
	}); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/ms/ms-catalog-wait", nil)
	rec := httptest.NewRecorder()
	handler.HandleMicroservices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body.String())
	}
	var envelope msInspectEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	catalog, ok := envelope.Data["models"].(map[string]any)
	if !ok {
		t.Fatalf("expected catalog in inspect JSON, got %#v", envelope.Data)
	}
	if catalog["bindPath"] != "/models" || catalog["permissions"] != "rw" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	items, ok := catalog["items"].([]any)
	if !ok {
		t.Fatalf("expected catalog items, got %#v", catalog["items"])
	}
	if len(items) != 1 {
		t.Fatalf("expected one catalog item, got %#v", catalog["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected catalog item object, got %#v", items[0])
	}
	if item["name"] != "test-model" {
		t.Fatalf("expected item name test-model, got %#v", item)
	}
	statusText, ok := envelope.Data["statusText"].(string)
	if !ok {
		t.Fatalf("expected statusText string, got %#v", envelope.Data["statusText"])
	}
	if !strings.Contains(statusText, "test-model") || !strings.Contains(statusText, models.ModelStatePulling) {
		t.Fatalf("expected wait status with model name, got %q", statusText)
	}
}

func TestHandleMicroservices_InspectFailStatusIncludesModelName(t *testing.T) {
	handler := setupModelAPI(t)
	if err := store.GetInstance().UpsertLocalModel(&models.LocalModel{
		Name:       "test-model",
		Source:     models.ModelSourceLocal,
		Repo:       "org/test-model",
		RegistryID: 1,
		State:      models.ModelStateFailed,
		LastError:  "checksum mismatch",
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
	if err := store.GetInstance().UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "ms-catalog-fail",
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

	req := httptest.NewRequest(http.MethodGet, "/v1/ms/ms-catalog-fail", nil)
	rec := httptest.NewRecorder()
	handler.HandleMicroservices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body.String())
	}
	var envelope msInspectEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	statusText, ok := envelope.Data["statusText"].(string)
	if !ok {
		t.Fatalf("expected statusText string, got %#v", envelope.Data["statusText"])
	}
	if !strings.Contains(statusText, "test-model") || !strings.Contains(statusText, "checksum mismatch") {
		t.Fatalf("expected fail status with model name and last error, got %q", statusText)
	}
}
