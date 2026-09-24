package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type deployModelApplyData struct {
	Accepted bool             `json:"accepted"`
	Kind     string           `json:"kind"`
	Name     string           `json:"name"`
	DryRun   bool             `json:"dryRun"`
	Model    map[string]any   `json:"model"`
	Pulls    []map[string]any `json:"pulls"`
}

type deployModelApplyEnvelope struct {
	Success bool                 `json:"success"`
	Data    deployModelApplyData `json:"data"`
}

type modelPullStartData struct {
	OperationID string `json:"operationId"`
}

type modelPullStartEnvelope struct {
	Data modelPullStartData `json:"data"`
}

type modelPullStatusData struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

type modelPullStatusEnvelope struct {
	Data modelPullStatusData `json:"data"`
}

type modelInspectData struct {
	State        string `json:"state"`
	Name         string `json:"name"`
	Source       string `json:"source"`
	UUID         string `json:"uuid"`
	BindRefCount int    `json:"bindRefCount"`
}

type modelInspectEnvelope struct {
	Data modelInspectData `json:"data"`
}

type testModelPuller struct{}

func (s *testModelPuller) Pull(_ context.Context, req modelpull.Request) (*modelpull.Result, error) {
	content := modelpull.ContentDir(req.ModelsRoot, req.Name)
	if err := os.MkdirAll(content, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(content, "weights.bin"), []byte("ok"), 0o644); err != nil {
		return nil, err
	}
	manifestPath := modelpull.ManifestPath(req.ModelsRoot, req.Name)
	onDisk := modelpull.OnDiskManifest{
		MetadataName:      req.Name,
		RegistryID:        req.Registry.ID,
		RegistryType:      req.Registry.NormalizedType(),
		Repo:              req.Repo,
		RequestedRevision: req.Revision,
		ResolvedRevision:  "resolved-rev",
		Digest:            "sha256:abc",
		Files:             req.Files,
		ContentPaths:      []string{"content/weights.bin"},
		TotalBytes:        2,
	}
	if err := modelpull.WriteOnDiskManifest(manifestPath, onDisk); err != nil {
		return nil, err
	}
	return &modelpull.Result{
		Digest:           onDisk.Digest,
		ResolvedRevision: onDisk.ResolvedRevision,
		TotalBytes:       2,
		ManifestPath:     manifestPath,
		ContentPath:      content,
		ContentPaths:     onDisk.ContentPaths,
		Format:           req.FormatHint,
	}, nil
}

func setupModelAPI(t *testing.T) *EdgeletAPIHandler {
	t.Helper()
	db := store.GetInstance()
	_ = db.Close()
	diskDir := t.TempDir()
	if err := db.Open(filepath.Join(diskDir, "db")); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("seed default registries: %v", err)
	}
	hf := models.NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(models.RegistryTypeHF).Build()
	if err := db.UpsertLocalRegistry(hf); err != nil {
		t.Fatalf("upsert hf registry: %v", err)
	}

	cfg := setupConfigForGPSTests(t)
	cfg.DiskDirectory = diskDir

	handler := NewEdgeletAPIHandler()
	mm := modelmanager.New(db, modelpull.Root(diskDir))
	mm.SetPullers(&testModelPuller{}, &testModelPuller{})
	handler.facade.SetModelManager(mm)
	return handler
}

func validModelManifest() string {
	return `
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: llama-2-7b-q2k
spec:
  repo: second-state/Llama-2-7B-Chat-GGUF
  revision: 064fe43ea8c1e1f93477ef4a170bdc2b244ef02c
  registry: 5
  files:
    - llama-2-7b-chat.Q5_K_M.gguf
  format: gguf
`
}

