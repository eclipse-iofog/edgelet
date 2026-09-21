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

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type deployKnowledgeApplyData struct {
	Accepted bool             `json:"accepted"`
	Kind     string           `json:"kind"`
	Name     string           `json:"name"`
	DryRun   bool             `json:"dryRun"`
	Pulls    []map[string]any `json:"pulls"`
}

type deployKnowledgeApplyEnvelope struct {
	Success bool                     `json:"success"`
	Data    deployKnowledgeApplyData `json:"data"`
}

type knowledgePullStartData struct {
	OperationID string `json:"operationId"`
}

type knowledgePullStartEnvelope struct {
	Data knowledgePullStartData `json:"data"`
}

type knowledgePullStatusData struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

type knowledgePullStatusEnvelope struct {
	Data knowledgePullStatusData `json:"data"`
}

type knowledgeInspectData struct {
	State            string `json:"state"`
	Name             string `json:"name"`
	Source           string `json:"source"`
	Format           string `json:"format"`
	ResolvedRevision string `json:"resolvedRevision"`
	Digest           string `json:"digest"`
	RevisionFloating bool   `json:"revisionFloating"`
	TotalBytes       int64  `json:"totalBytes"`
	UUID             string `json:"uuid"`
	BindRefCount     int    `json:"bindRefCount"`
}

type knowledgeInspectEnvelope struct {
	Data knowledgeInspectData `json:"data"`
}

type knowledgeListData struct {
	Items []map[string]any `json:"items"`
	Count int              `json:"count"`
}

type knowledgeListEnvelope struct {
	Data knowledgeListData `json:"data"`
}

type testKnowledgePuller struct{}

func (s *testKnowledgePuller) Pull(_ context.Context, req modelpull.Request) (*modelpull.Result, error) {
	content := modelpull.ContentDir(req.ModelsRoot, req.Name)
	if err := os.MkdirAll(content, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(content, "guide.jsonl"), []byte("ok"), 0o644); err != nil {
		return nil, err
	}
	manifestPath := modelpull.ManifestPath(req.ModelsRoot, req.Name)
	onDisk := modelpull.OnDiskManifest{
		MetadataName:      req.Name,
		RegistryID:        req.Registry.ID,
		RegistryType:      req.Registry.NormalizedType(),
		Repo:              req.Repo,
		RequestedRevision: req.Revision,
		ResolvedRevision:  "9f3c111122223333444455556666777788889999",
		Digest:            "sha256:abc",
		Files:             req.Files,
		ContentPaths:      []string{"content/guide.jsonl"},
		TotalBytes:        2,
		Format:            req.FormatHint,
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

func setupKnowledgeAPI(t *testing.T) *EdgeletAPIHandler {
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
	km := knowledgemanager.New(db, modelpull.KnowledgeRoot(diskDir))
	km.SetPullers(&testKnowledgePuller{}, &testKnowledgePuller{})
	handler.facade.SetKnowledgeManager(km)
	return handler
}

func validKnowledgeManifest() string {
	return `
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: product-docs
spec:
  repo: acme/product-manuals
  revision: 9f3c111122223333444455556666777788889999
  registry: 5
  files:
    - data/**/*.jsonl
  format: jsonl
`
}

func TestHandleDeployKnowledgeApply_WritesRow(t *testing.T) {
	handler := setupKnowledgeAPI(t)
	req := newManifestMultipartRequest(t, "/v1/deploy/knowledge:apply", validKnowledgeManifest(), nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployKnowledgeApply(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope deployKnowledgeApplyEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Success || !envelope.Data.Accepted || envelope.Data.Kind != "Knowledge" || envelope.Data.Name != "product-docs" {
		t.Fatalf("unexpected apply payload: %#v", envelope.Data)
	}
	if len(envelope.Data.Pulls) != 1 {
		t.Fatalf("expected apply to start a pull, got %#v", envelope.Data.Pulls)
	}
	opID, ok := envelope.Data.Pulls[0]["operationId"].(string)
	if !ok || strings.TrimSpace(opID) == "" {
		t.Fatalf("expected apply to start a pull, got %#v", envelope.Data.Pulls)
	}
	row, err := store.GetInstance().GetLocalKnowledge("product-docs")
	if err != nil {
		t.Fatalf("expected local knowledge row: %v", err)
	}
	if row.RegistryID != 5 || row.Repo != "acme/product-manuals" {
		t.Fatalf("unexpected stored knowledge: %+v", row)
	}
	waitKnowledgeInspectState(t, handler, "product-docs", models.KnowledgeStateReady)
}

func TestHandleKnowledge_ListInspectRemove(t *testing.T) {
	handler := setupKnowledgeAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/knowledge:apply", validKnowledgeManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployKnowledgeApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", applyRec.Code, applyRec.Body.String())
	}
	waitKnowledgeInspectState(t, handler, "product-docs", models.KnowledgeStateReady)

	listReq := httptest.NewRequest(http.MethodGet, "/v1/knowledge", nil)
	listRec := httptest.NewRecorder()
	handler.HandleKnowledge(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", listRec.Code, listRec.Body.String())
	}
	var list knowledgeListEnvelope
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Data.Count != 1 || len(list.Data.Items) != 1 {
		t.Fatalf("expected one knowledge item, got %s", listRec.Body.String())
	}
	if list.Data.Items[0]["name"] != "product-docs" || list.Data.Items[0]["state"] != models.KnowledgeStateReady || list.Data.Items[0]["source"] != models.KnowledgeSourceLocal {
		t.Fatalf("expected name+state+source in list: %s", listRec.Body.String())
	}

	inspectReq := httptest.NewRequest(http.MethodGet, "/v1/knowledge/product-docs", nil)
	inspectRec := httptest.NewRecorder()
	handler.HandleKnowledge(inspectRec, inspectReq)
	if inspectRec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", inspectRec.Code, inspectRec.Body.String())
	}
	var inspect knowledgeInspectEnvelope
	if err := json.Unmarshal(inspectRec.Body.Bytes(), &inspect); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if inspect.Data.Name != "product-docs" || inspect.Data.Source != models.KnowledgeSourceLocal || inspect.Data.State != models.KnowledgeStateReady {
		t.Fatalf("expected inspect identity fields, got %#v", inspect.Data)
	}
	if inspect.Data.Format != "jsonl" || inspect.Data.ResolvedRevision == "" || inspect.Data.Digest == "" {
		t.Fatalf("expected inspect format/revision/digest, got %#v", inspect.Data)
	}

	rmReq := httptest.NewRequest(http.MethodDelete, "/v1/knowledge/product-docs", nil)
	rmRec := httptest.NewRecorder()
	handler.HandleKnowledge(rmRec, rmReq)
	if rmRec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rmRec.Code, rmRec.Body.String())
	}
	if _, err := store.GetInstance().GetLocalKnowledge("product-docs"); err == nil {
		t.Fatal("expected knowledge row gone after remove")
	}
	if _, err := os.Stat(modelpull.ModelDir(modelpull.KnowledgeRoot(config.GetInstance().DiskDirectory), "product-docs")); !os.IsNotExist(err) {
		t.Fatalf("expected knowledge tree gone after remove, err=%v", err)
	}

	missing := httptest.NewRequest(http.MethodGet, "/v1/knowledge/product-docs", nil)
	missingRec := httptest.NewRecorder()
	handler.HandleKnowledge(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after remove, got %d %s", missingRec.Code, missingRec.Body.String())
	}
}