func TestHandleDeployModelsApply_WritesRow(t *testing.T) {
	handler := setupModelAPI(t)
	req := newManifestMultipartRequest(t, "/v1/deploy/models:apply", validModelManifest(), nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployModelsApply(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope deployModelApplyEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Success || !envelope.Data.Accepted || envelope.Data.Kind != "Model" || envelope.Data.Name != "llama-2-7b-q2k" {
		t.Fatalf("unexpected apply payload: %#v", envelope.Data)
	}
	if len(envelope.Data.Pulls) != 1 {
		t.Fatalf("expected apply to start a pull, got %#v", envelope.Data.Pulls)
	}
	opID, ok := envelope.Data.Pulls[0]["operationId"].(string)
	if !ok || strings.TrimSpace(opID) == "" {
		t.Fatalf("expected apply to start a pull, got %#v", envelope.Data.Pulls)
	}
	row, err := store.GetInstance().GetLocalModel("llama-2-7b-q2k")
	if err != nil {
		t.Fatalf("expected local model row: %v", err)
	}
	if row.RegistryID != 5 || row.Repo != "second-state/Llama-2-7B-Chat-GGUF" {
		t.Fatalf("unexpected stored model: %+v", row)
	}
	waitModelInspectState(t, handler, "llama-2-7b-q2k", models.ModelStateReady)
}

func TestHandleModelsPull_AsyncReachesReady(t *testing.T) {
	handler := setupModelAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/models:apply", validModelManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployModelsApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply failed: %d %s", applyRec.Code, applyRec.Body.String())
	}

	pullReq := httptest.NewRequest(http.MethodPost, "/v1/models:pull", strings.NewReader(`{"name":"llama-2-7b-q2k"}`))
	pullReq.Header.Set("Content-Type", "application/json")
	pullRec := httptest.NewRecorder()
	handler.HandleModelPull(pullRec, pullReq)
	if pullRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", pullRec.Code, pullRec.Body.String())
	}
	var start modelPullStartEnvelope
	if err := json.Unmarshal(pullRec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode pull start: %v", err)
	}
	if strings.TrimSpace(start.Data.OperationID) == "" {
		t.Fatalf("expected operationId, got %s", pullRec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/models:pull/"+start.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleModelPullStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("status: %d %s", statusRec.Code, statusRec.Body.String())
		}
		var envelope modelPullStatusEnvelope
		if err := json.Unmarshal(statusRec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		status = envelope.Data.Status
		if status == "succeeded" {
			break
		}
		if status == "failed" {
			t.Fatalf("pull failed: %s", envelope.Data.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("expected succeeded pull, got %q", status)
	}

	inspectReq := httptest.NewRequest(http.MethodGet, "/v1/models/llama-2-7b-q2k", nil)
	inspectRec := httptest.NewRecorder()
	handler.HandleModels(inspectRec, inspectReq)
	if inspectRec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", inspectRec.Code, inspectRec.Body.String())
	}
	var inspect modelInspectEnvelope
	if err := json.Unmarshal(inspectRec.Body.Bytes(), &inspect); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if inspect.Data.State != models.ModelStateReady || inspect.Data.Name != "llama-2-7b-q2k" {
		t.Fatalf("expected Ready model, got %#v", inspect.Data)
	}
}

func TestHandleDeployModelsValidate_RejectsInvalidFixtures(t *testing.T) {
	handler := setupModelAPI(t)
	fixtures, err := filepath.Glob(filepath.Join("testdata", "invalid-model-*.yaml"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("expected invalid model fixtures")
	}
	for _, path := range fixtures {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		req := newManifestMultipartRequest(t, "/v1/deploy/models:validate", string(raw), nil)
		rec := httptest.NewRecorder()
		handler.HandleDeployModelsValidate(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") {
			t.Fatalf("%s: expected invalid argument, got %s", path, rec.Body.String())
		}
	}
}

func TestHandleModels_ListInspectPruneRemove(t *testing.T) {
	handler := setupModelAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/models:apply", validModelManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployModelsApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", applyRec.Code, applyRec.Body.String())
	}
	waitModelInspectState(t, handler, "llama-2-7b-q2k", models.ModelStateReady)

	listReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	listRec := httptest.NewRecorder()
	handler.HandleModels(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), "llama-2-7b-q2k") {
		t.Fatalf("expected model in list: %s", listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), `"source":"local"`) {
		t.Fatalf("expected local source in list: %s", listRec.Body.String())
	}

	inspectReq := httptest.NewRequest(http.MethodGet, "/v1/models/llama-2-7b-q2k", nil)
	inspectRec := httptest.NewRecorder()
	handler.HandleModels(inspectRec, inspectReq)
	if inspectRec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", inspectRec.Code, inspectRec.Body.String())
	}
	var inspect modelInspectEnvelope
	if err := json.Unmarshal(inspectRec.Body.Bytes(), &inspect); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if inspect.Data.Source != models.ModelSourceLocal {
		t.Fatalf("expected local source on inspect, got %#v", inspect.Data)
	}

	rmReq := httptest.NewRequest(http.MethodDelete, "/v1/models/llama-2-7b-q2k", nil)
	rmRec := httptest.NewRecorder()
	handler.HandleModels(rmRec, rmReq)
	if rmRec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rmRec.Code, rmRec.Body.String())
	}

	pruneReq := httptest.NewRequest(http.MethodPost, "/v1/models:prune?mode=dangling", nil)
	pruneRec := httptest.NewRecorder()
	handler.HandleModelPrune(pruneRec, pruneReq)
	if pruneRec.Code != http.StatusOK {
		t.Fatalf("prune: %d %s", pruneRec.Code, pruneRec.Body.String())
	}

	missing := httptest.NewRequest(http.MethodGet, "/v1/models/llama-2-7b-q2k", nil)
	missingRec := httptest.NewRecorder()
	handler.HandleModels(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after remove, got %d %s", missingRec.Code, missingRec.Body.String())
	}
}

func waitModelInspectState(t *testing.T, handler *EdgeletAPIHandler, name, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+name, nil)
		rec := httptest.NewRecorder()
		handler.HandleModels(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("inspect %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var inspect modelInspectEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &inspect); err != nil {
			t.Fatalf("decode inspect: %v", err)
		}
		state = inspect.Data.State
		if state == want {
			return
		}
		if state == models.ModelStateFailed && want != models.ModelStateFailed {
			t.Fatalf("model %s failed while waiting for %s", name, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for model %s state %s, last=%q", name, want, state)
}

func TestHandleDeployModelsApply_DryRunDoesNotPersistOrPull(t *testing.T) {
	handler := setupModelAPI(t)
	req := newManifestMultipartRequest(t, "/v1/deploy/models:apply", validModelManifest(), map[string]string{"dryRun": "true"})
	rec := httptest.NewRecorder()
	handler.HandleDeployModelsApply(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope deployModelApplyEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Data.DryRun || len(envelope.Data.Pulls) != 0 {
		t.Fatalf("dry-run should not start pulls: %#v", envelope.Data)
	}
	_, err := store.GetInstance().GetLocalModel("llama-2-7b-q2k")
	if err == nil {
		t.Fatal("expected no persisted model on dry-run")
	}
}

func TestHandleModelPull_DetachedFromRequestContext(t *testing.T) {
	handler := setupModelAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/models:apply", validModelManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployModelsApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply failed: %d %s", applyRec.Code, applyRec.Body.String())
	}
	waitModelInspectState(t, handler, "llama-2-7b-q2k", models.ModelStateReady)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pullReq := httptest.NewRequest(http.MethodPost, "/v1/models:pull", strings.NewReader(`{"name":"llama-2-7b-q2k"}`))
	pullReq = pullReq.WithContext(ctx)
	pullReq.Header.Set("Content-Type", "application/json")
	pullRec := httptest.NewRecorder()
	handler.HandleModelPull(pullRec, pullReq)
	if pullRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", pullRec.Code, pullRec.Body.String())
	}
	var start modelPullStartEnvelope
	if err := json.Unmarshal(pullRec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode pull start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/models:pull/"+start.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleModelPullStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var envelope modelPullStatusEnvelope
		if err := json.Unmarshal(statusRec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		status = envelope.Data.Status
		if status == "succeeded" {
			return
		}
		if status == "failed" {
			t.Fatalf("canceled request context must not fail pull: %s", envelope.Data.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected succeeded pull after canceled request context, got %q", status)
}

func TestHandleModelPull_UpsertWithSpec(t *testing.T) {
	handler := setupModelAPI(t)
	body := `{"name":"llama-2-7b-q2k","repo":"second-state/Llama-2-7B-Chat-GGUF","revision":"064fe43ea8c1e1f93477ef4a170bdc2b244ef02c","registryId":5,"files":["llama-2-7b-chat.Q5_K_M.gguf"],"format":"gguf"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/models:pull", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleModelPull(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	waitModelInspectState(t, handler, "llama-2-7b-q2k", models.ModelStateReady)
}

func TestHandleModelPull_SpecRequiresRepoAndRegistry(t *testing.T) {
	handler := setupModelAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/models:pull", strings.NewReader(`{"name":"tiny-gpt2","files":["config.json"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleModelPull(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "repo and registryId") {
		t.Fatalf("expected spec validation error, got %s", rec.Body.String())
	}
}

func TestHandleModelPull_RequiresName(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/models:pull", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.HandleModelPull(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleModelPrune_InvalidMode(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/models:prune?mode=volumes", nil)
	rec := httptest.NewRecorder()
	handler.HandleModelPrune(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