func TestHandleKnowledgePull_AsyncReachesReady(t *testing.T) {
	handler := setupKnowledgeAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/knowledge:apply", validKnowledgeManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployKnowledgeApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply failed: %d %s", applyRec.Code, applyRec.Body.String())
	}

	pullReq := httptest.NewRequest(http.MethodPost, "/v1/knowledge:pull", strings.NewReader(`{"name":"product-docs"}`))
	pullReq.Header.Set("Content-Type", "application/json")
	pullRec := httptest.NewRecorder()
	handler.HandleKnowledgePull(pullRec, pullReq)
	if pullRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", pullRec.Code, pullRec.Body.String())
	}
	var start knowledgePullStartEnvelope
	if err := json.Unmarshal(pullRec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode pull start: %v", err)
	}
	if strings.TrimSpace(start.Data.OperationID) == "" {
		t.Fatalf("expected operationId, got %s", pullRec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/knowledge:pull/"+start.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleKnowledgePullStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("status: %d %s", statusRec.Code, statusRec.Body.String())
		}
		var envelope knowledgePullStatusEnvelope
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
	waitKnowledgeInspectState(t, handler, "product-docs", models.KnowledgeStateReady)
}

func TestHandleDeployKnowledgeApply_DryRunDoesNotPersistOrPull(t *testing.T) {
	handler := setupKnowledgeAPI(t)
	req := newManifestMultipartRequest(t, "/v1/deploy/knowledge:apply", validKnowledgeManifest(), map[string]string{"dryRun": "true"})
	rec := httptest.NewRecorder()
	handler.HandleDeployKnowledgeApply(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope deployKnowledgeApplyEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Data.DryRun || len(envelope.Data.Pulls) != 0 {
		t.Fatalf("dry-run should not start pulls: %#v", envelope.Data)
	}
	_, err := store.GetInstance().GetLocalKnowledge("product-docs")
	if err == nil {
		t.Fatal("expected no persisted knowledge on dry-run")
	}
}

func TestHandleKnowledgePrune_Dangling(t *testing.T) {
	handler := setupKnowledgeAPI(t)
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/knowledge:apply", validKnowledgeManifest(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployKnowledgeApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", applyRec.Code, applyRec.Body.String())
	}
	waitKnowledgeInspectState(t, handler, "product-docs", models.KnowledgeStateReady)

	pruneReq := httptest.NewRequest(http.MethodPost, "/v1/knowledge:prune?mode=dangling", nil)
	pruneRec := httptest.NewRecorder()
	handler.HandleKnowledgePrune(pruneRec, pruneReq)
	if pruneRec.Code != http.StatusOK {
		t.Fatalf("prune: %d %s", pruneRec.Code, pruneRec.Body.String())
	}
	if _, err := store.GetInstance().GetLocalKnowledge("product-docs"); err == nil {
		t.Fatal("expected unbound local knowledge removed by dangling prune")
	}
}

func TestHandleKnowledgePrune_InvalidMode(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge:prune?mode=volumes", nil)
	rec := httptest.NewRecorder()
	handler.HandleKnowledgePrune(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgePull_RequiresName(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge:pull", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.HandleKnowledgePull(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func waitKnowledgeInspectState(t *testing.T, handler *EdgeletAPIHandler, name, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/"+name, nil)
		rec := httptest.NewRecorder()
		handler.HandleKnowledge(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("inspect %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var inspect knowledgeInspectEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &inspect); err != nil {
			t.Fatalf("decode inspect: %v", err)
		}
		state = inspect.Data.State
		if state == want {
			return
		}
		if state == models.KnowledgeStateFailed && want != models.KnowledgeStateFailed {
			t.Fatalf("knowledge %s failed while waiting for %s", name, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for knowledge %s state %s, last=%q", name, want, state)
}
